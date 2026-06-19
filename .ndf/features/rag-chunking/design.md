# S1+S2 设计 — rag-chunking

## 架构现状（已核实，file:line）
- 生产切块：`biz.go:235` `NewCompatibilitySplitter{Max1000,Min200}` → 内部 `HybridSplitter`（Semantic max=2000 / Rule max=1800，`splitter_adapter.go:128`）。实现 `StrategyAwareSplitter`（`SplitWithStrategy`）。
- ingest 管线：`ingest/pipeline.go:95 process()` parse → split → 转 `domain.KnowledgeChunk{ID,DocumentID,UserID,Content,Tags}`（headers 并入 tags，`pipeline.go:199`）→ tag(LLM summary) → `storeChunksToMySQL` → `store.Upsert`。
- 向量写：`adapter/sqlitevec_store.go:104 Upsert` — **若 `chunk.Vector` 为空则懒 embed `chunk.Content`**。chunks 表列：id,document_id,user_id,content,summary,source_ref,tags。`Search` 返回 `Content`（不含任何 embed-only 文本）。
- embedding：`NewGatewayEmbedder(profile.SalesragEmbed, 2048)`，text-embedding-v4@2048。
- 无 doc_type 列；类别隐含（tags / opinion_track 表 / 代码常量 catProduct/catCase/catFAQ）。无 reindex 端点。

## 核心设计决策（AI 拍板）

### D1. 面包屑注入 = 新增瞬态 `EmbedText` 字段（零 sqlite schema 变更）
- `domain.KnowledgeChunk` += `EmbedText string json:"-"`（瞬态，不持久化）。
- `ingest.SplitChunk` += `EmbedText string`（切块器产出每块的"嵌入文本"=面包屑前缀 + 正文）。
- `pipeline.process` 构造 KnowledgeChunk 时 `EmbedText = sc.EmbedText`。
- `sqlitevec_store.Upsert`：embed `chunk.EmbedText`（非空）否则 `chunk.Content`。
- 收益：**向量含 `# 顶 > ## 节` 面包屑、返回内容保持干净**；chunks 表无需加列、无需迁移现有 .db。老调用方 EmbedText 空 → 行为不变（零回归）。
- DashVector/Memory store 对应路径同样处理（保持一致；dashvector 是回退兼容，memory 是测试）。

### D2. 新 `StructureAwareSplitter`（`ingest/structure_splitter.go`），实现 `StrategyAwareSplitter`
单位：**rune（字符）**，非字节（区别于旧 splitter 的字节 *3）。常量：
- `targetRunes=420`、`maxRunes=620`、`minRunes=120`（小于则并入相邻）、`overlapRunes=60`（仅通用打包档用）。

**Profiler（探测文档结构，选档）** — 基于正则密度打分，按命中度选一档：
- `faq`：`(?m)^\s*\d+\s*[.、）)]\s*\S` 或 `^\s*(问|Q)\s*[:：]` 密度高（≥3 命中且占行比显著）。
- `opinion`：`(?m)^#{2,4}\s` 或 `观点\s*[一二三四五六七八九十\d]+` 多次。
- `case`：`案例\s*[\d一二三...]` 或 `客户(背景|画像|情况)` 多次。
- `generic`（产品/默认）：按 markdown 标题树 + 段落打包。

**各档切法**（每块产出 Content=干净正文，EmbedText=面包屑+正文）：
- `faq`：按"问答对"边界切（一个编号问题 + 其答案，直到下一个问题标记）。单对过长（>maxRunes）再按句切，但不拆问题与首段答案。
- `opinion`：按单条观点切（标题/编号边界），每条一块。
- `case`：按单案例切。
- `generic`：按标题分节 → 节内段落贪心打包到 ~targetRunes、不超 maxRunes，受保护块（``` 代码 / 表格行 `|...|` / `$$...$$` / 图片 `![](...)`）整体不切；相邻小块 <minRunes 合并；通用档加 overlap。

**面包屑**：维护标题栈，每块 EmbedText 前缀 `# 顶 > ## 节 > ### 子\n\n` + Content。无标题时前缀文档名（profiler 可接收 docName）。

