# 知识库统一检索底座（Retrieval Base Unification）— 提案

> 本提案基于一次完整的 RAG 链路审计 + 4 个大厂开源项目对标（Tencent/WeKnora、deepset/Haystack、ByteDance/deer-flow、openai/codex）+ 针对本仓库的多轮技术可行性核实编写。所有"现状"结论均已 `file:line` 验证。
> 目标：把"知识库 + 检索"做成一个领域无关的**通用底座**，让 **SOP / chatbot / salesrag / agent-mode** 四个通道共用同一套检索能力，并把计划中的 RAG 提升一次性做进底座（升级一次，四通道受益）。

---

## §1 方案概述 [客户可见]

### 要解决的问题

同一件事——"在知识库里查资料、把查到的喂给 AI 回答"——目前四个通道各干各的，水平差距悬殊：

- **salesrag（销售智能体）**：查得最专业（会改写问题、多路检索、重排挑最相关、要求 AI 基于资料不得编造）。
- **chatbot（智能体）**：查得最糙——直接拿用户原话去搜、搜到啥塞啥、不挑不排、也不叮嘱 AI 基于资料。**这是用户反馈"chatbot 回答怪怪的"的根因。**
- **agent-mode**：有个 `kb_search` 工具，但它内部先让 AI 生成一遍答案、agent 再基于答案重想一遍，**白白多花一次大模型调用**；且工具不传文档 ID 就检索为空。
- **SOP**：完全不会查知识库。

数据层其实早就统一了（一个向量库、一个 embedding、一套知识库表），但**检索逻辑被物理锁在 salesrag 包里**，别人用不上。结果就是：想做的 RAG 提升（重排、混合检索、grounding、评估）如果每个通道各做一遍，是重复劳动且永远对不齐。

### 解决方案

把"查资料"的能力从 salesrag 里**请出来，做成一个公共的检索底座**，四通道都调它：

1. **一套"查"**：query 改写 → 混合检索 → 重排 → 打分片段，所有通道共用同一条检索管线。
2. **"答"各管各**：检索到资料后怎么组织成回答，各通道保留自己的风格（agent 要原始片段自己推理、chatbot/SOP 要通用 grounding、salesrag 要销售话术+策略+观点）。这是有意为之，不是妥协。
3. **提升做进底座**：重排、混合检索（中文 BM25）、grounding、非对称嵌入、评估体系，全部落在底座层，**升级一次四通道一起变好**。

### 预期效果

- **chatbot**：立刻白嫖到 query 改写 + 重排 + grounding，"回答怪"从根上解决。
- **agent**：拿干净的原始片段，顺手修掉"双重大模型调用"的浪费。
- **SOP**：底座预留接入口（本期不做前端，见边界）。
- **salesrag**：检索切到底座，销售编排瘦成薄适配，行为保持不变。
- **可量化**：建立 RAG 评估，每次改检索都能用数据证明"确实变好了"，而不是凭感觉。

### 本期不做的事（明确边界）

- **SOP 的前端配置入口不做**（在 SOP 编辑器里加"这一步参考哪个知识库"是产品工作量）——本期只在底座**留好接入口**，以后再接。
- **不做 GraphRAG**（WeKnora 有，对当前体量是 overkill）。
- **不单独为 chatbot 加 HyDE**（salesrag 已有，chatbot 优先补更基础的重排/grounding）。
- **不换向量库、不上 ES**（中文 BM25 用 sqlite 自带 FTS5 即可，不引新基础设施）。
- **不动 salesrag 的销售编排逻辑**（strategy / opinion 观点库 / 客户画像 / 销售话术 prompt 全部留作薄适配层，不进底座）。

---

## §2 周期与节奏（内部功能，无外部报价）

按"绞杀者模式"分批，先低风险高收益、后收入路径收口。粗估：

