# SOP / 销售智能体 父账户归属隔离 — 技术设计

**Spec date**: 2026-05-18
**Feature ID**: sop-salesrag-parent-scope
**Track**: Standard
**Status**: DRAFT（已通过 S2 gate）

## §1 设计概览

### 1.1 目标

补齐 numind-server 多租户隔离体系的 **Layer 0（父账户归属）**。在已有的 Layer 1（子用户可见性 visibility_restricted）+ Layer 2（子用户运行权限 user_template_permission）之上叠加一层最外的"父账户归属"过滤，让"哪些 SOP/销售智能体属于本租户"成为 SQL 层确定性事实，而非依赖巧合（"其他父账户没发布过 SOP"）。

### 1.2 修复的 3 个结构性缺口

| 缺口 | 现状 | 修复 |
|------|------|------|
| **G1**: SOP 列表 SQL 缺 owner 过滤 | `store/sop.go:142 ListVisibleTemplates` 仅 `WHERE status='active' AND publish_status='published'` | 加 `WHERE creator_user_id = ?` 单值过滤，参数 = 当前用户所属父账户 id |
| **G2**: `creator_user_id` 字段语义对齐不一致 | `CreateTemplateByUser` 写入 actor.ID（可能是子用户）；`CreateTemplate`（admin 路径）写入 NULL | 统一改为"始终存父账户 id"——user 路径 biz 层 assert ParentUserID==nil + 写 actor.ID；admin 路径 signature 加 adminUserID + 写 adminUserID |
| **G3**: 销售智能体无 owner 字段 | `customer.HasFeaturePermission` 在 `parent_user_id IS NULL` 时硬 return true，所有父账户都看到磁贴 | 新建 `sales_agent_owner(parent_user_id)` 极简表；`HasFeaturePermission` 在 `featureKey=='sales_agent'` 分支改查该表（移除该分支父账户硬 bypass）。其他 feature_key 保留 bypass 不动 |

### 1.3 关键不变量（修复后必须保持）

1. **租户归属不变量**：每条 `sop_template` 行 `creator_user_id` 不为 NULL（除非历史脏数据），且其值始终是某个 `parent_user_id IS NULL` 的父账户 id。**修复后新写入路径**（admin/user 两条）永不产生 NULL 或子账户 id。
2. **可见性串行不变量**：Layer 0（本需求新加）→ Layer 1（visibility_restricted）→ Layer 2（user_template_permission）三层 gate 顺序固定。被 Layer 0 拦截的 SOP，Layer 1/2 的状态对其无意义。
3. **销售智能体可见性不变量**：磁贴对当前用户可见 ⟺ 当前用户所属父账户在 `sales_agent_owner` 表中 ∧（父账户自己 ∨ 子账户在 `user_feature_permission` 有 sales_agent 行）。
4. **content_monitor / self_service_config 不变量**：这两个 feature_key 的访问语义完全保留（父账户 bypass）。本次修复**仅在 sales_agent 分支**改 `HasFeaturePermission` 行为。
5. **user 30 + 全部子账户体验不变量**：修复前后所看到的 SOP 集合、chatbot 集合、销售智能体磁贴显示状态完全一致。

---

## §2 数据模型

### 2.1 新建表 `sales_agent_owner`

#### Go model

