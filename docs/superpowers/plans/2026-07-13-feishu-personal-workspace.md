# 飞书个人工作空间连接 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让每个有数账号拥有独立、可长期保持的飞书个人工作空间连接，并让现有 Agent 在真实业务命令缺少连接或精确权限时引导用户授权、随后自动恢复原操作，首版支持 Docs、Base、Wiki 的创建、读取和更新。

**Architecture:** 现有 Agent 通过 `lark_skill_read` 读取固定版本官方技能，通过受控 `lark_execute` 提交结构化 argv；后端以命令目录、加密 vault、持久化 operation、DB lease 和确定性授权状态机执行。已连接热路径直接执行真实命令，不预跑 `auth status`；授权恢复只重放数据库中已加密保存的原 operation，并把结果回填原 tool call。

**Tech Stack:** Go 1.24、Gin、GORM、MySQL 8、AES-256-GCM、CloudWeGo Eino、lark-cli 1.0.68、Vue 3.4、TypeScript 5.4、Pinia、Vitest、Playwright。

**Spec:** `docs/superpowers/specs/2026-07-13-feishu-personal-workspace-design.md`

---

## 0. 实施边界与工作方式

- 后端只在 `/private/tmp/wt-feishu-personal-workspace-numind-server` 修改，分支 `feature/feishu-personal-workspace`。
- 前端只在 `/private/tmp/wt-feishu-personal-workspace-numind-web-v3` 修改，分支 `feature/feishu-personal-workspace`。
- 两个仓库分别提交；不要把前后端文件放入同一个 Git commit。
- 继续复用 `features.feishu_integration.enabled` 作为灰度开关；不新增第二个同义 feature flag。
- 不修改 `config_prod.yaml`，不把密钥、Token、device code、完整授权 URL、App Secret 或测试账号写入代码和测试快照。
- 旧 `feishu-integration` worktree 只作历史参考，不从它合并代码；本 worktree 中已经存在的飞书代码必须通过测试驱动逐步替换。
- 首版不注册 `lark_send_message`，不允许 IM、删除、权限管理、任意 `api`、`auth`、`config` 或 shell。
- 每个任务先红测、再最小实现、再绿测、再 commit。后端每次修改后至少跑相关 `go test`；任务 15 统一跑 `task lint`。前端每次修改后至少跑相关 Vitest；任务 15 统一跑 `npm run lint && npm run type-check`。

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
| `internal/numind/biz/feishu/runner.go` | 固定版本、无 shell、限时限长、JSON envelope 的 CLI 运行器 |
| `internal/numind/biz/feishu/runner_test.go` | fake binary contract tests |
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

- [ ] **Step 1: 写 store 红测**

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

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/store -run 'TestFeishuWorkspaceStore' -count=1`

Expected: FAIL，缺少 `newFeishuWorkspaceTestStore`、新 models 和 store 方法。

- [ ] **Step 3: 创建 migration 与 model**

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
	UserID uint `gorm:"primaryKey"`
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

- [ ] **Step 4: 实现 store 原语**

Store 接口固定为：

```go
type IFeishuWorkspaceStore interface {
	GetVault(ctx context.Context, userID uint, generation uint64) (*model.FeishuCLIVault, error)
	PutVaultCAS(ctx context.Context, vault *model.FeishuCLIVault, expectedRevision uint64) error
	DeleteVault(ctx context.Context, userID uint, generation uint64) error
	CreateSession(ctx context.Context, session *model.FeishuAuthSession) error
	GetSessionForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error)
	ClaimSession(ctx context.Context, id, owner string, now, leaseUntil time.Time) (bool, error)
	UpdateSessionState(ctx context.Context, id, owner, state string, completedAt *time.Time) error
	CreateOrGetOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error)
	GetOperationForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error)
	ClaimOperation(ctx context.Context, id, owner string, now, leaseUntil time.Time) (bool, error)
	TransitionOperation(ctx context.Context, id, owner, from []string, to string, fields map[string]any) error
	CancelPendingForGeneration(ctx context.Context, userID uint, generation uint64) error
}
```

使用 GORM transaction 和条件 UPDATE 检查 `RowsAffected == 1`。任何客户端可见 ID 的读取都走 `Get*ForUser`，查不到和归属不符都返回 `gorm.ErrRecordNotFound`。

- [ ] **Step 5: 绿测和 migration 静态检查**

Run: `go test ./internal/numind/store ./internal/pkg/model -run 'Feishu|ThirdParty' -count=1`

Expected: PASS。

Run: `rg -n 'uniq_feishu_operation_user_key|lease_until|request_ciphertext|generation' migrations/20260713_130000_feishu_personal_workspace.sql`

Expected: 四类字段/索引均命中。

- [ ] **Step 6: Commit**

```bash
git add migrations/20260713_130000_feishu_personal_workspace* internal/pkg/model internal/numind/store internal/numind/helper.go
git commit -m "feat(feishu): add personal workspace persistence"
```

## Task 2: AAD 加密与 EncryptedCLIHomeVault

**Files:** `internal/pkg/crypto/aesgcm.go`、`aesgcm_test.go`、`internal/numind/biz/feishu/vault.go`、`vault_test.go`。

- [ ] **Step 1: 写加密与 vault 红测**

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

另测：tar 中 `../escape`、绝对路径、symlink 一律拒绝；其他用户/generation 无法解密；CAS 冲突不覆盖新快照；callback 出错也清理临时目录；文件解封后统一收紧到 `0600`。

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/pkg/crypto ./internal/numind/biz/feishu -run 'CipherAAD|EncryptedCLIHomeVault' -count=1`

