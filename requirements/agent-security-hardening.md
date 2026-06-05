# Agent Mode 安全门禁加固（BLK-1 恢复 + 软拦截不中断 + 平台级安全输入禁令 / BLK-3）

## 来源
- 提出人：产品（agent mode 上线前 prod-readiness 评审，§0.1 红线 BLK-1/BLK-3 + §3.1 安全）
- 提出日期：2026-06-05
- 关联文档：`docs/agent-mode/agent-mode-prod-readiness-test-plan.md`

## 背景：B2B2C 三层与"平台级"定位

- **第一个 B = 平台（跃迁有数）**：设平台级安全硬规则，对**所有机构 / 所有用户**生效。
- **第二个 B = 客户机构**：父账户（`parent_user_id=null`）配自己租户的规则（`agent_permission_config`，按 `parent_user_id` 存）。
- **C = 子账户**：终端使用者。

> **本需求做的是平台级安全（第一个 B）**：硬编码在代码里、对所有租户生效，**绝不写进按 `parent_user_id` 的租户表（`tenant_admin_rule`）**。

## 需求描述（3 个已确认问题）

### 问题 1 — BLK-1：权限门禁被"测试嗅探"后门全局关闭【确认】

`internal/numind/biz/permission/gate.go:110` 的 `PermissionGate.Check` 用 `flag.Lookup("test.v")` 嗅探运行环境：
- **只有跑 `go test` 时**才走真实 7-validator pipeline；
- **dev / prod 真实二进制一律返回 `ForceAllowAllGate` 全放行**（commit `14754a39` "force release all permissions globally"）。

后果：所有权限校验在真实运行时**全失效**——平台硬规则（`PlatformHardRule`）、机构禁区（`TenantAdminRule` L2 黑名单）、`IsDestructive` 拦截、bashvalidator（经管线时）全是死代码。更隐蔽的是：现有 `gate_test.go` 因 `test.v` 存在而走真管线 → **6 个测试全绿 = 虚假信心**。

> **修复已存在**于分支 `fix/remove-permission-backdoor`（2 commit：`test(qa):` 失败复现测试 + fix）。fix 把后门改成 **config 驱动**：`agent.permission.enforce`（默认 `true`，全环境强制真管线；经 `viper.SetDefault` 兜底使 `config_prod.yaml` 无需修改）；仅显式 `enforce=false` 才全局放行（高危逃生舱 + loud-warn + audit 落库）。
> 该分支**落后 develop ~81 commit、未合并**。需 rebase/重新落到当前 develop（`gate.go` 在 81 commit 间可能动过 → 解决冲突）、跑 `biz/permission` 测试，再纳入本 feature。

### 问题 2 — 拦截体验太硬：命中即终止整个 run【确认】

命中权限 deny → `HookActionPermissionDeny`（`hooks.go:19`）→ `TerminalPermissionDenied`（`state.go:20`）→ **整个 agent run 终止**。对用户而言：一个被安全策略挡下的工具调用，会让整段对话直接失败。

### 问题 3 — BLK-3：危险命令刹车太弱【高】

`internal/numind/biz/agent/bashvalidator/validator.go` 的 8 个检查器**只防混淆字符、不防语义危险命令**。实测探针：`rm -rf /`、`curl|sh`、fork 炸弹 `:(){ :|:& };:`、`base64 -d|sh` 全部放行。`tool_run_python.go` 的 Python 源码零命令校验。一旦 prod 开 sandbox（rollout Phase E2 明确要开），等于任意命令执行。

## 业务目标（产品已拍板）

1. **恢复门禁**：合并 BLK-1 fix，`enforce` 默认开，全环境跑真实 pipeline。
2. **不禁用任何整工具**：`tool_blacklist` 留空，所有工具对 agent 开放（安全靠规则精准拦，不靠砍工具）。
3. **拦截改"软拦截不中断"**：命中规则时**只挡这一次工具调用**，给 LLM 返回一条"此操作被安全策略拦截"的工具结果，**让 ReAct 循环继续、大模型自己决定下一步**（换方式 / 道歉 / 问用户），**不终止整个 run**。**必须加防呆**：防止 LLM 反复试同一个被拦工具导致死循环（同一工具 + 同类输入连续 N 次拦截就终止 run，且拦截文案明确告知"此类操作被禁止，请勿重试，改用其它方式"）。
4. **加平台级安全输入禁令（第一批 1–4）**，平台级、对所有机构/用户生效：
   - ① **内网 / 云元数据地址（SSRF）**：`169.254.169.254`、`127.0.0.1` / `localhost`、私网段 `10.x` / `192.168.x` / `172.16~31.x`。作用于 **web_fetch（非沙箱）+ run_python 下载 input_file + bash 的 curl/wget**。
   - ② **毁灭性命令（bash）**：`rm -rf /`、`rm -rf ~` / `$HOME` / `/*`、`mkfs`、`dd ... of=/dev/...`、fork 炸弹 `:(){ :|:& };:`、`> /dev/sd*`。
   - ③ **下载即执行（bash）**：`curl/wget ... | sh/bash`、`... | base64 -d | sh`。
   - ④ **读凭据/密钥文件**：`/etc/shadow`、`~/.ssh/`、`~/.aws/credentials`、`.env`、`/proc/<pid>/environ`。作用于 **bash + file_read**。
   - （第二批可选，本任务先不做：反弹 shell `nc -e`、`bash -i >& /dev/tcp/...`。）