| 阶段 | 内容 | 粗估 |
|------|------|------|
| P0 | 评估 harness（Recall@k / MRR）+ 标注 30-50 题 + 跑出基线 | 1.5 ~ 2.5 天（标注是主要成本） |
| P1 | 抽底座骨架（`port` + `domain` 通用部分 + `Retrieve()→[]chunk` 出口 + ingest 迁移） | 2.5 ~ 4 天（含 `salesrag/domain` 拆包跨包改名） |
| P2 | 迁 chatbot + agent 到底座（验证接口）+ chatbot 补 grounding/重排/query 改写 | 3 ~ 4 天 |
| P3 | RAG 提升落底座：混合检索（FTS5+RRF）、非对称嵌入（text_type） | 3 ~ 5 天（FTS5 全量重编译 + 关键词索引回填） |
| P4 | SOP 留接口（不做前端） | 0.5 ~ 1 天 |
| P5 | salesrag 收口：检索切底座，销售层瘦成薄适配，**逐位行为对齐** | 2 ~ 3 天（收入路径，靠 P0 评估当安全网） |

> P3 的"缩小 chunk + 标题面包屑入向量"需要全量重嵌（成本依赖 prod chunk 数，见 §9），可视评估数据决定是否纳入本期或单开。

---

## §3 现状与技术可行性 [AI 内部]

### 3.1 数据面已统一（最难的地基已就位）

`internal/numind/biz/biz.go` 中，全局只创建**一次** `vStore`（向量库）+ 一个 `embedder` 闭包（`profile.SalesragEmbed`，固定 2048 维），同时喂给 ingestion pipeline、salesRAGService、chatbotService、agent 的 PlatformToolFactory。四通道共享：同一个向量库实例、同一个 embedder、同一套 `knowledge_document`/`knowledge_chunk` + KB 表、同一个 scope 原语 `SearchFilter{UserID, DocumentIDs}`。

### 3.2 四通道检索现状（含 scope 严格模式——已核实更正）

**关键纠正**：底层向量库是**严格模式**——`adapter/sqlitevec_store.go:242` 有 `if len(filter.DocumentIDs) == 0 { return nil, nil }`，即**不给明确文档范围就检索为空，根本没有"默认翻全部"**。

| 通道 | 检索范围从哪来 | 入口 | "答"的形态 |
|------|---------------|------|-----------|
| **salesrag** | 会话配置的分类文档（产品/案例/FAQ/观点库），存在 `session.ProductDocIDs/CaseDocIDs/FAQDocIDs/OpinionDocIDs/OpinionTrackIDs`（`salesrag.go:2003-2018`，观点库走独立通道） | `ChatWithSession` | 检索→销售 prompt→流式答案 |
| **chatbot** | 挂载的知识库 → docIDs（`ListMountedKBs`→`ListDocumentIDsByKBs`） | `stream.go:231` 直连 `vectorStore.Search`（无改写/重排/grounding） | 检索→裸 user 消息→答案 |
| **agent** | 工具调用传的 `doc_ids`（不传→检索空；`Retrieve` 的 ListByUser 白名单只用于**校验**传入 docIDs，非检索范围，`salesrag.go:604-615`） | `tool_kb_search.go:60` → `salesrag.Retrieve` | 工具内部先生成答案，返回整个 `RetrievalVerdict`（含 Answer）→ agent 再推理（双重 LLM） |
| **SOP** | 未接入（无 vectorStore/embedder/检索 import） | — | prompt 有 `RoleEvidence/SourceFile` 槽，现仅放上传附件 |

### 3.3 通用检索核心可抽取性：高

检索主干（query 改写 → 多 query 并行检索 → 去重 → 重排 → 阈值）**已经领域无关**，销售耦合只集中在边缘且都已是"可选"：

- `SalesRAGService` 结构体仅 3 个字段（`store`/`router`/`strategySvc`）；strategy 是可空注入字段（`if chatMode=="sales" && strategySvc!=nil`）。
- opinion 观点库本质就是"在第二个 scope 上跑同一套通用检索"，检索算法里无销售逻辑。
- 客户画像 / 销售阶段 / 销售话术**根本不在 service 层**，全在上层 biz 的 prompt 拼装里。
- `parallelSearch`（`sales_rag.go:260`）、`rerankWithLimit`（`sales_rag.go:332`，billing label 已是参数）完全通用。
- 入库链 `pipeline.go`（parse→split→tag→embed→双写）已领域无关；tagger 只有一个 Langfuse prompt key 带销售味，可参数化。
- port 抽象 70% 就位：`vector_store.go` / `parser.go` / `tagger.go` 已是干净的领域无关抽象。

