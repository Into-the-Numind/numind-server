# SOP / 销售智能体 父账户归属隔离 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Layer 0 多租户隔离修复——SOP 列表 SQL 加 owner 过滤 + 新建销售智能体 owner tag 表 + 修复 admin/user 两条 SOP 写入路径，让"哪个父账户拥有，只有该父账户及其子用户可见"成为 SQL 级确定性事实。

**Architecture:** 单仓库（numind-server）。三层重构：(1) 新建 `sales_agent_owner(parent_user_id PK)` 极简表存销售智能体 owner tag (2) `sopStore.ListVisibleTemplates` 加 `ownerParentUserID` 参数 + SQL 防御性 `IS NOT NULL` (3) `HasFeaturePermission` dispatch 上移 biz 层（顺手修既有 middleware→store 直调的 layer violation），sales_agent 分支双层 AND 检查，其他 feature_key 保留父账户 bypass。`creator_user_id` 语义升级为"始终存父账户 id"，user/admin 两条写入路径都加上 owner resolution。无前端改动。

**Tech Stack:** Go 1.24 + Gin + GORM + MySQL 8.0

**Spec 引用**: [2026-05-18-sop-salesrag-parent-scope-design.md](../specs/2026-05-18-sop-salesrag-parent-scope-design.md)（S2 gate 通过，含 9 决策 D1-D9 + adversarial reviewer 2 P0 + 6 P1 已吸收）

---

## 文件清单

### 新建
| 路径 | 职责 |
|---|---|
| `migrations/20260518_220000_sop_salesrag_parent_scope.sql` | Forward migration（CREATE sales_agent_owner + INSERT user 30 + UPDATE 2 SOP）|
| `migrations/20260518_220000_sop_salesrag_parent_scope_rollback.sql` | Rollback |
| `internal/pkg/model/sales_agent_owner.go` | SalesAgentOwner GORM model |
| `internal/numind/store/sales_agent_owner.go` | ISalesAgentOwnerStore + 实现 |
| `internal/numind/store/sales_agent_owner_test.go` | 4 个 store 单测 |

### 修改
| 路径 | 改动内容 |
|---|---|
| `internal/pkg/model/sop.go` | SopTemplate.CreatorUserID 字段注释升级反映租户 owner 语义（D5）|
| `internal/numind/store/store.go` | IStore interface 加 `SalesAgentOwners()` + 工厂注册 |
| `internal/numind/store/sop.go` | `ListVisibleTemplates` 签名加 `ctx context.Context, ownerParentUserID uint`（2-axis 变更）+ SQL 加 owner 过滤 + IS NOT NULL 防御（D7）|
| `internal/numind/store/customer.go` | 删除 `HasFeaturePermission`；新增 `CheckSubUserFeatureGrant(ctx, subUserID, featureKey)` 纯查询（D2）|
| `internal/numind/biz/biz.go` | 新增包级 `var B IBiz` 全局变量 + 在 `NewBiz` 初始化时设置（D2 wiring 前置）|
| `internal/numind/biz/sop/sop.go` | (a) `ListVisibleTemplates(WithPermission)` 改 owner 参数 (b) `CreateTemplateByUser` 加 ParentUserOnly assertion + 仅改 CreatorUserID 一行，保留 TrailingChatEnabled / GORM default:true fixup / log 等所有现有逻辑（D1+D8）(c) `CreateTemplate` 加 `adminUserID` 参数，保留 GrantTemplateToConfiguredSubUsers 自动授权（D6）|
| `internal/numind/biz/customer/customer.go` | `CheckFeaturePermission` 改为 dispatch + 新增 `hasSalesAgentPermission` helper（D2）|
| `internal/numind/biz/monitor/monitor.go` | line 146 `mb.store.Customers().HasFeaturePermission(...)` 改调 `biz.B.Customers().CheckFeaturePermission(...)`（caller 迁移）|
| `internal/numind/controller/v1/admin_sop/sop.go` | `CreateTemplate` 调用传入 adminUserID（从 `middleware.GetCurrentUser(c)` helper 取）|
| `internal/pkg/middleware/middleware.go` | `FeaturePermission` 改调 `biz.B.Customers().CheckFeaturePermission`（修 layer violation）|
| `internal/numind/numind.go` | wire 中调 `biz.NewBiz(ds)` 时同步赋值 `biz.B`（确保 store.S 已初始化后）|
| `internal/numind/store/sop_template_visibility_test.go`（沿用现有 visibility 测试文件，避免新建 sop_test.go）| 新增 3 个 ListVisibleTemplates 测试 |
| `internal/numind/store/customer_test.go` | `HasFeaturePermission` 测试迁移到 `CheckSubUserFeatureGrant` |
| `internal/numind/store/customer_permission_lifecycle_test.go` | 11 处 `HasFeaturePermission` 调用全部迁移到 `CheckSubUserFeatureGrant`（或必要时通过 biz）|
| `internal/numind/biz/customer/customer_test.go` | 加 9 个 CheckFeaturePermission 矩阵测试 |
| `internal/numind/biz/sop/sop_test.go` | 加 7 个 SOP biz 测试（list + create 路径）|

---

## TOC（6 个原子 task）

### Phase 1：数据基础设施
- **Task 1**: Migration SQL + SalesAgentOwner model + Store + 注册 DataStore

### Phase 2：SOP 列表 Owner 过滤
- **Task 2**: SopStore.ListVisibleTemplates 签名 + 全部 biz 调用方原子改造

### Phase 3：HasFeaturePermission 跨层重构
- **Task 3**: store 拆纯查询 + biz dispatch + middleware redirect（一次性 atomic 重构）

### Phase 4：SOP 写入路径修复
- **Task 4**: SopBiz.CreateTemplateByUser 父账户 invariant 改造（D1 + D8）
- **Task 5**: SopBiz.CreateTemplate (admin) + admin controller 改造（D6）

### Phase 5：S5 验证策略
- **Task 6**: S5 验证策略 doc（NDF Rule 10 强制）

---

# Phase 1：数据基础设施

## Task 1: Migration + Model + Store

**Files:**
- Create: `numind-server/migrations/20260518_220000_sop_salesrag_parent_scope.sql`
- Create: `numind-server/migrations/20260518_220000_sop_salesrag_parent_scope_rollback.sql`
- Create: `numind-server/internal/pkg/model/sales_agent_owner.go`
- Create: `numind-server/internal/numind/store/sales_agent_owner.go`
- Create: `numind-server/internal/numind/store/sales_agent_owner_test.go`
- Modify: `numind-server/internal/pkg/model/sop.go`（仅注释升级 line 16 的 `CreatorUserID`）
- Modify: `numind-server/internal/numind/store/store.go`（IStore interface 加方法 + 工厂注册）

**Spec 引用**: §2.1, §2.2, §3.1, §7.1, §8.2

- [ ] **Step 1: 写 forward migration SQL**

文件 `migrations/20260518_220000_sop_salesrag_parent_scope.sql`，内容照搬 spec §7.1：

```sql
-- +migrate Up
-- sop-salesrag-parent-scope: 多租户 Layer 0 父账户归属隔离
-- 见 docs/superpowers/specs/2026-05-18-sop-salesrag-parent-scope-design.md
--
-- 操作:
--   1. CREATE TABLE sales_agent_owner (IF NOT EXISTS 幂等)
--   2. INSERT user 30 (INSERT IGNORE 幂等)
--   3. UPDATE sop_template SET creator_user_id=30 WHERE id IN (1, 2)
--
-- 注: 本仓库 migration 由人工 SSH 跑 (CI 不跑, 见 memory dev_deploy_migration_gap)

CREATE TABLE IF NOT EXISTS sales_agent_owner (
  parent_user_id INT UNSIGNED NOT NULL PRIMARY KEY,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_sao_parent FOREIGN KEY (parent_user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='销售智能体父账户归属表（owner tag）';

INSERT IGNORE INTO sales_agent_owner (parent_user_id, created_at)
  VALUES (30, NOW(3));

UPDATE sop_template SET creator_user_id = 30 WHERE id IN (1, 2);

-- 部署后人工 verify:
--   SELECT COUNT(*) FROM sales_agent_owner WHERE parent_user_id=30;  -- 应为 1
--   SELECT id, creator_user_id FROM sop_template WHERE id IN (1,2,3,4);  -- 应均为 30
```

