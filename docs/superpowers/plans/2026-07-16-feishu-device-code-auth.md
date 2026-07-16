# 飞书 Device Code 授权闭环 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将飞书 `user_auth` 从不可恢复的单进程阻塞模式改为 lark-cli 1.0.68 的两段式 device-code 协议，让用户授权后可以在同一张卡片中安全继续原始 Docs/Base/Wiki 操作，并且无需刷新页面就能看到 Agent 后续处理和正式回复。

**Architecture:** 保留现有 create-app、app-scope、operation、Agent continuation 和 Docs/Base/Wiki 执行链；新增聚焦的 `DeviceAuthFlow`，用短进程生成 URL 和 device code、加密持久化续传凭据，再由用户点击继续触发第二段 CLI。成功时先生成未发布的 HOME candidate，再通过 account → session → operation → vault 的同一事务围栏发布；预期中的 pending/processing/rejected/expired 通过 200 + 安全 `notice_code` 返回，前端只原位更新现有卡片。

**Tech Stack:** Go 1.24、Gin、GORM、MySQL 8、AES-256-GCM、lark-cli 1.0.68、Vue 3.4、TypeScript 5.4、Pinia、Vitest、Playwright。

**Spec:** `docs/superpowers/specs/2026-07-16-feishu-device-code-auth-design.md`

---

## 0. 固定边界、工作目录与提交纪律

- 后端代码只在 `/private/tmp/wt-feishu-device-code-auth-numind-server` 修改，分支 `feature/feishu-device-code-auth`。
- 前端代码只在 `/private/tmp/wt-feishu-device-code-auth-numind-web-v3` 修改，分支 `feature/feishu-device-code-auth`。
- 后端任务必须先于前端任务；两个仓库分别提交，禁止跨仓库混合 commit。
- 不新增路由，不修改 `config_prod.yaml`，不创建第二套飞书连接服务，不引入独立飞书 Agent。
- 继续使用现有 `feishu.cipher_keyring` 和 `feishu.key_version`；device code 通过独立 AAD purpose 隔离，不要求每个用户或每个环境新建一把密钥。
- `create_app` 继续使用现有受控 blocking worker；只有 `user_auth` 进入 `DeviceAuthFlow`。
- `app_scope` 继续由用户确认后重放原 operation 验证；不把 requested scopes 当作权限已生效的证据。
- URL 只允许出现在 connect/refresh/resume 的实时响应和前端内存；device code、完整 argv、HOME、token、App Secret、文档/Base/Wiki 内容不得进入响应、日志、指标、错误文本或 Agent snapshot。
- 每个实现任务都按红测 → 最小实现 → 绿测 → commit。客户 bug 的两个仓库首个代码 commit 必须是失败复现。
- Task 1 的 commit 有意开启经批准的后端 RED 窗口：其中 start 断言在 Task 6 变绿，restart/dispatch 断言到 Task 9 才变绿；Task 2–8 的验收只跑各自 focused tests，不声称全量 suite 已绿。Task 14 同理，Task 15 将前端 store 回归变绿。

## 1. 最终文件映射

### 1.1 numind-server 新建

| 路径 | 单一职责 |
|---|---|
| `migrations/20260716_230000_feishu_device_code_auth.sql` | 增加 protocol-v2 凭据字段并终止不可恢复的 legacy pending user-auth |
| `migrations/20260716_230000_feishu_device_code_auth_rollback.sql` | 删除新增列；明确不逆转已经 supersede 的旧授权会话 |
| `internal/numind/biz/feishu/device_auth_cli.go` | lark-cli 1.0.68 两段式命令、严格 JSON parser、归一化 outcome |
| `internal/numind/biz/feishu/device_auth_cli_test.go` | 固定版本 start/complete fixture、边界和脱敏测试 |
| `internal/numind/biz/feishu/device_auth_cipher.go` | device code 的 purpose-separated AAD 加解密 |
| `internal/numind/biz/feishu/device_auth_cipher_test.go` | 每个 owner 字段篡改、轮换、密文边界测试 |
| `internal/numind/biz/feishu/device_auth_flow.go` | start/complete/reconcile/replacement 的聚焦状态机 |
| `internal/numind/biz/feishu/device_auth_flow_test.go` | lease、重复点击、过期、拒绝、崩溃边界和 at-most-once 测试 |
| `internal/pkg/errno/feishu.go` | 409 conflict 和 503 dependency 的稳定 HTTP errno |

### 1.2 numind-server 修改

| 路径 | 改动 |
|---|---|
| `internal/pkg/model/feishu_workspace.go` | 给 `FeishuAuthSession` 增加五个 protocol-v2 字段 |
| `internal/numind/store/store.go` | 注册 device-auth store 原语 |
| `internal/numind/store/feishu_workspace.go` | credential attach/release/clear、replacement、fenced candidate publish |
| `internal/numind/store/feishu_workspace_test.go` | SQLite 原子性、代际、lease、secret clearing 回归 |
| `internal/numind/store/feishu_workspace_mysql_integration_test.go` | MySQL 精确列类型、事务锁序和 rollback 证明 |
| `internal/numind/biz/feishu/vault.go` | 增加只在内存返回密文 candidate 的 HOME API |
| `internal/numind/biz/feishu/vault_test.go` | candidate 不提前发布、CAS 冲突和临时 HOME 清理 |
| `internal/numind/biz/feishu/auth_session_service.go` | create-app 保留；user-auth 委托 `DeviceAuthFlow`；旧 refresh 支持 v2 |
| `internal/numind/biz/feishu/auth_session_service_test.go` | 客户回归、create-app 非回归、legacy replacement |
| `internal/numind/biz/feishu/service.go` | `Resume(user_completed)` 调用 `CompleteUserAuthorization` 并返回 notice |
| `internal/numind/biz/feishu/service_test.go` | pending/processing/rejected/expired/completed/terminal 矩阵 |
| `internal/numind/biz/feishu/personal_workspace_integration_test.go` | 重启、重复 resume、Docs/Base/Wiki 独立能力闭环 |
| `internal/numind/biz/feishu_adapter.go` | 用现有 keyring 组合 cipher、flow、auth service，保持单 composition root |
| `internal/numind/biz/feishu_adapter_test.go` | 完整依赖图、key rotation、启动失败 fail-closed |
| `internal/numind/controller/v1/feishu/feishu.go` | resume allowlist 增加 live URL 和 `notice_code`；映射 409/503 |
| `internal/numind/controller/v1/feishu/feishu_test.go` | HTTP body/status/secret omission 契约 |
| `internal/numind/biz/agent/bashvalidator/validator.go` | 注册 `LarkCLIRoute` 语义规则 |
| `internal/numind/biz/agent/bashvalidator/semantic_validators.go` | 拒绝 direct/absolute/wrapper `lark-cli` |
| `internal/numind/biz/agent/bashvalidator/semantic_validators_test.go` | shell routing 回归与 benign non-regression |
| `internal/numind/biz/agent/tool_bash_exec.go` | 工具说明明确飞书业务必须走 `lark_execute` |
| `internal/numind/biz/agent/tool_lark_personal_workspace_test.go` | execute-first guidance 与无 raw CLI 回归 |

### 1.3 numind-web-v3 修改

| 路径 | 改动 |
|---|---|
| `src/api/feishu.ts` | resume action 允许 live URL；增加严格 `notice_code` union/validator |
| `src/api/feishu.test.ts` | 正常、replacement、非法 notice/action 响应契约 |
| `src/types/agent.ts` | `ExternalActionMessage` 增加安全 `notice_code` |
| `src/stores/agentChat.ts` | 原位替换 session/URL/expiry，保留 pending 链接并守住 epoch |
| `src/stores/__tests__/agentChat-resume.spec.ts` | 客户回归、重复点击、stale response、无刷新 continuation |
| `src/components/agent/FeishuActionCard.vue` | 固定 notice 文案；沿用现有 busy/QR/ARIA/token |
| `src/components/agent/__tests__/FeishuActionCard.spec.ts` | notice、QR 替换、error clearing、mobile/keyboard |
| `src/components/agent/AgentMessageItem.vue` | session 变化时清理旧 transport error |
| `src/components/agent/__tests__/AgentMessageItem.spec.ts` | busy/error/replacement 组件联动 |
| `e2e/feishu-personal-workspace.spec.ts` | resume replacement 和 Agent 后续流无需刷新 |

## 2. 数据与 API 固定契约

### 2.1 protocol-v2 session shape

```go
type FeishuAuthSession struct {
	ID                         string
	UserID                     uint
	Generation                 uint64
	OperationID                *string
	Phase                      string
	RequestedScopesJSON        datatypes.JSON
	State                      string
	LeaseOwner                 string
	LeaseUntil                 *time.Time
	ExpiresAt                  time.Time
	ProtocolVersion            uint8  `gorm:"type:tinyint unsigned;not null;default:1" json:"-"`
	ResumeCredentialCiphertext []byte `gorm:"type:longblob" json:"-"`
	ResumeKeyVersion           string `gorm:"size:32" json:"-"`
	ResumeExpiresAt            *time.Time `json:"-"`
	ScopeHash                  string `gorm:"size:64" json:"-"`
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	CompletedAt                *time.Time
}
```

有效 shape 只有以下四种：

1. v1 create-app/app-scope：四个 resume 字段为空；
2. v2 user-auth 尚未 start：ciphertext/key/expiry 为空，scope hash 非空；
3. v2 user-auth 等待用户：ciphertext/key/expiry/scope hash 全部有效；
4. 任意 terminal：ciphertext/key/expiry 必须为空，scope hash 可保留。

### 2.2 CLI fixed protocol

```text
lark-cli auth login --scope offline_access --no-wait --json
lark-cli auth login --device-code secret-device-code --json
```

测试中的固定 start data 为：

```json
{"device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","expires_in":600}
```

