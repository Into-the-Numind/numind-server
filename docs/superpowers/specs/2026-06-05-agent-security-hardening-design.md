# Spec: Agent Mode 安全门禁加固（BLK-1 + 软拦截 + 平台级安全输入禁令）

> 精简 Standard（S1 提案 + S2 设计合并）。Feature id: `agent-security-hardening`。
> 需求卡：`requirements/agent-security-hardening.md`。计划：`docs/superpowers/plans/2026-06-05-agent-security-hardening-plan.md`。
> 基线：develop HEAD `dca51300`（worktree `feature/agent-security-hardening`）。
> 调研依据：3 个 read-only Explore agent 的代码勘察（file:line 已交叉核实，见各节）。

---

## 1. 问题与目标（S1 提案精简）

Agent mode 上线前 prod-readiness 评审（`docs/agent-mode/agent-mode-prod-readiness-test-plan.md` §0.1）确认两条安全红线 + 一项体验问题，产品已拍板一并修：

| # | 问题 | 现状（第一手核实） | 目标 |
|---|------|------|------|
| **BLK-1** | 权限门禁被环境嗅探后门全局关闭 | `permission/gate.go:110` `if flag.Lookup("test.v")!=nil { 真 pipeline } else { ForceAllowAllGate }`（commit `14754a39`）⇒ dev/prod 全放行；单测因 test.v 走真管线虚假全绿 | enforce 默认开，全环境跑真 7-validator pipeline |
| **拦截太硬** | 命中 deny 即终止整 run | `HookActionPermissionDeny`(hooks.go:19) → 经 runner override → `TerminalPermissionDenied`(state.go) → run 终止 | 软拦截：只挡这一次工具调用，喂回 LLM，ReAct 继续；防呆防死循环 |
| **BLK-3** | 危险命令刹车太弱 | `bashvalidator` 8 检查器只防混淆字符；`rm -rf /`/`curl\|sh`/fork bomb 全放行；`run_python` downloadInputFile 无 SSRF | 平台级安全输入禁令第一批 1–4，精准只锁危险形态 |

**B2B2C 定位（关键）**：第一个 B = **平台（跃迁有数）**，本 feature 做的全是**平台级**规则——硬编码在代码里、对所有机构/用户生效，**绝不写进按 `parent_user_id` 的租户表（`tenant_admin_rule` / `agent_permission_config`）**。第二个 B（客户机构）的租户规则、C（子账户）不在本 feature 范围。

**复用判断（可行性）**：
- BLK-1 修复**已存在**于 `fix/remove-permission-backdoor`（2 commit：`test(qa):` 复现 + fix），落后 develop ~81 commit 未合并 → 重落即可。`gate.go` 在 develop 上**与 fix 分支基线逐字相同**（后门仍在 106–121），cherry-pick `gate.go` 干净；`biz.go` 因 billing 改动需 3-way 微调（见 §2.3）。
- SSRF 防护**已有产线级实现** `validateFetchURL`+`checkIPSafe`（`tool_web_fetch.go:278–356`）可复用，`run_python` 缺。
- bash 语义检查直接扩 `bashvalidator.AllValidators()`，自动经 `PlatformHardRule`（权限管线）+ `tool_bash_exec.Execute` 双 gate 生效。
- **无新表、无新端点、无新外部服务**。工作量：M 级（多文件但每块独立）。

**优先级**：高（go-live blocker）。

---

## 2. BLK-1：恢复权限门禁（enforce 配置驱动）

### 2.1 修复语义（沿用 fix 分支 H1-D1/D2/D3）

删除 `gate.go::Check` 的 `flag.Lookup("test.v")` 环境嗅探分支；全局 override 改由显式 config `agent.permission.enforce` 控制：

```go
// gate.go — PermissionGate 加字段 + WithEnforce option（默认 true，zero-value-safe）
type PermissionGate struct { pipeline *PermissionPipeline; enforce bool; /* ... */ }
func WithEnforce(enforce bool) Option { return func(g *PermissionGate) { g.enforce = enforce } }
func NewPermissionGate(opts ...Option) *PermissionGate {
    g := &PermissionGate{ chanSize: 1024, closeCh: make(chan struct{}), enforce: true /* 默认 enforce */ }
    /* apply opts ... */
    if !g.enforce { log.Warnw("PermissionGate: global enforcement DISABLED ... force-allowed. Unsafe; controlled debugging only.") }
    /* ... */
}
func (g *PermissionGate) Check(ctx, req) PermissionResult {
    if g.enforce { result = g.pipeline.Check(ctx, req) }       // 所有环境跑真 pipeline
    else { result = PermissionResult{Behavior: BehaviorAllow, ValidatorID: "ForceAllowAllGate", ...} } // 显式逃生舱
    /* audit 不变 */
}
```

