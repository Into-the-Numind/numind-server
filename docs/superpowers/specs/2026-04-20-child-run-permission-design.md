# 子账号运行权限管理（SOP + Chatbot）— 技术 Spec

- **Feature ID**: child-run-permission
- **NDF Stage**: S2 (Technical Design)
- **Created**: 2026-04-20
- **Supersedes PRD**: `numind-server/proposals/child-run-permission-proposal.md`

## §1 目标与边界

### 目标

1. 新增 chatbot 维度的 per-child 白名单权限，结构对称现有 SOP 模板权限
2. 翻转默认语义：`user_template_permission` 0 记录从"默认 allow-all"改为"默认 deny-all"；`user_chatbot_permission`（新表）直接 deny-all 起步
3. Backfill migration 保护存量：为所有 0 记录的现存子账号写入"父账号当前全部已发布 SOP"的权限行，冻结可见范围；chatbot 侧无存量

### 明确边界（不做的事）

- 不支持父账号之间互相借用 chatbot / SOP
- 不做权限审计日志
- 不改管理端 admin_router.go
- 不改父账号层面（父账号永远 bypass 白名单）
- 不新增"角色"维度（不自动按角色批量授权）
- SalesRAG 知识库权限不改（已于 commit 23c3f94 全开）
- 硬编码的"销售智能体"功能权限 checkbox 保留（在权限弹窗内部仍是独立开关）

## §2 架构概览

```
 前端弹窗 CustomersView.vue                后端 router.go
├── 功能权限（既有 sales_agent）            │
├── SOP 模板（既有 + feature A 显示全量）    │
│     fetchAllTemplates / grantTemplates    │
│     fetchUserTemplates / revokeTemplates  │  /v1/customers/sub-users/:id/templates   (既有)
│                                           │  /v1/customers/sub-users/:id/features    (既有)
└── 【新】智能体区块                         │
      fetchAllChatbots / fetchUserChatbots  │  /v1/customers/sub-users/:id/chatbots    (新)
      grantChatbots    / revokeChatbots     │
      batchGrantChatbots / batchRevoke      │  /v1/customers/batch/grant-chatbots     (新)
                                            │  /v1/customers/batch/revoke-chatbots    (新)

 运行时判定                                 │
├── SOP 运行点（biz/sop/sop.go:304,421,1290）│  ds.Customers().HasTemplatePermission  (逻辑翻转)
└── Chatbot 运行点                          │  ds.Customers().HasChatbotPermission   (新，对称语义)
      biz/chatbot/chatbot.go:               │
        ListVisibleChatbots   → 列表过滤    │
        CreateSession         → 守卫        │
        Chat                  → 守卫        │
        ListSessions / Messages → 按 session 归属隐式守卫（session 已绑 user_id + chatbot_id）
```

**总改动面**：
- 后端 ~8 文件（migration ×2 + model ×1 + store ×2 + biz ×2 + controller ×1 + router ×1）
- 前端 ~2 文件（api/customers.ts + views/CustomersView.vue）
- 单测 ~6 文件

## §3 详细设计

### §3.1 新表：`user_chatbot_permission`

