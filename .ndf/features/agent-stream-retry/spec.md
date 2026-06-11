# agent-stream-retry — 技术设计 Spec

> 架构选型见 ADR `.ndf/decisions/agent-stream-retry/0001-streaming-retry-architecture.md`（Option 3：既有 Retry+Fallback middleware 流式化）。本 spec 定义实现契约 + AC 映射。

## §1 总览

四块改动（均在 numind-server，internal/pkg/aiservice 下）：

| 模块 | 文件 | 改动 |
|------|------|------|
| C 修复 | `gateway.go` | ChatStream handler 改 per-route lookupProvider（对齐 Embed），删锁定 primary 适配器 + NOTE(rerank-routing T1) |
| D registry | `registry/store.go` `registry/registry.go` | 新增 `ListResolvedRoutesByModel`(store) + `ResolveModelAlternates`(Registry 接口+impl) |
| 流式 Retry | `middleware/retry.go`（+ 新 helper） | Retry 流式感知：首内容 chunk 前 retryable error → 同 route 重试 ≤1 次 |
| 流式 Fallback | `middleware/fallback.go` | Fallback 流式感知：首内容 chunk 前 retryable error → cascade 同模型 alternates |

共享 reattempt 助手放新文件 `middleware/stream_reattempt.go`，被 Retry 与 Fallback 复用。

## §2 共享 reattempt 助手（stream_reattempt.go）

```go
// reattemptFunc 返回下一次尝试的 channel。Retry 传「重调同 route」，
// Fallback 传「cascade 下一个 alternate route」。返回 (nil, false) 表示无更多尝试。
type reattemptFunc func(ctx context.Context) (<-chan aiservice.ChatChunk, error, bool)

// wrapStreamWithReattempt 消费 firstCh，对外返回单一拼接 channel。
// 不变量（对应 ADR MUST-HANDLE）：
//  P0-1 失败 channel 的 error 终止 chunk 绝不冒泡——重试成功后丢弃它；
//       重试耗尽才把「最后一次」error 终止 chunk 透传（对外有且仅一个终止 chunk）。
//  P0-2 重试前 for range 旧 channel drain 到底（防 HTTP body/goroutine 泄漏）。
//  P0-3 firstContentForwarded：任何 Delta!="" || ReasoningDelta!="" 即置真，永久禁用重试。
//  P1-5 重试判定仅 chunk.IsFinal && chunk.Err != nil && retryableError(chunk.Err) && !firstContentForwarded，不看 Usage。
// backoff 为 nil 时不等待（Fallback）；非 nil 时 select ctx.Done/After（Retry）。
func wrapStreamWithReattempt(
    ctx context.Context,
    firstCh <-chan aiservice.ChatChunk,
    reattempt reattemptFunc,
    backoff func() time.Duration, // nil = 不等待
) <-chan aiservice.ChatChunk
```

核心循环骨架（单 goroutine 顺序消费，零竞态）：
```
ch := firstCh; firstContentForwarded := false
for {
  var pendingErr *ChatChunk
  for chunk := range ch {
    if chunk.IsFinal && chunk.Err != nil && !firstContentForwarded && retryableError(chunk.Err) {
        c := chunk; pendingErr = &c; break          // 候选重试，先不转发
    }
    if chunk.Delta != "" || chunk.ReasoningDelta != "" { firstContentForwarded = true }
    select { case out <- chunk:; case <-ctx.Done(): drain(ch); return }
    if chunk.IsFinal { drainRest(ch); return }       // 正常/非可重试终止已透传
  }
  if pendingErr == nil { return }                     // channel 关闭无终止
  drain(ch)                                            // P0-2
  if backoff != nil { select { <-ctx.Done() / <-After(backoff()) } }
  next, err, more := reattempt(ctx)
  if !more || err != nil || next == nil {             // 无更多尝试/起流失败
      out <- *pendingErr; return                       // P0-1 透传最后错误（仅此一个终止）
  }
  ch = next                                            // 继续下一尝试
}
```
> 注：reattempt 自身负责状态（Retry 用 0/1 计数；Fallback 用 alternate 索引）。`more=false` 表示用尽。

## §3 流式 Retry（retry.go）

`retryWithPolicy` 改：调 `next()` 拿到 resp。
- `shouldSkipRetry(ctx)` 时仍直接 `return next(...)`（P0-4：保留 early-return；fallback 候选不被同供应商重试）。
- 若 `resp` 是 `<-chan aiservice.ChatChunk` 且 err==nil：用 `wrapStreamWithReattempt` 包裹，reattempt = 「计数<1 时重调 `next(ctx, route, req)` 取 channel，more=true；否则 more=false」，backoff = `policy.retryDelay`。返回 wrapped channel + nil。
- 否则（非流式）：保留**现有**同步重试逻辑（含 ctxKeyFirstChunkSent 检查 retry.go:146 不动）。

零回归：非流式路径字节级不变；流式成功流（首 chunk 即内容）退化为透明转发。

## §4 流式 Fallback（fallback.go）

