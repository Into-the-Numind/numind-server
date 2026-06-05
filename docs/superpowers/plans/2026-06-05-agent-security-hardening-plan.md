# Plan: Agent Mode 安全门禁加固

> Feature id: `agent-security-hardening`. Spec: `docs/superpowers/specs/2026-06-05-agent-security-hardening-design.md`.
> Worktree: `/private/tmp/wt-agent-security-hardening-numind-server`（branch `feature/agent-security-hardening`）。
> 全部在 numind-server 单仓库；**6 task**（T1–T5 编码 + T6 S5 验证策略）。

## Tier 分析（§11）

- 文件重叠：T1 与 T3 都改 `biz.go`（T1 cherry-pick enforce wiring；T3 加 soft_deny config 默认）；T3 依赖 T2 的产物。
- 安全高风险 + 共享 `biz.go`/`adapter` ⇒ **全部串行（Tier 4）**，每 task 完成后**并行双 Sonnet reviewer**（Rule 6）。
- 备注：T2/T4/T5 文件互不相交，理论可 Tier-3 并行，但本 feature 安全敏感 + 主 session 单线，**选串行**以零协调风险换确定性。

## 顺序与依赖

```
T1 (BLK-1 恢复, 基础) → T2 (SoftDenyController) → T3 (软拦截布线, 依赖 T2)
→ T4 (SSRF helper + run_python + web_fetch) → T5 (bashvalidator 语义检查器) → T6 (S5 策略)
```
T1 先行：移除 test.v 依赖后，T2–T5 的 `go test` 经 enforce-default-true 仍跑真 pipeline。

---

## T1 — BLK-1 恢复权限门禁（cherry-pick，保留 Rule 11 链）

**目标**：把 `fix/remove-permission-backdoor` 的 2 commit 落到 feature 分支，enforce 默认 true 全环境真 pipeline。

**涉及文件**：`permission/gate.go`、`permission/gate_test.go`、`biz/biz.go`、`config_dev.yaml`、`config_local.yaml`（cherry-pick 自带）。

**步骤**：
0. **验 SHA 存在（reviewer P1）**：`git fetch origin fix/remove-permission-backdoor 2>/dev/null; git cat-file -t fdcff8d1 && git cat-file -t 27c550cf`——任一缺失即停（防 source 分支被 rebase/force-push 后 silent 错 pick）。
1. 在 worktree：`git cherry-pick fdcff8d1 27c550cf`（test 复现 commit 在前 → fix 在后，保留 Rule 11 链）。
2. **解冲突**：`gate.go`/`gate_test.go`/config 预期干净；`biz.go` 若冲突，手工把 `viper.SetDefault("agent.permission.enforce", true)` + `permission.WithEnforce(viper.GetBool(...))` + wire 后 `log.Infow` 落进当前 `NewPermissionGate(...)` 块（spec §2.3）。
3. **prod-shape 单测加固（reviewer P2，持久化 BLK-1 教训）**：确认 cherry-pick 的 `gate_test.go` 含"默认构造（无 opts、无 test.v 依赖）+ 注入 Deny validator → 实际 Deny"用例，把"非测试构建仍 enforce"钉成持久回归（不靠 test.v）。
4. **验证**：`go test ./internal/numind/biz/permission/...`（含 enforce 三态）全 PASS；`go build ./...`。

**验收**：enforce 三态测试 PASS（默认/WithEnforce(true) honor Deny；WithEnforce(false) force-allow）；`task lint` exit 0；commit log 含 `test(qa): reproduce permission gate force-allows in non-test builds`（Rule 11）。

**原子性**：完成后 gate 即真生效，系统可编译可跑。

---

## T2 — SoftDenyController（纯逻辑，TDD）

**目标**：`agent/soft_deny.go` 实现 spec §3.2 的运行期防呆控制器 + ctx helpers。

**涉及文件**：`internal/numind/biz/agent/soft_deny.go`（新）、`soft_deny_test.go`（新）。

