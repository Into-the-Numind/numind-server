# retrieval-base S3 任务计划（第一批 P0+P1+P2）

> S2 设计：`.ndf/features/retrieval-base/spec.md`。每个 task 完成后按 NDF Rule 6 双 Sonnet reviewer（spec 合规 + 代码质量）并行审查，0 P0/P1 才进下一个。

## 并行 Tier 判定

P1 抽包链对 `salesrag/domain`、`salesrag/service` 有**强顺序依赖 + 共享文件**（domain→port→adapter/ingest/retrieve→salesrag 改 import→biz 接线），属 **Tier 4 串行**，不并行。P2 两个 task（chatbot / agent）文件不相交（`biz/chatbot/*` vs `biz/agent/*`），理论可 Tier 3 并行，但都依赖 P1 完成且需 biz.go 接线协调，**保守串行**。P0 评估 harness 与 P1 物理无关，可与 P1 并行起步（但 T0.1 标注有创始人依赖）。

## 任务清单

### P0 — 评估安全网

| Task | 交付物 | 验收 | 文件 | 依赖 |
|------|--------|------|------|------|
| **T0.1 标注集** | 30-50 条 `query→expected chunk_ids`（JSONL）放 `testdata/` | 创始人复核确认 | `numind-server/testdata/retrieval_eval/*.jsonl`（采样脚本+人工） | 创始人复核（人工依赖） |
| **T0.2 评估 harness** | sqlitevec Recall@k+MRR **+ exact-match golden 模式**（有序 chunk_id 序列存 golden fixture，`reflect.DeepEqual` 比对，唯一能验 I1 逐位一致），`//go:build integration` | 可跑；产出改前基线 Recall/MRR + golden 序列 | 照 `dashvector_eval_internal_test.go` 复刻 + 加 exact-match | T0.1 |

### P1 — 抽底座骨架（Tier 4 串行）

> **迁移策略（Rule 9 原子性）**：每个搬迁 task = MOVE + **同 task 内更新其全部 consumer（含 `biz.go` 对应构造/import 行）**，保证每个 task 完成后**全仓库 `go build` 通过**。biz.go 在 T1.3/T1.4/T1.5/T1.6 被增量更新各自那部分，T1.7 做最终全绿门槛 + 死代码清理 + 测试分拆收口。