**最大成本不在解耦逻辑，而在 `salesrag/domain` 拆包**：通用的 `KnowledgeChunk`/`KnowledgeDocument` 和纯销售的 `BasicStrategy`/`MetaStrategy` 现在同包，迁出通用类型会触发 chatbot/agent/knowledgebase 三处 import 的连锁改名——纯机械、编译器引导，但面广。

### 3.4 可行性核实结论

| 项 | 判定 | 证据 / 关键事实 |
|---|---|---|
| **复用 rerank** | ✅ 干净 | `aiservice.Rerank` 是包级全局入口（`ai.go:37`），任何通道可直接调，无需碰 SalesRAGService。阈值/保底逻辑约 30 行 |
| **复用 query 改写** | ✅ 零耦合 | `LLMRouter` 是零字段 struct、`NewLLMRouter()` 无参、不依赖 DB，只调 `aiservice.Chat`；已做指代消解 + 5 轮历史压缩。⚠️ prompt 是销售定制，chatbot 需配中性 prompt |
| **非对称嵌入 text_type** | ✅ 官方支持 | DashScope text-embedding-v4 原生 API 支持 `parameters.text_type`（query/document，默认 document），**仅原生 API 支持而我们正走原生路径**（`ali.go:181` 切 `/api/v1`）。历史向量都是 document，只需把**检索侧切到 query**，**免重灌库** |
| **混合检索（BM25）** | ⚠️ 可行但有坑 | sqlite 驱动是 mattn/go-sqlite3 (CGO)，**当前未启用 FTS5**（无 `sqlite_fts5` build tag）。需给所有 build 入口（`Dockerfile:44` + Taskfile 三处）加 `-tags sqlite_fts5`（纯 build flag 零代码，触发一次全量 CGO 重编译，**需运行时验证一次**）。中文用 gojieba 预切词存 FTS5（项目已有）。RRF 融合约 30 行 |
| **chunk.Score** | ✅ 现成 | `sqlitevec_store.go:369` `Score = 1 - cosine distance`（0-1）。注意 rerank 会覆盖为 rerank 分 |
| **评估 harness** | ✅ 有模板 | `dashvector_eval_internal_test.go` 已有 Recall@k+MRR+延迟框架（`//go:build integration`），但 fixture 是失效绝对路径，需迁到新建 `testdata/` 并改连 sqlite-vec |
| **scoreToImportance / grounding builder** | 复制 / 不复用 | `scoreToImportance` 是包内私有 12 行纯函数，chatbot 复制即可；salesrag 的 grounding builder 深耦合 `*salesRAGBiz`+销售结构，**chatbot 应走自己的 ContextFragment 路径**，不复用 |

---

## §4 目标架构 [AI 内部]

### 4.1 分层草图（基于真实代码）

```
底座层  internal/pkg/retrieval/        (领域无关，四通道共享)
  domain/    ← 从 salesrag/domain 迁出 KnowledgeChunk / KnowledgeDocument / SearchFilter
               (strategy.go 不迁，留 salesrag)
  port/      ← 搬 vector_store / parser / tagger
               + 新增瘦 QueryRewriter{ Rewrite(query, history) → {queries, hyde} }
  ingest/    ← 搬 pipeline.go 全套（parse→split→tag→embed→双写 MySQL+向量库）
  retrieve/  ← 抽 parallelSearch + rerankWithLimit；统一出口：
               Retrieve(ctx, query, scope, opts) → RetrievalResult{ Chunks[], RewriteQueries[], Extra }
               (opinion = 多传一个 scope；strategy = 可选 PostRetrievalHook)
  adapter/   ← sqlitevec / dashvector / memory store + embedder 闭包 + (新)FTS5 关键词索引
─────────────────────────────────────────
salesrag 适配层（薄）← 销售 prompt、strategy hook、opinion scope、客户画像/阶段、ChatWithSession+credits
chatbot 适配层       ← 删 stream.go:226-243 裸检索，改调底座（白嫖检索能力）+ 自己的 grounding fragment
agent 适配层         ← kb_search 改调底座 Retrieve()→[]chunk（修双重 LLM）
SOP 适配层           ← 本期只留接入口（检索结果进 RoleEvidence fragment），前端不做
```

### 4.2 核心接口（草案）