- `biz.go`：`viper.SetDefault("agent.permission.enforce", true)` + `permission.WithEnforce(viper.GetBool("agent.permission.enforce"))` + wire 后 `log.Infow("agent permission gate wired", "enforce", ...)`。
- **prod 安全**：`config_prod.yaml` 禁止修改（项目硬规则）；靠 `viper.SetDefault` 默认 true 兜底，prod 自动 enforce。
- `config_dev.yaml` / `config_local.yaml` 文档化该 key（标注默认 enforce）。
- `enforce=false` 是**高危逃生舱**：构造时 loud-warn + 每次放行经 audit 落库可追溯。

### 2.2 ⚠️ 副作用必须正视：enforce=true 激活已有 8 个 bash 检查器

`bashvalidator` 现有检查器**已相当激进**：`CommandSubstitution` 拒任何 `$(`/反引号/`<(`/`>(`；`IFS` 拒 `${IFS}`/`$'...'`；`BraceExpansion` 拒 `{a,b}`/`{1..9}`/嵌套花括号。BLK-1 之前这些从未在 prod 跑过（被 force-allow），**enforce=true 后会真实拦截依赖命令替换/花括号展开的正常 bash**。

这是**既有基线行为**（这 8 个检查器是 #6 设计的 P0 安全检查），不在本 feature 的"新规则"范围。但两个 reviewer 均把它标 P1：`CommandSubstitution` 拒**所有** `$(...)`、`BraceExpansion` 拒 `{a,b}`/`{1..9}` 是**极常见的正常 bash**，enforce=true 上线即激活，若现有 agent 任务用到这些会被拦。

**缓解（软拦截）**：软拦截（§3）让这些既有检查器的 deny 不再终止 run，LLM 收到"命令被拦截"后可改写命令——把"硬失败"降为"一次重试"，大幅降低体验冲击。但 reviewer 正确指出软拦截不能完全消除问题（LLM 可能不会改写、文案可能困惑、浪费 turn）。

**S5 硬门禁（reviewer P1，本 feature 必须有 accept/reject 阈值，不能只"观测"）**：
- S5 在 dev prod-shape 实测一组**正常 bash 回归样本**（至少：`echo "today $(date)"`、`ls dir/{a,b}`、`for i in {1..3}; do ...`、`RESULT=$(cat f)`、`tar czf o.tgz {a,b}`）。
- **判定**：若既有 8 检查器拦下其中**任何**正常样本 → **触发决策门**（不静默放行）：在以下三选一，由用户拍板（已在 S2 设计门禁前置征询，见 §8 开放项 O1）：
  - (A) 接受 + 仅靠软拦截缓解（LLM 改写）+ 记 follow-up 放宽；
  - (B) 本 PR 内把 `CommandSubstitution`/`BraceExpansion` 改"warn-only"或放宽（扩本 feature 范围）；
  - (C) 本 PR 内对这两个检查器加白名单/降级。
- **默认倾向**：(A)——本 feature 聚焦"恢复门禁 + 软拦截 + 新平台禁令"，既有检查器调优是独立关注点；软拦截正是为此设计的缓冲。但**最终由用户在设计门禁选定**。

### 2.3 cherry-pick 冲突预案

S4 在 feature worktree 跑 `git cherry-pick <test commit> <fix commit>`（保留 Rule 11 链）：
- `gate.go`：干净（基线逐字相同）。
- `gate_test.go`：干净（新增测试函数，无冲突）。
- `config_dev.yaml` / `config_local.yaml`：大概率干净（agent 块尾部追加）。
- `biz.go`：**可能冲突**——fix 在 `NewPermissionGate(...)` 块插入 `viper.SetDefault`+`WithEnforce`+`log.Infow`；develop 上该块（biz.go:314–319）被 billing/narration 改动包围。冲突时手工把 3 行落进当前 `NewPermissionGate` 调用。冲突解完跑 `go test ./internal/numind/biz/permission/...` 验证。

