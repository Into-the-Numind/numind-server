# AI Service Manager — S3 实施规划

> 基于 spec `numind-server/docs/superpowers/specs/2026-04-15-ai-service-manager-design.md`。
> 遵守 NDF 规则 9（task 原子性）+ 规则 10（S5 验证策略作为独立 task）。
> 后端 task 在前，管理端 task 在后；每个 task 完成后必须走 NDF 规则 6 的两阶段 review + commit。

## 任务总览（S3 review 修订后）

| # | Task | 仓库 | 前置依赖 | 预估 |
|---|---|---|---|---|
| 1a | Migration SQL + Rollback SQL + docker 演练 | numind-server | — | 0.3d |
| 1b | GORM Models（含老 LLMModel 别名 + 兼容 smoke）+ 空 seed 函数占位 | numind-server | #1a | 0.5d |
| 2 | Gateway 骨架 + Capability Schema + Matching | numind-server | #1b | 0.6d |
| 3 | Registry + Task Profile Resolver（DB 读写 + cache） | numind-server | #1b, #2 | 0.5d |
| 4 | 中间件链（Tracing + Billing + Retry + Fallback） | numind-server | #2, #3 | 1.0d |
| 5 | Provider Adapters（ali / volc / dmxapi） | numind-server | #2, #4 | 0.8d |
| 6 | Provider Adapters（baidu-ocr / bailian-file / funasr） | numind-server | #2, #4 | 0.6d |
| 7 | Legacy billing ctx skip flag（预备迁移） | numind-server | #2 | 0.4d |
| 8 | Gateway 对外入口 + Config struct 扩展 + seed 生效 + `/healthz/ai` | numind-server | #3, #4, #5, #6 | 0.6d |
| 9 | 迁移 SOP + ChatBot 调用 + **迁移前后 billing 对账** | numind-server | #7, #8 | 0.6d |
| 10a | 迁移 SalesRAG 问答（intent + chat + rerank） | numind-server | #7, #8 | 0.5d |
| 10b | 迁移 SalesRAG 入库（embed + tagging） | numind-server | #7, #8 | 0.4d |
| 10c | 迁移 SalesRAG 多模态（profile + chatstyle） | numind-server | #7, #8 | 0.4d |
| 11 | 迁移 Monitor（briefing/analyze/transcribe）+ Baidu OCR | numind-server | #7, #8 | 0.7d |
| 12 | Admin API — Services CRUD | numind-server | #3 | 0.5d |
| 13 | Admin API — Tasks CRUD + Capability Matching + Override + Audit | numind-server | #3, #12 | 0.7d |
| 14 | 兼容 `/v1/admin/llm/*` 改写内部实现（Task 1b smoke 已覆盖 `/v1/llm/models`） | numind-server | #12 | 0.2d |
| 15a | Config 清理（删老默认、`config_prod.yaml` 仅加 comment） | numind-server | #1b, #9, #10c, #11 | 0.2d |
| 15b | 文档更新：`.claude/rules/ai-service.md` + prod-config-sync runbook + tech debt 登记 | — | #15a | 0.3d |
| 16 | Admin-web：API client + 类型 + capability schema store | numind-admin-web | #12, #13（可与 14/15a 并行） | 0.4d |
| 17 | Admin-web：Services 列表/编辑页 | numind-admin-web | #16 | 0.8d |
| 18 | Admin-web：Tasks 列表/编辑页（含 TaskBindingEditor）+ **Playwright E2E 2 条** | numind-admin-web | #16 | 1.2d |
| 19 | Admin-web：Audit Log 页 | numind-admin-web | #16 | 0.3d |
| 20 | S5 验证策略（独立 task，规则 10 要求；16 条关键路径） | — | 全部 | 文档性 |

**合计**：12.5 天编码 + 0.5 天文档 ≈ **13 天**
**含 review buffer 20%**：**S4 预算 15-16 天**（修订自原 12 天，诚实反映规则 6 返工时间）

与 S2 spec §2 工时预估（正常 11-12 天 + 1 天 buffer + 风险触发 +2-3 天 = 最坏 14 天）对比：S3 修订后略超 S2 上限（15-16 vs 14），合理解释：
- 多了 Playwright E2E 2 条（~0.3d）
- Task 10 拆分带来多轮 review（~0.5d）
- Task 15b 文档同步新增（~0.3d）
- 若 S4 实际未触发风险（volc 流式、schema 合并意外），仍可收敛到 13-14 天

---

## Task 详述

### Task 1a：Migration SQL + Rollback SQL + docker 演练

**目标**：落地 spec §2 的全部 schema 变更，但**不改任何 Go 代码**——保证可独立 commit + 独立演练。

**影响文件**（仅 SQL）：
- 新增 `numind-server/migrations/20260416_000001_ai_service_manager.sql`
- 新增 `numind-server/migrations/20260416_000001_ai_service_manager_rollback.sql`

**SQL 内容**：按 spec §2.5 原子分组 A-E 顺序执行；每段有 comment；seed INSERT 的 api_key 字段留空字符串（由 Task 8 启动时 Go 同步填）。

**验收条件**：
- [ ] 用 `docker mysql:8.0.36` + dev 环境最近一次快照数据演练 migration 成功
- [ ] 用 rollback SQL 反向演练成功（结构 + 数据回到 migration 前）
- [ ] 演练步骤录入 `docs/deployment/ai-service-manager-migration-runbook.md`
- [ ] MySQL 版本要求 8.0.13+（因 JSON DEFAULT 语法），prod 版本 SSH 确认写入 runbook
- [ ] idx_task_created 索引用 `EXPLAIN` 验证能被 `WHERE task_id=? AND created_at BETWEEN ...` 命中（S2 spec P2-1 落地）