- [ ] **Step 2: 写 rollback migration SQL**

文件 `migrations/20260518_220000_sop_salesrag_parent_scope_rollback.sql`：

```sql
-- +migrate Down
-- Rollback for sop-salesrag-parent-scope.
-- WARNING: 会重新打开数据泄露 bug, 仅 critical incident 使用.

UPDATE sop_template SET creator_user_id = NULL WHERE id IN (1, 2);
DROP TABLE IF EXISTS sales_agent_owner;
```

- [ ] **Step 3: SalesAgentOwner GORM model**

文件 `internal/pkg/model/sales_agent_owner.go`：

```go
package model

import "time"

// SalesAgentOwner 销售智能体父账户归属表 (owner tag)
//
// 每行表示一个父账户拥有"销售智能体卡片"的访问权。该表是销售智能体的
// owner tag 存储——与 chatbot_config.user_id 对销售智能体的等价概念。
//
// 极简设计 (spec D3): 不启用 GORM soft-delete、无 updated_at。
// 写入仅在 migration 或手工 SQL (无 admin UI); 撤销走 hard DELETE。
// FK 到 user(id) ON DELETE CASCADE 保证父账户被删时无残留。
type SalesAgentOwner struct {
    ParentUserID uint      `gorm:"primaryKey;type:int unsigned" json:"parent_user_id"`
    CreatedAt    time.Time `gorm:"type:datetime(3)" json:"created_at"`
}

// TableName 返回数据库表名
func (SalesAgentOwner) TableName() string {
    return "sales_agent_owner"
}
```

- [ ] **Step 4: 更新 SopTemplate.CreatorUserID 字段注释**

`internal/pkg/model/sop.go` line 16 附近：

```go
// CreatorUserID — 租户 owner 父账户 id（多租户归属，spec D1）。
// 2026-05-19 起语义升级：始终 = 父账户 user.id，永不为子账户 id。
// 在 biz.CreateTemplate (admin) / biz.CreateTemplateByUser (user) 两个写入路径中保证。
// nullable 为兼容历史脏数据；列表 SQL 防御性 `IS NOT NULL` 过滤 (spec D7)。
CreatorUserID *uint `gorm:"index:idx_st_creator" json:"creator_user_id"`
```

- [ ] **Step 5: SalesAgentOwnerStore + 接口**

文件 `internal/numind/store/sales_agent_owner.go`：

```go
package store

import (
    "context"

    "gorm.io/gorm"

    "numind-server/internal/pkg/model"
)

// ISalesAgentOwnerStore 销售智能体归属表数据访问接口
type ISalesAgentOwnerStore interface {
    // Exists 检查指定父账户是否拥有销售智能体。
    // 返回 (true, nil) 表示存在; (false, nil) 表示不存在 (不返回 ErrRecordNotFound);
    // (false, err) 表示查询失败。
    Exists(ctx context.Context, parentUserID uint) (bool, error)
}

type salesAgentOwnerStore struct {
    db *gorm.DB
}

// NewSalesAgentOwnerStore 构造销售智能体归属表 store
func NewSalesAgentOwnerStore(db *gorm.DB) ISalesAgentOwnerStore {
    return &salesAgentOwnerStore{db: db}
}

var _ ISalesAgentOwnerStore = (*salesAgentOwnerStore)(nil)

// Exists 检查父账户是否在 owner 表中
func (s *salesAgentOwnerStore) Exists(ctx context.Context, parentUserID uint) (bool, error) {
    var count int64
    err := s.db.WithContext(ctx).
        Model(&model.SalesAgentOwner{}).
        Where("parent_user_id = ?", parentUserID).
        Count(&count).Error
    if err != nil {
        return false, err
    }
    return count > 0, nil
}
```

⚠️ 检查正确的 module 路径前缀（grep 现有 store 文件的 import path 确认）。

- [ ] **Step 6: 注册 IStore interface + 工厂**

`internal/numind/store/store.go`：

1. 在 `DataStore` interface 加：
```go
SalesAgentOwners() ISalesAgentOwnerStore
```
位置：紧邻 `SopVisibilityGrant() / ChatbotVisibilityGrant()` 行（line 39-40 附近）

2. 在 `datastore` struct method 段加：
```go
// SalesAgentOwners 返回一个实现了 ISalesAgentOwnerStore 接口的实例
func (ds *datastore) SalesAgentOwners() ISalesAgentOwnerStore {
    return NewSalesAgentOwnerStore(ds.db)
}
```

- [ ] **Step 7: 写 4 个 store 单测**

文件 `internal/numind/store/sales_agent_owner_test.go`，参考 `customer_test.go:21-30` 的 sqlite 初始化模式：

```go
package store

import (
    "context"
    "testing"

    "github.com/stretchr/testify/require"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"

    "numind-server/internal/pkg/model"
)

func setupSalesAgentOwnerStoreTest(t *testing.T) (ISalesAgentOwnerStore, *gorm.DB) {
    tmp := t.TempDir()
    db, err := gorm.Open(sqlite.Open(tmp+"/sao_test.db?_busy_timeout=5000"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.AutoMigrate(&model.SalesAgentOwner{}))
    return NewSalesAgentOwnerStore(db), db
}

func TestSalesAgentOwner_Exists_True(t *testing.T) {
    s, db := setupSalesAgentOwnerStoreTest(t)
    ctx := context.Background()

    require.NoError(t, db.Create(&model.SalesAgentOwner{ParentUserID: 30}).Error)

    exists, err := s.Exists(ctx, 30)
    require.NoError(t, err)
    require.True(t, exists)
}

func TestSalesAgentOwner_Exists_False(t *testing.T) {
    s, _ := setupSalesAgentOwnerStoreTest(t)
    ctx := context.Background()

    exists, err := s.Exists(ctx, 30)
    require.NoError(t, err)
    require.False(t, exists, "empty table 必须返回 (false, nil), 不能是 ErrRecordNotFound")
}

func TestSalesAgentOwner_Exists_DifferentParentID(t *testing.T) {
    s, db := setupSalesAgentOwnerStoreTest(t)
    ctx := context.Background()

    require.NoError(t, db.Create(&model.SalesAgentOwner{ParentUserID: 30}).Error)

    exists, err := s.Exists(ctx, 1)  // admin 不在表中
    require.NoError(t, err)
    require.False(t, exists)
}

func TestSalesAgentOwner_Exists_DBClosed(t *testing.T) {
    s, db := setupSalesAgentOwnerStoreTest(t)
    sqlDB, _ := db.DB()
    sqlDB.Close()  // 模拟 DB 错误

    exists, err := s.Exists(context.Background(), 30)
    require.Error(t, err)
    require.False(t, exists)
}
```

⚠️ Note：SQLite 不支持 FK CASCADE 测试（spec §6.1 提到的 FK CASCADE 测试需在集成测试中跑，本 store 单测覆盖业务逻辑层）。

- [ ] **Step 8: 跑测试 + lint**

```bash
cd numind-server
go test ./internal/numind/store/ -run TestSalesAgentOwner -v
# 期望: 4 个测试全 PASS

task lint
# 期望: 0 errors
```

- [ ] **Step 9: Commit**