---

## 3. 软拦截不中断（soft interception）

### 3.1 现状控制流（Explore agent 1 核实）

```
PreToolCall 返回 HookActionPermissionDeny
  → adapter_full_to_eino.go:104  Registry.Record(deny)
  → adapter_full_to_eino.go:108  emitNarration(StateRejected) "操作被拦截"
  → adapter_full_to_eino.go:109  return "", error("tool execution stopped by hook")
  → Eino 捕获 error → runner.go override(1234-1240) / runner_runstream applyHookOverride(598-609)
       读 Registry.LastAction()==PermissionDeny → HookActionToLoopEvent → Transition → TerminalPermissionDenied
  → 整 run 终止
```

权限 deny 的"原因"已有传播通道：permission hook 在 deny 时把 `*PermissionDenialDetail{ToolName,Behavior,DecisionReason,ValidatorID,Message}` 送进 `PermissionSinkFromCtx(ctx)`（`wrap_hooks.go:44–59`），runner 末尾非阻塞收进 `RunResult.PermissionDenial`。`Message` 即人类可读原因（bash 拦截原因 / 平台规则 message）。

### 3.2 软拦截设计

新增**运行期 `SoftDenyController`**（per-run，经 ctx 注入，与 `permDenialSink` 并列），承载：拦截原因 + 防呆计数。介入点最小化：

**(a) 新文件 `internal/numind/biz/agent/soft_deny.go`**
```go
type SoftDenyController struct {
    mu           sync.Mutex
    enabled      bool
    maxSame      int            // 同一(工具+输入)连续拦截上限（默认 3）
    maxTotal     int            // 任意连续拦截上限，进展即清零（默认 5）
    maxLifetime  int            // 同指纹「整 run 累计」拦截上限，成功不清零（默认 10，防 R2-B 绕过）
    pending      *PermissionDenialDetail // permission/compliance hook 在 deny 时写入；adapter 读
    consecutive  int            // 全局连续拦截数（成功即清零）
    lastFP       string         // 上次拦截指纹 toolName+sha1(input)
    sameStreak   int            // 同指纹连续连击（成功清零）
    lifetimeByFP map[string]int // 同指纹整 run 累计拦截（永不清零）
}
// NewSoftDenyController(cfg) — enabled=false 时构造即 log.Warnw（仿 enforce=false loud-warn，R2-E）
// SetPending(detail)  — permission/compliance hook 在 deny 时调（喂原因）
// Resolve(tool, input) (tripped bool, msg string) — adapter 在 deny 时调：累计三类计数 + 取 pending.Message 拼 LLM 文案；
//      sameStreak>=maxSame 或 consecutive>=maxTotal 或 lifetimeByFP[fp]>=maxLifetime ⇒ tripped=true（硬终止）
// OnSuccess() — adapter 在工具成功后调：consecutive=0, sameStreak=0（agent 有进展即清零）；lifetimeByFP 不动
// softDenyToolResult(msg) string — 同文件导出，拼 §3.5 文案（在 soft_deny.go 内可单测；adapter 引用）
// ctx helpers: WithSoftDenyController / SoftDenyFromCtx（仿 permission_sink.go）
```
- `enabled`/`maxSame`/`maxTotal`/`maxLifetime` 来自 config（§3.4）。`enabled=false` ⇒ 退回硬终止（旧行为，安全阀）。
- **R2-B 防绕过（reviewer P1）**：仅 `consecutive`+`sameStreak`（成功清零）会被"每次拦截间插一次无关成功"绕过 → 加 `lifetimeByFP`（同指纹整 run 累计、**成功不清零**），`maxLifetime=10` 兜底，杜绝"插成功无限重试同一被禁操作"。

**(b) permission hook 喂原因**（`wrap_hooks.go` deny 分支，紧挨现有 sink send）：
```go
if c := agent.SoftDenyFromCtx(ctx); c != nil { c.SetPending(detail) }
```
（compliance gate 的 deny 分支同样补一行，统一覆盖所有"被 hook 拦"的原因来源。）

**(c) adapter 改造**（`adapter_full_to_eino.go::InvokableRun`，deny 分支）：