### Task 1b：GORM Models + 兼容 smoke

**目标**：新 struct + 老 struct 的 TableName 策略落地；保证老代码零改动仍可工作。

**影响文件**：
- 新增 `internal/pkg/model/ai_service.go`（新 struct `AIService`、`AIServiceRoute`、全量列）
- 新增 `internal/pkg/model/task_profile.go`（`TaskProfile`、`TaskProfileService`）
- 新增 `internal/pkg/model/ai_service_audit.go`（`AIServiceAuditLog`）
- 修改 `internal/pkg/model/usage.go`（加 9 个新字段，nullable）
- 修改 `internal/pkg/model/llm.go`（老 `LLMModel` / `LLMModelProvider` 的 `TableName()` 改为返回 `"ai_service"` / `"ai_service_route"`；struct 加 `service_type` 字段，默认 scope 自动附加 `WHERE service_type='llm'`；biz 层老调用不改 struct）
- 新增 `internal/pkg/aiservice/seed.go`：仅声明 `SyncProviderCredentials(ctx, db, cfg) error` 函数骨架，函数体为空 return nil（Task 8 填实现）
- 新增 `internal/pkg/aiservice/types.go`（基础 ChatRequest/EmbedRequest 等 struct 占位，Task 2 填字段）
- 修改 `internal/pkg/errno/` 新增 6 个错误码 `ErrAIXxx`（41001-41006；先确认 41xxx 命名空间未被占用——S2 spec P2-2 落地）

**关键**：此 task commit 后服务必须能启动且老业务照常工作（/v1/llm/models、ModelSelector 等）。这是 P0-2 的防线。

**验收条件**：
- [ ] `task lint` 通过
- [ ] dev 环境启动服务（基于 Task 1a 演练后的 DB）
- [ ] **兼容 smoke（硬验收）**：不改任何业务代码的情况下，`/v1/llm/models`、`/v1/llm/preference`（GET）、`/v1/admin/llm/models`（GET）返回 shape 和迁移前逐字段对比相同
- [ ] `/v1/admin/llm/providers` 返回迁移前字段 + 新字段 `provider_type='llm'`（下游可忽略）
- [ ] 新增 errno 41001-41006 不与现有冲突（grep `internal/pkg/errno/` 验证）
- [ ] go test 通过（不含新增 aiservice 包的测试）

---

### Task 2：Gateway 骨架 + Capability Schema + Matching

**目标**：建立 `internal/pkg/aiservice/` 包框架，实现 capability schema 单一数据源 + matching 算法。

**影响文件**：
- 新增 `internal/pkg/aiservice/types.go`（ChatRequest/EmbedRequest/... 等 struct）
- 新增 `internal/pkg/aiservice/profile/constants.go`（14 个 taskID 常量）
- 新增 `internal/pkg/aiservice/profile/capability_schema.go`（按 service_type 的 schema 定义）
- 新增 `internal/pkg/aiservice/profile/capability.go`（Match 函数，见 spec §5.2 伪代码）
- 新增 `internal/pkg/aiservice/profile/capability_test.go`

**验收条件**：
- [ ] `profile.Match(req, svc)` 覆盖 LLM/OCR/ASR 三类
- [ ] unit test：兼容/不兼容各 2 条（覆盖 modalities、context、features、dimension、OCR 格式、ASR 时长、语言）
- [ ] embedding dimension 不匹配返回明确 reason
- [ ] 字段语义约定以 godoc 注释形式写在 capability.go 顶部
- [ ] taskID 常量全覆盖 §5.1 的 14 个

---

### Task 3：Registry + Task Profile Resolver（DB 读写 + cache）

**目标**：封装 `ai_service` + `ai_service_route` + `task_profile` 的读写；实现 cache + invalidate。

**影响文件**：
- 新增 `internal/pkg/aiservice/registry/registry.go`（Service/Route/Profile 的 CRUD + Resolver）
- 新增 `internal/pkg/aiservice/registry/cache.go`（in-memory cache，30s TTL + 写触发 invalidate）
- 新增 `internal/pkg/aiservice/registry/store.go`（GORM-based DB access）
- 新增 `internal/pkg/aiservice/registry/registry_test.go`

**验收条件**：
- [ ] `Resolver.ResolveTask(ctx, taskID) (PrimarySpec, []FallbackSpec, error)` 返回主服务 + fallback 列表（按 priority 排序）
- [ ] Resolver 按 `deprecated_at IS NULL` 过滤已下架服务
- [ ] 写操作（SaveService/SaveProfile/DeprecateService）触发 cache local invalidate
- [ ] Cache miss 时从 DB 重新加载
- [ ] unit test：TTL 过期自动刷新 + 写操作立即生效

---

### Task 4：中间件链

**目标**：实现 spec §6 的 4 个中间件 + chain 执行器。

**影响文件**：
- 新增 `internal/pkg/aiservice/middleware/chain.go`
- 新增 `internal/pkg/aiservice/middleware/tracing.go`
- 新增 `internal/pkg/aiservice/middleware/billing.go`
- 新增 `internal/pkg/aiservice/middleware/retry.go`
- 新增 `internal/pkg/aiservice/middleware/fallback.go`
- 新增 `internal/pkg/aiservice/middleware/*_test.go`（每个中间件独立 test）

**关键实现点**：
- 中间件顺序：`Tracing → Billing → Fallback → Retry → Adapter`（Fallback 外、Retry 内）
- Retry 的流式首 chunk 约束：通过 ctx 传递 `first_chunk_sent=bool` 标志
- Fallback 注入 ctx `skip_retry=true` 到 fallback 调用
- Billing：读 pricing 进 ctx → 写 UsageRecord（按 service_type 选字段）；失败 log 不 block
- Tracing：Langfuse SDK 调用 `context.WithTimeout(2s)` + recover 保护；LLM 用 Generation，OCR/ASR/Rerank 用 Span