```bash
git add migrations/20260518_220000_sop_salesrag_parent_scope*.sql \
        internal/pkg/model/sales_agent_owner.go \
        internal/pkg/model/sop.go \
        internal/numind/store/sales_agent_owner.go \
        internal/numind/store/sales_agent_owner_test.go \
        internal/numind/store/store.go
git commit -m "feat(parent-scope): add sales_agent_owner table + model + store

新建 sales_agent_owner 极简表 (parent_user_id PK, FK CASCADE 到 user.id)
作为销售智能体的 owner tag 存储。升级 SopTemplate.CreatorUserID 注释
反映多租户 owner 语义 (spec D1, D3, D5)."
```

---

# Phase 2：SOP 列表 Owner 过滤

## Task 2: SopStore.ListVisibleTemplates 签名改造

**Files:**
- Modify: `numind-server/internal/numind/store/sop.go`（ISopStore interface + 实现）
- Modify: `numind-server/internal/numind/biz/sop/sop.go`（ISopBiz interface + 2 个 caller）
- Modify: `numind-server/internal/numind/store/sop_template_visibility_test.go`（沿用现有 visibility 测试文件，避免新建；更新原 ListVisibleTemplates 测试 + 加 3 个新测试）

**Spec 引用**: §3.4, §3.5, §4.1, §5.2 (AC6-AC9), §5.4 (B4, B5)

**关键 2-axis 签名变更**: store 层 `ListVisibleTemplates` 当前签名 `(offset, limit int)`，**没有 ctx**。本 task 同时加 `ctx context.Context` 和 `ownerParentUserID uint` 两个参数。Caller 必须同时传 ctx。

**关键不变量**：本 task 必须一次性 atomic commit，否则中间态系统无法编译。所有 store 和 biz 的 caller 必须同步修改。

- [ ] **Step 1: 修改 ISopStore.ListVisibleTemplates 接口签名**

`internal/numind/store/sop.go` 第 13-25 行附近（interface 定义）：

```go
// ListVisibleTemplates 列出指定父账户租户下 C 端用户可见的模板.
// 多租户隔离 (spec D1): WHERE creator_user_id = ownerParentUserID.
// 防御性 (spec D7): 列表永不返回 creator_user_id IS NULL 行.
ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error)
```

- [ ] **Step 2: 修改 ListVisibleTemplates 实现**

`internal/numind/store/sop.go:142` 附近：

```go
func (s *sopStore) ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error) {
    var templates []model.SopTemplate
    var total int64

    query := s.db.WithContext(ctx).Model(&model.SopTemplate{}).
        Where("creator_user_id = ?", ownerParentUserID).
        Where("creator_user_id IS NOT NULL").  // 防御性 (spec D7)
        Where("status = ?", "active").
        Where("publish_status = ?", model.SopPublishStatusPublished)

    if err := query.Count(&total).Error; err != nil {
        return nil, 0, err
    }
    if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&templates).Error; err != nil {
        return nil, 0, err
    }
    return templates, total, nil
}
```

- [ ] **Step 3: 修改 ISopBiz.ListVisibleTemplates 接口签名 + 实现**

`internal/numind/biz/sop/sop.go` 第 33-35 行（接口）+ 171-173 行（实现）：

Interface：
```go
// ListVisibleTemplates 列出 C 端用户可见的模板（不附 permission 标志，admin/debug 路径用）。
// owner 解析由调用方负责（biz 层不重复解析）。
ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error)
```

实现：
```go
func (b *sopBiz) ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error) {
    return b.ds.Sop().ListVisibleTemplates(ctx, ownerParentUserID, offset, limit)
}
```

- [ ] **Step 4: 修改 ListVisibleTemplatesWithPermission 解析 ownerID**

`internal/numind/biz/sop/sop.go:195` 附近，**仅修改头几行**，Layer 1/2 逻辑保持不变：

```go
func (b *sopBiz) ListVisibleTemplatesWithPermission(ctx context.Context, user *model.User, offset, limit int) ([]SopTemplateVisibleItem, int64, error) {
    // 解析当前用户所属父账户 id (Layer 0 过滤参数, spec §3.5)
    ownerID := user.ID
    if user.ParentUserID != nil {
        ownerID = *user.ParentUserID
    }

    templates, total, err := b.ds.Sop().ListVisibleTemplates(ctx, ownerID, offset, limit)
    if err != nil {
        return nil, 0, fmt.Errorf("ListVisibleTemplatesWithPermission: %w", err)
    }

    // 父账号: Layer 1/2 跳过 (保持现状)
    if user.ParentUserID == nil {
        items := make([]SopTemplateVisibleItem, 0, len(templates))
        for _, t := range templates {
            items = append(items, SopTemplateVisibleItem{SopTemplate: t, HasPermission: true})
        }
        return items, total, nil
    }

    // ... Layer 1 visibility 过滤 + Layer 2 run-permission 标志逻辑保持不变
    // (line 210-247 一字不改)
}
```

- [ ] **Step 5: Grep 其他 ListVisibleTemplates 调用方更新**

```bash
cd numind-server
grep -rn "ListVisibleTemplates\b" internal/ --include='*.go' | grep -v _test.go | grep -v "WithPermission"
```

期望命中：
- store/sop.go（接口 + 实现，本 task 已改）
- biz/sop/sop.go（接口 + 实现 + ListVisibleTemplatesWithPermission，本 task 已改）

如有其他 caller（例如 admin 接口），同步更新签名。

- [ ] **Step 6: 更新现有 ListVisibleTemplates 测试**

grep 仓库 `_test.go` 找所有 `ListVisibleTemplates(...)` 调用（store 层 + biz 层都有可能），加上 `ctx` 和 `ownerParentUserID` 参数。

- [ ] **Step 7: 加 3 个新的 store 测试**

文件 `internal/numind/store/sop_template_visibility_test.go` 末尾追加（沿用现有 visibility 测试文件 — `sop_test.go` 不存在，避免新建）：

```go
func TestListVisibleTemplates_FilterByOwner(t *testing.T) {
    s, db := setupSopStoreTest(t)  // 沿用既有 setup helper
    ctx := context.Background()

    // Seed: parentA(30) 2 published + parentB(31) 1 published + parentA 1 draft + 1 NULL
    parentA := uint(30)
    parentB := uint(31)
    db.Create(&model.SopTemplate{Name: "A1", CreatorUserID: &parentA, Status: "active", PublishStatus: "published"})
    db.Create(&model.SopTemplate{Name: "A2", CreatorUserID: &parentA, Status: "active", PublishStatus: "published"})
    db.Create(&model.SopTemplate{Name: "B1", CreatorUserID: &parentB, Status: "active", PublishStatus: "published"})
    db.Create(&model.SopTemplate{Name: "AD", CreatorUserID: &parentA, Status: "active", PublishStatus: "draft"})
    db.Create(&model.SopTemplate{Name: "NULL", CreatorUserID: nil, Status: "active", PublishStatus: "published"})

    // Query parentA: 仅 2 行
    items, total, err := s.ListVisibleTemplates(ctx, parentA, 0, 100)
    require.NoError(t, err)
    require.Equal(t, int64(2), total)
    require.Len(t, items, 2)
}

func TestListVisibleTemplates_DefensiveNullFilter(t *testing.T) {
    s, db := setupSopStoreTest(t)
    ctx := context.Background()

    db.Create(&model.SopTemplate{Name: "NULL", CreatorUserID: nil, Status: "active", PublishStatus: "published"})

    items, total, err := s.ListVisibleTemplates(ctx, 30, 0, 100)
    require.NoError(t, err)
    require.Equal(t, int64(0), total)
    require.Empty(t, items)
}

func TestListVisibleTemplates_EmptyForNonExistentParent(t *testing.T) {
    s, _ := setupSopStoreTest(t)
    ctx := context.Background()

    items, total, err := s.ListVisibleTemplates(ctx, 999, 0, 100)
    require.NoError(t, err)
    require.Equal(t, int64(0), total)
    require.Empty(t, items)
}
```

