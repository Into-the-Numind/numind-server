# S0-S3 — rag-doc2query（RAG 升级项4：桥接情境↔案例，破天花板）

> 第二阶段。依赖项1（在结构感知小块上生成问题）。全程 dev、不上 prod、flag 门控。

## S0 需求 / 依据
phase-1 实测：切块 +28pp（真实增益待 full-content 重测）是唯一有效杠杆；hybrid/hardening 对 348（无产品码、83% 情境教练类）零增益。根因=**抽象鸿沟**：情境提问（"客户老说再考虑怎么办"）与答它的案例（"预算犹豫分期促成"）几乎零字面重叠，切块/BM25/重排都碰不到 → ~60% 天花板。doc2query 是唯一能破它的机制（研究三方收敛 + critique 确认"hybrid/hardening=0 正是 doc2query 该做的正面证据"）。

## 设计（最低 churn，复用已建管线）
- ingest 时给每个 chunk 用 LLM 生成"它能回答哪些问题"（含口语化/情境化表述），追加进 `chunk.EmbedText`（一起被向量化）→ 情境提问与生成问题同构 → 命中。**Content 保持干净，问题只进 EmbedText**（复用项1的 EmbedText/面包屑机制，零新 schema）。
- `ingest/doc2query.go` `Doc2QueryGenerator`：MaybeAugment（flag 开才跑，并发 8，best-effort 单块失败跳过）；generate 走 `aiservice.Chat(profile.SalesragTagging)`（已注册路由 v4-flash，控成本）；**内部 userID=0 + skip 计费 = 公司成本不扣用户**（项4计划）。parseDoc2QueryLines 纯函数解析。
- pipeline.process：tagging 后、向量化前调 `p.doc2query.MaybeAugment(ctx, kChunks)`（构造在 NewIngestionPipeline 内，不改签名）。
- flag `features.doc2query.enabled`（dev 开、prod 不配=OFF）。仅影响新入库/重灌。

## S5 验证策略（用修好的 full-content harness）
- A/B：对 348 内容重新上传两版（无 doc2query=已有 117-120 / 有 doc2query=新上传），**用完整内容 judge**，**重点测情境教练类问题**（83% 那桶），看能否突破 chunking-only 的天花板。
- 回归：parseDoc2QueryLines 单测 + flag-off no-op 单测（零回归）。
- 成本：每块一次 v4-flash，ingest 变慢但 dev 可接受。

## 风险
- ingest 变慢/变贵（每块一次 LLM）→ 并发 8 + best-effort + v4-flash 控；prod flag-off。
- 生成问题质量依赖 prompt → dev 抽样人工核 + A/B 数据说话。