**验收条件（测试覆盖扩充）**：
- [ ] Tracing：至少 2 个 unit test（成功路径 + Langfuse 挂起路径）
- [ ] **Billing：3 个 service_type × 2 种路径（成功 / 错误）= 6 个 unit test**，验证 pricing snapshot + unit 字段 + 按 service_type 选用 tokens_input/output vs call_count vs duration_seconds
- [ ] **Fallback：3 个 unit test**——(a) 主成功（不触发 fallback）(b) 主失败+备成功（调用数 = 主 2 次 + 备 1 次 = 3）(c) 主失败+备失败（ErrAIFallbackExhausted）
- [ ] **Retry：3 个 unit test**——(a) 成功不重试 (b) 一次重试后成功 (c) 重试后仍失败
- [ ] 流式 Retry 首 chunk 单测：吐出 1 个 chunk 后失败不触发 retry（独立 test）
- [ ] Langfuse 降级单测：mock Langfuse SDK timeout/panic → 主流程仍返回正常结果
- [ ] `go test -race ./internal/pkg/aiservice/middleware/` 通过

---

### Task 5：Provider Adapters — ali / volc / dmxapi

**目标**：实现 3 个主力 LLM provider 的 adapter。

**影响文件**：
- 新增 `internal/pkg/aiservice/adapter/adapter.go`（interface 定义，按能力拆分 ChatAdapter/EmbedAdapter/...）
- 新增 `internal/pkg/aiservice/adapter/ali.go`（Chat/ChatStream/Embed/Vision/VisionStream）
- 新增 `internal/pkg/aiservice/adapter/volc.go`（Chat/ChatStream/Embed/Vision/VisionStream — 补齐 Langfuse 合规的 httpclient 使用）
- 新增 `internal/pkg/aiservice/adapter/dmxapi.go`（复用 `internal/pkg/llm/dmxapi_client.go` 底层）
- 新增 `internal/pkg/aiservice/adapter/*_test.go`（roundtrip tests with mock httpclient）

**关键实现点**：
- adapter 不做 retry、tracing、billing；仅 http 调用 + 格式翻译
- volc 的 `StreamChat` / `VisionAnalyze` 必须改走 `internal/pkg/httpclient`（不再裸 `http.Client`）
- adapter 通过 `ServiceSpec` 参数接收 provider 凭据（不 import 配置）

**验收条件**：
- [ ] 每个 adapter 至少 1 个 roundtrip test（用 httpmock/httptest.Server）
- [ ] volc adapter 无裸 `http.Post` / `http.NewRequest`
- [ ] Chat / ChatStream / Embed 覆盖完整
- [ ] `grep -rn "http.Post\|http.NewRequest" internal/pkg/aiservice/adapter/` 返回 0

---

### Task 6：Provider Adapters — baidu-ocr / bailian-file / funasr

**目标**：实现 OCR / 文件 / ASR 三个非 LLM adapter。

**影响文件**：
- 新增 `internal/pkg/aiservice/adapter/baidu_ocr.go`
- 新增 `internal/pkg/aiservice/adapter/bailian_file.go`
- 新增 `internal/pkg/aiservice/adapter/funasr.go`
- 新增对应 `*_test.go`

**关键实现点**：
- baidu OCR 的 access_token 获取逻辑从 `biz/baidu` 迁移过来；改走 httpclient
- bailian file 的签名逻辑从 `internal/service/bailian_http.go` 迁移
- funasr 直接调本地 URL，无鉴权

**验收条件**：
- [ ] 3 个 adapter 各 1 个 roundtrip test
- [ ] 都实现各自对应的小 interface（OCRAdapter / ASRAdapter）
- [ ] 都走 httpclient，不裸 http

---

### Task 7：Legacy billing ctx skip flag（预备迁移）

**目标**：为所有老封装层的 billing 写入逻辑增加 ctx flag 支持，迁移前逐个设置以防双记账。

**前置**：仅需 Task 2（`aiservice/legacy.go` 常量包），**不需要 Task 4（中间件链）**。可与 Task 3/4/5/6 并行。

**影响文件**：
- 修改 `internal/numind/biz/ali/ali.go`（相关 billing 写入处）
- 修改 `internal/numind/biz/volc/volc.go`
- **`internal/numind/biz/baidu/ocr.go` no-op**（inventory 确认 baidu 当前无 billing 写入；Task 11 迁移后由 Gateway Billing 中间件统一写入；此处加注释说明）
- 修改 `internal/numind/biz/salesrag/adapter/dmxapi_client.go`
- 修改 `internal/pkg/llm/dmxapi_client.go`
- 新增常量 `internal/pkg/aiservice/legacy.go`（`CtxKeySkipLegacyBilling` 等）

**实现方式**：
```go
// 每个 billing 调用点包裹
if !aiservice.ShouldSkipLegacyBilling(ctx) {
    // 原有 billing 写入逻辑
}
```

**验收条件**：
- [ ] `task lint` 通过
- [ ] 老路径在无 flag 时行为不变（现有业务未迁移前照常记账）
- [ ] 加 flag 时老 billing 被跳过
- [ ] baidu 注释明确说明 no-op 原因

---

### Task 8：Gateway 对外入口 + Config struct 扩展 + seed 生效

**目标**：组装骨架 + Registry + 中间件 + Adapter，暴露 `ai.Chat` / `ai.ChatStream` / `ai.Embed` / `ai.Rerank` / `ai.OCR` / `ai.ASR` 顶层函数；此 task 完成后 Gateway 可端到端工作。