⚠️ `setupSopStoreTest` helper 不存在。在 `sop_template_visibility_test.go` 复用现有 setup（grep 该文件确认 helper 名字），或新建 `setupSopStoreTestDB(t) (ISopStore, *gorm.DB)` helper，沿用 `customer_test.go:21-30` 的 sqlite 初始化 + AutoMigrate(&model.SopTemplate{}) 模式。

⚠️ 测试用 `db.Create(&model.SopTemplate{...})` 不能直接设 `gorm.Model.ID`，因为 gorm.Model 内嵌 ID 字段会被 AutoIncrement 覆盖。如需固定 ID，用 INSERT raw SQL 或 `db.Exec("INSERT INTO ...")`。

- [ ] **Step 8: 跑测试**

```bash
cd numind-server
go test ./internal/numind/store/ -run TestListVisibleTemplates -v
go test ./internal/numind/biz/sop/ -v
go build ./...  # 全仓库编译检查
task lint
```

期望：全部 PASS，编译 0 错误。

- [ ] **Step 9: Commit**

```bash
git add internal/numind/store/sop.go internal/numind/biz/sop/sop.go \
        internal/numind/store/sop_template_visibility_test.go
git commit -m "feat(parent-scope): add owner filter to SOP list query

SopStore.ListVisibleTemplates 加 ownerParentUserID 参数, SQL WHERE
creator_user_id = ? AND creator_user_id IS NOT NULL (spec D7 防御).
biz 层 ListVisibleTemplatesWithPermission 从 user.ParentUserID 解析
owner 传入, 与 chatbot ListPublishedByOwner 模式对齐 (spec D1)."
```

---

# Phase 3：HasFeaturePermission 跨层重构

## Task 3: store 拆纯查询 + biz dispatch + middleware redirect

**Files:**
- Modify: `numind-server/internal/numind/biz/biz.go`（新增包级 `var B IBiz` 全局变量 + NewBiz 时赋值）
- Modify: `numind-server/internal/numind/numind.go`（wire 时确保 store.NewStore → biz.NewBiz 顺序正确）
- Modify: `numind-server/internal/numind/store/customer.go`（删 HasFeaturePermission，加 CheckSubUserFeatureGrant）
- Modify: `numind-server/internal/numind/biz/customer/customer.go`（CheckFeaturePermission 改 dispatch + 加 hasSalesAgentPermission helper）
- Modify: `numind-server/internal/numind/biz/monitor/monitor.go`（line 146 caller 迁移：`mb.store.Customers().HasFeaturePermission(...)` → `biz.B.Customers().CheckFeaturePermission(...)`）
- Modify: `numind-server/internal/pkg/middleware/middleware.go`（FeaturePermission 改调 biz.B）
- Modify: `numind-server/internal/numind/store/customer_test.go`（迁移到 CheckSubUserFeatureGrant）
- Modify: `numind-server/internal/numind/store/customer_permission_lifecycle_test.go`（11 处 `HasFeaturePermission` 调用迁移到 `CheckSubUserFeatureGrant`，或调 biz.CheckFeaturePermission 视测试语义）
- Modify: `numind-server/internal/numind/biz/customer/customer_test.go`（加 9 个矩阵测试）

**Spec 引用**: §3.2, §3.3, §5.2 (AC10-AC12), §5.4 (B1-B3, B8), §6.2

**关键不变量**：本 task 必须一次性 atomic commit。中间态会让 middleware 调不存在的方法。

**Step 0 (biz.B singleton wiring) 必须先于其他 step 完成**，否则 Step 3 middleware redirect 无目标可调。

- [ ] **Step 0: 新增 `biz.B` 全局单例 wiring（D2 前置基础设施）**

`internal/numind/biz/biz.go` 加：

```go
// B 是 biz 层全局单例,镜像 store.S 模式. middleware/cron 等 wire 不便注入的代码路径
// 可通过此变量调 biz 函数. 在 NewBiz 时初始化, 不应被外部直接重置.
var B IBiz

// NewBiz 已有签名不变，函数体末尾追加：
//   B = newBizInstance
// 确保 store.S 已 init 后再调 biz.NewBiz.
```

`internal/numind/numind.go` 验证 wire 顺序：`store.NewStore(...)` 先于 `biz.NewBiz(...)` 调用（如本就如此，本 step 仅做断言性 grep 确认）。

grep `biz\.B\b` 全仓库验证当前未占用此名字，避免冲突。

- [ ] **Step 1: 删除 store.HasFeaturePermission 加新方法**

`internal/numind/store/customer.go:279-304` 整段替换：

```go
// CheckSubUserFeatureGrant 检查子用户是否被授权指定 feature.
// 纯查询: 不读 user 表, 不做 dispatch. 调用方 (biz 层) 负责父账户判断和分流.
// spec D2: dispatch 逻辑上移至 biz 层, store 层只剩原子查询.
func (c *customerStore) CheckSubUserFeatureGrant(ctx context.Context, subUserID uint, featureKey string) (bool, error) {
    var count int64
    err := c.db.WithContext(ctx).Model(&model.UserFeaturePermission{}).
        Where("sub_user_id = ? AND feature_key = ?", subUserID, featureKey).
        Count(&count).Error
    if err != nil {
        return false, err
    }
    return count > 0, nil
}
```

同步更新 `ICustomerStore` interface 定义（同文件 line 36 附近）：
- 删 `HasFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error)`
- 加 `CheckSubUserFeatureGrant(ctx context.Context, subUserID uint, featureKey string) (bool, error)`

- [ ] **Step 2: 改造 biz.CheckFeaturePermission**

`internal/numind/biz/customer/customer.go:320-325` 附近替换：

```go
// CheckFeaturePermission 检查用户是否有 feature 权限。
// Dispatch by featureKey (spec D2):
//   - "sales_agent": 走 hasSalesAgentPermission 双层 AND, 无父账户硬 bypass
//   - 其他 (content_monitor / self_service_config / 未来): 父账户 bypass + 子用户 grant 查询
func (c *customerBiz) CheckFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error) {
    var user model.User
    if err := c.ds.DB().WithContext(ctx).First(&user, userID).Error; err != nil {
        return false, fmt.Errorf("CheckFeaturePermission: lookup user: %w", err)
    }

    if featureKey == model.FeatureKeySalesAgent {
        return c.hasSalesAgentPermission(ctx, &user)
    }

    // 其他 feature_key 保留父账户硬 bypass (本需求不动)
    if user.ParentUserID == nil {
        return true, nil
    }
    return c.ds.Customers().CheckSubUserFeatureGrant(ctx, user.ID, featureKey)
}

// hasSalesAgentPermission 销售智能体双层 AND 检查 (spec §3.2):
//   Layer 0: 用户所属父账户必须在 sales_agent_owner 表中
//   Layer 1: 子用户必须额外在 user_feature_permission 表中有 sales_agent 行
//   父账户用户: 仅需 Layer 0
func (c *customerBiz) hasSalesAgentPermission(ctx context.Context, user *model.User) (bool, error) {
    parentID := user.ID
    if user.ParentUserID != nil {
        parentID = *user.ParentUserID
    }

    // Layer 0
    ownerExists, err := c.ds.SalesAgentOwners().Exists(ctx, parentID)
    if err != nil {
        return false, fmt.Errorf("hasSalesAgentPermission: owner check: %w", err)
    }
    if !ownerExists {
        return false, nil
    }

    // 父账户: Layer 0 已足够
    if user.ParentUserID == nil {
        return true, nil
    }

    // 子账户: Layer 1 必查
    return c.ds.Customers().CheckSubUserFeatureGrant(ctx, user.ID, model.FeatureKeySalesAgent)
}
```

