# Spec：SOP 流式执行健壮性批次

- **Feature ID**: sop-stream-robustness
- **日期**: 2026-06-05
- **关联**: requirements/ + proposals/sop-stream-robustness-proposal.md

> 权威实现源。代码必须实现本 spec 的全部内容。行号为撰写时快照，实现时以实际为准。

---

## §0 现状关键事实（已读码确认）

- `biz.ExecuteNodeStream`（sop.go:501）注入 langfuse trace + billing meta + reservation
  ref 到 `ctx`（:783-793），Reserve（:835）+ defer `FinalizeReservation`（:812）。
  控制器传入的是 `heartbeatCtx = WithCancel(c.Request.Context())`（controller:778）。
- 失败路径（sop.go:872-894）把节点标 `Failed` 并 defer 走 Refund。
- `executeViaGateway`（executor.go:416）消费 `aiservice.ChatStream` 的 `<-chan ChatChunk`；
  adapter（ali/volc/dmxapi）`make(chan ChatChunk, 64)` + `go runOAIStream(resp.Body,...)`。
  adapter HTTP 请求用传入 ctx → **取消 ctx 会关闭连接，runOAIStream 的 `scanner.Scan()`
  随之返回错误并 close(ch)**。
- 兜底 `callAli/callVolcDeepThinkingStream`（executor.go:747/1108）：裸 `http.Client{Timeout:0}`
  + `bufio.Reader.ReadBytes('\n')` 阻塞读，仅循环顶 `select ctx.Done()`，**无 idle 超时**。
  `[DONE]` 时调 `handler("done","")`（:881/:1232）。
- 控制器 3 个 SSE handler（ExecuteNodeStream:636 / EditTextStream:2058 / ChatAfterRunStream:2283）
  各自 `var mu sync.Mutex` + `WithCancel` + `NewTicker(15s)` + 心跳 goroutine（含冗余内层
  `c.Request.Context().Done()` 检查）。
- `ExecuteNodeStream` 控制器：handler 收到 `done` 写一次（:846），biz 返回后又写一次（:885）→
  **兜底路双 done**；gateway 路 executeViaGateway 不发 done → 单 done。
- `ChatAfterRunStream` biz（sop.go:1591-1602）已拦截 `done` 事件，末尾 `handler("done", donePayload)`
  携带 `message_id`（:1707-1708）。
- 再生清理（sop.go:738-749）：`DeleteNodeRunsAfterSort` + `DeleteNotesByRun` +
  `DeleteChatMessagesByRun` 三次独立删，各自失败只 Warnw 续行。
- 状态更新：draft→running（sop.go:559-573）先内存置 Running 再 UpdateRun，失败只 log 续行；
  二次兜底（:656-670）`if run.Status != Running` 因内存已置 Running 被跳过，UpdateRun 失败 return error。
- `credit.classifyReason`（credit_service.go:1146）：`context.Canceled`→`user_cancelled`，
  `context.DeadlineExceeded`→`provider_timeout`，else→`op_failed`。
- `errtranslate.ToErrno`（translate.go:46）：errors.As(*errno.Errno) → errors.Is(ErrInsufficientCredits)。
  `FriendlyForSSE` 映射不到 → 通用文案。`errno.ErrAIProviderTimeout` 已存在（ali adapter 用）。
- `GetRunStatus`（sop.go:998 / route `GET /v1/sop/runs/:id/status`，router.go:202）已返回
  completedNodes 含 `Output`/`Thinking` → **重连查询无需新端点**。
- SOP store 方法多不接 `ctx`（GORM 用裸 `s.db`）→ 落库写入不受 ctx 取消影响（关键：detach 后落库安全）。

---

## §1 问题 1：断连解耦（P1）

**根因**：LLM 调用跑在请求 ctx 上，客户端断开 → ctx 取消 → 调用被掐 → 节点判 Failed + 退款。

**设计**：

### 1.1 biz 层 detach（sop.go `ExecuteNodeStream` + `ChatAfterRunStream`）
在注入 trace/billing **之前**，把执行 ctx 从请求生命周期剥离，并叠加整体超时（§2.2）：

