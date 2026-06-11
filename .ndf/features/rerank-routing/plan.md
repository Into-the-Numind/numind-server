# 实施计划 Plan：rerank-routing

> S3 工件 · feature id: rerank-routing · 2026-06-11
> 输入：spec.md。task 串行（多数 task 触及 gateway/adapter/middleware 重叠区，Tier 4 串行，主 session 自己实现 + 每 task 后并行双 Sonnet reviewer）。

## 依赖图（无环）

```
T1 (gateway 每路由自适配器) ──┐
T2 (ali rerank 适配器) ───────┼──► T4 (fallback 多级 cascade) ──► T5 (S5 验证策略)
T3 (429 retryable) ──────────┘
```
T1/T2/T3 各自独立可编译可测；T4 逻辑独立但端到端依赖 T1（用对家适配器）+ T2（ali 能 rerank）+ T3（429 触发）。实现顺序：T1 → T2 → T3 → T4。

---

## T1：gateway 每路由自适配器解析

- **描述**：重构 `resolveAndRun`，dispatch 闭包在调用时按 `route.Provider.Name` 解析适配器（非捕获 primary）。抽 `lookupProvider(name)`。每能力提供 dispatch（capability 断言 + 调用）。保留 primary fail-fast（chain 前，错误信息不变）。`ChatStream` 不动。
- **涉及文件**：`internal/pkg/aiservice/gateway.go`、`internal/pkg/aiservice/gateway_test.go`
- **验收**：
  - 表驱动单测：fallback route 的 provider ≠ primary 时，dispatch 用 fallback provider 的适配器（用两个 fake provider 验证调用落在哪个）。
  - primary 不支持能力时，chain 前返回（错误信息与今日一致）。
  - 现有 Chat/Embed/Rerank/OCR/ASR 单测全绿（零回归）。
  - `task lint` + `go test ./internal/pkg/aiservice/...` 退出 0。

## T2：ali/百炼 rerank 适配器

- **描述**：`ali.go` 加 `Rerank`（native 端点 `…/api/v1/services/rerank/text-rerank/text-rerank`，nested 请求，`doRawPost`），`dashscopeRerankRequest`/`Response` 结构体（spec §2.T2 实测形态），`Capabilities()` 加 `"rerank"`，编译断言 `var _ RerankAdapter = (*AliAdapter)(nil)`。document.text 空时回退 `req.Documents[index]`。
- **涉及文件**：`internal/pkg/aiservice/adapter/ali.go`、`internal/pkg/aiservice/adapter/ali_rerank_test.go`（新）
- **验收**：
  - 单测（mock httpclient 返回实测 native 响应 JSON）：解析出 index/score/document，按序映射。
  - 空 Documents 返回空 RerankResponse（不发请求）。
  - provider error（Code != ""）返回错误。
  - 编译断言通过（ali 实现 RerankAdapter）。
  - `go test ./internal/pkg/aiservice/adapter/...` 退出 0。

## T3：429/408 → 可触发 fallback

- **描述**：`wrapHTTPStatusErr`（ali.go:341）加 `statusCode == 429 || statusCode == 408` → `errno.ErrAIProviderError`（retryable）。更新函数 doc comment。
- **涉及文件**：`internal/pkg/aiservice/adapter/ali.go`（与 T2 同文件，串行）、`internal/pkg/aiservice/adapter/*_test.go`
- **验收**：
  - 单测：`wrapHTTPStatusErr("x", 429, ...)` → `errors.Is(err, errno.ErrAIProviderError)` true；`retryableError(该 err)` true。
  - 400/403/404 仍非 retryable（回归断言）。
  - `go test ./internal/pkg/aiservice/...` 退出 0。

## T4：Fallback 中间件多级 cascade

- **描述**：`middleware/fallback.go` 把单次 `fallbacks[0]` 尝试改成按优先级遍历 `fallbacks` 全列表，首个成功即返回，全失败返回 `ErrAIFallbackExhausted`。每次注入 skip_retry + fallback metadata。primary 非 retryable 仍直接传播（不变）。
- **涉及文件**：`internal/pkg/aiservice/middleware/fallback.go`、`internal/pkg/aiservice/middleware/fallback_test.go`
- **验收**：
  - 单测：3 个 fallback，前两个失败第三个成功 → 返回第三个结果。
  - 单测：全部失败 → `ErrAIFallbackExhausted`。
  - 单测（零回归）：单 fallback 成功/失败行为与旧版一致。
  - 单测：primary 非 retryable 错误 → 不进 fallback（直接传播）。
  - `go test ./internal/pkg/aiservice/middleware/...` 退出 0。

## T5（S5 验证策略，Rule 10 必填）

- **验证方式**：**后端 TDD 单测为主 + dev 实跑 sanity**（无 UI 改动 → 不做 Playwright E2E）。
- **理由**：本 feature 全是后端 gateway/adapter/middleware 逻辑，无前端。Go 单测覆盖核心逻辑（每路由解析、cascade、429 映射、ali rerank 解析）并永久留存做回归保护。dev 实跑确认真实链路（ali key、Langfuse trace、限流 cascade）。
- **关键路径（S5 需验证）**：
  1. `task lint` + `go test ./...`（完整）+ `go test -race ./internal/pkg/aiservice/...` 全绿。
  2. dev 部署后：配 dev 注册表路由（dmxapi/bge primary + ali/qwen3-rerank fallback，见 spec §2.T5）。
  3. dev 经 gateway 跑一次 rerank（触发 chatbot 查询 "创业要经过哪几个阶段"）→ Langfuse rerank span **不再 ERROR**，记录实际 provider/model。
  4. 验证 cascade：把 dev primary 临时指向必失败模型（或观察 bge 限流），确认自动切到 ali/qwen3 并成功（span 显示 fallback provider）。验证后恢复。
  5. chat 零回归：dev 跑一次普通对话，确认正常（T1 改了共享派发核心）。
- **回归保护诚实声明**：后端单测永久留存（不是一次性）。dev 实跑 sanity 是一次性确认，不产生持久测试，但核心逻辑已被 Go 单测覆盖。涉及共享 AI gateway（间接影响 billing 计费路径），故 reviewer 应确认 chat/embed 零回归断言充分。

## Bug-from-Customer（Rule 11）判定

本 feature 起因是**线上 bug 报告**（用户实测 chatbot rerank 报错、要求查服务器日志）。按 Rule 11，**第一个 commit 必须是失败的复现测试**：
- T2 或 T1 的首 commit 用 `test(qa): reproduce rerank provider ali does not support Rerank` 前缀，复现"ali provider 无 rerank 适配器导致 gateway 报错 + 跨家 fallback 拿错适配器"。
- 该测试在 T1/T2 修复后变 PASS，永久留存。
- spec-compliance reviewer 必须 grep 分支 commit log 验证存在 `test(qa):`/`test(repro):` 前缀 commit。
