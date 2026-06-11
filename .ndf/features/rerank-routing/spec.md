# 技术设计 Spec：rerank-routing

> S2 工件 · feature id: rerank-routing · 2026-06-11
> 权威设计——S4 代码必须实现本 spec 的全部内容。

## 0. 设计目标

让 rerank 成为注册表驱动的一等能力：可挂多供应商、按优先级自动多级 fallback、跨异构 provider 用各自适配器。复用现有 `task_profile_service`（role=fallback + priority DESC）机制，与主对话模型同一套配置体验。

## 1. 现状根因（实证）

- gateway `resolveAndRun`（gateway.go:196-241）：`p := g.providers[primary.Provider.Name]` 后 `makeHandler(p, primary)`，闭包**捕获 primary 适配器**。Fallback 中间件用 fallback route 重调 `next` 时，适配器仍是 primary 的 → 跨家失败。
- Fallback 中间件（middleware/fallback.go）只试 `fallbacks[0]`，且仅当 `retryableError(primaryErr)`。
- `wrapHTTPStatusErr`（ali.go:341）：4xx 全归普通 error（非 retryable）→ 429 限流不触发 fallback。
- `ali.go`：`Capabilities()=["chat","embed"]`，无 Rerank。仅 `dmxapi.go` 实现 Rerank。

## 2. 改动设计（5 个 task）

### T1：gateway 每路由自适配器解析（核心）

改 `resolveAndRun`：dispatch 闭包在**调用时**按 `route.Provider.Name` 解析适配器，而非捕获 primary。

```go
func (g *Gateway) resolveAndRun(ctx, taskID, req, dispatch) (interface{}, error) {
    primary, _, err := g.registry.ResolveTask(ctx, taskID)   // 不变
    ... model override ...                                   // 不变
    // 保留 primary 快速失败（fail-fast，行为与今日一致）：
    pp := g.lookupProvider(primary.Provider.Name)
    if pp == nil { return nil, fmt.Errorf("gateway: no provider registered for %q", primary.Provider.Name) }
    if !dispatch.supports(pp) { return nil, dispatch.unsupportedErr(pp) } // 同今日错误信息
    // 每路由解析（供 fallback 用对家适配器）：
    handler := GatewayHandler(func(ctx, r, rawReq) (interface{}, error) {
        p := g.lookupProvider(r.Provider.Name)
        if p == nil { return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name) }
        return dispatch.call(ctx, p, r, rawReq)   // dispatch 内部断言能力 + 调用
    })
    if chainFn != nil { handler = chainFn(handler) }
    return handler(ctx, primary, req)
}
```

- **具体实现（reviewer 澄清）**：保留 `makeHandler func(p Provider, route) (GatewayHandler, error)` 签名不变；把"`p` 的解析"从**闭包构造时**（makeHandler 调用处捕获 primary 的 p）移到**闭包执行时**（handler 被 Fallback 用 fallback route 调用时，按 `r.Provider.Name` 重新解析）。即返回的 `GatewayHandler` 内部首行 `p := g.lookupProvider(r.Provider.Name)`。
- `lookupProvider(name)` = 抽出现有逻辑 `g.providers[name]` else `findAdapterByPrefix(name)`。**必须内部持 `g.mu.RLock()`**——现有 `findAdapterByPrefix` doc 注明"Caller must hold g.mu.RLock"；闭包在 chain 调用时执行（RLock 已释放），故 `lookupProvider` 自己加锁，否则 data race（`go test -race` 必跑）。`findAdapterByPrefix` 的"未知 provider 兜底到 dmxapi 适配器"行为（gateway.go:173-176）保留——per-route 路径下若 provider 名拼错会落到 dmxapi 适配器，再由闭包内的 capability 断言兜底；加一个测试用例覆盖未注册 provider 的行为。
- 每个能力（Chat/Embed/Rerank/OCR/ASR）提供 `dispatch`（capability 断言 + 实际调用）。
- **保留 primary fail-fast**：primary 不支持该能力时，在 chain 前返回（错误信息不变）→ 行为零回归。
- **fallback route** 经 `next` 进入 handler 时，按 fallback 的 provider 名解析适配器 → 用对家适配器。
- **范围**：只改 `resolveAndRun`（覆盖 Chat/Embed/Rerank/OCR/ASR）。`ChatStream`（gateway.go:268-323）有**结构等同 pre-T1 resolveAndRun 的独立解析块**、不走 resolveAndRun，本次**故意不动**（流式跨家 fallback 罕见且 chat 均 OAI 兼容，捕获 primary 适配器无害）——作为已知限制记录。

