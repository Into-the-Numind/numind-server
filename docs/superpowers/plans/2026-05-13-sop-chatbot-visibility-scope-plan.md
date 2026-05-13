# SOP / 智能体可见范围权限 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 SOP 模板和智能体编辑页内联新增「可见范围」权限：父账户可选择仅向部分子用户展示；未在白名单的子用户在工作区列表看不到该实体。与已上线的 child-run-permission 共存（两层 gate 串行：visibility → run-permission）。

**Architecture:** 独立白名单表 `sop_visibility_grant` / `chatbot_visibility_grant` + 在 `sop_template` / `chatbot_config` 加 `visibility_restricted bool` 短路字段。biz 层加 visibility 过滤层置于 run-permission 之前。前端抽出可复用 `SubUserMultiSelectDialog` + `VisibilityScopeCard`。**双路径删除**：UpdateVisibility 用 `Unscoped()` 物理删（避免唯一索引含 NULL 共存问题）；DeleteSubUser/Entity 用软删做审计。

**Tech Stack:** Go 1.24 + Gin + GORM + MySQL 8.0（后端） / Vue 3 + Pinia + axios（前端）

**Spec 引用**: [2026-05-13-sop-chatbot-visibility-scope-design.md](../specs/2026-05-13-sop-chatbot-visibility-scope-design.md)（3 轮 review PASS）

---

## 文件清单

### numind-server

#### 新建
| 路径 | 职责 |
|---|---|
| `migrations/20260513_120000_sop_chatbot_visibility_scope.sql` | Forward migration |
| `migrations/20260513_120000_sop_chatbot_visibility_scope_rollback.sql` | Rollback |
| `internal/pkg/model/sop_visibility_grant.go` | SopVisibilityGrant GORM model |
| `internal/pkg/model/chatbot_visibility_grant.go` | ChatbotVisibilityGrant GORM model |
| `internal/numind/store/sop_visibility_grant.go` | ISopVisibilityGrantStore + 实现 |
| `internal/numind/store/chatbot_visibility_grant.go` | IChatbotVisibilityGrantStore + 实现 |
| `internal/numind/biz/sop/visibility.go` | SOP visibility biz 函数（5 个）|
| `internal/numind/biz/chatbot/visibility.go` | Chatbot visibility biz 函数（对称）|
| `internal/numind/controller/v1/sop/visibility.go` | SOP GET/PUT visibility controller |
| `internal/numind/controller/v1/chatbot/visibility.go` | Chatbot GET/PUT visibility controller |
| `internal/numind/biz/sop/visibility_test.go` | 7 个 SOP 测试 |
| `internal/numind/biz/chatbot/visibility_test.go` | 6 个 chatbot 测试 |

#### 修改
| 路径 | 改动内容 |
|---|---|
| `internal/pkg/model/sop.go` | SopTemplate 加 `VisibilityRestricted bool` 字段 |
| `internal/pkg/model/chatbot.go` | ChatbotConfig 加 `VisibilityRestricted bool` 字段 |
| `internal/pkg/errno/code.go` | 加 4 错误码（VisibilityPermissionDenied, EntityNotOwnedByCaller, CrossParentSubUser, SubUserNotFound）|
| `internal/numind/store/store.go` | IStore interface 加 `SopVisibilityGrant()` + `ChatbotVisibilityGrant()`；datastore 加构造 |
| `internal/numind/biz/sop/sop.go` | ListVisibleTemplatesWithPermission 加 visibility 前置过滤；DeleteSopTemplate 事务加 CleanupByEntity |
| `internal/numind/biz/chatbot/chatbot.go` | ListVisibleChatbotsWithPermission 加 visibility 前置过滤；DeleteChatbot 事务加 CleanupByEntity |
| `internal/numind/biz/customer/customer.go` | DeleteSubUser 事务加 visibility CleanupBySubUser × 2 |
| `internal/numind/router.go` | 注册 4 端点 |

### numind-web-v3

#### 新建
| 路径 | 职责 |
|---|---|
| `src/components/SubUserMultiSelectDialog.vue` | 子用户多选弹窗（可复用）|
| `src/components/VisibilityScopeCard.vue` | 可见范围卡片（接入到 SOP/chatbot 编辑页）|
| `src/api/visibility.ts` | 4 个 API 函数 |
| `e2e/sop-chatbot-visibility-scope.spec.ts` | Playwright E2E（S5 验证）|

#### 修改
| 路径 | 改动内容 |
|---|---|
| `src/views/sop/SopTemplateEdit.vue`（S4 grep 确认路径）| 接入 VisibilityScopeCard + onSave 两阶段保存 |
| `src/views/chatbot/ChatbotEdit.vue`（S4 grep 确认路径）| 同 SOP |
| SOP / chatbot 编辑页 store | 加 visibility 字段 + load/save 函数 |

---

## TOC（23 个原子 task，跨 9 阶段）

### Phase 1：Schema & Foundation（numind-server）
- **Task 1：Migration（forward + rollback）**
- **Task 2：GORM Models（VisibilityRestricted 字段 + 2 grant models）**
- **Task 3：错误码 4 个**
- **Task 4：Store 层 + IStore interface 扩展**

### Phase 2：Biz 层（numind-server）
- **Task 5：validateSubUsersBelongToCaller 工具**
- **Task 6：SOP visibility biz（5 函数）**
- **Task 7：Chatbot visibility biz（5 函数，对称）**

### Phase 3：Controller + Router（numind-server）
- **Task 8：SOP visibility controller（GET + PUT）**
- **Task 9：Chatbot visibility controller（GET + PUT）**
- **Task 10：Router 注册 4 端点**

### Phase 4：列表过滤接入（numind-server）
- **Task 11：ListVisibleTemplatesWithPermission 加 visibility 过滤层**
- **Task 12：ListVisibleChatbotsWithPermission 加 visibility 过滤层**

### Phase 5：级联清理接入（numind-server）
- **Task 13：DeleteSubUser 事务加 CleanupBySubUser × 2**
- **Task 14：DeleteSopTemplate 事务加 CleanupByEntity（EC-6）**
- **Task 15：DeleteChatbot 事务加 CleanupByEntity（EC-6）**

### Phase 6：后端测试（numind-server）
- **Task 16：13 个单元测试用例**

### Phase 7：前端基础组件（numind-web-v3）
- **Task 17：api/visibility.ts**
- **Task 18：SubUserMultiSelectDialog.vue**
- **Task 19：VisibilityScopeCard.vue**

### Phase 8：前端编辑页接入（numind-web-v3）
- **Task 20：SOP 编辑页 store + 接入**
- **Task 21：Chatbot 编辑页 store + 接入**

### Phase 9：S5 验证策略
- **Task 22：Playwright E2E spec**
- **Task 23：S5 验证策略 task（NDF Rule 10 强制）**

---

# Phase 1：Schema & Foundation

## Task 1: Migration（forward + rollback）

**Files:**
- Create: `numind-server/migrations/20260513_120000_sop_chatbot_visibility_scope.sql`
- Create: `numind-server/migrations/20260513_120000_sop_chatbot_visibility_scope_rollback.sql`

**Spec 引用**: §7.1 / §7.2

- [ ] **Step 1: 写 forward migration SQL**

文件内容直接照搬 spec §7.1：
```sql
-- +migrate Up

ALTER TABLE sop_template
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '可见范围限制: 0=全部可见; 1=白名单模式';

ALTER TABLE chatbot_config
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '可见范围限制: 0=全部可见; 1=白名单模式';

-- 注: 唯一索引故意不含 deleted_at, 配合 biz 层 Unscoped().Delete 物理删模式
CREATE TABLE IF NOT EXISTS sop_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL,
  sub_user_id BIGINT UNSIGNED NOT NULL,
  sop_template_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_svg_sub_template_unique (sub_user_id, sop_template_id),
  KEY idx_svg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_svg_template (sop_template_id),
  KEY idx_svg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='SOP 可见范围授权（白名单）';

CREATE TABLE IF NOT EXISTS chatbot_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL,
  sub_user_id BIGINT UNSIGNED NOT NULL,
  chatbot_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_cvg_sub_chatbot_unique (sub_user_id, chatbot_id),
  KEY idx_cvg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_cvg_chatbot (chatbot_id),
  KEY idx_cvg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='Chatbot 可见范围授权（白名单）';
```

- [ ] **Step 2: 写 rollback migration SQL**

```sql
-- +migrate Down
DROP TABLE IF EXISTS chatbot_visibility_grant;
DROP TABLE IF EXISTS sop_visibility_grant;
ALTER TABLE chatbot_config DROP COLUMN visibility_restricted;
ALTER TABLE sop_template DROP COLUMN visibility_restricted;
```

- [ ] **Step 3: 本地试跑 dev 库验证 forward + rollback**

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
  "mysql -u<user> -p<pass> numind_dev < /path/to/migration_forward.sql"
# 验证表创建
sshpass -p "$DEV_SSH_PASS" ssh ... "mysql -e 'DESCRIBE sop_visibility_grant; DESCRIBE chatbot_visibility_grant;' numind_dev"
# 验证 rollback
sshpass -p "$DEV_SSH_PASS" ssh ... "mysql -u<user> -p<pass> numind_dev < /path/to/rollback.sql"
```

> 注：dev 实际运行 migration 由 S6 部署时 CI 处理，此处仅本地验证 SQL 语法。

- [ ] **Step 4: Commit**

```bash
git add migrations/20260513_120000_sop_chatbot_visibility_scope*.sql
git commit -m "feat(visibility-scope): forward+rollback migration for visibility tables"
```

---

## Task 2: GORM Models

**Files:**
- Modify: `numind-server/internal/pkg/model/sop.go`（SopTemplate 加 VisibilityRestricted）
- Modify: `numind-server/internal/pkg/model/chatbot.go`（ChatbotConfig 加 VisibilityRestricted）
- Create: `numind-server/internal/pkg/model/sop_visibility_grant.go`
- Create: `numind-server/internal/pkg/model/chatbot_visibility_grant.go`

**Spec 引用**: §2.1, §2.4

- [ ] **Step 1: SopTemplate 加 VisibilityRestricted 字段**

编辑 `internal/pkg/model/sop.go`，在 `SopTemplate` struct 末尾追加：

```go
// VisibilityRestricted 可见范围限制开关 (false=全部子用户可见; true=仅 sop_visibility_grant 白名单子用户可见)
VisibilityRestricted bool `gorm:"not null;default:0" json:"visibility_restricted"`
```

- [ ] **Step 2: ChatbotConfig 加 VisibilityRestricted 字段**

编辑 `internal/pkg/model/chatbot.go`，在 `ChatbotConfig` struct 末尾追加：

```go
// VisibilityRestricted 可见范围限制开关 (false=全部子用户可见; true=仅 chatbot_visibility_grant 白名单子用户可见)
VisibilityRestricted bool `gorm:"not null;default:0" json:"visibility_restricted"`
```

- [ ] **Step 3: 创建 sop_visibility_grant.go**

```go
package model