`Fallback` 改：调 `next(ctx, route, req)` 拿 resp。
- 若 `resp` 是 `<-chan ChatChunk` 且 err==nil：用 `wrapStreamWithReattempt` 包裹，reattempt = 「按序取下一个 alternate route，注入 `withSkipRetry`+`withFallbackFromServiceID(route.ServiceID)`，调 `next(fbCtx, &altRoute, req)` 取 channel；alternates 用尽则 more=false」，backoff=nil。
  - alternates 来源：`deps.Resolver` 若实现 `ResolveModelAlternates`（type-assert，graceful）则取「同模型不同供应商」路由；空则无 fallback。
  - 注：流式 Fallback 优先用同模型 alternates（满足需求「同一模型的另一个供应商」）。现有非流式 role=fallback cascade 路径保持不变。
- 否则（非流式）：保留**现有**同步 cascade 逻辑（fallback.go:33-94 不动）。

## §5 配套 C：gateway.ChatStream per-route lookup（gateway.go）

handler 闭包改（对齐 Embed gateway.go:368-378）：
```go
handler := GatewayHandler(func(ctx, r, rawReq) (interface{}, error) {
    rp := g.lookupProvider(r.Provider.Name)
    if rp == nil { return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name) }
    cp, ok := rp.(ChatProvider)
    if !ok { return nil, fmt.Errorf("gateway: provider %q does not support ChatStream: %w", rp.Name(), errno.ErrAICapabilityMismatch) }
    ch, err := cp.ChatStream(ctx, r, rawReq.(ChatRequest))
    if err != nil { return nil, err }
    return ch, nil
})
```
构造期保留 primary lookup 作 fail-fast capability 检查（与 resolveAndRun 的 `p` 同义），删除 NOTE(rerank-routing T1) 注释。

## §6 配套 D：registry（store.go + registry.go）

### store.go
`ListResolvedRoutesByModel(ctx, serviceID uint64) ([]*resolvedRouteRow, error)`：复制 GetResolvedRoute 的 JOIN（ai_service→ai_service_route→llm_provider，同 is_active/deprecated 过滤），**去掉 LIMIT 1**，`ORDER BY r.priority DESC, r.id ASC`，返回全部 active 路由行。

### registry.go（Registry 接口新增方法）
```go
// ResolveModelAlternates 返回 primary 模型的「同模型不同供应商」备用路由，按 priority DESC，
// 排除 excludeProviderID（已作 primary 试过的供应商）。无备用返回空 slice（非 error）。
ResolveModelAlternates(ctx context.Context, taskID string, primaryServiceID, excludeProviderID uint64) ([]ResolvedRoute, error)
```
impl：`ListResolvedRoutesByModel(primaryServiceID)` → `buildResolvedRoute(taskID, row)` 逐行 → 过滤 `row.ProviderID == excludeProviderID` → 返回。4 个 test stub 各加 `return nil, nil`（unused-by-them，零行为变化）。

## §7 AC → 实现映射

| AC | 落点 |
|----|------|
| AC1 同供应商重试 | §3 Retry 流式 reattempt（计数≤1）|
| AC2 跨供应商降级 | §4 Fallback 流式 + §6 alternates |
| AC3 全失败才报错 | helper P0-1 透传最后 error 终止 chunk |
| AC4 已吐内容不重试 | helper P0-3 firstContentForwarded |
| AC5 不重复计费 | Retry 在 Reserve 之下 + helper P0-1 吞错误 chunk（ContextBudget/Billing 只见单终止）|
| AC6 不重复透传内容 | helper：重试只在 !firstContentForwarded，丢弃失败 channel 内容（首 chunk 前无内容可丢）|
| AC7 ≤3 upstream | retry≤1 + skip_retry（P0-4）+ alternates 串行 |
| AC8 真切 endpoint | §5 per-route lookup + adapter per-route 读凭据；dev 实测 |
| AC9 零回归 | 非流式/成功流退化透明转发 + chatbot/sop/salesrag 回归测试 |

## §8 验证策略（S5，对应 ndf-enforcement 规则 10）
- 方式：**后端 Go TDD 为主**（支付/计费高风险逻辑 testing.md 要求单测）+ **dev 实跑端到端**（无 UI 改动，不做 Playwright）。
- 关键单测（fake stream，参考 adapter/stream_idle_test.go 的 stallingReader 范式）：
  1. 首 chunk 前 ErrAIProviderTimeout → 断言同 route reattempt 被调用 1 次（RED 复现：现状不重试）。
  2. 重试再失败 → 断言降级到另一供应商 route（不同 Provider.Name/ServiceID）。
  3. 已吐内容后 error → 断言不重试、原样透传。
  4. 计费断言：mock CreditService，断言 Reserve 调用次数（同供应商重试=1 次 Reserve；跨供应商=primary 1 次 reserve+refund、alt 1 次 reserve+reconcile）。
  5. 零回归：正常流式（首 chunk 即内容、无 error）→ chunk 序列字节级不变、reattempt 0 次。
- dev 实跑：admin/admin123456 登录 dev，用「定位调研助手」(agent 100008) 跑完整调研路径；核 Langfuse rerank/chat span + 容器 image sha；验证跨供应商降级真打到 dmxapi endpoint（如能制造 aihubmix 卡顿则观测；否则用单测覆盖 + 注册表配置核对）。

## §9 关键用户路径（S5 需覆盖）
1. agent 流式正常跑完（首 chunk 前无卡顿）——零回归。
2. agent 流式首 chunk 前卡顿 → 自动重试/降级 → 正常跑完（核心新增能力）。
3. chatbot / SOP 流式正常——零回归。