Expected: FAIL，缺少 AAD API 和 vault。

- [ ] **Step 3: 实现 AAD API**

```go
func (c *Cipher) EncryptWithAAD(plaintext, aad []byte) ([]byte, error)
func (c *Cipher) DecryptWithAAD(ciphertext, aad []byte) ([]byte, error)
```

旧 `Encrypt`/`Decrypt` 调用新方法并传 `nil`，保证其他业务零回归。Vault AAD 精确编码为 `lark|<userID>|<generation>|<keyVersion>`，checksum 使用密文 SHA-256 hex。

- [ ] **Step 4: 实现安全打包和 WithHome**

`WithHome` 固定算法：读取当前连接 generation → 取 vault → 校验 checksum → AAD 解密 → 新建随机 temp HOME → chmod 0700 → 安全解包并将普通文件 chmod 0600 → 执行 callback → 仅当 callback 报告 changed 时重新打包、加密并 CAS → defer `os.RemoveAll`。任何解包条目经 `filepath.Clean` 后必须仍在 temp HOME 内，拒绝非普通文件/目录。

- [ ] **Step 5: 绿测**

Run: `go test ./internal/pkg/crypto ./internal/numind/biz/feishu -run 'Cipher|Vault' -count=1`

Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/crypto internal/numind/biz/feishu/vault.go internal/numind/biz/feishu/vault_test.go
git commit -m "feat(feishu): encrypt per-user cli homes"
```

## Task 3: 固定 lark-cli 1.0.68 与受控 Runner

**Files:** `Dockerfile`、`internal/numind/biz/feishu/runner.go`、`runner_test.go`；替换旧 runner 的调用点但暂不删除旧固定业务工具。

- [ ] **Step 1: 写 runner contract 红测**

测试 fake executable 的 argv/env/stdout/stderr，断言：启动先校验版本 `1.0.68`；调用使用 `exec.CommandContext(binary, argv...)`；环境含隔离 HOME、`LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1`；stdout/stderr 分别限长；exit 0 + `ok:false` 仍失败；非 JSON、超限、timeout 失败；中文和空格保持一个 argv 元素。

```go
func TestLarkCLIRunner_DoesNotUseShell(t *testing.T) {
	r := newFakeBinaryRunner(t, `{"ok":true,"data":{"argv":["docs","+create","a; touch /tmp/pwned"]}}`)
	_, err := r.Run(context.Background(), testHome(t), []string{"docs", "+create", "a; touch /tmp/pwned"}, nil)
	require.NoError(t, err)
	require.NoFileExists(t, "/tmp/pwned")
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'LarkCLIRunner' -count=1`

Expected: FAIL，旧 runner 没有通用受控 Run 和版本 fail-closed。

- [ ] **Step 3: 实现 runner 和 envelope**

固定类型：

```go
const LarkCLIVersion = "1.0.68"

type CLIEnvelope struct {
	OK bool `json:"ok"`
	Data json.RawMessage `json:"data,omitempty"`
	Error *CLIError `json:"error,omitempty"`
}

func (r *LarkCLIRunner) VerifyVersion(ctx context.Context) error
func (r *LarkCLIRunner) Run(ctx context.Context, home string, argv []string, stdinJSON []byte) (*CLIResult, error)
```

写命令开始后若 context timeout、进程被杀、输出损坏或无完整 envelope，`CLIResult` 必须保留 `InvocationStarted=true` 供 operation service 判定 unknown。

- [ ] **Step 4: 更新 Dockerfile**

固定：

```dockerfile
ARG LARK_CLI_VERSION=1.0.68
ARG LARK_CLI_SHA256=8daaeb11b7cadcc77f07fd9ae7948f6c370e8305337888cb930ac7362a05cad8
```

保留下载后 `sha256sum -c -` 和 `lark-cli version` 构建检查，更新注释为 Docs/Base/Wiki 受控执行，不再描述 IM 或持久明文 HOME。

- [ ] **Step 5: 绿测和 Dockerfile 检查**

Run: `go test ./internal/numind/biz/feishu -run 'LarkCLIRunner' -count=1`

Expected: PASS。

Run: `rg -n '1\.0\.68|8daaeb11b7cadcc77f07fd9ae7948f6c370e8305337888cb930ac7362a05cad8' Dockerfile`

Expected: version 与 hash 均命中。

- [ ] **Step 6: Commit**

```bash
git add Dockerfile internal/numind/biz/feishu/runner.go internal/numind/biz/feishu/runner_test.go
git commit -m "feat(feishu): pin and harden lark cli runner"
```

## Task 4: CommandCatalog、exact scopes 与参数风险

**Files:** `command_catalog.go`、`command_catalog_test.go`。

- [ ] **Step 1: 写完整 catalog 红测**

测试表逐项覆盖设计 spec §5。最低允许集合：Docs `+create/+fetch/+update`；Base 的 app/table/field/view/record create/get/list/search/upsert/update；Wiki space/node create/get/list 及 Wiki 内容经 Docs fetch/update。拒绝含 delete/remove/trash/purge、IM、`api/auth/config`、`--as/--home/--profile/--brand`、未知 flag、超过 20 条 record 批次、Docs `block_delete`、已有资源无确认的 overwrite。

```go
func TestCommandCatalog_PermanentDenials(t *testing.T) {
	c := NewCommandCatalog()
	for _, argv := range [][]string{
		{"im", "+messages-send"}, {"api", "post", "/x"}, {"auth", "status"},
		{"docs", "+update", "doc", "--command", "block_delete"},
		{"base", "+record-delete", "app", "table", "record"},
	} {
		_, err := c.Normalize(argv, nil)
		require.ErrorIs(t, err, ErrCommandDenied, "%v", argv)
	}
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'CommandCatalog' -count=1`

Expected: FAIL，catalog 不存在。

- [ ] **Step 3: 实现 catalog**

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
	ReplaySafeOnAuthError bool
}
```

Catalog 自行计算 scopes/risk，忽略模型声明；末尾统一追加 `--as user`。Flag parser 必须逐命令允许，不使用“未知 flag 原样透传”。URL/token 只接受飞书支持 host 和 spec 允许的 opaque token 形状；正文长度、数组长度、分页上限和 stdout 上限写成命名常量。

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
	"base +record-create": {"base:record:create"},
	"base +record-upsert": {"base:record:create", "base:record:update"},
	"base +record-update": {"base:record:update"},
}

var wikiScopes = map[string][]string{
	"wiki +space-create": {"wiki:space:write_only"},
	"wiki +node-create":  {"wiki:node:create", "wiki:node:read", "wiki:space:read"},
	"wiki +node-get":     {"wiki:node:retrieve"},
	"wiki +node-list":    {"wiki:node:retrieve"},
}
```

`base:table:delete` 只因 lark-cli 1.0.68 `+base-create` 替换默认表而出现在授权说明中；catalog 仍必须拒绝所有删除命令。改变既有字段类型、超过 20 条 record 写入和覆盖既有 Doc 进入 `waiting_confirmation`；该确认复用 Agent 通用高风险确认，不借 OAuth 页面替代。

- [ ] **Step 4: 写版本 manifest 快照测试**

`TestCommandCatalogManifest_1068` 序列化 `path/domain/scopes/risk/limits` 并与 testdata 固定 JSON 比较。任何 CLI 升级必须显式更新该快照和审阅差异。

- [ ] **Step 5: 绿测**

Run: `go test ./internal/numind/biz/feishu -run 'CommandCatalog' -count=1`

Expected: PASS，拒绝表全部通过。

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/command_catalog* internal/numind/biz/feishu/testdata
git commit -m "feat(feishu): enforce docs base wiki command policy"
```

## Task 5: 结构化错误分类与 unknown 写保护

**Files:** `error_classifier.go`、`error_classifier_test.go`、`.ndf/features/feishu-personal-workspace/fixtures/*.json`。

- [ ] **Step 1: 把 S2 脱敏错误保存为 fixtures 并写红测**

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

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'ErrorClassifier' -count=1`

Expected: FAIL，classifier 不存在。

- [ ] **Step 3: 实现固定 code/subtype 分类表**

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

- [ ] **Step 4: 绿测**

Run: `go test ./internal/numind/biz/feishu -run 'ErrorClassifier' -count=1`

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/feishu/error_classifier* .ndf/features/feishu-personal-workspace/fixtures
git commit -m "feat(feishu): classify cli recovery errors"
```

## Task 6: FeishuOperationService 幂等执行和精确重放

**Files:** `operation_service.go`、`operation_service_test.go`。

- [ ] **Step 1: 写 operation 红测**

覆盖：本地 connected 直接调用真实业务命令且 `AuthStatus` 调用数为 0；none 直接 waiting_connection；同 user+key 并发 20 次只有一次 runner 调用；授权错误保存规范化请求并等待；resume 读取密文原请求而不是接收新 argv；写 timeout → unknown 且重复 resume 不执行；读 timeout 最多有界重试；generation 不符取消；成功结果幂等返回。

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

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'OperationService' -count=1`

Expected: FAIL，service 不存在。

- [ ] **Step 3: 实现请求、结果和执行算法**

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
	Action *ExternalAction `json:"action,omitempty"`
}
```

执行顺序固定为：鉴权 context 的 userID → 验 receipt → catalog Normalize → 加密规范化请求 → CreateOrGetOperation → terminal 则返回旧结果 → claim lease → connection 明确 none 时创建 auth session → connected 时直接 `vault.WithHome` + runner.Run → classifier → 只对 proven-no-side-effect 授权错误进入 waiting → 写入结果或 unknown → release/finish lease。日志只写 operation id、path、domain、risk、state、错误 code，不写 argv/stdin/data。

High-risk command 在 runner 前转 `waiting_confirmation` 并调用现有 Agent 通用确认契约；用户确认后仍从加密 request 恢复，拒绝后转 cancelled。增加清理入口：成功 operation 的 request/result 正文在 Agent 消费后可立即擦除，其他密文最多保留 7 天；审计摘要不含正文、URL 或资源字段值。

- [ ] **Step 4: 实现 Resume**

`Resume(ctx,userID,operationID)` 只能读取已存 request ciphertext，拒绝 body 中 argv/scopes；等待 session 未完成时返回现状；session completed 时 CAS waiting → executing 后重放；terminal 时原样返回摘要。重复点击和 auth worker 自动回调共享同一入口。

- [ ] **Step 5: 绿测和 race**

Run: `go test -race ./internal/numind/biz/feishu -run 'OperationService' -count=1`

Expected: PASS，无 data race，同幂等键 runner 调用一次。

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/operation_service*
git commit -m "feat(feishu): execute idempotent personal operations"
```

## Task 7: AuthSessionService 与确定性连接编排

**Files:** `auth_session_service.go`、`auth_session_service_test.go`、改写 `connect_orchestrator.go` 及测试。

- [ ] **Step 1: 写状态机红测**

覆盖：manual connect 只申请 `offline_access`；业务 operation 只使用 catalog exact scopes；create_app、app_scope、user_auth 三 phase；URL 只存在 worker 内存和返回值，不落 DB；session lease 丢失后 supersede 并生成新链接；auth worker 正常退出自动调用 operation Resume；用户点击完成调用同一 Resume；相同 scopes 连续两次不循环；本租户无管理员步骤可直接跳过；有 `console_url` 时显示 waiting_app_approval。

```go
func TestAuthSessionService_ManualConnectRequestsOfflineAccessOnly(t *testing.T) {
	h := newAuthHarness(t)
	action, err := h.service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, []string{"offline_access"}, h.worker.RequestedScopes)
	require.Equal(t, "user_auth", action.Phase)
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'AuthSession|ConnectOrchestrator' -count=1`

Expected: FAIL，旧 orchestrator 仍以 HOME/in-memory session 为真相并请求 broad domains。

- [ ] **Step 3: 实现 DB 状态机和 worker**

`AuthSessionService` 依赖 store、vault、runner、instanceID、clock、operation resumer。Blocking worker 持 DB lease；从 CLI 输出解析原始 URL 后通过内存 registry 通知 caller/UI；DB 仅保存 phase/scopes/state/lease/expires_at。worker 成功后 seal HOME、session completed、调用 `Resume`。进程/实例丢失时 recovery job 将过期 lease session 标 superseded，先用连接恢复专用 `auth status` 检查 vault，再决定完成或新建链接。

- [ ] **Step 4: 删除 broad domain 行为**

删除 `auth login --domain docs,im,base`。内部 auth 命令只能由 orchestrator 生成：

```go
[]string{"auth", "login", "--json", "--scope", strings.Join(exactScopes, " ")}
```

Business runner/catalog 永远不能接受 `auth` argv。

- [ ] **Step 5: 绿测**

Run: `go test -race ./internal/numind/biz/feishu -run 'AuthSession|ConnectOrchestrator' -count=1`

Expected: PASS，IM scope/`--domain` 在 production path 不出现。

Run: `rg -n -- '--domain|docs,im,base|im:message' internal/numind/biz/feishu`

Expected: 只允许历史测试说明命中；任何可执行生产代码命中必须删除。

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/auth_session_service* internal/numind/biz/feishu/connect_orchestrator*
git commit -m "feat(feishu): orchestrate incremental authorization"
```

## Task 8: 官方技能读取与签名 receipt

**Files:** `skill_reader.go`、`skill_reader_test.go`。

- [ ] **Step 1: 写 reader 红测**

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

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/feishu -run 'SkillReader|SkillReceipt' -count=1`

Expected: FAIL，reader 不存在。

- [ ] **Step 3: 实现 reader 和 receipt signer**

技能内容从固定 lark-cli 1.0.68 自带 `skills read` 读取，不走用户 HOME、不联网。Cursor 使用 HMAC 签名的 `skill|reference|offset|runID|expiresAt`；单页最大 32 KiB；只有最后一页签发 receipt。Receipt HMAC key 从现有第三方密钥经 domain separation `HMAC(key,"feishu-skill-receipt-v1")` 派生，不新增明文配置。

- [ ] **Step 4: 实现执行前 required receipt**

Domain 映射固定：Docs 要 `lark-shared+lark-doc`；Base 要 `lark-shared+lark-base`；Wiki 命令要 `lark-shared+lark-wiki`，Wiki 内容经 Docs 操作时还要 `lark-doc`。Operation service 在持久化/执行前完成校验，缺失返回 `skill_required`，不创建副作用 operation。

- [ ] **Step 5: 绿测**

Run: `go test ./internal/numind/biz/feishu -run 'SkillReader|SkillReceipt' -count=1`

Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/skill_reader*
git commit -m "feat(feishu): read versioned official lark skills"
```

## Task 9: Agent 工具与 tool call context

**Files:** `tool_call_ctx.go`、`adapter_full_to_eino.go`、`tool_lark_skill_read.go`、`tool_lark_execute.go`、`factory_platform.go` 和测试。

- [ ] **Step 1: 写 adapter/tool 红测**

断言 FullTool.Execute 能读到 adapter 生成的同一 synthetic toolCallID；`lark_execute` 不接受 user_id；idempotency key 固定为 `<runID>:<toolCallID>`，不信任模型输入；waiting 返回 external action yield；skill read 不需要连接；两个工具 soft error 不终止 Agent。

```go
func TestFullToolAdapterInjectsToolCallID(t *testing.T) {
	ft := &capturesToolCallID{}
	_, err := adaptFullToEinoTool(ft, nil).InvokableRun(WithRunID(context.Background(), 9), `{}`)
	require.NoError(t, err)
	require.NotEmpty(t, ft.got)
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/agent -run 'ToolCallID|LarkPersonalWorkspace' -count=1`

Expected: FAIL，context helper 与新工具不存在。

- [ ] **Step 3: 注入 tool call ID**

```go
func WithToolCallID(ctx context.Context, id string) context.Context
func ToolCallIDFromContext(ctx context.Context) string
```

在 `adapter_full_to_eino.go` 唯一 Execute 调用点改为 `invokeToolGuarded(WithToolCallID(ctx, toolCallID), input)`；SSE/narration 继续复用同一 ID。

- [ ] **Step 4: 实现两个 FullTool**

`lark_skill_read` input 为 `skill/reference/cursor`；`lark_execute` input 只含 `argv/stdin_json/skill_receipts`。后者从 middleware context 取 userID，从 Agent context 取 runID/toolCallID，内部生成 idempotency key。任何缺 context ID 都返回 soft error，不降级生成随机业务 key。

- [ ] **Step 5: 收缩 Agent 工具目录**

`factory_platform.go` 只向模型注册 `lark_skill_read` 和 `lark_execute`。移除 `lark_create_doc`、`lark_read_bitable`、`lark_send_message`、`feishu_connect` 的注册；旧源文件可在本任务保留以减小迁移风险，但测试必须断言 registry 查不到这四个名字，尤其不能查到 IM。

- [ ] **Step 6: 绿测**

Run: `go test ./internal/numind/biz/agent -run 'ToolCallID|LarkPersonalWorkspace|PlatformTool' -count=1`

Expected: PASS。

- [ ] **Step 7: Commit**

```bash
git add internal/numind/biz/agent/tool_call_ctx* internal/numind/biz/agent/adapter_full_to_eino.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/agent/factory_platform.go
git commit -m "feat(agent): add controlled lark workspace tools"
```

## Task 10: 外部操作暂停与原 tool call 精确恢复

**Files:** `yield_error.go`、`agent_run.go` model/store、`external_tool_resume.go`、相关 tests、runner snapshot/SSE 代码。

- [ ] **Step 1: 写恢复红测**

场景：`lark_execute` 返回 waiting_user_auth；run 持久化 `operation_id/tool_call_id/phase/expires_at`；resume 成功后原子清等待态并向 messages 添加 role=tool、相同 tool_call_id、真实 result；恢复 Agent 时没有新增 role=user “已完成”；runner 不再次调用 `lark_execute` 生成 argv；原 run 已取消时只保存结果状态不新建 run。

```go
func TestExternalToolResumeAppendsOriginalToolResultWithoutUserAnswer(t *testing.T) {
	h := newExternalResumeHarness(t)
	require.NoError(t, h.resumer.Resume(h.ctx, ExternalToolResult{
		RunID: 41, ToolCallID: "tc-9", OperationID: "op-1", Result: json.RawMessage(`{"ok":true}`),
	}))
	msgs := h.store.Messages(41)
	require.Equal(t, "tool", msgs[len(msgs)-1].Role)
	require.Equal(t, "tc-9", msgs[len(msgs)-1].ToolCallID)
	require.NotContains(t, string(h.store.RawMessages(41)), "我已完成")
	require.Zero(t, h.larkExecuteCalls)
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/biz/agent ./internal/numind/store -run 'ExternalTool|ExternalAction' -count=1`

Expected: FAIL，当前 auth pause 走普通 question answer。

- [ ] **Step 3: 扩展 yield/SSE 契约**

```go
type ExternalActionPayload struct {
	Provider string `json:"provider"`
	OperationID string `json:"operation_id"`
	SessionID string `json:"session_id"`
	Phase string `json:"phase"`
	URL string `json:"url,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type YieldPayload struct {
	Questions []YieldQuestion `json:"questions,omitempty"`
	PauseType string `json:"pause_type,omitempty"`
	ExternalAction *ExternalActionPayload `json:"external_action,omitempty"`
}
```

普通 question 继续走 answer API；external action 不允许 answer API 消费。授权 URL 可出现在当前 SSE/消息卡 payload，但不得写 `agent_run.pending_question_json` 的长期字段；长期快照只保存 session/operation/phase/expiry，刷新链接从 action endpoint 获取。

- [ ] **Step 4: 实现原子 store 与 resumer**

`AgentRun` 增加以下固定字段；`IAgentRunStore` 增加两个固定方法：

```go
PendingExternalActionJSON datatypes.JSON `gorm:"type:json;column:pending_external_action_json" json:"pending_external_action_json,omitempty"`
PendingExternalActionAt *time.Time `gorm:"column:pending_external_action_at" json:"pending_external_action_at,omitempty"`