import "gorm.io/gorm"

// SopVisibilityGrant SOP 可见范围授权表（白名单）
type SopVisibilityGrant struct {
    gorm.Model
    ParentUserID  uint `gorm:"not null;index:idx_svg_parent_sub" json:"parent_user_id"`
    SubUserID     uint `gorm:"not null;uniqueIndex:idx_svg_sub_template_unique;index:idx_svg_parent_sub" json:"sub_user_id"`
    SopTemplateID uint `gorm:"not null;uniqueIndex:idx_svg_sub_template_unique;index:idx_svg_template" json:"sop_template_id"`

    ParentUser  *User        `gorm:"foreignKey:ParentUserID;references:ID" json:"parent_user,omitempty"`
    SubUser     *User        `gorm:"foreignKey:SubUserID;references:ID" json:"sub_user,omitempty"`
    SopTemplate *SopTemplate `gorm:"foreignKey:SopTemplateID;references:ID" json:"sop_template,omitempty"`
}

func (SopVisibilityGrant) TableName() string { return "sop_visibility_grant" }
```

- [ ] **Step 4: 创建 chatbot_visibility_grant.go**

```go
package model

import "gorm.io/gorm"

// ChatbotVisibilityGrant Chatbot 可见范围授权表（白名单）
type ChatbotVisibilityGrant struct {
    gorm.Model
    ParentUserID uint `gorm:"not null;index:idx_cvg_parent_sub" json:"parent_user_id"`
    SubUserID    uint `gorm:"not null;uniqueIndex:idx_cvg_sub_chatbot_unique;index:idx_cvg_parent_sub" json:"sub_user_id"`
    ChatbotID    uint `gorm:"not null;uniqueIndex:idx_cvg_sub_chatbot_unique;index:idx_cvg_chatbot" json:"chatbot_id"`

    ParentUser *User          `gorm:"foreignKey:ParentUserID;references:ID" json:"parent_user,omitempty"`
    SubUser    *User          `gorm:"foreignKey:SubUserID;references:ID" json:"sub_user,omitempty"`
    Chatbot    *ChatbotConfig `gorm:"foreignKey:ChatbotID;references:ID" json:"chatbot,omitempty"`
}

func (ChatbotVisibilityGrant) TableName() string { return "chatbot_visibility_grant" }
```

- [ ] **Step 5: 验证编译通过**

```bash
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server
go build ./...
```
Expected: 退出码 0，无 compile error。

- [ ] **Step 6: Lint**

```bash
task lint
```
Expected: 退出码 0。

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/model/sop.go internal/pkg/model/chatbot.go \
        internal/pkg/model/sop_visibility_grant.go internal/pkg/model/chatbot_visibility_grant.go
git commit -m "feat(visibility-scope): add VisibilityRestricted field + 2 grant GORM models"
```

---

## Task 3: 错误码 4 个

**Files:**
- Modify: `numind-server/internal/pkg/errno/code.go`

**Spec 引用**: §3.6

- [ ] **Step 1: 先 grep 检查命名冲突**

```bash
cd numind-server
grep -n "ErrEntityNotOwnedByCaller\|ErrVisibilityPermissionDenied\|ErrCrossParentSubUser\|ErrSubUserNotFound\|ErrSopTemplateNotFound\|ErrChatbotNotFound" internal/pkg/errno/*.go
```

Expected: 如果某错误码已存在，本 task 不重复定义；如全部不存在则全部新增。

- [ ] **Step 2: 在 code.go 末尾追加新错误码（仅未存在的）**

```go
// SOP / Chatbot 可见范围权限相关错误码
var (
    ErrEntityNotOwnedByCaller = &Errno{
        HTTP: 403,
        Code: "FailedOperation.EntityNotOwnedByCaller",
        Message: "The entity is not owned by the caller.",
    }

    ErrVisibilityPermissionDenied = &Errno{
        HTTP: 403,
        Code: "FailedOperation.VisibilityPermissionDenied",
        Message: "Only parent accounts can configure visibility scope.",
    }

    ErrCrossParentSubUser = &Errno{
        HTTP: 422,
        Code: "InvalidParameter.CrossParentSubUser",
        Message: "One or more sub_user_ids do not belong to the caller.",
    }

    ErrSubUserNotFound = &Errno{
        HTTP: 422,
        Code: "InvalidParameter.SubUserNotFound",
        Message: "One or more sub_user_ids do not exist.",
    }

    // 如下已有则跳过 (Step 1 已 grep 验证)
    ErrSopTemplateNotFound = &Errno{
        HTTP: 404,
        Code: "ResourceNotFound.SopTemplateNotFound",
        Message: "SOP template was not found.",
    }

    ErrChatbotNotFound = &Errno{
        HTTP: 404,
        Code: "ResourceNotFound.ChatbotNotFound",
        Message: "Chatbot was not found.",
    }
)
```

- [ ] **Step 3: Lint + 编译**

```bash
go build ./... && task lint
```
Expected: 退出码 0。

- [ ] **Step 4: Commit**

```bash
git add internal/pkg/errno/code.go
git commit -m "feat(visibility-scope): add 4 new errno (Entity/Visibility/Cross/SubUserNotFound)"
```

---

## Task 4: Store 层 + IStore interface 扩展

**Files:**
- Create: `numind-server/internal/numind/store/sop_visibility_grant.go`
- Create: `numind-server/internal/numind/store/chatbot_visibility_grant.go`
- Modify: `numind-server/internal/numind/store/store.go`

**Spec 引用**: §4.1.6（UpdateSopVisibility 物理删 Unscoped），§5.3（CleanupBySubUser），§9 EC-6（CleanupByEntity）

- [ ] **Step 1: 写 ISopVisibilityGrantStore interface + impl**

`internal/numind/store/sop_visibility_grant.go`：

```go
package store

import (
    "context"
    "gorm.io/gorm"
    "github.com/numind/internal/pkg/model"  // 实际包路径以项目为准
)

// ISopVisibilityGrantStore SOP 可见范围 grant store.
type ISopVisibilityGrantStore interface {
    // ListSubUserIDsBySopID 返回某 SOP 的白名单子用户 ID (未软删).
    ListSubUserIDsBySopID(ctx context.Context, sopID uint) ([]uint, error)
    // ListVisibleSopIDsBySubUser 返回某子用户能看到的 SOP ID set (未软删).
    ListVisibleSopIDsBySubUser(ctx context.Context, subUserID uint) (map[uint]struct{}, error)
    // CountBySubUserAndSop 用于 IsSopVisibleToUser 判断, 返回 (sub_user_id, sop_template_id) 未软删的记录数 (0 或 1).
    CountBySubUserAndSop(ctx context.Context, subUserID, sopID uint) (int64, error)
    // ReplaceGrantsTx 物理删全部该 SOP 的现有 grant (含软删) 后插入新 grant. 用于 UpdateSopVisibility restricted=true 路径.
    ReplaceGrantsTx(ctx context.Context, tx *gorm.DB, sopID, parentUserID uint, subUserIDs []uint) error
    // CleanupBySubUser 软删某子用户的所有 SOP grant (DeleteSubUser 路径).
    CleanupBySubUser(ctx context.Context, tx *gorm.DB, subUserID uint) error
    // CleanupByEntity 软删某 SOP 的所有 grant (DeleteSopTemplate 路径, EC-6).
    CleanupByEntity(ctx context.Context, tx *gorm.DB, sopID uint) error
}

type sopVisibilityGrantStore struct {
    db *gorm.DB
}

func NewSopVisibilityGrantStore(db *gorm.DB) *sopVisibilityGrantStore {
    return &sopVisibilityGrantStore{db: db}
}

func (s *sopVisibilityGrantStore) ListSubUserIDsBySopID(ctx context.Context, sopID uint) ([]uint, error) {
    var ids []uint
    if err := s.db.WithContext(ctx).
        Model(&model.SopVisibilityGrant{}).
        Where("sop_template_id = ?", sopID).
        Pluck("sub_user_id", &ids).Error; err != nil {
        return nil, err
    }
    return ids, nil
}

func (s *sopVisibilityGrantStore) ListVisibleSopIDsBySubUser(ctx context.Context, subUserID uint) (map[uint]struct{}, error) {
    var ids []uint
    if err := s.db.WithContext(ctx).
        Model(&model.SopVisibilityGrant{}).
        Where("sub_user_id = ?", subUserID).
        Pluck("sop_template_id", &ids).Error; err != nil {
        return nil, err
    }
    set := make(map[uint]struct{}, len(ids))
    for _, id := range ids {
        set[id] = struct{}{}
    }
    return set, nil
}

func (s *sopVisibilityGrantStore) CountBySubUserAndSop(ctx context.Context, subUserID, sopID uint) (int64, error) {
    var count int64
    err := s.db.WithContext(ctx).
        Model(&model.SopVisibilityGrant{}).
        Where("sub_user_id = ? AND sop_template_id = ?", subUserID, sopID).
        Count(&count).Error
    return count, err
}

func (s *sopVisibilityGrantStore) ReplaceGrantsTx(ctx context.Context, tx *gorm.DB, sopID, parentUserID uint, subUserIDs []uint) error {
    // Unscoped() 物理删 (含软删记录), 避免唯一索引冲突
    if err := tx.WithContext(ctx).Unscoped().
        Where("sop_template_id = ?", sopID).
        Delete(&model.SopVisibilityGrant{}).Error; err != nil {
        return err
    }
    if len(subUserIDs) == 0 {
        return nil
    }
    records := make([]model.SopVisibilityGrant, 0, len(subUserIDs))
    for _, uid := range subUserIDs {
        records = append(records, model.SopVisibilityGrant{
            ParentUserID:  parentUserID,
            SubUserID:     uid,
            SopTemplateID: sopID,
        })
    }
    return tx.WithContext(ctx).Create(&records).Error
}

func (s *sopVisibilityGrantStore) CleanupBySubUser(ctx context.Context, tx *gorm.DB, subUserID uint) error {
    return tx.WithContext(ctx).
        Where("sub_user_id = ?", subUserID).
        Delete(&model.SopVisibilityGrant{}).Error
}

func (s *sopVisibilityGrantStore) CleanupByEntity(ctx context.Context, tx *gorm.DB, sopID uint) error {
    return tx.WithContext(ctx).
        Where("sop_template_id = ?", sopID).
        Delete(&model.SopVisibilityGrant{}).Error
}
```

