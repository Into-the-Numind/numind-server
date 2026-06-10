# retrieval-base S2 技术设计 spec（第一批 P0+P1+P2）

> S0 需求卡：`requirements/retrieval-base.md`｜S1 提案：`proposals/retrieval-base-unification-proposal.md`
> 本 spec 仅覆盖第一批（P0 评估 + P1 抽骨架 + P2 迁 chatbot/agent）。**不含** FTS5 混合检索、text_type 非对称嵌入、小块重嵌、SOP 接入（均为后续独立 feature）。

---

## §1 设计目标与不变量

- 抽一个领域无关的 `internal/pkg/retrieval` 包，承载"查"（query 改写→多 query 检索→去重→重排→打分片段）+ 入库（parse→split→tag→embed→双写）。
- **不变量 I1**：P1 是纯搬迁，salesrag 检索行为**逐位一致**（同 query → 同 top-K chunk_id）。
- **不变量 I2**：底座**不含"答"**——只返回打分片段（+可选 grounding context），各通道自拼 prompt 调 LLM。
- **不变量 I3**：底座**不依赖任何销售概念**（strategy/opinion/客户画像/销售阶段一律留 salesrag 适配层）。
- **不变量 I4**：所有 AI 调用仍走 `aiservice` 统一入口（embed/rerank/chat），Langfuse + billing 不变；billing label 参数化。

---

## §2 跨包影响范围（已 grep 核实，比提案估计小）

`grep -rl "numind/biz/salesrag/domain"` 在 salesrag 包**外**的 import 仅：
- `internal/numind/biz/chatbot/stream.go`（+ `chatbot_test.go`、`prefix_stability_test.go`）

`salesrag/port` 包外 import：`biz.go`、`chatbot/chatbot.go`、`chatbot/stream.go`。
`salesrag/service` 包外 import：`agent/tool_kb_search.go`（+test）、`biz.go`。

**结论**：domain 拆包的跨包改名实际只牵动 **chatbot（2 prod + 2 test）**；agent 走 service（迁 Retrieve 调用点）；`biz.go` 是接线（它 import salesrag 的 adapter/port/seed/service 四子包，P1 后需重接线）。**`biz/knowledgebase` 包存在但只 import 顶层 `salesrag`(Ingest)、不 import domain/port/service 子包**，故 domain 拆包不牵动它（提案"三处"为高估）。P1 风险显著低于提案预期。

---

## §3 包结构

```
internal/pkg/retrieval/
  domain/        KnowledgeChunk / DocStatus / KnowledgeDocument / SearchFilter
  port/          VectorStore / DocumentParser / ContentTagger / QueryRewriter(新, 瘦)
  ingest/        IngestionPipeline (parse→split→tag→embed→双写)
  retrieve/      RetrievalService.Retrieve(query, scope, opts) → RetrievalResult
  adapter/       sqlitevec / dashvector / memory store + embedder 闭包工厂
```

salesrag 包保留：`domain/strategy.go`(MetaStrategy/BasicStrategy)、`port/strategy_router.go`、`adapter/{llm_router,regex_router,strategy_router}.go`、`service/{strategy_*,sales_rag(销售编排壳)}.go`、`salesrag.go`(销售 prompt + ChatWithSession)。

---

## §4 domain 拆分（精确）

| 源 | 类型 | 去向 |
|----|------|------|
| `salesrag/domain/schema.go:9` | `KnowledgeChunk` | → `retrieval/domain` |
| `salesrag/domain/schema.go:37` | `DocStatus` + 常量 | → `retrieval/domain` |
| `salesrag/domain/schema.go:50` | `KnowledgeDocument` | → `retrieval/domain` |
| `salesrag/domain/strategy.go:5` | `MetaStrategy` | **留 salesrag** |
| `salesrag/domain/strategy.go:16` | `BasicStrategy` | **留 salesrag** |