**步骤（TDD）**：
1. **RED**：`soft_deny_test.go` 覆盖：单次 deny→`Resolve` 返回 `(false, 含原因文案)`；同(工具+输入)连击达 `maxSame`→`(true,_)`；任意连击达 `maxTotal`→`(true,_)`；**`lifetimeByFP`：每次拦截间插一次 `OnSuccess` 仍在同指纹累计达 `maxLifetime`→`(true,_)`（R2-B 防绕过）**；`OnSuccess` 清零 consecutive/sameStreak 后重新计数；`enabled=false`→Enabled() false；`SetPending(nil)` 后 `Resolve` 不 panic、回退通用文案；`softDenyToolResult(msg)` 拼出含原因的中文文案。引用未实现符号 → 编译失败=RED。
2. **GREEN**：实现 struct + 方法（mutex 保护；指纹 `toolName+sha1(input)`；文案见 spec §3.5）；`softDenyToolResult(msg string) string` **也在 `soft_deny.go` 内**（可单测，adapter 引用）；`NewSoftDenyController` 在 `enabled=false` 时 `log.Warnw`（R2-E）。

**验收**：`go test ./internal/numind/biz/agent/ -run TestSoftDeny` 全 PASS（含 lifetime 防绕过 + nil-pending 不 panic + softDenyToolResult）；纯逻辑无外部依赖；`task lint` exit 0。

**原子性**：新增 2 文件，编译独立（不被任何调用方引用前可单测）。

---

## T3 — 软拦截布线（adapter + hooks + runner 注入，依赖 T2）

**目标**：把 SoftDenyController 接进 deny 路径，实现"软拦截不中断 + 防呆"。

**涉及文件**：`agent/adapter_full_to_eino.go`、`permission/wrap_hooks.go`、`agent/compliancegate/gate.go`、`agent/runner.go`、`agent/runner_runstream.go`、`biz/biz.go`（soft_deny config 默认）；测试 `adapter_full_to_eino_test.go`（或新增）。

> **T3 前置（reviewer P0/P1）**：`biz.go` 改动**仅限 soft_deny config 默认**——T1 的 enforce wiring 已 commit；先 `git log --oneline -1 -- internal/numind/biz/biz.go | grep -q enforce`-意识确认 T1 已落，T3 的 biz.go diff 只含 soft_deny 行（保持 task delta 最小可独立 review）。

**步骤（TDD）**：
1. **RED**（覆盖 reviewer 三处测试 gap）：
   - **(a) 核心防呆 + registry 卫生**：注入总 deny stub hook + SoftDenyController(enabled,maxSame=3)，断言第 1–2 次 `InvokableRun` 返回 `(softMsg, nil)` **且 `Registry.LastAction()==HookActionContinue`**（R6-A registry 卫生钉死）；第 3 次返回 `("", error)` 且 `Registry.LastAction()==PermissionDeny`；成功调用后 `OnSuccess` 清零。
   - **(b) compliancegate 来源**（reviewer P1）：模拟 compliancegate deny → `SetPending(detail)` 被调 → `Resolve` 返回**非空** msg；另设"未 SetPending"场景断言回退通用文案不 panic（nil-pending）。
   - **(c) runner 未注入退化**（reviewer P2）：构造不注入 `SoftDenyController` 的 ctx → adapter `SoftDenyFromCtx==nil` → 首次 deny 即 `("", error)`（enabled=false 等价退化，钉两路径都必须注入）。
   - `enabled=false` 时首次即 `("", error)`。
2. **GREEN**：
   - `adapter` deny 分支按 spec §3.2(c) **结构性重构**（拆无条件 `Record(action)` 为 per-path，软路径早返回）；成功分支补 `OnSuccess`。
   - `wrap_hooks.go` + `compliancegate/gate.go` deny 分支各补 `SetPending(detail)`。
   - `runner.go` **和** `runner_runstream.go` 都注入 `WithSoftDenyController`（紧挨 `WithPermissionSink`）；两处 `permDenialSink` buffer 1→16。
   - `biz.go` 加 `viper.SetDefault("agent.permission.soft_deny.{enabled,max_same_consecutive,max_total_consecutive,max_lifetime_per_fingerprint}", ...)`。
3. **回归**：跑现有 `TestRunner_Run_RegistryStopPropagatesToTerminalReason` 等——硬停（Stop/BudgetExceeded）仍终止。

**验收**：软 deny 不终止（registry=Continue）、tripped 终止（registry=PermissionDeny）、compliancegate 原因可达、未注入退化、硬停不受影响、`enabled=false` 退回旧行为，全 PASS；`go test ./internal/numind/biz/agent/... ./internal/numind/biz/permission/...` PASS；`task lint` exit 0。