> **⚠️ P0 结构性要求（reviewer 6-A/1-A）**：现行 `adapter:103-104` 是**无条件** `Registry.Record(action)`，紧跟 `adapter:106` 的 `if action != Continue` 短路。软拦截改造**必须把无条件 Record(action) 拆成 per-path**——软路径 `Record(Continue)`、硬路径 `Record(action)`——并让软路径**早返回**，绝不可"在 line 104 之后再插软检查"（那样 registry 末值仍是 PermissionDeny，run 完成时被 `applyHookOverride` 误升 terminal）。注意 `wrap_hooks.go:42` 在返回前已先 `Record(PermissionDeny)` 到**同一** registry，软路径的 `Record(Continue)` 必须是**该次工具调用的最后一次 registry 写**。

```go
action, err := a.hooks.PreToolCall(ctx, a, args)
if err != nil { return "", fmt.Errorf("PreToolCall: %w", err) }
// 软拦截分支：必须在「无条件 Record(action)」之前，且软路径早返回。
if action == HookActionPermissionDeny {
    if c := SoftDenyFromCtx(ctx); c != nil && c.Enabled() {
        tripped, msg := c.Resolve(a.ft.Name(), args)
        if !tripped {
            if a.hooks.Registry != nil { a.hooks.Registry.Record(HookActionContinue) } // 末次写：覆盖 wrap_hooks 的 PermissionDeny
            a.emitNarration(ctx, narration.StateRejected, input, nil, nil, msg)         // reason 填实（adapter:201 预留位）
            return softDenyToolResult(msg), nil  // (result string, nil error) → Eino 包成 tool message → 循环继续
        }
        // tripped：连续/累计拦截超限 → 落到下方硬终止（记 PermissionDeny + return error → 复用既有 override）
    }
}
if a.hooks.Registry != nil { a.hooks.Registry.Record(action) } // 仅非软路径到达（硬停 + tripped 的 PermissionDeny）
if action != HookActionContinue {                              // Stop/BlockingStop/BudgetExceeded + tripped
    a.emitNarration(ctx, narration.StateRejected, input, nil, nil, "")
    return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
}
```
- 工具**成功**分支（`adapter:184-187` 的 StateResult）补 `if c := SoftDenyFromCtx(ctx); c != nil { c.OnSuccess() }`。
- **registry 卫生**：软拦截路径 `Record(Continue)` 是正确性关键——`applyHookOverride` 只在 run 末尾读 `LastAction`（核实 runner_runstream.go:544/580），不清就会把已软处理的 deny 在 run 完成时误升 `TerminalPermissionDenied`。**T3 必须有针对性测试断言软 deny 后 `Registry.LastAction()==Continue`**（见 plan T3）。

**(d) runner 注入**（`runner.go` 与 `runner_runstream.go`，紧挨 `WithPermissionSink`，**两条路径都要**）：
```go
softDeny := NewSoftDenyController(cfg)  // cfg 从 viper 读
ctx = WithSoftDenyController(ctx, softDeny)
```
- **R2-D**：缺任一路径注入 → adapter 的 `SoftDenyFromCtx(ctx)==nil` → 静默退回硬终止（旧行为）。T3 集成测试需覆盖"runner 未注入 ⇒ 走 enabled=false 退化路径"以钉住两路径都注入。
- **sink 容量（reviewer P2 SINK-CAPACITY）**：`permDenialSink` 现 buffer=1，软拦截下一 run 多次 deny 只第一条进 `RunResult.PermissionDenial`（后续 `default:` 丢弃 + warn）。把两处 `make(chan *PermissionDenialDetail, 1)` 提到 `, 16`，让一 run 的多次拦截详情不被吞（不影响硬停语义）。

### 3.3 行为矩阵

| 场景 | 结果 |
|------|------|
| 单次工具被拦（enabled） | 软：LLM 收到"被安全策略拦截：<原因>。请勿重试同类操作，改用其它方式或向用户说明"。ReAct 继续，最终 `TerminalCompleted` |
| 拦截后 LLM 换合规方式成功 | `consecutive` 清零；run 正常完成 |
| LLM 死磕同一被禁(工具+输入) ≥ maxSame(3) 次连续 | tripped → 硬终止 `TerminalPermissionDenied`（防死循环/budget 泄漏） |
| 任意被禁操作连击无进展 ≥ maxTotal(5) 次连续 | tripped → 硬终止 `TerminalPermissionDenied` |
| 同一被禁(工具+输入)整 run 累计 ≥ maxLifetime(10) 次（即便每次插一个无关成功） | tripped → 硬终止（R2-B 防绕过） |
| 硬停 hook（Stop/BlockingStop/BudgetExceeded） | **不变**：仍立即终止（软拦截只作用于 PermissionDeny） |
| `enabled=false` | 退回旧行为：任何 deny 立即 `TerminalPermissionDenied` |

