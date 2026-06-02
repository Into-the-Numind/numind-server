# Proposal: persistent-sandbox-session

> S1 工件 · 2026-06-01 · 设计评估（2-3 方案 + 推荐 + 风险 + 工量 + 容量）

## 1. 问题一句话

当前沙箱"每次工具调用一个全新容器、用完即毁"，导致 LLM 无法在一次对话内分步搭建大文档（第二次调用时第一次的文件没了）；要让 `/workdir` 在**同一个 agent_run 内跨调用持久**。

## 2. 设计轴：容器"什么时候释放"

所有方案的差异本质是一根轴——容器在哪个时机销毁：

```
现状(无状态) ────────────── A(最强持久) ────────────── B(持久+空闲驱逐)
每次调用后立刻毁              整段对话持有，终态才毁          活跃期持有，空闲超时则毁 + 终态毁
容量最省                    容量最重                        容量自愈
LLM 无法续写                LLM 可续写                       LLM 可续写（活跃期内）
```

## 3. 三个候选方案

### 方案 A — 严格 per-run 绑定（整段对话独占一个容器）
- 一个 run 第一次调沙箱工具 → 借一个容器、绑定 run_id。
- 该 run 之后所有沙箱调用复用**同一容器**，`/workdir` 全程保留。
- run 达终态（completed/error/aborted）才释放销毁。
- **问题**：容器被**整段对话独占**，包括用户在那儿慢慢看、慢慢想的空闲时间。`pool_min=5` → 最多 **5 个并发活跃对话**能用沙箱。容量模型崩坏，必须大改池子。

### 方案 B — 持久 + 空闲驱逐（**推荐**）
- 容器仍按调用借用，但 `Return` 时**不销毁**，而是按 run_id **停泊（park）**并打一个空闲 TTL（如 120s）。
- 同一 run 下次调用 → 若停泊的容器还活着就**复用**（状态保留）；否则借新的。
- 后台 reaper 把空闲超过 TTL 的停泊容器销毁。
- run 达终态 → **立刻**释放销毁（不等 TTL）。
- 再加一道**最大存活上限**（如 30 min）：无论活不活跃，单容器活过上限强制销毁（安全/资源兜底）。
- **优点**：LLM 需要的持久性拿到了（一次生成流程里两次调用通常隔几秒，远小于 TTL）；容量**自愈**——对话一安静，容器就被回收，不会被空闲对话长期占着。

### 方案 C — 混合（A 的语义 + B 的纪律）
- 本质是 B。把"per-run 绑定"说成强语义、"空闲 TTL + 最大存活"说成纪律。与 B 收敛，不单列。

## 4. 推荐：方案 B

**理由：**
1. **拿到持久性、又不崩容量**：活跃生成流程里调用间隔是秒级，TTL=120s 足够覆盖；对话空闲就回收，容量按"任意 2 分钟窗口内**正在执行**沙箱代码的对话数"算，而不是"总对话数"。
2. **多租户安全可控**：最大存活上限把单容器寿命**封顶**（从"秒级"变"≤30min"，而非"无限对话级"），攻击/泄漏窗口有界。
3. **降级路径自然**：池子满了（活跃对话 > 容器上限）→ 上限内按需多 spawn；超上限 → 退回"每调用借新容器"的**无状态降级**（功能不挂，只是那一刻失去续写能力）或短暂排队。
4. **改动可控（但不轻，S1 reviewer P1 修正）**：复用现有 Borrow/Return/reapOrphans **骨架**，但**不是"只加个停泊表"那么轻**——现池子是无锁 channel 设计（`warm chan *Session`，无 run_id 概念），停泊需要一张 **mutex 保护的 `parked map[runID]*Session`** 与 channel 并存（不是原地扩展 channel），外加空闲 reaper goroutine + 最大存活 ticker + Borrow 路径分支。准确说法是"**对现有骨架的显著结构性新增**"——不是推倒重写，但比"加张表"重，pool.go 若同期被别的改动碰到有 merge 冲突风险。工量按此上修（见 §8）。
5. **subagent-forward**（S0 P1 要求验证）：停泊表的 key 现在是 run_id；未来 subagent 落地时，每个 sub-context 有自己的 id，key 扩成 `(run_id, subcontext_id)`。**诚实说**（S1 reviewer P2）：这"结构上可延展但非平凡"——一个 subcontext 一个容器意味着**一个 run 多个容器**，容量账要按 subcontext 单独算，且 subagent 之间默认**不共享 workdir**（要共享需另设协议）。S1 只确认"不是死路"，真正的 subagent 容量模型留给那个独立 initiative。

