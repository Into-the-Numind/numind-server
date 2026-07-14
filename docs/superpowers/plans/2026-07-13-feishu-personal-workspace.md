# 飞书个人工作空间连接 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让每个有数账号拥有独立、可长期保持的飞书个人工作空间连接，并让现有 Agent 在真实业务命令缺少连接或精确权限时引导用户授权、随后自动恢复原操作，首版支持 Docs、Base、Wiki 的创建、读取和更新。

**Architecture:** 现有 Agent 通过 `lark_skill_read` 读取固定版本官方技能，通过受控 `lark_execute` 提交结构化 argv；后端以命令目录、加密 vault、持久化 operation、DB lease 和确定性授权状态机执行。已连接热路径直接执行真实命令，不预跑 `auth status`；授权恢复只重放数据库中已加密保存的原 operation，并把结果回填原 tool call。

**Tech Stack:** Go 1.24、Gin、GORM、MySQL 8、AES-256-GCM、CloudWeGo Eino、lark-cli 1.0.68、Vue 3.4、TypeScript 5.4、Pinia、Vitest、Playwright。

**Spec:** `docs/superpowers/specs/2026-07-13-feishu-personal-workspace-design.md`

---

## 一期交付边界（2026-07-14 经用户确认）

本计划原本把核心闭环、生产增强、历史清理和完整验收合并为一个 23-task 的首发版本。现拆为两期；**本 feature 的 S4/S5 只以一期范围作为完成标准**，二期必须作为新的 Standard feature 重新立项，不得静默遗漏。

### 一期：可用且安全的个人飞书工作空间闭环

一期必须交付：每个有数账号独立、加密保存的个人自建应用连接；受控的 lark-cli Docs/Base/Wiki 创建、读取、更新；业务操作优先、仅在确定的未连接/缺 scope/授权失效时发起配置；用户完成飞书授权后自动继续**原始** Agent tool call；最小连接状态/解绑入口；必要的后端集成、前端单元与关键 Playwright E2E，以及本地真实账号 Gate。

纳入一期的 S4 tasks：**1–13、16–19、21–23**；Task 24 是一期的 S5 本地真实飞书 Gate。Task 22 在一期只覆盖关键授权/恢复 happy path、过期刷新、无普通 answer 回退及 mobile/desktop 冒烟；其完整状态矩阵、键盘细节和视觉回归扩展留到二期。

一期不可降级的安全承诺：不共用用户凭据；不执行 IM/删除/raw API/shell；不泄露 argv、token 或完整授权 URL；不把授权完成伪造成用户消息；不会因为重复 resume 重复写业务资源；用户删除会话后不得继续执行飞书写操作。Task 11 的恢复、删除与租约修复属于这些承诺，不能后置。

### 二期：增强与遗留收口

二期单独立项并覆盖：Task 14 的旧明文 HOME 迁移、Task 15 的完整 trace/metric、Task 20 的遗留文件彻底删除，以及 Task 22 的完整前端 E2E/视觉与键盘矩阵。例外：若一期发布前探测到仍在使用的旧明文 HOME，Task 14 自动升级为一期发布阻塞项；若旧实现仍进入 production graph 或暴露 IM/broad auth，必须在一期通过禁用/隔离证明，否则不得发布。

---

## 0. 实施边界与工作方式

- 后端只在 `/private/tmp/wt-feishu-personal-workspace-numind-server` 修改，分支 `feature/feishu-personal-workspace`。
- 前端只在 `/private/tmp/wt-feishu-personal-workspace-numind-web-v3` 修改，分支 `feature/feishu-personal-workspace`。
- 两个仓库分别提交；不要把前后端文件放入同一个 Git commit。
- 继续复用 `features.feishu_integration.enabled` 作为灰度开关；不新增第二个同义 feature flag。
- 不修改 `config_prod.yaml`，不把密钥、Token、device code、完整授权 URL、App Secret 或测试账号写入代码和测试快照。
- 旧 `feishu-integration` worktree 只作历史参考，不从它合并代码；本 worktree 中已经存在的飞书代码必须通过测试驱动逐步替换。
- 首版不注册 `lark_send_message`，不允许 IM、删除、权限管理、任意 `api`、`auth`、`config` 或 shell。
- 每个实现任务先红测、再最小实现、再绿测、再 commit。后端每次修改后至少跑相关 `go test`；Task 23 统一跑 `task lint`。前端每次修改后至少跑相关 Vitest；Task 23 统一跑 `npm run lint && npm run type-check`。

## 1. 文件结构与职责

### numind-server 新建

| 路径 | 单一职责 |
|---|---|
| `migrations/20260713_130000_feishu_personal_workspace.sql` | 扩展连接表并创建 vault、auth session、operation 三张表 |
| `migrations/20260713_130000_feishu_personal_workspace_rollback.sql` | 仅供本地未投产回滚验证；生产回滚保留新数据表 |
| `internal/pkg/model/feishu_workspace.go` | 三张新表的 GORM model、状态常量、TableName |
| `internal/numind/store/feishu_workspace.go` | vault CAS、session lease、operation 幂等/lease/状态转换的数据访问 |
| `internal/numind/store/feishu_workspace_test.go` | store 归属、代际、CAS、lease、幂等事务测试 |
| `internal/numind/biz/feishu/vault.go` | HOME 打包、AAD 加密、临时解封、权限和清理 |
| `internal/numind/biz/feishu/vault_test.go` | 跨用户解密失败、路径穿越、权限、CAS、清理测试 |
| `internal/numind/biz/feishu/controlled_runner.go` | 固定版本、无 shell、限时限长、JSON envelope 的新 CLI 运行器 |
| `internal/numind/biz/feishu/controlled_runner_test.go` | fake binary contract tests |
| `internal/numind/biz/feishu/command_catalog.go` | Docs/Base/Wiki 命令、exact scopes、风险和 argv 规则 |
| `internal/numind/biz/feishu/command_catalog_test.go` | 允许项与永久拒绝项完整测试 |
| `internal/numind/biz/feishu/error_classifier.go` | 固定版本结构化错误到恢复动作的 fail-closed 分类 |
| `internal/numind/biz/feishu/error_classifier_test.go` | spike fixture 和未知写结果测试 |
| `internal/numind/biz/feishu/operation_service.go` | operation 创建、执行、租约、幂等、精确重放和结果清理 |
| `internal/numind/biz/feishu/operation_service_test.go` | 热路径、重放、并发、代际、未知写结果测试 |
| `internal/numind/biz/feishu/auth_session_service.go` | create_app/app_scope/user_auth worker 与 session 生命周期 |
| `internal/numind/biz/feishu/auth_session_service_test.go` | 链接、租约、重启、循环和 exact scope 测试 |
| `internal/numind/biz/feishu/skill_reader.go` | 固定四个官方技能的分页读取和签名 receipt |
| `internal/numind/biz/feishu/skill_reader_test.go` | 越界、分页、跨 run、过期、伪造 receipt 测试 |
| `internal/numind/biz/feishu/home_migrator.go` | 旧明文 per-user HOME 到加密 vault 的一次性安全迁移 |
| `internal/numind/biz/feishu/home_migrator_test.go` | 成功自检后删除与失败保留测试 |
| `internal/numind/biz/agent/tool_call_ctx.go` | 把当前 synthetic tool call ID 注入工具 context |
| `internal/numind/biz/agent/tool_call_ctx_test.go` | tool call ID context round-trip 测试 |
| `internal/numind/biz/agent/tool_lark_skill_read.go` | Agent 官方技能读取工具 |
| `internal/numind/biz/agent/tool_lark_execute.go` | Agent 受控飞书执行工具 |
| `internal/numind/biz/agent/tool_lark_personal_workspace_test.go` | 两个新工具、receipt、暂停 envelope 和多租户测试 |
| `internal/numind/biz/agent/external_tool_resume.go` | 外部操作完成后向原 run 回填同一 tool call result 并恢复生成 |
| `internal/numind/biz/agent/external_tool_resume_test.go` | 不追加用户答案、不生成第二条 argv 的精确恢复测试 |

### numind-server 修改

| 路径 | 改动 |
|---|---|
| `internal/pkg/model/user_third_party_account.go` | 增加 connection/capability/version/generation 字段，保留旧字段兼容 |
| `internal/pkg/crypto/aesgcm.go`、`aesgcm_test.go` | 增加带 AAD 的加解密，旧方法保持兼容 |
| `internal/numind/store/store.go` | 注册新 store 接口 |
| `internal/numind/helper.go` | feature flag 开启时 AutoMigrate 新 model |
| `internal/numind/biz/feishu/service.go`、`feishu_adapter.go`、`biz.go` | 组合新 vault/runner/catalog/operation/auth 服务 |
| `internal/numind/biz/agent/adapter_full_to_eino.go` | Execute 前注入当前 tool call ID |
| `internal/numind/biz/agent/yield_error.go` | 新增结构化 `external_action`，保留普通问答 pause |
| `internal/numind/biz/agent/factory_platform.go` | 只注册 `lark_skill_read`、`lark_execute`，移除三个旧 lark 工具和旧 connect 工具 |
| `internal/numind/store/agent_run.go` | 原子追加 external tool result、清除等待态 |
| `internal/pkg/model/agent_run.go` | 增加不含 URL 的 external action 等待 JSON 与时间字段 |
| `internal/numind/controller/v1/feishu/feishu.go`、`feishu_test.go` | 新状态、resume、refresh、解绑 DTO |
| `internal/numind/router.go` | 注册 operation resume 与 action refresh 路由 |
| `Dockerfile` | 固定 lark-cli 1.0.68 和 linux-amd64 SHA256 |

### numind-web-v3 新建/修改