- [ ] **Step 2: 写 IChatbotVisibilityGrantStore interface + impl**

`internal/numind/store/chatbot_visibility_grant.go`：结构完全对称，将 `SopTemplateID` 替换为 `ChatbotID`，`sop_template_id` 替换为 `chatbot_id`，`model.SopVisibilityGrant` 替换为 `model.ChatbotVisibilityGrant`。函数名 `*BySopID` 改为 `*ByChatbotID`，`SopID` 参数改为 `ChatbotID`。

- [ ] **Step 3: 在 store.go 的 IStore interface 加入 2 个新方法**

编辑 `internal/numind/store/store.go`，在 `IStore` interface 中追加：

```go
type IStore interface {
    // ... 既有方法 ...
    SopVisibilityGrant() ISopVisibilityGrantStore
    ChatbotVisibilityGrant() IChatbotVisibilityGrantStore
}
```

在 `datastore` 实现下追加：

```go
func (ds *datastore) SopVisibilityGrant() ISopVisibilityGrantStore {
    return NewSopVisibilityGrantStore(ds.db)
}

func (ds *datastore) ChatbotVisibilityGrant() IChatbotVisibilityGrantStore {
    return NewChatbotVisibilityGrantStore(ds.db)
}
```

- [ ] **Step 4: 写 ReplaceGrantsTx 的单元测试（防回归）**

`internal/numind/store/sop_visibility_grant_test.go`（内联 test 在 store 层，验证 Unscoped 行为）：

```go
func TestReplaceGrantsTx_PhysicalDeleteIncludesSoftDeleted(t *testing.T) {
    db := setupTestDB(t)
    s := NewSopVisibilityGrantStore(db)

    // 1. 插入一条 active grant
    require.NoError(t, db.Create(&model.SopVisibilityGrant{
        ParentUserID: 1, SubUserID: 10, SopTemplateID: 100,
    }).Error)

    // 2. 软删它 (模拟 CleanupBySubUser/Entity 路径)
    require.NoError(t, db.Where("sop_template_id=?", 100).Delete(&model.SopVisibilityGrant{}).Error)

    // 3. 用 ReplaceGrantsTx 重新插入 (sub_user_id=10) — 应该成功而非唯一冲突
    err := db.Transaction(func(tx *gorm.DB) error {
        return s.ReplaceGrantsTx(context.Background(), tx, 100, 1, []uint{10})
    })
    require.NoError(t, err, "ReplaceGrantsTx should physically delete soft-deleted records first")

    // 4. 验证: 表中应只有 1 条 active 记录, 0 条 soft-deleted
    var activeCount, allCount int64
    db.Model(&model.SopVisibilityGrant{}).Where("sop_template_id=?", 100).Count(&activeCount)
    db.Unscoped().Model(&model.SopVisibilityGrant{}).Where("sop_template_id=?", 100).Count(&allCount)
    assert.Equal(t, int64(1), activeCount)
    assert.Equal(t, int64(1), allCount, "physical delete should have purged soft-deleted row")
}
```

- [ ] **Step 5: 运行测试 + lint + build**

```bash
go test ./internal/numind/store/... -run TestReplaceGrants -v
task lint
go build ./...
```
Expected: 测试 PASS，lint + build 退出码 0。

- [ ] **Step 6: Commit**

```bash
git add internal/numind/store/sop_visibility_grant.go \
        internal/numind/store/chatbot_visibility_grant.go \
        internal/numind/store/sop_visibility_grant_test.go \
        internal/numind/store/store.go
git commit -m "feat(visibility-scope): store layer (2 grant stores + IStore extension + Unscoped test)"
```

---

# Phase 2：Biz 层

## Task 5: validateSubUsersBelongToCaller 工具

**Files:**
- Create: `numind-server/internal/numind/biz/customer/validate_sub_users.go`（或合并到既有 customer biz 文件，S4 grep 确认）

**Spec 引用**: §4.1.8

- [ ] **Step 1: 写两步校验函数**

签名接收 `store.IStore` 而非裸 `*gorm.DB`，符合三层架构规则（biz 层通过 store 接口访问数据）。事务/查询通过 `s.DB()` 取出（与项目既有 biz 模式一致，如 `b.ds.DB().WithContext(ctx).Transaction(...)`）。

```go
package customer

import (
    "context"
    "fmt"
    "github.com/numind/internal/pkg/errno"
    "github.com/numind/internal/pkg/model"
    "github.com/numind/internal/numind/store"
)

// ValidateSubUsersBelongToCaller 两步校验:
//   Step 1: 全部 ID 在 user 表中存在 (不含软删) → 否则 ErrSubUserNotFound
//   Step 2: 全部 ID 的 parent_user_id 等于 callerID → 否则 ErrCrossParentSubUser
// 接收 store.IStore (而非裸 *gorm.DB) 以符合三层架构; 单次性 COUNT 查询走 s.DB()
// 与项目既有 biz 事务模式一致, 不污染 UserStore 接口.
func ValidateSubUsersBelongToCaller(ctx context.Context, s store.IStore, callerID uint, subUserIDs []uint) error {
    if len(subUserIDs) == 0 {
        return nil
    }
    db := s.DB().WithContext(ctx)

    // Step 1: 存在性 (GORM 默认 scope 自动过滤 deleted_at IS NULL)
    var existCount int64
    if err := db.Model(&model.User{}).
        Where("id IN ?", subUserIDs).Count(&existCount).Error; err != nil {
        return fmt.Errorf("ValidateSubUsersBelongToCaller: count exist: %w", err)
    }
    if existCount != int64(len(subUserIDs)) {
        return errno.ErrSubUserNotFound
    }

    // Step 2: 归属
    var belongCount int64
    if err := db.Model(&model.User{}).
        Where("id IN ? AND parent_user_id = ?", subUserIDs, callerID).Count(&belongCount).Error; err != nil {
        return fmt.Errorf("ValidateSubUsersBelongToCaller: count belong: %w", err)
    }
    if belongCount != int64(len(subUserIDs)) {
        return errno.ErrCrossParentSubUser
    }
    return nil
}
```

- [ ] **Step 2: 单元测试 4 个 case**

```go
func TestValidateSubUsersBelongToCaller(t *testing.T) {
    s := setupTestStore(t)  // IStore-backed test fixture
    // seed: parent=1, sub=10/11 属于 1; sub=20 属于 2; sub=999 不存在
    seedUsers(t, s.DB(), []userSeed{
        {id: 1, parentUserID: 0},
        {id: 2, parentUserID: 0},
        {id: 10, parentUserID: 1},
        {id: 11, parentUserID: 1},
        {id: 20, parentUserID: 2},
    })

    cases := []struct{
        name string
        callerID uint
        subUserIDs []uint
        wantErr error
    }{
        {"empty", 1, nil, nil},
        {"happy", 1, []uint{10, 11}, nil},
        {"not_exist", 1, []uint{10, 999}, errno.ErrSubUserNotFound},
        {"cross_parent", 1, []uint{10, 20}, errno.ErrCrossParentSubUser},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            err := ValidateSubUsersBelongToCaller(ctx, s, c.callerID, c.subUserIDs)
            assert.ErrorIs(t, err, c.wantErr)
        })
    }
}
```

- [ ] **Step 3: 跑测试 + lint**

```bash
go test ./internal/numind/biz/customer/... -run TestValidateSubUsers -v
task lint
```

- [ ] **Step 4: Commit**

```bash
git add internal/numind/biz/customer/validate_sub_users.go \
        internal/numind/biz/customer/validate_sub_users_test.go
git commit -m "feat(visibility-scope): ValidateSubUsersBelongToCaller two-step validation"
```

---

## Task 6: SOP visibility biz（5 函数）

**Files:**
- Create: `numind-server/internal/numind/biz/sop/visibility.go`

**Spec 引用**: §4.1.1, §4.1.3, §4.1.5, §4.1.6

- [ ] **Step 1: 写 5 个函数（按 spec 伪代码）**