**为什么不选 A**：容量崩坏是硬伤（5 容器=5 并发活跃对话），且容器被空闲对话长期独占，安全/资源面比 B 差。A 的唯一好处是"持久性绝对"——**用户交互式使用时中途走开 >TTL 再回来，B 会丢容器、A 不会**（S1 reviewer P2）。但对"自动文档生成"这个目标场景（调用间隔秒级），这个差别价值为零。

## 4b. 一个 run 用几个容器？（S1 reviewer P2，S1 就定）

**决定：一个 agent_run 共享一个容器，bash_exec 和 run_python 都用它。**
- 停泊表 key = **`run_id`**（不是现状的 `(run_id, tool_name)`）。
- 理由：持久化的全部价值就是让 LLM 在 `/workdir` 里**攒状态**——它可能先 `run_python` 生成数据、再 `bash_exec` 处理、再 `run_python` 读回。若每个工具各占一个容器，`/workdir` 不互通，持久化就废了一半。
- 影响：`factory_sandbox_hooks.go` 现在按 `(runID, toolName)` 存 borrow，需改为按 `runID` 存（一个 run 一条），bash_exec/run_python 复用同一条。容量账按"一个活跃 run 一个容器"算，不是"一个活跃 run × 工具数"。

## 5. 关键参数（S2 定稿，这里给初值供评估）

| 参数 | 初值 | 作用 |
|---|---|---|
| 空闲 TTL | 120s | 两次沙箱调用间隔超过即回收停泊容器 |
| 最大存活上限 | 30 min | 单容器绝对寿命封顶（安全/资源兜底） |
| 池子 PoolMin（warm） | 5（dev 现状） | 预热容器数 |
| 容器硬上限 max_containers | 待定（如 20） | 按需 spawn 的天花板，防 docker 被打爆 |
| 池满降级策略 | 无状态降级 or 短排队 | 活跃对话 > 上限时的行为，S2 定 |
| feature flag | `sandbox.persistent_session` | stateful 新行为 ↔ 现状无状态 可切换灰度 |

## 6. 风险表