```go
// detachStreamContext 把 LLM 流式执行从 HTTP 请求生命周期剥离（问题 1）：
// 客户端断开只取消请求 ctx，不应中断生成+落库。WithoutCancel 保留所有 ctx 值
// （langfuse trace / billing meta / reservation ref）。叠加整体超时兜底（问题 2）。
func detachStreamContext(parent context.Context, overall time.Duration) (context.Context, context.CancelFunc) {
    return context.WithTimeout(context.WithoutCancel(parent), overall)
}
```

调用点（两个 biz 流式入口）：在权限/前置检查之后、billing/trace 注入之前：
```go
ctx, cancelStream := detachStreamContext(ctx, sopOverallTimeout())
defer cancelStream()
```
**defer 顺序**：`detachStreamContext` 的 `defer cancelStream()` 必须注册在
`defer FinalizeReservation`（:812）**之前**，使其 LIFO 后执行 → Reconcile/Refund 时
ctx 仍有效。detach 点在 :782 之前满足此序。

落库安全：detach 后整体超时若在 LLM 中途触发，错误路径仍写 `UpdateNodeRun`（store 不接
ctx，不受取消影响）→ 节点失败状态正常落库。

### 1.2 控制器 handler 不因断连中止生成
三个 SSE handler 现状：handler 内 `case <-c.Request.Context().Done(): return ...Err()`
→ 兜底路 executor 收到 handler error 会 `return`（中止生成）。改为**断连不中止**：

```go
var clientGone atomic.Bool
// ... handler closure:
if clientGone.Load() {
    return nil // 已断开：停止向客户端推送，但让 biz 继续生成+落库
}
select {
case <-c.Request.Context().Done():
    clientGone.Store(true)
    log.C(c).Infow("client disconnected; continuing generation in background")
    return nil
default:
}
mu.Lock(); defer mu.Unlock()
if _, err := c.Writer.WriteString(data); err != nil {
    clientGone.Store(true)
    log.C(c).Warnw("client write failed; continuing generation in background", "error", err)
    return nil // 不向上传播错误，生成继续
}
flusher.Flush()
return nil
```
gateway 路 executeViaGateway 本就忽略 handler error（log+continue），与之一致。
仅 `ExecuteNodeStream` / `ChatAfterRunStream` 两个 SOP 节点生成 handler 套用此模式。
EditTextStream（文本编辑助手，非节点生成、无退款）保持请求 ctx 语义，仅做心跳/done 收敛。

### 1.3 重连查询
复用 `GET /v1/sop/runs/:id/status`（返回节点 Output）。前端瞬断后轮询此接口即可拉到
后台落库的结果。**不新增端点**。`runningRuns` 互斥锁（sop.go:503）确保重连重复触发同节点
返回"正在处理中"，不会双跑。

**验收**：断连后生成完成并落库；不退款（正常 Reconcile）；重连可查到结果；无 `event: error`。

---

## §2 问题 2：idle + 整体超时（P1）

### 2.1 配置 + helper（executor.go，package sop）
```go
func sopIdleTimeout() time.Duration {
    if v := viper.GetDuration("sop.stream_idle_timeout"); v > 0 { return v }
    return 4 * time.Minute // 代码兜底（含 prod）
}
func sopOverallTimeout() time.Duration {
    if v := viper.GetDuration("sop.stream_overall_timeout"); v > 0 { return v }
    return 30 * time.Minute
}
```
config_local/dev/qa.yaml 加（**不动 config_prod.yaml**）：
```yaml
sop:
  stream_idle_timeout: 4m       # 连续多久无新数据视为卡死（idle 超时）
  stream_overall_timeout: 30m   # 单次流式生成整体上限（兜底，足够复杂任务）
```

### 2.2 整体超时
由 §1.1 `detachStreamContext` 的 `WithTimeout` 提供（context deadline，**绝不用
http.Client.Timeout**）。超时 → `context.DeadlineExceeded` → classifyReason `provider_timeout`。