```go
package sop

import (
    "context"
    "fmt"
    "gorm.io/gorm"
    "github.com/numind/internal/pkg/errno"
    "github.com/numind/internal/pkg/model"
    "github.com/numind/internal/numind/biz/customer"
    "github.com/numind/internal/numind/store"
)

// IsSopVisibleToUser 判断 SOP 是否对给定用户可见.
func IsSopVisibleToUser(ctx context.Context, s store.IStore, userID, sopID uint) (bool, error) {
    user, err := s.Users().Get(ctx, userID)
    if err != nil {
        return false, fmt.Errorf("IsSopVisibleToUser: get user: %w", err)
    }
    if user.ParentUserID == nil {
        return true, nil // 父账户总是可见
    }
    sop, err := s.Sop().GetTemplate(ctx, sopID)
    if err != nil {
        return false, errno.ErrSopTemplateNotFound
    }
    if !sop.VisibilityRestricted {
        return true, nil // 短路
    }
    count, err := s.SopVisibilityGrant().CountBySubUserAndSop(ctx, userID, sopID)
    if err != nil {
        return false, fmt.Errorf("IsSopVisibleToUser: count grant: %w", err)
    }
    return count > 0, nil
}

// ListSubUserVisibleSopIDs 返回该子用户在 sop_visibility_grant 表中所有未软删的 sop_template_id 集合.
// 过滤逻辑由调用方结合 sop.visibility_restricted 字段判断. 详见 spec §4.1.3.
func ListSubUserVisibleSopIDs(ctx context.Context, s store.IStore, subUserID uint) (map[uint]struct{}, error) {
    return s.SopVisibilityGrant().ListVisibleSopIDsBySubUser(ctx, subUserID)
}

// GetSopVisibility 返回 SOP 的可见范围配置 (restricted, subUserIDs, error).
// subUserIDs 始终从 grant 表返回 (D3 保留语义: restricted=false 时也返回历史名单).
// 接收 callerID 用于 owner 校验 (业务逻辑统一在 biz 层, controller 层不重复).
func GetSopVisibility(ctx context.Context, s store.IStore, callerID, sopID uint) (bool, []uint, error) {
    sop, err := s.Sop().GetTemplate(ctx, sopID)
    if err != nil {
        return false, nil, errno.ErrSopTemplateNotFound
    }
    caller, err := s.Users().GetByID(ctx, callerID)
    if err != nil {
        return false, nil, fmt.Errorf("GetSopVisibility: get caller: %w", err)
    }
    if caller.ParentUserID != nil {
        return false, nil, errno.ErrVisibilityPermissionDenied
    }
    if sop.CreatorUserID == nil || *sop.CreatorUserID != callerID {
        return false, nil, errno.ErrEntityNotOwnedByCaller
    }
    ids, err := s.SopVisibilityGrant().ListSubUserIDsBySopID(ctx, sopID)
    if err != nil {
        return false, nil, fmt.Errorf("GetSopVisibility: list grants: %w", err)
    }
    return sop.VisibilityRestricted, ids, nil
}

// UpdateSopVisibility 更新 SOP 的可见范围配置 (D3 + 双路径删除模式).
// 当 restricted=true 时, 全删全插 grant; restricted=false 时, 不动 grant 表.
// 见 spec §4.1.6 完整伪代码.
//
// 事务模式: 使用项目既有的 b.ds.DB().WithContext(ctx).Transaction(...)
// (IStore 接口只暴露 DB() *gorm.DB, 不存在 WithTx 包装; 见 credit/payment biz 同款用法)
func UpdateSopVisibility(ctx context.Context, s store.IStore, callerID, sopID uint, restricted bool, subUserIDs []uint) error {
    sop, err := s.Sop().GetTemplate(ctx, sopID)
    if err != nil {
        return errno.ErrSopTemplateNotFound
    }
    caller, err := s.Users().Get(ctx, callerID)
    if err != nil {
        return fmt.Errorf("UpdateSopVisibility: get caller: %w", err)
    }
    if caller.ParentUserID != nil {
        return errno.ErrVisibilityPermissionDenied
    }
    if sop.CreatorUserID == nil || *sop.CreatorUserID != callerID {
        return errno.ErrEntityNotOwnedByCaller
    }
    if restricted {
        if err := customer.ValidateSubUsersBelongToCaller(ctx, s, callerID, subUserIDs); err != nil {
            return err
        }
    }
    return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        if restricted {
            if err := s.SopVisibilityGrant().ReplaceGrantsTx(ctx, tx, sopID, callerID, subUserIDs); err != nil {
                return fmt.Errorf("UpdateSopVisibility: replace grants: %w", err)
            }
        }
        return tx.Model(&model.SopTemplate{}).Where("id=?", sopID).
            Update("visibility_restricted", restricted).Error
    })
}
```

- [ ] **Step 2: 单元测试 7 个 case（覆盖 visibility 关闭 / 开启全选 / 开启零选 / 开启部分 / 权限校验 / 越权配置 / 短路）**

测试代码量大，建议放在 Task 16 集中实现；本 task 先写最小冒烟测试：

```go
func TestUpdateSopVisibility_Smoke(t *testing.T) {
    s := setupTestStore(t)
    seedParentAndSubUsers(t, s, 1, []uint{10, 11})
    seedSopTemplate(t, s, 100, 1) // owner=1

    err := UpdateSopVisibility(ctx, s, 1, 100, true, []uint{10})
    require.NoError(t, err)
    restricted, ids, err := GetSopVisibility(ctx, s, 1, 100)  // callerID=1 (owner)
    require.NoError(t, err)
    assert.True(t, restricted)
    assert.ElementsMatch(t, []uint{10}, ids)
}
```

- [ ] **Step 3: lint + 测试**

```bash
go test ./internal/numind/biz/sop/... -run TestUpdateSopVisibility -v
task lint
```

- [ ] **Step 4: Commit**

```bash
git add internal/numind/biz/sop/visibility.go internal/numind/biz/sop/visibility_test.go
git commit -m "feat(visibility-scope): SOP visibility biz (5 functions + smoke test)"
```

---

## Task 7: Chatbot visibility biz（5 函数，对称）

**Files:**
- Create: `numind-server/internal/numind/biz/chatbot/visibility.go`

**Spec 引用**: §4.1.7（特别注意 chatbot.UserID 非 *uint，与 SOP 的 CreatorUserID 不同）

- [ ] **Step 1: 写对称 5 函数**

完全复制 Task 6 的 5 个函数结构，关键差异：

```go
// Owner 校验差异: chatbot.UserID != callerID (非指针, 直接比较)
if chatbot.UserID != callerID {
    return errno.ErrEntityNotOwnedByCaller
}
```

替换：
- `model.SopTemplate` → `model.ChatbotConfig`
- `*sop.CreatorUserID` → `chatbot.UserID`（非指针）
- `s.Sop().GetTemplate` → `s.ChatbotConfig().Get`
- `s.SopVisibilityGrant()` → `s.ChatbotVisibilityGrant()`
- `errno.ErrSopTemplateNotFound` → `errno.ErrChatbotNotFound`
- 函数名 `*Sop*` → `*Chatbot*`

⚠️ **不要 copy-paste 而不改 owner 检查**——`chatbot` 没有 `CreatorUserID` 字段，直接 copy 会 compile error。

- [ ] **Step 2: 冒烟测试**

```go
func TestUpdateChatbotVisibility_Smoke(t *testing.T) {
    s := setupTestStore(t)
    seedParentAndSubUsers(t, s, 1, []uint{10, 11})
    seedChatbotConfig(t, s, 200, 1) // user_id=1

    err := UpdateChatbotVisibility(ctx, s, 1, 200, true, []uint{10})
    require.NoError(t, err)
    restricted, ids, err := GetChatbotVisibility(ctx, s, 1, 200)  // callerID=1 (owner)
    require.NoError(t, err)
    assert.True(t, restricted)
    assert.ElementsMatch(t, []uint{10}, ids)
}
```

- [ ] **Step 3: lint + 测试**

```bash
go test ./internal/numind/biz/chatbot/... -run TestUpdateChatbotVisibility -v
task lint
```

- [ ] **Step 4: Commit**

```bash
git add internal/numind/biz/chatbot/visibility.go internal/numind/biz/chatbot/visibility_test.go
git commit -m "feat(visibility-scope): chatbot visibility biz (5 functions + smoke test)"
```

---

# Phase 3：Controller + Router

## Task 8: SOP visibility controller（GET + PUT）

**Files:**
- Create: `numind-server/internal/numind/controller/v1/sop/visibility.go`

**Spec 引用**: §3.2, §3.3

- [ ] **Step 1: 写 controller**

```go
package sop

import (
    "github.com/gin-gonic/gin"
    "github.com/numind/internal/pkg/core"
    "github.com/numind/internal/pkg/errno"
    "github.com/numind/internal/numind/biz/sop"
    "github.com/numind/internal/numind/store"
)

type VisibilityController struct {
    store store.IStore
}

func NewVisibilityController(s store.IStore) *VisibilityController {
    return &VisibilityController{store: s}
}

type visibilityResp struct {
    Restricted  bool   `json:"restricted"`
    SubUserIDs  []uint `json:"sub_user_ids"`
}

type updateVisibilityReq struct {
    Restricted  bool   `json:"restricted"`
    SubUserIDs  []uint `json:"sub_user_ids"`
}

// GetVisibility GET /v1/sop/templates/:id/visibility
// Controller 仅做参数绑定 + 调用 biz; owner 校验在 biz 层 (api-design.md §6).
func (c *VisibilityController) GetVisibility(ctx *gin.Context) {
    callerID := ctx.GetUint("userID")
    sopID, err := strconv.ParseUint(ctx.Param("id"), 10, 64)
    if err != nil {
        core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid sop id"), nil)
        return
    }
    restricted, ids, err := sop.GetSopVisibility(ctx, c.store, callerID, uint(sopID))
    if err != nil {
        core.WriteResponse(ctx, err, nil)
        return
    }
    core.WriteResponse(ctx, nil, visibilityResp{Restricted: restricted, SubUserIDs: ids})
}

// UpdateVisibility PUT /v1/sop/templates/:id/visibility
func (c *VisibilityController) UpdateVisibility(ctx *gin.Context) {
    callerID := ctx.GetUint("userID")
    sopID, err := strconv.ParseUint(ctx.Param("id"), 10, 64)
    if err != nil {
        core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid sop id"), nil)
        return
    }
    var req updateVisibilityReq
    if err := ctx.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(ctx, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }
    if err := sop.UpdateSopVisibility(ctx, c.store, callerID, uint(sopID), req.Restricted, req.SubUserIDs); err != nil {
        core.WriteResponse(ctx, err, nil)
        return
    }
    core.WriteResponse(ctx, nil, nil)
}
```

- [ ] **Step 2: Lint + 编译**

```bash
go build ./... && task lint
```

- [ ] **Step 3: Commit**

```bash
git add internal/numind/controller/v1/sop/visibility.go
git commit -m "feat(visibility-scope): SOP visibility controller (GET + PUT)"
```

---

## Task 9: Chatbot visibility controller（GET + PUT）

**Files:**
- Create: `numind-server/internal/numind/controller/v1/chatbot/visibility.go`

**Spec 引用**: §3.4

- [ ] **Step 1: 复制 Task 8 结构（thin controller），替换字段**

Controller 仅做参数绑定 + 调用 biz；owner 校验在 biz 层 `GetChatbotVisibility(callerID, chatbotID)` 内部完成（与 SOP 对称），controller 不重复。

关键差异（参照 Task 7）：
- biz 函数：`chatbot.GetChatbotVisibility(ctx, s, callerID, chatbotID)`, `chatbot.UpdateChatbotVisibility(...)`
- 错误码透传：`errno.ErrChatbotNotFound` / `errno.ErrEntityNotOwnedByCaller` / `errno.ErrVisibilityPermissionDenied` 由 biz 返回，controller `core.WriteResponse(ctx, err, nil)` 直接透传

- [ ] **Step 2: 编译 + lint**

```bash
go build ./... && task lint
```

- [ ] **Step 3: Commit**

```bash
git add internal/numind/controller/v1/chatbot/visibility.go
git commit -m "feat(visibility-scope): chatbot visibility controller (GET + PUT)"
```

---

## Task 10: Router 注册 4 端点

**Files:**
- Modify: `numind-server/internal/numind/router.go`

**Spec 引用**: §3.7

- [ ] **Step 1: 先 grep 当前路由组织方式**

```bash
grep -n "templates\|chatbot" internal/numind/router.go | head -20
```

定位 SOP 模板路由组 + chatbot 路由组的位置。

- [ ] **Step 2: 在 SOP 路由组追加 2 端点**

