# S3 计划 — rag-chunking

## Task 原子分解（每个 task 完成后系统可编译、可独立验证 → 双 Sonnet review）

### T1 — EmbedText 贯通（面包屑注入基建，零行为变化）
- `domain.KnowledgeChunk` += `EmbedText string json:"-"`（瞬态）。
- `ingest.SplitChunk` += `EmbedText string`。
- `pipeline.process`：构造 KnowledgeChunk 时 `EmbedText: sc.EmbedText`。
- `sqlitevec_store.Upsert` + `dashvector_store` + `memory_store` 的 Upsert/embed 路径：embed `chunk.EmbedText` 非空否则 `chunk.Content`。
- 验收：`go build ./...` + 现有测试全绿；EmbedText 处处为空时行为与现状逐位一致（无回归）。

### T2 — StructureAwareSplitter 核心 + 单测
- 新 `ingest/structure_splitter.go`：profiler（faq/opinion/case/generic）+ 各档切法 + 面包屑 EmbedText + validator + fallback 到 CompatibilitySplitter；实现 `StrategyAwareSplitter`。
- `splitter_adapter.go normalizeStrategy`：structure_* 视为有效策略。
- 新 `ingest/structure_splitter_test.go`：每档一个代表性样本（FAQ 编号问答 / 观点编号 / 案例 / 通用长文）断言：块数合理、单块 rune 在区间、EmbedText 含面包屑且 Content 不含、保护块不被切、退化输入触发 fallback。
- 验收：`go test ./internal/pkg/retrieval/ingest/...` 绿。

### T3 — flag 门控 + biz 接线
- flag 常量 `FlagStructureAwareChunking="features.structure_aware_chunking.enabled"`（放 `biz/rag/` flag 常量处，与现有 FlagUniversalRewriter 同址）。
- `biz.go`：`viper.GetBool(flag)` 开 → `NewStructureAwareSplitter`，关 → `NewCompatibilitySplitter`（现状）。
- `config_dev.yaml`：features.structure_aware_chunking.enabled=true（prod 不配=关）。
- 验收：build 绿；flag 关时构造的 splitter 与现状一致。

### T4 — preview + reindex admin 端点
- 新 controller（`controller/v1/...` 或复用 rag-eval controller 文件）：`ChunkerPreview` + `ChunkerReindex`，flag-gated（同 rag-eval flag 或新 flag）、admin_token。
- `router.go` 注册 `POST /v1/admin/chunker/preview`、`POST /v1/admin/chunker/reindex`（照搬 rag-eval 注册范式）。
- preview 只读零副作用；reindex 走 Delete + Submit 异步重灌。
- 脚本 `scripts/rag_eval/reindex_dev.sh`（批量灰度调用 reindex）。
- 验收：build 绿；端点经隧道 smoke（dev 部署后）。

## S5 验证策略（Rule 10 — 在 S3 定，gate reviewer 审）
- **方式**：后端 + dev harness（非前端，无 UI 改动）。grounding-usefulness LLM-judge A/B，复用 §4 方法。**不**用 Playwright（无前端）；不用 gstack（后端检索质量）。
- **回归保护诚实声明**：核心切块逻辑由 `structure_splitter_test.go` Go 单测做持久回归保护（确定性断言）。grounding-usefulness A/B 是一次性 dev 验收（LLM judge，非持久测试），结论用于判断 +16pp 是否复现——这部分无自动回归，符合"检索质量是统计指标"的性质。
- **关键路径**：
  1. dev 部署新切块器（flag 开）。
  2. 预览端点对 user 348 的真实文档做 preview → 人工核对块聚焦、面包屑正确、无大块/碎块爆炸（定性 + 块数/字数统计）。
  3. reindex user 348 文档（dev）→ rebuild §2.2 harness（prod 问题 from MySQL via SSH + judge via dmxapi v4-flash）→ A/B 旧切块（reindex 前快照 or pilot 对照）vs 新切块，grounding-usefulness。
  4. 目标：复现 ~+16pp（趋势，注意 n 小有 ±8pp judge 噪声，必要时扩样本/多客户）；lookup 桶不回归。
- **探活**：dev rerank 免费档限流会让检索静默返空 → 跑 harness 前先探活、失败重试退避。

## 依赖 / 顺序
T1 → T2 → T3 → T4（T2 依赖 T1 的 SplitChunk.EmbedText；T3 依赖 T2；T4 依赖 T3 的 flag + T1/T2 的切块器）。串行实现，每 task 双 review。

## prod 安全
全程 dev；reindex 端点 flag-gated 默认关；不改 config_prod.yaml；不上 prod（独立 user-gated）。
