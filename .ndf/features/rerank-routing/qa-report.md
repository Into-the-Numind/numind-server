# S5 QA 报告：rerank-routing

> S5 工件 · 2026-06-11 · 验证策略=后端 TDD 为主 + dev 实跑 sanity（无 UI → 无 Playwright，见 plan T5）

## 1. 自动化测试（本地，永久回归保护）

| 范围 | 命令 | 结果 |
|------|------|------|
| 改动包 + race | `go test -race ./internal/pkg/aiservice/...` | ✅ 全绿 |
| 全仓 | `go test ./...` | ✅ 零 FAIL |
| lint | `golangci-lint run ./internal/pkg/aiservice/...` | ✅ exit 0 |
| vet | `go vet ./...` | ✅ exit 0（仅 sqlite-vec cgo deprecation 预存警告）|

> 注：`task lint` 因环境 PATH 未含 golangci-lint 而 127；直接用 `$(go env GOPATH)/bin/golangci-lint` 跑改动包 exit 0。

## 2. 关键路径覆盖（Go 单测，永久留存）

- **T0 复现（Rule 11）**：`TestGateway_RerankFailsOverToDifferentProvider` — primary rerank 返回可重试错误 → 跨家 fallback 用 fallback provider **自己的**适配器成功。RED（修前）→ GREEN（修后）。
- **T1**：`TestGateway_EmbedFailsOverToDifferentProvider`（embed 跨家，billing 关键路径）/ `TestGateway_Rerank_PrimaryLacksCapability`（复现生产原始错误 "does not support Rerank"）/ `TestGateway_Rerank_UnregisteredProvider_Errors`。
- **T2**：`TestAliAdapter_Rerank`（native nested wire + 映射）/ empty-docs no-call / document fallback / provider-error。
- **T3**：`TestWrapHTTPStatusErr_Retryability`（429/408/5xx → 可触发 fallback；400/401/403/404/422 → 否）。
- **T4**：`TestFallback_CascadeTriesAllUntilSuccess`（3 候选逐级 + 顺序）/ `...AllFailReturnsExhaustedWithProvenance` / `...ContinuesOnNonRetryableCandidate`；既有单 fallback 测试零回归。

## 3. 可观测性（AI 功能）

trace 结构未改。billing 中间件在 Fallback 内层 → 计费正确归属实际生效 provider/model。验收口径：dev rerank span 不再 ERROR（部署后实跑确认）。

## 4. 待 dev 部署后实跑 sanity（S6 后）

1. 配 dev 注册表：rerank task → dmxapi/bge-reranker-v2-m3-free（primary，止血已配）+ ali-dashscope/qwen3-rerank（fallback，task_profile_service role=fallback）。
2. 触发 chatbot 查询 → Langfuse rerank span 不再 ERROR。
3. cascade 实测：临时让 primary 失败 → 观察自动切 ali/qwen3-rerank 成功 → 验证后恢复。
4. chat 零回归：dev 跑普通对话正常（T1 改了共享派发核心）。

## 5. 结论

后端逻辑 S5 验证通过（全绿 + race + lint）。无 UI 改动，回归保护由 Go 单测永久承担。dev 实跑 sanity 在 S6 ndf-done + /deploy-dev 后执行。