UpdatePendingExternalAction(ctx context.Context, runID uint64, payloadJSON []byte) error
ResumeExternalTool(ctx context.Context, runID uint64, operationID, toolCallID string, resultTurn json.RawMessage) (bool, error)
```

Store transaction 锁 agent_run，验证等待 JSON 中的 operation/toolCallID，append 由 `schema.ToolMessage(string(result), toolCallID)` 序列化出的 tool turn，清 external waiting，状态回 running。返回 bool=false 表示相同 operation 已恢复过，调用方幂等结束。`AgentRunResumer` 调现有 runner 的非流式恢复入口，输入为更新后的 history，不追加自然语言。

- [ ] **Step 5: 绿测**

Run: `go test -race ./internal/numind/biz/agent ./internal/numind/store -run 'ExternalTool|ExternalAction' -count=1`

Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/agent internal/numind/store/agent_run.go internal/pkg/model/agent_run.go
git commit -m "feat(agent): resume external tool calls exactly"
```

## Task 11: HTTP API、解绑、旧 HOME 迁移和 composition root

**Files:** controller/router/service/adapter/biz、`home_migrator.go`、tests。

- [ ] **Step 1: 写 HTTP 与 service 红测**

覆盖 GET status 不生成 URL；POST connect body 只允许 intent=manual；POST operation resume 不接受 argv/scopes；POST action refresh 校验归属并 supersede 旧 session；跨用户 ID 统一 404；DELETE generation+1、取消等待、删除 vault、停止 worker、清能力，返回远端 app 保留说明。