complete 的 `authorization_pending`、`access_denied`、`expired_token` 只从结构化 `CLIEnvelope.Error.Subtype` 分类；未知 subtype、重复 JSON、未知字段、无效 URL/expiry 和超限输出一律 fail closed。

### 2.3 Resume response

```go
type AuthorizationNoticeCode string

const (
	AuthorizationPending    AuthorizationNoticeCode = "authorization_pending"
	AuthorizationProcessing AuthorizationNoticeCode = "authorization_processing"
	AuthorizationRejected   AuthorizationNoticeCode = "authorization_rejected"
	AuthorizationExpired    AuthorizationNoticeCode = "authorization_expired"
	AuthorizationUpdated    AuthorizationNoticeCode = "authorization_updated"
)

type OperationResult struct {
	OperationID string                  `json:"operation_id"`
	State       string                  `json:"state"`
	Data        json.RawMessage         `json:"data,omitempty"`
	Action      *OperationAction        `json:"action,omitempty"`
	NoticeCode  AuthorizationNoticeCode `json:"notice_code,omitempty"`
	AgentRunID  uint64                  `json:"-"`
	ToolCallID  string                  `json:"-"`
}
```

只有 resume 本次确实生成新 live action 时才返回 URL。pending/processing 省略 action，前端保留旧卡；success/terminal 不返回 notice。

## 3. 依赖顺序

```text
Task 1 RED
  → Task 2 schema/store primitives
  → Task 3 credential cipher
  → Task 4 strict CLI adapter
  → Task 5 Vault candidate + success transaction
  → Task 6 start + minimal composition
  → Task 7 complete + heartbeat + reconciliation
  → Task 8 rejection/expiry/replacement
  → Task 9 durable dispatch + restart + observability
  → Task 10 lifecycle + HTTP contract
  → Task 11 startup cleanup composition
  → Task 12 shell routing boundary
  → Task 13 backend hardening
  → Task 14 frontend Playwright diagnosis + RED
  → Task 15 frontend API/types/store
  → Task 16 frontend card/message components
  → Task 17 frontend no-refresh E2E
  → Task 18 frontend hardening
  → Task 19 final read-only quality gates
```

---

## Task 1: 提交后端客户失败复现（RED）

**Files:**
- Modify: `internal/numind/biz/feishu/auth_session_service_test.go`
- Modify: `internal/numind/biz/feishu/personal_workspace_integration_test.go`

**Depends on:** none

- [ ] **Step 1: 在现有 harness 中暴露测试专用的 active blocking run 计数**

只修改 fake/harness，不改 production。`blockingAuthCLI` 在 `RunBlocking` 进入/退出时原子增减 active count，并允许同一 datastore 被第二个 service owner 重新打开。

- [ ] **Step 2: 增加可编译但失败的“无遗留 worker + 持久化 exact operation”回归**

```go
func TestAuthSessionService_UserAuthStartLeavesNoWorkerAndPersistsExactOperationCredential(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	service := h.newService("split-protocol-regression")
	operationID := "op-restart-safe"

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, Kind: RecoveryUserScope,
		OperationID: operationID, Scopes: []string{"offline_access"},
	})
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseUserAuth, action.Phase)
	require.Eventually(t, func() bool { return h.cli.ActiveRuns() == 0 }, time.Second, 10*time.Millisecond)
	argv, _ := h.cli.snapshot()
	require.Equal(t, [][]string{{
		"auth", "login", "--scope", "offline_access", "--no-wait", "--json",
	}}, argv)

	var persisted struct {
		OperationID string
		Ciphertext  []byte
	}
	err = h.db.Raw(`SELECT operation_id, resume_credential_ciphertext AS ciphertext
		FROM feishu_auth_session WHERE id = ?`, action.SessionID).Scan(&persisted).Error
	require.NoError(t, err)
	require.Equal(t, operationID, persisted.OperationID)
	require.NotEmpty(t, persisted.Ciphertext)
}
```

- [ ] **Step 3: 增加第二实例从 durable state 继续同一 operation 的回归**

```go
func TestPersonalWorkspaceIntegration_UserAuthResumeSurvivesServiceRestart(t *testing.T) {
	h := newPersonalWorkspaceHarness(t)
	first := h.newService("instance-a")
	action := h.startDocsCreate(first, "restart-safe-document")
	require.Zero(t, h.cli.ActiveRuns())

	second := h.newService("instance-b")
	result, err := second.Resume(h.ctx, h.userID, action.OperationID, ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.Equal(t, 1, h.dispatcher.Count(action.OperationID))
	require.Equal(t, action.OperationID, h.dispatcher.LastOperationID())
}
```

第二实例必须使用同一个数据库/vault，不能复用第一实例内存中的 flow、worker、URL registry 或 credential。当前实现应在 blocking worker 和缺少 durable credential/dispatcher continuation 上失败。

- [ ] **Step 4: 运行两条单测并保存失败证据**

Run: `go test ./internal/numind/biz/feishu -run 'Test(AuthSessionService_UserAuthStartLeavesNoWorkerAndPersistsExactOperationCredential|PersonalWorkspaceIntegration_UserAuthResumeSurvivesServiceRestart)' -count=1`

Expected: FAIL；现有 user-auth 留下 blocking worker，表中没有可恢复密文，第二实例无法完成并 dispatch 原 operation。

- [ ] **Step 5: 只提交失败测试**

```bash
git add internal/numind/biz/feishu/auth_session_service_test.go internal/numind/biz/feishu/personal_workspace_integration_test.go
git commit -m "test(qa): reproduce split Feishu device authorization failure"
```

**Acceptance:** 这是 numind-server feature 分支第一个代码 commit；production 代码不变；测试同时证明当前进程遗留 worker、durable credential 缺失，以及新实例不能恢复 exact operation。

---

## Task 2: 增加 protocol-v2 schema 与 session 持久化原语

**Files:**
- Create: `migrations/20260716_230000_feishu_device_code_auth.sql`
- Create: `migrations/20260716_230000_feishu_device_code_auth_rollback.sql`
- Modify: `internal/pkg/model/feishu_workspace.go`
- Modify: `internal/numind/store/store.go`
- Modify: `internal/numind/store/feishu_workspace.go`
- Modify: `internal/numind/store/feishu_workspace_test.go`
- Modify: `internal/numind/store/feishu_workspace_mysql_integration_test.go`

**Depends on:** Task 1

- [ ] **Step 1: 写 store 红测覆盖完整/部分 credential shape、lease fence 和 terminal clearing**

测试名固定为：

```go
func TestFeishuWorkspaceStore_AttachDeviceAuthCredentialRequiresOwnedLeaseAndReleasesIt(t *testing.T)
func TestFeishuWorkspaceStore_ReleaseDeviceAuthLeaseRetainsCredential(t *testing.T)
func TestFeishuWorkspaceStore_TerminalDeviceAuthClearsCredential(t *testing.T)
func TestFeishuWorkspaceStore_ReplaceDeviceAuthSessionRebindsExactOperation(t *testing.T)
func TestFeishuWorkspaceStore_SweepDeviceAuthCredentialsUsesBoundedKeysetPage(t *testing.T)
```

`AttachDeviceAuthCredential` 的输入必须是一个完整 DTO，禁止散落的十几个参数：

```go
type FeishuDeviceAuthCredentialAttach struct {
	UserID       uint
	Generation   uint64
	SessionID    string
	LeaseOwner   string
	AppID        string
	Ciphertext   []byte
	KeyVersion   string
	ResumeExpiry time.Time
	ScopeHash    string
	Now          time.Time
}
```

该 DTO 不接受任意 account state。store 在同一 account → session 事务内固定写入 `connection_state=waiting_user_auth, connected=false`，并在 credential 成功 attach 的同一事务释放 start lease；credential attach、lease release 或 account update 任一失败必须整体 rollback。对应测试要在注入 account update failure 后断言 credential 四字段仍全空且原 lease 未被局部清除。

- [ ] **Step 2: 运行 store 红测**

Run: `go test ./internal/numind/store -run 'TestFeishuWorkspaceStore_(AttachDeviceAuth|ReleaseDeviceAuth|TerminalDeviceAuth|ReplaceDeviceAuth|SweepDeviceAuth)' -count=1`

Expected: FAIL，缺少 model 字段和 store 方法。

- [ ] **Step 3: 写 forward migration 和显式 rollback**

```sql
ALTER TABLE feishu_auth_session
  ADD COLUMN protocol_version TINYINT UNSIGNED NOT NULL DEFAULT 1 AFTER expires_at,
  ADD COLUMN resume_credential_ciphertext LONGBLOB NULL AFTER protocol_version,
  ADD COLUMN resume_key_version VARCHAR(32) NULL AFTER resume_credential_ciphertext,
  ADD COLUMN resume_expires_at DATETIME(3) NULL AFTER resume_key_version,
  ADD COLUMN scope_hash CHAR(64) NULL AFTER resume_expires_at;

UPDATE feishu_auth_session
SET state = 'superseded',
    completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP(3)),
    lease_owner = '',
    lease_until = NULL
WHERE phase = 'user_auth'
  AND state = 'pending'
  AND protocol_version = 1;
```

Rollback 仅 `DROP COLUMN IF EXISTS` 五列，并在文件头注明旧 pending 会话已经不可逆地 supersede，不能伪造恢复。

- [ ] **Step 4: 实现 model 和 store 方法表面**

只给 concrete `IFeishuWorkspaceStore` 增加以下方法；现有 `AuthSessionStore` 不扩张，避免在 flow 尚未接线前打破 create-app 的测试 fake。Task 6 的新 `DeviceAuthStore` 再从 concrete store 取精确子集：

