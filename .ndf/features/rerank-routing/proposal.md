# 提案 + PRD：rerank-routing

> S1 工件 · feature id: rerank-routing · 2026-06-11

## 1. 产品级思考（office-hours 视角）

**真问题**：不是"修好 rerank"，而是"让 rerank 像主模型一样可运营、可冗余、可扩展"。一次性硬编码切回 bge 只是止血；产品要的是"加供应商=配置一行"的能力，这样以后任何一家 rerank 模型下架/限流，运营自己在 admin 改路由即可，不再需要工程介入。

**为什么现在做**：DMXAPI 刚下架 qwen3-rerank 暴露了脆弱性——rerank 是单点、无冗余、无适配器多样性。这是系统性风险（embed 同样脆弱，本次 T1 顺带修好）。

**最小切口 vs 完整**：完整切口（多级 cascade + 跨家适配器）只比最小切口（单 fallback）多改一处中间件循环，却把"1 个兜底"变成"任意条优先级链"，且顺带修好 embed。性价比高，做完整。

## 2. 技术可行性

- **gateway 每路由自适配器**：`resolveAndRun` 现在 `makeHandler(p, primary)` 捕获 primary 适配器。改为在 handler 调用时按 `route.Provider.Name` 动态解析适配器（复用现有 `g.providers` + `findAdapterByPrefix`）。低风险：chat 现有跨家 fallback 本就依赖 OAI 协议同形，改后变成"真正用对家的适配器"，对 OAI 兼容家行为不变，对异构家（rerank/embed）才有实质修复。
- **ali rerank 适配器**：`ali.go` 已有百炼原生管线（embed 走 `…/api/v1/…` native + `doPost`/`doRawPost`），加 Rerank 顺着 embed 写。百炼 qwen3-rerank flat 结构 `{model, query, documents, top_n, return_documents}`，端点 `…/api/v1/services/rerank/text-rerank/text-rerank`（或兼容 `/compatible-api/v1/reranks`）。实测计费 ¥0.0005/千token。
- **429 retryable**：`wrapHTTPStatusErr` 加 429（+408）→ `ErrAIProviderError`（或新 rate-limit errno），`retryableError` 即认。
- **多级 cascade**：Fallback 中间件把 `fallbacks[0]` 改成遍历 `fallbacks` 全列表，每个失败再试下一个，全失败才 `ErrAIFallbackExhausted`。skip_retry 仍注入避免放大调用数。
- **注册表数据**：`task_profile_service` role=fallback + priority 已是现成机制（与 chat 同）。dev 配：service(dmxapi/bge) 作 default_service_id，新建 service(ali/qwen3-rerank) 绑为 fallback。

风险：T1 改派发核心，影响 chat/embed/ocr/asr 全部能力 → 必须强单测 + reviewer 重点审 + S5 dev 实跑 chat 不回归。

## 3. 工作量估算

5 个 task，约 1 个 session。后端单仓库。

## 4. PRD

### 4.1 功能描述
rerank 任务支持注册表驱动的多供应商优先级路由 + 自动 cascade fallback，与主对话模型配置体验一致。

### 4.2 涉及仓库
numind-server（gateway / adapter / middleware / errno + 测试）。注册表数据经 SQL/admin 配（dev）。

### 4.3 验收标准
- **AC1**：`task_profile_service` 可为 rerank 任务绑定 ≥2 个不同 provider 的 service，按 priority DESC 排序。
- **AC2**：主供应商返回可重试错误（429/5xx/超时）时，Fallback 中间件按优先级依次尝试 fallback 供应商，任一成功即返回；全失败返回 `ErrAIFallbackExhausted`。
- **AC3**：ali 适配器实现 Rerank，经百炼 qwen3-rerank 返回 `{index, relevance_score}`（实测 HTTP 200）。
- **AC4**：gateway 对每条 route（含 fallback route）用其 provider 自己的适配器；fallback 到 ali 时用 ali 适配器而非 primary 的 dmxapi 适配器。
- **AC5**：HTTP 429 被 `retryableError` 识别为可触发 fallback。
- **AC6**：chat/embed 既有行为零回归——单 fallback 配置下 cascade 等价旧的"试 fallbacks[0]"；现有单测全绿。
- **AC7**：rerank 调用经 aiservice 统一入口，Langfuse rerank span 不再 ERROR（trace topology：rerank 作为 retrieval trace 下的 generation/span，记 provider+model）。

### 4.4 trace topology（AI 功能必填）
rerank 调用发生在 `retrieve.Service.Retrieve` 内，作为检索 trace 的一个子操作。现状已有 rerank span（之前显示 ERROR）。本次不改 trace 结构，只确保 fallback 后实际生效的 provider/model 反映在 generation/span metadata（billing 中间件 + tracing 中间件已在 chain 内，每条 route 调用都会记）。

## 5. 客户确认门禁
用户已授权"从 S0 直接走到 deploy dev 才停"（含 S1 提案确认、S2 设计确认、S6 dev 验收前的硬门禁均预授权），prod（S7）不在授权内。