```go
// 假设 SOP 模板路由组已存在为 templatesGroup
templatesGroup.GET("/:id/visibility", sopVisibilityCtrl.GetVisibility)
templatesGroup.PUT("/:id/visibility", sopVisibilityCtrl.UpdateVisibility)
```

- [ ] **Step 3: 在 chatbot 路由组追加 2 端点**

```go
chatbotGroup.GET("/:id/visibility", chatbotVisibilityCtrl.GetVisibility)
chatbotGroup.PUT("/:id/visibility", chatbotVisibilityCtrl.UpdateVisibility)
```

- [ ] **Step 4: 在 router 初始化处实例化 2 个 controller**

```go
sopVisibilityCtrl := sop.NewVisibilityController(store)
chatbotVisibilityCtrl := chatbot.NewVisibilityController(store)
```

- [ ] **Step 5: 启动本地服务验证 4 端点 404 不再发生**

```bash
task dev &
sleep 5
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:9091/v1/sop/templates/1/visibility -H "Authorization: Bearer fake"
# Expected: 401 (auth) 而不是 404 (route not registered)
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:9091/v1/chatbot/1/visibility -H "Authorization: Bearer fake"
# Expected: 401
pkill -f "task dev"
```

- [ ] **Step 6: Lint + Commit**

```bash
task lint
git add internal/numind/router.go
git commit -m "feat(visibility-scope): register 4 visibility endpoints"
```

---

# Phase 4：列表过滤接入

## Task 11: ListVisibleTemplatesWithPermission 加 visibility 过滤层

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/sop.go`

**Spec 引用**: §4.2.1（gate 顺序：visibility → run-permission）

- [ ] **Step 1: 定位现有函数**

```bash
grep -n "ListVisibleTemplatesWithPermission" internal/numind/biz/sop/sop.go
```

- [ ] **Step 2: 在函数中现有 run-permission 过滤之前插入 visibility 过滤**

伪代码改造（spec §4.2.1）：

```go
func (b *sopBiz) ListVisibleTemplatesWithPermission(ctx context.Context, userID uint) ([]model.SopTemplate, error) {
    templates, err := b.store.Sop().ListVisibleTemplates(ctx)
    if err != nil {
        return nil, err
    }
    user, err := b.store.Users().Get(ctx, userID)
    if err != nil {
        return nil, err
    }
    if user.ParentUserID == nil {
        return templates, nil // 父账户全可见
    }

    // NEW: visibility 过滤层
    visibilitySet, err := b.store.SopVisibilityGrant().ListVisibleSopIDsBySubUser(ctx, userID)
    if err != nil {
        return nil, fmt.Errorf("list visibility grants: %w", err)
    }
    filteredByVisibility := make([]model.SopTemplate, 0, len(templates))
    for _, t := range templates {
        if t.VisibilityRestricted {
            if _, ok := visibilitySet[t.ID]; !ok {
                continue // 跳过受限且不在白名单的
            }
        }
        filteredByVisibility = append(filteredByVisibility, t)
    }

    // 既有: run-permission 过滤
    permissionSet, err := b.store.Customers().ListSubUserTemplateIDs(ctx, userID)
    if err != nil {
        return nil, err
    }
    result := make([]model.SopTemplate, 0, len(filteredByVisibility))
    for _, t := range filteredByVisibility {
        if _, ok := permissionSet[t.ID]; ok {
            result = append(result, t)
        }
    }
    return result, nil
}
```

- [ ] **Step 3: 加测试覆盖 4 象限矩阵**

```go
func TestListVisibleTemplates_FourQuadrants(t *testing.T) {
    s := setupTestStore(t)
    // seed: parent=1, sub=10; SOP 100/101/102/103
    // SOP100: visibility OFF, run-perm grant         → visible
    // SOP101: visibility OFF, run-perm no grant       → not visible (run-perm 拦)
    // SOP102: visibility ON +sub10 grant, run-perm grant → visible
    // SOP103: visibility ON +no grant, run-perm grant   → not visible (visibility 拦)
    // ... seed code ...
    result, err := biz.ListVisibleTemplatesWithPermission(ctx, 10)
    require.NoError(t, err)
    ids := extractIDs(result)
    assert.ElementsMatch(t, []uint{100, 102}, ids)
}
```

- [ ] **Step 4: 跑测试 + lint**

```bash
go test ./internal/numind/biz/sop/... -run TestListVisibleTemplates -v
task lint
```

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/sop/sop.go internal/numind/biz/sop/list_filter_test.go
git commit -m "feat(visibility-scope): inject visibility filter before run-permission in ListVisibleTemplates"
```

---

## Task 12: ListVisibleChatbotsWithPermission 加 visibility 过滤层

**Files:**
- Modify: `numind-server/internal/numind/biz/chatbot/chatbot.go`

**Spec 引用**: §4.2.2

- [ ] **Step 1: 定位函数**

```bash
grep -n "ListVisibleChatbotsWithPermission\|ListSubUserChatbotIDs" internal/numind/biz/chatbot/chatbot.go
```

- [ ] **Step 2: 同 Task 11 模式，加 visibility 过滤层**

替换：
- `SopVisibilityGrant` → `ChatbotVisibilityGrant`
- `ListVisibleSopIDsBySubUser` → `ListVisibleChatbotIDsBySubUser`
- `ListSubUserTemplateIDs` → `ListSubUserChatbotIDs`
- `model.SopTemplate` → `model.ChatbotConfig`
- `t.ID` 不变（GORM 都有 ID 字段）

- [ ] **Step 3: 4 象限测试**

- [ ] **Step 4: 跑测试 + lint + commit**

```bash
go test ./internal/numind/biz/chatbot/... -run TestListVisibleChatbots -v
task lint
git add internal/numind/biz/chatbot/chatbot.go internal/numind/biz/chatbot/list_filter_test.go
git commit -m "feat(visibility-scope): inject visibility filter before run-permission in ListVisibleChatbots"
```

---

# Phase 5：级联清理接入

## Task 13: DeleteSubUser 事务加 CleanupBySubUser × 2

**Files:**
- Modify: `numind-server/internal/numind/biz/customer/customer.go`

**Spec 引用**: §5.2, §5.3

- [ ] **Step 1: 定位 DeleteSubUser 函数**

```bash
grep -n "DeleteSubUser" internal/numind/biz/customer/customer.go
```

- [ ] **Step 2: 在事务闭包内加 2 行清理（使用项目既有事务模式）**

```go
return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
    // NEW: 软删 visibility grants
    if err := b.ds.SopVisibilityGrant().CleanupBySubUser(ctx, tx, subUserID); err != nil {
        return fmt.Errorf("cleanup sop visibility: %w", err)
    }
    if err := b.ds.ChatbotVisibilityGrant().CleanupBySubUser(ctx, tx, subUserID); err != nil {
        return fmt.Errorf("cleanup chatbot visibility: %w", err)
    }
    // ... 既有: 清理 user_template_permission, user_chatbot_permission, user ...
    return nil
})
```

> 注：实际字段名以既有 `customer biz` 为准（如 `b.ds` 或 `b.store`），不假设。S4 grep 现有 DeleteSubUser 函数定位事务和 store 引用变量名。

- [ ] **Step 3: 测试用例**

```go
func TestDeleteSubUser_CleanupVisibilityGrants(t *testing.T) {
    s := setupTestStore(t)
    // seed: parent=1, sub=10; visibility grants for sub=10 in both tables
    seedVisibilityGrants(t, s, 1, 10, []uint{100, 101}, []uint{200})

    err := biz.DeleteSubUser(ctx, s, 1, 10)
    require.NoError(t, err)

    // 验证 grants 已软删 (默认 scope 查不到)
    sopIDs, _ := s.SopVisibilityGrant().ListVisibleSopIDsBySubUser(ctx, 10)
    cbIDs, _ := s.ChatbotVisibilityGrant().ListVisibleChatbotIDsBySubUser(ctx, 10)
    assert.Empty(t, sopIDs)
    assert.Empty(t, cbIDs)
}
```

- [ ] **Step 4: 跑测试 + lint + commit**

```bash
go test ./internal/numind/biz/customer/... -run TestDeleteSubUser -v
task lint
git add internal/numind/biz/customer/customer.go internal/numind/biz/customer/cleanup_test.go
git commit -m "feat(visibility-scope): inject CleanupBySubUser × 2 into DeleteSubUser tx"
```

---

## Task 14: DeleteSopTemplate 事务加 CleanupByEntity（EC-6）

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/sop.go`

**Spec 引用**: §9 EC-6

- [ ] **Step 1: 定位 DeleteSopTemplate 函数**

```bash
grep -n "DeleteSopTemplate\|func.*Delete.*[Tt]emplate" internal/numind/biz/sop/sop.go
```

- [ ] **Step 2: 在事务内加清理（使用项目既有事务模式 b.ds.DB().Transaction）**

```go
return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
    if err := b.ds.SopVisibilityGrant().CleanupByEntity(ctx, tx, sopID); err != nil {
        return fmt.Errorf("cleanup visibility on sop delete: %w", err)
    }
    // ... 既有 SOP 删除逻辑 ...
    return nil
})
```

- [ ] **Step 3: 测试**

```go
func TestDeleteSopTemplate_CleanupVisibility(t *testing.T) {
    s := setupTestStore(t)
    seedSopTemplate(t, s, 100, 1)
    seedVisibilityGrants(t, s, 1, 10, []uint{100}, nil)
    require.NoError(t, biz.DeleteSopTemplate(ctx, s, 1, 100))
    ids, _ := s.SopVisibilityGrant().ListSubUserIDsBySopID(ctx, 100)
    assert.Empty(t, ids)
}
```

- [ ] **Step 4: lint + commit**

```bash
go test ./internal/numind/biz/sop/... -run TestDeleteSopTemplate_CleanupVisibility -v
task lint
git add internal/numind/biz/sop/sop.go internal/numind/biz/sop/ec6_test.go
git commit -m "feat(visibility-scope): EC-6 cleanup visibility grants on sop template delete"
```

---

## Task 15: DeleteChatbot 事务加 CleanupByEntity（EC-6）

**Files:**
- Modify: `numind-server/internal/numind/biz/chatbot/chatbot.go`

**Spec 引用**: §9 EC-6

- [ ] **Step 1-4: 同 Task 14，对称 chatbot 版**

```bash
grep -n "DeleteChatbot\|func.*Delete.*[Cc]hatbot" internal/numind/biz/chatbot/chatbot.go
```

在 chatbot delete biz 函数中使用项目既有事务模式 `b.ds.DB().WithContext(ctx).Transaction(...)`，闭包内加：

```go
if err := b.ds.ChatbotVisibilityGrant().CleanupByEntity(ctx, tx, chatbotID); err != nil {
    return fmt.Errorf("cleanup visibility on chatbot delete: %w", err)
}
```

```bash
go test ./internal/numind/biz/chatbot/... -run TestDeleteChatbot_CleanupVisibility -v
task lint
git add internal/numind/biz/chatbot/chatbot.go internal/numind/biz/chatbot/ec6_test.go
git commit -m "feat(visibility-scope): EC-6 cleanup visibility grants on chatbot delete"
```

---

# Phase 6：后端测试集中补全

## Task 16: 14 个单元测试用例（含并发 PUT）

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/visibility_test.go`
- Modify: `numind-server/internal/numind/biz/chatbot/visibility_test.go`