```go
func TestResumeRejectsCrossUserAsNotFound(t *testing.T) {
	r := newFeishuHTTPHarness(t, 8)
	resp := r.POST("/v1/feishu/operations/op-owned-by-7/resume", `{"action":"user_completed"}`)
	require.Equal(t, http.StatusNotFound, resp.Code)
}
```

- [ ] **Step 2: 运行红测**

Run: `go test ./internal/numind/controller/v1/feishu ./internal/numind/biz/feishu -run 'Status|Connect|Resume|Refresh|Unbind|HomeMigrator' -count=1`

Expected: FAIL，新 API 和迁移器缺失。

- [ ] **Step 3: 实现 service/controller/router**

注册：

```go
feishuAuthGroup.GET("/status", feishuCtrl.Status)
feishuAuthGroup.POST("/connect", feishuCtrl.Connect)
feishuAuthGroup.POST("/operations/:id/resume", feishuCtrl.ResumeOperation)
feishuAuthGroup.POST("/actions/:session_id/refresh", feishuCtrl.RefreshAction)
feishuAuthGroup.DELETE("/connection", feishuCtrl.Unbind)
```

Controller 只解析登录 userID、path/body DTO 和写 response；状态推进全部调用 biz service。Status 返回 spec §10.1 的 state/connected/app_id_masked/cli_version/capabilities/active_action，active action 的 `link_available` 不含 URL。