```go
AttachDeviceAuthCredential(context.Context, FeishuDeviceAuthCredentialAttach) error
ReleaseDeviceAuthLease(context.Context, uint, uint64, string, string, time.Time) (bool, error)
TerminalizeDeviceAuthSession(context.Context, uint, uint64, string, string, string, time.Time) error
ReplaceDeviceAuthSession(context.Context, FeishuDeviceAuthReplacement) (*model.FeishuAuthSession, error)
SweepDeviceAuthCredentials(context.Context, time.Time, string, int) (FeishuDeviceAuthCleanupPage, error)
```

`FeishuDeviceAuthReplacement` 必须携带 old session、lease owner、terminal state、新 session、operation waiting state、旧/新 summary 和 `Now`。store 内统一使用 account → old session → operation 的锁序；terminal/replace/supersede 都同时清空 ciphertext/key/expiry/lease。`AttachDeviceAuthCredential` 使用 account → session 锁序并原子写 account waiting state、credential 和空 lease，使用户完成官方授权后可以立即 claim completion。

`FeishuDeviceAuthCleanupPage` 返回 `NextSessionID`、`Scanned`、`Cleared`、`Done`。每次先沿主键 `id > afterSessionID ORDER BY id LIMIT scanLimit` 读取最多 100 个 session ID，再只对这批记录中的 expired/terminal credential 做条件更新；不以未索引的 `resume_expires_at` 做无界全表候选扫描。flow 在内存保存 cursor，扫到末尾后归零开始下一轮；MySQL 测试用 `EXPLAIN` 和 scanned count 证明单次最多读取 scanLimit 行，因此不新增 S2 已明确不需要的索引。

- [ ] **Step 5: 扩展 RefreshOperationSession 的允许状态和 secret clearing**

允许 exactly-bound `rejected`、`expired`、legacy `superseded` 进入替换，但不能替换有 live lease 的新 session。所有 orphan retirement updates 加入：

```go
"resume_credential_ciphertext": nil,
"resume_key_version":           "",
"resume_expires_at":            nil,
```

- [ ] **Step 6: 扩展 MySQL schema/transaction 集成测试**

精确断言五列类型、nullable/default，并用两个数据库连接证明 live lease owner 之外的 attach/terminal/replacement 均为零行写入。

- [ ] **Step 7: 运行绿测并提交**

Run: `go test ./internal/numind/store ./migrations -count=1`

Expected: PASS。

```bash
git add migrations/20260716_230000_feishu_device_code_auth.sql migrations/20260716_230000_feishu_device_code_auth_rollback.sql internal/pkg/model/feishu_workspace.go internal/numind/store
git commit -m "feat(feishu): persist split authorization credentials"
```

**Acceptance:** protocol-v2 shape 可由 DB 强围栏写入；terminal secret 同事务清除；legacy user-auth 不会再进入不可完成的 blocking 恢复。

---

## Task 3: 实现 purpose-separated credential cipher

**Files:**
- Create: `internal/numind/biz/feishu/device_auth_cipher.go`
- Create: `internal/numind/biz/feishu/device_auth_cipher_test.go`

**Depends on:** Task 2

- [ ] **Step 1: 写逐字段篡改 AAD 的 cipher 红测**

```go
type DeviceAuthCredentialBinding struct {
	UserID          uint
	Generation      uint64
	AppID           string
	OperationID     string
	SessionID       string
	ScopeHash       string
	ResumeExpiresAt time.Time
}
```

表驱动修改 user、generation、app、operation/manual、session、scope hash、expiry、key version；每个变化都必须 `Open` 失败。另测 active writer + previous reader key rotation、空 keyring、未知 key version、超限明文和畸形 envelope。

- [ ] **Step 2: 实现固定 schema envelope 和 purpose-separated AAD**

```go
type deviceAuthCredentialEnvelope struct {
	Version    uint8  `json:"version"`
	DeviceCode string `json:"device_code"`
}

type DeviceAuthCredentialCipher struct {
	ciphers    map[string]*pkgcrypto.Cipher
	keyVersion string
}

func NewDeviceAuthCredentialCipher(
	ciphers map[string]*pkgcrypto.Cipher,
	keyVersion string,
) (*DeviceAuthCredentialCipher, error)

func deviceAuthCredentialAAD(binding DeviceAuthCredentialBinding, keyVersion string) []byte {
	payload := struct {
		Purpose     string `json:"purpose"`
		UserID      uint   `json:"user_id"`
		Generation  uint64 `json:"generation"`
		AppID       string `json:"app_id"`
		OperationID string `json:"operation_id"`
		SessionID   string `json:"session_id"`
		ScopeHash   string `json:"scope_hash"`
		ExpiresAt   string `json:"resume_expires_at"`
		KeyVersion  string `json:"key_version"`
	}{
		Purpose: "feishu-auth-resume/v1", UserID: binding.UserID,
		Generation: binding.Generation, AppID: binding.AppID,
		OperationID: binding.OperationID, SessionID: binding.SessionID,
		ScopeHash: binding.ScopeHash,
		ExpiresAt: binding.ResumeExpiresAt.UTC().Format(time.RFC3339Nano),
		KeyVersion: keyVersion,
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}
```

manual flow 的 `OperationID` 固定为字符串 `manual`，不是空字符串；返回 error 只含固定分类，不格式化 device code 或解密后的 envelope。

- [ ] **Step 3: 运行绿测并单独提交 cipher**

Run: `go test ./internal/numind/biz/feishu -run 'TestDeviceAuthCredential' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/device_auth_cipher.go internal/numind/biz/feishu/device_auth_cipher_test.go
git commit -m "feat(feishu): encrypt device authorization credentials"
```

**Acceptance:** credential 只能在 exact user/generation/app/operation/session/scope/expiry/key-version AAD 下解密；轮换只允许配置中的 active/previous key。

---

## Task 4: 实现 lark-cli 1.0.68 严格两段式 adapter

**Files:**
- Create: `internal/numind/biz/feishu/device_auth_cli.go`
- Create: `internal/numind/biz/feishu/device_auth_cli_test.go`
- Modify: `internal/numind/biz/feishu/controlled_runner_test.go`

**Depends on:** Task 2

- [ ] **Step 1: 写 start/complete 固定 fixture 红测**

```go
func TestControlledLarkCLIRunner_StartUserAuthStrictFixture(t *testing.T)
func TestControlledLarkCLIRunner_CompleteUserAuthOutcomeMatrix(t *testing.T)
func TestControlledLarkCLIRunner_DeviceCodeNeverAppearsInError(t *testing.T)
func TestControlledLarkCLIRunner_RejectsMalformedDeviceAuthOutput(t *testing.T)
```

矩阵含 success、pending、access_denied、expired_token、unknown subtype、duplicate JSON、unknown data field、bad URL、zero/oversized expiry、stdout/stderr oversize 和 timeout。

- [ ] **Step 2: 实现 typed adapter 和 fail-closed parser**

```go
type DeviceAuthCLI interface {
	StartUserAuth(context.Context, string, []string) (DeviceAuthStart, error)
	CompleteUserAuth(context.Context, string, string) (DeviceAuthOutcome, error)
	AuthStatus(context.Context, string) (bool, error)
	AppIDFromHome(context.Context, string) (string, error)
}

type DeviceAuthStart struct {
	VerificationURL string
	DeviceCode      string
	ExpiresIn       time.Duration
}

type DeviceAuthOutcome string

const (
	DeviceAuthCompleted           DeviceAuthOutcome = "completed"
	DeviceAuthPending             DeviceAuthOutcome = "pending"
	DeviceAuthRejected            DeviceAuthOutcome = "rejected"
	DeviceAuthExpired             DeviceAuthOutcome = "expired"
	DeviceAuthRetryableDependency DeviceAuthOutcome = "retryable_dependency"
	DeviceAuthProtocolFailure     DeviceAuthOutcome = "protocol_failure"
	DeviceAuthAmbiguous           DeviceAuthOutcome = "ambiguous"
)
```

`StartUserAuth` 只构造 `[]string{"auth", "login", "--scope", strings.Join(scopes, " "), "--no-wait", "--json"}`；`CompleteUserAuth` 只构造 `[]string{"auth", "login", "--device-code", deviceCode, "--json"}`。二者复用固定 absolute binary 的 controlled runner，不经过 shell。parser 使用单个 JSON object、未知字段拒绝、输出大小上限；device code 不进入 error、日志或 captured argv snapshot。

- [ ] **Step 3: 运行绿测并单独提交 adapter**

Run: `go test ./internal/numind/biz/feishu -run 'TestControlledLarkCLIRunner_(StartUserAuth|CompleteUserAuth|DeviceCode|RejectsMalformedDeviceAuth)' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/device_auth_cli.go internal/numind/biz/feishu/device_auth_cli_test.go internal/numind/biz/feishu/controlled_runner_test.go
git commit -m "feat(feishu): add split device authorization adapter"
```

**Acceptance:** start 在返回前退出；complete 只按 lark-cli 1.0.68 固定结构分类；任何 raw secret 和 argv 都不会泄漏。

---

## Task 5: 增加 Vault candidate 与原子成功发布事务

**Files:**
- Modify: `internal/numind/biz/feishu/vault.go`
- Modify: `internal/numind/biz/feishu/vault_test.go`
- Modify: `internal/numind/store/feishu_workspace.go`
- Modify: `internal/numind/store/feishu_workspace_test.go`
- Modify: `internal/numind/store/feishu_workspace_mysql_integration_test.go`

**Depends on:** Tasks 2–4

- [ ] **Step 1: 写 candidate 不提前发布红测**

```go
func TestEncryptedCLIHomeVault_WithHomeCandidateDoesNotPublish(t *testing.T)
func TestEncryptedCLIHomeVault_WithHomeCandidateCleansRuntimeHome(t *testing.T)
func TestFeishuWorkspaceStore_FinalizeDeviceAuthSuccessPublishesAtomically(t *testing.T)
func TestFeishuWorkspaceStore_FinalizeDeviceAuthSuccessRejectsLateOwner(t *testing.T)
func TestFeishuWorkspaceStore_FinalizeDeviceAuthSuccessRollsBackVaultConflict(t *testing.T)
```