迁移手法：`schema.go` 整文件移到 `retrieval/domain`，salesrag 侧改 import。salesrag 的 strategy 代码改为 import `retrieval/domain`（取 KnowledgeChunk）+ 本包 strategy 类型。编译器引导改名，逐文件验证。

---

## §5 port 设计

- **直接搬**（已领域无关）：`vector_store.go`（`VectorStore` + `SearchFilter{UserID, Tags, DocumentIDs}`）、`parser.go`（`DocumentParser`）、`tagger.go`（`ContentTagger`）→ `retrieval/port`。
- **新增瘦接口** `QueryRewriter`（底座用，剥离销售意图枚举）：
  ```go
  type QueryRewriter interface {
      Rewrite(ctx, query string, history []string) (RewriteResult, error)
  }
  type RewriteResult struct { Queries []string; HyDE string }
  ```
  salesrag 的 `LLMRouter.AnalyzeIntentV2` 通过一个 adapter 实现此瘦接口（丢弃销售专属返回字段）。**chatbot 路径下该 adapter 绑定 `chatMode="free"`**（避免销售话术污染通用问答）；chatbot 专属中性 prompt 作 follow-up。
- **留 salesrag**：`port/strategy_router.go`（依赖 strategy 类型）。

---

## §6 retrieve 核心接口（底座的"查"）

```go
type RetrievalService interface {
    Retrieve(ctx context.Context, query string, scope Scope, opts Options) (*RetrievalResult, error)
}

type Scope struct {
    UserID      uint
    DocumentIDs []uint            // 明确范围
    AllEnabled  bool              // 显式"全部启用文档"（agent 用）；与 DocumentIDs 互斥
}
type Options struct {
    TopK         int     // 召回放大（默认 20~30）
    RerankTopN   int     // 重排后保留（0=不重排）
    RewriteQuery bool    // 是否走 QueryRewriter（多 query + HyDE）
    PrewrittenQueries []string // 已有改写结果时直接复用（opinion 第二 scope 用；非空则跳过 QueryRewriter，保 I1 逐位一致）
    BillingLabel string  // 计费/trace 归因
}
type RetrievalResult struct {
    Chunks         []domain.KnowledgeChunk  // 已打分、已重排
    RewriteQueries []string
}
```

**内部实现**（从 `service/sales_rag.go` 抽通用主干）：
1. `RewriteQuery` 开 → `QueryRewriter.Rewrite` 出多 query + HyDE（复用 `parallelSearch` 多 query 去重，`sales_rag.go:260`）。
2. 按 `scope` 解析 docIDs → `VectorStore.Search`（严格模式：空 docIDs 返回空）。
3. 去重合并（`seenIDs`）。
4. `RerankTopN>0` → 复用 `rerankWithLimit`（`sales_rag.go:332`，阈值 + 保底 + label 参数化）。

**scope 规则（决策 1 落地）**：
- 底座只认明确范围：`DocumentIDs` 非空，或 `AllEnabled=true`（解析为该 user 所有 `IsEnabled && Status==COMPLETED` 文档）。
- 两者皆空 → 返回**类型化错误** `ErrEmptyScope`（不静默翻全部）。
- **chatbot/salesrag** 调用方：上层校验"用户没选" → 走各自的"提示去选"分支（不调底座或调到 ErrEmptyScope 转提示）。
- **agent**：`kb_search` 不传 doc_ids → 适配层置 `AllEnabled=true`（修空结果隐患）。

salesrag 的 **opinion 通道** = 在第二个 scope 上再调一次 `Retrieve`（不同 docIDs），无销售语义进底座。**strategy** = salesrag 在 service 层并行跑（保留在 salesrag），不进底座 Retrieve。

---

## §7 ingest 迁移

`service/pipeline.go`（`IngestionPipeline`：parse→split→tag→embed→双写 MySQL+向量库）整体搬到 `retrieval/ingest`。**splitter 完整随迁 5 文件**：`splitter.go`、`enhanced_splitter.go`、`hybrid_splitter.go`、`splitter_adapter.go`（prod 用的 `NewCompatibilitySplitter` 依赖链）、`embedding_splitter.go`（`salesrag.go:3387` 直接引用）。tagger 的 Langfuse prompt key（`salesrag-tagging`）改为构造参数（默认值不变，行为不变）。`ingestion.go`（旧同步版，死代码）确认无生产引用后随迁或弃。