满足上线 Gate（§6）：BLK-1 / BLK-3 从红线转为已关闭；compliance/permission 决策"真实生效"得到部分兑现。

## ⚠️ 最重要的约束：别误伤正常功能（产品方明确担心）

- **沙箱前提**：`bash_exec` / `run_python` 仅在 `EnableSandbox` 开启、沙箱内运行（prod 当前沙箱关，降级报错）。②③④-bash 类**只在沙箱场景生效**；①SSRF 还作用于 web_fetch（非沙箱）。沙箱**外**操作（用户上传/下载产物、看图表文档、联网搜公开网站）不受这些禁令影响。
- **规则必须精准，只锁危险形态，严禁误伤正常用法：**
  - `rm` 只禁 `rm -rf /`、`rm -rf ~`/`$HOME`/`/*` 等**毁灭形态**，**绝不能禁所有 `rm`**（脚本删临时文件正常）。
  - SSRF 只禁**内网/元数据地址**，**不能禁所有联网**（搜公开网站、下载公开 COS 文件正常）。
  - 凭据文件只禁那几个**服务器敏感路径**，**不影响 file_read 读用户上传文件**（file_read 走 COS URL 不是服务器路径）。
  - 用户下载 agent 生成产物（create_csv/html/png、run_python 输出 → COS）**完全不受影响**。
- **验收必须证明双向**：正常 agent（搜资料、生成文档、跑正常 python、删临时文件）不被误拦；危险输入被拦且 LLM 能继续（软拦截）。

## 优先级
**高**（go-live blocker：BLK-1 是线上正在生效的安全后门；BLK-3 在 prod 开 sandbox 后即被"武装"）

## Triage
- 推荐轨道：**Standard**
- 分类理由（5 条标准）：
  1. 数据库 schema 变更：**否**（纯代码 + config 文档化，不动表）
  2. 新增 API 端点：**否**
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（gate.go / biz.go / 新平台 input-deny validator / bashvalidator / adapter_full_to_eino.go / tool_run_python.go / 可能 tool_file_read.go + 大量测试）
  5. 高风险业务逻辑（支付/**权限**/会员）：**是**（恢复安全门禁 + 新增安全策略 + 改 run 终止语义）
- 人类决定：**Standard**（任务交付说明已预判：安全高风险 + 跨多文件；与 triage 一致，不可降级）

## 范围边界（待 S1/S2 细化，此处先框定）

**必做（in scope）**
- BLK-1 门禁恢复：把 `fix/remove-permission-backdoor` 的 2 commit（含 `test(qa):` 复现测试）落到当前 develop，解决 `gate.go` 冲突，`enforce` 默认 true。
- 软拦截不中断：改 `adapter_full_to_eino.go` 的 PreToolCall deny 分支——`HookActionPermissionDeny` 改为返回一条工具结果消息喂回 LLM 并继续循环，而非 terminal；评估 `hooks.go` HookAction 语义 + `state.go` `permission_denied` 终态是否保留（可能仅"硬停"策略才用）。**加循环防呆**。
- 平台级安全禁令 1–4：平台级 input-deny validator（扩 `platform_hard_rule.go` 或新建）；bash 命令类（②③）强化 `bashvalidator`（顺带补 BLK-3）；SSRF（①）复用 web_fetch 已有 SSRF 防护 + 补 `run_python` downloadInputFile；凭据文件（④）作用于 bash + file_read。
- Rule 11：保留 BLK-1 fix 分支已有的 `test(qa):` 复现测试链。
- Rule 10（安全高风险）：S5 强验证——门禁恢复后真管线生效 / 软拦截只挡单次工具调用且 run 继续 / 防呆 / 1–4 各禁令"命中危险 + 放行正常用法"双向用例 / 正常助手回归不被误拦；尽量 Playwright e2e。

**不做（out of scope）**
- 不禁用任何整工具（`tool_blacklist` 留空）。
- 第二批输入禁令（反弹 shell）本任务不做。
- 沙箱本身加固（seccomp default-deny、去 `docker.sock`、沙箱逃逸面）= prod-readiness 其它红线，非本任务。
- compliance L0/输出闸接线（ENG-SEC-4）= 独立任务。
- **禁碰 prod**：验证只在 dev。

## 验证目标环境
- **dev**（真实后端 9091 + dev MySQL + sandbox=docker），**禁碰 prod**（CLAUDE.md 硬规则）。
- 安全红线必须用 **prod-shape 二进制**（非 `test.v`）验证——这正是 BLK-1 的核心教训。