### 2.3 idle 超时 — 兜底路（callAli / callVolc / 旧 plain reader）
共享 helper（executor.go）：
```go
// idleWatcher 在 idle 时长内无 mark() 调用（无新 chunk）时关闭 body 解除阻塞读；
// ctx 取消时同样关闭 body。tripped() 区分 idle 超时 vs ctx 取消，供调用方返回明确错误。
type idleWatcher struct {
    timer   *time.Timer
    tripped atomic.Bool
}
func startIdleWatcher(ctx context.Context, body io.Closer, idle time.Duration) (*idleWatcher, func()) {
    w := &idleWatcher{}
    w.timer = time.AfterFunc(idle, func() { w.tripped.Store(true); _ = body.Close() })
    stop := make(chan struct{})
    go func() {
        select {
        case <-ctx.Done(): _ = body.Close()
        case <-stop:
        }
    }()
    return w, func() { w.timer.Stop(); close(stop) }
}
func (w *idleWatcher) mark(idle time.Duration) { w.timer.Reset(idle) }
```
在每个兜底 reader：
```go
idle := sopIdleTimeout()
watcher, stopWatcher := startIdleWatcher(ctx, resp.Body, idle)
defer stopWatcher()
for {
    // 删除循环顶冗余 select（ctx 取消已由 watcher 关闭 body 解阻塞）
    lineBytes, readErr := reader.ReadBytes('\n')
    if readErr != nil {
        if watcher.tripped.Load() {
            return usage, fmt.Errorf("LLM provider stalled: no data for %s: %w", idle, context.DeadlineExceeded)
        }
        // ... 既有处理（ctx.Err() / EOF / read error）
    }
    watcher.mark(idle) // 收到任意 provider 字节即重置 idle 时钟
    // ... 既有行处理
}
```
idle 超时错误 wrap `context.DeadlineExceeded` → 退款分类 `provider_timeout` + errtranslate 友好化。

### 2.4 idle 超时 — Gateway 主路（executeViaGateway）
对 `ch` 的消费循环加 idle 计时 + ctx cancel 传导（cancel 会关闭 adapter 连接 →
runOAIStream 退出）：
```go
streamCtx, cancelStream := context.WithCancel(ctx)
defer cancelStream()
ch, err := aiservice.ChatStream(streamCtx, taskID, req) // 注意传 streamCtx
...
idle := sopIdleTimeout()
idleTimer := time.NewTimer(idle)
defer idleTimer.Stop()
for {
    select {
    case chunk, ok := <-ch:
        if !ok { goto streamDone } // channel 关闭 = 流结束
        if !idleTimer.Stop() { select { case <-idleTimer.C: default: } }
        idleTimer.Reset(idle)
        // ... 既有 chunk 处理（Delta/ReasoningDelta/IsFinal/Usage）
    case <-idleTimer.C:
        cancelStream()                    // 关闭 adapter 连接
        go func() { for range ch {} }()   // drain，防 adapter goroutine 阻塞泄漏
        return fullContent.String(), finalUsage,
            fmt.Errorf("gateway stream idle timeout: no data for %s: %w", idle, context.DeadlineExceeded)
    }
}
streamDone:
// ... 既有 streamErr / empty 检查
```
整体超时由父 ctx（detach 的 WithTimeout）提供：超时 → adapter scan 失败 → 终止 chunk 带 Err →
现有 `streamErr` 分支返回（错误链含 DeadlineExceeded）。

### 2.5 errtranslate 超时友好化
`ToErrno` 增加：
```go
case errors.Is(err, context.DeadlineExceeded):
    return errno.ErrAIProviderTimeout, true
```
（确认 `errno.ErrAIProviderTimeout.Message` 为用户友好文案；若不够友好，SetMessage。）

**验收**：4 分钟无字节 → idle 超时干净中止 + 友好错误；30 分钟整体兜底可配置不误杀；
两条路覆盖。

---

## §3 问题 3：心跳 helper（P2）

新文件 `controller/v1/sop/sse.go`：
```go
const sseHeartbeatInterval = 15 * time.Second

// startSSEHeartbeat 启动心跳 goroutine（每 15s 写 SSE 注释行保活），返回写锁
// （调用方所有 c.Writer 写入必须持有它）和 stop（defer 调用）。心跳在 stop() 或
// 请求 ctx 取消（客户端断开）时停止。去掉原冗余内层 ctx 检查（ctx 派生自请求 ctx）。
func startSSEHeartbeat(c *gin.Context, flusher http.Flusher) (*sync.Mutex, func()) {
    mu := &sync.Mutex{}
    ctx, cancel := context.WithCancel(c.Request.Context())
    ticker := time.NewTicker(sseHeartbeatInterval)
    go func() {
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                mu.Lock()
                _, err := c.Writer.WriteString(":\n\n")
                if err == nil { flusher.Flush() }
                mu.Unlock()
                if err != nil {
                    log.C(c).Warnw("Failed to send heartbeat", "error", err)
                    return
                }
            }
        }
    }()
    return mu, cancel
}
```
三个 handler 替换为：
```go
mu, stopHeartbeat := startSSEHeartbeat(c, flusher)
defer stopHeartbeat()
```
写入处用 `mu.Lock()/Unlock()`；biz/edit 调用传 `c.Request.Context()`（或其 billing 包装）。
心跳逻辑本身保留（长文必需）。