**原子性**：完成后软拦截端到端可用；硬停路径回归绿。

---

## T4 — SSRF 共享 helper + run_python + web_fetch 软化

**目标**：抽 SSRF 守卫为共享，补 run_python，web_fetch SSRF 命中改软 ToolResult。

**涉及文件**：`internal/numind/biz/agent/security/ssrf.go`（新，导出 `ValidateFetchURL`/`CheckIPSafe`）+ `ssrf_test.go`（新）；`agent/tool_web_fetch.go`（私有 `validateFetchURL`/`checkIPSafe` 改为薄委托共享 + SSRF 命中改软）；`agent/tool_run_python.go`（downloadInputFile 加 SSRF + 软）；`tool_run_python_test.go`。

**步骤（TDD）**：
1. **RED**：`ssrf_test.go` 端口 web_fetch 现有 IP 用例（169.254.169.254/loopback/0.0.0.0/`::1`/10.x/172.16.x/192.168.x 拦、8.8.8.8 放）到共享；**行为测试（reviewer P1）**：`tool_web_fetch_test.go` 加"`Execute({"url":"http://169.254.169.254/"})` 返回 `(非空 string, nil)`（软 ToolResult）非 `("", error)`"；`tool_run_python_test.go` 加 downloadInputFile 内网拦（返软）/公网 COS 放/元数据拦。引用未实现共享符号 → RED。
2. **GREEN**：建共享 helper；web_fetch 私有函数委托共享（**保现有 web_fetch 测试不破**）+ SSRF 命中返回软 ToolResult；run_python downloadInputFile **在 presign 替换之前（对入参原始 fileURL，reviewer P2）** 调共享 + 命中返回软 ToolResult。

**验收**：共享 helper 双向测试 + run_python SSRF（校验原始 URL）+ web_fetch 软结果行为 + 回归全 PASS；公网 COS 下载放行用例 PASS（防误伤）；`task lint` exit 0。

**原子性**：SSRF 三处（web_fetch/run_python/共享）一致，编译独立。

---

## T5 — bashvalidator 语义危险检查器（双向表驱动）

**目标**：新增 6 个 validator（spec §4.2 表）+ 注册 `AllValidators()`，**每个双向测试**。

**涉及文件**：`internal/numind/biz/agent/bashvalidator/`（新检查器，可单文件 `semantic_validators.go` 或按 ID 分）；`validator.go`（`AllValidators()` 追加）；`bashvalidator/semantic_validators_test.go`（新）。

**步骤（TDD）**：
1. **RED**：表驱动测试，每个 validator 一组 `{cmd, wantDeny}`——**危险样本 Deny + 正常样本 Allow**：
   - `DestructiveRemove`：`rm -rf /`✓Deny / `rm -rf ~`✓ / `rm -rf $HOME`✓ / `rm -rf /*`✓ ‖ `rm -rf /tmp/x`✗ / `rm -rf ./build`✗ / `rm file`✗（防误伤）。
   - `DiskDestruct`：`mkfs.ext4 /dev/sda`✓ / `dd if=x of=/dev/sda`✓ / `cat>/dev/sda`✓ ‖ `dd if=a of=/tmp/b`✗ / `echo>out.txt`✗。
   - `ForkBomb`：`:(){ :\|:& };:`✓ / `f(){ f\|f& };f`✓ ‖ `f(){ echo hi; }`✗ / `ls\|grep x`✗。
   - `DownloadExec`：`curl u\|sh`✓ / `wget -qO- u\|bash`✓ / `curl u\|base64 -d\|sh`✓ / **`curl u -o /tmp/x && bash /tmp/x`✓（两步式，reviewer P2）** ‖ `curl -o f u`✗ / `wget -O f u`✗。
   - `CredentialFile`：`cat /etc/shadow`✓ / `cat ~/.ssh/id_rsa`✓ / `cat ~/.aws/credentials`✓ / `cat .env`✓ / `source .env`✓ ‖ **`echo "edit your .env file"`✗（R3-C 不误伤）** / `cat .env.example`✗ / `cat .envrc`✗ / `cat config.yaml`✗ / `ls ~/proj`✗。
   - `SSRFLiteral`：`curl http://169.254.169.254/`✓ / `wget http://127.0.0.1:6379`✓ / `curl http://10.0.0.1`✓ / **`curl http://0.0.0.0/`✓ / `curl http://[::1]:8080`✓（reviewer P2）** ‖ `curl https://api.example.com`✗ / `curl https://8.8.8.8`✗。