| 路径 | 单一职责 |
|---|---|
| `src/api/feishu.ts` | 新 status/connect/resume/refresh/unbind 类型和请求 |
| `src/api/feishu.test.ts` | 飞书 HTTP 请求 body/path 契约 |
| `src/stores/feishu.ts` | 连接状态、能力缓存和 action 生命周期 |
| `src/stores/__tests__/feishu.spec.ts` | 连接状态和 action 生命周期测试 |
| `src/stores/__tests__/agentChat-resume.spec.ts` | external action 不走普通问答恢复的回归测试 |
| `src/components/agent/FeishuActionCard.vue` | 对话中的四阶段授权卡、原始 URL、二维码、过期、恢复 |
| `src/components/agent/__tests__/FeishuActionCard.spec.ts` | 状态、URL/二维码、ARIA、过期与按钮契约 |
| `src/components/agent/AgentMessageItem.vue` | 渲染 `external_action`，不走普通 answer channel |
| `src/components/agent/__tests__/AgentMessageItem.spec.ts` | 飞书 action 发独立 resume 事件 |
| `src/components/feishu/FeishuConnection.vue` | 设置页单卡、能力概览、继续连接、重新授权、解绑 |
| `src/components/feishu/__tests__/FeishuConnection.spec.ts` | loading/empty/error/success 与无 IM 文案 |
| `src/stores/agentChat.ts` | 接收 external action SSE/快照并调用 operation resume |
| `e2e/feishu-personal-workspace.spec.ts` | mocked UI flow + dev 真实验收入口 |

---

## Task 1: 数据模型、migration 与持久化原语

**Files:** migration、`internal/pkg/model/{user_third_party_account,feishu_workspace}.go`、`internal/numind/store/{store,feishu_workspace}.go`、`internal/numind/helper.go`。

- [x] **Step 1: 写 store 红测**

测试必须覆盖：`(user_id,idempotency_key)` 唯一；不同用户可复用相同 key；vault revision CAS；session/operation claim 只允许 lease 过期或同 owner；所有按 ID 查询同时校验 user_id + generation；解绑代际增加后旧 operation 不能 claim。

```go
func TestFeishuWorkspaceStore_IdempotencyIsPerUser(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	a, err := s.CreateOrGetOperation(ctx, newOperation(7, "same-key"))
	require.NoError(t, err)
	b, err := s.CreateOrGetOperation(ctx, newOperation(7, "same-key"))
	require.NoError(t, err)
	c, err := s.CreateOrGetOperation(ctx, newOperation(8, "same-key"))
	require.NoError(t, err)
	require.Equal(t, a.ID, b.ID)
	require.NotEqual(t, a.ID, c.ID)
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/store -run 'TestFeishuWorkspaceStore' -count=1`

Expected: FAIL，缺少 `newFeishuWorkspaceTestStore`、新 models 和 store 方法。

- [x] **Step 3: 创建 migration 与 model**

模型状态值必须逐字固定：

```go
const (
	FeishuConnectionNone = "none"
	FeishuConnectionCreatingApp = "creating_app"
	FeishuConnectionAppReady = "app_ready"
	FeishuConnectionWaitingAppApproval = "waiting_app_approval"
	FeishuConnectionWaitingUserAuth = "waiting_user_auth"
	FeishuConnectionConnected = "connected"
	FeishuConnectionReauthRequired = "reauth_required"
	FeishuConnectionError = "error"
	FeishuConnectionDisconnecting = "disconnecting"
)

type FeishuCLIVault struct {
	UserID uint `gorm:"type:bigint unsigned;primaryKey;autoIncrement:false"`
	Generation uint64 `gorm:"not null"`
	Ciphertext []byte `gorm:"type:longblob;not null"`
	KeyVersion string `gorm:"size:32;not null"`
	Checksum string `gorm:"size:64;not null"`
	Revision uint64 `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
```

`FeishuAuthSession` 和 `FeishuOperation` 严格包含 spec §8.3/§8.4 字段。Migration 为 MySQL 8 明确创建联合唯一键 `uniq_feishu_operation_user_key(user_id,idempotency_key)`、session/operation lease 索引和 `longblob` 密文字段；rollback 只用于本地验证并按 operation → session → vault 顺序 drop，再移除新增连接列。

同一 migration 给 `agent_run` 增加 `pending_external_action_json JSON NULL` 与 `pending_external_action_at DATETIME(3) NULL`。JSON 只保存 operation/session/phase/expiry/tool_call_id，不保存 URL；不新增 `TerminalReason` 或 `LoopEvent` 枚举。

- [x] **Step 4: 实现 store 原语**

Store 接口固定为：

```go
type IFeishuWorkspaceStore interface {
	GetVault(ctx context.Context, userID uint, generation uint64) (*model.FeishuCLIVault, error)
	PutVaultCAS(ctx context.Context, vault *model.FeishuCLIVault, expectedRevision uint64) error
	DeleteVault(ctx context.Context, userID uint, generation uint64) error
	CreateSession(ctx context.Context, session *model.FeishuAuthSession) error
	GetSessionForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error)
	ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	UpdateSessionState(ctx context.Context, userID uint, generation uint64, id, owner, state string, now time.Time, completedAt *time.Time) error
	CreateOrGetOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error)
	GetOperationForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error)
	ClaimOperation(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	TransitionOperation(ctx context.Context, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any) error
	CancelPendingForGeneration(ctx context.Context, userID uint, generation uint64) error
}
```

使用 GORM transaction 和条件 UPDATE 检查 `RowsAffected == 1`。Claim/transition 必须在同一个 UPDATE 中绑定调用方 `user_id + generation`，状态提交还必须校验 `lease_until > now`；`TransitionOperation.fields` 只能写审计/结果白名单字段。任何客户端可见 ID 的读取都走 `Get*ForUser`，查不到和归属不符都返回 `gorm.ErrRecordNotFound`。Vault CAS 必须先锁定当前 `(user_id, provider='lark')` 账号行并核对 generation。

- [x] **Step 5: 绿测和 migration 静态检查**

Run: `go test ./internal/numind/store ./internal/pkg/model -run 'Feishu|ThirdParty' -count=1`

Expected: PASS。

Run: `rg -n 'uniq_feishu_operation_user_key|lease_until|request_ciphertext|generation' migrations/20260713_130000_feishu_personal_workspace.sql`

Expected: 四类字段/索引均命中。

- [x] **Step 6: Commit**

```bash
git add migrations/20260713_130000_feishu_personal_workspace* internal/pkg/model internal/numind/store internal/numind/helper.go
git commit -m "feat(feishu): add personal workspace persistence"
```

## Task 2: AAD 加密与 EncryptedCLIHomeVault

**Files:** `internal/pkg/crypto/aesgcm.go`、`aesgcm_test.go`、`internal/numind/biz/feishu/vault.go`、`vault_test.go`。

- [x] **Step 1: 写加密与 vault 红测**

```go
func TestCipherAADRejectsDifferentUser(t *testing.T) {
	c := newTestCipher(t)
	sealed, err := c.EncryptWithAAD([]byte("home"), []byte("lark|7|1|v1"))
	require.NoError(t, err)
	_, err = c.DecryptWithAAD(sealed, []byte("lark|8|1|v1"))
	require.Error(t, err)
}