| 风险 | 严重度 | 缓解 |
|---|---|---|
| **资源泄漏**（容器永不释放） | 高 | 三道保险：空闲 TTL reaper + 最大存活上限 + run 终态释放钩子；启动 `reapOrphans` 已有 |
| **池子耗尽**（活跃对话 > 容器上限） | 中 | 上限内按需 spawn；超上限走降级策略（无状态 fallback / 排队），不让 run 直接失败 |
| **跨 run 隔离被破坏** | 高（但易守） | **硬不变量：一容器只服务一个 run_id，绝不共享**。停泊表 key=run_id，复用前校验 run_id 匹配 + 容器存活。这条是 review FAIL 红线 |
| **多租户安全面扩大**（容器寿命秒级→对话级，注入代码可分两步 stage payload） | 高 | 最大存活上限封顶窗口；容器仍 network-restricted + 资源限额 + 只跑一个 run 的代码；S2 出专门威胁分析；prod 启用前单独安全评审 |
| **同 run 内跨调用的输入/状态面扩大**（S1 reviewer **P0**）：持久容器里，call A 下载的 input_files、写的中间文件，对 call B 的 LLM 生成代码**可见**。 | 中（需命名，非越权） | **这是本 feature 的预期行为**（同一 user/run，攒状态正是目的），**不是跨 run 越权**。但要在 S2 威胁模型里**显式分析**：(a) call A 下载的敏感文档，call B 即使意图无关也能读到——同 user 可接受但要 framing；(b) call A 写的 payload 留到 call B 执行——被注入时的 intra-run 提权面。S2 给出"是否需要按调用清理 /workdir/input、还是接受同 run 全程可见"的结论。本行作用是**把它从"默认隐含"提成"已命名风险"**。 |
| **服务重启把停泊容器当 orphan 全清**（S1 reviewer **P1**）：`reapOrphans` 启动时按 `SandboxContainerLabel` 找遗留容器销毁，但停泊容器和 warm 容器**同一个 label**，重启会把所有活跃 run 的 workdir 状态一起清掉。 | 中 | S2 三选一：(a) 停泊容器打**独立 label**，reapOrphans 跳过；(b) 接受"**重启 = 所有持久会话被驱逐**"作为文档化不变量（鉴于 TTL 模型，这其实可接受——重启本就罕见，丢的只是进行中的 workdir，LLM 下次调用拿新容器）。**倾向 (b)**，但 S2 明确写死。 |
| **终态检测漏掉**（run 异常未达终态 → 容器漏释放） | 中 | 不依赖终态钩子兜底——空闲 TTL + 最大存活上限是**独立**的兜底，钩子只是"尽快释放"的优化 |
| **guidance 冲突**（"一次做完"提示与新"可分步"行为矛盾） | 低 | flag on 时反转 `tool_run_python.go` + `output_tools_priority_prompt.go` 的 guidance；flag off 保持现状提示。两者随 flag 联动 |
| **tmpfs 累积撑爆**（一段对话写很多文件） | 低 | 现有 `WorkdirSizeMB` 上限即覆盖（tmpfs size 限制）；超限 python 写盘自然报错，LLM 可感知 |
| **subagent 未来要重设计** | 中 | key 设计预留 `(run_id, subcontext_id)` 扩展位；若 S2 评估无法兼顾，显式记录"接受重设计风险"（S0 P1 要求） |

## 7. 容量影响（重要）

- **prod 现状**：`skills_enabled=false`，**沙箱是 dev-only**。所以**当前没有 prod 容量压力**，升级窗口宽松。
- **dev**：`pool_min=5` 够 dev 测试。
- **prod 未来启用沙箱时**：容量按"峰值并发活跃沙箱对话数"设计。方案 B 的空闲驱逐让这个数远小于"总在线对话数"。具体 max_containers 与降级策略在 prod 启用前据真实并发再调——本 feature 只让架构就绪，不预设 prod 数字。

## 8. 工量估算

| 模块 | 工量 |
|---|---|
| `pool.go` mutex parked-map + 空闲 reaper goroutine + 复用逻辑 + 最大存活 ticker + Borrow 分支（S1 reviewer P2 上修：结构性新增比"加表"重） | **6-9h** |
| `factory_sandbox_hooks.go` run-scoped 借用（key 从 `(runID,tool)` 改 `runID`、借一次复用 + 终态释放） | 3-4h |
| `runner.go` 终态释放钩子 | 1-2h |
| config 新参数 + feature flag | 1-2h |
| guidance 反转（flag 联动） | 1h |
| 安全威胁分析文档（S2 产出） | 2h |
| 测试（隔离/复用/驱逐/降级/重启/flag off 回归） | 4-6h |
| **总** | **18-26h**（真 Standard；S2 精确设计后再校准） |

## 9. 替代：维持现状（不做）

也是合理选项：当前创可贴覆盖 99% 文档场景，prod 还没启用沙箱。**若用户判断"超大文档"场景近期不会真实发生，可以不做**，把本评估存档作为未来触发时的现成方案。本 proposal 已把这个"不做"也列为正式选项。

## 10. 下一步

S2 细化推荐方案 B 的精确技术设计（停泊表数据结构、复用校验逻辑、终态钩子接入点、降级策略、DB 是否加字段、**多租户威胁分析**、feature flag 灰度）。S2 后**停下**，等用户 go/no-go。