**Validator（拒绝退化输出 → 回退）**：若产出退化（块数爆炸 avg <50 rune，或 >40% 块 <minRunes，或只切出 1 块但原文 > maxRunes*2），判定该档不适用 → **fallback 到现有 `CompatibilitySplitter`**（语义/规则兜底），strategy 标 `rule_fallback`，detail 记原因。保证"切块层永不让入库失败 / 永不退化"不变式。

`SplitWithStrategy` 返回 strategy ∈ {`structure_faq`/`structure_opinion`/`structure_case`/`structure_generic`/`rule_fallback`/`no_split`}，归一化时把 structure_* 视作有效策略（扩 `normalizeStrategy`）。

### D3. flag 门控接线（prod 零影响）
- 新 flag 常量 `features.structure_aware_chunking.enabled`（config_dev 开、prod 不配=关）。
- `biz.go` 构造 splitter 处：flag 开 → `NewStructureAwareSplitter(cfg)`，关 → 现状 `NewCompatibilitySplitter`。
- StructureAwareSplitter 内部持有一个 CompatibilitySplitter 实例做 fallback（组合，非继承）。

### D4. 预览端点 `POST /v1/admin/chunker/preview`（注册在用户服务 9091 router.go，admin_token 中间件，flag-gated）
- 与 rag-eval 端点同处（§1.3：admin 9099 无向量卷/无 gateway，检索/ingest 类必须在 9091）。
- body：`{text?:string, document_id?:uint}`（二选一；document_id 时从 COS 下载+解析源文本重切）。
- resp：`{strategy, detail, profile, chunk_count, chunks:[{seq, runes, content_preview, embed_text_preview, headers}]}`。
- 纯切块预览不触发 embed/写库（只读、零副作用）。document_id 模式需下载+解析（复用 pipeline 的 parser + COS 签名逻辑）。

### D5. reindex 路径（重灌）`POST /v1/admin/chunker/reindex`（同 router，admin，flag-gated）
- body：`{document_id:uint}`（单文档）或 `{user_id, document_ids:[]}`（批量灰度）。
- 行为：取 doc → 下载源文本 → DeleteByDocumentID（向量+MySQL chunk）→ 重置 status=PENDING → `pipeline.Submit`（异步走新切块器重切重嵌）。
- **dev only**（flag），prod 走独立 user-gated。提供脚本 `scripts/rag_eval/reindex_dev.sh` 批量灰度调用。

## 关键技术点 / 坑
- rune vs byte：新切块器全程 `[]rune`，仅在产出 Content（string）时转回。
- 保护块不切：代码块 ``` 配对、表格连续 `|` 行、`$$` 公式、图片链接整体保留（借鉴 enhanced_splitter `isInsideCodeBlock`）。
- `[上下文衔接]` 标记：新切块器**不产生**该标记（与 enhanced 一致）；展示侧已有 `domain.StripContextJoinMarker` 兜底剥旧标记。
- embedding 维度 2048 固定，不变。
- 预览/reindex 端点鉴权：复用 rag-eval 的 admin 中间件 + flag gate 模式（读 router.go 现有注册照搬）。
- 父子块本期不做（见 requirement 非目标）；为不堵死后续，EmbedText 设计已与 Content 解耦，未来加 ParentContent 字段即可扩展。

## 影响文件
- `internal/pkg/retrieval/domain/schema.go`（+EmbedText）
- `internal/pkg/retrieval/ingest/splitter.go`（SplitChunk +EmbedText）
- `internal/pkg/retrieval/ingest/structure_splitter.go`（新，核心算法）+ `structure_splitter_test.go`
- `internal/pkg/retrieval/ingest/splitter_adapter.go`（normalizeStrategy 扩 structure_*）
- `internal/pkg/retrieval/ingest/pipeline.go`（EmbedText 透传）
- `internal/pkg/retrieval/adapter/sqlitevec_store.go` + `dashvector_store.go` + `memory_store.go`（Upsert 用 EmbedText）
- `internal/numind/biz/biz.go`（flag 选 splitter）
- 新 controller + router.go 注册（preview + reindex）
- `config_dev.yaml`（flag 开）
- 新建 `internal/numind/biz/rag/flags.go` 或复用现有 flag 常量位置（加 FlagStructureAwareChunking）
