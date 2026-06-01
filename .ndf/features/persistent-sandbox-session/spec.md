# Spec: persistent-sandbox-session

> S2 工件 · 2026-06-01 · 推荐方案 B 的精确技术设计 + 多租户威胁分析
> **本 spec 是 go/no-go 决策文档。S2 后停下，等用户拍板是否进 S3/S4 编码。**

## 0. 决策回顾（来自 S1）

- **方案 B**：容器按 run_id 停泊（park）复用，空闲 TTL + 最大存活上限 + 终态立即释放。
- **一个 agent_run 一个容器**，bash_exec 和 run_python 共享同一容器（停泊 key = `run_id`）。
- **硬不变量**：一容器只服务一个 run_id，绝不跨 run/用户共享。
- feature flag 灰度，flag off = 现状无状态。

## 1. 数据结构

### 1.1 Session 扩展（pool.go）
```go
type Session struct {
    ContainerID string
    ImageTag    string
    Config      SpawnConfig
    BorrowedAt  time.Time
    SpawnedAt   time.Time   // 新增：用于最大存活上限判定
    runID       uint64      // 新增：停泊归属（0 = 未绑定，走旧无状态路径）
    lastUsedAt  time.Time   // 新增：用于空闲 TTL 判定
    mu          sync.Mutex
    returned    bool
}
```

### 1.2 池子新增停泊表（pool.go）
```go
type agentSandboxPool struct {
    // ... 现有 warm chan *Session, spawnReq, dc, cfg, logger ...
    parkedMu sync.Mutex
    parked   map[uint64]*Session   // 新增：key=run_id，停泊中的容器
}
```
> **设计要点（S1 reviewer P1）**：`parked` 是 **mutex 保护的 map**，与无锁 `warm chan` **并存**，不是改 channel。warm = 未分配的预热容器；parked = 已绑 run、等复用的容器。两者独立。

## 2. 生命周期状态机

```
       ┌─────────────────────────────────────────────────────────┐
       │                                                         │
   [warm pool] ──Borrow(runID)──> [in-use bound to runID]        │
       ▲                                  │                       │
       │                          Return(runID)                   │
       │                                  ▼                       │
       │                          ┌─ flag on ─> [parked@runID] ──┤
       │                          │                  │            │
       │                          │           同 run 下次 Borrow ─┘ (复用，状态保留)
       │                          │                  │
       │                          │            空闲 > TTL  ─┐
       │                          │            存活 > MaxLife ┤──> [Destroy] ──spawn replacement──> warm
       │                          │            run 终态     ─┘
       │                          └─ flag off ─> [Destroy] ──> warm   (现状行为)
       │                                                              │
       └──────────────────────────────────────────────────────────────┘
```

## 3. Pool 改造（pool.go）

### 3.1 Borrow 增加 runID 参数 + 复用分支
```go
// 接口签名变更：Borrow(ctx) → Borrow(ctx, runID)。runID=0 表示无状态调用（flag off 或非 agent 场景）。
func (p *agentSandboxPool) Borrow(ctx context.Context, runID uint64) (*Session, error) {
    if runID != 0 {  // stateful 路径
        p.parkedMu.Lock()
        if sess, ok := p.parked[runID]; ok {
            delete(p.parked, runID)
            p.parkedMu.Unlock()
            if p.isAlive(sess.ContainerID) {           // 复用前校验存活
                sess.BorrowedAt = time.Now()
                sess.lastUsedAt = time.Now()
                return sess, nil                        // ★ 复用，/workdir 状态保留
            }
            p.discardDead(sess)                         // 死了就丢，往下借新的
        } else {
            p.parkedMu.Unlock()
        }
    }
    // 走现有 warm channel 借用逻辑（不变），借到后 sess.runID = runID
    sess := <-p.warm ... ; sess.runID = runID ; sess.SpawnedAt 已在 spawn 时设
    return sess, nil
}
```

### 3.2 Return 改为"停泊或销毁"
```go
func (p *agentSandboxPool) Return(sess *Session, exitCode int, errMsg string) error {
    // double-return 守卫不变
    if sess.runID != 0 && p.persistentEnabled && exitCode == 0 && !overMaxLifetime(sess) {
        sess.lastUsedAt = time.Now()
        p.parkedMu.Lock(); p.parked[sess.runID] = sess; p.parkedMu.Unlock()
        return nil   // ★ 不销毁，停泊待复用
    }
    // 否则走现有 Destroy + spawn-replacement（失败的、超寿的、无状态的、flag off 的）
    return p.destroyAndReplace(sess, exitCode, errMsg)
}
```