**影响文件**：
- 新增 `internal/pkg/aiservice/ai.go`（顶层函数）
- 新增 `internal/pkg/aiservice/gateway.go`（Gateway struct + Build 函数，注入 DB + config + provider adapters）
- **修改 `internal/pkg/config/config.go`**（扩展 `AIProviders` 段 struct 定义，nullable 字段方便 config_*.yaml 渐进迁移）
- **实现 `internal/pkg/aiservice/seed.go` 的 `SyncProviderCredentials`**（Task 1b 只留空骨架，本 task 填 UPSERT 逻辑）
- 修改 `cmd/numind/main.go`（启动时 Build Gateway + 调 `SyncProviderCredentials`，注入全局 singleton；seed 失败 log error 不 block）
- 新增 `internal/pkg/aiservice/healthz.go`（`/healthz/ai` 端点 handler）
- 注册路由：修改 `internal/numind/router.go`（挂载 `/healthz/ai`，免鉴权）

**验收条件**：
- [ ] 6 个顶层函数都能工作（通过 mock adapter + registry 跑一次端到端 test）
- [ ] `go test -race ./internal/pkg/aiservice/...` 全部通过（覆盖 gateway + middleware + profile + registry）
- [ ] `/healthz/ai` 返回 200 + JSON（各 provider 最近错误率；初始值可为空 map）
- [ ] **dev 启动验证 seed**：故意在 config_dev.yaml 留 1 个 provider 的 key 为 `""` → 服务仍启动 + log 有 warn；填对后重启 → upsert 到 DB 成功
- [ ] Gateway mock adapter 端到端 test：构造"SOP 节点调用" → Gateway → middleware chain → mock adapter → 返回值 + UsageRecord 写入 + Langfuse generation 创建（mock 端点）

---

### Task 9：迁移 SOP + ChatBot 调用 + billing 对账

**目标**：把 SOP executor 和 ChatBot stream 从 `llmrouter.StreamChat` 迁移到 `ai.ChatStream`；同时完成**迁移前后 billing 字段对账**（P0-T20-2 要求）。

**影响文件**：
- 修改 `internal/numind/biz/sop/executor.go`（调用入口改 `ai.ChatStream(ctx, profile.SopText/SopVision, req)`）
- 修改 `internal/numind/biz/chatbot/stream.go`（改 `ai.ChatStream(ctx, profile.ChatbotStream, req)`）
- 迁移前在这两处设置 `ctx = aiservice.WithSkipLegacyBilling(ctx)`

**迁移前对账准备**（在改代码前执行）：
1. 在 dev 环境跑一次典型 SOP 调用，记录 UsageRecord 行（字段 + 值）到 `docs/superpowers/plans/ai-service-manager-billing-baseline.md`
2. 同样跑一次 ChatBot 调用，记录到同一文档

**迁移后对账**：
3. 同样调用跑一次
4. 用 SQL diff 对比迁移前后 UsageRecord 关键字段：`cost_cents` / `total_credits_cost` / tokens 字段 / user_id / feature_ref 等
5. 所有差异必须可解释（如：新增字段 task_id/service_type/pricing_snapshot 从 null → 有值是预期；cost_cents 应在合理 ±5% 内，因估算方法可能微调）
6. 若发现任何非预期差异（如 cost_cents 差 50%、user_id 错位），task **不能声明完成**

**验收条件**：
- [ ] SOP 节点执行 dev 环境手测 1 次，Langfuse 看到 generation（task_id=sop.text 或 sop.vision）
- [ ] ChatBot 问答 dev 环境手测 1 次，Langfuse 看到 generation
- [ ] UsageRecord 新字段正确填充（service_type=llm、task_id、pricing snapshot 等）
- [ ] ModelSelector（C 端）仍工作（/v1/llm/preference 读写正常）
- [ ] **Billing 对账文档 `billing-baseline.md` 生成，迁移前后 SQL diff 全部差异可解释**
- [ ] 无双记账：同一次调用在 UsageRecord 表只有 1 行

---

### Task 10a：迁移 SalesRAG 问答（intent + chat + rerank）

**目标**：把 SalesRAG 的问答链路（意图分析 → 向量检索 → rerank → 回答生成）迁到 Gateway。

**影响文件**：
- 修改 `internal/numind/biz/salesrag/salesrag.go`（主问答入口）
- 修改 `internal/numind/biz/salesrag/service/sales_rag.go`（rerank 调用）
- 调用入口替换为 `ai.Chat(salesragIntent)` / `ai.ChatStream(salesragChat)` / `ai.Rerank(salesragRerank)`

**验收条件**：
- [ ] dev 手测问答 1 次，Langfuse trace `salesrag-chat` 下有 3 个嵌套：intent generation / rerank span / chat generation
- [ ] UsageRecord 三条记录，task_id 各自正确
- [ ] Rerank 返回 top-N 正确
- [ ] 与 Task 9 同样做 billing diff 对账（更简单：新记录字段齐全即可）

### Task 10b：迁移 SalesRAG 入库（embed + tagging）

**目标**：把 SalesRAG ingestion pipeline 的 embedding 和 tagging 迁到 Gateway。

**影响文件**：
- 修改 `internal/numind/biz/salesrag/service/pipeline.go`
- 修改 `internal/numind/biz/salesrag/service/tagger.go`

**验收条件**：
- [ ] dev 手测：上传 1 份小型文档 → Langfuse trace `salesrag-ingest` 下有 embed generation + tagging generation
- [ ] 向量库写入成功，向量维度正确（task_profile 的 requirements.dimension=1024 约束未触发错误）
- [ ] UsageRecord 两条，service_type=llm，tokens/call_count 正确

### Task 10c：迁移 SalesRAG 多模态（profile + chatstyle）