### §7.1 测试随迁策略
- **随底座迁**（测通用检索/入库）：`pipeline_test.go`、`ingestion_test.go`、`splitter_test.go`/`enhanced_splitter_test.go`/`hybrid_splitter_test.go`、`sales_rag_test.go` 中的通用检索用例、`sqlitevec_store_test.go`/`memory_store_test.go`。
- **留 salesrag**（测销售逻辑）：`strategy_*_test.go`、`tagger_dmx_test.go`、`salesrag_credits_integration_test.go`；更新其 import 指向 `retrieval/domain`。

---

## §8 adapter 迁移

- store 实现（`sqlitevec/dashvector/memory`，含其 `Search` 严格模式 `sqlitevec_store.go:242`）搬到 `retrieval/adapter`。
- **embedder 闭包**（现 `biz.go:166-178`，`aiservice.Embed(profile.SalesragEmbed, dim 2048)`）→ 底座提供构造工厂；P1 维持现签名 `func(ctx, text)`（text_type 非对称是后续 feature，不在本批）。
- **RetrievalService 构造签名**：`NewRetrievalService(vStore, rewriter, docStore)`——`Scope.AllEnabled=true` 解析"全部启用文档"需注入 `store.KnowledgeDocumentStore`（查 `IsEnabled && Status==COMPLETED`）。
- `biz.go` 接线：构造 `retrieval` 的 store+embedder+RetrievalService+IngestionPipeline，注入 salesRAGService（壳）、chatbotService、agent factory（`factory_platform.go` 签名同步改，见 §9.3）。

---

## §9 各通道适配改动（P2）

### 9.1 biz.go 接线
构造底座 RetrievalService 一次，注入三处消费方。salesRAGService 变为"销售编排壳 + 底座 Retrieve"。

### 9.2 chatbot（`biz/chatbot/stream.go`）
- 删 `stream.go:226-243` 裸 `vectorStore.Search` → 调 `retrievalSvc.Retrieve(query, scope, Options{RewriteQuery:true, RerankTopN:6, BillingLabel:"chatbot_retrieval"})`。**白嫖 query 改写 + 重排**。
- scope：挂载 KB→docIDs（现有 `ListMountedKBs`→`ListDocumentIDsByKBs`）；为空 → 走"提示去选"（不再静默空检索）。
- grounding（chatbot 自己的 fragment 路径，不复用 salesrag builder）：
  - evidence fragment content 前缀 `[知识N] (相关度:X%)`；
  - 新增/改写 system fragment 注入硬约束（"仅依据资料、不得编造、资料不足须声明"）；
  - `importance` 用 `scoreToImportance(chunk.Score)`（复制 salesrag 12 行私有函数），替换 `stream.go:121` 硬编码 7。
- 流式回答后：正则 `\[(\d+)\]` 解析引用 → 回填来源（front-end 显示出处）。
- query 改写 prompt：本批先复用 LLMRouter 机制，chatbot 专属中性 prompt 作为 follow-up（避免销售话术污染通用问答，记 §11 风险）。

### 9.3 agent（`biz/agent/tool_kb_search.go`）
- `tool_kb_search.go:60` `t.rag.Retrieve(...)`（返回整个 `RetrievalVerdict` 含 Answer，双重 LLM）→ 改调底座 `Retrieve()→[]chunk`，marshal **原始片段**（含 content + source + score）给 agent。
- 空 `doc_ids` → `Scope{AllEnabled:true}`（修空结果隐患）。
- 工具返回结构保留向后兼容字段（核对 agent prompt 对返回的预期）。
- **`biz/agent/factory_platform.go` 同步改造**：`kbSearchTool` 现注入 `salesrag.SalesRAGBiz`，P2 改为注入底座 `retrieval.RetrievalService`，构造函数签名随之变更。