2. **GREEN**：实现各 validator——`DestructiveRemove` 分段+token 法（非单一大正则；`$VAR` 绕过为文档化 gap，**不**blanket-deny，spec §4.2）；`ForkBomb` **两段式检测**（找 `(){` + 解析体含 `|`&`&`+复述函数名，非脆弱正则，spec §4.2）；`CredentialFile` `.env` **动词门控**（仅文件读取动词上下文，spec §4.2）；`SSRFLiteral` 含 IPv6/0.0.0.0；`AllValidators()` 追加（保持既有 8 个不动）。

**验收**：6 validator 双向用例全 PASS（**Allow 用例是防误伤铁证**）；既有 8 validator 测试不破；`go test ./internal/numind/biz/agent/bashvalidator/...` PASS；`task lint` exit 0。

**原子性**：纯逻辑，新检查器加进切片即生效（双 gate 自动），编译独立。

---

## T6 — S5 验证策略（Rule 10，独立 task，S3 gate reviewer 一并审）

**验证方式**：**Go 单测（持久回归，主）+ dev 真实黑盒（prod-shape）+ Playwright e2e（尽量）**。

**理由**：本 feature 是支付/权限同级的**安全高风险**——Rule 10 要求持久回归，不可只靠一次性 gstack /qa。BLK-1 的核心教训正是"单测在 test.v 下虚假全绿、prod-shape 行为相反"，故**必须 prod-shape 二进制（非 test.v）dev 黑盒**验证门禁真生效。

**dev 夹具（reviewer P1，不留"S5 现场决"）**：dev「从零建 agent」有 422 bug 挡 UI 建 agent → S5 **不走 UI**，用既有 seed agent + 直接 API 触发：复用 `seed_e2e_test_agent`（prod-readiness §6 Wave 0）/ dev 已有 fixtures（parent30=user_moxiaopai、child 600901k/pw moxiaopai；agent 100003/100004）。触发危险命令需 dev `sandbox=docker` + 直接打 agent-runs API 注入危险 bash/python 输入。**前置确认**：S5 第一步先验 dev seed agent 可经 API 跑通一轮（healthz + 一次 run completed），再做安全断言；若 seed 不可用先补 seed（不阻塞——有 API 路径，不依赖 422 UID 修复）。

**S5 需验证的关键路径**：
1. **BLK-1 门禁真生效**（prod-shape，非 test.v）：dev 部署后，配一个会触发 deny 的工具调用（危险 bash），确认被拦（非 force-allow）。
2. **软拦截**：危险工具调用被拦 → SSE/run **不终止**，LLM 收到拦截文案后继续 → 最终 `TerminalCompleted`。
3. **防呆**：构造连续/累计同操作被拦 → 达 maxSame/maxTotal/maxLifetime → run 终止 `permission_denied`（不无限循环、不耗 budget）。
4. **4 类禁令双向**：每类危险样本被拦 + 对应正常样本放行（`rm -rf /tmp`、公网搜索、公网 COS 下载、`echo "...env..."`、读 COS 上传文件）。
5. **正常 agent 回归不被误拦**：公网搜资料 + 生成文档(create_*) + 跑正常 python + 下载 agent 产物，全程无误拦。
6. **硬停回归**：budget 超限/Stop hook 仍正常终止。
7. **既有 8 bash 检查器误伤实测（O1 accept/reject 阈值，reviewer P1）**：跑正常 bash 回归样本 `echo "today $(date)"`/`ls dir/{a,b}`/`for i in {1..3};...`/`RESULT=$(cat f)`/`tar czf o.tgz {a,b}`。**判定门**：若任一被既有检查器拦 → 触发 §2.2/O1 三选一决策（已在设计门禁前置征询用户），不静默放行。

**回归保护诚实声明**：1–7 的核心断言落 **Go 持久回归**（gate/soft_deny/bashvalidator/ssrf 单测 + adapter 集成）；dev 黑盒与 Playwright 为一次性验证。

**reviewer 注意**：选 Go 持久回归而非纯 gstack /qa，符合 Rule 10「权限高风险须 Playwright/Go 持久回归」；prod-shape（非 test.v）dev 黑盒是 BLK-1 教训的不可省项。