| Task | 交付物 | 验收 | 关键文件 |
|------|--------|------|----------|
| **T1.1 domain** | `internal/pkg/retrieval/domain`（迁 schema.go：KnowledgeChunk/DocStatus/KnowledgeDocument），salesrag 改 import | 编译过；chatbot(stream.go+2test)/salesrag 全部改 import 指向新包 | `domain/schema.go` 迁出；`strategy.go` 留 |
| **T1.2 port** | `retrieval/port`（迁 vector_store/parser/tagger + 新增瘦 `QueryRewriter`） | 全仓库编译过；**同步改约 18 个 `salesrag/port` consumer import**（biz.go + chatbot/chatbot.go + stream.go + salesrag/adapter/* + salesrag/service/*）；`strategy_router.go` 留 salesrag | port/*.go + 18 consumers |
| **T1.3 adapter** | `retrieval/adapter`（迁 sqlitevec/dashvector/memory store + embedder 闭包工厂） | 编译过；严格模式行为不变 | adapter/*_store.go；embedder 从 biz.go:166 迁 |
| **T1.4 ingest** | `retrieval/ingest`（迁 pipeline + 5 splitter + tagger，prompt key 参数化）；确认 `ingestion.go` 死代码无生产引用后处置 | 全仓库编译过；入库行为不变 | pipeline.go + 5 splitter + ingestion.go |
| **T1.5 retrieve 核心** | `retrieval/retrieve`：`RetrievalService.Retrieve(query,scope,opts)`（抽 parallelSearch+rerankWithLimit）+ Scope/Options/Result + ErrEmptyScope + AllEnabled 解析(注入 docStore) | 单测覆盖：明确 docIDs/AllEnabled/空 scope 三态；rerank+rewrite 通路 | 新建 + 抽 sales_rag.go:95/260/332 |
| **T1.6 salesrag 改调底座** | RetrieveForResponseV2 主干调底座 Retrieve；strategy/opinion/verdict/prompt 留；opinion 用 PrewrittenQueries 复用改写。**注：opinion 由并行改串行二次 Retrieve = 已知结构变更（非纯搬迁）** | **逐位一致**：用 T0.2 **exact-match 模式**比对同批 query 改前/改后 top-K chunk_id **有序序列完全相同** | salesrag/service/sales_rag.go、salesrag.go |
| **T1.7 接线收口+全绿** | biz.go 最终接线收口 + 删 salesrag 已迁死代码；**spec §7.1 测试随迁分拆完成**（salesrag 留下的测试 import 改 retrieval/domain）；`go test ./...` 绿 + `task lint` 净 | 全仓库编译+测试通过 | biz.go + 测试分拆 |

### P2 — 迁消费方（依赖 P1 完成）

| Task | 交付物 | 验收 | 关键文件 |
|------|--------|------|----------|
| **T2.1 chatbot** | 删裸检索→调底座 Retrieve(RewriteQuery+RerankTopN:6)；grounding(硬约束 system fragment + `[知识N](相关度X%)` + 引用解析)；importance=scoreToImportance；没选 KB→提示去选 | **`BuildChatContextFragments` unit test（持久回归）**：system 硬约束 fragment 存在 / evidence 有 `[知识N](相关度X%)` 前缀 / importance=scoreToImportance 非硬编码 7；eval 指标不退化/提升；dev /qa 手测带 grounding+来源；没选 KB 走提示 | `biz/chatbot/stream.go`(226-243/59-132/121) |
| **T2.2 agent** | tool_kb_search 调底座 Retrieve()→[]chunk(原始片段,去双重 LLM)；空 doc_ids→AllEnabled；factory_platform.go 注入签名改 | dev 手测 kb_search 返回原始片段、空 scope 不再空结果 | `tool_kb_search.go:60` + `factory_platform.go` |

### S5 验证策略（Rule 10 — 独立 task）

**T-S5：本 feature 的验证方式与关键路径**

- **回归/检索质量**：T0.2 评估 harness（Recall@k + MRR）—— P1 后核 salesrag **逐位一致**，P2 后核 chatbot **不退化/提升**。这是持久化回归保护（留在代码库）。
- **编译/单元**：`go test ./...`（每 task 后）+ `task test`（S5 终验，含 race+coverage）+ `task lint`。retrieve 核心三态 scope 单测。
- **前端/端到端**（chatbot grounding + agent kb_search 行为）：用 gstack `/qa` 在 dev 手测（一次性，不产持久回归）。**关键用户路径**：
  1. chatbot 挂载知识库 → 提问 → 回答**基于资料且带 `[N]` 来源标注**；
  2. chatbot **未选知识库** → 提示去选（不静默乱答）；
  3. agent 触发 `kb_search`（不传 doc_ids）→ 返回原始片段、非空；
  4. salesrag 销售会话 → 回答与改造前**观感一致**（逐位一致由 eval 兜底）。
- **理由**：检索质量必须有持久化量化回归（eval harness），因本 feature 触及多通道用户可见回答路径，非一次性 /qa 能保护；salesrag 是收入路径，逐位一致是硬门槛。
- **诚实声明**：gstack /qa 是一次性验证，chatbot/agent 的 UI 层无持久回归，未来改动需重跑 /qa；检索层有 eval harness 持久保护。

## 任务顺序

```
T0.1 ──► T0.2 ─────────────────────────┐(基线)
T1.1 ► T1.2 ► T1.3/T1.4 ► T1.5 ► T1.6 ►(用 T0.2 核逐位一致)► T1.7
                                                   └──► T2.1 ► T2.2 ►(用 T0.2 核不退化)► T-S5 终验
```

P0 与 P1 可并行起步（物理无关）；P1 内部 Tier 4 串行；P2 在 P1 后；T-S5 终验在最后。