### 9.4 salesrag（P1 不动语义，仅换底座）
- `RetrieveForResponseV2` 的通用主干改调底座 `Retrieve`；strategy 分支、opinion 第二 scope、`RetrievalVerdict` 组装、销售 prompt（`salesrag.go:1172-1660`）、`ChatWithSession` 全部**留在 salesrag**。
- **opinion query 共享（保 I1）**：现状是一次 `AnalyzeIntentV2` 的 queries 同时喂主通道 + opinion。迁移后 salesrag 先调 `Retrieve(主 scope, RewriteQuery:true)` 取回 `RetrievalResult.RewriteQueries`，再调 `Retrieve(opinion scope, RewriteQuery:false, PrewrittenQueries: 上一步结果)`——**两通道复用同一组改写 query，不触发第二次 intent analysis**，与改前逐位等价。
- **验收 = 逐位一致**（I1）：用 P0 评估 fixture 对同一批 query 比对改前/改后 top-K chunk_id 完全相同。

---

## §10 P0 评估 harness 设计

- 落点：`retrieval`（或 salesrag）包内 `*_recall_eval_internal_test.go`，`//go:build integration`，照 `dashvector_eval_internal_test.go` 复刻（已有 Recall@k+MRR+延迟逻辑），改连 `adapter.NewSQLiteVecStore`。
- fixture：`testdata/`（新建）放 30-50 条 `query → expected chunk_ids`（JSONL/TSV）。**取代**原硬编码失效绝对路径。
- 标注数据：从 salesrag 会话 / agent_run 历史采样真实 query → AI 出 expected chunk_ids 初稿 → 创始人复核（关键人工依赖）。
- 指标：Recall@k、MRR（确定性）**+ exact-match golden 模式**：改前 top-K **有序 chunk_id 序列**存 golden fixture，改后 `reflect.DeepEqual` 比对——验证 I1"逐位一致"的唯一可靠手段（Recall@k 只验 document 命中、验不了有序 chunk 全集相等）。LLM-judge faithfulness/context-relevance 为可选第二层。
- 用途：① P1 后核 salesrag 逐位一致；② P2 后核 chatbot 指标提升不退化。

---

## §11 风险与设计权衡

| 风险 | 缓解 |
|------|------|
| salesrag 逐位一致被破坏（收入路径） | P0 评估 fixture 精确比对；P1 不改算法只搬迁；salesrag 收口（语义瘦身）推迟到 P5 单独 feature |
| domain 拆包跨包改名 | 实际仅 chatbot（已核实）；编译器引导，逐文件 commit |
| query 改写销售 prompt 污染 chatbot 通用问答 | 本批复用机制，chatbot 中性 prompt 作 follow-up；或本批即给 chatbot 独立 Langfuse prompt key |
| 增加 rewrite+rerank 调用 → 延迟/积分 | 意图分类对闲聊跳过检索；dev 实测延迟；rerank 便宜 |
| agent 工具返回结构变更 | 保留向后兼容字段；核对 agent prompt 预期 |

---

## §12 关键文件清单（落点）

- 抽出源：`biz/salesrag/service/sales_rag.go`（parallelSearch:260 / rerankWithLimit:332 / RetrieveForResponseV2:95）、`service/pipeline.go`、`adapter/*_store.go`、`domain/schema.go`、`port/{vector_store,parser,tagger}.go`、`biz.go:166-244`（embedder+wiring）。
- 改造点：`biz/chatbot/stream.go`（226-243 检索 + 59-132 fragment + 121 importance）、`biz/agent/tool_kb_search.go:60` + `biz/agent/factory_platform.go`（注入签名）、`biz.go` 接线。
- 新建：`internal/pkg/retrieval/**`、`testdata/` + `*_recall_eval_internal_test.go`。
- 不动（本批）：salesrag 销售 prompt/strategy/opinion/ChatWithSession、FTS5、text_type、SOP。