- [ ] **Step 4: 实现安全解绑**

事务先置 disconnecting 并 generation+1，再取消旧 generation 等待项；执行中写操作按 unknown 收口；尽力运行 logout/remove，但远端失败不保留本地 vault；删除 vault 和 temp HOME；最终 state none/connected false。响应文案明确远端个人应用仍保留。

- [ ] **Step 5: 实现旧 HOME 迁移**

启动时仅在 feature flag 开启且 runner 版本正确时扫描配置的旧 `u<userID>` 目录。逐用户锁定 → 权限校验 → 打包加密 → PutVaultCAS → 立刻解密 checksum 自检 → 成功才删除旧目录。失败时保留旧目录但该用户新 feature fail closed，不能让新旧 runner 同时使用。测试不得使用真实凭据。

- [ ] **Step 6: 组合依赖并删除旧热路径 auth gate**

`buildFeishuService` 构造 cipher/store/vault/runner/catalog/classifier/skill reader/auth/operation/resumer；启动时 VerifyVersion 失败则服务不注册。删除 `Client.gate` 每次调用 `AuthStatus` 的生产使用，设置页 refresh/recovery 才可调用 status。

在现有 trace provider 上增加 `tool.lark_skill_read`、`tool.lark_execute`、`feishu.operation.execute`、`feishu.connect`、`feishu.auth`、`feishu.operation.resume`、`feishu.vault.open`、`feishu.vault.seal` spans。Metric 标签只含 domain/path/risk/state/error code/CLI version；禁止把 argv、stdin、内容、完整 URL、完整 app id 或密文作为 tag。相同 scope 授权循环和 unknown 写操作必须有独立 counter。