> `consecutive`/`sameStreak` 成功即清零 ⇒ 健康 agent 偶尔撞一次墙再正常干活永不 trip；只有真·死循环（连续拦截无进展）或同一被禁操作整 run 累计 10 次（绕过成功清零）才 trip。wall_time(900s)+MaxTurns 仍是终极兜底。

### 3.4 防呆参数（config）

`agent.permission.soft_deny`：`enabled`(默认 true) / `max_same_consecutive`(默认 3) / `max_total_consecutive`(默认 5) / `max_lifetime_per_fingerprint`(默认 10)。`viper.SetDefault` 兜底，prod 无需改 config_prod。dev/local 文档化。`NewSoftDenyController` 构造时若 `enabled=false` → `log.Warnw` loud-warn（R2-E，仿 enforce 逃生舱），使误配可观测。

### 3.5 LLM 文案（model-facing，中文，仿 bashFriendlyError 风格）

```
⚠️ 该工具调用被平台安全策略拦截，未执行。
原因：<PermissionDenialDetail.Message>
请不要以相同或同类方式重试此操作。你可以：换一种不触发安全策略的方式完成任务，或向用户说明该操作受限。
```
- `tripped` 前的最后一次（sameStreak≥2）追加更强语气："你已多次尝试此被禁操作（已拦截 N 次），请立即停止重试。"
- 用户侧 narration 仍是"操作被拦截"（`aiservice_fallback.go:93`，可被 `rejected_template` 自定义），`reason` 现填实。

---

## 4. 平台级安全输入禁令（第一批 1–4）

**架构定位**：SSRF（①）落**工具层**（fetch 时要做 DNS 解析，天然属工具）；bash 类（②③④ + ①-bash-literal）落 **`bashvalidator`**（经 `PlatformHardRule` 权限管线 + `tool_bash_exec.Execute` 双 gate 自动生效）。**不新建 validator 类、不写租户表**。bash 类经权限管线 deny ⇒ 自动享受 §3 软拦截。

### 4.1 ① SSRF（内网/云元数据）

- **web_fetch**：`validateFetchURL`+`checkIPSafe`（已拦 169.254.169.254 / loopback / RFC1918 私网 / link-local / `.local`）**已在 Execute 启用**——本 feature **不改逻辑**，只补回归测试 + 把 SSRF 命中的返回从 Go error 改为**软 ToolResult**（与 §3 一致，LLM 收到文案继续，不终止）。
- **run_python**：抽 `validateFetchURL`/`checkIPSafe` 为共享 helper（`agent/security/ssrf.go` 或 `agent` 包内导出），在 `tool_run_python.go::downloadInputFile` 调用；命中 → 软 ToolResult。**校验对象=入参原始 `fileURL`（reviewer P2，presign 之前 ~396 行）**——若先 `extractObjectKeyFromURL`+`GenerateSignedURL` 再校验，攻击者可用"长得像 COS key 但指向内网"的 URL 绕过；故 SSRF 校验必须先于 presign 替换。
- **bash 的 curl/wget**：bash 沙箱 `--network=none`（§1.3，无网络）是**主控**；`bashvalidator` 加文本检查（curl/wget 命中内网 IP 字面量）作**纵深**（捕获不到 DNS-名解析内网，由网络隔离兜）。

**不误伤**：只拦内网/元数据 IP；公网站点、公网 COS 下载、公网 API 全放行（`net.IP.IsPrivate()`/`IsLoopback()` 精准，公网 IP 返回 nil）。

### 4.2 ②③④ + ①-bash-literal：bashvalidator 新增语义检查器

每个新 validator 实现 `bashvalidator.Validator`（`ID()`+`Validate(cmd) Result`），加进 `AllValidators()`（first-deny-wins）。**所有 pattern 经双向表驱动测试：危险→Deny + 正常→Allow。**