```go
// internal/pkg/model/sales_agent_owner.go
package model

import "time"

// SalesAgentOwner 销售智能体父账户归属表
//
// 每行表示一个父账户拥有"销售智能体卡片"的访问权。该表是销售智能体的
// owner tag 存储——与 chatbot_config.user_id 对销售智能体的等价概念。
//
// 极简设计（D3）：不启用 GORM soft-delete、无 updated_at。
// 写入仅在 migration 或手工 SQL（无 admin UI）；理论上的撤销走 hard delete。
// FK 到 user(id) ON DELETE CASCADE 保证父账户被删时无残留。
type SalesAgentOwner struct {
    ParentUserID uint      `gorm:"primaryKey;type:int unsigned" json:"parent_user_id"`
    CreatedAt    time.Time `gorm:"type:datetime(3)" json:"created_at"`
}

func (SalesAgentOwner) TableName() string {
    return "sales_agent_owner"
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|------|------|------|
| `parent_user_id` | INT UNSIGNED, PK | 拥有销售智能体的父账户 user.id。**INT** 而非 BIGINT，与 user.id 类型对齐（避免 JOIN 时索引退化） |
| `created_at` | DATETIME(3) | 行创建时间，用于审计追溯何时被授予 |

**故意省略**：
- `updated_at`：本表是 write-once，没有更新场景
- `deleted_at` / `gorm.Model`：业务上无 soft-delete 需求，撤销走 hard delete（DELETE 语义清晰）
- `id` 自增主键：`parent_user_id` 本身就是天然主键

#### DDL

```sql
CREATE TABLE IF NOT EXISTS sales_agent_owner (
  parent_user_id INT UNSIGNED NOT NULL PRIMARY KEY,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_sao_parent FOREIGN KEY (parent_user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='销售智能体父账户归属表（owner tag）';
```

### 2.2 `sop_template.creator_user_id` 语义升级

**表结构无变更**——字段 `creator_user_id *uint` 保持 nullable pointer。**仅语义升级**：

| 维度 | 修复前 | 修复后 |
|------|--------|--------|
| 物理类型 | `INT UNSIGNED NULL`（GORM `*uint`） | 同左 |
| 索引 | `idx_st_creator` 单列索引 | 同左 |
| 语义 | "B 端创建者用户 id"——可能是父账户也可能是子账户 | **租户 owner = 父账户 id**——go-forward 不变量 |
| NULL 允许场景 | 平台预置模板（如 prod 现状 id=1, 2） | 兼容历史脏数据；列表 SQL 防御性 `IS NOT NULL` 过滤 |

**Model 注释更新**（必须）：

```go
// CreatorUserID — 租户 owner 父账户 id（多租户归属，spec D1）。
// 2026-05-19 起 go-forward 不变量：新写入始终 = 父账户 user.id，永不为子账户 id。
// 在 biz.CreateTemplate (admin) / biz.CreateTemplateByUser (user) 两个写入路径中保证。
// nullable 为兼容历史脏数据；列表 SQL 防御性 `IS NOT NULL` 过滤（spec D7）。
CreatorUserID *uint `gorm:"index:idx_st_creator" json:"creator_user_id"`
```

---

## §3 模块接口

### 3.1 新增 store: `SalesAgentOwnerStore`

文件：`internal/numind/store/sales_agent_owner.go`（新增）

```go
package store

import (
    "context"

    "gorm.io/gorm"

    "numind-server/internal/pkg/model"
)

// ISalesAgentOwnerStore 销售智能体归属表的数据访问接口
type ISalesAgentOwnerStore interface {
    // Exists 检查指定父账户是否拥有销售智能体。
    // 返回 (true, nil) 表示存在; (false, nil) 表示不存在; (false, err) 表示查询失败。
    // 不存在时不返回 gorm.ErrRecordNotFound——by design 调用方判 bool 即可。
    Exists(ctx context.Context, parentUserID uint) (bool, error)
}

type salesAgentOwnerStore struct {
    db *gorm.DB
}

func NewSalesAgentOwnerStore(db *gorm.DB) ISalesAgentOwnerStore {
    return &salesAgentOwnerStore{db: db}
}

func (s *salesAgentOwnerStore) Exists(ctx context.Context, parentUserID uint) (bool, error) {
    var count int64
    err := s.db.WithContext(ctx).Model(&model.SalesAgentOwner{}).
        Where("parent_user_id = ?", parentUserID).
        Count(&count).Error
    if err != nil {
        return false, err
    }
    return count > 0, nil
}
```

并在 `store.go` 的 `IStore` interface 添加方法 + factory 中初始化。

### 3.2 `customer.CheckFeaturePermission` 改造（biz 层）

**当前**：biz 层 `CheckFeaturePermission` 仅是 store 直通：
```go
// internal/numind/biz/customer/customer.go:324
return c.ds.Customers().HasFeaturePermission(ctx, userID, featureKey)
```

**改造后**：把 dispatch + 双层 AND 逻辑**上移到 biz 层**（修复既有 layer violation——middleware 之前直接调 store 跳过 biz，本次顺手修），store 层只剩纯查询：

```go
// internal/numind/biz/customer/customer.go (改造)
func (c *customerBiz) CheckFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error) {
    var user model.User
    if err := c.ds.DB().WithContext(ctx).First(&user, userID).Error; err != nil {
        return false, fmt.Errorf("CheckFeaturePermission: lookup user: %w", err)
    }

    // sales_agent: owner-tag based, 双层 AND. 无父账户硬 bypass.
    if featureKey == model.FeatureKeySalesAgent {
        return c.hasSalesAgentPermission(ctx, &user)
    }

    // 其他 feature_key (content_monitor / self_service_config / 未来) 保留父账户硬 bypass.
    if user.ParentUserID == nil {
        return true, nil
    }
    return c.ds.Customers().CheckSubUserFeatureGrant(ctx, user.ID, featureKey)
}

// hasSalesAgentPermission 实现销售智能体双层 AND:
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

    // 子账户: Layer 1 子用户授权必查
    return c.ds.Customers().CheckSubUserFeatureGrant(ctx, user.ID, model.FeatureKeySalesAgent)
}
```

**Store 接口改造**：`HasFeaturePermission` 拆分为 `CheckSubUserFeatureGrant`，只做"子用户在 user_feature_permission 表中是否有指定 feature_key 行"这一件事：

```go
// internal/numind/store/customer.go (改造)
// 替换原 HasFeaturePermission 方法
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

原 `HasFeaturePermission` 删除（biz 层已接管 user 表查询和 dispatch）。

**已识别的 caller 全清单**（必须全部迁移到 biz 路径）：
1. `internal/pkg/middleware/middleware.go:222` — middleware FeaturePermission
2. `internal/numind/biz/monitor/monitor.go:146` — monitor biz
3. `internal/numind/store/customer_permission_lifecycle_test.go` — 11 处测试调用（迁移到测试新接口）

### 3.3 `middleware.FeaturePermission` 改造

文件：`internal/pkg/middleware/middleware.go:222`

**当前**（layer violation：middleware 直接调 store）：
```go
hasPermission, err := store.S.Customers().HasFeaturePermission(c, user.ID, featureKey)
```

**改造后**（middleware → biz → store 正确分层）：
```go
hasPermission, err := biz.B.Customers().CheckFeaturePermission(c, user.ID, featureKey)
```

**前置工作**：`biz.B` 全局变量当前不存在。需要在 biz 层引入类似 `store.S` 模式的全局单例，由 `internal/numind/numind.go` 的初始化路径填充。具体实现（在 plan Task 3 内完成）：
- `internal/numind/biz/biz.go` 新增 `var B IBiz` 包级变量
- `NewBiz(ds)` 初始化时同步设置 `B = newBizInstance`
- 顺序：`store.NewStore(...)` → `biz.NewBiz(...)` （biz 初始化 store.S 已就绪后）