- [ ] **Step 2: 增加 candidate API，不调用 PutVaultCAS**

```go
type CLIHomeCandidate struct {
	Vault            model.FeishuCLIVault
	ExpectedRevision uint64
}

func (v *EncryptedCLIHomeVault) WithHomeCandidate(
	ctx context.Context,
	userID uint,
	generation uint64,
	callback func(string) error,
) (*CLIHomeCandidate, error)
```

实现复用现有 restore/unpack/pack/encrypt/checksum/AAD 代码；callback 成功后返回 candidate；defer 始终删除 0700 临时 HOME。现有 `WithHome` 和普通业务 operation 保持原行为。

- [ ] **Step 3: 定义 success transaction DTO**

```go
type FeishuDeviceAuthSuccess struct {
	UserID              uint
	Generation          uint64
	SessionID           string
	OperationID         string
	LeaseOwner          string
	ExpectedAppID       string
	ExpectedWaitingState string
	Candidate           model.FeishuCLIVault
	ExpectedVaultRevision uint64
	Evidence            model.FeishuConnectionEvidence
	Now                 time.Time
}
```

- [ ] **Step 4: 实现 `FinalizeDeviceAuthSuccess` 的固定锁序**

事务顺序必须是：

1. lock account，校验 user/generation/app/non-disconnecting；
2. lock pending v2 user-auth session，校验 exact unexpired lease 和完整 credential；
3. operation 存在时 lock exact waiting operation，并校验 summary 的 session/phase；
4. lock existing vault 或确认 revision 0；
5. CAS 写 candidate revision；
6. account 置 connected 并写 CLI evidence；
7. session 置 completed，同时清 credential/lease；
8. commit。

任何一步失败，vault revision、account 和 session 都不得局部变化。

- [ ] **Step 5: 运行 SQLite + MySQL 绿测并提交**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/store -run 'Test(EncryptedCLIHomeVault_WithHomeCandidate|FeishuWorkspaceStore_FinalizeDeviceAuthSuccess)' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/vault.go internal/numind/biz/feishu/vault_test.go internal/numind/store/feishu_workspace.go internal/numind/store/feishu_workspace_test.go internal/numind/store/feishu_workspace_mysql_integration_test.go
git commit -m "feat(feishu): fence authorization vault publication"
```

**Acceptance:** CLI 可能写过的 HOME 在事务成功前永远不是 active Vault；lease/generation/app/operation/CAS 任一冲突都回滚。

---

## Task 6: 实现 DeviceAuthFlow start 并接入最小 production composition

**Files:**
- Create: `internal/numind/biz/feishu/device_auth_flow.go`
- Create: `internal/numind/biz/feishu/device_auth_flow_test.go`
- Modify: `internal/numind/biz/feishu/auth_session_service.go`
- Modify: `internal/numind/biz/feishu/auth_session_service_test.go`
- Modify: `internal/numind/biz/feishu_adapter.go`
- Modify: `internal/numind/biz/feishu_adapter_test.go`

**Depends on:** Tasks 2–5

- [ ] **Step 1: 写 start 红测**

覆盖：URL 只有 credential attach 成功后才返回；CLI start 成功但 attach 失败时 URL 丢弃；并发 owner 只有一个调用 CLI；credential expiry 取 CLI/server 最早值；IM scope 被拒；manual 和 operation AAD 分离；start 后没有 worker/process。

- [ ] **Step 2: 定义聚焦 flow API**

```go
type DeviceAuthFlowDeps struct {
	Accounts   AuthSessionAccountStore
	Sessions   DeviceAuthStore
	Vault      DeviceAuthHomeVault
	CLI        DeviceAuthCLI
	Cipher     *DeviceAuthCredentialCipher
	Dispatcher OperationResumeDispatcher
	Owner      string
	Now        func() time.Time
	NewID      func() string
	NewLeaseToken func() string
	LeaseDuration time.Duration
	SessionDuration time.Duration
	HeartbeatInterval time.Duration
	StartTimeout time.Duration
	CompletionTimeout time.Duration
}

type DeviceAuthFlow struct {
	accounts AuthSessionAccountStore
	sessions DeviceAuthStore
	vault DeviceAuthHomeVault
	cli DeviceAuthCLI
	cipher *DeviceAuthCredentialCipher
	dispatcher OperationResumeDispatcher
	now func() time.Time
	newID func() string
	newLeaseToken func() string
	leaseDuration time.Duration
	sessionDuration time.Duration
	heartbeatInterval time.Duration
	startTimeout time.Duration
	completionTimeout time.Duration
	liveURLs *authSessionURLRegistry
}
```

内部窄接口必须写在同一文件，便于 fake 隔离 store/Vault：

```go
type DeviceAuthStore interface {
	GetSessionForUser(context.Context, uint, uint64, string) (*model.FeishuAuthSession, error)
	ClaimSession(context.Context, uint, uint64, string, string, time.Time, time.Time) (bool, error)
	RenewSession(context.Context, uint, uint64, string, string, time.Time, time.Time) (bool, error)
	AttachDeviceAuthCredential(context.Context, store.FeishuDeviceAuthCredentialAttach) error
	ReleaseDeviceAuthLease(context.Context, uint, uint64, string, string, time.Time) (bool, error)
	TerminalizeDeviceAuthSession(context.Context, uint, uint64, string, string, string, time.Time) error
	ReplaceDeviceAuthSession(context.Context, store.FeishuDeviceAuthReplacement) (*model.FeishuAuthSession, error)
	FinalizeDeviceAuthSuccess(context.Context, store.FeishuDeviceAuthSuccess) error
}

type DeviceAuthHomeVault interface {
	WithHome(context.Context, uint, uint64, func(string) (bool, error)) error
	WithHomeCandidate(context.Context, uint, uint64, func(string) error) (*CLIHomeCandidate, error)
}

type DeviceAuthRefreshRequest struct {
	UserID           uint
	Generation       uint64
	OldSessionID     string
	OperationID      string
	WaitingState     string
	OperationSummary []byte
}
```

公开方法仅为：

```go
StartUserAuthorization(context.Context, *model.UserThirdPartyAccount, *model.FeishuAuthSession, []string) (*OperationAction, error)
CompleteUserAuthorization(context.Context, uint, uint64, string) (*DeviceAuthCompletion, error)
RefreshUserAuthorization(context.Context, DeviceAuthRefreshRequest) (*DeviceAuthCompletion, error)
	CleanupExpiredCredentials(context.Context, int) (int64, error)
```

- [ ] **Step 3: 实现 canonical scope hash 和 v2 session validation**

scope hash 为 canonical sorted/deduplicated space-joined scopes 的 SHA-256 lowercase hex。允许 docs/base/wiki catalog scopes 和 `offline_access`，明确拒绝任何 `im:` scope。

- [ ] **Step 4: 实现 start 固定顺序**

```go
func (f *DeviceAuthFlow) StartUserAuthorization(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
	scopes []string,
) (*OperationAction, error) {
	if err := validateDeviceAuthStart(account, session, scopes, f.now()); err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	lease, claimed, err := f.claimStart(ctx, session)
	if err != nil || !claimed {
		return nil, ErrDeviceAuthProcessing
	}
	start, err := f.startCLIInReadOnlyHome(ctx, session, scopes)
	if err != nil {
		f.releaseOrFailOwnedStart(ctx, session, lease, err)
		return nil, classifyDeviceAuthStartError(err)
	}
	expiresAt := earliestDeviceAuthExpiry(f.now(), session.ExpiresAt, start.ExpiresIn)
	binding := f.binding(account, session, deviceAuthScopeHash(scopes), expiresAt)
	ciphertext, keyVersion, err := f.cipher.Seal(binding, start.DeviceCode)
	if err != nil {
		f.releaseOrFailOwnedStart(ctx, session, lease, err)
		return nil, ErrAuthSessionUnavailable
	}
	err = f.sessions.AttachDeviceAuthCredential(ctx, store.FeishuDeviceAuthCredentialAttach{
		UserID: session.UserID, Generation: session.Generation, SessionID: session.ID,
		LeaseOwner: lease, AppID: account.AppID, Ciphertext: ciphertext,
		KeyVersion: keyVersion, ResumeExpiry: expiresAt,
		ScopeHash: binding.ScopeHash, Now: f.now(),
	})
	if err != nil {
		f.releaseOwnedStartLease(ctx, session, lease)
		return nil, ErrAuthSessionUnavailable
	}
	// AttachDeviceAuthCredential 已在同一事务释放 start lease。
	f.liveURLs.put(authSessionRegistryKey(session), start.VerificationURL, expiresAt)
	return f.actionFor(session, start.VerificationURL, expiresAt), nil
}
```

helper 中必须保证 start HOME callback 返回 unchanged，不发布 start 过程对 HOME 的任何副作用。
`releaseOrFailOwnedStart` 对 transient dependency 只释放 lease，使无 credential session 可 reclaim；确定的 parser/crypto invariant 才把 session 置 failed。attach 事务失败时 device flow 被丢弃，URL 不写 registry，best-effort 释放 lease，释放失败也只能等待短 lease 到期后 reclaim。

- [ ] **Step 5: 让 AuthSessionService 只在 user-auth 委托 flow**

`authSessionPlan` 对 user-auth 仍只负责 phase/scopes，不再产生 blocking argv。`start` 创建 v2 session 后调用 flow；create-app/app-scope 继续走原路径。`AuthSessionServiceDeps` 增加非空 `DeviceAuth *DeviceAuthFlow`，但 blocking `CLI` 仍用于 create-app。

- [ ] **Step 6: 同一 commit 完成最小 production composition**

`feishu_adapter.go` 用现有 ciphers/keyVersion 构造 credential cipher 和 `DeviceAuthFlow`，再把同一 flow 注入 `AuthSessionService`。此时不改 HTTP/lifecycle 行为，但整个 `internal/numind/biz` 必须可编译；`feishu_adapter_test.go` 断言缺少 flow/cipher 时启动 fail closed，create-app、operation、auth 仍共享同一 vault/runner/dispatcher。

- [ ] **Step 7: 运行 start 绿测并提交**

Run: `go test ./internal/numind/biz/feishu -run 'Test(DeviceAuthFlow_Start|AuthSessionService_UserAuthUsesRestartSafeSplitProtocol|AuthSessionService_ManualConnect)' -count=1`

Expected: start 单测 PASS；Task 1 中“无遗留 worker/密文存在”的断言变绿，但第二实例 dispatch 断言仍保持 RED，直到 Task 9。

```bash
git add internal/numind/biz/feishu/device_auth_flow.go internal/numind/biz/feishu/device_auth_flow_test.go internal/numind/biz/feishu/auth_session_service.go internal/numind/biz/feishu/auth_session_service_test.go internal/numind/biz/feishu_adapter.go internal/numind/biz/feishu_adapter_test.go
git commit -m "feat(feishu): start restart-safe user authorization"
```

**Acceptance:** user-auth start 是短进程；返回 URL 前密文已持久化；create-app 行为未变。

---

## Task 7: 实现 complete、heartbeat 与独立 reconciliation context

**Files:**
- Modify: `internal/numind/biz/feishu/device_auth_flow.go`
- Modify: `internal/numind/biz/feishu/device_auth_flow_test.go`

**Depends on:** Task 6

- [ ] **Step 1: 写 completion 状态矩阵红测**

固定测试：

```go
func TestDeviceAuthFlow_CompletePendingRetainsCredential(t *testing.T)
func TestDeviceAuthFlow_CompleteConcurrentOwnerReturnsProcessing(t *testing.T)
func TestDeviceAuthFlow_CompleteSuccessPublishesCandidateAtomically(t *testing.T)
func TestDeviceAuthFlow_CompleteAmbiguousReconcilesAuthStatus(t *testing.T)
func TestDeviceAuthFlow_CompleteLateOwnerCannotPublish(t *testing.T)
func TestDeviceAuthFlow_CompleteRenewsLeaseUntilFinalize(t *testing.T)
func TestDeviceAuthFlow_ReconcileUsesFreshContextAfterCLITimeout(t *testing.T)
```

- [ ] **Step 2: 定义 safe completion result/error vocabulary**

```go
type DeviceAuthCompletion struct {
	Completed bool
	NoticeCode AuthorizationNoticeCode
	Action *OperationAction
}