确保 import 包含 `fmt` 和 model 包。

- [ ] **Step 3: 改 middleware.FeaturePermission 调 biz**

`internal/pkg/middleware/middleware.go:222` 附近：

修改前：
```go
hasPermission, err := store.S.Customers().HasFeaturePermission(c, user.ID, featureKey)
```

修改后：
```go
hasPermission, err := biz.B.Customers().CheckFeaturePermission(c, user.ID, featureKey)
```

`biz.B` 已在 Step 0 创建。如 Step 0 漏做导致 `biz.B == nil` 运行时 panic，回到 Step 0 修。

**同 step 同步迁移其他 caller**（搜过完整 caller 清单）：

- `internal/numind/biz/monitor/monitor.go:146` 改：
  ```go
  // 原: has, err := mb.store.Customers().HasFeaturePermission(ctx, userID, model.FeatureKeyContentMonitor)
  // 新: has, err := biz.B.Customers().CheckFeaturePermission(ctx, userID, model.FeatureKeyContentMonitor)
  ```
  注意：原代码用 `mb.store.Customers()` 直调 store 跳过 biz 也是 layer violation，本次顺手统一修。

- grep 兜底验证：
  ```bash
  grep -rn "HasFeaturePermission" internal/ --include='*.go'
  ```
  期望命中仅在被删除的接口定义处 + 已迁移的测试文件。任何 production 代码命中 = 漏改。

- [ ] **Step 4: 迁移 store 测试（两个测试文件，~14 处调用）**

迁移两个文件中所有的 `HasFeaturePermission` 调用：

1. `internal/numind/store/customer_test.go` — 历史测试（~3 处）
2. `internal/numind/store/customer_permission_lifecycle_test.go` — **11 处 `HasFeaturePermission` 调用必须全部迁移**（grep `cs.HasFeaturePermission\|store.Customers().HasFeaturePermission` 确认 11 处都改）

每处的语义判断：
- 如果测试在断言"父账户 bypass"行为 → 该测试转换为 biz 层测试（在 customer biz_test 写）；或者重写为构造 user 后调 `biz.CheckFeaturePermission`
- 如果测试在断言"子账户 grant 表行存在"行为 → 直接改为 `CheckSubUserFeatureGrant(subUserID, featureKey)` 等价签名（注意：不再传 userID + 自动读 user 表，调用方必须先知道是子账户 ID）

新增的核心 store 测试（在 customer_test.go 末尾）：
2. 现存测试改为：

```go
func TestCheckSubUserFeatureGrant_True(t *testing.T) {
    s, db := setupCustomerStoreTest(t)
    ctx := context.Background()

    db.Create(&model.UserFeaturePermission{
        ParentUserID: 30, SubUserID: 100, FeatureKey: "sales_agent",
    })

    has, err := s.CheckSubUserFeatureGrant(ctx, 100, "sales_agent")
    require.NoError(t, err)
    require.True(t, has)
}

func TestCheckSubUserFeatureGrant_False(t *testing.T) {
    s, _ := setupCustomerStoreTest(t)
    has, err := s.CheckSubUserFeatureGrant(context.Background(), 100, "sales_agent")
    require.NoError(t, err)
    require.False(t, has)
}

func TestCheckSubUserFeatureGrant_FeatureKeyDoesNotMix(t *testing.T) {
    s, db := setupCustomerStoreTest(t)
    ctx := context.Background()

    db.Create(&model.UserFeaturePermission{
        ParentUserID: 30, SubUserID: 100, FeatureKey: "content_monitor",
    })

    has, err := s.CheckSubUserFeatureGrant(ctx, 100, "sales_agent")
    require.NoError(t, err)
    require.False(t, has, "feature_key 必须精确匹配, 不串味")
}
```

- [ ] **Step 5: 加 9 个 biz 矩阵测试**

`internal/numind/biz/customer/customer_test.go`：

测试 setup helper（如不存在）：

```go
func setupCheckFeaturePermissionTest(t *testing.T) (Customers, *gorm.DB) {
    tmp := t.TempDir()
    db, err := gorm.Open(sqlite.Open(tmp+"/biz_feature_test.db?_busy_timeout=5000"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.AutoMigrate(
        &model.User{},
        &model.UserFeaturePermission{},
        &model.SalesAgentOwner{},
    ))
    ds := newTestDataStore(db)  // 沿用既有模式或新建 helper
    return NewCustomerBiz(ds), db
}
```

9 个矩阵测试（spec §6.2）：

```go
// === sales_agent 双层 AND ===

func TestCheckFeaturePermission_SalesAgent_ParentOwnerExists(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    // 注: model.User 内嵌 gorm.Model，AutoIncrement ID 会覆盖手设 ID，用 raw INSERT 固定 ID:
db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (30, NULL, datetime('now'), datetime('now'))")  // parent
    db.Create(&model.SalesAgentOwner{ParentUserID: 30})

    has, err := biz.CheckFeaturePermission(context.Background(), 30, "sales_agent")
    require.NoError(t, err)
    require.True(t, has)
}

func TestCheckFeaturePermission_SalesAgent_ParentOwnerAbsent(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (1, NULL, datetime('now'), datetime('now'))")  // admin parent
    // NOT inserted into sales_agent_owner

    has, err := biz.CheckFeaturePermission(context.Background(), 1, "sales_agent")
    require.NoError(t, err)
    require.False(t, has, "关键回归: admin 不在 owner 表 → false")
}

func TestCheckFeaturePermission_SalesAgent_SubUserBothLayers(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    // 注: model.User 内嵌 gorm.Model，AutoIncrement ID 会覆盖手设 ID，用 raw INSERT 固定 ID:
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (30, NULL, datetime('now'), datetime('now'))")
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (100, 30, datetime('now'), datetime('now'))")
    db.Create(&model.SalesAgentOwner{ParentUserID: 30})
    db.Create(&model.UserFeaturePermission{ParentUserID: 30, SubUserID: 100, FeatureKey: "sales_agent"})

    has, err := biz.CheckFeaturePermission(context.Background(), 100, "sales_agent")
    require.NoError(t, err)
    require.True(t, has)
}

func TestCheckFeaturePermission_SalesAgent_SubUserLayer1Only(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    // 注: model.User 内嵌 gorm.Model，AutoIncrement ID 会覆盖手设 ID，用 raw INSERT 固定 ID:
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (30, NULL, datetime('now'), datetime('now'))")
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (100, 30, datetime('now'), datetime('now'))")
    // NOT inserted into sales_agent_owner
    db.Create(&model.UserFeaturePermission{ParentUserID: 30, SubUserID: 100, FeatureKey: "sales_agent"})

    has, err := biz.CheckFeaturePermission(context.Background(), 100, "sales_agent")
    require.NoError(t, err)
    require.False(t, has, "Layer 0 拦截: parent 不在 owner 表 → 即使 Layer 1 通过也 deny")
}

func TestCheckFeaturePermission_SalesAgent_SubUserLayer0Only(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    // 注: model.User 内嵌 gorm.Model，AutoIncrement ID 会覆盖手设 ID，用 raw INSERT 固定 ID:
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (30, NULL, datetime('now'), datetime('now'))")
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (100, 30, datetime('now'), datetime('now'))")
    db.Create(&model.SalesAgentOwner{ParentUserID: 30})
    // NOT inserted into user_feature_permission

    has, err := biz.CheckFeaturePermission(context.Background(), 100, "sales_agent")
    require.NoError(t, err)
    require.False(t, has, "Layer 1 拦截: 子用户无 grant → deny")
}

// === content_monitor / self_service_config 回归 ===

func TestCheckFeaturePermission_ContentMonitor_ParentBypass(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (1, NULL, datetime('now'), datetime('now'))")  // admin parent

    has, err := biz.CheckFeaturePermission(context.Background(), 1, "content_monitor")
    require.NoError(t, err)
    require.True(t, has, "回归保证: content_monitor 父账户 bypass 保留")
}

func TestCheckFeaturePermission_SelfServiceConfig_ParentBypass(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (1, NULL, datetime('now'), datetime('now'))")

    has, err := biz.CheckFeaturePermission(context.Background(), 1, "self_service_config")
    require.NoError(t, err)
    require.True(t, has, "回归保证: self_service_config 父账户 bypass 保留")
}

func TestCheckFeaturePermission_ContentMonitor_SubUserDeny(t *testing.T) {
    biz, db := setupCheckFeaturePermissionTest(t)
    // 注: model.User 内嵌 gorm.Model，AutoIncrement ID 会覆盖手设 ID，用 raw INSERT 固定 ID:
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (30, NULL, datetime('now'), datetime('now'))")
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (100, 30, datetime('now'), datetime('now'))")

    has, err := biz.CheckFeaturePermission(context.Background(), 100, "content_monitor")
    require.NoError(t, err)
    require.False(t, has, "子账户 + content_monitor + 无 grant → deny (现状)")
}

// === 错误路径 ===

func TestCheckFeaturePermission_UserNotFound(t *testing.T) {
    biz, _ := setupCheckFeaturePermissionTest(t)

    has, err := biz.CheckFeaturePermission(context.Background(), 999, "sales_agent")
    require.Error(t, err, "user 不存在应返回 err (gorm.ErrRecordNotFound chain)")
    require.False(t, has)
}
```