**目标**：最复杂的多模态场景（文本+图片输入）。

**影响文件**：
- 修改 `internal/numind/biz/salesrag/salesrag.go`（profile 和 chatstyle 入口）

**验收条件**：
- [ ] dev 手测客户档案分析：文本 + 图片各 1 张 → Langfuse 有 vision-stream generation（task_id=salesrag.profile）
- [ ] 聊天风格分析同样验证
- [ ] ChatRequest.Messages 的 multipart content（文本 + image_url）在 adapter 层正确翻译为 provider API 格式

---

### Task 11：迁移 Monitor + Baidu OCR

**目标**：4 个调用点（briefing / analyze / transcribe / ocr.baidu）全部走 Gateway。

**影响文件**：
- 修改 `internal/numind/biz/monitor/briefing.go`
- 修改 `internal/numind/biz/monitor/analyzer.go`
- 修改 `internal/numind/biz/monitor/transcriber.go`
- 修改 `internal/numind/biz/baidu/ocr.go`（或删除裸 http 调用处，改调 `ai.OCR`）

**验收条件**：
- [ ] 手动触发 Monitor 简报 1 次，Langfuse 看到 trace + generation（briefing + analyze 嵌套）
- [ ] 手动触发 Monitor 视频转写 1 次，Langfuse 看到 span（task_id=monitor.transcribe）
- [ ] 上传图片触发 OCR 1 次，Langfuse 看到 span（task_id=ocr.baidu）+ UsageRecord 含 call_count=1
- [ ] transcribe 的 duration_seconds 字段正确填充

---

### Task 12：Admin API — Services CRUD

**影响文件**：
- 新增 `internal/numind/controller/v1/admin/ai_service.go`
- 新增 `internal/numind/biz/aiservice_admin/` 业务层（CRUD + 软删 + 恢复 + audit）
- 修改 `internal/numind/admin_router.go`

**端点（详见 spec §4.2）**：
- GET/POST `/v1/admin/ai/services`
- GET/PUT/DELETE `/v1/admin/ai/services/:id`
- POST `/v1/admin/ai/services/:id/restore`
- GET `/v1/admin/ai/capability-schema`

**验收条件**：
- [ ] 6 个端点 curl 验证返回 shape 符合 spec §4.4 示例
- [ ] 软删除后 list 默认不展示（`?include_deprecated=true` 展示）
- [ ] restore 需填 reason，不填返回 400
- [ ] 所有变更写 audit log

---

### Task 13：Admin API — Tasks CRUD + Capability Matching + Override + Audit + Health

**影响文件**：
- 新增 `internal/numind/controller/v1/admin/task_profile.go`
- 新增 `internal/numind/controller/v1/admin/audit_log.go`
- 扩展 `internal/numind/biz/aiservice_admin/`
- 修改 `admin_router.go`

**端点**：
- GET/PUT `/v1/admin/ai/tasks` + `/:id`
- PUT `/v1/admin/ai/tasks/:id?force=true`（强制保存不兼容绑定）
- POST `/v1/admin/ai/services/:id/validate-against/:task_id`
- GET `/v1/admin/ai/audit-logs`

**验收条件**：
- [ ] 保存不兼容绑定返回 41001 + incompatible_bindings 列表（spec §4.4 示例）
- [ ] force=true 时强制保存 + 写 audit（action=capability.override）
- [ ] force=true 无 reason 返回 400
- [ ] validate-against 端点返回 compatible bool + reasons
- [ ] audit-logs 支持按 actor_id / target_type 筛选
- [ ] `/healthz/ai` 已在 #8 挂接，此处验证内容正确

---

### Task 14：`/v1/admin/llm/*` 内部实现清理

**目标**：`/v1/llm/models`、`/v1/llm/preference` 已由 Task 1b 的 TableName 映射自动透明工作；本 task 仅做 `/v1/admin/llm/*` 管理端的 GORM 查询清理（显式加 `WHERE service_type='llm'` 过滤，避免将来 admin 误看到 OCR/ASR 行混入）。

**影响文件**：
- 修改 `internal/numind/biz/llmrouter/router.go`（管理端相关查询加 service_type 过滤）
- 修改 `internal/numind/controller/v1/admin/llm.go`（如需要）

**验收条件**：
- [ ] `/v1/admin/llm/models` 返回只含 LLM 类型（不出现 OCR/ASR）
- [ ] `/v1/admin/llm/providers` 返回只含 `provider_type='llm'`（ocr/asr provider 不混入）
- [ ] 现有 admin-web 的 LLM 管理老 UI（如有）不受影响
- [ ] Task 1b 的兼容 smoke 作为前置保证已过

---

### Task 15a：Config 清理

**目标**：按 spec §11 删除老默认模型配置，新增 `ai_providers` 段（config struct 已在 Task 8 扩展）。

**影响文件**：
- 修改 `config_local.yaml` / `config_dev.yaml` / `config_qa.yaml`（**不改 config_prod.yaml**，按 CLAUDE.md 硬规则）
- 删除 ali.text.model / ali.vision.model / volc.model 等字段
- 新增 `ai_providers:` 段（凭据）

**验收条件**：
- [ ] dev 环境启动成功，seed 自动同步 5 个 provider 的 api_key 到 DB
- [ ] 老 biz 代码对已删除配置字段的读取（`viper.GetString("ali.text.model")` 等）全部找到并清理（grep 扫描）
- [ ] 清理后 `task lint` 通过
- [ ] 迁移完的业务（SOP/SalesRAG/Monitor 等）全部正常（dev 手测每个模块 1 次）

### Task 15b：文档更新 + tech debt 登记

**目标**：补齐 S1 proposal §治理承诺遗留的文档工作。