var (
	ErrDeviceAuthProcessing = errors.New("feishu authorization is processing")
	ErrDeviceAuthConflict = errors.New("feishu authorization state conflict")
	ErrDeviceAuthDependency = errors.New("feishu authorization dependency unavailable")
)
```

这些 error 不包装 CLI raw message。

- [ ] **Step 3: 实现 30 秒 bounded complete 与 lease heartbeat**

流程：load exact session/account → 校验 v2 完整 shape/expiry → claim → decrypt → `WithHomeCandidate` callback 内执行 `CompleteUserAuth` → outcome switch。CLI 使用独立 context：

```go
cliBudget := minDuration(30*time.Second, session.ResumeExpiresAt.Sub(f.now()))
cliCtx, cancelCLI := context.WithTimeout(context.WithoutCancel(ctx), cliBudget)
defer cancelCLI()
```

浏览器断开不能取消已经 claim 的服务端完成，但 heartbeat 每 `leaseDuration/3` 续租；credential 过期、generation 改变或续租失败会取消 CLI callback，且 late owner 无法 publish。

- [ ] **Step 4: 给 ambiguous/retryable 创建全新的短 reconciliation context**

30 秒 CLI context 可能已经 deadline exceeded，禁止复用。仍在同一个尚未清理的 candidate HOME callback 中创建：

```go
reconcileBudget := minDuration(
	5*time.Second,
	session.ResumeExpiresAt.Sub(f.now()),
	currentLeaseUntil.Sub(f.now()),
)
reconcileCtx, cancelReconcile := context.WithTimeout(context.WithoutCancel(ctx), reconcileBudget)
defer cancelReconcile()
```

只在 `AuthStatus(reconcileCtx, home)` 为 user available 且 `AppIDFromHome(reconcileCtx, home)` 等于 account AppID 时把 ambiguous 归为 success；否则 credential 未过期则归为 pending，已过期则返回 typed expired outcome 给 Task 8，不能在 callback 外重新打开临时 HOME。

- [ ] **Step 5: 创建独立 mutation context 完成 DB finalize/release/fail**

CLI/reconciliation context 都不能复用到数据库写。heartbeat 持续到 mutation 返回；在 outcome 已确定后创建：

```go
mutationBudget := minDuration(
	5*time.Second,
	session.ResumeExpiresAt.Sub(f.now()),
	currentLeaseUntil.Sub(f.now()),
)
mutationCtx, cancelMutation := context.WithTimeout(context.WithoutCancel(ctx), mutationBudget)
defer cancelMutation()
```

`commitCandidate`、`releaseOwnedLease` 和 `failOwnedSession` 只接收 `mutationCtx`；budget 非正或 heartbeat 已失去 fence 时不做 mutation，返回 conflict。固定时钟测试只调用注入的 `f.now()`，不调用 `time.Until`。

- [ ] **Step 6: 实现 pending/success/ambiguous outcome switch**

```go
switch outcome {
case DeviceAuthCompleted:
	return f.commitCandidate(mutationCtx, account, session, lease, candidate)
case DeviceAuthPending:
	f.releaseOwnedLease(mutationCtx, session, lease)
	return &DeviceAuthCompletion{NoticeCode: AuthorizationPending}, nil
case DeviceAuthRejected:
	f.releaseOwnedLease(mutationCtx, session, lease)
	return &DeviceAuthCompletion{NoticeCode: AuthorizationRejected}, nil
case DeviceAuthExpired:
	f.releaseOwnedLease(mutationCtx, session, lease)
	return &DeviceAuthCompletion{NoticeCode: AuthorizationExpired}, nil
case DeviceAuthRetryableDependency, DeviceAuthAmbiguous:
	return f.reconcileOrRelease(mutationCtx, account, session, lease, candidate)
default:
	f.failOwnedSession(mutationCtx, session, lease)
	return nil, ErrAuthSessionUnavailable
}
```

Task 7 只归一化 rejected/expired，不创建 replacement，也不接 durable dispatcher；Task 8 负责 terminalize、重绑和新 action，Task 9 再负责 completed dispatch，避免把三个可独立验证的状态机塞进同一 commit。

- [ ] **Step 7: 运行 completion/reconcile 绿测并提交**

Run: `go test ./internal/numind/biz/feishu -run 'TestDeviceAuthFlow_(Complete|Reconcile)' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/device_auth_flow.go internal/numind/biz/feishu/device_auth_flow_test.go
git commit -m "feat(feishu): complete split authorization safely"
```

**Acceptance:** complete 有 bounded CLI context、独立 reconciliation context 和持续 lease；成功候选可原子发布，pending/processing/rejected/expired 都是无 raw secret 的 typed result。

---

## Task 8: 实现拒绝、过期与 legacy session 的原位 replacement

**Files:**
- Modify: `internal/numind/biz/feishu/device_auth_flow.go`
- Modify: `internal/numind/biz/feishu/device_auth_flow_test.go`
- Modify: `internal/numind/biz/feishu/auth_session_service.go`
- Modify: `internal/numind/biz/feishu/auth_session_service_test.go`

**Depends on:** Task 7

- [ ] **Step 1: 写 replacement 原子性红测**

```go
func TestDeviceAuthFlow_CompleteRejectedTerminalizesBeforeReplacement(t *testing.T)
func TestDeviceAuthFlow_CompleteExpiredReturnsLiveReplacement(t *testing.T)
func TestDeviceAuthFlow_RefreshLegacyPendingSupersedesAndRebinds(t *testing.T)
func TestDeviceAuthFlow_ReplacementStartFailureKeepsReclaimableSession(t *testing.T)
func TestDeviceAuthFlow_ReplacementNeverRevivesOldCredential(t *testing.T)
```

前两条测试额外断言 `ReleaseDeviceAuthLease` 在 replacement 前调用次数为 0；`ReplaceDeviceAuthSession` 收到的 owner 必须仍是本次 complete 持有且未过期的 lease。

- [ ] **Step 2: 替换 Task 7 的 rejected/expired 分支，不先 release lease**

Task 7 的临时分支会 release 并返回 typed notice；本 task 必须把它改为在同一个 `mutationCtx` 和仍有效的 complete lease 下直接调用 `replaceOwnedSession`。禁止 release 后再拿旧 token 进事务，也不允许先暴露 terminal notice 再异步生成链接。

- [ ] **Step 3: 实现 store-first replacement 顺序**

先调用 Task 2 的 `ReplaceDeviceAuthSession`：在同一 account → old session → operation 事务内把 old session 置 rejected/expired/superseded、清 ciphertext/key/expiry/lease，创建 v2 replacement，并把 exact waiting operation summary/session 绑定到 replacement。事务成功后才调用 Task 6 的正常 start。

```go
replacement, err := f.sessions.ReplaceDeviceAuthSession(mutationCtx, store.FeishuDeviceAuthReplacement{
	UserID: userID, Generation: generation, OldSessionID: old.ID,
	LeaseOwner: lease, TerminalState: terminalState,
	NewSession: newSession, OperationID: operationID,
	ExpectedWaitingState: waitingState, Now: f.now(),
})
if err != nil { return nil, ErrDeviceAuthConflict }
startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), f.startTimeout)
defer cancelStart()
return f.StartUserAuthorization(startCtx, account, replacement, scopes)
```

新 start 失败时 old session 保持 terminal 且 secret 已清，新 session 保持无 credential 的 pending/reclaimable 状态；禁止回滚旧 session 或再次返回旧 URL。

- [ ] **Step 4: 把 rejected/expired/legacy refresh 映射为新 live action**

`CompleteUserAuthorization` 的 rejected/expired outcome 调用 replacement helper；`RefreshUserAuthorization` 对 protocol-v1 pending user-auth 先 claim exact old session，再用该 owner 固定 supersede 并创建 v2。response 只含 new session ID、phase、expiry、verification URL 和 operation ID。

- [ ] **Step 5: 运行 replacement 绿测并提交**

Run: `go test ./internal/numind/biz/feishu -run 'TestDeviceAuthFlow_.*(Rejected|Expired|Legacy|Replacement)' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/device_auth_flow.go internal/numind/biz/feishu/device_auth_flow_test.go internal/numind/biz/feishu/auth_session_service.go internal/numind/biz/feishu/auth_session_service_test.go
git commit -m "feat(feishu): replace terminal authorization sessions"
```

**Acceptance:** rejected/expired/legacy 会话在新链接生成前已不可恢复且 secret 清零；operation 仍指向同一业务意图，只更新授权 session。

---

## Task 9: 接入 durable dispatch、重启恢复与脱敏 observability

**Files:**
- Modify: `internal/numind/biz/feishu/device_auth_flow.go`
- Modify: `internal/numind/biz/feishu/device_auth_flow_test.go`
- Modify: `internal/numind/biz/feishu/auth_session_service.go`
- Modify: `internal/numind/biz/feishu/auth_session_service_test.go`
- Modify: `internal/numind/biz/feishu/personal_workspace_integration_test.go`

**Depends on:** Task 8

- [ ] **Step 1: 写 committed-response-loss 和第二实例 dispatch 红测**

```go
func TestDeviceAuthFlow_CompleteCommittedResponseLossIsIdempotent(t *testing.T)
func TestDeviceAuthFlow_CompletedSessionDispatchesStoredOperationOnce(t *testing.T)
func TestPersonalWorkspaceIntegration_UserAuthResumeSurvivesServiceRestart(t *testing.T)
func TestPersonalWorkspaceIntegration_DispatcherRestartReadsStoredResult(t *testing.T)
func TestDeviceAuthFlow_ObservabilityNeverContainsCredentialOrBusinessContent(t *testing.T)
```

测试必须销毁 instance A 的 flow/registry，instance B 只共享数据库和 encrypted vault；不能在测试 fixture 中直接把 device code 或 operation closure 传给 B。

- [ ] **Step 2: completed session 只通过 durable dispatcher 恢复 exact operation**

成功 transaction commit 后调用现有 `OperationResumeDispatcher.Dispatch(operationID)`；若 HTTP response 丢失，下一次 resume 看到 completed session 时不得再调用 auth CLI，而是重新调用 dispatcher。dispatcher 依赖 operation lease/idempotency；succeeded 返回 stored result，processing 返回 processing，failed/unknown/cancelled 走现有 terminal settlement，绝不自动重放未知业务写。

- [ ] **Step 3: 增加 allowlist observability**

记录 start/complete outcome、lease claim/renew loss、candidate conflict、replacement、dispatch retry 和耗时。字段只允许 user ID、generation、operation/session ID、phase、outcome class、CLI version、duration；禁止 scope、URL、device code、HOME、token、App Secret、文档/Base/Wiki 内容作为字段或 metric label。captured logger 和 error flatten 测试扫描固定 secret，必须零命中。

- [ ] **Step 4: 运行重启/dispatch 绿测并提交**

Run: `go test ./internal/numind/biz/feishu -run 'Test(DeviceAuthFlow_.*(Committed|Completed|Observability)|PersonalWorkspaceIntegration_.*(Restart|Dispatch))' -count=1`

Expected: PASS；Task 1 两条客户回归全部变绿。

```bash
git add internal/numind/biz/feishu/device_auth_flow.go internal/numind/biz/feishu/device_auth_flow_test.go internal/numind/biz/feishu/auth_session_service.go internal/numind/biz/feishu/auth_session_service_test.go internal/numind/biz/feishu/personal_workspace_integration_test.go
git commit -m "feat(feishu): resume authorized operations after restart"
```

**Acceptance:** 进程重启、HTTP 丢响应和 dispatcher 中断均不丢 operation；auth CLI 不重复完成，业务 operation 最多执行一次，日志不含 secret 或正文。

---

## Task 10: 接入 lifecycle 与 HTTP contract

**Files:**
- Modify: `internal/numind/biz/feishu/service.go`
- Modify: `internal/numind/biz/feishu/service_test.go`
- Create: `internal/pkg/errno/feishu.go`
- Modify: `internal/numind/controller/v1/feishu/feishu.go`
- Modify: `internal/numind/controller/v1/feishu/feishu_test.go`

**Depends on:** Task 9

- [ ] **Step 1: 写 lifecycle/API 红测**

覆盖 200 pending、200 processing、200 rejected+URL、200 expired+URL、200 updated+URL、200 success、400 invalid、404 cross-user、409 conflict、503 dependency、500 invariant。每个 JSON 断言不含 `device_code|scope|app_id|credential|token|home|argv`。

- [ ] **Step 2: 扩展 `WorkspaceLifecycleAuth` 和 Resume**

接口增加：

```go
CompleteUserAuthorization(context.Context, uint, uint64, string) (*DeviceAuthCompletion, error)
```

`ResumeActionUserCompleted` 的 user-auth 分支必须调用该方法；create-app 仍观察 worker completion，app-scope 仍调用 `CompleteAppApproval`。completion 返回 notice/action 时，以原 operation ID 和 waiting state 构造 `OperationResult`。

- [ ] **Step 3: 扩展 public allowlist DTO**

```go
type operationResponse struct {
	OperationID string                       `json:"operation_id"`
	State       string                       `json:"state"`
	Data        json.RawMessage              `json:"data,omitempty"`
	Action      *liveLifecycleActionResponse `json:"action,omitempty"`
	NoticeCode  string                       `json:"notice_code,omitempty"`
}
```

只通过 `publicLiveLifecycleAction` 投影 URL；internal Provider/Scopes 永不输出。

- [ ] **Step 4: 增加稳定 errno 映射**

```go
var (
	ErrFeishuLifecycleConflict = &Errno{HTTP: 409, Code: "Conflict.FeishuLifecycle", Message: "飞书授权状态已更新，请使用最新步骤"}
	ErrFeishuDependencyUnavailable = &Errno{HTTP: 503, Code: "ServiceUnavailable.Feishu", Message: "飞书授权服务暂时不可用，请稍后重试"}
)
```

`writeLifecycleResponse` 先判 not-found/invalid/conflict/dependency/unexpected；expected notice 不是 error，始终 200。

- [ ] **Step 5: 运行 lifecycle/API 绿测并提交**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/controller/v1/feishu -run 'Test.*(Authorization|Feishu.*Resume|Lifecycle)' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/service.go internal/numind/biz/feishu/service_test.go internal/pkg/errno/feishu.go internal/numind/controller/v1/feishu/feishu.go internal/numind/controller/v1/feishu/feishu_test.go
git commit -m "feat(feishu): expose recoverable authorization outcomes"
```