- [ ] **Step 6: 跑 lint + 全部测试**

```bash
cd numind-server
go build ./...  # 必须 0 错误
go test ./internal/numind/store/ -run "CheckSubUserFeatureGrant" -v
go test ./internal/numind/biz/customer/ -run "CheckFeaturePermission" -v
task lint
```

期望：全部 PASS，编译 0 错误。

- [ ] **Step 7: Commit**

```bash
git add internal/numind/store/customer.go internal/numind/biz/customer/customer.go \
        internal/pkg/middleware/middleware.go \
        internal/numind/store/customer_test.go \
        internal/numind/biz/customer/customer_test.go
git commit -m "refactor(parent-scope): dispatch HasFeaturePermission to biz layer

- store: 拆 HasFeaturePermission → CheckSubUserFeatureGrant 纯查询
- biz: CheckFeaturePermission dispatch by featureKey
  - sales_agent: 双层 AND (Layer 0 sales_agent_owner + Layer 1 user_feature_permission)
  - 其他 feature: 保留父账户硬 bypass (content_monitor / self_service_config)
- middleware: 改调 biz 修复既有 layer violation

spec D2."
```

---

# Phase 4：SOP 写入路径修复

## Task 4: SopBiz.CreateTemplateByUser 父账户 invariant 改造

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/sop.go`（CreateTemplateByUser 函数体）
- Modify: `numind-server/internal/numind/biz/sop/sop_test.go`（加 2 测试）

**Spec 引用**: §3.7, §5.2 (AC13), §5.3 (AC10, AC13)

- [ ] **Step 1: 修改 CreateTemplateByUser**

`internal/numind/biz/sop/sop.go:2210` 附近（grep 实际位置）：

修改前关键行：
```go
template := &model.SopTemplate{
    Name: req.Name, Description: req.Description,  // 注: 实际 CreateTemplateByUserReq 没有 Prompt 字段
    CreatorUserID: &userID,  // ❌ 可能存子用户 id (sub_user.ID 而非 parent.ID)
    ...
}
```

修改后：
```go
func (b *sopBiz) CreateTemplateByUser(ctx context.Context, userID uint, req *CreateTemplateByUserReq) (*model.SopTemplate, error) {
    // 读取调用者以获取 parent_user_id (spec D1)
    var actor model.User
    if err := b.ds.DB().WithContext(ctx).First(&actor, userID).Error; err != nil {
        return nil, fmt.Errorf("CreateTemplateByUser: lookup user: %w", err)
    }

    // 防御性 assertion (spec D8): 路由层 ParentUserOnly 中间件已保证 actor 是父账户,
    // biz 层再次断言, 即使将来 middleware 配错也不会 silent 让子用户写入.
    if actor.ParentUserID != nil {
        return nil, errno.ErrForbidden.SetMessage("仅父账户可创建 SOP 模板")
    }

    // 未显式传入时默认开启，保持与历史行为一致（原有逻辑保留不动）
    trailingChat := true
    if req.TrailingChatEnabled != nil {
        trailingChat = *req.TrailingChatEnabled
    }

    ownerID := actor.ID  // 已 assert 是父账户

    template := &model.SopTemplate{
        Name:                req.Name,
        Description:         req.Description,
        CreatorUserID:       &ownerID,  // ✅ 始终是父账户 id (本次唯一改动行: 原为 &userID)
        PublishStatus:       model.SopPublishStatusDraft,
        Status:              "active",
        TrailingChatEnabled: trailingChat,
    }

    if err := b.ds.Sop().CreateTemplate(template); err != nil {
        return nil, fmt.Errorf("CreateTemplateByUser: %w", err)
    }

    // GORM default:true bool fixup (database.md §6) — 保留不动
    if !trailingChat && template.TrailingChatEnabled {
        if err := b.ds.DB().WithContext(ctx).Model(template).UpdateColumn("trailing_chat_enabled", false).Error; err != nil {
            return nil, fmt.Errorf("CreateTemplateByUser: fixup trailing_chat_enabled: %w", err)
        }
        template.TrailingChatEnabled = false
    }

    log.C(ctx).Infow("B-end user created SOP template", "user_id", userID, "template_id", template.ID, "name", req.Name)
    return template, nil
}
```

**关键约束**：
- `CreateTemplateByUserReq` 当前**没有** `Prompt` 字段（只有 Name / Description / TrailingChatEnabled），spec/plan 写法保持一致，不引入新字段。
- 本 task **唯一新增**: function 头部读 actor + assert + 把 `CreatorUserID: &userID` 改为 `CreatorUserID: &ownerID`（在已 assert 父账户的前提下两者同值，但显式声明 invariant）。
- 现有 `TrailingChatEnabled` handling + GORM `default:true` fixup（参见 `.claude/rules/database.md §6`）+ log 调用**完全保留**。

⚠️ 确认 `errno.ErrForbidden` 存在（middleware.go:231 已用过 `errno.ErrForbidden.SetMessage`，已确认存在）。

- [ ] **Step 2: 加单测**

`internal/numind/biz/sop/sop_test.go` 末尾：

```go
// setupSopBizTest helper（若不存在则新建，沿用 customer_test.go:21-30 sqlite 模式）：
// func setupSopBizTest(t *testing.T) (sop.ISopBiz, *gorm.DB) {
//     tmp := t.TempDir()
//     db, _ := gorm.Open(sqlite.Open(tmp+"/biz_sop_test.db?_busy_timeout=5000"), &gorm.Config{})
//     db.AutoMigrate(&model.User{}, &model.SopTemplate{})
//     ds := store.NewDataStoreForTest(db)  // 若已有此 helper; 否则手工构造
//     return sop.NewSopBiz(ds, nil, nil), db
// }

func TestCreateTemplateByUser_ParentSucceeds(t *testing.T) {
    biz, db := setupSopBizTest(t)
    ctx := context.Background()

    // 注: gorm.Model embeds AutoIncrement ID, 用 raw INSERT 固定 ID
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (30, NULL, datetime('now'), datetime('now'))")

    req := &sop.CreateTemplateByUserReq{Name: "test", Description: "desc"}  // 注: 无 Prompt 字段
    tpl, err := biz.CreateTemplateByUser(ctx, 30, req)
    require.NoError(t, err)
    require.NotNil(t, tpl)
    require.NotNil(t, tpl.CreatorUserID)
    require.Equal(t, uint(30), *tpl.CreatorUserID, "creator_user_id 必须 = parent.ID")
}