```go
// 检索：领域无关，"查"统一
type RetrievalService interface {
    Retrieve(ctx context.Context, query string, scope Scope, opts Options) (*RetrievalResult, error)
}

type Scope struct {
    UserID      uint
    DocumentIDs []uint   // 明确范围；空 = 由 opts.AllowAll 决定，默认不允许（严格）
    Filters     map[string]any
}
type Options struct {
    TopK         int      // 召回放大（如 20~30）
    RerankTopN   int      // 重排后保留
    Rerank       bool
    RewriteQuery bool     // 是否做 query 改写/指代消解
    Hybrid       bool     // 是否混合检索（向量+BM25）
    BillingLabel string   // 计费归因
    // ...
}
type RetrievalResult struct {
    Chunks         []domain.KnowledgeChunk  // 已打分、已重排、可选已加 grounding 标注
    RewriteQueries []string
    Extra          map[string]any           // strategy 等通过 PostRetrievalHook 注入
}
```

**"答"不进底座**：底座只返回打分片段（+可选通用 grounding context），各通道用自己的方式拼 prompt、调 LLM。agent 直接拿 `Chunks`；chatbot/SOP 套通用 grounding 模板；salesrag 在结果上套销售编排。

---

## §5 检索能力提升（全部落底座，升级一次四通道受益）

来自大厂对标（WeKnora/Haystack），每项标注来源参数 + 难度 + 是否需重建索引：

| 提升 | 具体做法（OSS 背书 + 参数） | 难度 | 重建索引? |
|------|----------------------------|------|-----------|
| **重排 + 阈值 + 降级兜底** | 召回放大（top-20~30）→ `aiservice.Rerank`（qwen3-rerank 现成）→ 阈值过滤 → 留 top-N；空结果阈值×0.7、保 top-1（抄 WeKnora `rerank.go`） | 低 | 否 |
| **Grounding + 来源标注** | 片段包成 `[知识N](相关度X%)` + 硬约束"仅依据资料、不得编造、资料不足须声明"（抄 WeKnora system_prompt）+ 正则 `\[(\d+)\]` 解析引用回填出处（抄 Haystack AnswerBuilder） | 低 | 否 |
| **多轮 query 改写** | 复用 `LLMRouter`（指代消解+多 query+HyDE）；chatbot 配中性 prompt + 自己的 billing label | 低-中 | 否 |
| **importance 按 score** | chatbot 复制 `scoreToImportance`，替换硬编码 7 | 极低 | 否 |
| **非对称嵌入 text_type** | `EmbedRequest` 加 `TextType` → ali adapter 透传 `parameters.text_type` → embedder 闭包签名带 textType，入库 document / 检索 query | 中 | 否（只切检索侧） |
| **混合检索（中文 BM25 + RRF）** | sqlite FTS5 + gojieba 预切词建关键词索引；向量 + BM25 并行 → RRF 融合（`score += w/(60+rank)`，向量 0.7/关键词 0.3，抄 WeKnora） | 中 | 否（回填关键词索引，不重嵌） |
| **缩小 chunk + 标题面包屑入向量 + 溯源 meta** | chunk 6000→~512 字符；标题面包屑 `# 章 > ## 节` 嵌入前拼正文（不持久化，抄 WeKnora ContextHeader）；写 source_id/split_id；修死配置（`biz.go:234` 的 1000/200 被 `splitter_adapter.go:54` 忽略） | 中-高 | **是（全量重嵌）** |
| **评估 harness** | 确定性 Recall@k/MRR（标 30-50 题）做回归 + LLM-judge faithfulness/context-relevance 量化幻觉（抄 Haystack evaluator prompt） | 中 | 否 |

---

## §6 关键设计决策

### 决策 1 —— scope 模型（已核实更正版）

底层本来就是"严格模式"（不给范围=检索空），顺势统一为：**底座契约 = 必须有明确 scope；"全部启用文档"作为显式可选项，绝不"没选就静默翻全部"**（静默翻全部=精度杀手，正是要避免的）。

- **chatbot / salesrag**（人在操作）：必须先选（现状本就如此，无需改）。
- **agent**（自主）：默认可用"全部启用文档"这个**显式范围**，也允许缩小到具体知识库。
- **已定（2026-06-10）**：salesrag/chatbot 在"用户一个都没选"时**提示用户去选知识库**——不静默翻全部、不给默认 KB、不返回空白答案。