**Acceptance:** 原有 endpoint 不变；客户端只提交 fixed action；预期授权状态有可区分的 200/409/503，不再统一 Internal server error。

---

## Task 11: 增加 bounded credential cleanup 并完成 startup composition

**Files:**
- Modify: `internal/numind/biz/feishu/device_auth_flow.go`
- Modify: `internal/numind/biz/feishu/device_auth_flow_test.go`
- Modify: `internal/numind/biz/feishu_adapter.go`
- Modify: `internal/numind/biz/feishu_adapter_test.go`

**Depends on:** Task 10

- [ ] **Step 1: 写 startup fail-closed 与 bounded sweep 红测**

```go
func TestDeviceAuthFlow_CleanupExpiredCredentialsAdvancesBoundedCursor(t *testing.T)
func TestFeishuAdapter_DeviceAuthStartupCleanupFailsClosed(t *testing.T)
func TestFeishuAdapter_DeviceAuthStartupCleanupUsesFiveSecondBudget(t *testing.T)
func TestFeishuAdapter_DeviceAuthCompositionSharesVaultAndDispatcher(t *testing.T)
```

- [ ] **Step 2: 实现内存 cursor 驱动的 bounded cleanup**

`CleanupExpiredCredentials(ctx, 100)` 循环只调用一次 Task 2 的 `SweepDeviceAuthCredentials(ctx, now, cursor, 100)`，保存 `NextSessionID`；`Done` 时 cursor 归零，等待下一轮，不在一次调用中扫完整表。start/complete 入口可 best-effort 扫最多 20 行；cleanup 只清 expired/terminal credential/lease，不改变 operation waiting state。

- [ ] **Step 3: 在服务暴露前执行一次 startup cleanup**

composition 用独立 5 秒 context 调用 `flow.CleanupExpiredCredentials(ctx, 100)`；失败时 Feishu feature fail closed，不启动半可用服务。`feishu_adapter.go` 保持 Task 6 已创建的同一 cipher/flow 实例，并验证 lark-cli 版本 1.0.68 后再暴露 lifecycle。