- [ ] **Step 7: 绿测**

Run: `go test ./internal/numind/controller/v1/feishu ./internal/numind/biz/feishu ./internal/numind/biz -count=1`

Expected: PASS。

- [ ] **Step 8: Commit**

```bash
git add internal/numind/controller/v1/feishu internal/numind/router.go internal/numind/biz/feishu internal/numind/biz/feishu_adapter.go internal/numind/biz/biz.go
git commit -m "feat(feishu): expose personal workspace lifecycle api"
```

## Task 12: 前端改动前 Playwright 运行时诊断

**Files:** 只生成不进 Git 的诊断输出；不改业务代码。

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

## Task 13: 前端 API 与 Pinia 状态契约

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
  phase: 'create_app' | 'app_scope' | 'user_auth'
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

## Task 14: Agent 飞书 Action Card 与设置页

**Files:** `FeishuActionCard.vue`、`AgentMessageItem.vue`、`FeishuConnection.vue` 及 tests。

- [ ] **Step 1: 写组件红测**

覆盖 create_app/app_scope/user_auth/继续原任务四阶段；URL 展示值和复制值完整一致；二维码 payload 与 URL 字节相同；过期后禁用旧 continue 并显示刷新；resume/refresh 独立 emit；`aria-live=polite`、错误 `role=alert`；375px 无横向溢出；设置页 loading/empty/error/success；解绑用现有 ConfirmModal；页面不出现“消息/IM/发送飞书消息”。