### T2：ali/百炼 rerank 适配器

`ali.go` 新增 `Rerank`，照 `Embed` 写（native 端点 + `doRawPost`），更新 `Capabilities()` 加 `"rerank"`，加编译断言 `var _ RerankAdapter = (*AliAdapter)(nil)`。

**端点**（与 embed 同款 nativeBase 派生）：
```
nativeBase := strings.Replace(route.Provider.BaseURL, "/compatible-mode/v1", "/api/v1", 1)
rerankURL  := nativeBase + "/services/rerank/text-rerank/text-rerank"
```

**请求**（实测 nested 才行）：
```go
type dashscopeRerankRequest struct {
    Model string `json:"model"`
    Input struct {
        Query     string   `json:"query"`
        Documents []string `json:"documents"`
    } `json:"input"`
    Parameters struct {
        ReturnDocuments bool `json:"return_documents,omitempty"`
        TopN            int  `json:"top_n,omitempty"`
    } `json:"parameters"`
}
```
- Model = `route.ProviderModelID`；ReturnDocuments=true；TopN 仅 >0 时设。空 Documents 直接返回空（同 dmxapi）。

**响应**（实测）：
```go
type dashscopeRerankResponse struct {
    Output struct {
        Results []struct {
            Index          int     `json:"index"`
            RelevanceScore float64 `json:"relevance_score"`
            Document       struct{ Text string `json:"text"` } `json:"document"`
        } `json:"results"`
    } `json:"output"`
    Usage     struct{ TotalTokens int `json:"total_tokens"` } `json:"usage"`
    Code      string `json:"code"`     // 逻辑错误（同 embed 的 Code 处理）
    Message   string `json:"message"`
    RequestID string `json:"request_id"`
}
```
- 映射到 `aiservice.RerankResult{Index, Score: relevance_score, Document: document.text}`；document.text 为空时回退 `req.Documents[index]`（同 dmxapi.go:342）。
- `Code != ""` → 返回 provider error（同 embed）。
- 实测 wire（已 probe dev ali key）：兼容/native 双端点均 200，document 嵌套 `{text}`、分数 `relevance_score`。

### T3：429 限流 → 可触发 fallback

`wrapHTTPStatusErr`（ali.go:341）：429（Too Many Requests）+ 408（Request Timeout）→ `errno.ErrAIProviderError`（已被 `retryableError` 认）。其余 4xx 仍非 retryable。

```go
func wrapHTTPStatusErr(op string, statusCode int, body []byte) error {
    if statusCode >= 500 || statusCode == 429 || statusCode == 408 {
        return errno.ErrAIProviderError.SetMessage("%s: HTTP %d: %s", op, statusCode, string(body))
    }
    return fmt.Errorf("%s: HTTP %d: %s", op, statusCode, string(body))
}
```
- 影响所有用 `wrapHTTPStatusErr` 的适配器（ali/dmxapi/volc）——统一正确语义。bge 免费档 5次/分钟限流（429）从此触发 fallback。

### T4：Fallback 中间件多级优先级 cascade

middleware/fallback.go：把"试 `fallbacks[0]` 一次"改成"按优先级遍历 `fallbacks` 全列表"。

```go
resp, err := next(ctx, route, req)
if err == nil { return resp, nil }
if !retryableError(err) { return resp, err }       // primary 非 retryable → 不 fallback（不变）
_, fallbacks, resolveErr := deps.Resolver.ResolveTask(ctx, route.TaskID)
if resolveErr != nil || len(fallbacks) == 0 { return resp, err }
// 进 cascade 前记一条 warn（保留旧版可观测性契约：primary_error + 候选数）
deps.warnw("fallback: engaging cascade", "task_id", route.TaskID,
    "primary_service_id", route.ServiceID, "primary_error", err, "candidates", len(fallbacks))
triedIDs := make([]uint64, 0, len(fallbacks))
for i := range fallbacks {
    fb := fallbacks[i]
    triedIDs = append(triedIDs, fb.ServiceID)
    fbCtx := withFallbackFromServiceID(withSkipRetry(ctx), route.ServiceID)
    fbResp, fbErr := next(fbCtx, &fb, req)
    if fbErr == nil { return fbResp, nil }
    // 任一 fallback 失败（含 capability-mismatch / 非 retryable）→ 继续试下一条
    deps.warnw("fallback: candidate failed", "task_id", route.TaskID, "fallback_service_id", fb.ServiceID, "err", fbErr)
}
// 全失败：带 provenance（试了哪些 service id）便于排障/对账
return nil, errno.ErrAIFallbackExhausted.SetMessage("tried %d fallback(s): %v", len(triedIDs), triedIDs)
```
- **零回归**：单 fallback 配置时循环 1 次 = 旧行为。多 fallback 时依次尝试（旧代码会忽略 `fallbacks[1:]`，这是隐性 bug，本次顺带修）。
- **fallback 候选失败处理**：循环内对**任何**错误（retryable 或非 retryable，含 `ErrAICapabilityMismatch`）都继续试下一条——fallback 已是 last-resort，逐家尝试是预期行为（`retryableError` 门禁只作用于是否对 **primary** 启动 cascade）。
- **provenance**：全失败返回的 `ErrAIFallbackExhausted` 带上试过的 service id 列表（reviewer F4：便于排障 + 对账）。
- skip_retry 仍注入，单家不放大调用数。