| ID | 拦（危险形态） | 放（正常用法，必测 Allow） |
|----|--------------|--------------------------|
| `DestructiveRemove` | `rm` 带递归+强制(`-rf`/`-fr`/`-r -f`/`--recursive --force`) **且** 目标是根/家目录：`/`、`/*`、`~`、`~/`、`$HOME`、`${HOME}`、`$HOME/*`、`/.` | `rm -rf /tmp/x`、`rm -rf ./build`、`rm file`、`rm -r node_modules`、`rm -rf $TMPDIR/x`（非根目标全放） |
| `DiskDestruct` | `mkfs`(任意)、`dd ... of=/dev/...`、重定向 `> /dev/sd[a-z]`/`/dev/nvme*`/`/dev/vd*`/`/dev/hd*` | `dd if=a of=/tmp/b`、`echo x > out.txt` |
| `ForkBomb` | 自引用函数管道到后台：`:(){ :\|:& };:` 及 `f(){ f\|f& };f` 等形态（**两段式检测**，见下） | 普通函数定义 `f(){ echo hi; }`、普通管道 `ls\|grep x` |
| `DownloadExec` | (a) 管道直送：`curl/wget ... \| (sudo )?(ba\|z\|da)?sh`/`\| base64 -d ... \| sh`/`\| python -`；(b) **两步式（reviewer P2）**：`curl/wget ... -o/-O <f>` 同串后接 `&&`/`;` + `sh/bash/python <f>` | `curl -o f.json url`、`wget -O- url > f`（不进 shell 全放） |
| `CredentialFile` | `/etc/shadow`、`/etc/gshadow`、`/etc/sudoers`；`(/root\|/home/<u>\|~\|$HOME)/.ssh`；`.aws/credentials`；`/proc/<pid>/environ`(与既有 `ProcEnviron` 部分重叠，保留更广覆盖)；**`.env` 仅当紧跟文件读取命令动词时**（见下） | `cat config.yaml`、`cat .env.example`、`echo "edit your .env file"`(R3-C 不误伤)、`ls ~/myproj`、读用户自建文件 |
| `SSRFLiteral` | curl/wget 命中内网/元数据 IP 字面量：`169.254.169.254`/`127.0.0.1`/`localhost`/`0.0.0.0`/`[::1]`/`[fe80:`/`10.x`/`192.168.x`/`172.(16-31).x` | curl/wget 公网域名或公网 IP（含 `8.8.8.8`） |

**精准设计要点（防误伤 / 反绕过，均经 reviewer 加固）**：
- `DestructiveRemove` **不用单一大正则**——按 `;`/`&&`/`\|\|`/`\|`/换行切段，每段 tokenize：首 token 为 `rm`、flag 集合含 r 且含 f、且存在一个**整词等于**危险目标的 arg 才 Deny。`rm -rf /tmp` 的 `/tmp` 不等于 `/` ⇒ 放行。
  - **已知 gap（R3-D，文档化不修）**：`rm -rf $VAR`（`$VAR` 运行时展开为 `/`）文本层判不出。**不**blanket-deny 裸变量 `rm -rf $VAR`——那会误伤 `rm -rf $TMPDIR/build` 等正常脚本（违反"严禁误伤"）。缓解：bash 沙箱**临时、`--network=none`、用后即焚**，`rm` 只毁沙箱自身 workdir 非宿主；此检查本就是纵深。诚实标注。
- `ForkBomb` **两段式检测**避免单正则歧义（reviewer P2 FORKBOMB-REGEX-CLARITY，Go RE2 无 `\|` 元义）：检测"存在 `<name>()` 函数定义" **且** 其体内同时含 `|` 管道与 `&` 后台 **且** 体内复述函数名（含 `:` 特例）。用代码逻辑（找 `(){` + 解析体）而非脆弱大正则。
- `.env` 误伤风险最高（R3-C：`echo "...用 .env 文件"` 会被裸 `.env` pattern 误拦）→ **动词门控**：仅当 `.env` 紧跟文件读取/打开命令动词（`cat`/`source`/`.`/`less`/`more`/`head`/`tail`/`vi`/`vim`/`nano`/`cp`/`mv`/`scp`/`base64`/`xxd`/`od`/`strings`/`grep`/`awk`/`sed` + 路径上下文）时才 Deny；裸文本/echo/printf 中的 `.env` 放行。**不**匹配 `.env.example`/`.env.sample`/`.envrc`。沙箱内 `.env` 价值有限（无真服务器 secret），属纵深；若 S5 仍发现误伤即进一步收窄或降级。
- `SSRFLiteral` 补 IPv6 loopback `[::1]`、link-local `[fe80:`、unspecified `0.0.0.0`（reviewer P2 SSRF-IPV6/UNSPECIFIED）——与工具层 `checkIPSafe` 的 `IsLoopback`/`IsUnspecified`/`IsLinkLocalUnicast` 对齐。
- 凭据/SSRF-bash 检查在**沙箱内**（bash `--network=none` + 容器自带 /etc/shadow 非宿主）安全价值偏纵深；dev 挂 `docker.sock` 场景下价值更实（§1.5）。文案诚实标注。