**影响文件**：
- 修改 `.claude/rules/ai-service.md`：
  - 旧规"所有 LLM 调用必须集成 Langfuse + 走封装层"升级为"所有 AI 服务调用必须通过 `aiservice.Xxx` 入口；禁止业务代码 `import` provider 包（ali/volc/baidu/dmxapi/bailian）；禁止裸 http"
  - 更新"提供商与模型清单"章节指向 DB Registry（说明：现在运营从 admin-web 管理）
- 新增 `docs/deployment/prod-config-sync-runbook.md`：prod 部署时 config_prod.yaml 新增 `ai_providers` 段的手动同步步骤（一人公司的 "运维" = 项目负责人自己，此文档就是 checklist）
- 修改 `numind-server/build-manifest.yaml` 的本 feature 条目添加 `tech_debt` 段：
  - "langfuse.base_url 内网 IP（config_prod.yaml:439）"
  - "llmrouter 包最终拆除或保留的决策（Phase 2 评估）"

**验收条件**：
- [ ] `.claude/rules/ai-service.md` 更新后读通，无矛盾
- [ ] prod-config-sync runbook 有分步骤指令，可照做
- [ ] manifest tech_debt 段 2 条记录

---

### Task 16：admin-web API client + 类型 + capability schema store

**前置**：Task 12 + 13（后端 API 定义稳定即可）。**可与 Task 14/15a 并行**（打破串行瓶颈）。

**影响文件**（仓库：numind-admin-web）：
- 新增 `src/api/ai.ts`
- 新增 `src/types/ai.ts`
- 新增 `src/stores/aiCapabilitySchema.ts`（Pinia store，启动拉一次缓存）
- 新增 `src/composables/useCapabilityMatching.ts`（本地 matching，避免调用风暴）

**验收条件**：
- [ ] API client 覆盖 Task 12、13 的所有端点
- [ ] `npm run type-check` 通过
- [ ] schema store 启动一次加载 capability-schema
- [ ] 本地 matching composable 与后端 matching 规则一致（依据 spec §5.2 伪代码）
- [ ] 单元测试对比 composable 与后端 `/validate-against` 接口：随机生成 10 组 (service, task) 组合，两边结果一致

---

### Task 17：admin-web — Services 列表/编辑页

**影响文件**：
- 新增 `src/views/AIService/ServicesList.vue`
- 新增 `src/views/AIService/ServiceEdit.vue`
- 新增 `src/components/ai/ServiceForm.vue`（按 service_type 动态渲染）
- 新增 `src/components/ai/ServiceTable.vue`（DataTable 封装）
- 路由注册 `src/router/ai.ts`
- 侧边栏加入口

**UI 硬规则合规**：
- DataTable 非 Card Grid（硬规则 1）
- 4 状态：loading/empty/error/success（硬规则 2）
- 字段 blur 触发验证（硬规则 3）
- 删除/下架用 ConfirmModal（硬规则 4）
- 不用 Element Plus / Vant 等（硬规则 5）

**验收条件**：
- [ ] 列表页可筛选 service_type / status / provider
- [ ] 编辑页按 service_type 动态渲染不同能力字段
- [ ] 保存时 blur 触发验证
- [ ] 软删除/恢复走 ConfirmModal
- [ ] `npm run lint && npm run type-check` 通过
- [ ] **手动 QA 跑一次增删改查**（对照 Task 20 路径 10）

---

### Task 18：admin-web — Tasks 列表/编辑页（含 TaskBindingEditor）+ Playwright E2E

**影响文件**：
- 新增 `src/views/AIService/TasksList.vue`
- 新增 `src/views/AIService/TaskEdit.vue`
- 新增 `src/components/ai/TaskBindingEditor.vue`（核心组件）
- 新增 `src/components/ai/CompatibilityIndicator.vue`（实时显示匹配状态）
- **新增 `e2e/ai-service-task-binding.spec.ts`**（Playwright E2E）

**关键实现**：
- TaskBindingEditor 实时本地 matching（用 Task 16 的 composable）；不匹配的 service 选项 disabled + tooltip 说明原因
- 强制保存（force=true）走独立确认 dialog，要求填 reason
- fallback/allowed 支持拖动排序（priority）

**Playwright E2E 必覆盖 2 条路径**（防止 TaskBindingEditor 本地/后端 matching 漂移）：
1. **不兼容绑定被拒**：登录 → 打开 `sop.vision` 编辑 → 选纯文本 service 作 default → 下拉项 disabled + tooltip 可见；手动切到 force 模式 → 弹窗要求 reason → 填 reason 保存成功 → 跳到 audit log 页可见 `capability.override` 记录含 actor/reason
2. **Services 软删 + 恢复 reason 校验**：打开 Services 列表 → 选一个 service → 点软删 → ConfirmModal 出现 → 取消不删；再点软删 → 确认 → 列表隐藏；进 deprecated 视图 → 恢复 → 必须填 reason → 提交 → audit 可见

**验收条件**：
- [ ] 选中不兼容 service 时，下拉项 disabled + tooltip 显示具体原因
- [ ] force override 流程：点"强制保存"→弹窗要求 reason→保存成功显示 audit 记录
- [ ] fallback 列表拖动排序 priority 生效
- [ ] 本地 matching 结果与后端一致（单测 + E2E 覆盖）
- [ ] Playwright E2E 2 条 spec 通过（在 CI 或本地 `npm run test:e2e`）
- [ ] `npm run lint && npm run type-check` 通过

---

### Task 19：admin-web — Audit Log 页

**影响文件**：
- 新增 `src/views/AIService/AuditLogs.vue`

**验收条件**：
- [ ] 按 actor / target_type / 时间范围筛选
- [ ] 展示 diff（JSON 可展开）
- [ ] 支持分页
- [ ] `npm run lint && npm run type-check` 通过