func TestCreateTemplateByUser_SubUserRejected(t *testing.T) {
    biz, db := setupSopBizTest(t)
    ctx := context.Background()

    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (30, NULL, datetime('now'), datetime('now'))")
    db.Exec("INSERT INTO user (id, parent_user_id, created_at, updated_at) VALUES (100, 30, datetime('now'), datetime('now'))")

    req := &sop.CreateTemplateByUserReq{Name: "test", Description: "desc"}
    _, err := biz.CreateTemplateByUser(ctx, 100, req)
    require.Error(t, err, "子用户调用必须被拒")
    // 可选: assert errno.ErrForbidden chain
}
```

- [ ] **Step 3: 跑测试 + lint**

```bash
cd numind-server
go test ./internal/numind/biz/sop/ -run "TestCreateTemplateByUser" -v
go build ./...
task lint
```

- [ ] **Step 4: Commit**

```bash
git add internal/numind/biz/sop/sop.go internal/numind/biz/sop/sop_test.go
git commit -m "feat(parent-scope): assert parent + resolve owner in CreateTemplateByUser

CreateTemplateByUser 入口读 user 并 assert ParentUserID==nil (spec D8 defense-
in-depth). CreatorUserID 始终存父账户 id, 不再存子用户 id (spec D1).
2 个单测覆盖父账户成功路径 + 子账户被拒路径."
```

---

## Task 5: SopBiz.CreateTemplate (admin) + admin controller 改造

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/sop.go`（CreateTemplate 函数签名 + 实现，line 142 附近）
- Modify: `numind-server/internal/numind/controller/v1/admin_sop/sop.go`（CreateTemplate 调用，line 73）
- Modify: `numind-server/internal/numind/biz/sop/sop_test.go`（加 2 测试）

**Spec 引用**: §3.6, §5.2 (AC14), §5.3 (AC14)

- [ ] **Step 1: 修改 CreateTemplate 签名 + 实现**

`internal/numind/biz/sop/sop.go:30 + 142` 附近：

接口（line 30 附近）：
```go
// CreateTemplate (admin 路径) — 必须显式传入 adminUserID 作为 owner.
// admin 创建的 SOP 归属 admin 自己, 不会跨租户泄露给其他父账户.
CreateTemplate(ctx context.Context, adminUserID uint, name, description, prompt string) (*model.SopTemplate, error)
```

实现（**关键约束：保留所有现有逻辑** —— Status / PublishStatus 默认值 + GrantTemplateToConfiguredSubUsers 自动授权）：

```go
func (b *sopBiz) CreateTemplate(ctx context.Context, adminUserID uint, name, description, prompt string) (*model.SopTemplate, error) {
    if adminUserID == 0 {
        return nil, fmt.Errorf("CreateTemplate: adminUserID required (spec D6)")
    }

    template := &model.SopTemplate{
        Name:          name,
        Description:   description,
        Prompt:        prompt,
        Status:        model.SopNodeStatusActive,                  // 保留原有逻辑
        PublishStatus: model.SopPublishStatusPublished,             // 保留原有: admin 创建默认发布
        CreatorUserID: &adminUserID,                                // 新增此行 (spec D6)
    }
    if err := b.ds.Sop().CreateTemplate(template); err != nil {
        return nil, fmt.Errorf("failed to create template: %w", err)
    }

    // 保留原有逻辑: 自动授权给所有已配置权限的子用户
    if err := b.ds.Customers().GrantTemplateToConfiguredSubUsers(ctx, template.ID); err != nil {
        log.C(ctx).Warnw("Failed to auto-grant new template to sub-users", "template_id", template.ID, "err", err)
    }

    return template, nil
}
```

**本 task 唯一新增**:
1. signature 加 `adminUserID uint` 参数
2. function 头部 `if adminUserID == 0 { return error }`
3. template struct 加 `CreatorUserID: &adminUserID,` 一行

**保留**: Status / PublishStatus 默认值 + GrantTemplateToConfiguredSubUsers 自动授权 + log + error wrapping。

- [ ] **Step 2: 更新 admin controller 调用**

`internal/numind/controller/v1/admin_sop/sop.go:64-80` 附近：

```go
func (ctrl *SopController) CreateTemplate(c *gin.Context) {
    log.C(c).Infow("Create SOP template called")

    // 从 admin token middleware 设置的 context 中取 admin user.id (spec D6)
    // middleware.GetCurrentUser(c) 实际签名是单返回值 *model.User (中间件源码 line 203-210)
    adminUser := middleware.GetCurrentUser(c)
    if adminUser == nil {
        core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("admin context missing"), nil)
        return
    }

    var req v1.CreateSopTemplateRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
        return
    }

    template, err := ctrl.sopBiz.CreateTemplate(c, adminUser.ID, req.Name, req.Description, req.Prompt)
    if err != nil {
        core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
        return
    }

    core.WriteResponse(c, nil, template)
}
```

⚠️ 确认 `c.Get("current_user")` 是 admin middleware 实际填入的 key（grep `middleware.go:112` 的 `c.Set("current_user", user)` 已确认）。

- [ ] **Step 3: 加单测**

`internal/numind/biz/sop/sop_test.go`：

```go
func TestCreateTemplate_AdminSucceeds(t *testing.T) {
    biz, _ := setupSopBizTest(t)  // 沿用 Task 4 的 helper
    ctx := context.Background()

    adminID := uint(1)
    tpl, err := biz.CreateTemplate(ctx, adminID, "demo", "demo desc", "p")
    require.NoError(t, err)
    require.NotNil(t, tpl.CreatorUserID)
    require.Equal(t, adminID, *tpl.CreatorUserID)
}

func TestCreateTemplate_RequiresAdminUserID(t *testing.T) {
    biz, _ := setupSopBizTest(t)
    ctx := context.Background()

    _, err := biz.CreateTemplate(ctx, 0, "demo", "demo desc", "p")
    require.Error(t, err, "adminUserID=0 必须报错")
}
```

⚠️ 这两个测试假设 `b.ds.Customers().GrantTemplateToConfiguredSubUsers(ctx, ...)` 在测试 fake datastore 中要么 mock 要么 graceful no-op（参考 biz/sop/sop_test.go 现有测试看 fake/mock 模式）。

- [ ] **Step 4: 跑测试 + lint + 全仓库 build 检查**

```bash
cd numind-server
go test ./internal/numind/biz/sop/ -run "TestCreateTemplate_" -v
go build ./...  # 必须 0 错误 (admin controller 调用方已同步)
task lint
```

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/sop/sop.go internal/numind/biz/sop/sop_test.go \
        internal/numind/controller/v1/admin_sop/sop.go
git commit -m "feat(parent-scope): admin CreateTemplate requires adminUserID

修复 prod 上 admin 创建 SOP 留下 NULL creator_user_id 的结构性 bug
(spec D6, 即本次修复的源头 bug). CreateTemplate signature 加 adminUserID
参数, admin controller 从 c.Get('current_user') 取 admin user.id 传入.
admin SOP 归属 admin 自己 (id=1), 不会跨租户泄露给 user 30."
```

---

# Phase 5：S5 验证策略

## Task 6: S5 验证策略 doc

**Files:**
- Create: `numind-server/docs/superpowers/specs/2026-05-18-sop-salesrag-parent-scope-validation-strategy.md`

**Spec 引用**: §12 (草案已在 spec)

NDF Rule 10 强制：S5 验证策略必须在 S3 plan 中独立 task。S3 gate 的独立 reviewer 一并审查"验证策略合理性"，确保不偷懒。

- [ ] **Step 1: 写验证策略 doc**

文件内容：

```markdown
# S5 验证策略 — sop-salesrag-parent-scope