**Spec 引用**: §10.2

补足 Task 6/7 的冒烟测试，覆盖完整 14 个 case（13 spec 要求 + 1 并发 PUT 补足）：

- [ ] **Step 1: SOP 7 个测试**

1. `TestUpdateSopVisibility_TurnOnFull` — restricted=true 全选
2. `TestUpdateSopVisibility_TurnOnPartial` — restricted=true 部分选
3. `TestUpdateSopVisibility_TurnOnEmpty` — restricted=true sub_user_ids=[] (白名单严格全拒)
4. `TestUpdateSopVisibility_TurnOffPreservesGrants` — D3 保留语义验证
5. `TestUpdateSopVisibility_CrossParent` — 提交他人子用户 → ErrCrossParentSubUser
6. `TestUpdateSopVisibility_SubUserNotExist` — 提交不存在 ID → ErrSubUserNotFound
7. `TestUpdateSopVisibility_NonOwner` — 非 owner 调用 → ErrEntityNotOwnedByCaller

- [ ] **Step 2: Chatbot 5 个对称测试 + 1 个边界**

1-4: 同 SOP 1-4 (TurnOn Full/Partial/Empty + TurnOffPreservesGrants)
5: `TestUpdateChatbotVisibility_NonOwner` — chatbot.UserID 差异校验
6: `TestUpdateChatbotVisibility_IdempotentReplay` — 同一 PUT 连续 2 次, 第二次无副作用 (P0-2 验证)

- [ ] **Step 3: 4 象限矩阵测试** — 若 Task 11/12 测试文件存在则 `go test -run TestListVisible` 验证全通过；若不存在或某象限漏覆盖，则必须在本 task 补足（不可跳过）

- [ ] **Step 3b: 并发 PUT 测试 (spec §10.2 必需)**

```go
func TestUpdateSopVisibility_ConcurrentPUT_LastWriteWins(t *testing.T) {
    s := setupTestStore(t)
    seedParentAndSubUsers(t, s, 1, []uint{10, 11, 12})
    seedSopTemplate(t, s, 100, 1)

    // 两 goroutine 并发 PUT 不同子用户名单, 验证不死锁 + last-write-wins
    var wg sync.WaitGroup
    errA, errB := error(nil), error(nil)
    wg.Add(2)
    go func() {
        defer wg.Done()
        errA = biz.UpdateSopVisibility(ctx, s, 1, 100, true, []uint{10, 11})
    }()
    go func() {
        defer wg.Done()
        errB = biz.UpdateSopVisibility(ctx, s, 1, 100, true, []uint{12})
    }()

    done := make(chan struct{})
    go func() { wg.Wait(); close(done) }()
    select {
    case <-done:
    case <-time.After(5 * time.Second):
        t.Fatal("ConcurrentPUT 超时 — 疑似死锁")
    }
    require.NoError(t, errA)
    require.NoError(t, errB)

    // 最终状态必须等于某一次 PUT 的全量结果 (last-write-wins, 不应混合)
    _, ids, err := biz.GetSopVisibility(ctx, s, 100)
    require.NoError(t, err)
    isA := equalSet(ids, []uint{10, 11})
    isB := equalSet(ids, []uint{12})
    assert.True(t, isA || isB, "final state should match one of the writes, got: %v", ids)
}
```

- [ ] **Step 4: EC-6 后再 grant 测试**

```go
func TestSopVisibility_EC6PhysicalDeleteThenRegrant(t *testing.T) {
    s := setupTestStore(t)
    // seed: parent=1, sub=10, SOP=100
    seedSopTemplate(t, s, 100, 1)
    require.NoError(t, biz.UpdateSopVisibility(ctx, s, 1, 100, true, []uint{10}))
    // 软删 sub=10 (模拟 DeleteSubUser 路径)
    require.NoError(t, s.SopVisibilityGrant().CleanupBySubUser(ctx, s.DB(), 10))
    // 同一 SOP 重新 PUT, sub_user_ids 含完全不同的 sub=11
    seedSubUser(t, s, 11, 1)
    err := biz.UpdateSopVisibility(ctx, s, 1, 100, true, []uint{11})
    require.NoError(t, err, "ReplaceGrantsTx physical delete should clear soft-deleted residue")
}
```

- [ ] **Step 5: 全测试 + race detection**

```bash
go test ./internal/numind/biz/... -race -v -run TestUpdate
go test ./internal/numind/biz/... -race -v -run TestSopVisibility
```
Expected: 全部 PASS。

- [ ] **Step 6: Lint + Commit**

```bash
task lint
git add internal/numind/biz/sop/visibility_test.go internal/numind/biz/chatbot/visibility_test.go
git commit -m "test(visibility-scope): 14 unit test cases (13 spec §10.2 + 1 concurrent PUT)"
```

---

# Phase 7：前端基础组件

## Task 17: api/visibility.ts

**Files:**
- Create: `numind-web-v3/src/api/visibility.ts`

**Spec 引用**: §6.6

- [ ] **Step 1: 写 API 函数**

```ts
import { request } from './request'

export interface VisibilityState {
  restricted: boolean
  sub_user_ids: number[]
}

export interface VisibilityUpdatePayload {
  restricted: boolean
  sub_user_ids?: number[]
}

export function getSopVisibility(id: number) {
  return request.get<VisibilityState>(`/v1/sop/templates/${id}/visibility`)
}

export function putSopVisibility(id: number, body: VisibilityUpdatePayload) {
  return request.put(`/v1/sop/templates/${id}/visibility`, body)
}

export function getChatbotVisibility(id: number) {
  return request.get<VisibilityState>(`/v1/chatbot/${id}/visibility`)
}

export function putChatbotVisibility(id: number, body: VisibilityUpdatePayload) {
  return request.put(`/v1/chatbot/${id}/visibility`, body)
}
```

- [ ] **Step 2: type-check + lint**

```bash
cd numind-web-v3
npm run type-check && npm run lint
```

- [ ] **Step 3: Commit**

```bash
git add src/api/visibility.ts
git commit -m "feat(visibility-scope): visibility API layer (4 functions)"
```

---

## Task 18: SubUserMultiSelectDialog.vue

**Files:**
- Create: `numind-web-v3/src/components/SubUserMultiSelectDialog.vue`

**Spec 引用**: §6.2

- [ ] **Step 1: 写组件骨架**

```vue
<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { getSubUsers } from '@/api/customers'

interface Props {
  modelValue: number[]
  visible: boolean
  searchable?: boolean
  title?: string
}

const props = withDefaults(defineProps<Props>(), {
  searchable: true,
  title: '选择子用户',
})

const emit = defineEmits<{
  'update:modelValue': [val: number[]]
  'update:visible': [val: boolean]
  'confirm': [selected: number[]]
  'cancel': []
}>()

const subUsers = ref<{ id: number; nickname: string; phone: string }[]>([])
const loading = ref(false)
const errorMsg = ref('')
const search = ref('')
const selected = ref<number[]>([...props.modelValue])

const filteredUsers = computed(() => {
  if (!search.value) return subUsers.value
  const q = search.value.toLowerCase()
  return subUsers.value.filter(u =>
    u.nickname.toLowerCase().includes(q) || u.phone.includes(q)
  )
})

const allSelected = computed(() =>
  filteredUsers.value.length > 0 &&
  filteredUsers.value.every(u => selected.value.includes(u.id))
)

watch(() => props.visible, async (v) => {
  if (v) {
    loading.value = true
    errorMsg.value = ''
    try {
      const res = await getSubUsers()
      subUsers.value = res.data.list || []
      selected.value = [...props.modelValue]
    } catch (err: any) {
      errorMsg.value = err.message || '加载失败'
    } finally {
      loading.value = false
    }
  }
})

function toggleAll() {
  if (allSelected.value) {
    const filteredIds = filteredUsers.value.map(u => u.id)
    selected.value = selected.value.filter(id => !filteredIds.includes(id))
  } else {
    filteredUsers.value.forEach(u => {
      if (!selected.value.includes(u.id)) selected.value.push(u.id)
    })
  }
}

function onConfirm() {
  emit('update:modelValue', selected.value)
  emit('confirm', selected.value)
  emit('update:visible', false)
}

function onCancel() {
  emit('cancel')
  emit('update:visible', false)
}
</script>

<template>
  <div v-if="visible" class="modal-overlay" @click.self="onCancel">
    <div class="modal-content">
      <div class="modal-header">
        <h3>{{ title }}</h3>
      </div>
      <div class="modal-body">
        <div v-if="loading" class="loading">加载中...</div>
        <div v-else-if="errorMsg" class="error">
          {{ errorMsg }}
          <button @click="$emit('update:visible', true)">重试</button>
        </div>
        <div v-else-if="subUsers.length === 0" class="empty">
          您还没有子用户
          <button @click="$router.push('/customers')">去添加</button>
        </div>
        <div v-else>
          <input v-if="searchable" v-model="search" placeholder="搜索昵称或手机号" />
          <label>
            <input type="checkbox" :checked="allSelected" @change="toggleAll" />
            全选（{{ filteredUsers.length }}）
          </label>
          <ul class="user-list">
            <li v-for="u in filteredUsers" :key="u.id">
              <label>
                <input type="checkbox" :value="u.id" v-model="selected" />
                {{ u.nickname }} <span class="phone">{{ u.phone }}</span>
              </label>
            </li>
          </ul>
        </div>
      </div>
      <div class="modal-footer">
        <button @click="onCancel">取消</button>
        <button class="primary" @click="onConfirm">确认（已选 {{ selected.length }}）</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 样式参考 @DESIGN.md 既有 dialog 模式 */
.modal-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; z-index: 1000; }
.modal-content { background: white; border-radius: 8px; width: min(90vw, 480px); max-height: 80vh; display: flex; flex-direction: column; }
.modal-header, .modal-footer { padding: 16px 20px; }
.modal-body { padding: 0 20px; overflow-y: auto; flex: 1; }
.user-list { list-style: none; padding: 0; }
.user-list li { padding: 8px 0; border-bottom: 1px solid #eee; }
.phone { color: #888; margin-left: 8px; font-size: 12px; }
button.primary { background: var(--brand-primary, #2563eb); color: white; }
</style>
```