### 4.3 ④ file_read：结构上已安全，不改（Explore agent 3 核实）

`file_read` 输入是 **COS URL**（正则强制 `/agent-(attachments\|outputs)/<userID>/`，`tool_file_read.go`），结构上**不可能**接受 `/etc/shadow` 等服务器路径，且带 owner 校验。给 file_read 加路径禁令是死代码 ⇒ **不改**。这是对任务原始设想（"④作用于 bash + file_read"）的实证修正（S0 已与用户沟通）。

---

## 5. 不变量与不破坏（硬约束自检）

- **不新增任何 enum 值**：复用 `TerminalPermissionDenied`（仅防呆 trip 时到达）、不加 HookAction(I6)/TerminalReason(I2)/LoopEvent(I7)。`SoftDenyController` 是 struct 非 enum。
- **system prompt 6 段(I3)/aiservice 唯一入口(I5)**：不触碰。
- **三层架构**：新逻辑在 biz/agent + biz/permission；controller/store 不动。
- **不禁用任何整工具**：`tool_blacklist` 留空。
- **不写租户表**：平台规则全硬编码在 bashvalidator/工具层，`agent_permission_config` 零写入。
- **config_prod.yaml 不改**：全靠 `viper.SetDefault`。

---

## 6. 不误伤总论证（产品方核心关切）

| 担心 | 为何不会误伤 |
|------|------------|
| 禁了所有 `rm` | `DestructiveRemove` 只匹配 递归+强制+根/家目标；`rm -rf ./build`/`rm file` 放行（必测 Allow 用例） |
| 禁了所有联网 | SSRF 只拦内网/元数据 IP（`IsPrivate`/`IsLoopback`）；公网搜索、公网 COS 下载放行 |
| 影响 file_read 读上传文件 | file_read=COS-URL-only，禁令不碰它；只 bash 路径检查服务器敏感路径 |
| 用户下载 agent 产物 | create_csv/html/png、run_python 输出 → COS，全程不经任何新禁令 |
| 软拦截把正常 run 拦死 | 软拦截只挡单次工具调用，run 继续；防呆只在"连续拦截无进展"才 trip；成功即清零 |
| enforce 激活既有 8 检查器误伤 bash | §2.2 记录为既有基线，S5 实测观测；软拦截缓解（不终止 run，LLM 可改写） |

**S5 验收必须双向证明**：正常 agent（公网搜资料、生成文档、跑正常 python、`rm` 临时文件、读 COS 上传）不被误拦；危险输入（4 类各样本）被拦且 LLM 能继续（软拦截）。

---

## 7. 验证策略（Rule 10，安全高风险 → 持久回归）

- **Go 单测（持久回归，主）**：
  - BLK-1：cherry-pick 的 `gate_test.go`（enforce 三态）+ prod-shape 断言（无 test.v 时仍 enforce——通过 default-true 构造覆盖）。
  - 软拦截：`SoftDenyController` 单测（软/tripped-same/tripped-total/成功清零/enabled=false）；adapter 集成（deny→tool-result+nil+registry=Continue / tripped→error+registry=PermissionDeny）。
  - bashvalidator：6 个新 validator 各**双向**表驱动（危险 Deny + 正常 Allow），含全部"不误伤"用例。
  - SSRF：共享 helper 单测（内网拦/公网放/元数据拦）+ run_python downloadInputFile SSRF + web_fetch 回归。
- **dev 真实黑盒（prod-shape 二进制，禁碰 prod）**：部署 dev（sandbox=docker），seed agent，构造危险命令场景实测软拦截 + 4 类禁令 + 正常 agent 回归。
- **Playwright e2e（尽量）**：若 dev agent 链路可走通，固化"危险命令被软拦 + run 继续"为持久 e2e；dev「从零创建 agent」422 bug 可能挡 UI 建 agent → 退而用 seed agent + API（S5 决）。