---

### Task 20：S5 验证策略（规则 10 必选）

> 本 task 不产生代码，仅确认 S5 验证方式。由 S3 gate reviewer 专门审查（NDF 规则 10）。

#### 验证方式选择
- **主力：dev 环境手测 + biz 层 unit test**
- **不使用 Playwright E2E**（因本功能 C 端 UI 不改，ModelSelector 等老组件按原样工作；新 admin-web 页面有管理员手工 QA）
- **不使用 gstack /qa**（同上）

#### 理由
1. C 端 UI 零改动 → 用户侧无 UI 回归面
2. Admin-web 新页面由一人公司项目负责人自己用，不值得建 Playwright 契约
3. Gateway 核心逻辑通过 unit test 保障（中间件、capability matching、adapter roundtrip、race）
4. 集成验证靠 dev 环境的 Langfuse + UsageRecord 对账

#### 关键用户路径（S5 必验）— **16 条**
1. **SOP 纯文本节点**：dev 触发一次，Langfuse 有 generation（task_id=sop.text），UsageRecord 新字段完整
2. **SOP 图文节点**：dev 触发一次带图输入，task_id=sop.vision，Langfuse confirm
3. **ChatBot 问答**：dev 触发一次，Langfuse confirm + token 正确
4. **SalesRAG 问答（完整 RAG）**：dev 触发一次，含 intent/rerank/chat 三条 generation
5. **SalesRAG 入库**：dev 上传一份文档，embed + tagging 两条 generation
6. **SalesRAG 客户档案（多模态）**：dev 触发一次，含图片输入
7. **Monitor 简报**：手动触发，briefing trace 嵌套 analyze span
8. **Monitor 视频转写（ASR）**：上传一段音频，duration_seconds 正确
9. **Baidu OCR**：上传图片，call_count=1，pricing_call_snapshot 正确
10. **管理端 Services CRUD**：新增一个 service、编辑能力、软删、恢复（Playwright E2E 已覆盖软删+恢复 reason 路径）
11. **管理端 Tasks 绑定 + Capability Matching**：
    - 尝试给 `sop.vision` 绑定纯文本 service → 保存被拒绝 + 原因显示
    - force override → 必须填 reason → 成功 + audit 记录可见（Playwright E2E 已覆盖）
12. **Fallback 容灾**：手动改 dmxapi base_url 为无效值 → 请求自动切 fallback（验证调用数 ≤ 3：主 2 + 备 1）→ 恢复 base_url
13. **Langfuse 降级**：手动关闭 Langfuse（如 config.enabled=false 重启）→ 业务流程仍正常
14. **Pricing 修改不污染历史**：改某 service pricing → 对比改前后的 UsageRecord：旧行 pricing_snapshot 为改前价、新行为改后价
15. **（新增）Billing 对账复核**：Task 9 的 `billing-baseline.md` 迁移前后对账结论在 S5 重新确认——抽查 SOP + ChatBot + 一条 SalesRAG 的 UsageRecord 迁移前后数据，确认 BillingAccount 扣费金额在合理范围（±5%）
16. **（新增）兼容层 + Seed smoke**：(a) `/v1/llm/models` 和 `/v1/llm/preference` C 端（numind-web-v3 浏览器）可访问，ModelSelector 下拉正常显示模型列表；(b) 故意 config 中去掉一个 provider key 重启服务，确认仍启动 + log warn（seed 降级路径）

#### 回归保护的诚实声明
- Gateway 中间件 + capability matching 有 unit test 作为持久化回归保护 ✓
- 每个 adapter 有 roundtrip test ✓
- **admin-web TaskBindingEditor 本地/后端 matching 一致性通过 Task 18 Playwright E2E 2 条 spec 持续保护** ✓
- 业务层迁移后的集成行为仅靠 dev 环境手测 + Langfuse 对账，**未来修改 biz 层 AI 调用代码时，除了上述 E2E 外没有自动 E2E 保护**。需要人工跑上述 16 条路径
- **billing 属于财务数据**（UsageRecord 写入会影响 BillingAccount 余额扣费，见 `.claude/rules/business-logic.md §4`）。本功能虽然不改 billing 核心计算逻辑，但写入路径从老封装层迁到 Gateway 中间件，**Task 9 迁移前后对账**（`billing-baseline.md`）是必做验收。S5 路径 15 复核对账结论；若 Phase 2 扩展 Gateway billing 逻辑，必须每次对账

#### manifest 登记要求
- S3 gate 通过后，本 Task 20 作为 S5 策略由 reviewer 与 plan 原子性审查同一个 reviewer 一并审查（NDF 规则 10）
- **S5 执行前 cross-check**：主控 AI 必须对照本 16 条路径生成 test matrix。S4 执行过程中若发现任何新增调用点（本期未列入 Task Profile 的 AI 调用点），必须补入本路径列表，不得省略
- S5 结束后，主控 AI 必须更新 manifest.last_verified 段落记录全部 16 条路径通过情况

---

## 依赖关系图（S3 review 修订后）