---

## §4 问题 4：done 只发一次（P2）

**根因**：兜底 executor `[DONE]` 发 `handler("done","")` 透传到控制器写一次，biz 返回后
控制器又写一次。

**设计**（executor 不再向客户端透传 done，控制器统一在 biz 正常返回后发一次）：
- 删 `callAliDeepThinkingStream`（:881）/`callVolcDeepThinkingStream`（:1232）的
  `_ = handler("done","")`。
- 删 `ExecuteNodeStreamWithThinking` handler wrapper 中已 dead 的 `case "done"`（:614/:630）。
- 删 `callVolcEditStream`/`callAliEditStream`（controller，约 :2712 `_ = handler("done","")`）。
- 删 `ExecuteNodeStream` 控制器 handler 内已 dead 的 `else if event=="done"`（:845-846）
  与 `EditTextStream` 内对应分支（:2187-2188 / :2232-2233）。终结 done 由各控制器末尾
  单次写出（ExecuteNodeStream:885 携带 uploaded_file_ids；EditText:2278；ChatAfterRun 由 biz
  `handler("done", donePayload)` 携带 message_id）。
- `ChatAfterRunStream` 控制器 handler 的 `case "done"`（:2397-2402）**保留**（biz 主动发
  携带 message_id 的 done）；biz 的 done-拦截（sop.go:1598）**保留**作防御。

**不破坏 message_id**：ChatAfterRun 的 done 由 biz 携带 message_id，路径不变。

**验收**：两条路各只发一次 done；ChatAfterRun done 仍含 message_id。

---

## §5 问题 5：再生清理事务化（P2）

store 新增（store/sop.go + ISopStore 接口）：
```go
// CleanupDownstreamForRegeneration 在单个事务内删除：下游节点运行（sort > afterSort）、
// 该 run 的最终笔记、该 run 的对话消息。任一失败整体回滚（要么全成、要么全不动），
// 避免再生时残留过时下游数据。
func (s *sopStore) CleanupDownstreamForRegeneration(runID uint, afterSort int) error {
    return s.db.Transaction(func(tx *gorm.DB) error {
        if err := tx.Where("run_id = ? AND sort > ?", runID, afterSort).Delete(&model.SopNodeRun{}).Error; err != nil {
            return fmt.Errorf("delete downstream node runs: %w", err)
        }
        if err := tx.Where("run_id = ?", runID).Delete(&model.SopNote{}).Error; err != nil {
            return fmt.Errorf("delete run notes: %w", err)
        }
        if err := tx.Where("run_id = ?", runID).Delete(&model.SopChatMsg{}).Error; err != nil {
            return fmt.Errorf("delete run chat messages: %w", err)
        }
        return nil
    })
}
```
biz（sop.go:736-749）替换三次删为：
```go
if err := b.ds.Sop().CleanupDownstreamForRegeneration(runID, node.Sort); err != nil {
    // 原子清理失败 = 可能残留过时下游数据，中止再生而非在脏时间线上继续。
    return fmt.Errorf("failed to clean up downstream records for regeneration: %w", err)
}
```
保留旧的 3 个独立 Delete 方法（通用，可能他处用）。

**验收**：原子；失败回滚；upstream（sort ≤ afterSort）幸存。

---

## §6 问题 6：状态更新退避重试（P2）