### 3.3 新增空闲 reaper goroutine + 最大存活
```go
// 每 30s 扫 parked，销毁 (now-lastUsedAt > IdleTTL) 或 (now-SpawnedAt > MaxLifetime) 的容器。
func (p *agentSandboxPool) parkedReaper() {
    tick := time.NewTicker(30 * time.Second)
    for range tick.C {
        p.parkedMu.Lock()
        for runID, sess := range p.parked {
            if idleExpired(sess) || overMaxLifetime(sess) {
                delete(p.parked, runID)
                go p.destroyAndReplace(sess, -1, "idle/maxlife reap")
            }
        }
        p.parkedMu.Unlock()
    }
}
```

### 3.4 终态立即释放（新接口方法）
```go
// ReleaseRun 在 agent_run 达终态时由 runner 调用，立刻销毁该 run 停泊的容器（不等 TTL）。
func (p *agentSandboxPool) ReleaseRun(runID uint64) {
    p.parkedMu.Lock(); sess, ok := p.parked[runID]; delete(p.parked, runID); p.parkedMu.Unlock()
    if ok { go p.destroyAndReplace(sess, 0, "run terminal") }
}
```

## 4. Hook 层改造（factory_sandbox_hooks.go）

- **borrow key 从 `(runID, toolName)` 改为 `runID`**：一个 run 一条 borrow 记录，bash_exec/run_python 复用。
- `preToolCall`：若 `borrows[runID]` 已有存活容器 → 复用（不再 Borrow）；否则 `pool.Borrow(ctx, runID)`。
- `postToolCall`：**flag on 时不 Return**（容器留给本 run 后续调用；由 ReleaseRun/reaper 回收）。flag off 时维持现状 per-call Return。
- `SandboxSessionFor(runID, toolName)` → `SandboxSessionFor(runID)`（工具名不再是 key）。

## 5. Runner 终态钩子（runner.go）

- 在 run 写终态（`UpdateState(..., "terminated", ...)`）的**所有路径**后调 `pool.ReleaseRun(runID)`（completed / 各 error / aborted / context_exhausted / 短路路径）。
- **不依赖此钩子兜底**：即使漏调（panic/crash），空闲 TTL + 最大存活是**独立**兜底。钩子只是"尽快释放"的优化。

## 6. 多租户威胁分析（S1 P0 要求 — 决策关键）

### 6.1 不变量（必须 100% 守住）
- **跨 run/跨用户隔离**：`parked` key=run_id，复用前校验 `sess.runID == 请求 runID`。一容器物理上只跑过一个 run 的代码。这条破了 = 数据泄漏 = review FAIL 红线。
- 容器仍 network-restricted（`NetworkPolicy`）、资源限额（mem/cpu/pids）、`/workdir` tmpfs 有 size 上限。这些不变。

### 6.2 新增面：容器寿命秒级 → 对话级
| 面 | 分析 | 结论 |
|---|---|---|
| 攻击窗口变长 | 注入代码可"call A 写 payload → call B 触发"。但 payload 只能影响**同一个 run/用户自己**的后续调用，碰不到别人。 | 最大存活上限（30min）封顶绝对窗口；同 run 自己坑自己不构成多租户风险 |
| call A 的 input_files 留到 call B（S1 P0） | call A 下载的敏感文档在 call B 仍在 `/workdir/input/`。同 user/run 可读自己的东西，**不越权**。 | **决定：每次 run_python 调用前清空 `/workdir/input/`**（只清 input，不清 output——output 是续写的本钱）。理由：input 是"本次调用的参数"，不该泄漏到下次；output 是"积累的产物"，要保留。这是个干净的边界。 |
| 资源被长期占用 | 恶意用户故意开很多对话各占容器。 | 容器硬上限 max_containers + 空闲 TTL 快速回收 + 单用户并发对话本就有上层限制 |
| 容器逃逸的横向影响变大 | 若有 0-day 逃逸，长活容器给攻击者更多时间。 | 这是既有风险，不因本 feature 质变（逃逸了秒级也够）；但 prod 启用前的安全评审要复核 docker 隔离配置 |

### 6.3 结论
- **多租户隔离不被本 feature 削弱**（容器仍 per-run）。
- 新增的是 **intra-run（同用户自己）** 的状态可见性——这是 feature 的目的，通过"每调用清 /workdir/input、保留 /workdir/output"把"参数泄漏"和"产物积累"分开。
- 绝对窗口由最大存活上限封顶。
- **prod 启用前需独立安全评审**（不在本 feature；本 feature 只让 dev 架构就绪）。