- [ ] **Step 4: 运行 composition 绿测并提交**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/biz -run 'Test.*(CleanupExpiredCredentials|DeviceAuthStartup|DeviceAuthComposition)' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/feishu/device_auth_flow.go internal/numind/biz/feishu/device_auth_flow_test.go internal/numind/biz/feishu_adapter.go internal/numind/biz/feishu_adapter_test.go
git commit -m "feat(feishu): clean stale authorization credentials"
```

**Acceptance:** 单次 cleanup 工作量被主键 keyset page 限制；启动异常 fail closed；没有未索引的全表过期扫描。

---

## Task 12: 封住 Agent 通过 bash_exec 直接调用 lark-cli 的旁路

**Files:**
- Modify: `internal/numind/biz/agent/bashvalidator/validator.go`
- Modify: `internal/numind/biz/agent/bashvalidator/semantic_validators.go`
- Modify: `internal/numind/biz/agent/bashvalidator/semantic_validators_test.go`
- Modify: `internal/numind/biz/agent/tool_bash_exec.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`

**Depends on:** Task 11

- [ ] **Step 1: 写 direct/absolute/wrapper 红测**

拒绝：`lark-cli docs +create`、`/usr/local/bin/lark-cli auth status`、`sudo lark-cli`、`command lark-cli`、`exec lark-cli`、`env X=1 lark-cli`、`nohup lark-cli`、`time lark-cli` 以及 `echo ok; lark-cli whoami`。允许 `echo lark-cli`、`rg lark-cli README.md`、`my-lark-cli-helper`。

- [ ] **Step 2: 实现窄语义规则**

```go
type larkCLIRouteValidator struct{}

func NewLarkCLIRouteValidator() Validator { return &larkCLIRouteValidator{} }
func (v *larkCLIRouteValidator) ID() string { return "LarkCLIRoute" }
func (v *larkCLIRouteValidator) Validate(cmd string) Result {
	for _, segment := range splitSegments(cmd) {
		name, _ := firstCommand(segment)
		if name == "lark-cli" {
			return denyResult(v.ID(),
				"飞书操作必须使用 lark_execute，以便绑定当前用户的加密工作空间和授权恢复流程",
				"lark-cli")
		}
	}
	return allowResult()
}
```

在 `AllValidators()` 的语义规则区注册；更新计数注释为 15。

- [ ] **Step 3: 更新 bash tool description 和 Agent 工具回归**

只添加一句固定说明：飞书 Docs/Base/Wiki 必须通过 `lark_skill_read` + `lark_execute`；不改系统 prompt 的整体策略，不注册 IM 工具。

- [ ] **Step 4: 运行绿测并提交**

Run: `go test ./internal/numind/biz/agent/bashvalidator ./internal/numind/biz/agent -run 'Test.*(LarkCLI|LarkExecute|BashExec)' -count=1`

Expected: PASS。

```bash
git add internal/numind/biz/agent/bashvalidator internal/numind/biz/agent/tool_bash_exec.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go
git commit -m "fix(agent): route Feishu commands through lark execute"
```

**Acceptance:** LLM 判断能力仍用于选择何时操作飞书；真正执行必须进入 hosted per-user credential boundary。

---

## Task 13: 补强后端安全、并发与三域集成覆盖

**Files:**
- Modify: `internal/numind/biz/feishu/personal_workspace_integration_test.go`
- Modify: `internal/numind/biz/feishu/device_auth_flow_test.go`
- Modify: `internal/numind/controller/v1/feishu/feishu_test.go`

**Depends on:** Tasks 1–12

- [ ] **Step 1: 增加三域 operation-independent fixtures**

同一授权状态机分别接 Docs create、Base read、Wiki update；断言授权只恢复 exact encrypted operation，domain 不改变 device flow，IM 命令永远在 catalog/validator 前置拒绝。

- [ ] **Step 2: 增加 crash/concurrency/security 矩阵**

覆盖 attach 前崩溃、attach 后重启、CLI success 后 candidate 失败、事务失败、response 丢失、dispatcher 中断、两实例同时 resume、两用户相同 session-like ID、unbind generation bump、late owner、tampered ciphertext。对 API JSON、operation summary、Agent snapshot、errors 和 captured logs 扫描 secret device code 与 URL query，结果必须为零命中。

- [ ] **Step 3: 运行 focused/race/full checks**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/store ./internal/numind/controller/v1/feishu ./internal/numind/biz/agent/bashvalidator -count=1`

Run: `go test -race ./internal/numind/biz/feishu ./internal/numind/store -count=1`

Run: `task lint`

Run: `task test`

Expected: 全部 PASS，无 race、lint 或 secret scan 失败。

- [ ] **Step 4: 提交后端测试补强**

```bash
git add internal/numind/biz/feishu/personal_workspace_integration_test.go internal/numind/biz/feishu/device_auth_flow_test.go internal/numind/controller/v1/feishu/feishu_test.go
git commit -m "test(feishu): cover device authorization recovery boundaries"
```

若测试已由前置 task 完整覆盖且没有 diff，不创建空 commit。

**Acceptance:** 所有后端实现、加固和完整测试先于前端代码；覆盖真实根因、崩溃边界、at-most-once、用户隔离和 Docs/Base/Wiki 三域。

---

## Task 14: 先用 Playwright 诊断，再提交前端客户失败复现（RED）

**Files:**
- Modify: `e2e/feishu-personal-workspace.spec.ts`（诊断代码不提交）
- Modify: `src/stores/__tests__/agentChat-resume.spec.ts`（失败回归提交）

**Depends on:** Task 13

- [ ] **Step 1: 在一次性诊断测试中接入 `createDiagnostics`**

诊断现有 resume 返回 live replacement 的 DOM、`/v1/feishu/operations/*/resume` 响应、Pinia message、console/pageerror 和后续 SSE。保存 `test-results/debug-feishu-device-resume.png` 和 trace；随后删除一次性 `createDiagnostics`/`console.log` 代码，不提交诊断噪音。

- [ ] **Step 2: 运行现状 Playwright 诊断**

Run: `npx playwright test e2e/feishu-personal-workspace.spec.ts --project=chromium --trace=on --workers=1`

Expected evidence: resume 响应即使含新 URL，现有 store 仍删除或忽略 replacement URL，卡片进入 missing-link/refresh 路径。若运行时证据不同，先更新本计划的根因段再写代码。

- [ ] **Step 3: 写可运行但失败的 Pinia 回归**

```ts
it('replaces the same card with a live resume action without a page refresh', async () => {
  const store = useAgentChatStore()
  store.applyStreamEvent(deviceAuthActionEvent('op-old', 'session-old', 'https://open.feishu.cn/old'))
  vi.mocked(feishuAPI.resumeFeishuOperation).mockResolvedValueOnce({
    operation_id: 'op-old', state: 'waiting_user_auth', notice_code: 'authorization_expired',
    action: { operation_id: 'op-old', session_id: 'session-new', phase: 'user_auth',
      expires_at: futureExpiry(), url: 'https://open.feishu.cn/new' }
  } as never)

  await store.resumeFeishuOperation('op-old')

  expect(store.messages.filter((message) => message.type === 'external_action')).toHaveLength(1)
  expect(store.messages[0]).toMatchObject({
    operation_id: 'op-old', session_id: 'session-new', url: 'https://open.feishu.cn/new',
    notice_code: 'authorization_expired', action_status: 'pending'
  })
})
```

- [ ] **Step 4: 运行单测确认 RED 并只提交失败测试**

Run: `npm run test:unit -- src/stores/__tests__/agentChat-resume.spec.ts -t 'replaces the same card with a live resume action'`

Expected: FAIL；现有实现删除 URL，且 message 没有 notice code。

```bash
git add src/stores/__tests__/agentChat-resume.spec.ts
git commit -m "test(qa): reproduce Feishu resume card refresh failure"
```

**Acceptance:** 这是 numind-web-v3 feature 分支第一个代码 commit；已有 Playwright 运行时证据；commit 中没有实现或临时诊断日志。

---

## Task 15: 实现前端 API validator、message type 与 Pinia 原位更新

**Files:**
- Modify: `src/api/feishu.ts`
- Modify: `src/api/feishu.test.ts`
- Modify: `src/types/agent.ts`
- Modify: `src/stores/agentChat.ts`
- Modify: `src/stores/__tests__/agentChat-resume.spec.ts`

**Depends on:** Task 14 and backend Task 10 contract

- [ ] **Step 1: 扩展 TS contract 并做 runtime allowlist**

```ts
export type FeishuAuthorizationNoticeCode =
  | 'authorization_pending'
  | 'authorization_processing'
  | 'authorization_rejected'
  | 'authorization_expired'
  | 'authorization_updated'

export interface FeishuOperationResult {
  operation_id: string
  state: FeishuOperationState
  data?: unknown
  action?: FeishuExternalAction
  notice_code?: FeishuAuthorizationNoticeCode
}
```

pending/processing 可无 action；rejected/expired/updated 必须有完整 live action；succeeded/terminal 不得带 notice。非法组合抛固定本地错误，不渲染服务器 raw message。

- [ ] **Step 2: 扩展 message 并实现原位 replacement**

```ts
export interface ExternalActionMessage extends BaseMessage {
  type: 'external_action'
  run_id: number
  operation_id: string
  session_id: string
  phase: FeishuActionPhase
  expires_at: string
  url?: string
  notice_code?: FeishuAuthorizationNoticeCode
  action_status: ExternalActionStatus
  terminal_state?: FeishuRefreshTerminal['state']
}
```

`updatePendingExternalAction` 只更新同 operation 的 pending message，将 session/phase/expiry/URL/notice 全部换成 response action；不能保留 old URL。action 过期则 settle；navigation epoch 不匹配则丢弃。

- [ ] **Step 3: action 省略时也写 notice，保留旧 URL**

```ts
if (result.action) {
  updatePendingExternalAction(operationID, result.action, result.notice_code)
} else if (result.notice_code) {
  updateExternalActionNotice(operationID, result.notice_code)
}
```

`updateExternalActionNotice` 保留原 session/URL/expiry；新的 live action、下一次成功 stream 或 terminal settlement 清掉旧 notice。

- [ ] **Step 4: 增加 API/store 矩阵并提交**

覆盖 pending/processing 无 action、rejected/expired/updated replacement、非法组合、duplicate click、stale epoch、503 保留链接和 success 清 notice。