- [ ] **Step 2: type-check + lint**

```bash
npm run type-check && npm run lint
```

- [ ] **Step 3: Commit**

```bash
git add src/components/SubUserMultiSelectDialog.vue
git commit -m "feat(visibility-scope): SubUserMultiSelectDialog reusable component"
```

---

## Task 19: VisibilityScopeCard.vue

**Files:**
- Create: `numind-web-v3/src/components/VisibilityScopeCard.vue`

**Spec 引用**: §6.3

- [ ] **Step 1: 写组件**

```vue
<script setup lang="ts">
import { ref, computed } from 'vue'
import SubUserMultiSelectDialog from './SubUserMultiSelectDialog.vue'
import { useUserStore } from '@/stores/user'

interface VisibilityValue {
  restricted: boolean
  subUserIDs: number[]
}

interface Props {
  modelValue: VisibilityValue
  entityType: 'sop' | 'chatbot'
  disabled?: boolean
  dirty?: boolean
  loading?: boolean  // AC-22: visibility GET 进行中显示 skeleton
}

const props = withDefaults(defineProps<Props>(), {
  disabled: false,
  dirty: false,
  loading: false,
})

const emit = defineEmits<{
  'update:modelValue': [val: VisibilityValue]
  'retry': []  // P1-2: 重试按钮触发
}>()

const userStore = useUserStore()
const hasSubUsers = computed(() => userStore.hasSubUsers)
const dialogVisible = ref(false)
const showConfirmDisable = ref(false)
const showHistoryHint = ref(false)

const entityLabel = computed(() => props.entityType === 'sop' ? 'SOP' : '智能体')

function onToggle(val: boolean) {
  if (val) {
    // 从关到开：若有历史名单显示提示
    if (props.modelValue.subUserIDs.length > 0) {
      showHistoryHint.value = true
    } else {
      // 立即打开弹窗 (开了但没选不允许)
      emit('update:modelValue', { restricted: true, subUserIDs: [] })
      dialogVisible.value = true
    }
  } else {
    // 从开到关：弹确认 (保留名单)
    if (props.modelValue.subUserIDs.length > 0) {
      showConfirmDisable.value = true
    } else {
      emit('update:modelValue', { restricted: false, subUserIDs: [] })
    }
  }
}

function confirmDisable() {
  emit('update:modelValue', { restricted: false, subUserIDs: props.modelValue.subUserIDs }) // 保留名单
  showConfirmDisable.value = false
}

function keepHistory() {
  emit('update:modelValue', { restricted: true, subUserIDs: props.modelValue.subUserIDs })
  showHistoryHint.value = false
}

function clearAndReselect() {
  emit('update:modelValue', { restricted: true, subUserIDs: [] })
  showHistoryHint.value = false
  dialogVisible.value = true
}

function onDialogConfirm(ids: number[]) {
  emit('update:modelValue', { restricted: true, subUserIDs: ids })
}
</script>

<template>
  <div class="visibility-card">
    <h3>可见范围</h3>
    <div v-if="loading" class="skeleton">
      <div class="skeleton-line" style="width: 60%;"></div>
      <div class="skeleton-line" style="width: 30%;"></div>
    </div>
    <div v-else-if="!hasSubUsers" class="empty-hint">
      您还没有子用户。添加子用户后才能设置 {{ entityLabel }} 的可见范围。
    </div>
    <div v-else>
      <label class="toggle">
        <input
          type="checkbox"
          :checked="modelValue.restricted"
          :disabled="disabled"
          @change="(e: any) => onToggle(e.target.checked)"
        />
        仅指定子用户可见
      </label>
      <div v-if="modelValue.restricted" class="actions">
        <span>已选 {{ modelValue.subUserIDs.length }} 位</span>
        <button @click="dialogVisible = true">选择子用户</button>
      </div>
      <div v-if="dirty" class="error-banner">
        可见范围未保存
        <button @click="$emit('retry')">重试</button>
      </div>
    </div>

    <SubUserMultiSelectDialog
      v-model="modelValue.subUserIDs"
      :visible="dialogVisible"
      @update:visible="dialogVisible = $event"
      @confirm="onDialogConfirm"
    />

    <!-- 从开到关确认 -->
    <div v-if="showConfirmDisable" class="confirm-overlay" @click.self="showConfirmDisable = false">
      <div class="confirm-box">
        <p>已配置 {{ modelValue.subUserIDs.length }} 位子用户的名单将保留，下次打开恢复。仍要关闭吗？</p>
        <button @click="showConfirmDisable = false">取消</button>
        <button class="primary" @click="confirmDisable">关闭</button>
      </div>
    </div>

    <!-- 从关到开 历史名单提示 -->
    <div v-if="showHistoryHint" class="confirm-overlay" @click.self="showHistoryHint = false">
      <div class="confirm-box">
        <p>上次已配置 {{ modelValue.subUserIDs.length }} 位子用户</p>
        <button @click="clearAndReselect">清空重选</button>
        <button class="primary" @click="keepHistory">保留并打开</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.visibility-card { padding: 20px; border: 1px solid #eee; border-radius: 8px; }
.error-banner { background: #fef2f2; color: #c00; padding: 12px; margin-top: 12px; border-radius: 4px; }
.confirm-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; z-index: 1100; }
.confirm-box { background: white; padding: 24px; border-radius: 8px; max-width: 400px; }
button.primary { background: var(--brand-primary, #2563eb); color: white; }
</style>
```

- [ ] **Step 2: 验证 useUserStore().hasSubUsers 存在**

```bash
grep -n "hasSubUsers" src/stores/user.ts
```

如不存在，S4 implementer 需要在 user store 加 computed `hasSubUsers` getter（基于现有 subUsers count 或新发起一次 getSubUsers 调用缓存）。

- [ ] **Step 3: type-check + lint**

```bash
npm run type-check && npm run lint
```

- [ ] **Step 4: Commit**

```bash
git add src/components/VisibilityScopeCard.vue
git commit -m "feat(visibility-scope): VisibilityScopeCard component with dirty/retry support"
```

---

# Phase 8：前端编辑页接入

## Task 20: SOP 编辑页 store + 接入

**Files:**
- Modify: `numind-web-v3/src/views/sop/SopTemplateEdit.vue`（S4 grep 确认路径）
- Modify: SOP 编辑页 store（S4 grep 确认）

**Spec 引用**: §6.4, §6.5

- [ ] **Step 1: grep 定位编辑页与 store**

```bash
grep -rn "PUT.*sop/templates" src/views/ src/api/
grep -rn "useSopTemplate" src/stores/
```

- [ ] **Step 2: store 加 5 字段 + load/save 函数**

按 spec §6.4：

```ts
const state = reactive({
  // ... 既有字段 ...
  visibilityRestricted: false,
  visibilitySubUserIDs: [] as number[],
  visibilityLoaded: false,
  visibilityOriginalRestricted: false,
  visibilityDirty: false,
})

async function loadVisibility(sopID: number) {
  const res = await getSopVisibility(sopID)
  state.visibilityRestricted = res.data.restricted
  state.visibilityOriginalRestricted = res.data.restricted
  state.visibilitySubUserIDs = res.data.sub_user_ids
  state.visibilityLoaded = true
  state.visibilityDirty = false
}

async function saveVisibility(sopID: number) {
  try {
    await putSopVisibility(sopID, {
      restricted: state.visibilityRestricted,
      sub_user_ids: state.visibilityRestricted ? state.visibilitySubUserIDs : undefined,
    })
    state.visibilityOriginalRestricted = state.visibilityRestricted
    state.visibilityDirty = false
  } catch (err) {
    state.visibilityDirty = true
    throw err
  }
}
```

- [ ] **Step 3: 编辑页加 VisibilityScopeCard + 两阶段 onSave**

```vue
<VisibilityScopeCard
  v-model="visibilityValue"
  entity-type="sop"
  :dirty="store.state.visibilityDirty"
  @retry="onRetryVisibility"
/>
```

```ts
const visibilityValue = computed({
  get: () => ({
    restricted: store.state.visibilityRestricted,
    subUserIDs: store.state.visibilitySubUserIDs,
  }),
  set: (v) => {
    store.state.visibilityRestricted = v.restricted
    store.state.visibilitySubUserIDs = v.subUserIDs
  },
})

async function onSave() {
  try {
    await saveTemplate()
  } catch (err) {
    toast.error("模板保存失败"); return
  }
  if (store.state.visibilityDirty ||
      store.state.visibilityRestricted !== store.state.visibilityOriginalRestricted) {
    try {
      await store.saveVisibility(sopID)
    } catch (err) {
      toast.warning("模板已保存, 但可见范围更新失败. 请检查后重试"); return
    }
  }
  toast.success("已保存")
  router.push("/sop/templates")
}

async function onRetryVisibility() {
  try { await store.saveVisibility(sopID); toast.success("已保存") }
  catch (err: any) { toast.error(err.message) }
}
```

- [ ] **Step 4: onMounted 调 loadVisibility + loading skeleton（AC-22）**

```ts
const visibilityLoading = ref(false)

onMounted(async () => {
  await store.loadTemplate(sopID)
  visibilityLoading.value = true
  try {
    await store.loadVisibility(sopID)
  } finally {
    visibilityLoading.value = false
  }
})
```

VisibilityScopeCard 接收 `:loading="visibilityLoading"` prop（Task 19 已含 4 状态处理 `<div v-if="loading" class="loading">加载中...</div>`，或在卡片顶部加 skeleton）：

```vue
<VisibilityScopeCard
  v-model="visibilityValue"
  entity-type="sop"
  :loading="visibilityLoading"
  :dirty="store.state.visibilityDirty"
  @retry="onRetryVisibility"
/>
```

> Task 19 的 VisibilityScopeCard Props 接口同步新增 `loading?: boolean`，组件内顶部加 `<div v-if="loading" class="skeleton">...</div>` 区块；Task 19 实施时一并完成。