---

## 8. 风险与开放项

**开放项（需用户在设计门禁拍板）：**
- **O1 — enforce 激活既有 8 bash 检查器的误伤处理策略**（§2.2，两 reviewer P1）：上线 enforce=true 会激活 `CommandSubstitution`(拒所有 `$()`)/`BraceExpansion`(拒 `{a,b}`) 等既有 P0 检查器，可能拦正常 bash。默认倾向 (A) 接受 + 软拦截缓解 + S5 实测阈值 + follow-up 放宽；但 (B) 本 PR 内放宽 / (C) 加白名单 由用户选。**这是设计门禁的核心待决项。**

**已纳入 reviewer 修正的风险（P0/P1 已固化进设计）：**
1. **R6-A/P0 adapter registry 重构**（§3.2c）：无条件 `Record(action)` 必须拆 per-path，软路径 `Record(Continue)` 早返回——否则软 deny 末值仍 PermissionDeny，run 完成被误升 terminal。已显式写入 spec + T3 强制测试。
2. **R2-B/P1 防呆绕过**（§3.2a）：加 `lifetimeByFP`（成功不清零，maxLifetime=10）杜绝"插成功无限重试同一被禁操作"。
3. **R3-C/P1 `.env` 误伤**（§4.2）：动词门控，echo/裸文本中的 `.env` 放行。
4. **R3-D/P1 `rm -rf $VAR` 绕过**（§4.2）：文档化为沙箱临时性兜底的已知 gap，**不**blanket-deny（避免误伤正常变量 rm）。

**其它风险：**
5. **bash curl/wget SSRF** 文本检查捕获不到 DNS-名解析内网——由沙箱 `--network=none` 兜底（主控），文本检查仅纵深。
6. **软拦截 + 流式路径**：`applyHookOverride` 只读末尾 LastAction，registry 卫生（Record Continue）是正确性关键，T3 必须有针对性测试（软 deny 后 `LastAction()==Continue` + run 仍 Completed）。
7. **cherry-pick biz.go 冲突**（§2.3）——手工解 + 跑 permission 测试验证；T1 先 `git cat-file -t` 验 SHA 存在。
8. **CLAUDE.md §6b enum 计数过时**（reviewer P2）：文档称 TerminalReason/LoopEvent 各 19，实际 state.go 14/21。本 feature **行为上不加任何 enum 值**（这才是 invariant 实质）；计数订正留作独立 doc follow-up，不在本 PR 动 L0 文件。

---

## 9. 文件改动清单（S4 落地）

| 文件 | 改动 | 类别 |
|------|------|------|
| `permission/gate.go` | cherry-pick：去 test.v、加 enforce+WithEnforce | BLK-1 |
| `permission/gate_test.go` | cherry-pick：enforce 三态回归（Rule 11 链） | BLK-1 |
| `biz/biz.go` | cherry-pick：viper.SetDefault+WithEnforce+log（解冲突）；+ soft_deny config 默认 | BLK-1/软拦截 |
| `config_dev.yaml`/`config_local.yaml` | cherry-pick：enforce 文档化 + soft_deny 文档化 | BLK-1/软拦截 |
| `agent/soft_deny.go`(新)+`_test.go` | SoftDenyController + ctx helpers | 软拦截 |
| `agent/adapter_full_to_eino.go` | deny 分支软拦截 + 成功 OnSuccess | 软拦截 |
| `permission/wrap_hooks.go` | deny 分支 SetPending(detail) | 软拦截 |
| `agent/compliancegate/gate.go` | deny 分支 SetPending(detail) | 软拦截 |
| `agent/runner.go`/`runner_runstream.go` | 注入 SoftDenyController | 软拦截 |
| `agent/security/ssrf.go`(新)+`_test.go` | 抽 validateFetchURL/checkIPSafe 共享 | SSRF |
| `agent/tool_web_fetch.go` | 调用共享 helper + SSRF 命中改软 ToolResult | SSRF |
| `agent/tool_run_python.go` | downloadInputFile 加 SSRF + 软 ToolResult | SSRF |
| `agent/bashvalidator/*.go`(新检查器)+`_test.go` | 6 新 validator + AllValidators 注册 + 双向测试 | BLK-3 |