func TestEncryptedCLIHomeVault_RuntimePermissionsAndCleanup(t *testing.T) {
	v := newTestVault(t)
	err := v.WithHome(context.Background(), 7, 1, func(home string) (bool, error) {
		requireMode(t, home, 0o700)
		require.NoError(t, os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"apps":[]}`), 0o600))
		return true, nil
	})
	require.NoError(t, err)
	require.Empty(t, runtimeHomes(t, v.RuntimeBase()))
}
```

另测：tar 中 `../escape`、绝对路径、symlink 一律拒绝；其他用户/generation 无法解密；CAS 冲突不覆盖新快照；callback 出错也清理临时目录；文件解封后统一收紧到 `0600`；v1 快照可由同时配置 v1/v2 且 current=v2 的 keyring 读取，changed=true 时用 v2 重封，缺失历史 key 时 fail closed。

- [x] **Step 2: 运行红测**

Run: `go test ./internal/pkg/crypto ./internal/numind/biz/feishu -run 'CipherAAD|EncryptedCLIHomeVault' -count=1`

Expected: FAIL，缺少 AAD API 和 vault。

- [x] **Step 3: 实现 AAD API**

```go
func (c *Cipher) EncryptWithAAD(plaintext, aad []byte) ([]byte, error)
func (c *Cipher) DecryptWithAAD(ciphertext, aad []byte) ([]byte, error)
```

旧 `Encrypt`/`Decrypt` 调用新方法并传 `nil`，保证其他业务零回归。Vault AAD 精确编码为 `lark|<userID>|<generation>|<keyVersion>`，checksum 使用密文 SHA-256 hex。Vault keyring 按快照版本选择历史 Cipher 解密，始终用 current key/version 加密；key version 只允许 1–32 字节稳定 ASCII 标识，构造后 keyring map 冻结。

- [x] **Step 4: 实现安全打包和 WithHome**

`WithHome` 固定算法：读取当前连接 generation → 取 vault → 校验 checksum → AAD 解密 → 新建随机 temp HOME → chmod 0700 → 安全解包并将普通文件 chmod 0600 → 执行 callback → 仅当 callback 报告 changed 时重新打包、加密并 CAS → defer `os.RemoveAll`。任何解包条目经 `filepath.Clean` 后必须仍在 temp HOME 内，拒绝非普通文件/目录。提供 startup-only 残留清理 API，只删除 runtime base 的直属 `lark-home-*`；Task 12 必须在发布 service/启动 worker 前调用，失败则阻止飞书能力启动。

- [x] **Step 5: 绿测**

Run: `go test ./internal/pkg/crypto ./internal/numind/biz/feishu -run 'Cipher|Vault' -count=1`

Expected: PASS。

- [x] **Step 6: Commit**

```bash
git add internal/pkg/crypto internal/numind/biz/feishu/vault.go internal/numind/biz/feishu/vault_test.go
git commit -m "feat(feishu): encrypt per-user cli homes"
```

## Task 3: 固定 lark-cli 1.0.68 与 ControlledLarkCLIRunner

**Files:** `Dockerfile`、`internal/numind/biz/feishu/controlled_runner.go`、`controlled_runner_test.go`。本任务不修改旧 `provisioner_cli.go`，旧 `LarkCLIRunner` 保留到 Task 20 清理，避免同名类型和中间态编译失败。

- [x] **Step 1: 写 runner contract 红测**

测试 fake executable 的 argv/env/stdout/stderr，断言：启动先校验版本 `1.0.68`；调用使用 `exec.CommandContext(binary, argv...)`；环境含隔离 HOME、`LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1`；stdout/stderr 分别限长；exit 0 + `ok:false` 仍失败；非 JSON、超限、timeout 失败；中文和空格保持一个 argv 元素。另验证 timeout/取消不会留下可继续改写 HOME 的子进程；若当前运行模型无法提供独立 UID/sandbox，必须把同 UID 主动 symlink/root-rename 竞态作为明确 P2 带入 Task 23，而不是声称 Vault 已抵御恶意本地进程。

```go
func TestControlledLarkCLIRunner_DoesNotUseShell(t *testing.T) {
	r := newFakeBinaryRunner(t, `{"ok":true,"data":{"argv":["docs","+create","a; touch /tmp/pwned"]}}`)
	_, err := r.Run(context.Background(), testHome(t), []string{"docs", "+create", "a; touch /tmp/pwned"}, nil)
	require.NoError(t, err)
	require.NoFileExists(t, "/tmp/pwned")
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'ControlledLarkCLIRunner' -count=1`

Expected: FAIL，旧 runner 没有通用受控 Run 和版本 fail-closed。

- [x] **Step 3: 实现 runner 和 envelope**

固定类型：

```go
const LarkCLIVersion = "1.0.68"

type CLIEnvelope struct {
	OK bool `json:"ok"`
	Data json.RawMessage `json:"data,omitempty"`
	Error *CLIError `json:"error,omitempty"`
}

func (r *ControlledLarkCLIRunner) VerifyVersion(ctx context.Context) error
func (r *ControlledLarkCLIRunner) Run(ctx context.Context, home string, argv []string, stdinJSON []byte) (*CLIResult, error)
```

写命令开始后若 context timeout、进程被杀、输出损坏或无完整 envelope，`CLIResult` 必须保留 `InvocationStarted=true` 供 operation service 判定 unknown。

- [x] **Step 4: 更新 Dockerfile**

固定：

```dockerfile
ARG LARK_CLI_VERSION=1.0.68
ARG LARK_CLI_SHA256=8daaeb11b7cadcc77f07fd9ae7948f6c370e8305337888cb930ac7362a05cad8
```

保留下载后 `sha256sum -c -` 和 `lark-cli version` 构建检查，更新注释为 Docs/Base/Wiki 受控执行，不再描述 IM 或持久明文 HOME。

- [x] **Step 5: 绿测和 Dockerfile 检查**

Run: `go test ./internal/numind/biz/feishu -run 'ControlledLarkCLIRunner' -count=1`

Expected: PASS。

Run: `rg -n '1\.0\.68|8daaeb11b7cadcc77f07fd9ae7948f6c370e8305337888cb930ac7362a05cad8' Dockerfile`

Expected: version 与 hash 均命中。

- [x] **Step 6: Commit**

```bash
git add Dockerfile internal/numind/biz/feishu/controlled_runner.go internal/numind/biz/feishu/controlled_runner_test.go
git commit -m "feat(feishu): pin and harden lark cli runner"
```

## Task 4: CommandCatalog、exact scopes 与参数风险

**Files:** `command_catalog.go`、`command_catalog_test.go`。

- [x] **Step 1: 写完整 catalog 红测**

测试表逐项覆盖设计 spec §5。最低允许集合：Docs `+create/+fetch/+update`；Base 的 app/table/field/view 读写与真实的 record `+record-get/+record-list/+record-search/+record-batch-create/+record-upsert/+record-batch-update`；Wiki space/node create/get/list 及 Wiki 内容经 Docs fetch/update。拒绝含 delete/remove/trash/purge、IM、`api/auth/config`、`--as/--home/--profile/--brand`、未知 flag、超过 lark-cli 上限的 record 批次、Docs `block_delete`、已有资源无确认的 overwrite；21 条以上且未超过 CLI 上限的 record 写入标为高风险，交给通用确认状态机。

```go
func TestCommandCatalog_PermanentDenials(t *testing.T) {
	c := NewCommandCatalog()
	for _, argv := range [][]string{
		{"im", "+messages-send"}, {"api", "post", "/x"}, {"auth", "status"},
		{"docs", "+update", "--doc", "doxcnEXAMPLE123", "--command", "block_delete"},
		{"base", "+record-delete", "app", "table", "record"},
	} {
		_, err := c.Normalize(argv, nil)
		require.ErrorIs(t, err, ErrCommandDenied, "%v", argv)
	}
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'CommandCatalog' -count=1`

Expected: FAIL，catalog 不存在。

- [x] **Step 3: 实现 catalog**

```go
type RiskLevel string
const (
	RiskRead RiskLevel = "read"
	RiskWrite RiskLevel = "write"
	RiskHigh RiskLevel = "high"
)

type NormalizedCommand struct {
	Path string
	Domain string
	Action string
	Risk RiskLevel
	Scopes []string
	Argv []string
	StdinJSON []byte
	RequiresCLIYes bool
	ReplaySafeOnAuthError bool
}
```

Catalog 自行计算 scopes/risk，忽略模型声明；末尾统一追加 `--as user`。Flag parser 必须逐命令允许，不使用“未知 flag 原样透传”。URL/token 只接受飞书支持 host 和 spec 允许的 opaque token 形状；正文长度、数组长度、分页上限、runner argv count/bytes 和 stdout 上限写成命名常量。最终规范化 argv 必须在返回前通过 Task 3 runner 的同一输入 Gate；`RequiresCLIYes` 命令还必须用未来末尾追加 `--yes` 的精确 argv 预留 count/bytes，禁止出现“Catalog 允许但确认后 runner 必然拒绝”的命令。

`RequiresCLIYes` 是服务端静态元数据：1.0.68 只有 `base +field-update` 为 true；Docs overwrite 和大批量 record 虽然同为 `RiskHigh`，CLI 并不接受 `--yes`。模型永远不能自行传 `--yes`，Task 7 只在高风险状态已确认且该字段为 true 时追加。`ReplaySafeOnAuthError` 只表达 catalog 的无条件基线（首版仅 read=true）；写命令是否能在授权后精确重放，必须由 Task 7 固定错误分类器证明本次请求未产生副作用。

Scope manifest 必须把以下值作为 contract test 的精确期望，不使用 domain shortcut：

```go
var docsScopes = map[string][]string{
	"docs +create": {"docx:document:create"},
	"docs +fetch":  {"docx:document:readonly"},
	"docs +update": {"docx:document:write_only", "docx:document:readonly"},
}

var baseScopes = map[string][]string{
	"base +base-create":   {"base:app:create", "base:table:read", "base:table:create", "base:table:update", "base:table:delete"},
	"base +base-get":      {"base:app:read"},
	"base +table-list":    {"base:table:read"},
	"base +table-get":     {"base:table:read"},
	"base +field-list":    {"base:field:read"},
	"base +field-get":     {"base:field:read"},
	"base +view-list":     {"base:view:read"},
	"base +view-get":      {"base:view:read"},
	"base +record-get":    {"base:record:read"},
	"base +record-list":   {"base:record:read"},
	"base +record-search": {"base:record:read"},
	"base +table-create":  {"base:table:create"},
	"base +table-update":  {"base:table:update"},
	"base +field-create":  {"base:field:create"},
	"base +field-update":  {"base:field:update"},
	"base +record-batch-create": {"base:record:create"},
	"base +record-upsert":       {"base:record:create", "base:record:update"},
	"base +record-batch-update": {"base:record:update"},
}

var wikiScopes = map[string][]string{
	"wiki +space-create": {"wiki:space:write_only"},
	"wiki +node-create":  {"wiki:node:create", "wiki:node:read", "wiki:space:read"},
	"wiki +node-get":     {"wiki:node:retrieve"},
	"wiki +node-list":    {"wiki:node:retrieve"},
}
```

> 版本事实校正：固定的 lark-cli 1.0.68 不存在 `+record-create` / `+record-update`。单条创建或更新由 `+record-upsert` 根据是否带 `--record-id` 完成；多条写入使用上面的两个 batch shortcut。Catalog 只登记真实可执行路径，详见 ADR 0005。

`base:table:delete` 只因 lark-cli 1.0.68 `+base-create` 替换默认表而出现在授权说明中；catalog 仍必须拒绝所有删除命令。改变既有字段类型、超过 20 条 record 写入和覆盖既有 Doc 进入 `waiting_confirmation`；该确认复用 Agent 通用高风险确认，不借 OAuth 页面替代。

- [x] **Step 4: 写版本 manifest 快照测试**

`TestCommandCatalogManifest_1068` 从 `LarkCLIVersion` 推导 testdata 文件名，并把 `cli_version`、runner 全局限制、`path/domain/scopes/risk/limits/requires_cli_yes` 序列化到固定 JSON。任何 CLI 版本常量升级都会先因缺少新版快照而失败，必须显式审阅并更新快照。

- [x] **Step 5: 绿测**

Run: `go test ./internal/numind/biz/feishu -run 'CommandCatalog' -count=1`

Expected: PASS，拒绝表全部通过。

- [x] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/command_catalog* internal/numind/biz/feishu/testdata
git commit -m "feat(feishu): enforce docs base wiki command policy"
```

## Task 5: 结构化错误分类与 unknown 写保护

**Files:** `error_classifier.go`、`error_classifier_test.go`、`.ndf/features/feishu-personal-workspace/fixtures/*.json`。

- [x] **Step 1: 把 S2 脱敏错误保存为 fixtures 并写红测**

只保留 `ok/type/subtype/code/missing_scopes/permission_violations/identity/console_url_present`，删除 URL 值、app id 和用户信息。分类断言：missing_scope exact scopes → user/app scope；unauthorized/revoked → reauth；资源 ACL → 不 OAuth；429/5xx/network/timeout；未知 code fail closed。

```go
func TestErrorClassifier_MissingScopeIsReplayable(t *testing.T) {
	e := loadCLIErrorFixture(t, "docs-create-missing-scope.json")
	c := NewErrorClassifier().Classify(e, RiskWrite, true)
	require.Equal(t, RecoveryUserScope, c.Recovery)
	require.Equal(t, []string{"docx:document:create"}, c.MissingScopes)
	require.True(t, c.ProvenNoSideEffect)
}

func TestErrorClassifier_TimeoutAfterWriteIsUnknown(t *testing.T) {
	c := NewErrorClassifier().ClassifyTransport(context.DeadlineExceeded, RiskWrite, true)
	require.Equal(t, RecoveryNone, c.Recovery)
	require.Equal(t, model.FeishuOperationUnknown, c.TerminalState)
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'ErrorClassifier' -count=1`

Expected: FAIL，classifier 不存在。

- [x] **Step 3: 实现固定 code/subtype 分类表**

```go
type RecoveryKind string
const (
	RecoveryNone RecoveryKind = "none"
	RecoveryCreateApp RecoveryKind = "create_app"
	RecoveryAppScope RecoveryKind = "app_scope"
	RecoveryUserScope RecoveryKind = "user_scope"
	RecoveryResourceACL RecoveryKind = "resource_acl"
)

type Classification struct {
	Recovery RecoveryKind
	MissingScopes []string
	ProvenNoSideEffect bool
	RetryRead bool
	TerminalState string
	PublicCode string
}
```

只允许 fixture 覆盖的 code/subtype 设置 `ProvenNoSideEffect=true`。不得用中文/英文 message substring 猜授权类型。相同 scopes 连续两次返回同一 recovery 时由上层停止循环。

- [x] **Step 4: 绿测**

Run: `go test ./internal/numind/biz/feishu -run 'ErrorClassifier' -count=1`

Expected: PASS。

- [x] **Step 5: Commit**

```bash
git add internal/numind/biz/feishu/error_classifier* .ndf/features/feishu-personal-workspace/fixtures
git commit -m "feat(feishu): classify cli recovery errors"
```

## Task 6: 官方技能读取与签名 receipt

**Files:** `skill_reader.go`、`skill_reader_test.go`。

- [x] **Step 1: 写 reader 红测**

只允许 `lark-shared/lark-doc/lark-base/lark-wiki`；reference 必须来自该技能主文件声明的清单；拒绝绝对路径、`..`、symlink；分页未读完无 receipt；最终 receipt 绑定 runID/skill/version/expiry；换 run、换 skill、换 version、过期、篡改均拒绝。

```go
func TestSkillReceiptIsBoundToRunAndVersion(t *testing.T) {
	r := newSkillReaderHarness(t)
	page, err := r.reader.Read(r.ctx, SkillReadRequest{AgentRunID: 11, Skill: "lark-doc"})
	require.NoError(t, err)
	require.NotEmpty(t, page.Receipt)
	require.NoError(t, r.reader.Verify(page.Receipt, 11, "lark-doc", "1.0.68"))
	require.Error(t, r.reader.Verify(page.Receipt, 12, "lark-doc", "1.0.68"))
	require.Error(t, r.reader.Verify(page.Receipt, 11, "lark-doc", "1.0.69"))
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'SkillReader|SkillReceipt' -count=1`

Expected: FAIL，reader 不存在。

- [x] **Step 3: 实现 reader 和 receipt signer**

技能内容从固定 lark-cli 1.0.68 自带 `skills read` 读取，不走用户 HOME、不联网。Cursor 使用 HMAC 签名的 `skill|reference|offset|runID|expiresAt`；单页最大 32 KiB；只有最后一页签发 receipt。Receipt HMAC key 从现有第三方密钥经 domain separation `HMAC(key,"feishu-skill-receipt-v1")` 派生，不新增明文配置。

- [x] **Step 4: 提供无反向依赖的 receipt verifier 方法**

`SkillReader` 提供 `VerifyRequired(receipts []string, runID uint64, domain string) error`。Domain 映射固定：Docs 要 `lark-shared+lark-doc`；Base 要 `lark-shared+lark-base`；Wiki 命令要 `lark-shared+lark-wiki`，Wiki 内容经 Docs 操作时还要 `lark-doc`。本任务不修改 `operation_service.go`；Task 7 只通过小接口依赖该方法。

- [x] **Step 5: 绿测**

Run: `go test ./internal/numind/biz/feishu -run 'SkillReader|SkillReceipt' -count=1`

Expected: PASS。

- [x] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/skill_reader*
git commit -m "feat(feishu): read versioned official lark skills"
```

## Task 7: FeishuOperationService 幂等执行和精确重放

**Files:** `operation_service.go`、`operation_service_test.go`。

- [x] **Step 1: 写 operation 红测**

覆盖：connected 直接调用真实业务命令且 `AuthStatus` 调用数为 0；none 进入 waiting_connection；同 user+key 并发 20 次只有一次 runner 调用；授权错误保存规范化请求；resume 只读密文原请求；写 timeout → unknown；读 timeout 有界重试；generation 不符取消；成功结果幂等返回。用 barrier 模拟 runner 已开始后 generation bump：旧执行不能提交 succeeded 或写回旧 Vault；写操作按 unknown 交给解绑层收口，且不得再 claim。

```go
func TestOperationService_ConnectedHotPathSkipsAuthStatus(t *testing.T) {
	d := newOperationHarness(t)
	d.connection.State = model.FeishuConnectionConnected
	d.runner.Result = okCLIResult(`{"document_id":"doc1"}`)
	got, err := d.service.Execute(d.ctx, ExecuteRequest{
		UserID: 7, AgentRunID: 9, ToolCallID: "tc1",
		IdempotencyKey: "9:tc1", Argv: []string{"docs", "+create", "--title", "报告"},
		SkillReceipts: d.validReceipts("lark-shared", "lark-doc"),
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", got.State)
	require.Zero(t, d.runner.AuthStatusCalls)
	require.Equal(t, 1, d.runner.BusinessCalls)
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'OperationService' -count=1`

Expected: FAIL，service 不存在。

- [x] **Step 3: 定义依赖倒置接口**

```go
type ReceiptVerifier interface {
	VerifyRequired(receipts []string, runID uint64, domain string) error
}
type RecoveryStarter interface {
	StartRecovery(ctx context.Context, req RecoveryRequest) (*OperationAction, error)
}
type ConfirmationRequester interface {
	RequestConfirmation(ctx context.Context, operationID string, summary ConfirmationSummary) (*OperationAction, error)
}
```

`operation_service.go` 不得 import `biz/agent`。本任务的 tests 为三接口注入 fake；Task 6 的 `SkillReader` 满足 `ReceiptVerifier`，Task 8 的 `AuthSessionService` 满足 `RecoveryStarter`，Task 12 才做生产装配。

- [x] **Step 4: 实现请求、结果和执行算法**

```go
type ExecuteRequest struct {
	UserID uint
	AgentRunID uint64
	ToolCallID string
	IdempotencyKey string
	Argv []string
	StdinJSON json.RawMessage
	SkillReceipts []string
}
type OperationResult struct {
	OperationID string `json:"operation_id"`
	State string `json:"state"`
	Data json.RawMessage `json:"data,omitempty"`
	Action *OperationAction `json:"action,omitempty"`
	AgentRunID uint64 `json:"-"`
	ToolCallID string `json:"-"`
}
```

执行顺序固定为：验 receipt → catalog Normalize → 加密规范化请求 → CreateOrGetOperation → claim lease → 明确 none 调 `RecoveryStarter` → connected 直接 `vault.WithHome` + controlled runner → classifier → proven-no-side-effect 权限错误调 `RecoveryStarter` → 成功/失败/unknown 收口。High-risk 只调 `ConfirmationRequester` 并返回 `waiting_confirmation`；不直接调用 Agent 包。成功密文在消费后可擦除，其他 operation 密文最多保留 7 天。

- [x] **Step 5: 实现 Resume**

`Resume(ctx,userID,operationID)` 只能读取已存 request ciphertext；等待 session 未完成时返回现状；完成时 CAS waiting → executing 后重放；terminal 原样返回摘要。重复点击和后台 dispatcher 共用此入口。

- [x] **Step 6: 绿测和 race**

Run: `go test -race ./internal/numind/biz/feishu -run 'OperationService' -count=1`

Expected: PASS，无 data race，同幂等键 runner 调用一次。

- [x] **Step 7: Commit**

```bash
git add internal/numind/biz/feishu/operation_service.go internal/numind/biz/feishu/operation_service_test.go
git commit -m "feat(feishu): execute idempotent personal operations"
```

## Task 8: AuthSessionService 与确定性连接编排

**Files:** `auth_session_service.go`、`auth_session_service_test.go`、`connect_orchestrator.go`、`connect_orchestrator_test.go`；实现期为闭合跨实例会话复用、瞬时 console URL 与 operation activation barrier，扩展 `operation_service.go`、`operation_service_test.go`、`store/feishu_workspace.go`、`store/feishu_workspace_test.go`。

- [x] **Step 1: 写状态机红测**

覆盖 manual connect 只申请 `offline_access`；业务 operation 只使用 exact scopes；create_app/app_scope/user_auth；URL 不落 DB；lease 丢失 supersede；worker 成功调用 dispatcher；相同 scopes 连续两次停止；有 `console_url` 时 waiting_app_approval。

```go
func TestAuthSessionService_ManualConnectRequestsOfflineAccessOnly(t *testing.T) {
	h := newAuthHarness(t)
	action, err := h.service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, []string{"offline_access"}, h.worker.RequestedScopes)
	require.Equal(t, "user_auth", action.Phase)
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'AuthSession|ConnectOrchestrator' -count=1`

Expected: FAIL，新状态机不存在。

- [x] **Step 3: 定义单向恢复接口并实现 worker**

```go
type OperationResumeDispatcher interface {
	DispatchResume(ctx context.Context, userID uint, operationID string) error
}
```

`AuthSessionService` 只依赖该接口，不持有具体 `OperationService`。Blocking worker 持 DB lease；从 CLI 输出提取 URL 后只放内存 registry；DB 保存 phase/scopes/state/lease/expiry。成功后 seal HOME、标 session completed、调用 dispatcher。过期 lease 先用 recovery 专用 `auth status` 检查 vault，再完成或 supersede。Operation-linked worker 必须在 URL ready 后等待 `Activate`，由 `OperationService` 持久化 waiting 后放行；`Abort` 终止未能持久化的恢复。`RecoveryStarter` 在生产依赖中强制包含 Start/Activate/Abort，不得通过可选 type assertion 静默绕过。

- [x] **Step 4: 新路径只生成 exact scope auth**

```go
[]string{"auth", "login", "--json", "--scope", strings.Join(exactScopes, " ")}
```

新 `auth_session_service.go` 和 `connect_orchestrator.go` 不得出现 `--domain`、IM scope。旧 `auth_cli.go` 在 Task 20 前保持未注册状态，避免本任务同时承担清理。

- [x] **Step 5: 绿测**

Run: `go test -race ./internal/numind/biz/feishu -run 'AuthSession|ConnectOrchestrator' -count=1`

Expected: PASS。

Run: `rg -n -- '--domain|docs,im,base|im:message' internal/numind/biz/feishu/auth_session_service.go internal/numind/biz/feishu/connect_orchestrator.go`

Expected: 零命中。

- [x] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/auth_session_service.go internal/numind/biz/feishu/auth_session_service_test.go internal/numind/biz/feishu/connect_orchestrator.go internal/numind/biz/feishu/connect_orchestrator_test.go
git commit -m "feat(feishu): orchestrate incremental authorization"
```

## Task 9: External Action 传输与持久等待契约

**Files:** `internal/pkg/model/agent_run.go`、`internal/pkg/externalaction/`、`internal/numind/store/agent_run.go`、`internal/numind/store/agent_run_external_action_test.go`、`internal/numind/biz/agent/yield_error.go`、`yield_external_action_test.go`、`runner.go`、`runner_stream.go`、`runner_runstream.go`、`student_query.go`、`answer.go`、`stream/events.go` 及对应 external-action tests；为保持既有 SQLite 手写 schema 与正式 migration 一致，机械补齐相关测试 fixture 两列。

- [x] **Step 1: 写 transport 红测**

覆盖 external action 产生独立 `external_action` SSE；普通 question 不回归；持久化只含 operation/session/phase/expiry/tool_call_id，不含 URL；session snapshot 可重建无 URL 的等待卡；answer API 拒绝 external action。

```go
func TestExternalActionPersistenceOmitsURL(t *testing.T) {
	p := ExternalActionPayload{OperationID: "op-1", SessionID: "s-1", ToolCallID: "tc-1", Phase: "user_auth", URL: "https://opaque", ExpiresAt: time.Now()}
	stored := p.Persistent()
	b, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NotContains(t, string(b), "https://opaque")
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/agent ./internal/numind/biz/agent/stream ./internal/numind/store -run 'ExternalAction' -count=1`

Expected: FAIL，独立 event 和等待字段不存在。

- [x] **Step 3: 定义 payload/event/model**

```go
type ExternalActionPayload struct {
	Provider string `json:"provider"`
	OperationID string `json:"operation_id"`
	SessionID string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	Phase string `json:"phase"`
	URL string `json:"url,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

PendingExternalActionJSON datatypes.JSON `gorm:"type:json;column:pending_external_action_json" json:"pending_external_action_json,omitempty"`
PendingExternalActionAt *time.Time `gorm:"column:pending_external_action_at" json:"pending_external_action_at,omitempty"`
```

`stream.EventExternalAction = "external_action"`。Live event 可含 URL；`Persistent()` 返回不包含 URL 字段的中立 durable DTO。共享 token parser 要求 6 个精确小写字段各出现一次，拒绝重复、大小写变体、未知/敏感字段和 trailing JSON；store 只保存重新序列化的 canonical JSON。不要修改固定 `TerminalReason`/`LoopEvent` 数量。

- [x] **Step 4: 添加独立 writer 接口和 runner 分支**

```go
type IExternalActionWriter interface {
	UpdatePendingExternalAction(ctx context.Context, runID uint64, payloadJSON []byte) error
}
```

具体 `agentRunStore` 实现该接口；不向 `IAgentRunStore` 强加新方法，避免所有旧测试 fake 同步修改。Runner 发现 external action 时用 `writer, ok := r.runStore.(store.IExternalActionWriter)` 获取能力，生产 store 缺能力或写入失败则 fail closed；只有 durable identity 写入后才发独立 SSE、进入既有 waiting_for_user_choice 状态。普通 question 继续调用 `UpdatePendingQuestion`。快照只要检测到 external JSON 就不得回退 stale question；Answer API 在普通 question 解析前明确拒绝 external wait。

- [x] **Step 5: 绿测与精确提交**

Run: `go test ./internal/numind/biz/agent ./internal/numind/biz/agent/stream ./internal/numind/store -run 'ExternalAction|QuestionPrompt' -count=1`

Expected: PASS。

```bash
git add internal/pkg/model/agent_run.go internal/numind/store/agent_run.go internal/numind/store/agent_run_external_action_test.go internal/numind/biz/agent/yield_error.go internal/numind/biz/agent/yield_external_action_test.go internal/numind/biz/agent/runner.go internal/numind/biz/agent/runner_stream.go internal/numind/biz/agent/student_query.go internal/numind/biz/agent/stream/events.go internal/numind/biz/agent/stream/events_external_action_test.go
git commit -m "feat(agent): persist external action waits"
```

## Task 10: Agent 飞书工具与 tool call context

**Files:** `tool_call_ctx.go`、`tool_call_ctx_test.go`、`adapter_full_to_eino.go`、`adapter_full_to_eino_test.go`、`tool_lark_skill_read.go`、`tool_lark_execute.go`、`tool_lark_personal_workspace_test.go`、`factory_platform.go`、`factory_platform_test.go`。

- [x] **Step 1: 写 adapter/tool 红测**

断言 Execute 能读同一 synthetic toolCallID；`lark_execute` 不接受 user_id；idempotency key 固定 `<runID>:<toolCallID>`；waiting 发 Task 9 external action；skill read 不要求连接；factory 查不到四个旧工具。

```go
func TestFullToolAdapterInjectsToolCallID(t *testing.T) {
	ft := &capturesToolCallID{}
	_, err := adaptFullToEinoTool(ft, nil).InvokableRun(WithRunID(context.Background(), 9), `{}`)
	require.NoError(t, err)
	require.NotEmpty(t, ft.got)
}
```

- [x] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/agent -run 'ToolCallID|LarkPersonalWorkspace|PlatformTool' -count=1`

Expected: FAIL，新工具不存在。

- [x] **Step 3: 注入 tool call ID**

```go
func WithToolCallID(ctx context.Context, id string) context.Context
func ToolCallIDFromContext(ctx context.Context) string
```

Execute 唯一调用点改为 `invokeToolGuarded(WithToolCallID(ctx, toolCallID), input)`。

- [x] **Step 4: 通过小接口实现两个 FullTool**

```go
type SkillReadExecutor interface { Read(context.Context, feishu.SkillReadRequest) (*feishu.SkillReadPage, error) }
type LarkExecutor interface { Execute(context.Context, feishu.ExecuteRequest) (*feishu.OperationResult, error) }
```

`lark_skill_read` input 为 skill/reference/cursor；`lark_execute` input 只含 argv/stdin_json/skill_receipts。userID/runID/toolCallID 只取 context。Factory 接受接口依赖并 nil-safe；本任务不构造具体 operation/auth 服务，Task 12 才装配。

- [x] **Step 5: 收缩 registry 并绿测**

Factory 只注册 `lark_skill_read`、`lark_execute`；旧源文件保留到 Task 20，但 registry 查不到 `lark_create_doc/lark_read_bitable/lark_send_message/feishu_connect`。

Run: `go test ./internal/numind/biz/agent -run 'ToolCallID|LarkPersonalWorkspace|PlatformTool' -count=1`

Expected: PASS。

- [x] **Step 6: Commit**

```bash
git add internal/numind/biz/agent/tool_call_ctx.go internal/numind/biz/agent/tool_call_ctx_test.go internal/numind/biz/agent/adapter_full_to_eino.go internal/numind/biz/agent/adapter_full_to_eino_test.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/agent/factory_platform.go internal/numind/biz/agent/factory_platform_test.go
git commit -m "feat(agent): add controlled lark workspace tools"
```

## Task 11: 原 tool call 精确结果恢复

**Files:** `internal/numind/store/agent_run.go`、`agent_run_external_resume_test.go`、`internal/numind/biz/agent/external_tool_resume.go`、`external_tool_resume_test.go`、`answer.go`、`answer_external_resume_test.go`。

- [ ] **Step 1: 写恢复红测**

resume 原子追加相同 tool_call_id 的 role=tool；不新增 role=user “我已完成”；不调用第二次 lark_execute；重复结果幂等；原 run 已取消只保存 operation 结果不新建 run。

```go
func TestExternalToolResumeAppendsOriginalToolResultWithoutUserAnswer(t *testing.T) {
	h := newExternalResumeHarness(t)
	require.NoError(t, h.resumer.Resume(h.ctx, ExternalToolResult{RunID: 41, ToolCallID: "tc-9", OperationID: "op-1", Result: json.RawMessage(`{"ok":true}`)}))
	msgs := h.store.Messages(41)
	require.Equal(t, "tool", msgs[len(msgs)-1].Role)
	require.Equal(t, "tc-9", msgs[len(msgs)-1].ToolCallID)
	require.NotContains(t, string(h.store.RawMessages(41)), "我已完成")
	require.Zero(t, h.larkExecuteCalls)
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/agent ./internal/numind/store -run 'ExternalToolResume' -count=1`

Expected: FAIL，resumer 不存在。

- [ ] **Step 3: 实现原子 resumer store**

```go
type IExternalToolResumer interface {
	ResumeExternalTool(ctx context.Context, runID uint64, operationID, toolCallID string, resultTurn json.RawMessage) (bool, error)
}
```

事务锁 agent_run，验证等待 JSON，append `schema.ToolMessage(string(result), toolCallID)` 序列化 turn，清 external waiting，状态回 running。bool=false 表示已恢复。

- [ ] **Step 4: 抽取无用户消息的 runner 恢复入口**

在 `answer.go` 抽取 `resumeRunFromStoredHistory(ctx, runID)`；普通 Answer 先 `AnswerAndClear` 再调用它，ExternalToolResumer 在 store transaction 后直接调用它。两条路径共用生成逻辑但只有普通 Answer 追加 user turn。

- [ ] **Step 5: 绿测和 Commit**

Run: `go test -race ./internal/numind/biz/agent ./internal/numind/store -run 'ExternalToolResume|Answer' -count=1`

Expected: PASS。

```bash
git add internal/numind/store/agent_run.go internal/numind/store/agent_run_external_resume_test.go internal/numind/biz/agent/external_tool_resume.go internal/numind/biz/agent/external_tool_resume_test.go internal/numind/biz/agent/answer.go internal/numind/biz/agent/answer_external_resume_test.go
git commit -m "feat(agent): resume original external tool calls"
```

## Task 12: 后端 composition 与 Agent factory 注入

**Files:** `internal/numind/biz/feishu_resume_dispatcher.go`、`feishu_resume_dispatcher_test.go`、`internal/numind/biz/feishu/operation_confirmation.go`、`operation_confirmation_test.go`、`internal/numind/biz/feishu_adapter.go`、`feishu_adapter_test.go`、`internal/numind/biz/biz.go`、`internal/numind/biz/agent/factory_platform.go`、`factory_platform_test.go`。

- [ ] **Step 1: 写 composition 红测**

Feature flag off 返回 nil；版本不符 fail closed；flag on 构造 controlled runner/vault/catalog/classifier/skill reader/operation/auth/resumer；高风险返回 waiting_confirmation；auth worker 自动完成后原 tool result 只回填一次；Agent registry 只暴露两个新工具；生产热路径不构造旧 Client。Vault startup cleanup 必须在 service/factory 对外可见及 worker 启动前完成，cleanup 失败时 fail closed。

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz ./internal/numind/biz/agent -run 'ResumeDispatcher|OperationConfirmation|BuildFeishu|FeishuComposition|PlatformTool' -count=1`

Expected: FAIL，生产依赖尚未装配。

- [ ] **Step 3: 实现共用 resume dispatcher**

```go
type WorkspaceResumeDispatcher struct {
	operations *feishu.OperationService
	agentResumer *agent.AgentRunResumer
}
func (d *WorkspaceResumeDispatcher) DispatchResume(ctx context.Context, userID uint, operationID string) error
```

dispatcher 调 `operation.Resume`；结果 succeeded 时读取 OperationResult 内部 `AgentRunID/ToolCallID`（字段 `json:"-"`），调用 Task 11 resumer；waiting/failed/unknown 不回填成功 result。用 operationID 幂等保证 auth worker 与用户点击并发时只回填一次。Task 8 worker 和 Task 13 service 必须持有这个 dispatcher，不可各自直接调 operation.Resume。

- [ ] **Step 4: 实现生产 ConfirmationRequester**

`NewOperationConfirmationRequester(store)` 实现 Task 7 接口，把 operation 置 `waiting_confirmation` 并返回 `OperationAction{Phase:"confirmation"}`；不得返回 nil 让高风险静默通过。Task 13 接受 `confirmed/cancelled` 后由同一 dispatcher 继续或取消加密 operation。该 adapter 位于 `biz/feishu`，不 import `biz/agent`。`WorkspaceResumeDispatcher` 位于外层 `package biz`，由它同时 import `biz/feishu` 与 `biz/agent`，从而避免 `feishu → agent → feishu` 包循环。

- [ ] **Step 5: 构造无包循环 bridge**

在 `feishu_adapter.go` 用 closure bridge 装配：先构造 keyring vault 并同步执行 startup cleanup；再创建引用 `authSvc` 的 `RecoveryStarterFunc`，用 receipt verifier + recovery starter + non-nil confirmation requester 构造 operation；创建 AgentRunResumer 和共用 dispatcher；再构造 AuthSessionService 并注入 dispatcher；最后赋值 authSvc。构造函数返回前全部 bridge 必须完整，cleanup/版本验证任一失败都不得发布半初始化 service。

- [ ] **Step 6: 注入 Agent factory**

Factory 获得 Task 6 `SkillReader` 与 Task 7 `OperationService` 的接口；移除旧 client/orchestrator 的 production registration。启动时先 `ControlledLarkCLIRunner.VerifyVersion`，失败则不注册飞书 service/tools。

- [ ] **Step 7: 绿测和 Commit**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz ./internal/numind/biz/agent -run 'ResumeDispatcher|OperationConfirmation|Feishu|PlatformTool' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu_resume_dispatcher.go internal/numind/biz/feishu_resume_dispatcher_test.go internal/numind/biz/feishu/operation_confirmation.go internal/numind/biz/feishu/operation_confirmation_test.go internal/numind/biz/feishu_adapter.go internal/numind/biz/feishu_adapter_test.go internal/numind/biz/biz.go internal/numind/biz/agent/factory_platform.go internal/numind/biz/agent/factory_platform_test.go
git commit -m "feat(feishu): compose personal workspace services"
```

## Task 13: Lifecycle HTTP API 与安全解绑

**Files:** `internal/numind/biz/feishu/service.go`、`service_test.go`、`internal/numind/controller/v1/feishu/feishu.go`、`feishu_test.go`、`internal/numind/router.go`。

- [ ] **Step 1: 写 HTTP/service 红测**

覆盖 GET status 不生成 URL；manual connect；resume body 只允许 `user_completed/confirmed/cancelled` 且不接受 argv/scopes；refresh 校验归属；跨用户统一 404；HTTP user_completed 与 worker 并发只回填一次；DELETE generation+1、取消等待、删 vault、停止 worker、清能力并说明远端 app 保留。另用 barrier 覆盖解绑与 executing write 交错：解绑必须等待有效 execution lease 或将结果收口 unknown，不能让旧 generation 成功提交/重领。

```go
func TestResumeRejectsCrossUserAsNotFound(t *testing.T) {
	r := newFeishuHTTPHarness(t, 8)
	resp := r.POST("/v1/feishu/operations/op-owned-by-7/resume", `{"action":"user_completed"}`)
	require.Equal(t, http.StatusNotFound, resp.Code)
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/controller/v1/feishu ./internal/numind/biz/feishu -run 'Status|Connect|Resume|Refresh|Unbind' -count=1`

Expected: FAIL，新 API 缺失。

- [ ] **Step 3: 实现 routes 与薄 controller**

```go
feishuAuthGroup.GET("/status", feishuCtrl.Status)
feishuAuthGroup.POST("/connect", feishuCtrl.Connect)
feishuAuthGroup.POST("/operations/:id/resume", feishuCtrl.ResumeOperation)
feishuAuthGroup.POST("/actions/:session_id/refresh", feishuCtrl.RefreshAction)
feishuAuthGroup.DELETE("/connection", feishuCtrl.Unbind)
```

Controller 只解析鉴权 userID/path/body；biz service 推进状态。`user_completed` 调 Task 12 共用 `DispatchResume`；`confirmed/cancelled` 只在 waiting_confirmation 合法，并由同一 dispatcher 重放或取消。Status 返回 spec §10.1，不含当前 URL。

- [ ] **Step 4: 实现安全解绑**

事务先 disconnecting + generation+1；取消旧 generation 等待；执行中写等待租约或按 unknown 收口；停止相应 worker，再尽力 logout/remove、删除本地 vault/temp HOME；最终 none/connected false。锁顺序保持 account → operation/session → vault，旧 generation 不得成功提交或重领。远端 app 删除不作成功承诺。

- [ ] **Step 5: 绿测和 Commit**

Run: `go test ./internal/numind/controller/v1/feishu ./internal/numind/biz/feishu -run 'Status|Connect|Resume|Refresh|Unbind' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/service.go internal/numind/biz/feishu/service_test.go internal/numind/controller/v1/feishu/feishu.go internal/numind/controller/v1/feishu/feishu_test.go internal/numind/router.go
git commit -m "feat(feishu): expose workspace lifecycle api"
```

## Task 14: 旧明文 HOME 安全迁移（二期；一期按需升级）

**Files:** `internal/numind/biz/feishu/home_migrator.go`、`home_migrator_test.go`、`internal/numind/biz/feishu_adapter.go`、`feishu_adapter_test.go`。

- [ ] **Step 1: 写 migrator 红测**

覆盖合法 `u<userID>`；成功自检后删除；加密/CAS/自检失败保留原目录；非法权限/软链拒绝；vault 已存在幂等；迁移失败使该用户 fail closed。

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz -run 'HomeMigrator' -count=1`

Expected: FAIL，migrator 不存在。

- [ ] **Step 3: 实现一次性迁移并接启动**

只在 feature flag 开启且 CLI 版本正确时扫描旧目录：逐用户锁 → 权限校验 → vault 打包加密 → PutVaultCAS → 立即解密 checksum 自检 → 成功才删除。失败保留旧数据且不允许新旧 runner 同时使用。

- [ ] **Step 4: 绿测和 Commit**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz -run 'HomeMigrator' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/home_migrator.go internal/numind/biz/feishu/home_migrator_test.go internal/numind/biz/feishu_adapter.go internal/numind/biz/feishu_adapter_test.go
git commit -m "feat(feishu): migrate legacy cli homes"
```

## Task 15: 飞书 operation 可观测性（二期）

**Files:** `internal/numind/biz/feishu/observability.go`、`observability_test.go`、`operation_service.go`、`auth_session_service.go`、`vault.go`、`internal/numind/biz/agent/tool_lark_skill_read.go`、`tool_lark_execute.go`。

- [ ] **Step 1: 写脱敏 span/metric 红测**

断言 8 个 span 名称；允许标签只有 user hash/run/tool/operation/path/domain/risk/state/version/duration/exit/error code/scope count/attempt；argv/stdin/正文/完整 URL/app id/密文不出现；授权循环和 unknown 写有独立 counter。

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz/agent -run 'FeishuObservability' -count=1`

Expected: FAIL，observability wrapper 不存在。

- [ ] **Step 3: 实现 spans 与 metrics**

固定 span：`tool.lark_skill_read`、`tool.lark_execute`、`feishu.operation.execute`、`feishu.connect`、`feishu.auth`、`feishu.operation.resume`、`feishu.vault.open`、`feishu.vault.seal`。沿用原 Agent trace，不新增 LLM generation。

- [ ] **Step 4: 绿测和 Commit**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz/agent -run 'FeishuObservability' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/observability.go internal/numind/biz/feishu/observability_test.go internal/numind/biz/feishu/operation_service.go internal/numind/biz/feishu/auth_session_service.go internal/numind/biz/feishu/vault.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_execute.go
git commit -m "feat(feishu): trace workspace operations safely"
```

## Task 16: 前端改动前 Playwright 运行时诊断

**Files:** `.ndf/features/feishu-personal-workspace/s3-frontend-baseline.md`（脱敏结论进 Git）；Playwright trace/screenshot 留在临时输出且不进 Git；不改业务代码。

- [ ] **Step 1: 启动当前前端并使用现有诊断 helper**

Run in web worktree: `npm run dev -- --host 127.0.0.1`

Run: `npx playwright test e2e/agent-ask-user-question.spec.ts --project=chromium --trace=on`

Expected: 当前普通 question/auth card 基线可复现；若环境 API 不可用，用现有 route mock，不修改生产代码。

- [ ] **Step 2: 记录基线证据**

使用 `e2e/helpers/diagnose.ts` 记录：auth question_prompt DOM、Pinia message snapshot、点击“我已完成”发出的旧 answer request、设置页飞书 card 四态、375px viewport 溢出。把结论写到 plan 同目录旁的 `.ndf/features/feishu-personal-workspace/s3-frontend-baseline.md`，只写结构和请求路径，不写真实 URL/token。

- [ ] **Step 3: Commit 诊断文档到 server worktree**

```bash
git add .ndf/features/feishu-personal-workspace/s3-frontend-baseline.md
git commit -m "docs(feishu): capture frontend authorization baseline"
```

## Task 17: 前端 API 与 Pinia 状态契约

**Files:** `src/api/feishu.ts`、`src/stores/feishu.ts`、`src/stores/agentChat.ts` 及 tests。

- [ ] **Step 1: 写 API/store 红测**

测试状态 union 完整；status 不假设 unknown=available；resume 只发 `{action:'user_completed'}`；refresh path 只含 session id；SSE/快照 external action 转为同一 message；成功后原卡变 completed；普通 question answer 行为不回归。

```ts
it('resumes an operation without sending argv or scopes', async () => {
  await resumeFeishuOperation('op-1')
  expect(mockPost).toHaveBeenCalledWith('/v1/feishu/operations/op-1/resume', {
    action: 'user_completed',
  })
})
```

- [ ] **Step 2: 运行红测**

Run: `npm run test:unit -- src/api/feishu.test.ts src/stores/__tests__/feishu.spec.ts src/stores/__tests__/agentChat-resume.spec.ts`

Expected: FAIL，新类型/方法/message 不存在。

- [ ] **Step 3: 实现类型和 API**

```ts
export type FeishuConnectionState =
  | 'none' | 'creating_app' | 'app_ready' | 'waiting_app_approval'
  | 'waiting_user_auth' | 'connected' | 'reauth_required' | 'error' | 'disconnecting'

export type FeishuCapabilityState =
  | 'unknown' | 'available' | 'needs_app_scope' | 'needs_user_scope' | 'revoked' | 'resource_denied'

export interface FeishuExternalAction {
  provider: 'lark'
  operation_id: string
  session_id: string
  phase: 'create_app' | 'app_scope' | 'user_auth' | 'confirmation'
  url?: string
  expires_at: string
}
```

所有请求复用 `src/api/request.ts`；不 import 原生 axios。

- [ ] **Step 4: 实现 stores**

`feishu` store 管 status/connect/refresh/unbind；`agentChat` store 接 external action event 并提供 `resumeFeishuOperation`，不调用 `startResume` 的普通 answer path。轮询采用有限截止时间；页面隐藏或 action terminal 时停止。

- [ ] **Step 5: 绿测**

Run: `npm run test:unit -- src/api/feishu.test.ts src/stores/__tests__/feishu.spec.ts src/stores/__tests__/agentChat-resume.spec.ts`

Expected: PASS。

- [ ] **Step 6: Commit（前端仓库）**

```bash
git add src/api/feishu.ts src/api/feishu.test.ts src/stores/feishu.ts src/stores/__tests__/feishu.spec.ts src/stores/agentChat.ts src/stores/__tests__/agentChat-resume.spec.ts
git commit -m "feat(feishu): add personal workspace client state"
```

## Task 18: Agent 飞书 Action Card

**Files:** `src/components/agent/FeishuActionCard.vue`、`src/components/agent/__tests__/FeishuActionCard.spec.ts`、`src/components/agent/AgentMessageItem.vue`、`src/components/agent/__tests__/AgentMessageItem.spec.ts`。

- [ ] **Step 1: 写组件红测**

覆盖 create_app/app_scope/user_auth/confirmation/继续原任务；URL 展示值和复制值完整一致；二维码 payload 与 URL 字节相同；过期后禁用旧 continue 并显示刷新；resume/refresh/confirmed/cancelled 独立 emit；`aria-live=polite`、错误 `role=alert`；375px 无横向溢出。

```ts
it('emits operation resume instead of a question answer', async () => {
  const wrapper = mount(FeishuActionCard, { props: { action: waitingUserAuthAction } })
  await wrapper.get('[data-testid="feishu-continue"]').trigger('click')
  expect(wrapper.emitted('resume')).toEqual([['op-1']])
  expect(wrapper.emitted('answer-submitted')).toBeUndefined()
})
```

- [ ] **Step 2: 运行红测**

Run: `npm run test:unit -- src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts`

Expected: FAIL，新卡不存在且旧卡走 answer-submitted。

- [ ] **Step 3: 实现 FeishuActionCard**

用现有 `AppButton` 和 `qrcode`，不引入 UI 框架。卡片使用 `var(--surface)`、`var(--border)`、`var(--shadow-card)`、`var(--radius-md)`、T-shirt spacing token；主 CTA 使用翠绿 `var(--primary)`；heading 使用 `var(--font-heading)`。阶段文案严格采用 spec §12.1；URL 使用 `overflow-wrap:anywhere`；二维码容器在移动端限制 `max-width:100%`。

- [ ] **Step 4: 接入 AgentMessageItem**

`external_action` 渲染新卡；普通 `pause_type=question` 仍渲染 QuestionPrompt。删去旧 `pause_type=auth` → ordinary answer 的桥接代码。resume 调 store operation API，结果更新原 message，不创建用户气泡。

- [ ] **Step 5: 绿测**

Run: `npm run test:unit -- src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts`

Expected: PASS。

- [ ] **Step 6: Commit（前端仓库）**

```bash
git add src/components/agent/FeishuActionCard.vue src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/AgentMessageItem.vue src/components/agent/__tests__/AgentMessageItem.spec.ts
git commit -m "feat(feishu): add recoverable agent action card"
```

## Task 19: 设置页飞书连接卡

**Files:** `src/components/feishu/FeishuConnection.vue`、`src/components/feishu/__tests__/FeishuConnection.spec.ts`。

- [ ] **Step 1: 写设置卡红测**

覆盖 loading/empty/error/success；脱敏 app id；Docs/Base/Wiki unknown/available/revoked；连接/继续/重新授权；解绑使用现有 ConfirmModal；文案不出现 IM/发送消息；empty 引导用户直接在 Agent 提需求。

- [ ] **Step 2: 运行红测**

Run: `npm run test:unit -- src/components/feishu/__tests__/FeishuConnection.spec.ts`

Expected: FAIL，旧设置卡状态不足且含旧能力文案。

- [ ] **Step 3: 实现单一连接卡**

单卡展示真实 connection state、app id mask、三域最近状态；unknown 显示“尚未验证”；empty 说明具体能力首次使用时按需授权且“不包含消息发送”；解绑确认明确远端 app 保留。使用现有 AppButton/ConfirmModal 和设计 token。

- [ ] **Step 4: 绿测和 Commit**

Run: `npm run test:unit -- src/components/feishu/__tests__/FeishuConnection.spec.ts`

Expected: PASS。

```bash
git add src/components/feishu/FeishuConnection.vue src/components/feishu/__tests__/FeishuConnection.spec.ts
git commit -m "feat(feishu): show personal workspace status"
```

## Task 20: 清理旧固定飞书实现（二期；一期必须证明未进入 production graph）

**Files:** 删除 `internal/numind/biz/agent/tool_lark_create_doc.go`、`tool_lark_read_bitable.go`、`tool_lark_send_message.go`、`tool_lark_common.go`、`tool_lark_test.go`、`tool_feishu_connect.go`、`tool_feishu_connect_test.go`、`yield_authpause_test.go`；删除 `internal/numind/biz/feishu/api.go`、`api_test.go`、`client.go`、`client_test.go`、`ops_cli.go`、`auth_cli.go`、`auth_cli_test.go`、`provisioner.go`、`provisioner_test.go`、`provisioner_cli.go`、`provisioner_cli_test.go`、`connect_phase_from_home_test.go` 中已被新架构替代且无引用的代码。

- [ ] **Step 1: 先证明新 production graph 不引用旧实现**

Run: `go list -deps ./cmd/numind | rg 'biz/feishu'`

Expected: production composition 只构造 Task 1-15 新实现。再用 `rg` 核对每个待删 symbol 无新调用。

- [ ] **Step 2: 删除旧文件并保留必要纯 helper**

若 `decodeFirstJSON`、CLI error envelope 等纯 helper 仍被新实现使用，先移动到 `controlled_runner.go` 并由新 tests 覆盖，再删除旧文件。禁止保留旧 IM/broad auth 的可执行路径。

- [ ] **Step 3: 验证无旧工具/IM/broad auth**

Run: `rg -n 'lark_create_doc|lark_read_bitable|lark_send_message|feishu_connect|docs,im,base|im:message' internal/numind`

Expected: 零 production 命中。

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz/agent -count=1`

Expected: PASS。

- [ ] **Step 4: Commit 删除清单**

```bash
git add -A internal/numind/biz/feishu internal/numind/biz/agent/tool_lark_create_doc.go internal/numind/biz/agent/tool_lark_read_bitable.go internal/numind/biz/agent/tool_lark_send_message.go internal/numind/biz/agent/tool_lark_common.go internal/numind/biz/agent/tool_lark_test.go internal/numind/biz/agent/tool_feishu_connect.go internal/numind/biz/agent/tool_feishu_connect_test.go internal/numind/biz/agent/yield_authpause_test.go
git commit -m "refactor(feishu): remove fixed legacy tools"
```

## Task 21: 后端恢复链路集成测试

**Files:** `internal/numind/biz/feishu/personal_workspace_integration_test.go`、`internal/numind/biz/agent/lark_external_resume_integration_test.go`。

- [ ] **Step 1: 写完整集成测试**

覆盖 never-connected → create app → app scope → user auth → exact replay；connected 无 auth status；resource ACL 不 OAuth；重复 resume 单副作用；三个 phase 模拟 restart；双用户隔离；撤销后 reauth；解绑 generation 失效；写 timeout unknown；同 tool_call_id 恢复无第二条 argv。另用真实 MySQL 8 环境执行本 feature migration → AutoMigrate → information_schema schema diff，并并发验证 generation bump 与 `PutVaultCAS` 的 `SELECT ... FOR UPDATE` 互斥语义；SQLite 只承担逻辑单测，不作为该并发 Gate 的替代。

- [ ] **Step 2: 运行完整集成测试**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz/agent -run 'PersonalWorkspaceIntegration|LarkExternalResumeIntegration' -count=1`

Expected: PASS。Task 21 是纯测试任务，不允许修改生产文件；如果测试发现生产缺陷，立即停止 Task 21，在 plan/manifest 新增一个独立 fix task，完成双 review 后再返回本任务。

- [ ] **Step 3: 绿测和 Commit**

Run: `go test -race ./internal/numind/biz/feishu ./internal/numind/biz/agent -run 'PersonalWorkspaceIntegration|LarkExternalResumeIntegration' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/personal_workspace_integration_test.go internal/numind/biz/agent/lark_external_resume_integration_test.go
git commit -m "test(feishu): cover workspace recovery integration"
```

## Task 22: 前端 Playwright E2E（一期关键路径；二期扩展完整矩阵）

**Files:** `e2e/feishu-personal-workspace.spec.ts`。

- [ ] **Step 1: 写 mocked E2E**

一期：Mock status/SSE/resume/refresh，验证关键授权/恢复 happy path、过期刷新、resume 无 argv/scopes、成功回原任务、375×812 与 desktop 冒烟；trace 中不得出现普通 answer request。二期再补四阶段穷举、设置能力全矩阵、键盘操作和视觉回归。

- [ ] **Step 2: 运行并修复 E2E**

Run: `npm run test:e2e -- e2e/feishu-personal-workspace.spec.ts --project=chromium`

Expected: PASS，trace 中无普通 answer request。

- [ ] **Step 3: Commit**

```bash
git add e2e/feishu-personal-workspace.spec.ts
git commit -m "test(feishu): cover workspace authorization ui"
```

## Task 23: S4 最终质量 Gate

**Files:** 无；本任务只验证，不产生兜底 commit。

- [ ] **Step 1: 后端全量验证**

Run: `go test ./...`

Expected: PASS。

Run: `task lint`

Expected: exit 0，无 lint error。

- [ ] **Step 2: 前端全量验证**

Run in web worktree: `npm run test:unit`

Expected: PASS。

Run: `npm run lint && npm run type-check`

Expected: exit 0。

Run: `npm run test:e2e -- e2e/feishu-personal-workspace.spec.ts`

Expected: Chromium PASS，mobile/desktop assertions PASS。

- [ ] **Step 3: 状态与敏感信息检查**

Run: `git status --short` in both worktrees。

Expected: 无未提交业务文件。

Run: `rg -n 'token|app_secret|device_code|https://.*feishu' internal/numind/biz/feishu src/components/agent src/components/feishu`

Expected: 仅结构字段/测试 opaque fixture，无真实凭据或真实授权 URL。

- [ ] **Step 4: 本地执行隔离风险 Gate**

重复运行 runner 的取消、超时、正常 leader 退出 child-marker 测试，确认进程组残留清理无回归；核对生产只使用固定 hash/绝对路径/无 shell。明确复审 ADR 0004 中仍存在的 defense-in-depth P2：同 UID 主动 symlink/root rename、descendant `setsid` 逃逸、post-Wait PGID 理论复用。若首版信任固定官方 CLI 的边界不再成立，必须新增独立 fix task 实现 supervisor/cgroup/独立 UID sandbox，不得直接通过 Gate。

## Task 24: S5 本地真实飞书 Gate（不计入 S4 manifest progress）

**Files:** `.ndf/features/feishu-personal-workspace/s5-real-tenant-e2e.md`，不保存凭据或完整 URL。

- [ ] **Step 1: 在两个 feature worktree 启动本地环境**

本地数据库应用 migration；本地后端 `task dev`，前端 `npm run dev`；确认 lark-cli 1.0.68。不得先 merge 或部署 dev。

- [ ] **Step 2: 真实账号 E2E**

按顺序执行并记录脱敏结果：首次连接仅 offline_access；Docs 创建/读取/更新；Base 创建、表/字段/记录读取与更新；Wiki 创建节点、解析、读取/更新内容；app/user missing_scope；三个授权阶段重启；两个用户隔离；撤销后 reauth；写 timeout unknown；解绑后不可访问且无明文 HOME。

- [ ] **Step 3: 验证一次副作用与热路径**

同一 operation 重复点击 resume，飞书侧资源只出现一次；已连接且权限足够的操作 trace 中没有前置 `auth status`；第二次缺相同 scopes 不进入无限授权循环。

- [ ] **Step 4: 写 Gate 证据**

`s5-real-tenant-e2e.md` 记录每项 PASS/FAIL、operation 状态、脱敏资源类型、后端版本和 CLI 版本。不得记录测试账号、完整资源 URL、device code、Token、app secret、完整 app id。

- [ ] **Step 5: Gate 决策并停止本地服务**

只有 Docs/Base/Wiki 三域、重启、撤销、unknown、解绑、双用户隔离全部 PASS 才进入 S6。若错误结构无法证明写请求未产生副作用，关闭对应自动重放并回 S4 修复。完成后停止本地前后端。

- [ ] **Step 6: Commit Gate 证据**

```bash
git add .ndf/features/feishu-personal-workspace/s5-real-tenant-e2e.md
git commit -m "docs(feishu): record real tenant acceptance"
```

## S6 checklist（不属于实现 Task）

1. 在两个 worktree 分别运行 `ndf-done`，原子化 merge develop、push、清理分支/worktree。
2. 使用 `/deploy-dev server` 部署后端，使用前端 `/deploy-dev` 部署用户端。
3. 健康检查通过后，向用户提供基于 PRD 的 dev 验收步骤。
4. 只有用户确认 dev 产品可用后才进入 S7；生产部署仍需单独授权。

---

## Spec 覆盖索引

| Spec 要求 | 实施任务 |
|---|---|
| 每用户独立连接、generation、加密 HOME、多租户 | 1、2、7、8、13、14 |
| 固定 1.0.68、无 shell、限长、JSON fail closed | 3 |
| Docs/Base/Wiki create/read/update；无 IM/删除/raw API | 4、7、10、20 |
| exact scopes、增量授权、无热路径 auth status | 4、7、8 |
| 错误分类、ACL、不确定写结果 | 5、7 |
| operation 幂等、lease、跨重启恢复 | 1、7、8、21 |
| 官方技能与 receipt | 6、10 |
| 原 tool_call 精确结果恢复 | 9、10、11 |
| status/connect/resume/refresh/unbind API | 13 |
| 可观测性和敏感信息脱敏 | 15、23 |
| Agent 状态卡、设置页、可访问性、移动端 | 16、17、18、19、22 |
| 旧工具下线、集成测试、质量门禁 | 20、21、22、23 |
| 本地真实租户 E2E 与发布硬 Gate | 24、S6 checklist |

## 完成标准

- S4 Task 1-22 已完成并有对应 Conventional Commit；Task 23 质量 Gate 通过。Manifest `total_tasks=23` 只统计 S4。
- S5 Task 24 已通过并提交脱敏证据；S6 checklist 在 `ndf-done` 后执行。
- 后端 `go test ./...`、`task lint` 通过。
- 前端 unit、`npm run lint && npm run type-check`、目标 Playwright E2E 通过。
- Agent registry 只暴露 `lark_skill_read`、`lark_execute` 两个飞书工具。
- 已连接业务 trace 不包含前置 `auth status`。
- resume 请求不含 argv/scopes，Agent history 不新增“我已完成”用户消息。
- 真实租户 Gate 全部通过并完成脱敏记录。