### T5：dev 注册表路由配置（数据，非代码；在 S5/部署阶段执行）

dev 上把 rerank 配成优先级链：
- service 22（model_key 改为代表 bge，或新建 service）→ provider dmxapi / `bge-reranker-v2-m3-free` 作为 **default_service_id**（primary）。
- 新建/复用 service（ali-dashscope / `qwen3-rerank`）→ `task_profile_service` role=fallback, priority 绑定到 task_profile 6（salesrag.rerank）。
- 经 SQL 配（dev），prod 不在本次授权。注意 `uk_model_provider(model_id, provider_id)` 每 service 每 provider 限一条 route。

## 3. trace topology（AI 功能）

rerank 调用在 `retrieve.Service.Retrieve` 内（已有 rerank span）。本次不改 trace 结构。

**chain 层序（reviewer 澄清，chain.go）**：Tracing（最外）→ Fallback → ContextBudget → Billing → Retry → Adapter。

- **Billing 在 Fallback 内层**：Fallback 用 fallback route 调 `next` 时，Billing 收到的是 fallback route → **计费正确记录实际生效的 provider/model**（T1 让 adapter 也对，端到端一致）。
- **Tracing 在 Fallback 外层**：generation/span 在 Fallback 之前用 **primary route** 创建。所以 fallback 生效时，外层 trace generation 显示的是 **primary 的 model 身份**、但结果为 success（Fallback 返回了 fallback 的成功结果）。
- **验收口径（修正 reviewer S2-P1）**：AC7 = rerank span **不再 ERROR**（这是用户真实目标）。**不**声称"span 反映 fallback 实际 provider"——那在当前 chain 拓扑下结构上不成立。计费侧由 Billing 中间件正确归属到实际 provider。若未来要让 trace 也精确反映 fallback provider，需把 Tracing 移到 Fallback 内层（invasive，不在本次范围）。

## 4. 验收标准映射

| AC | 由哪个 task 满足 | 验证 |
|----|----------------|------|
| AC1 多供应商优先级 | T5（机制 T4 支撑） | 注册表查询 + ResolveTask 返回 primary+fallbacks |
| AC2 自动 cascade | T4 + T1 | 单测：primary 429 → fallback 到下一家成功 |
| AC3 ali rerank 可用 | T2 | 单测（mock httpclient）+ dev 实跑 probe（已 200） |
| AC4 每路由自适配器 | T1 | 单测：fallback route provider≠primary 时用对家适配器 |
| AC5 429 retryable | T3 | 单测：wrapHTTPStatusErr(429) → ErrAIProviderError |
| AC6 chat/embed 零回归 | T1+T4 | 现有 gateway/middleware 单测全绿 + dev chat 实跑 |
| AC7 统一入口+trace | 全部 | dev Langfuse rerank span 不再 ERROR |

## 5. 测试策略（详见 S3 的 S5 验证策略 task）

- 后端 TDD 为主：T1（gateway dispatch 表驱动）、T2（ali rerank mock chatFn/httpclient）、T3（错误映射）、T4（fallback cascade，fake resolver + fake handler）。
- dev 实跑 sanity：部署后用 dev ali key 经 gateway 跑一次 rerank（已 probe 端点 200）+ 触发 chatbot 查询看 Langfuse rerank span 变绿 + 验证限流时 cascade（构造或观察）。
- 无 UI 改动 → 不做 Playwright E2E。回归保护靠 Go 单测（永久留存）。

## 6. 不做（明确边界）

- ChatStream 跨家 fallback（保留捕获 primary 适配器，已知限制）。
- admin UI rerank 路由可视化（用现有通用 AI 服务 CRUD）。
- prod 路由切换（停 dev，prod 后续授权）。
- embed 跨家 fallback 虽被 T1 修好，不专门验证（顺带受益）。