### 决策 2 —— SOP 本期只留接口，不做前端配置。

### 决策 3 —— 节奏：P0 评估 + P1 骨架 + P2 迁 chatbot/agent 先行；salesrag 收口（P5）放最后单独做（收入路径，最稳）。

### 架构决策（已拍板）—— 复用全局入口 + 各通道保留自己的"答"，**不合并四条路径**。

---

## §7 分步落地（绞杀者模式 6 步）

1. **P0**：先建评估 harness（重构 salesrag 的安全网，必须先有）+ 跑出改前基线。
2. **P1**：抽骨架——提取 `port` + `domain` 通用部分 + `Retrieve()→[]chunk` 出口 + ingest 迁移，**先不动 salesrag 的 strategy/opinion/prompt**。
3. **P2**：迁 chatbot + agent 到底座（最该改、风险最低），chatbot 补 grounding/重排/query 改写，agent 修双重 LLM。用评估验证。
4. **P3**：把 RAG 提升逐项落底座（混合检索 / 非对称嵌入 /（可选）小块重嵌）——四通道同时升级。
5. **P4**：SOP 留接入口（前端本期不做）。
6. **P5**：salesrag 收口——检索切底座，销售层瘦成薄适配，逐位行为对齐。

---

## §8 风险与缓解

| 风险 | 缓解 |
|------|------|
| **salesrag 是收入路径，重构后行为可能漂移** | P0 评估 harness 当安全网；P5 放最后单独做；改前/改后跑评估逐位对齐 |
| **`salesrag/domain` 拆包触发跨包改名（chatbot/agent/knowledgebase）** | 纯机械、编译器引导；第一刀只迁通用类型，分 commit 小步走 |
| **FTS5 漏加 build tag 任一入口 → prod panic** | 统一改 Dockerfile + Taskfile 三处；上线前运行时验证 `CREATE VIRTUAL TABLE ... USING fts5` |
| **query 改写 + 重排各加 1 次调用 → 延迟/积分上升** | 抄 WeKnora：意图分类对闲聊跳过检索；改写可与流式"思考中"并行；dev 实测延迟 |
| **小块重嵌成本未知** | P3 前先跑 `COUNT(*) knowledge_chunk` 估算；可拆出本期之外 |
| **agent 工具契约变更（RetrievalVerdict→原始 chunk）** | 核对 agent prompt 对工具返回的预期；返回结构保持向后兼容字段 |

---

## §9 待确认 / 独立发现

1. ~~salesrag/chatbot"一个都没选"时的默认行为~~ → **已定（2026-06-10）：提示用户去选知识库**，不静默翻全部、不给默认 KB。
2. **agent `kb_search` 空结果隐患**（待验证）：工具不传 `doc_ids` 就检索空，而 agent 通常不知道数字 doc ID——可能经常返回空。需验证 agent 是否被喂了可选文档列表；底座迁移时建议给 agent 一个"全部启用文档"的显式默认 scope。
3. **独立 bug（顺手发现，未改）**：`service/strategy_service.go:43` 硬编码绝对路径 `/Users/zhiyuchen/Desktop/莫小派/Codes/基础策略`（与项目实际 `Documents/...` 路径不符），prod/dev 大概率不存在 → `BasicStrategy.Content` 静默为空、销售策略注入失效。→ **已定（2026-06-10）：单独排查，独立于本提案。**

---

## §10 评估策略（贯穿全程的安全网）

- **第一层（零成本，先上）**：标注 30-50 条 `query → 应召回 chunk_id`（放 `internal/numind/biz/salesrag/testdata/` 或底座包），Go 实现 Recall@k + MRR（各 ~30 行），照 `dashvector_eval_internal_test.go` 复刻。每次改检索跑一遍做回归。
- **第二层（LLM-judge）**：抄 Haystack 的 faithfulness + context-relevance prompt，用便宜模型（qwen-plus/deepseek）量化幻觉率与检索相关性。
- **用途**：既是底座改进的"改前/改后 A/B"，也是 salesrag 收口（P5）的逐位行为对齐依据。

---

*文档状态：S0/S1 输入草案（待评审）。下一步：评审通过后按 NDF standard 轨道起首个 feature = P0 评估 + P1 骨架 + P2 迁 chatbot/agent。*