```sql
-- migrations/20260420_230000_create_user_chatbot_permission.sql

CREATE TABLE IF NOT EXISTS `user_chatbot_permission` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `sub_user_id` BIGINT UNSIGNED NOT NULL COMMENT '子账号 user.id',
  `chatbot_id` BIGINT UNSIGNED NOT NULL COMMENT 'chatbot_config.id',
  `created_at` DATETIME(3) NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ucp_sub_chatbot` (`sub_user_id`, `chatbot_id`),
  KEY `idx_ucp_chatbot` (`chatbot_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='子账号 chatbot 运行权限白名单';
```

- FK 不硬绑（对齐既有 `user_template_permission` 不带 FK 的风格，减少迁移阻塞）
- `UNIQUE KEY (sub_user_id, chatbot_id)` 保证 `INSERT IGNORE` 幂等
- 不加 soft delete（`deleted_at`），`DELETE FROM` 真删行

### §3.2 Backfill migration

**实际表结构确认**（S3 Gate review 发现 spec 初版错漏）：`user_template_permission` 嵌入 `gorm.Model`（含 `created_at / updated_at / deleted_at`）+ `parent_user_id NOT NULL` + `sub_user_id NOT NULL` + `template_id NOT NULL`。 UNIQUE KEY 在 `(sub_user_id, template_id)`。

```sql
-- migrations/20260420_230001_backfill_default_template_permissions.sql
-- 目标：为所有『活跃权限 = 0』的子账号写入父账号当前已发布 SOP 的授权行
-- 幂等保护：INSERT IGNORE（UNIQUE KEY 冲突跳过）+ NOT EXISTS（排除已有活跃记录的子账号）
-- 软删除关键：NOT EXISTS 子查询必须加 deleted_at IS NULL，否则被撤权的子账号会被误判为"已有记录"从而跳过 backfill

INSERT IGNORE INTO `user_template_permission` (`parent_user_id`, `sub_user_id`, `template_id`, `created_at`, `updated_at`)
SELECT
  u.parent_user_id AS parent_user_id,
  u.id AS sub_user_id,
  t.id AS template_id,
  NOW(3) AS created_at,
  NOW(3) AS updated_at
FROM `user` u
CROSS JOIN `sop_template` t
WHERE
  u.parent_user_id IS NOT NULL
  AND u.deleted_at IS NULL
  AND t.status = 'active'
  AND t.publish_status = 'published'
  AND t.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM `user_template_permission` p
    WHERE p.sub_user_id = u.id
      AND p.deleted_at IS NULL     -- 关键：软删除过滤
    LIMIT 1
  );
```

**关键约束**：
- **P0 修复（review 要求）**：`NOT EXISTS` 子查询加 `AND p.deleted_at IS NULL`。`user_template_permission` 用软删除 —— 曾被父账号撤权的子账号，hard rows 还在但活跃记录为 0，如果不过滤 deleted_at 会被误判为"已有记录"跳过 backfill，翻转后 deny-all。
- **parent_user_id 必填**：初版 spec 漏了这列，实际是 `NOT NULL`，从 `user.parent_user_id` JOIN 写入
- 只处理"活跃记录 = 0"的子账号（软删除后 = 0 同样处理）
- 产出行的 `created_at` 统一是 migration 执行时刻 —— 作为 rollback 的时间窗口标记
- 子账号的权限不限定于自己父账号的模板（复用现有 `HasTemplatePermission` 不做 `creator_user_id` 校验的语义）
- Chatbot 侧**不需要**backfill：上线前 prod 4 个 chatbot 全 draft，不可见；dev 也无非父非子账号对 chatbot 有依赖；新表从空起步，default-deny 即生效

### §3.3 Go Model

**文件**：`numind-server/internal/pkg/model/user_chatbot_permission.go`（新建）

```go
package model

import "time"

type UserChatbotPermission struct {
    ID         uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    SubUserID  uint      `gorm:"not null;uniqueIndex:uk_ucp_sub_chatbot" json:"sub_user_id"`
    ChatbotID  uint      `gorm:"not null;uniqueIndex:uk_ucp_sub_chatbot;index:idx_ucp_chatbot" json:"chatbot_id"`
    CreatedAt  time.Time `json:"created_at"`
}

func (UserChatbotPermission) TableName() string { return "user_chatbot_permission" }
```

### §3.4 Store 层

**文件**：`numind-server/internal/numind/store/customer.go`（既有，扩展）

**新增方法**（加到 `ICustomerStore` 接口 + `customerStore` 实现）：

```go
// 接口
HasChatbotPermission(ctx context.Context, userID, chatbotID uint) (bool, error)
ListSubUserChatbotIDs(ctx context.Context, subUserID uint) ([]uint, error)
GrantChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error
RevokeChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error

// 实现（对齐既有 Template 方法）
func (c *customerStore) HasChatbotPermission(ctx context.Context, userID, chatbotID uint) (bool, error) {
    var user model.User
    if err := c.db.WithContext(ctx).First(&user, userID).Error; err != nil {
        return false, err
    }
    // 父账号 bypass
    if user.ParentUserID == nil {
        return true, nil
    }
    // 子账号：必须有白名单记录（default-deny，不再检查 total count）
    var count int64
    if err := c.db.WithContext(ctx).Model(&model.UserChatbotPermission{}).
        Where("sub_user_id = ? AND chatbot_id = ?", userID, chatbotID).
        Count(&count).Error; err != nil {
        return false, err
    }
    return count > 0, nil
}

func (c *customerStore) ListSubUserChatbotIDs(ctx context.Context, subUserID uint) ([]uint, error) {
    var ids []uint
    err := c.db.WithContext(ctx).Model(&model.UserChatbotPermission{}).
        Where("sub_user_id = ?", subUserID).
        Pluck("chatbot_id", &ids).Error
    return ids, err
}

func (c *customerStore) GrantChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error {
    if len(chatbotIDs) == 0 {
        return nil
    }
    rows := make([]model.UserChatbotPermission, 0, len(chatbotIDs))
    now := time.Now()
    for _, id := range chatbotIDs {
        rows = append(rows, model.UserChatbotPermission{SubUserID: subUserID, ChatbotID: id, CreatedAt: now})
    }
    return c.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func (c *customerStore) RevokeChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error {
    if len(chatbotIDs) == 0 {
        return nil
    }
    return c.db.WithContext(ctx).
        Where("sub_user_id = ? AND chatbot_id IN ?", subUserID, chatbotIDs).
        Delete(&model.UserChatbotPermission{}).Error
}
```

**语义翻转 — `HasTemplatePermission`**：

```go
// 现有（customer.go:179-）
if totalPermissions == 0 {
    return true, nil  // default-allow
}
// 修改为：
if totalPermissions == 0 {
    return false, nil  // default-deny
}
```

**关联性分析**：`totalPermissions` 分支后续还检查 "是否在白名单"。翻转后语义变成：0 记录 → 拒绝；有记录 → 严格白名单；白名单包含 → 放行。

**兼容性**：在 backfill migration 之后，不会有"现存子账号 0 记录"的情况，所以翻转对存量 0 影响。新建子账号初始 0 记录 → 按新语义拒绝，要父账号手动授权。符合 PRD。

### §3.5 Biz 层

**文件**：`numind-server/internal/numind/biz/customer/customer.go`（扩展）

```go
// 接口增加
CheckChatbotPermission(ctx context.Context, userID, chatbotID uint) (bool, error)
ListSubUserChatbots(ctx context.Context, parentUserID, subUserID uint) ([]model.ChatbotConfig, error)  // 只返回已授权的
GrantChatbots(ctx context.Context, parentUserID, subUserID uint, chatbotIDs []uint) error
RevokeChatbots(ctx context.Context, parentUserID, subUserID uint, chatbotIDs []uint) error
BatchGrantChatbots(ctx context.Context, parentUserID uint, subUserIDs []uint, chatbotIDs []uint) error
BatchRevokeChatbots(ctx context.Context, parentUserID uint, subUserIDs []uint, chatbotIDs []uint) error

// 父子关系校验（和 GrantTemplates 对齐）：
//   1. parent 必须是调用者（从 ctx 提取的 userID）
//   2. subUser.ParentUserID == parentUserID
//   3. 所有 chatbotIDs 必须属于 parentUserID（user_id = parentUserID 的 chatbot_config）
```

**Chatbot 运行时守卫** — `biz/chatbot/chatbot.go`：

```go
// ListVisibleChatbots 改造
func (b *chatbotBiz) ListVisibleChatbots(ctx context.Context, user *model.User) ([]model.ChatbotConfig, error) {
    ownerID := user.ID
    if user.ParentUserID != nil {
        ownerID = *user.ParentUserID
    }
    configs, err := b.ds.ChatbotConfig().ListPublishedByOwner(ctx, ownerID)
    if err != nil {
        return nil, fmt.Errorf("ListVisibleChatbots: %w", err)
    }

    // 子账号：过滤白名单
    if user.ParentUserID != nil {
        allowedIDs, err := b.ds.Customers().ListSubUserChatbotIDs(ctx, user.ID)
        if err != nil {
            return nil, fmt.Errorf("ListVisibleChatbots whitelist: %w", err)
        }
        allowed := make(map[uint]bool, len(allowedIDs))
        for _, id := range allowedIDs {
            allowed[id] = true
        }
        filtered := configs[:0]
        for _, c := range configs {
            if allowed[c.ID] {
                filtered = append(filtered, c)
            }
        }
        configs = filtered
    }
    return configs, nil
}

// CreateSession 加入权限守卫
func (b *chatbotBiz) CreateSession(ctx context.Context, userID uint, chatbotID uint) (*model.ChatbotSession, error) {
    ok, err := b.ds.Customers().HasChatbotPermission(ctx, userID, chatbotID)
    if err != nil {
        return nil, fmt.Errorf("CreateSession permission: %w", err)
    }
    if !ok {
        return nil, errno.ErrChatbotRunDenied // 新增 errno
    }
    // ... 既有逻辑
}

// ChatStream（stream.go:31）同样加权限检查
// 【P1-B review 修正】：正确入口是 ChatStream，不是 Chat。接口定义在 chatbot.go:68
// 目的：撤销即时生效（PRD AS-5）—— 父账号撤权后子账号对已有 session 再发消息 → 403
func (b *chatbotBiz) ChatStream(ctx context.Context, userID uint, sessionID uint, message string, modelKey string, thinking bool, handler StreamHandler) error {
    // 既有逻辑：get session + session.UserID != userID 校验（stream.go:40）
    session, err := b.ds.ChatbotSession().Get(ctx, sessionID)
    if err != nil { ... }
    if session.UserID != userID { return ErrForbidden }

    // 【新增】：权限检查 — 即使拥有 session 也要检查 chatbot 当前是否仍被授权
    ok, err := b.ds.Customers().HasChatbotPermission(ctx, userID, session.ChatbotID)
    if err != nil {
        return fmt.Errorf("ChatStream permission: %w", err)
    }
    if !ok {
        return errno.ErrChatbotRunDenied
    }

    // ... 既有的向量检索 / LLM 调用 / 持久化
}
```

**权限错误码**：新增 `errno.ErrChatbotRunDenied`（code: 和 `ErrSopRunDenied` 同段位）。Controller 返回 HTTP 403。

### §3.6 Controller + Router

**文件**：`numind-server/internal/numind/controller/v1/customer/customer.go`（扩展）

新增 5 个 handler（对称 Template 的）：

```go
ListSubUserChatbots(c *gin.Context)   // GET  /v1/customers/sub-users/:user_id/chatbots
GrantChatbots(c *gin.Context)         // POST /v1/customers/sub-users/:user_id/chatbots
RevokeChatbots(c *gin.Context)        // DELETE /v1/customers/sub-users/:user_id/chatbots
BatchGrantChatbots(c *gin.Context)    // POST /v1/customers/batch/grant-chatbots
BatchRevokeChatbots(c *gin.Context)   // POST /v1/customers/batch/revoke-chatbots
```

**Request body schema**（JSON）：

```json
// POST /chatbots
{ "chatbot_ids": [1, 2, 3] }

// DELETE /chatbots
{ "chatbot_ids": [1, 2] }

// POST /batch/grant-chatbots
{ "user_ids": [10, 20], "chatbot_ids": [1, 2] }
```

**响应**：`core.WriteResponse(c, nil, gin.H{"granted": 3})` 或 `{"revoked": N}`

**Router 注册**（`internal/numind/router.go` 约 234 行处，紧邻 template 权限注册）：

```go
authGroup.GET("/customers/sub-users/:user_id/chatbots", customerCtrl.ListSubUserChatbots)
authGroup.POST("/customers/sub-users/:user_id/chatbots", customerCtrl.GrantChatbots)
authGroup.DELETE("/customers/sub-users/:user_id/chatbots", customerCtrl.RevokeChatbots)
authGroup.POST("/customers/batch/grant-chatbots", customerCtrl.BatchGrantChatbots)
authGroup.POST("/customers/batch/revoke-chatbots", customerCtrl.BatchRevokeChatbots)
```

### §3.7 前端

**`numind-web-v3/src/api/customers.ts`**（追加）：

```typescript
// 获取所有已发布 chatbot（供权限弹窗渲染全量列表）
export const fetchAllChatbots = (): Promise<ApiResponse<any[]>> => {
  return request.get('/v1/chatbot/list')  // 既有端点，父账号调用返回自己全部已发布
}

// 获取子用户已授权 chatbot id 列表
export const fetchUserChatbots = (userId: number | string): Promise<ApiResponse<{ chatbot_ids: number[] }>> => {
  return request.get(`/v1/customers/sub-users/${userId}/chatbots`)
}

export const grantChatbots = (userId: number | string, chatbotIds: (number | string)[]): Promise<ApiResponse<any>> => {
  return request.post(`/v1/customers/sub-users/${userId}/chatbots`, { chatbot_ids: chatbotIds })
}

export const revokeChatbots = (userId: number | string, chatbotIds: (number | string)[]): Promise<ApiResponse<any>> => {
  return request.delete(`/v1/customers/sub-users/${userId}/chatbots`, { data: { chatbot_ids: chatbotIds } })
}

export const batchGrantChatbots = (data: { user_ids: (number|string)[], chatbot_ids: (number|string)[] }): Promise<ApiResponse<any>> => {
  return request.post('/v1/customers/batch/grant-chatbots', data)
}

export const batchRevokeChatbots = (data: { user_ids: (number|string)[], chatbot_ids: (number|string)[] }): Promise<ApiResponse<any>> => {
  return request.post('/v1/customers/batch/revoke-chatbots', data)
}
```

**`numind-web-v3/src/views/CustomersView.vue`**：
- 加 state: `allChatbots`, `permChatbotSelectedIds`, `isPermChatbotAllSelected`
- 加方法: `loadAllChatbots`, `togglePermChatbot`, `togglePermChatbotSelectAll`
- 打开弹窗时 parallel 拉 `fetchAllChatbots()` + `fetchUserChatbots(uid)`
- 保存时 diff → `grantChatbots + revokeChatbots`
- 模板区后新增 "可用智能体" 区块（复制模板区结构，替换数据源）

**注意**：`/v1/chatbot/list` 是既有端点（父账号调用返回自己全部已发布 chatbot），不需要后端改动。

### §3.8 Feature A 合入依赖

Feature A (`sop-perm-dialog-show-all`) 先行 merge 到 develop。本 feature 分支从 develop merge A 之后的最新 commit 创建。若 A 修改了 `fetchAllTemplates` 签名（增加 limit 参数），本 feature 的 `loadAllTemplates` 也要保持一致。S3 Plan 显式记录。

## §4 错误码与 HTTP 状态

| 错误码 | HTTP | 触发场景 |
|--------|------|---------|
| `ErrChatbotRunDenied` | 403 | 子账号运行未授权的 chatbot（CreateSession/Chat） |
| `ErrForbidden`（既有） | 403 | 非父账号调用管理 API |
| `ErrNotFound`（既有） | 404 | grantChatbots 传入不存在的 chatbot_id |
| `ErrBind`（既有） | 400 | 请求 body 格式错误 / 缺 required 字段 |

## §5 测试矩阵

### §5.1 Unit test（Go biz/store 层）

| 用例 | 文件 | 覆盖 |
|------|------|------|
| `TestHasTemplatePermission_DefaultDeny` | `store/customer_test.go` | 翻转后 0 记录 → false |
| `TestHasTemplatePermission_WhitelistHit` | 既有扩展 | 1 条记录匹配 → true |
| `TestHasTemplatePermission_WhitelistMiss` | 既有扩展 | 记录但不含目标 → false |
| `TestHasChatbotPermission_ParentBypass` | 新 `store/chatbot_permission_test.go` | 父账号 → true |
| `TestHasChatbotPermission_DefaultDeny` | 同上 | 0 记录 → false |
| `TestHasChatbotPermission_WhitelistHit` | 同上 | 有记录匹配 → true |
| `TestGrantChatbotPermissions_Idempotent` | 同上 | 重复 grant 不报错，表行数不变 |
| `TestRevokeChatbotPermissions_Missing` | 同上 | revoke 不存在 → 无报错 |
| `TestGrantChatbots_ParentChildValidation` | `biz/customer/customer_test.go` | 非父子关系拒绝；非父账号拥有的 chatbot_id 拒绝 |
| `TestListVisibleChatbots_ChildFiltered` | `biz/chatbot/chatbot_test.go` | 子账号仅看到白名单内 |
| `TestListVisibleChatbots_ParentAll` | 同上 | 父账号不受限 |
| `TestCreateSession_ChatbotRunDenied` | 同上 | 未授权 → ErrChatbotRunDenied |

### §5.2 E2E（Playwright or gstack /qa）

**S3 决策**：S5 验证策略二选一。建议：**gstack /qa 浏览器手动验证** + **backfill migration 的 SQL 级验证**（不走 Playwright，因本 feature 主要变化是权限翻转 + 新 UI，UI 层回归面有限；权限判定已由 Go TDD 密集覆盖）。

### §5.3 Migration 幂等性验证

```bash
# Dev 上先空跑
mysql ... < backfill.sql
SELECT COUNT(*) FROM user_template_permission;  # baseline_N
mysql ... < backfill.sql
SELECT COUNT(*) FROM user_template_permission;  # 应等于 baseline_N（幂等）

# 对比 backfill 前后的 before_list 和 after_list
SELECT sub_user_id, template_id FROM user_template_permission ORDER BY sub_user_id, template_id;
```

## §6 部署流程（强约束顺序）

**【P1-A review 修正】**：原步骤 1 和 2 顺序反了。Migration 必须在 CI deploy 之前完成，否则翻转代码会先于存量数据保护上线，所有 0 记录子账号临时被 deny-all。正确顺序：

1. **先**在 dev DB 手动执行 create + backfill migration（SSH + docker exec mysql）
2. **再**合并 feature 到 develop（CI 自动触发 dev 部署）
3. ~~在 dev DB 手动执行 backfill migration：~~（原步骤移到 Step 1 前置）
   ```bash
   sshpass -p "$DEV_SSH_PASS" ssh ... "docker cp migrations/20260420_230001_backfill_default_template_permissions.sql numind-mysql-dev:/tmp/a.sql && docker exec ... mysql < /tmp/a.sql"
   ```
3. Dev E2E 验收（gstack /qa）：
   - 用一个上线前就存在的子账号登录 → 工作区 SOP 列表和之前一致
   - 用一个新建的子账号登录 → 工作区 SOP 和 chatbot 都为空
   - 父账号授权 → 子账号可见
4. 人工签署 dev 验收报告
5. Merge 到 release，打 tag（例如 v2.1.7）
6. QA 环境：先 migration 再 deploy code
7. Prod 环境：同 QA
8. 每次环境切换后运行 prod smoke test（抽样 5 个存量子账号的 `/v1/sop/templates` 结果比 baseline）

## §7 回滚策略

### 代码层

`git revert` 对应两个 feature 分支的 merge commit（注意先后顺序，B 先 revert）。Prod 同步 deploy 旧 binary。

### 数据层

```sql
-- backfill_rollback.sql
-- 【P2-B review 决策】：接受时间窗口过度删除的小风险，不加 source='backfill' 字段
-- 缓解（必须按顺序执行）：
--   1) backfill 执行窗口锁定在维护窗口（<2 分钟）
--   2) 窗口期间不允许父账号做 grant/revoke 操作（运维公告 + UI 只读）
--   3) rollback 前 dry-run：SELECT COUNT(*) FROM user_template_permission WHERE created_at BETWEEN :start AND :end → 人工确认行数合理再 DELETE

-- Step 1: dry-run 打印待删除行数
SELECT COUNT(*) AS will_delete FROM `user_template_permission`
WHERE `created_at` BETWEEN :backfill_start_ts AND :backfill_end_ts;

-- Step 2: 人工确认后真删
DELETE FROM `user_template_permission`
WHERE `created_at` BETWEEN :backfill_start_ts AND :backfill_end_ts
  AND `sub_user_id` IN (SELECT id FROM user WHERE parent_user_id IS NOT NULL AND deleted_at IS NULL);
```

### 回滚后的状态语义

- `HasTemplatePermission` 0 记录 → deny（代码未 revert）| allow（代码已 revert）
- 若数据回滚而代码未回滚 → 存量 0 记录子账号会被误屏蔽，必须同时 revert 代码或重新运行 backfill

## §8 决策

| # | 决策 | 备选 | 选择理由 |
|---|------|------|---------|
| D1 | 默认 deny（语义翻转） | 默认 allow + 父账号手动"锁定"某些资源 | 用户明确要求最小化权限原则；符合 B2B 合规惯例 |
| D2 | 一次性 backfill migration | 加 `permissions_initialized` 标记字段区分老/新账号 | backfill 是一次性迁移，数据模型统一；标记字段会让代码路径分叉，长期维护负担大 |
| D3 | `user_chatbot_permission` 独立表 | 在 `user_template_permission` 加 `resource_type` 列 | SOP 和 chatbot 生命周期独立；独立表结构清晰，不污染现有表 |
| D4 | 不硬绑 FK | 加 FK 到 `chatbot_config.id` | 对齐既有 `user_template_permission` 风格；chatbot 软删不触发级联；业务层保证引用完整性 |
| D5 | ~~面板列出全部 chatbot（含 draft）~~ **改为只列 published** | 含 draft 预授权 | 【P2-A review 修正】权限面板数据源 `/v1/chatbot/list` 既有接口只返 published，与原 D5 矛盾。default-deny 下"发布 + 0 权限 = 零泄露"，发布后设权限没风险，放弃 draft 预授权简化交互 |
| D6 | Chatbot 侧不 backfill | 为 chatbot 也做对等 backfill | 上线前 prod chatbot 全 draft 对子账号不可见；dev 无重要子账号-chatbot 依赖；从空起步干净 |
| D7 | `/v1/chatbot/list` 既有端点复用 | 新建 `/v1/chatbots/all` 专供权限面板 | `/v1/chatbot/list` 对父账号返回全部已发布 chatbot，语义已经满足；不增接口 |
| D8 | 响应 `{chatbot_ids:[...]}` 而非对象数组 | 返回 `[{id, name, ...}]` 对象 | 面板拉两次：`fetchAllChatbots` 拿对象；`fetchUserChatbots` 拿 id 集用于 check 状态；分离职责 |
| D9 | 上线顺序 migration-first | 代码 + migration 原子 deploy | 保证存量一致性 —— 若代码先 deploy 而 migration 未跑，所有 0 记录子账号立刻 deny | 

## §9 不在本 feature 范围的后续改进

- 权限审计日志（`permission_action_log` 表，记 grant/revoke）
- 管理端 admin 视角的跨父账号权限查看
- 按"角色"/"业务线"批量授权
- 父账号之间的 chatbot / SOP 互借
- SalesRAG 知识库权限（已全开，不改）