## 验证方式选择

**选择**: Playwright E2E + Go 单测 + 集成测试 三层组合

**理由**: 本需求是**后端跨层多租户隔离**修复，UI 完全无感（前端零改动）。
但功能涉及**高风险数据可见性**——bug 会导致跨机构数据泄露或本机构业务停摆。

| 候选方式 | 选 / 不选 | 理由 |
|---------|----------|------|
| 仅 Go 单测 | 不够 | 单测 mock store 层，无法验证 SQL 是否真的过滤了跨租户数据 |
| 仅 gstack /qa | 不够 | /qa 是一次性截图验证，不留持久化回归保护；高风险路径需要 commit 化的 spec |
| Playwright E2E | ✓ 选 | 端到端，持久化在 e2e/ 目录作为回归保护 |
| Go 单测 + 集成测试 | ✓ 选 | 单测覆盖矩阵广度，集成测试覆盖 SQL/DB 真实行为 |

## 关键用户路径（5 条，Playwright spec 必覆盖）

每条路径在 `numind-web-v3/e2e/sop-salesrag-parent-scope.spec.ts` 中独立 test。
登录凭据来自 `E2E_USERNAME` / `E2E_PASSWORD` 环境变量。

### 路径 1: user 30 父账户登录 - 修复前后行为完全一致
1. 用 user 30 的凭据登录用户端 (E2E_USERNAME=user_moxiaopai)
2. 导航到 /home
3. **断言**: AI 工作流区显示 3 张卡片（小红书图文 / AI 文稿创作 / AI 朋友圈）
4. **断言**: AI 智能体区显示销售智能体磁贴
5. **断言**: 销售智能体磁贴点击后跳转 /sales

### 路径 2: user 30 子账户登录 - 体验不变
1. 用 user 30 子账户凭据登录（如 sub_user_id=345）
2. 导航到 /home
3. **断言**: 看到 3 SOP（has_permission 状态按子账户授权）
4. **断言**: 销售智能体磁贴可见（因父账户 owner + 子账户在 user_feature_permission 有授权）

### 路径 3: admin 登录 - 空工作区
1. 用 admin 凭据登录用户端
2. 导航到 /home
3. **断言**: AI 工作流区 0 张卡片
4. **断言**: AI 智能体区无销售智能体磁贴
5. **断言**: 调用 GET /v1/sales-rag/check-permission 返回 has_permission=false

### 路径 4: admin 访问 content_monitor / self_service_config - 行为不变（回归保护）
1. 用 admin 凭据登录
2. **断言**: 访问 /v1/monitor 端点返回 200（父账户 bypass 保留）
3. **断言**: 访问 /v1/config/* 系列端点正常（self_service_config bypass 保留）

### 路径 5: 模拟新机构父账户隔离（用临时测试账户）
1. 创建临时父账户 test_parent_X（migration 期间脚本，迁移完毕清理）
2. 登录 test_parent_X
3. **断言**: AI 工作流区 0 卡片
4. **断言**: 销售智能体磁贴不可见
5. **断言**: 新机构想用销售智能体，需手工 INSERT 到 sales_agent_owner

## Go 单测覆盖矩阵（参考 spec §6.1, §6.2）

- store/sop_test.go: 3 个新测试（owner 过滤 / IS NOT NULL 防御 / 不存在 parent）
- store/sales_agent_owner_test.go: 4 个测试（Exists 真假 + 不同 parent + DB 错误）
- store/customer_test.go: 3 个 CheckSubUserFeatureGrant 测试（真 / 假 / feature_key 不串味）
- biz/customer_test.go: 9 个 CheckFeaturePermission 矩阵测试（spec §6.2）
- biz/sop_test.go: 4 个 SOP biz 测试（CreateTemplateByUser × 2 + CreateTemplate × 2）

## 集成测试（migration 真实跑通）

文件 `migrations/audit/test_migration_20260518.go`（参考 visibility-scope 同款）：

- TestMigration_Idempotent: 跑两次迁移 → 第二次零错误，行数不变
- TestMigration_AfterRunUser30Visibility: 跑完后 user 30 list 行数 = 3
- TestMigration_AfterRunAdminVisibility: 跑完后 admin list 行数 = 0
- TestMigration_AfterRunSalesAgentPermission: user 30 → has_permission=true; admin → false

## 回归保护承诺

**Playwright E2E 持久化**：5 条路径的 spec 提交到 git，未来任何 SOP/chatbot/sales_agent
权限相关改动都会触发完整跑。**不是 gstack /qa 一次性验证**。

**Go 单测自动 race detection**：S4 编码完毕用 `task test`（含 -race）跑完整测试套件。

## 不在范围内

- 性能/压测：本需求不涉及性能敏感路径
- Langfuse trace 验证：本需求 0 LLM 调用
- 视觉/像素 diff：前端零改动
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-05-18-sop-salesrag-parent-scope-validation-strategy.md
git commit -m "docs(parent-scope): S5 validation strategy

5 关键用户路径 + Go 单测矩阵 + 集成测试方案 + 回归保护承诺.
NDF Rule 10 强制 S3 plan 必含独立 task."
```

---

## 任务依赖图

```
Task 1 (foundation) → Task 2 (sop list owner filter) → Task 4 (CreateTemplateByUser)
                  ↘                                  ↗
                    Task 3 (HasFeaturePermission refactor)
                                                     ↘
                                                       Task 5 (admin CreateTemplate)
                                                                       ↓
                                                                  Task 6 (validation strategy doc)
```

- Task 1 是 hard dependency（其他所有都依赖 SalesAgentOwner model + store）
- Task 2, 3, 4, 5 可在 Task 1 完成后顺序执行（不应并行：subagent-driven-development 顺序执行）
- Task 6 是 doc-only，可在任何时间但建议放最后（避免反复修改路径列表）

---

## 验收 gate（NDF S3 → S4 transition）

进 S4 前确认：

- [ ] 每个 task 有编号 / 标题 / 描述 / 涉及文件 / 验收条件 ✓
- [ ] task 原子性：每个 task 完成并 commit 后 `go build ./...` 0 错误 + `task lint` 通过
- [ ] task 依赖无环 ✓ （线性链 1 → 2 → 3 → 4 → 5 → 6）
- [ ] spec 全部需求覆盖：
  - §1.2 G1 SOP list 过滤 → Task 2 ✓
  - §1.2 G2 creator 语义升级 → Task 4 + Task 5 ✓
  - §1.2 G3 销售智能体 owner tag → Task 1 + Task 3 ✓
  - §1.3 5 个不变量 → 全部 task 集体覆盖 + Task 6 验证策略 ✓
  - §9 9 个决策 D1-D9 → 全部对应 task ✓
- [ ] S5 验证策略已纳入 plan 作为独立 task → Task 6 ✓
- [ ] AI 功能条件 → N/A (0 LLM 调用)
- [ ] 多仓库条件 → N/A (单仓 numind-server)
- [ ] 独立 reviewer subagent 审查 plan 原子性 → 待 S3 gate 执行

---

## 估时

| Task | 估计时间 |
|------|---------|
| Task 1 (foundation) | 1.5 h |
| Task 2 (sop list) | 1.5 h |
| Task 3 (HasFeaturePermission refactor) | 2.5 h |
| Task 4 (CreateTemplateByUser) | 1 h |
| Task 5 (admin CreateTemplate) | 1 h |
| Task 6 (validation strategy doc) | 0.5 h |
| **合计** | **~8 h ≈ 1 工作日** |

与 S1 proposal §2 估的 1 天对齐 ✓