Run: `npm run test:unit -- src/api/feishu.test.ts src/stores/__tests__/agentChat-resume.spec.ts`

Expected: PASS；Task 14 客户回归变绿。

```bash
git add src/api/feishu.ts src/api/feishu.test.ts src/types/agent.ts src/stores/agentChat.ts src/stores/__tests__/agentChat-resume.spec.ts
git commit -m "fix(feishu): retain live authorization state"
```

**Acceptance:** expected notice 无论是否带 action 都原位反映；同一 operation 只有一条 external-action message，新 action URL 不会被 store 删除。

---

## Task 16: 更新授权卡片与消息容器的可感知状态

**Files:**
- Modify: `src/components/agent/FeishuActionCard.vue`
- Modify: `src/components/agent/__tests__/FeishuActionCard.spec.ts`
- Modify: `src/components/agent/AgentMessageItem.vue`
- Modify: `src/components/agent/__tests__/AgentMessageItem.spec.ts`

**Depends on:** Task 15

- [ ] **Step 1: 写 notice、busy、QR 与 error-clearing 组件红测**

覆盖 pending/processing busy、rejected/expired/updated 新 QR、session 变化清旧 transport error、keyboard、mobile wrap 和 `aria-live`。

- [ ] **Step 2: 实现固定 notice 文案**

```ts
const noticeText = {
  authorization_pending: '尚未检测到授权完成，请完成后再继续。',
  authorization_processing: '正在确认授权状态，请稍候。',
  authorization_rejected: '本次授权未通过，已生成新的授权链接。',
  authorization_expired: '原链接已过期，已生成新的授权链接。',
  authorization_updated: '授权步骤已更新，请使用新的授权链接。'
} as const
```

`FeishuActionCard` 只渲染 allowlist 文案，未知 notice 不显示服务器文本；保留 `aria-live="polite"`、QR watch、keyboard 和 mobile wrap。

- [ ] **Step 3: exact session 变化时清 transport error/busy**

`AgentMessageItem` watch `[operation_id, session_id]`；同 operation 的 new session 才清旧 error/busy。notice-only update 不误清请求；unmount 后异步返回不写状态。

- [ ] **Step 4: 运行组件绿测并提交**

Run: `npm run test:unit -- src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts`

Expected: PASS。

```bash
git add src/components/agent/FeishuActionCard.vue src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/AgentMessageItem.vue src/components/agent/__tests__/AgentMessageItem.spec.ts
git commit -m "fix(feishu): show recoverable authorization status"
```

**Acceptance:** 用户立即看见处理/待授权状态；链接替换和错误清理发生在原卡片，不需要刷新。

---

## Task 17: 完成同卡片 replacement 与 Agent continuation Playwright 验证

**Files:**
- Modify: `e2e/feishu-personal-workspace.spec.ts`

**Depends on:** Task 16

- [ ] **Step 1: 扩展 Playwright mock 为两次 resume + 后续 Agent stream**

第一次 resume 返回 `waiting_user_auth + authorization_expired + live action`，第二次返回 succeeded；随后 stream 发 `tool_call_result`、`reasoning`、`assistant_message`、`final_answer` 和 terminal。断言 card 始终为 1、新 URL/QR 可见、正式回复出现、没有 `page.reload()`、普通 `/answer` 请求、console/pageerror。

- [ ] **Step 2: 增加 missing-link reload 非回归**

从历史 snapshot 恢复且无 URL 时显示“重新生成链接”；refresh 返回 live action 后原位恢复 QR，不插入第二张卡。

- [ ] **Step 3: 运行 Playwright 并提交**

Run: `npx playwright test e2e/feishu-personal-workspace.spec.ts --project=chromium --workers=1`

Expected: PASS；trace 中没有 page reload、console error 或重复卡片。

```bash
git add e2e/feishu-personal-workspace.spec.ts
git commit -m "test(feishu): verify live authorization continuation"
```

**Acceptance:** 新链接、QR、处理中、思考内容与正式回复在同一页面连续出现，不依赖手动刷新。

---

## Task 18: 运行前端全量质量检查并收敛 feature-owned diff

**Files:**
- Potentially modify if formatter/test hardening requires it: `src/api/feishu.ts`
- Potentially modify if formatter/test hardening requires it: `src/api/feishu.test.ts`
- Potentially modify if formatter/test hardening requires it: `src/types/agent.ts`
- Potentially modify if formatter/test hardening requires it: `src/stores/agentChat.ts`
- Potentially modify if formatter/test hardening requires it: `src/stores/__tests__/agentChat-resume.spec.ts`
- Potentially modify if formatter/test hardening requires it: `src/components/agent/FeishuActionCard.vue`
- Potentially modify if formatter/test hardening requires it: `src/components/agent/__tests__/FeishuActionCard.spec.ts`
- Potentially modify if formatter/test hardening requires it: `src/components/agent/AgentMessageItem.vue`
- Potentially modify if formatter/test hardening requires it: `src/components/agent/__tests__/AgentMessageItem.spec.ts`
- Potentially modify if formatter/test hardening requires it: `e2e/feishu-personal-workspace.spec.ts`

**Depends on:** Task 17

- [ ] **Step 1: 运行全量前端检查**

Run: `npm run test:unit`

Run: `npm run test:e2e -- e2e/feishu-personal-workspace.spec.ts --project=chromium --workers=1`

Run: `npm run lint`

Run: `npm run type-check`

Expected: 全部 PASS；lint 如改写文件，只能落在本 feature 文件集合。

- [ ] **Step 2: 检查并提交仅由质量检查产生的差异**

Run: `git status --short`

Run: `git diff --check`

若有格式化或测试补强 diff：

```bash
git add src/api/feishu.ts src/api/feishu.test.ts src/types/agent.ts src/stores/agentChat.ts src/stores/__tests__/agentChat-resume.spec.ts src/components/agent/FeishuActionCard.vue src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/AgentMessageItem.vue src/components/agent/__tests__/AgentMessageItem.spec.ts e2e/feishu-personal-workspace.spec.ts
git commit -m "test(feishu): harden authorization continuation"
```

没有 diff 时不创建空 commit。

**Acceptance:** unit/E2E/lint/type-check 全绿；没有携带前端 feature 以外的格式化或用户改动。

---

## Task 19: 执行最终只读质量门与 commit-chain 审计

**Files:** none

**Depends on:** Task 18

- [ ] **Step 1: 在后端 worktree 重跑只读证据**

Run: `go test ./internal/numind/biz/feishu ./internal/numind/store ./internal/numind/controller/v1/feishu ./internal/numind/biz/agent/bashvalidator -count=1`

Run: `git diff develop...HEAD --check`

Run: `git status --short`

Expected: tests PASS、diff check clean、worktree clean。

- [ ] **Step 2: 在前端 worktree 重跑只读证据**

Run: `npm run type-check`

Run: `npm run test:unit -- src/api/feishu.test.ts src/stores/__tests__/agentChat-resume.spec.ts src/components/agent/__tests__/FeishuActionCard.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts`

Run: `git diff develop...HEAD --check`

Run: `git status --short`

Expected: checks PASS、worktree clean。

- [ ] **Step 3: 审计两个仓库的客户 bug commit 链和边界**

Run in each repo: `git log --oneline develop..HEAD`

后端首个 feature commit 必须是 `test(qa): reproduce split Feishu device authorization failure`；前端首个 feature commit 必须是 `test(qa): reproduce Feishu resume card refresh failure`。确认 diff 无 secret、无 `config_prod.yaml`、无新 route、无 IM/raw API/shell 扩权。

**Acceptance:** S4 实施可以进入独立双 reviewer；两个仓库均有正确 RED-first commit 链、干净 worktree 和可复现的自动化证据。

---

## 4. S4 完成后的真实 dev 验收门槛（S5 执行）

以下步骤不在 S3/S4 实施时提前执行，但会成为 S5 阻塞门槛：

1. 部署 develop 后端和前端到 dev，不部署 production；
2. 用测试账号发出 Docs create，首次只申请 exact user scopes；
3. 用户在飞书官方页完成授权后点击继续；
4. 页面不刷新，原卡片进入处理状态，Agent 自动返回新文档链接；
5. 对同一操作重复点击不会创建第二篇文档；
6. Docs create/read/update、Base create/read/update、Wiki create/read/update 全部真实成功；
7. 触发过期/拒绝路径时同卡片出现新链接，不返回 Internal server error；
8. 服务重启后仍能用已加密凭据继续未完成授权；
9. 同一账号第二次执行同 scope 的飞书命令直接走已连接热路径，不再次显示授权卡片；
10. 同一账号请求新的 Docs/Base/Wiki scope 时只触发 exact incremental scope 授权，旧能力仍可用；
11. 两个有数账号分别连接各自飞书账号，相同 operation/session-like ID 也不能串读 URL、Vault、token 或执行结果；
12. dev 容器确认 lark-cli 版本为 1.0.68，Agent sandbox 与 auth runner 不共享可读 process namespace；
13. 后端日志无 device code、URL query、token、App Secret、HOME 内容和业务正文。

## 5. 回滚策略

- 前端可先回滚：旧前端忽略 `notice_code`，若丢弃 live replacement URL，会退回已有“重新生成链接”路径。
- 后端回滚前必须先由仍运行的新版本执行一次受控 terminalization：把所有 pending/processing protocol-v2 user-auth 置 `superseded`，同事务清空 ciphertext/key/expiry/lease，并保留 waiting operation 供重新发起授权；确认 live lease 数为零后再关闭 Feishu feature flag和回滚应用。禁止让旧 blocking worker接管 v2 session。
- DB 不做破坏性生产 rollback。新增 nullable 列保留；protocol-v2 session 可由修复版本恢复。rollback SQL 只用于未承载真实授权的本地/dev 演练。
- 任何疑似业务写结果不明继续保持 operation `unknown`，禁止自动重放。