注意：parallel session 在 develop 上做的修改可能也涉及 biz 层 wiring；S4 编码时 grep 确认 `biz.B` 名字未被占用。

### 3.4 `SopStore.ListVisibleTemplates` 改造

文件：`internal/numind/store/sop.go:142`

**接口签名变更**：增加 `ctx context.Context` 和 `ownerParentUserID uint` 两个参数（2-axis 改动）。

**当前 signature**: `ListVisibleTemplates(offset, limit int) ([]model.SopTemplate, int64, error)`
**新 signature**: `ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error)`

```go
// 修改后
// ListVisibleTemplates 列出指定父账户租户下 C 端用户可见的模板.
// 多租户隔离: WHERE creator_user_id = ownerParentUserID (Layer 0).
// 防御: 列表永不返回 creator_user_id IS NULL 行 (历史脏数据兜底).
func (s *sopStore) ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error) {
    var templates []model.SopTemplate
    var total int64

    query := s.db.WithContext(ctx).Model(&model.SopTemplate{}).
        Where("creator_user_id = ?", ownerParentUserID).
        Where("creator_user_id IS NOT NULL").  // 防御性
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

**ISopStore interface** 相应改 method signature；所有 caller 通过编译错误强制更新。

### 3.5 `sopBiz.ListVisibleTemplatesWithPermission` 改造

文件：`internal/numind/biz/sop/sop.go:195`

```go
// 修改后
func (b *sopBiz) ListVisibleTemplatesWithPermission(ctx context.Context, user *model.User, offset, limit int) ([]SopTemplateVisibleItem, int64, error) {
    // 解析当前用户所属父账户 id (Layer 0 过滤参数)
    ownerID := user.ID
    if user.ParentUserID != nil {
        ownerID = *user.ParentUserID
    }

    templates, total, err := b.ds.Sop().ListVisibleTemplates(ctx, ownerID, offset, limit)
    if err != nil {
        return nil, 0, fmt.Errorf("ListVisibleTemplatesWithPermission: %w", err)
    }

    // 父账号: Layer 1/2 跳过, 全部 HasPermission=true
    if user.ParentUserID == nil {
        items := make([]SopTemplateVisibleItem, 0, len(templates))
        for _, t := range templates {
            items = append(items, SopTemplateVisibleItem{SopTemplate: t, HasPermission: true})
        }
        return items, total, nil
    }

    // 以下 Layer 1 visibility + Layer 2 run-permission 逻辑保持不变（与现有代码 line 210-247 一致）
}
```

biz.ListVisibleTemplates (无 permission 版本，line 171) 同样添加 ownerParentUserID 参数透传给 store。

### 3.6 `sopBiz.CreateTemplate` (admin 路径) 改造

文件：`internal/numind/biz/sop/sop.go:142`

**仅改 signature + CreatorUserID 一行**，其他逻辑（status / publish_status / GrantTemplateToConfiguredSubUsers 自动授权）**完全保留**：

```go
// CreateTemplate (admin 路径) — 必须显式传入 adminUserID 作为 owner.
// admin (id=1) 创建的 SOP 归属 admin 自己, 不会跨租户泄露给其他父账户.
func (b *sopBiz) CreateTemplate(ctx context.Context, adminUserID uint, name, description, prompt string) (*model.SopTemplate, error) {
    if adminUserID == 0 {
        return nil, fmt.Errorf("CreateTemplate: adminUserID required (spec D6)")
    }
    template := &model.SopTemplate{
        Name:          name,
        Description:   description,
        Prompt:        prompt,
        Status:        model.SopNodeStatusActive,
        PublishStatus: model.SopPublishStatusPublished,
        CreatorUserID: &adminUserID,  // 新增此行 (spec D6)
    }
    if err := b.ds.Sop().CreateTemplate(template); err != nil {
        return nil, fmt.Errorf("failed to create template: %w", err)
    }
    // 现有 GrantTemplateToConfiguredSubUsers 自动授权逻辑保留不动
    if err := b.ds.Customers().GrantTemplateToConfiguredSubUsers(ctx, template.ID); err != nil {
        log.C(ctx).Warnw("Failed to auto-grant new template to sub-users", "template_id", template.ID, "err", err)
    }
    return template, nil
}
```

**对应 controller 改造**：

`internal/numind/controller/v1/admin_sop/sop.go:64-80` 从 admin JWT 取 admin 自身 user.id 传入。Admin middleware 设置 `c.Set("current_user", *model.User)`，controller 读：

```go
// 推荐用 middleware.GetCurrentUser(c) helper 而非自己 type assert
adminUser, _ := middleware.GetCurrentUser(c)
template, err := ctrl.sopBiz.CreateTemplate(c, adminUser.ID, req.Name, req.Description, req.Prompt)
```

### 3.7 `sopBiz.CreateTemplateByUser` (user 路径) 改造

文件：`internal/numind/biz/sop/sop.go:2204`

**仅改 CreatorUserID 赋值 + 前面加 ParentUserOnly assertion**，其他逻辑（TrailingChatEnabled handling + GORM default:true UpdateColumn fixup per database.md §6 + log）**完全保留**：

```go
func (b *sopBiz) CreateTemplateByUser(ctx context.Context, userID uint, req *CreateTemplateByUserReq) (*model.SopTemplate, error) {
    // 读 user 以获取 parent_user_id (spec D1)
    var actor model.User
    if err := b.ds.DB().WithContext(ctx).First(&actor, userID).Error; err != nil {
        return nil, fmt.Errorf("CreateTemplateByUser: lookup user: %w", err)
    }

    // 防御性 assertion (spec D8): 路由层 ParentUserOnly 中间件已保证 actor 是父账户,
    // biz 层再次断言, 即使将来 middleware 配错也不会 silent 让子用户写入.
    if actor.ParentUserID != nil {
        return nil, errno.ErrForbidden.SetMessage("仅父账户可创建 SOP 模板")
    }

    // 未显式传入时默认开启，保持与历史行为一致（原有逻辑保留）
    trailingChat := true
    if req.TrailingChatEnabled != nil {
        trailingChat = *req.TrailingChatEnabled
    }

    ownerID := actor.ID  // 已 assert 是父账户

    template := &model.SopTemplate{
        Name:                req.Name,
        Description:         req.Description,
        CreatorUserID:       &ownerID,  // 改: 从 &userID 改为 &ownerID（同值，但显式声明 parent invariant）
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

**注意**：在 ParentUserOnly middleware 已经把关的前提下，`actor.ParentUserID != nil` assertion 严格说是冗余的（middleware 已经拒绝了子用户）。但显式 assert 让 biz 函数自带不变量声明，未来如果有人把 `CreateTemplateByUser` 接入新路由（忘了套 middleware）时立刻 fail-fast。

**字段语义**：在当前 prod 上，`CreateTemplateByUserReq` 没有 Prompt 字段（只有 Name/Description/TrailingChatEnabled），spec/plan 写法保持一致。

---

## §4 SQL 设计

### 4.1 SOP 列表查询

修改后的实际 SQL（GORM 翻译）：

```sql
SELECT * FROM sop_template
WHERE creator_user_id = ?            -- Layer 0 父账户归属
  AND creator_user_id IS NOT NULL    -- 防御性
  AND status = 'active'
  AND publish_status = 'published'
  AND deleted_at IS NULL              -- GORM 自动注入
ORDER BY created_at DESC
LIMIT ? OFFSET ?
```

参数：`creator_user_id = ownerID`，`ownerID` = 当前用户所属父账户 user.id。

**预期 EXPLAIN**（user 表 60 行 / sop_template 当前 4 行 / 5 年预期 < 50K 行）：

```
ref index idx_st_creator
key_len 5 (int unsigned + nullable padding)
rows ~ 估算行数（小）
Extra: Using where
```

单列 index `idx_st_creator` 已足够，无需复合索引。

### 4.2 sales_agent_owner 查询

```sql
SELECT COUNT(*) FROM sales_agent_owner
WHERE parent_user_id = ?
```

参数：`parent_user_id = ownerID`。

**预期 EXPLAIN**：PK 唯一索引 lookup，`type=const`，1 行扫描。

### 4.3 现有 chatbot 列表查询不变

参考 `chatbot_config.user_id` 上的 `ListPublishedByOwner`，本次**完全不动**——chatbot 已经正确按 owner_parent_user_id 过滤。

---

## §5 行为规格（覆盖 PRD §4 全部 20+ 验收标准 + 8 边界）

### 5.1 数据迁移正确性

| ID | 验收点 | 测试方式 |
|----|--------|--------|
| AC1 | migration 后 `sop_template.id=1` 和 `id=2` 的 `creator_user_id = 30` | migration 测试 + 集成测试 |
| AC2 | migration 后 `sales_agent_owner` 表有且仅 1 行 `(parent_user_id=30)` | 集成测试 |
| AC3 | migration 是顺序幂等（MySQL DDL 非严格事务） | 见 §7 |
| AC4 | migration 幂等（dev → qa → prod 重复执行不报错不重复） | 见 §7 |
| AC5 | `user_feature_permission` 表 48 行 sales_agent 数据零变更 | 修改前后 SELECT COUNT(*) 一致 |

### 5.2 修复前后行为对照（user 30 视角不变量）

| ID | 谁登录 | 看 SOP 列表 | 看销售智能体磁贴 | 看 chatbot 列表 | content_monitor 行为 | self_service_config 行为 |
|----|--------|-----------|-----------------|----------------|----------------------|--------------------------|
| AC6 | user 30 父账户 | 3 行 (id=1, 2, 4)，HasPermission=true | ✓ 见到 | 8 行 (user_id=30) | ✓ 可访问 | ✓ 可访问 |
| AC7 | user 30 子账户 sub_user_id=345 | 3 行，HasPermission 按 user_template_permission 行 | ✓ 见到（有 user_feature_permission 行）| 视 visibility + permission | 按 user_feature_permission（零行→deny） | 按 user_feature_permission（零行→deny） |
| AC8 | admin (id=1) | 0 行（admin 自创 SOP 0 条） | ✗ 不见 | 1 行 (user_id=1 的 demo chatbot) | ✓ 可访问（父 bypass 保留） | ✓ 可访问（父 bypass 保留） |
| AC9 | 未来新机构父账户 X | X 自创 SOP（默认 0 行） | ✗ 不见 | X 自创 chatbot（默认 0 行） | ✓ 可访问（父 bypass 保留）| ✓ 可访问（父 bypass 保留）|

### 5.3 代码层验收

| ID | 验收点 |
|----|--------|
| AC10 | `biz.CheckFeaturePermission(ctx, userID, "sales_agent")` 不存在父账户 IS NULL → return true 硬 bypass 路径 |
| AC11 | `biz.CheckFeaturePermission(ctx, userID, "content_monitor")` 父账户 IS NULL → return true（bypass 保留）|
| AC12 | `biz.CheckFeaturePermission(ctx, userID, "self_service_config")` 父账户 IS NULL → return true（bypass 保留）|
| AC13 | `sopBiz.CreateTemplateByUser` 当 user.ParentUserID != nil 时 return ErrForbidden |
| AC14 | `sopBiz.CreateTemplate` (admin) 必须传入非零 adminUserID 否则 error |
| AC15 | `middleware.FeaturePermission` 调用方变为 `biz.B.Customers().CheckFeaturePermission` |
| AC16 | `store.ISopStore.ListVisibleTemplates` 接口签名包含 `ctx context.Context, ownerParentUserID uint` 两参数 |
| AC17 | `model.SopTemplate.CreatorUserID` 注释更新反映"租户 owner = 父账户 id"语义 |
| AC18 | `model.SalesAgentOwner` 存在，TableName 返回 `sales_agent_owner` |
| AC19 | `store.ISalesAgentOwnerStore.Exists` 存在，参数 `parentUserID uint`，返回 `(bool, error)` |
| AC20 | `store.ICustomerStore.HasFeaturePermission` 旧方法被删，新方法 `CheckSubUserFeatureGrant` 替代 |
| AC21 | `biz.IMonitorBiz` 等其他 `HasFeaturePermission` 调用方迁移到 `biz.Customers().CheckFeaturePermission` |

### 5.4 边界 case 行为

| ID | 场景 | 预期行为 |
|----|------|---------|
| B1 | 子账户的 `parent_user_id` 指向已删除（软删）的用户 | Layer 0 owner 查询用 parent_user_id；若 sales_agent_owner 表中 FK CASCADE 已清掉该行（user hard-delete 触发）→ deny；若 user 软删未触发 CASCADE → 仍可能返回 true（取决于 user 软删未触发 FK），需 case-by-case 验证 |
| B2 | 子账户 `parent_user_id` 为 NULL（异常数据，子账户本不应这样） | Layer 0 解析 `parentID = user.ID`，`SalesAgentOwners.Exists(subUser.ID)` 几乎肯定 false → deny |
| B3 | 父账户登录但 user.id **不在** sales_agent_owner | deny（admin 在 prod 上正是此路径）|
| B4 | SOP 列表查询时父账户名下零子账户 | 单值 `WHERE creator_user_id = parent_id` 仍能匹配父账户自创的 SOP |
| B5 | `creator_user_id IS NULL` 的历史 SOP 行（理论上迁移后零行） | 被 SQL 防御性 `IS NOT NULL` 过滤掉，永不返回 |
| B6 | migration dev 已跑过，qa 再跑 | `CREATE TABLE IF NOT EXISTS` 跳过；`INSERT IGNORE` 跳过；UPDATE 是幂等的（重复 SET 同值无副作用）|
| B7 | 并发：迁移期间用户访问销售智能体 | 迁移仅在低峰跑，且 INSERT 单行 < 1ms；理论上极小窗口内访问返回 false（数据未到位），retry 即解 |
| B8 | sub-user 在 user_feature_permission 有 sales_agent 行但父账户被从 sales_agent_owner 撤销 | Layer 0 false → deny（双层 AND）|

---

## §6 测试矩阵

### 6.1 Store 层单测

文件：`internal/numind/store/sales_agent_owner_test.go`（新增）

| Test | 覆盖点 |
|------|--------|
| `TestSalesAgentOwner_Exists_True` | INSERT 后 Exists 返回 (true, nil) |
| `TestSalesAgentOwner_Exists_False` | 空表 / 不存在 parent_user_id 返回 (false, nil)，不是 ErrRecordNotFound |
| `TestSalesAgentOwner_Exists_DifferentParentID` | seed parent 30 后查 parent 1 返回 false |
| `TestSalesAgentOwner_Exists_DBError` | DB 关闭后 Exists 返回 (false, err) |

文件：`internal/numind/store/sop_template_visibility_test.go`（扩展，沿用现有 visibility 测试文件，避免新建 sop_test.go）

| Test | 覆盖点 |
|------|--------|
| `TestListVisibleTemplates_FilterByOwner` | seed: parentA 2 published + parentB 1 published + parentA 1 draft + 1 NULL creator + 1 soft-deleted parentA. 查 parentA → 仅 2 行 |
| `TestListVisibleTemplates_DefensiveNullFilter` | seed: 1 行 creator NULL active published. 查任意 parentID → 0 行 |
| `TestListVisibleTemplates_EmptyForNonExistentParent` | 查 parentID=999 → 0 行 |

文件：`internal/numind/store/customer_test.go` + `customer_permission_lifecycle_test.go`（迁移）

| Test | 覆盖点 |
|------|--------|
| `TestCheckSubUserFeatureGrant_True` | 有 user_feature_permission 行 → (true, nil) |
| `TestCheckSubUserFeatureGrant_False` | 无行 → (false, nil) |
| `TestCheckSubUserFeatureGrant_FeatureKeyDoesNotMix` | 其他 feature_key 行存在不影响目标 feature_key 查询 |
| `customer_permission_lifecycle_test.go` 中 11 处 `HasFeaturePermission` 调用 | 全部迁移到 `CheckSubUserFeatureGrant`（或必要时调 biz `CheckFeaturePermission`）|

### 6.2 Biz 层单测

文件：`internal/numind/biz/customer/customer_test.go`（扩展）

| Test | 覆盖点 |
|------|--------|
| `TestCheckFeaturePermission_SalesAgent_ParentOwnerExists` | parent in sales_agent_owner → true |
| `TestCheckFeaturePermission_SalesAgent_ParentOwnerAbsent` | parent NOT in sales_agent_owner → false（**关键回归点**：admin 路径覆盖）|
| `TestCheckFeaturePermission_SalesAgent_SubUserBothLayers` | parent in owner ∧ sub-user has user_feature_permission → true |
| `TestCheckFeaturePermission_SalesAgent_SubUserLayer1Only` | parent NOT in owner ∧ sub-user has user_feature_permission → false（Layer 0 拦截）|
| `TestCheckFeaturePermission_SalesAgent_SubUserLayer0Only` | parent in owner ∧ sub-user NO user_feature_permission → false（Layer 1 拦截）|
| `TestCheckFeaturePermission_ContentMonitor_ParentBypass` | parent (parent_user_id=nil) + content_monitor → true（**回归点**：bypass 保留）|
| `TestCheckFeaturePermission_SelfServiceConfig_ParentBypass` | parent + self_service_config → true（**回归点**：bypass 保留）|
| `TestCheckFeaturePermission_ContentMonitor_SubUserDeny` | sub-user + content_monitor + 无 user_feature_permission → false（行为保持现状）|
| `TestCheckFeaturePermission_UserNotFound` | userID 不存在 → (false, err)，error 包含 gorm.ErrRecordNotFound 链 |

文件：`internal/numind/biz/sop/sop_test.go`（扩展）

| Test | 覆盖点 |
|------|--------|
| `TestListVisibleTemplatesWithPermission_ParentSeesOwn` | parent → 自己 owner 的 SOP，HasPermission 全 true |
| `TestListVisibleTemplatesWithPermission_SubUserScoped` | sub-user → 父账户 owner 的 SOP，HasPermission 按 user_template_permission |
| `TestListVisibleTemplatesWithPermission_DoesNotLeakOtherParent` | parentA seed + parentB seed，parentA login → 仅看到 parentA 的 |
| `TestCreateTemplateByUser_ParentSucceeds` | parent.ParentUserID=nil → 成功，creator_user_id = parent.ID |
| `TestCreateTemplateByUser_SubUserRejected` | sub-user.ParentUserID != nil → ErrForbidden |
| `TestCreateTemplate_AdminSucceeds` | 传入 adminUserID=1 → 成功，creator_user_id=1 |
| `TestCreateTemplate_RequiresAdminUserID` | adminUserID=0 → error |

### 6.3 集成测试（端到端 + 数据迁移）

文件：`numind-server/migrations/audit/test_migration_20260518.go`（或类似）

| Test | 覆盖点 |
|------|--------|
| `TestMigration_Idempotent` | 跑两次迁移，第二次 0 错误，行数不变 |
| `TestMigration_AfterRunUser30Visibility` | 跑完后用 user 30 token 访问，list 行数 = 3 |
| `TestMigration_AfterRunAdminVisibility` | 跑完后用 admin token 访问，list 行数 = 0 |
| `TestMigration_AfterRunSalesAgentPermission` | user 30 → has_permission=true; admin → false |

---

## §7 Migration 设计

### 7.1 Migration SQL 文件

文件：`numind-server/migrations/20260518_220000_sop_salesrag_parent_scope.sql`

```sql
-- +migrate Up
-- sop-salesrag-parent-scope: 多租户 Layer 0 父账户归属隔离
-- 见 docs/superpowers/specs/2026-05-18-sop-salesrag-parent-scope-design.md
--
-- 操作:
--   1. CREATE TABLE sales_agent_owner (用 IF NOT EXISTS 幂等)
--   2. INSERT sales_agent_owner (parent_user_id=30) (用 INSERT IGNORE 幂等)
--   3. UPDATE sop_template SET creator_user_id=30 WHERE id IN (1, 2)
--      (UPDATE 重复执行无副作用, 天然幂等)
--
-- 注: 本仓库 migration 由人工 SSH 跑 (CI 不跑, 见 memory dev_deploy_migration_gap).
-- MySQL DDL 不是事务的; 失败处理由人工判断是否需要手工回滚 (§8).

CREATE TABLE IF NOT EXISTS sales_agent_owner (
  parent_user_id INT UNSIGNED NOT NULL PRIMARY KEY,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_sao_parent FOREIGN KEY (parent_user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='销售智能体父账户归属表（owner tag）';

INSERT IGNORE INTO sales_agent_owner (parent_user_id, created_at)
  VALUES (30, NOW(3));

UPDATE sop_template SET creator_user_id = 30 WHERE id IN (1, 2);

-- 完成后人工 verify:
--   SELECT COUNT(*) FROM sales_agent_owner WHERE parent_user_id=30;  -- 应为 1
--   SELECT id, creator_user_id FROM sop_template WHERE id IN (1,2,3,4);  -- 应均为 30
```

### 7.2 部署流程

参照 memory `project_dev_deploy_migration_gap`：

1. develop 分支合并后，手工 SSH dev 跑 migration（CI 不跑）
2. dev 验证后 release 分支同样手工 SSH qa 跑
3. tag 触发 prod 部署前，先 SSH prod 跑 migration，再触发服务部署
4. 每环境跑完用 §7.1 末尾的 verify SQL 确认

---

## §8 回滚方案

### 8.1 代码回滚

git revert merge commit → CI 自动重新部署服务到上一版本镜像。回滚后服务端代码读旧路径（`HasFeaturePermission` 父账户 bypass 全 feature），但 DB schema 已变。

### 8.2 数据库回滚

文件：`numind-server/migrations/20260518_220000_sop_salesrag_parent_scope_rollback.sql`

```sql
-- +migrate Down
-- Rollback for sop-salesrag-parent-scope.
-- 注意: 这会重新打开数据泄露 bug, 仅在 critical incident 下使用.

UPDATE sop_template SET creator_user_id = NULL WHERE id IN (1, 2);

DROP TABLE IF EXISTS sales_agent_owner;
```

**Note**: 回滚 DROP TABLE 时 FK CASCADE 自动清掉所有行，不需要先 DELETE。

### 8.3 回滚的副作用

- 4 个实体重新跨父账户全可见（但 prod 当前仅 2 父账户、admin 是 demo，影响面有限）
- 回滚后 prod 未来 admin 创建的 SOP 重新产生 NULL creator_user_id 行（旧 admin Create 路径不传 userID）

回滚的可接受窗口：**仅在新代码发现 P0 bug 时执行**。Prod 仅 2 父账户的现状下，回滚优于让 bug 漫延。

---

## §9 决策记录

### D1: SOP `creator_user_id` 语义升级为"始终存父账户 id"

**Context**: 当前 user 路径 CreateTemplateByUser 写入 actor.ID（可能子用户），admin 路径 CreateTemplate 写入 NULL；列表过滤需要按"租户"语义匹配。

**Decision**: 改为始终存父账户 user.id。两个写入路径都修改：user 路径 biz 层 assert `actor.ParentUserID == nil` 后写 `&actor.ID`；admin 路径加 `adminUserID` 参数写 `&adminUserID`。

**Rationale**: 与 chatbot_config.user_id 对齐（单值 WHERE 过滤，trivial 索引性能）。子用户被 reparent 时 SOP 不会"飘"。

**Rejected alternatives**:
- 保留原 actor.ID 语义 + 列表用 `creator_user_id IN (SELECT id FROM user WHERE id=? OR parent_user_id=?)` 子查询：每次 list 多一次 user 表 subquery；子用户 reparent 行为不一致；心智模型不对齐 chatbot
- 新增 `owner_parent_user_id` 列保留 creator_user_id 原审计语义：列冗余，索引冗余，迁移路径更长，YAGNI 违反

**Trade-off**: 字段名 `creator_user_id` 略 misleading（实际是 owner）。通过 model 注释 + 本 spec §2.2 显式声明语义升级缓解。Field rename 推迟（RENAME column 在 GORM + prod 上风险大）。

### D2: HasFeaturePermission dispatch 上移至 biz 层

**Context**: 既有 middleware 直调 store.HasFeaturePermission 跳过 biz，违反 controller→biz→store 单向规则。同时本次需要在该函数加 sales_agent 分支。

**Decision**: dispatch + 双层 AND 逻辑放 biz 层 `CheckFeaturePermission`；store 层只剩纯查询（`SalesAgentOwners.Exists` + `CheckSubUserFeatureGrant`）。middleware 改调 biz。

**Rationale**: 修正既有 layer violation + 符合 CLAUDE.md §3"业务判断必须在 biz 层"。dispatch logic 测试时无需 db user 表 mock 之外的复杂依赖。

**Implementation note**: biz 层目前没有 `biz.B` 全局单例（类比 `store.S`），本次需要引入。在 plan Task 3 内完成 wiring。

**Rejected alternatives**:
- inline if-tree 在 store 层：layer violation 继续；测试需 mock 整张 user 表 + user_feature_permission 表 + sales_agent_owner 表
- strategy pattern：对 3 个 feature_key 过度抽象

### D3: sales_agent_owner 表极简（无软删，PK = parent_user_id）

**Context**: 需要给销售智能体打 owner tag；类似表（chatbot_visibility_grant 等）历史上用 gorm.Model 带 deleted_at。

**Decision**: 字段仅 `parent_user_id INT UNSIGNED PK + created_at`。无 deleted_at、updated_at、auto-increment id。加 FK ON DELETE CASCADE 到 user(id)。

**Rationale**:
- 该表是"配置元数据"，无业务实体 lifecycle，没审计要求
- 写场景仅 INSERT（migration）和理论 DELETE（手工撤回），无 UPDATE
- 软删带来 Unscoped 恢复模式与本表语义不符
- INT 而非 BIGINT：与 user.id 类型对齐（避免 JOIN 索引失效）
- FK CASCADE：父账户被 hard-delete 时自动清理孤儿行

**Rejected alternatives**:
- gorm.Model 全套软删：YAGNI
- 增加 `updated_at`：write-once 表无更新场景
- BIGINT：与 user.id INT UNSIGNED 不匹配

### D4: 索引保持现状（不加复合索引）

**Context**: SOP 列表查询加 owner 过滤后，是否需要 (creator_user_id, status, publish_status) 复合索引？

**Decision**: 不加。`sop_template.creator_user_id` 已有 `idx_st_creator` 单列索引。

**Rationale**: 5 年规模预期 < 50K 行，单列索引 + filter sort 足够。复合索引徒增 INSERT 开销。`sales_agent_owner.parent_user_id` 作为 PK 已是唯一索引。

### D5: 字段名 `creator_user_id` 不重命名

**Context**: D1 后 creator_user_id 实际存 owner parent_user_id，字段名 misleading。

**Decision**: 保留名字 + model 注释强化语义说明。Field rename 推迟。

**Rationale**:
- RENAME column 在 GORM 框架 + prod 部署下风险大（中间态、ORM cache、回滚复杂）
- 注释 + spec §2.2 让读代码者立刻看到语义
- Field rename 是可独立做的后续 tech-debt-cleanup，不阻塞本需求

### D6: admin Create 路径也必须修

**Context**: reviewer 发现 `internal/numind/biz/sop/sop.go:142 CreateTemplate` 不传 userID，写入 NULL；这正是 prod id=1,2 产生 NULL 的源头。

**Decision**: 修复签名为 `CreateTemplate(ctx, adminUserID, name, description, prompt)`，强制非零 adminUserID；admin controller 从 JWT 取 admin user.id 传入；template.CreatorUserID = &adminUserID。

**Rationale**: 不修这条路径，下一次 admin 创建 SOP 立刻又产生 NULL 行，本次修复失效。admin SOP 归属 admin 自己（id=1），不跨租户泄露给 user 30。

### D7: 防御性 `IS NOT NULL` 过滤

**Context**: 历史脏数据可能有 NULL creator_user_id（已被 migration UPDATE 掉，但防御未来）。

**Decision**: SOP 列表 SQL 加 `AND creator_user_id IS NOT NULL`。

**Rationale**: 双保险——即使将来某条新写入路径漏掉 owner 解析造成 NULL，列表也不会泄露。

### D8: biz 层 defense-in-depth assertion

**Context**: reviewer 指出 `CreateTemplateByUser` 依赖外部 `ParentUserOnly` 中间件保证 parent invariant，biz 函数本身没法显式声明。

**Decision**: 在 `CreateTemplateByUser` 入口加 `if actor.ParentUserID != nil { return ErrForbidden }`。

**Rationale**: 让 biz 函数自带不变量声明；如果将来 wire 接入未套中间件的新路由，立刻 fail-fast 而不是默写脏数据。

### D9: 不修改 chatbot 创建路径

**Context**: reviewer 提到 chatbot 也可能有类似 admin/user 创建路径问题。

**Decision**: 本次 spec 范围明确**不含** chatbot 创建路径审查。

**Rationale**: chatbot 列表 SQL 已正确按 user_id 过滤，prod 数据无 NULL；本次仅修 SOP + sales_agent。chatbot 创建路径审查作为后续可选 tech-debt-cleanup（如有问题再单独 feature 处理）。

---

## §10 与现有功能的边界

| 已上线功能 | 与本需求的关系 |
|------------|----------------|
| `sop-chatbot-visibility-scope` (2026-05-14) | **本需求是它的 Layer 0**——三层 gate 串行：Layer 0 (本需求) → Layer 1 (visibility_restricted) → Layer 2 (user_template_permission) |
| `child-run-permission` (2026-04-20) | Layer 2 = `user_template_permission` 由该 feature 引入。本需求不动其表结构和语义 |
| `sales-agent-child-permission` | 与本需求是 Layer 0 vs Layer 1 关系。`user_feature_permission` 48 行子账户授权保留不动 |

## §11 风险与缓解

| 风险 | 缓解 |
|------|------|
| migration 部分失败（CREATE TABLE 成 INSERT 失败等 MySQL DDL 半事务问题）| §7.1 单文件按顺序写；幂等设计；人工 verify SQL；§8 提供 rollback SQL |
| prod 第 3 个父账户测试不充分 | 本次仅 2 父账户 prod 数据；通过单测 + 集成测试 mock 第 3 个 parent 验证 |
| 子用户 reparent 后行为 | D1 设计已考虑：creator_user_id = 旧父账户 → SOP 留在旧父账户租户内（正确）|
| layer violation 修复有未发现的中间件路径 | grep `store.S.Customers().HasFeaturePermission` 全仓库定位所有调用点；S4 编码时确保全部改为 biz 路径。已识别：middleware.go:222, biz/monitor/monitor.go:146, customer_permission_lifecycle_test.go 11 处 |
| `biz.B` 全局变量命名冲突 | S4 编码时 grep 确认未占用；如冲突改名 |

## §12 S5 验证策略草案

**验证方式**: 后端 Go 单测 + 集成测试 + Playwright E2E（最关键的 5 角色 × 资源访问矩阵）

**理由**: 本需求是后端跨层多租户隔离，UI 无感。功能涉及高风险数据可见性，必须 Playwright 端到端验证（gstack `/qa` 一次性不留回归保护，对该高风险路径不够）。

**关键用户路径**:
1. 用 user 30 token 登录用户端，访问 /home，期望见到 3 SOP + 销售智能体磁贴
2. 用 user 30 子账户 (sub_user_id=345) token 登录，访问 /home，期望见到 3 SOP + 销售智能体磁贴
3. 用 admin token 登录用户端，访问 /home，期望见到 0 SOP + 无销售智能体磁贴
4. 制造一个测试父账户 X（迁移期临时创建），用 X token 登录，期望见到 0 SOP + 无销售智能体磁贴
5. user 30 父账户测试 content_monitor + self_service_config 仍可访问（回归保护）

S3 plan 阶段把这些路径展开为具体 Playwright spec 文件。