biz helper（sop.go）：
```go
// updateRunStatusWithRetry 带 3 次指数退避持久化 run 状态更新。SOP run 状态抖动不应
// 暴露给用户（问题 6）：瞬时 DB 失败重试；全失败则记录（Error 级，进告警）留待
// ResetZombieRuns 收敛，用户流不受影响。
func (b *sopBiz) updateRunStatusWithRetry(ctx context.Context, runID uint, updates map[string]interface{}) error {
    var err error
    backoff := 100 * time.Millisecond
    for attempt := 0; attempt < 3; attempt++ {
        if err = b.ds.Sop().UpdateRun(runID, updates); err == nil {
            return nil
        }
        log.C(ctx).Warnw("run status update failed; will retry",
            "run_id", runID, "attempt", attempt+1, "error", err)
        time.Sleep(backoff)
        backoff *= 2
    }
    log.C(ctx).Errorw("run status update failed after retries; leaving for zombie-reset",
        "run_id", runID, "error", err)
    return err
}
```
**draft→running（sop.go:559-573）**：先重试 DB，**成功后**才翻内存状态（保证内存/DB 一致）：
```go
if run.Status == model.SopStatusDraft {
    var startedAt *time.Time
    draftUpdate := map[string]interface{}{"status": model.SopStatusRunning}
    if run.StartedAt == nil {
        now := time.Now(); startedAt = &now; draftUpdate["started_at"] = now
    }
    if err := b.updateRunStatusWithRetry(ctx, run.ID, draftUpdate); err != nil {
        // 重试耗尽，DB 仍 draft。不翻内存 → 下方二次兜底/完成块/zombie-reset 可再收敛。
        // 用户流不受影响（静默自愈）。
    } else {
        run.Status = model.SopStatusRunning
        if startedAt != nil { run.StartedAt = startedAt }
    }
}
```
**二次兜底（sop.go:656-670）**：`if run.Status != Running`（draft 重试失败时仍进）改用
`updateRunStatusWithRetry`；**失败不再 return error**，log 后续行（让生成进行，
完成块/zombie-reset 收敛）：
```go
if run.Status != model.SopStatusRunning {
    updateData := map[string]interface{}{"status": model.SopStatusRunning, "error_message": ""}
    if run.StartedAt == nil { updateData["started_at"] = time.Now() }
    if err := b.updateRunStatusWithRetry(ctx, runID, updateData); err != nil {
        log.C(ctx).Errorw("could not mark run running after retries; continuing (self-heal via completion/zombie-reset)",
            "run_id", runID, "error", err)
    } else {
        run.Status = model.SopStatusRunning
    }
}
```

**验收**：瞬时失败重试成功用户无感；全失败静默自愈；内存/DB 最终一致。

---

## §7 测试拓扑（Rule 10 / Rule 11）

纯后端 → Go 单测（持久回归保护，无前端/Playwright）。
- **问题 1（Rule 11 复现测试）**：`detachStreamContext`：父取消不传导到子 + 值保留 +
  整体超时生效。首 commit `test(qa): reproduce client-disconnect aborts SOP node generation`（RED）。
- **问题 2**：`sopIdleTimeout/sopOverallTimeout` 默认 + viper override；`idleWatcher`
  用 pipe 验证无数据时 body 被关 + tripped；callVolc 用 `httptest` 验证 idle 超时（短 idle）。
- **问题 4**：`httptest` SSE 含 `[DONE]`，调 callVolc/callAli 断言 handler **不**收到 `done`。
- **问题 5**：store in-memory SQLite 建 node runs+notes+chat，调
  `CleanupDownstreamForRegeneration` 断言下游/笔记/对话删除、upstream 幸存。
- **问题 6**：stub store（前 N 次 UpdateRun 失败后成功 / 恒失败），断言重试且最终一致、
  恒失败不 panic、返回 error。
- **回归**：`go test ./...` 全绿 + `task lint` exit 0 + `task test`（race+coverage）。

## §8 Langfuse / 可观测性
不新增 LLM 调用点。detach 用 `WithoutCancel` 保留 trace 值 → 现有 generation（callAli/volc
:667-680）与 Gateway trace 不受影响。idle/整体超时错误经现有错误路径记录。

## §9 决策记录
- **D1（detach 退款语义）**：client-disconnect 不再触发 `user_cancelled` 退款，改为生成
  完成正常 Reconcile。**这是期望行为**（产品口径：瞬断静默继续）。
- **D2（idle 超时分类）**：idle/整体超时 wrap `context.DeadlineExceeded` →
  classifyReason `provider_timeout`，退款分类正确。
- **D3（问题 6 行为变更）**：fail-fast → silent-retry-continue（产品口径：对用户静默）。
- **D4（EditText 范围）**：EditTextStream 纳入心跳 helper（问题 3）+ done 收敛（问题 4），
  但不做 detach（非节点生成、无退款）。
- **D5（旧 plain reader）**：`executor.ExecuteNodeStream` 非 gateway 读循环实际不触发
  （biz 总传非空 modelKey），但同样加 idle watcher 以一致覆盖。