```
#1a SQL 演练
    │
    ▼
#1b GORM Models + 兼容 smoke
    │
    ├──> #2 Gateway 骨架 + Matching ──┬──> #3 Registry ────┐
    │                                  │                   │
    │                                  │                   ▼
    │                                  │              #4 中间件链
    │                                  │                   │
    │                                  └──> #5/6 Adapters ─┤
    │                                  │                   │
    │                                  └──> #7 Legacy flag │ (并行)
    │                                                      │
    │                                                      ▼
    │                                                 #8 Gateway Entry + seed
    │                                                      │
    │                                                      ├──> #9 Mig SOP/ChatBot + billing 对账
    │                                                      ├──> #10a/b/c Mig SalesRAG
    │                                                      └──> #11 Mig Monitor/OCR
    │
    └──> #12 Admin Svc CRUD ──> #13 Admin Task CRUD ──> #14 兼容层清理
                                      │
                                      ▼
         （可与 #14、#15a 并行）─── #16 admin-web 基础
                                      │
                                      ├──> #17 Services UI (手动 QA)
                                      ├──> #18 Tasks UI + Playwright E2E × 2
                                      └──> #19 Audit UI

后端迁移完成（#9, #10c, #11）──> #15a Config 清理 ──> #15b 文档 + tech debt 登记

全部完成 ──> #20 S5 验证策略（16 条路径 cross-check）
```

**打破串行瓶颈**：
- Task 7 前置 = Task 2（非 #4），可与 #3/#4/#5/#6 并行
- Task 16 前置 = Task 12/13 API 稳定，可与 #14/#15a 并行，**不必等"后端全完"**
- Task 15a/15b 只需迁移完成（#9/#10c/#11），不等前端

---

## 每 Task 完成后必做（NDF 规则 6）

1. implementer subagent 完成 task（commit）
2. 主控 AI 验证 commit（NDF 规则 8：`git log --oneline -1 && git status`）
3. Spec Compliance Review（dispatch Sonnet subagent）
4. Code Quality Review（dispatch Sonnet subagent）
5. 任何 P0 发现 → fix subagent 修 → 重走 review
6. 两个 review PASS → `progress.reviewed_tasks += 1`
7. 然后才能开下一个 task

**禁止：** 批量实现后再统一 review；跳过 review；合并多个 task 一次 review。

---

## Progress Tracking（S4 填充，total_tasks=23）

| # | Task | 状态 | Spec Review | Code Review | Commit |
|---|---|---|---|---|---|
| 1a | Migration SQL + 演练 | pending | — | — | — |
| 1b | GORM Models + 兼容 smoke | pending | — | — | — |
| 2 | Gateway 骨架 + Capability | pending | — | — | — |
| 3 | Registry | pending | — | — | — |
| 4 | 中间件链 | pending | — | — | — |
| 5 | Adapters LLM | pending | — | — | — |
| 6 | Adapters 非 LLM | pending | — | — | — |
| 7 | Legacy billing flag | pending | — | — | — |
| 8 | Gateway 入口 + seed 生效 | pending | — | — | — |
| 9 | Mig SOP/ChatBot + billing 对账 | pending | — | — | — |
| 10a | Mig SalesRAG 问答 | pending | — | — | — |
| 10b | Mig SalesRAG 入库 | pending | — | — | — |
| 10c | Mig SalesRAG 多模态 | pending | — | — | — |
| 11 | Mig Monitor/OCR | pending | — | — | — |
| 12 | Admin Svc CRUD | pending | — | — | — |
| 13 | Admin Task CRUD | pending | — | — | — |
| 14 | 兼容层清理 | pending | — | — | — |
| 15a | Config 清理 | pending | — | — | — |
| 15b | 文档 + tech debt | pending | — | — | — |
| 16 | admin-web 基础 | pending | — | — | — |
| 17 | admin-web Services UI | pending | — | — | — |
| 18 | admin-web Tasks UI + E2E | pending | — | — | — |
| 19 | admin-web Audit UI | pending | — | — | — |
| 20 | S5 验证策略（文档） | pending | — | — | — |

---

## S3 Review 修订记录（2026-04-15）

| Review 发现 | 严重性 | 处理 |
|---|---|---|
| Task 1 原子性违背（seed 调用依赖未存在的 config 段） | P0 | ✅ 拆为 1a/1b；seed 生效移到 Task 8 |
| Task 14 顺序/兼容层映射风险 | P0 | ✅ Task 1b 加兼容 smoke；Task 14 降级为 admin/llm/* 过滤清理 |
| Task 10 伪装 1 个 task 实为 7 commits | P0 | ✅ 拆为 10a/10b/10c 三个独立 review 周期 |
| admin-web 零 E2E 回归真空 | P0 | ✅ Task 18 加 Playwright E2E 2 条 spec |
| Billing 迁移前后对账缺失 | P0 | ✅ Task 9 加 `billing-baseline.md` 对账 + Task 20 新增路径 15 |
| Task 7 前置错位 | P1 | ✅ 改为 Task 2 |
| 前后端过度串行 | P1 | ✅ 依赖图打破：Task 16 可并行 #14/#15a |
| 工时未算 review buffer | P1 | ✅ 改为 15-16 天（+20% buffer） |
| Task 4 测试覆盖不足 | P1 | ✅ Billing 扩至 6 测试、Fallback 3 测试、Retry 3 测试 |
| 遗漏 `.claude/rules/ai-service.md` 更新 | P1 | ✅ 新增 Task 15b 专门负责 |
| 遗漏 prod-config-sync runbook | P1 | ✅ Task 15b 负责 |
| 遗漏 tech debt 登记（Langfuse 内网 IP、llmrouter 终态） | P1 | ✅ Task 15b 写入 manifest |
| S2 spec P2 遗留未分配 | P2 | ✅ Task 1a（idx + MySQL 版本）+ Task 1b（errno） |
| Task 17 "Playwright 或手动 QA" 歧义 | P2 | ✅ 改为"手动 QA（对照 Task 20 路径 10）" |
| Task 20 16 条路径 + billing 财务声明修正 | P0（Task 20 强化） | ✅ 从 14 条扩到 16 条；修正不涉及 billing 的错误声明 |
| Task 20 S5 cross-check 要求 | P1 | ✅ 明确 S5 执行前/后的 manifest 更新义务 |