- [ ] **Step 5: type-check + lint**

```bash
npm run type-check && npm run lint
```

- [ ] **Step 6: Commit**

```bash
git add src/stores/sopTemplateEdit.ts src/views/sop/SopTemplateEdit.vue
git commit -m "feat(visibility-scope): integrate VisibilityScopeCard into SOP edit page"
```

---

## Task 21: Chatbot 编辑页 store + 接入

**Files:**
- Modify: chatbot 编辑页路径（S4 grep 确认）+ 对应 store

**Spec 引用**: §6.4, §6.5（同 SOP，对称）

- [ ] **Step 1: grep 定位**

```bash
grep -rn "PUT.*chatbot/" src/views/ src/api/
grep -rn "useChatbot.*Edit" src/stores/
```

- [ ] **Step 2-6: 完全照搬 Task 20 模式，替换：**
- `getSopVisibility` → `getChatbotVisibility`
- `putSopVisibility` → `putChatbotVisibility`
- `saveTemplate` → `saveChatbot`
- `sopID` → `chatbotID`
- `entity-type="sop"` → `entity-type="chatbot"`
- `/sop/templates` → `/chatbot`（跳转路径）

```bash
npm run type-check && npm run lint
git add src/stores/chatbotEdit.ts src/views/chatbot/ChatbotEdit.vue
git commit -m "feat(visibility-scope): integrate VisibilityScopeCard into chatbot edit page"
```

---

# Phase 9：S5 验证策略

## Task 22: Playwright E2E spec

**Files:**
- Create: `numind-web-v3/e2e/sop-chatbot-visibility-scope.spec.ts`

**Spec 引用**: §10.1

- [ ] **Step 1: 写 E2E 测试**

```ts
import { test, expect } from '@playwright/test'

// E2E_USERNAME / E2E_PASSWORD 父账户; 通过 admin API 提前 seed 一个父账户名下两个子账户
const PARENT = { username: process.env.E2E_PARENT_USERNAME!, password: process.env.E2E_PARENT_PASSWORD! }
const SUB_A = { username: process.env.E2E_SUB_A_USERNAME!, password: process.env.E2E_SUB_A_PASSWORD! }
const SUB_B = { username: process.env.E2E_SUB_B_USERNAME!, password: process.env.E2E_SUB_B_PASSWORD! }

test.describe('SOP 可见范围权限', () => {
  test('父账户配置 → 子用户 A 可见 / 子用户 B 不可见', async ({ page, browser }) => {
    let sopID: string  // 测试中创建/找到的 SOP ID, 后续步骤复用

    // Step 1: 父账户登录, 进 SOP 编辑页配置可见范围
    await page.goto('/login')
    await page.fill('[name="username"]', PARENT.username)
    await page.fill('[name="password"]', PARENT.password)
    await page.click('button[type="submit"]')
    await page.waitForURL(/home|sop/)

    await page.goto('/sop/templates')
    // 点击列表第一个 SOP 进入编辑页, 从 URL 提取 sopID
    await page.locator('[data-test="sop-template-row"]').first().click()
    await page.waitForURL(/\/sop\/templates\/edit\/\d+/)
    const m = page.url().match(/\/edit\/(\d+)/)
    expect(m).toBeTruthy()
    sopID = m![1]

    await page.locator('text=仅指定子用户可见').click()
    await page.locator('text=选择子用户').click()
    await page.locator(`text=${SUB_A.username}`).click()
    await page.locator('text=确认').click()
    await page.click('button:has-text("保存")')
    await expect(page.locator('text=已保存')).toBeVisible()

    // Step 2: 子用户 A 登录, 应看到该 SOP
    const ctxA = await browser.newContext()
    const pageA = await ctxA.newPage()
    await pageA.goto('/login')
    await pageA.fill('[name="username"]', SUB_A.username)
    await pageA.fill('[name="password"]', SUB_A.password)
    await pageA.click('button[type="submit"]')
    await pageA.goto('/sop/templates')
    await expect(pageA.locator(`[data-test="sop-template-row"][data-sop-id="${sopID}"]`)).toBeVisible()

    // Step 3: 子用户 B 登录, 应看不到该 SOP
    const ctxB = await browser.newContext()
    const pageB = await ctxB.newPage()
    await pageB.goto('/login')
    await pageB.fill('[name="username"]', SUB_B.username)
    await pageB.fill('[name="password"]', SUB_B.password)
    await pageB.click('button[type="submit"]')
    await pageB.goto('/sop/templates')
    await expect(pageB.locator(`[data-test="sop-template-row"][data-sop-id="${sopID}"]`)).not.toBeVisible()

    // Step 4: 父账户取消勾选 sub_a
    await page.goto(`/sop/templates/edit/${sopID}`)
    await page.locator('text=选择子用户').click()
    await page.locator(`text=${SUB_A.username}`).click() // 取消
    await page.locator('text=确认').click()
    await page.click('button:has-text("保存")')
    await expect(page.locator('text=已保存')).toBeVisible()

    // Step 5: 子用户 A 重登, 看不到
    await pageA.goto('/sop/templates')
    await expect(pageA.locator(`[data-test="sop-template-row"][data-sop-id="${sopID}"]`)).not.toBeVisible()

    // Step 6: D3 保留语义验证
    await page.goto(`/sop/templates/edit/${sopID}`)
    await page.locator('text=仅指定子用户可见').click() // 关闭
    await page.locator('text=关闭').click() // 确认对话框
    await page.click('button:has-text("保存")')
    await page.reload()
    await page.locator('text=仅指定子用户可见').click() // 重新打开
    await expect(page.locator('text=上次已配置')).toBeVisible()  // D3 历史名单提示
  })

  test('chatbot 路径 (对称)', async ({ page, browser }) => {
    // 类似 SOP 测试, 但操作 chatbot 模块
    // ... (省略, 同结构)
  })
})
```

- [ ] **Step 2: 跑 E2E（需要本地 dev 环境 + seed 数据）**

```bash
cd numind-web-v3
E2E_PARENT_USERNAME=$E2E_USERNAME E2E_PARENT_PASSWORD=$E2E_PASSWORD \
E2E_SUB_A_USERNAME=... E2E_SUB_A_PASSWORD=... \
E2E_SUB_B_USERNAME=... E2E_SUB_B_PASSWORD=... \
npm run test:e2e -- visibility-scope.spec
```

注：S4 阶段先写测试代码，实际跑通由 S5 阶段（本地 task dev + dev 数据 seed）。S4 仅确保 TypeScript 编译通过、Playwright DSL 语法合法。

- [ ] **Step 3: type-check**

```bash
npm run type-check
```

- [ ] **Step 4: Commit**

```bash
git add e2e/sop-chatbot-visibility-scope.spec.ts
git commit -m "test(visibility-scope): Playwright E2E spec (parent config + sub visibility + D3 retention)"
```

---

## Task 23: S5 验证策略 task（NDF Rule 10 强制）

**Files:**
- Create: `numind-server/docs/superpowers/specs/2026-05-13-sop-chatbot-visibility-scope-validation-strategy.md`

**Spec 引用**: §10

- [ ] **Step 1: 写验证策略文档**

```markdown
# S5 验证策略 — sop-chatbot-visibility-scope

## 验证方式选择

**Playwright E2E（必需）+ 后端单元测试（必需）+ gstack /qa 截图回归（必需）**

### 理由

权限主流程不能仅靠一次性截图验证（NDF Rule 10）。本功能涉及：
- 两层 gate 串行 (visibility → run-permission) 的语义边界
- 跨用户身份的列表可见性变化
- D3 保留语义跨多次操作的稳定性

必须有 Playwright E2E 提供持久化回归保护。

## 关键用户路径

### 路径 1: 父账户配置 SOP 可见范围 → 子用户列表生效
1. 父账户登录, 进入 SOP 编辑页
2. 打开「仅指定子用户可见」开关
3. 弹窗勾选子用户 A
4. 保存
5. 子用户 A 登录: 工作区列表能看到该 SOP
6. 子用户 B 登录: 工作区列表看不到该 SOP

### 路径 2: chatbot 路径 (对称)

### 路径 3: D3 保留语义
1. 父账户配置 SOP visibility, 选 3 个子用户
2. 关闭开关
3. 重新打开开关
4. 弹窗提示「上次已配置 3 位子用户」, 名单完整恢复

### 路径 4: 子用户级联清理
1. 父账户配置 SOP visibility, 选子用户 A + B
2. 父账户从客户管理删除子用户 A
3. 重新打开该 SOP visibility 弹窗
4. 弹窗中仅显示子用户 B (A 已级联清理)

### 路径 5: 实体删除清理 (EC-6)
1. 父账户配置 SOP visibility 后删除该 SOP
2. 数据库中 sop_visibility_grant 记录已软删除
3. (后端单元测试覆盖, 无 E2E)

### 路径 6: 越权防御
1. 父账户 X 尝试配置父账户 Y 创建的 SOP visibility
2. 返回 403 ErrEntityNotOwnedByCaller
3. 父账户 X 提交父账户 Y 名下子用户 ID
4. 返回 422 ErrCrossParentSubUser

## 验证清单

- [ ] `task lint` 退出码 0
- [ ] `go test ./... -race` 退出码 0
- [ ] `npm run lint` (numind-web-v3) 退出码 0
- [ ] `npm run type-check` (numind-web-v3) 退出码 0
- [ ] `npm run test:e2e -- visibility-scope.spec` 退出码 0
- [ ] gstack /qa 截图: SOP 编辑页 + SOP 工作区列表 + chatbot 编辑页 + chatbot 工作区列表 4 张
- [ ] 父账户身份手动验证: 自己创建的 SOP/chatbot 列表不受 visibility 影响 (I-10)
- [ ] Migration 在本地 SQLite 试跑 forward + rollback 干净
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-05-13-sop-chatbot-visibility-scope-validation-strategy.md
git commit -m "docs(visibility-scope): S5 validation strategy (E2E + tests + qa screenshots)"
```

---

## 完结检查清单

S4 实施全部 23 task 完成后：

- [ ] manifest progress.completed_tasks = 23
- [ ] manifest progress.reviewed_tasks = 23（每 task 完成后都需走 NDF Rule 6 两阶段 review）
- [ ] 后端两仓 `task lint` + `go test ./... -race` 全通过
- [ ] 前端 `npm run lint` + `npm run type-check` 全通过
- [ ] 前端 `npm run test:e2e` 全通过
- [ ] gstack /qa 4 张截图回归通过
- [ ] 进入 S5 自动验收阶段