## 7. reapOrphans / 重启策略（S1 P1）

**决定：接受"重启 = 所有停泊会话被驱逐"作为文档化不变量。**
- `reapOrphans` 维持现状（按 `numind.sandbox=1` label 清所有遗留容器，含停泊的）。
- 不引入独立 label——重启罕见，丢的只是进行中对话的 `/workdir`，下次调用拿新容器（功能降级到无状态那一次，LLM 可感知重试）。
- 简单 > 完美。S3 plan 里这条作为已知行为写进测试 AC-6b。

## 8. 池满降级策略（S1 PRD AC-8）

- max_containers 上限内：按需 spawn。
- 超上限：`Borrow(ctx, runID)` 等 `PoolMaxWaitMs`（现 30s）拿不到 → **降级为该次调用无状态**（借不到就这次不停泊、用完即毁），**run 不失败**，只是这一刻失去续写能力。LLM 行为退回"一次做完"。
- 不做复杂排队，避免引入新死锁面。

## 9. Feature flag + config + guidance

- `sandbox.persistent_session: false`（默认）→ 完全现状无状态。
- config 新增：`idle_ttl_seconds`(120) / `max_lifetime_seconds`(1800) / `max_containers`(20)。
- guidance 反转（flag on 时）：`tool_run_python.go` Description + `output_tools_priority_prompt.go` 把"STATELESS / 一次做完 / NEVER reopen" 换成"本对话内 /workdir 持久，可分步搭建、可重开 output 续写"。**flag 联动**（同一个 flag 既切池子行为又切提示文本），不能错位。

## 10. DB 决定

- `agent_sandbox_session` 审计表**已存在**且够用（记录每次容器生命周期）。
- **决定：不加新表、不加 schema 变更**。停泊/复用是**进程内内存状态**（`parked` map），不需持久化——重启即驱逐（§7）与"不持久化停泊态"一致。
- 审计行可选增强：复用时不新建行、只更新 `last_used`（可选，S3 决定，非必须）。
- → **本 feature 无 migration**。（这也把 triage 里"可能 DB"收敛为"否"。）

## 11. 测试计划

| 测试 | 覆盖 |
|---|---|
| `TestPool_Stateful_ReuseSameRun` | 同 runID 两次 Borrow 拿到同容器，/workdir 文件保留 |
| `TestPool_Stateful_CrossRunIsolation` | 两个 runID 并发，各自独立容器，互不可见（AC-5 红线） |
| `TestPool_Stateful_IdleReap` | mock 时钟，空闲 > TTL 被销毁（AC-3） |
| `TestPool_Stateful_MaxLifetime` | mock 时钟，存活 > MaxLife 强制销毁（AC-4） |
| `TestPool_Stateful_ReleaseRunOnTerminal` | ReleaseRun 立即销毁（AC-2） |
| `TestPool_FlagOff_Regression` | flag off：连两次调用，第二次新容器看不到第一次的文件（AC-6 负断言） |
| `TestPool_PoolFull_Degrades` | 压满池子，超限调用降级无状态、run 不失败（AC-8） |
| `TestHook_RunScopedBorrow` | bash_exec + run_python 同 run 共享一个容器（key=runID） |
| `TestPool_InputCleanedOutputKept` | 每次调用清 /workdir/input、保留 /workdir/output（§6.2 决定） |
| `TestRestart_ParkedEvicted` | 重启驱逐停泊容器、不残留（AC-6b） |

## 12. 实施分期（若 go）

- **Wave 1**：pool.go 停泊/复用/reaper/最大存活 + config + flag（核心，flag 默认 off，零行为变化）
- **Wave 2**：hook 层 run-scoped 借用 + runner 终态钩子 + /workdir/input 清理
- **Wave 3**：guidance 反转（flag 联动）+ 全测试 + dev flag on 实机验收（gstack /qa 跑"80 页分步 PPT"）
- 每 wave 两阶段并行 review（NDF Rule 6）。

## 13. 留给用户的 go/no-go 判断点

1. **值不值得现在做**：当前创可贴覆盖 99% 文档；prod 沙箱未启用。这是"消除天花板 + 架构对齐"的投资，18-26h。
2. **风险可接受度**：多租户安全面（§6）——隔离不削弱，但容器寿命变长 + intra-run 状态可见，prod 启用前要独立安全评审。dev 先行风险低。
3. **subagent 依赖**：本设计 subagent-forward（key 可扩 subcontext），但 subagent 本身是另一个大 initiative。
4. **替代**：不做也合理（S1 §9），把本评估存档作未来现成方案。

**S2 完，停。等用户决定。**