```ts
it('emits operation resume instead of a question answer', async () => {
  const wrapper = mount(FeishuActionCard, { props: { action: waitingUserAuthAction } })
  await wrapper.get('[data-testid="feishu-continue"]').trigger('click')
  expect(wrapper.emitted('resume')).toEqual([['op-1']])
  expect(wrapper.emitted('answer-submitted')).toBeUndefined()
})
```

- [ ] **Step 2: 运行红测**

Run: `npm run test:unit -- src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts src/components/feishu/__tests__/FeishuConnection.spec.ts`

Expected: FAIL，新卡不存在且旧卡走 answer-submitted。

- [ ] **Step 3: 实现 FeishuActionCard**

用现有 `AppButton` 和 `qrcode`，不引入 UI 框架。卡片使用 `var(--surface)`、`var(--border)`、`var(--shadow-card)`、`var(--radius-md)`、T-shirt spacing token；主 CTA 使用翠绿 `var(--primary)`；heading 使用 `var(--font-heading)`。阶段文案严格采用 spec §12.1；URL 使用 `overflow-wrap:anywhere`；二维码容器在移动端限制 `max-width:100%`。

- [ ] **Step 4: 接入 AgentMessageItem**

`external_action` 渲染新卡；普通 `pause_type=question` 仍渲染 QuestionPrompt。删去旧 `pause_type=auth` → ordinary answer 的桥接代码。resume 调 store operation API，结果更新原 message，不创建用户气泡。

- [ ] **Step 5: 重写 FeishuConnection**

单卡展示真实 connection state、脱敏 app id、Docs/Base/Wiki 最近状态、连接/继续连接/重新授权/解绑。empty 文案说明可直接在 Agent 里提出飞书任务；明确“具体能力首次使用时按需授权，不包含消息发送”；unknown 显示“尚未验证”，不能显示已授权。

- [ ] **Step 6: 绿测**

Run: `npm run test:unit -- src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts src/components/feishu/__tests__/FeishuConnection.spec.ts`

Expected: PASS。

- [ ] **Step 7: Commit（前端仓库）**

```bash
git add src/components/agent src/components/feishu
git commit -m "feat(feishu): add recoverable authorization experience"
```

## Task 15: 自动化集成、质量门禁与旧工具清理

**Files:** 后端集成测试、前端 E2E、删除未再引用的旧 lark 工具/runner 文件。

- [ ] **Step 1: 写后端集成测试**

覆盖 never-connected → create app → app scope → user auth → exact replay；connected hot path 无 auth status；resource ACL 不触发 OAuth；重复 resume 无重复副作用；三个阶段模拟 process restart；两个用户隔离；撤销后 reauth；解绑后旧 generation 失效；写 timeout unknown。

- [ ] **Step 2: 写 mocked Playwright E2E**

`e2e/feishu-personal-workspace.spec.ts` mock status/SSE/resume/refresh，验证：对话卡四阶段；完整 URL/二维码；过期刷新；resume request 无 argv/scopes；成功回到原任务；设置页能力状态；375×812 和 desktop；键盘可操作。

- [ ] **Step 3: 删除旧工具暴露和死代码**

确认没有调用后删除 `tool_lark_create_doc.go`、`tool_lark_read_bitable.go`、`tool_lark_send_message.go`、`tool_feishu_connect.go` 及对应旧 tests；删除旧 `client.go/ops_cli.go/auth_cli.go/provisioner_cli.go` 中被新实现完全替代的路径。保留仍被新 runner/orchestrator复用的纯解析 helper，并改成职责明确的小文件。

Run: `rg -n 'lark_create_doc|lark_read_bitable|lark_send_message|feishu_connect|docs,im,base|im:message' internal/numind`

Expected: 不再有模型注册、production IM scope 或旧 broad auth；允许 migration/spec 注释以外零命中。

- [ ] **Step 4: 后端全量验证**

Run: `go test ./...`

Expected: PASS。

Run: `task lint`

Expected: exit 0，无 lint error。

- [ ] **Step 5: 前端全量验证**

Run in web worktree: `npm run test:unit`

Expected: PASS。

Run: `npm run lint && npm run type-check`

Expected: exit 0。

Run: `npm run test:e2e -- e2e/feishu-personal-workspace.spec.ts`

Expected: Chromium PASS，mobile/desktop assertions PASS。

- [ ] **Step 6: 分仓 Commit**

Backend:

```bash
git add internal migrations Dockerfile .ndf
git commit -m "test(feishu): cover personal workspace recovery"
```

Frontend:

```bash
git add e2e src
git commit -m "test(feishu): cover workspace authorization flow"
```

## Task 16: dev 部署与真实飞书发布 Gate

**Files:** `.ndf/features/feishu-personal-workspace/s5-real-tenant-e2e.md`，不保存凭据或完整 URL。

- [ ] **Step 1: 部署前 DB 和二进制检查**

在 dev 运行 migration，确认三表/新列存在；构建镜像中运行 `lark-cli version`，必须为 1.0.68；检查应用启动日志只有版本/状态，没有 argv、正文、URL、app id 或 token。

- [ ] **Step 2: 部署 backend 与 frontend 到 dev**

分别使用项目 `/deploy-dev server` 和前端 `/deploy-dev` 工作流。健康检查通过后再开始真实写操作。

- [ ] **Step 3: 真实账号 E2E**

按顺序执行并记录脱敏结果：首次连接仅 offline_access；Docs 创建/读取/更新；Base 创建、表/字段/记录读取与更新；Wiki 创建节点、解析、读取/更新内容；app/user missing_scope；三个授权阶段重启；两个用户隔离；撤销后 reauth；写 timeout unknown；解绑后不可访问且无明文 HOME。

- [ ] **Step 4: 验证一次副作用与热路径**

同一 operation 重复点击 resume，飞书侧资源只出现一次；已连接且权限足够的操作 trace 中没有前置 `auth status`；第二次缺相同 scopes 不进入无限授权循环。

- [ ] **Step 5: 写 Gate 证据**

`s5-real-tenant-e2e.md` 记录每项 PASS/FAIL、operation 状态、脱敏资源类型、后端版本和 CLI 版本。不得记录测试账号、完整资源 URL、device code、Token、app secret、完整 app id。

- [ ] **Step 6: Gate 决策**

只有 Docs/Base/Wiki 三域、重启、撤销、unknown、解绑、双用户隔离全部 PASS 才进入 NDF S5/S6。若错误结构无法证明写请求未产生副作用，将对应写操作自动重放关闭并更新 CommandCatalog/测试/设计 ADR 后重新验收。

- [ ] **Step 7: Commit**

```bash
git add .ndf/features/feishu-personal-workspace/s5-real-tenant-e2e.md
git commit -m "docs(feishu): record real tenant acceptance"
```

---

## Spec 覆盖索引

| Spec 要求 | 实施任务 |
|---|---|
| 每用户独立连接、generation、加密 HOME、多租户 | 1、2、6、11 |
| 固定 1.0.68、无 shell、限长、JSON fail closed | 3 |
| Docs/Base/Wiki create/read/update；无 IM/删除/raw API | 4、9、15 |
| exact scopes、增量授权、无热路径 auth status | 4、6、7 |
| 错误分类、ACL、不确定写结果 | 5、6 |
| operation 幂等、lease、跨重启恢复 | 1、6、7 |
| 官方技能与 receipt | 8、9 |
| 原 tool_call 精确结果恢复 | 9、10 |
| status/connect/resume/refresh/unbind API | 11 |
| Agent 状态卡、设置页、可访问性、移动端 | 12、13、14 |
| 旧工具下线、质量门禁 | 15 |
| 真实租户 E2E 与发布硬 Gate | 16 |

## 完成标准

- 所有 16 个任务 checkbox 已完成并有对应 Conventional Commit。
- 后端 `go test ./...`、`task lint` 通过。
- 前端 unit、`npm run lint && npm run type-check`、目标 Playwright E2E 通过。
- Agent registry 只暴露 `lark_skill_read`、`lark_execute` 两个飞书工具。
- 已连接业务 trace 不包含前置 `auth status`。
- resume 请求不含 argv/scopes，Agent history 不新增“我已完成”用户消息。
- 真实租户 Gate 全部通过并完成脱敏记录。
