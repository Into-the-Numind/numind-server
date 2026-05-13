# SOP / 智能体可见范围权限 — 技术设计 Spec

**功能 ID**: `sop-chatbot-visibility-scope`
**ndf_version**: 1.1
**前置工件**: [requirement](../../../requirements/sop-chatbot-visibility-scope.md) | [proposal+PRD](../../../proposals/sop-chatbot-visibility-scope-proposal.md)
**日期**: 2026-05-13
**作者**: AI (S2 spec phase, brainstorming skill)

---

## §1 概览与范围

### 1.1 目的

在 SOP 模板和智能体（chatbot）编辑页**内联**新增「可见范围」权限：父账户可选择"仅向部分子用户展示"，未在白名单的子用户在工作区**列表层**看不到该实体。与已有的 child-run-permission（运行权限层）共存，串行 gate：可见范围 → 运行权限。

### 1.2 范围边界

| 范围 | 含 | 不含 |
|------|---|------|
| 实体类型 | SOP 模板、智能体 (chatbot_config) | 销售知识库 (SalesRAG)、卡片资源 |
| 配置方 | 父账户（C 端） | Admin 端、子账户 |
| 控制层级 | 工作区列表展示 | 运行权限 (已 child-run-permission)、运行历史可见性 |
| 时间维度 | 仅影响后续列表请求 | 进行中 run / 历史 run 不撤回 |

### 1.3 关键决策回顾（来自 S1 D1-D7 + S2 战术决策）

| # | 决策 | 选定方案 |
|---|------|---------|
| D1 | 数据表 | 独立新表 `sop_visibility_grant` + `chatbot_visibility_grant` |
| D2 | API 端点 | 独立端点（PUT/GET visibility × 2 资源 = 4 新端点）|
| D3 | 开关关闭名单数据 | 保留（重新打开恢复同一名单）|
| D4 | 子用户级联清理 | 单事务清理 4 张表 |
| D5 | 性能短路字段 | `visibility_restricted` boolean 加在 sop_template + chatbot_config |
| D6 | 通用 entity_type | 不引入，过度抽象 |
| D7 | 软删除 | 启用（与 user_template_permission 一致）|
| ST1 | 列表过滤模式 | 延续现有 O(N) 本地过滤（与 child-run-permission 一致）|
| ST2 | PUT grants 保存 | 全删全插（single transaction）|
| ST3 | 前端弹窗组件 | 提取为可复用 `SubUserMultiSelectDialog` |

---

## §2 数据模型

### 2.1 现有表字段扩展

```sql
-- sop_template
ALTER TABLE sop_template
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '0=全部子用户可见; 1=仅 sop_visibility_grant 中的子用户可见';

-- chatbot_config
ALTER TABLE chatbot_config
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '0=全部子用户可见; 1=仅 chatbot_visibility_grant 中的子用户可见';
```

**索引策略**：不为 `visibility_restricted` 单独建索引。理由：99% 实体不会启用限制，选择率极高（接近全表），B-tree 索引反而拖慢；本字段仅用于**字段读取后的代码分支判断**（is the entity restricted? → 决定是否查 grant 表），不参与 WHERE 过滤的 SQL 索引利用。

**GORM 模型字段补丁**（在现有 struct 后追加）：

```go
// internal/pkg/model/sop.go — SopTemplate 末尾追加
VisibilityRestricted bool `gorm:"not null;default:0" json:"visibility_restricted"` // S2.1

// internal/pkg/model/chatbot.go — ChatbotConfig 末尾追加
VisibilityRestricted bool `gorm:"not null;default:0" json:"visibility_restricted"` // S2.1
```

注：**不使用** `default:true` 风格（database.md §6 已记录的 GORM gotcha），`default:0` 即可且与字段语义一致。

### 2.2 新表 `sop_visibility_grant`

```sql
CREATE TABLE sop_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL COMMENT '父账户 ID（caller，冗余便于查询）',
  sub_user_id BIGINT UNSIGNED NOT NULL COMMENT '被授权可见的子用户 ID',
  sop_template_id BIGINT UNSIGNED NOT NULL COMMENT '受限可见的 SOP 模板 ID',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_svg_sub_template_unique (sub_user_id, sop_template_id, deleted_at),
  KEY idx_svg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_svg_template (sop_template_id),
  KEY idx_svg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='SOP 可见范围授权（白名单）';
```

**索引设计说明**：
- `idx_svg_sub_template_unique` (sub_user_id, sop_template_id, deleted_at)：唯一约束，但允许"软删 → 重新授予"（每个软删记录的 deleted_at 不同，satisfy uniqueness）
- `idx_svg_parent_sub`：清理「某子用户所有 grant」用，DeleteSubUser 路径
- `idx_svg_template`：列表查询「某 SOP 的全部 sub_user_ids」用，GET visibility 路径
- `idx_svg_deleted`：辅助软删过滤（GORM 标准实践）

### 2.3 新表 `chatbot_visibility_grant`

```sql
CREATE TABLE chatbot_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL,
  sub_user_id BIGINT UNSIGNED NOT NULL,
  chatbot_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_cvg_sub_chatbot_unique (sub_user_id, chatbot_id, deleted_at),
  KEY idx_cvg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_cvg_chatbot (chatbot_id),
  KEY idx_cvg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='Chatbot 可见范围授权（白名单）';
```

### 2.4 GORM 模型文件

```go
// internal/pkg/model/sop_visibility_grant.go (新文件)
package model

import "gorm.io/gorm"

// SopVisibilityGrant SOP 可见范围授权表（白名单）
type SopVisibilityGrant struct {
    gorm.Model
    ParentUserID  uint `gorm:"not null;index:idx_svg_parent_sub" json:"parent_user_id"`
    SubUserID     uint `gorm:"not null;index:idx_svg_sub_template_unique,unique;index:idx_svg_parent_sub" json:"sub_user_id"`
    SopTemplateID uint `gorm:"not null;index:idx_svg_sub_template_unique,unique;index:idx_svg_template" json:"sop_template_id"`

    ParentUser  *User        `gorm:"foreignKey:ParentUserID;references:ID" json:"parent_user,omitempty"`
    SubUser     *User        `gorm:"foreignKey:SubUserID;references:ID" json:"sub_user,omitempty"`
    SopTemplate *SopTemplate `gorm:"foreignKey:SopTemplateID;references:ID" json:"sop_template,omitempty"`
}

func (SopVisibilityGrant) TableName() string { return "sop_visibility_grant" }
```

```go
// internal/pkg/model/chatbot_visibility_grant.go (新文件)
package model

import "gorm.io/gorm"

// ChatbotVisibilityGrant Chatbot 可见范围授权表（白名单）
type ChatbotVisibilityGrant struct {
    gorm.Model
    ParentUserID uint `gorm:"not null;index:idx_cvg_parent_sub" json:"parent_user_id"`
    SubUserID    uint `gorm:"not null;index:idx_cvg_sub_chatbot_unique,unique;index:idx_cvg_parent_sub" json:"sub_user_id"`
    ChatbotID    uint `gorm:"not null;index:idx_cvg_sub_chatbot_unique,unique;index:idx_cvg_chatbot" json:"chatbot_id"`

    ParentUser *User          `gorm:"foreignKey:ParentUserID;references:ID" json:"parent_user,omitempty"`
    SubUser    *User          `gorm:"foreignKey:SubUserID;references:ID" json:"sub_user,omitempty"`
    Chatbot    *ChatbotConfig `gorm:"foreignKey:ChatbotID;references:ID" json:"chatbot,omitempty"`
}

func (ChatbotVisibilityGrant) TableName() string { return "chatbot_visibility_grant" }
```

### 2.5 不变量 (Invariants)

| # | 不变量 | 强制方式 |
|---|--------|---------|
| I-1 | `visibility_restricted=0` 时，对应 grant 表中可能有也可能没有记录（D3 保留语义）；查询时**不**应读 grant 表 | 代码逻辑（biz 层短路判断） |
| I-2 | `visibility_restricted=1` 时，grant 表中**可以**有 0 条记录（语义：所有子用户都看不到，白名单严格） | DDL 允许；biz 层不做"必须 >0"校验 |
| I-3 | grant 记录的 `parent_user_id` 必须等于该 entity 的 owner | biz 层校验，PUT 端点强制 |
| I-4 | grant 记录的 `parent_user_id` 必须等于 sub_user 的 parent_user_id | biz 层校验，PUT 端点强制 |
| I-5 | 软删除的 grant 记录在 visibility 判断中**不计入**白名单 | GORM `DeletedAt` 字段自动过滤 |
| I-6 | 同一 (sub_user_id, entity_id) 在 "未软删" 状态下只能存在一行 | 唯一索引强制 |

---

## §3 API 契约

### 3.1 端点清单

| 方法 | 路径 | 角色 | 用途 |
|------|------|------|------|
| GET | `/v1/sop/templates/:id/visibility` | 父账户 | 读取 SOP 的可见范围配置 |
| PUT | `/v1/sop/templates/:id/visibility` | 父账户 | 更新 SOP 的可见范围配置 |
| GET | `/v1/chatbot/:id/visibility` | 父账户 | 读取 chatbot 的可见范围配置 |
| PUT | `/v1/chatbot/:id/visibility` | 父账户 | 更新 chatbot 的可见范围配置 |
| GET | `/v1/sop/templates`（既有） | 子用户 | **行为变更**：内部加 visibility 过滤 |
| GET | `/v1/chatbot/list`（既有） | 子用户 | **行为变更**：内部加 visibility 过滤 |

### 3.2 GET `/v1/sop/templates/:id/visibility`

**Auth**: 父账户 token（中间件 `user_token`）

**Path params**:
- `:id` (uint, required) — SOP 模板 ID

**Response 200**:
```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "restricted": true,
    "sub_user_ids": [101, 102, 105]
  }
}
```

**Errors**:
- `404 ResourceNotFound.SopTemplateNotFound` — SOP 不存在
- `403 FailedOperation.EntityNotOwnedByCaller` — caller 不是 SOP owner

### 3.3 PUT `/v1/sop/templates/:id/visibility`

**Auth**: 父账户 token

**Path params**: `:id` (uint, required)

**Request body**:
```json
{
  "restricted": true,
  "sub_user_ids": [101, 102, 105]
}
```

| Field | Type | Required | Constraints |
|-------|------|----------|-------------|
| `restricted` | bool | ✅ | true → 启用白名单；false → 关闭白名单（保留 grant 数据，D3） |
| `sub_user_ids` | []uint | 条件 | 当 `restricted=true` 时必填（可为空数组）；当 `restricted=false` 时**忽略**该字段 |

**Response 200**:
```json
{
  "code": 0,
  "message": "ok",
  "data": null
}
```

**Errors**:
- `400 InvalidParameter.BindError` — JSON 绑定失败
- `404 ResourceNotFound.SopTemplateNotFound`
- `403 FailedOperation.EntityNotOwnedByCaller`
- `422 InvalidParameter.CrossParentSubUser` — sub_user_ids 中存在不属于 caller 的子用户
- `422 InvalidParameter.SubUserNotFound` — sub_user_ids 中存在不存在的用户

**幂等性**：连续相同请求得到相同状态（全删全插语义 + 唯一索引保证）。

### 3.4 GET `/v1/chatbot/:id/visibility` 与 PUT `/v1/chatbot/:id/visibility`

**结构与 SOP 端点完全对称**，仅资源类型不同。错误码：
- `404 ResourceNotFound.ChatbotNotFound`（替代 SopTemplateNotFound）
- 其他错误码共用

### 3.5 已有端点的行为变更

#### `GET /v1/sop/templates`（子用户身份调用）

**调用链**：`controller.ListTemplates` → `biz.ListVisibleTemplatesWithPermission(subUserID)` → `store.ListVisibleTemplates`

**新增过滤逻辑**（在 biz 层）：
```
1. 全量查 published+active SOP 列表（不变）
2. NEW: 调用 ListSubUserVisibleSopIDs(subUserID) 获取该子用户的可见集 set
3. NEW: 过滤掉 visibility_restricted=true 且不在 set 中的 SOP
4. 调用 ListSubUserTemplateIDs(subUserID) 获取运行权限白名单 set（既有）
5. 过滤掉运行权限不在 set 中的 SOP（既有）
6. 返回剩余列表
```

**父账户身份调用**：不应用 visibility 过滤（父账户始终看到自己创建的全部）。判断条件：`user.ParentUserID == nil`。

#### `GET /v1/chatbot/list`（子用户身份）

对称逻辑，调用 `ListSubUserVisibleChatbotIDs` + `ListSubUserChatbotIDs`。

### 3.6 错误码定义清单

新增错误码（在 `internal/pkg/errno/code.go` 中定义）：

```go
// SOP / Chatbot 可见范围权限相关
var (
    // ErrEntityNotOwnedByCaller 表示 caller 尝试操作不属于自己的实体.
    ErrEntityNotOwnedByCaller = &Errno{
        HTTP: 403,
        Code: "FailedOperation.EntityNotOwnedByCaller",
        Message: "The entity is not owned by the caller.",
    }

    // ErrCrossParentSubUser 表示父账户提交了不属于自己的子用户 ID.
    ErrCrossParentSubUser = &Errno{
        HTTP: 422,
        Code: "InvalidParameter.CrossParentSubUser",
        Message: "One or more sub_user_ids do not belong to the caller.",
    }

    // ErrSopTemplateNotFound 表示 SOP 模板不存在.
    ErrSopTemplateNotFound = &Errno{
        HTTP: 404,
        Code: "ResourceNotFound.SopTemplateNotFound",
        Message: "SOP template was not found.",
    }

    // ErrChatbotNotFound 表示智能体不存在.
    ErrChatbotNotFound = &Errno{
        HTTP: 404,
        Code: "ResourceNotFound.ChatbotNotFound",
        Message: "Chatbot was not found.",
    }
)
```

`ErrSopTemplateNotFound` / `ErrChatbotNotFound` 如果已存在于 errno 包中，**复用现有**，不重复定义（S4 实施前确认）。

### 3.7 Router 注册

```go
// internal/numind/router.go — 在用户端路由组内追加
sopGroup := userGroup.Group("/sop")
{
    // ... 既有路由 ...
    templatesGroup := sopGroup.Group("/templates")
    {
        // ... 既有路由 ...
        templatesGroup.GET("/:id/visibility", sopController.GetVisibility)   // NEW
        templatesGroup.PUT("/:id/visibility", sopController.UpdateVisibility) // NEW
    }
}

chatbotGroup := userGroup.Group("/chatbot")
{
    // ... 既有路由 ...
    chatbotGroup.GET("/:id/visibility", chatbotController.GetVisibility)    // NEW
    chatbotGroup.PUT("/:id/visibility", chatbotController.UpdateVisibility)  // NEW
}
```

实际位置需在 S4 实施时按现有路由文件结构精准定位（router.go 可能拆分）。

---

## §4 业务层（Biz）函数

### 4.1 新增函数（在 `biz/customer/` 或新建 `biz/visibility/` 包）

**包选择**：S4 实施时确定。建议放在 `biz/sop/visibility.go` + `biz/chatbot/visibility.go`，与领域对齐；或新建 `biz/visibility/` 统一管理。本 spec 采用前者方案（避免新包带来的注入连接成本）。

#### 4.1.1 `IsSopVisibleToUser`

```go
// IsSopVisibleToUser 判断 SOP 是否对给定用户可见.
// 判断逻辑:
//   - caller 是父账户 (parent_user_id IS NULL) → true (父账户总是可见自己的实体, 不查 grant 表)
//   - SOP.visibility_restricted == false → true (开关未启用, 全部子用户可见)
//   - SOP.visibility_restricted == true → 查 grant 表, sub_user_id 在白名单则 true, 否则 false
func IsSopVisibleToUser(ctx context.Context, userID, sopID uint) (bool, error)
```

伪代码：
```
user := store.GetUser(userID)
if user.ParentUserID == nil { return true, nil }  // 父账户

sop := store.GetSopTemplate(sopID)
if sop == nil { return false, ErrSopTemplateNotFound }
if !sop.VisibilityRestricted { return true, nil }  // 短路

count := store.CountSopVisibilityGrant(sopID, userID)
return count > 0, nil
```

#### 4.1.2 `IsChatbotVisibleToUser` — 对称结构

#### 4.1.3 `ListSubUserVisibleSopIDs`

```go
// ListSubUserVisibleSopIDs 返回给定子用户被授权可见的 SOP ID 集合.
// 用于列表查询的批量过滤. 父账户调用此函数无意义 (应在调用前判断).
// 返回的 map 仅包含 visibility_restricted=true 的 SOP 中授权的 IDs;
// visibility_restricted=false 的 SOP 不通过本函数判断 (列表过滤直接放行).
func ListSubUserVisibleSopIDs(ctx context.Context, subUserID uint) (map[uint]struct{}, error)
```

实现：单条 SQL `SELECT sop_template_id FROM sop_visibility_grant WHERE sub_user_id=? AND deleted_at IS NULL`，构建 map。

#### 4.1.4 `ListSubUserVisibleChatbotIDs` — 对称

#### 4.1.5 `GetSopVisibility(sopID)` — 用于 GET 端点

```go
// GetSopVisibility 返回 SOP 的当前可见范围配置.
// 返回: (restricted, subUserIDs, error)
// restricted = sop.visibility_restricted
// subUserIDs = grant 表中未软删除的 sub_user_id 列表 (无论 restricted 是 true 还是 false; D3 保留语义)
func GetSopVisibility(ctx context.Context, sopID uint) (restricted bool, subUserIDs []uint, err error)
```

#### 4.1.6 `UpdateSopVisibility(caller, sopID, restricted, subUserIDs)` — 用于 PUT 端点

伪代码：
```
sop := store.GetSopTemplate(sopID)
if sop == nil { return ErrSopTemplateNotFound }

// 权限校验
caller := store.GetUser(callerID)
if caller.ParentUserID != nil { return ErrVisibilityPermissionDenied }
if sop.CreatorUserID == nil || *sop.CreatorUserID != callerID {
    return ErrEntityNotOwnedByCaller
}

// 仅当 restricted=true 才校验 sub_user_ids; restricted=false 时忽略请求中的 sub_user_ids
if restricted {
    if err := validateSubUsersBelongToCaller(callerID, subUserIDs); err != nil {
        return err  // ErrCrossParentSubUser 或 ErrSubUserNotFound
    }
}

// 全删全插事务 (ST2)
return store.WithTx(func(tx) error {
    // 1. 软删该 SOP 现有所有 grant
    tx.Where("sop_template_id=?", sopID).Delete(&SopVisibilityGrant{})

    // 2. 仅当 restricted=true 且 sub_user_ids 非空时插入新 grant
    if restricted && len(subUserIDs) > 0 {
        records := make([]SopVisibilityGrant, len(subUserIDs))
        for i, uid := range subUserIDs {
            records[i] = SopVisibilityGrant{
                ParentUserID:  callerID,
                SubUserID:     uid,
                SopTemplateID: sopID,
            }
        }
        tx.Create(&records)
    }

    // 3. 更新 entity 短路字段
    tx.Model(&SopTemplate{}).Where("id=?", sopID).
       Update("visibility_restricted", restricted)
    return nil
})
```

**注意**: 第 3 步用 `Update("column_name", val)` 而非 `Updates(struct)`，避免 GORM `default:true` bool gotcha（database.md §6）。本字段 `default:0` 实际无此风险，但保持代码模式一致性。

#### 4.1.7 `UpdateChatbotVisibility(...)` — 对称

#### 4.1.8 `validateSubUsersBelongToCaller`

```go
// validateSubUsersBelongToCaller 校验所有 subUserIDs 都是 caller 的直接子用户.
// 一次 SELECT 查询: WHERE id IN (?) AND parent_user_id=?
// 返回的 count 必须等于 len(subUserIDs), 否则返回 ErrCrossParentSubUser.
func validateSubUsersBelongToCaller(ctx context.Context, callerID uint, subUserIDs []uint) error
```

### 4.2 列表查询过滤接入

#### 4.2.1 `ListVisibleTemplatesWithPermission`（已有，加新过滤）

**当前实现**（来自 Explore 报告）：
```
1. store.ListVisibleTemplates() — 全量查 status='active' AND publish_status='published'
2. user := store.GetUser(userID); if user.ParentUserID == nil { 返回全部 (父账户) }
3. permissionSet := store.ListSubUserTemplateIDs(userID) — 运行权限白名单
4. 本地过滤: 仅保留 ID 在 permissionSet 中的 SOP
5. 返回
```

**新实现**（插入新过滤层）：
```
1. store.ListVisibleTemplates() — 不变
2. user := store.GetUser(userID); if user.ParentUserID == nil { 返回全部 (父账户) }
3. NEW: visibilitySet := store.ListSubUserVisibleSopIDs(userID) — 可见性白名单
4. NEW: 本地过滤: 对每个 SOP, 若 sop.VisibilityRestricted==true 且 ID 不在 visibilitySet 中, 过滤掉
5. permissionSet := store.ListSubUserTemplateIDs(userID) — 运行权限白名单 (既有)
6. 过滤: 仅保留 ID 在 permissionSet 中的 SOP (既有)
7. 返回
```

**Gate 顺序固化**: visibility（步骤 3-4）必须在 run-permission（步骤 5-6）**之前**。这样：
- 可见但无运行权限 → 列表显示但点击运行报 403（正常 child-run-permission 行为）
- 不可见 → 列表不显示（visibility 层拦截）

#### 4.2.2 `ListVisibleChatbotsWithPermission` — 对称

### 4.3 `IsSopVisibleToUser` / `IsChatbotVisibleToUser` 的使用场景

**当前不在任何运行路径中调用**。它们是为了未来扩展（如直接通过 URL 进入 SOP run 页面时的二次校验）预留的判断函数。本功能的核心拦截在 §4.2 的列表过滤层。

S5 验证策略需要覆盖：直接访问 `/sop/run?template_id=X`（其中 X 是被限制可见的 SOP）的行为 — 当前实现下，子用户仍能开始运行（如果有运行权限）；这是符合 S0 决策的（"已开始的运行不受影响"）。但 S5 spec 要验证此行为符合预期。

---

## §5 子用户级联清理

### 5.1 现有删除路径

```
DELETE /v1/customers/sub-users/:user_id
→ controller.DeleteSubUser
→ biz.DeleteSubUser(ctx, callerID, subUserID)
→ store.WithTx:
    软删 user_template_permission WHERE sub_user_id=?
    物理删 user_chatbot_permission WHERE sub_user_id=?
    软删 user WHERE id=?
```

### 5.2 新事务序列

```
DELETE /v1/customers/sub-users/:user_id
→ controller.DeleteSubUser
→ biz.DeleteSubUser(ctx, callerID, subUserID)
→ store.WithTx:
    NEW: 软删 sop_visibility_grant WHERE sub_user_id=?
    NEW: 物理删 chatbot_visibility_grant WHERE sub_user_id=? 
         (或软删, 见 §5.4 决策)
    既有: 软删 user_template_permission WHERE sub_user_id=?
    既有: 物理删 user_chatbot_permission WHERE sub_user_id=?
    既有: 软删 user WHERE id=?
```

### 5.3 新增函数

```go
// CleanupVisibilityGrantsBySubUser 在事务中清理子用户的所有 visibility grant 记录.
// 必须在删除 user 之前调用 (虽然 FK 不强制级联, 但语义上先清子表).
// 幂等: 对不存在的 sub_user_id 无副作用.
func CleanupVisibilityGrantsBySubUser(ctx context.Context, tx *gorm.DB, subUserID uint) error {
    if err := tx.Where("sub_user_id=?", subUserID).Delete(&SopVisibilityGrant{}).Error; err != nil {
        return fmt.Errorf("cleanup sop visibility: %w", err)
    }
    if err := tx.Where("sub_user_id=?", subUserID).Delete(&ChatbotVisibilityGrant{}).Error; err != nil {
        return fmt.Errorf("cleanup chatbot visibility: %w", err)
    }
    return nil
}
```

### 5.4 软删 vs 物理删的选择（与 D7 一致）

**两张新表均启用软删除**（gorm.Model + DeletedAt）。理由：
- 与 `user_template_permission` 一致，便于审计"哪个子用户在哪个时间点失去过 visibility"
- `user_chatbot_permission` 用物理删除是历史决策，本 spec 不复制此差异

DeleteSubUser 路径中的 `Delete(&SopVisibilityGrant{})` 会自动转为软删（GORM 默认行为）。

### 5.5 不变量

| # | 不变量 | 强制方式 |
|---|--------|---------|
| I-7 | DeleteSubUser 完成后, 该子用户在 4 张权限表（含 2 visibility + 2 run-permission）中无未软删记录 | CleanupVisibilityGrantsBySubUser + 既有清理逻辑，事务原子性保证 |
| I-8 | 删除失败时，整个 DeleteSubUser 事务回滚（不留半成品状态） | `store.WithTx` 事务包装 |

---

## §6 前端架构

### 6.1 组件层级

```
SopTemplateEdit.vue (既有)
├── ... 既有内容 ...
└── VisibilityScopeCard.vue (新, 组件)
    └── SubUserMultiSelectDialog.vue (新, 跨页面复用)

ChatbotEdit.vue (S4 确认文件名)
├── ... 既有内容 ...
└── VisibilityScopeCard.vue (复用)
    └── SubUserMultiSelectDialog.vue (复用)
```

### 6.2 `SubUserMultiSelectDialog.vue` 规格

**文件位置**: `numind-web-v3/src/components/SubUserMultiSelectDialog.vue`

**Props**:
```ts
interface Props {
  modelValue: number[]        // v-model: 选中的 sub_user_ids
  visible: boolean            // v-model:visible 控制弹窗显示
  parentUserID?: number       // 可选; 不传则从 useUserStore() 推断
  searchable?: boolean        // 子用户数 > 10 时建议 true; 默认 true
  title?: string              // 弹窗标题; 默认 "选择子用户"
}

interface Emits {
  'update:modelValue': (val: number[]) => void
  'update:visible': (val: boolean) => void
  'confirm': (selectedIDs: number[]) => void
  'cancel': () => void
}
```

**内部行为**:
- 打开时调用 `getSubUsers()` 加载父账户名下子用户列表（缓存到 Pinia store）
- 显示每个子用户的 nickname + phone（脱敏后 4 位）
- 支持全选 / 反选 / 搜索（昵称或手机号模糊匹配）
- 确认时 emit `confirm` + `update:modelValue` + 关闭弹窗
- 取消时 emit `cancel` + 关闭弹窗（不更新 modelValue）

**4 状态处理**:
- loading（getSubUsers 进行中）: 显示 skeleton
- empty（父账户名下 0 子用户）: 显示"您还没有子用户，去客户管理添加" + CTA 跳转 `/customers`
- error: 显示错误 + 重试按钮
- success: 正常勾选列表

### 6.3 `VisibilityScopeCard.vue` 规格

**文件位置**: `numind-web-v3/src/components/VisibilityScopeCard.vue`

**Props**:
```ts
interface Props {
  modelValue: { restricted: boolean; subUserIDs: number[] }  // v-model
  entityType: 'sop' | 'chatbot'  // 决定文案 ("此 SOP" / "此智能体")
  disabled?: boolean             // 编辑权限不足时
}

interface Emits {
  'update:modelValue': (val: { restricted: boolean; subUserIDs: number[] }) => void
}
```

**UI 结构**:
```
卡片标题: 可见范围
开关: [仅指定子用户可见]  (默认关)
[当开关打开时显示]
  当前已选: N 位子用户  [选择子用户]
[当开关从开到关时]
  确认弹窗: "已配置 N 位子用户的名单将保留, 下次打开恢复. 仍要关闭吗?"
[当开关从关到开, 且 grant 数据非空时]
  顶部提示: "上次已配置 N 位子用户"  [保留并打开] [清空重选]
```

**短路 UX 处理**:
- 父账户名下 0 子用户：整个卡片 disabled + 提示"添加子用户后可设置可见范围"
- 通过 `useUserStore().hasSubUsers` 判断

### 6.4 Store 集成

**SOP 编辑页 store**（`useSopTemplateEditStore`，可能既有可能 S4 新建）：

```ts
const state = reactive({
  // ... 既有字段 ...
  visibilityRestricted: false,
  visibilitySubUserIDs: [] as number[],
  visibilityLoaded: false,  // 区分"未加载"和"加载后空"
})

async function loadVisibility(sopID: number) {
  const res = await getSopVisibility(sopID)
  state.visibilityRestricted = res.data.restricted
  state.visibilitySubUserIDs = res.data.sub_user_ids
  state.visibilityLoaded = true
}

async function saveVisibility(sopID: number) {
  await putSopVisibility(sopID, {
    restricted: state.visibilityRestricted,
    sub_user_ids: state.visibilityRestricted ? state.visibilitySubUserIDs : undefined,
  })
}
```

**chatbot 编辑页 store** 对称。

### 6.5 保存流程（编辑页主保存按钮）

**两阶段保存（顺序，错误隔离）**:
```
async function onSave() {
  try {
    await saveTemplate()     // 既有: PUT /v1/sop/templates/:id
  } catch (err) {
    toast.error("模板保存失败"); return
  }

  if (state.visibilityLoaded) {
    try {
      await saveVisibility(sopID)
    } catch (err) {
      // 模板已保存, visibility 失败 → 警告但不回滚
      toast.warning("模板已保存, 但可见范围更新失败: " + err.message)
      return
    }
  }

  toast.success("已保存")
  router.push("/sop/templates")
}
```

**为何不合并到 single PUT**: D2 决策（API 设计独立端点）。错误隔离 + 语义清晰。

### 6.6 API 层

**新文件**: `numind-web-v3/src/api/visibility.ts` (或合并到既有 `sop.ts` / `chatbot.ts`)

```ts
export interface VisibilityState {
  restricted: boolean
  sub_user_ids: number[]
}

export interface VisibilityUpdatePayload {
  restricted: boolean
  sub_user_ids?: number[]  // 仅 restricted=true 时使用
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

### 6.7 列表过滤

**纯后端强制**。前端 SOP 列表 / chatbot 列表 / 工作区 HomeView 的数据均来自 `GET /v1/sop/templates` 和 `GET /v1/chatbot/list`，后端在子用户身份时已过滤。前端**不做**客户端过滤，不读取 `visibility_restricted` 字段（即使返回了也忽略）。

---

## §7 Migration 脚本

### 7.1 Forward `20260513_120000_sop_chatbot_visibility_scope.sql`

```sql
-- +migrate Up

-- 1. sop_template 加 visibility_restricted 字段
ALTER TABLE sop_template
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '可见范围限制: 0=全部可见; 1=白名单模式';

-- 2. chatbot_config 加 visibility_restricted 字段
ALTER TABLE chatbot_config
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '可见范围限制: 0=全部可见; 1=白名单模式';

-- 3. 新表 sop_visibility_grant
CREATE TABLE IF NOT EXISTS sop_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL,
  sub_user_id BIGINT UNSIGNED NOT NULL,
  sop_template_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_svg_sub_template_unique (sub_user_id, sop_template_id, deleted_at),
  KEY idx_svg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_svg_template (sop_template_id),
  KEY idx_svg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='SOP 可见范围授权（白名单）';

-- 4. 新表 chatbot_visibility_grant
CREATE TABLE IF NOT EXISTS chatbot_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL,
  sub_user_id BIGINT UNSIGNED NOT NULL,
  chatbot_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_cvg_sub_chatbot_unique (sub_user_id, chatbot_id, deleted_at),
  KEY idx_cvg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_cvg_chatbot (chatbot_id),
  KEY idx_cvg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='Chatbot 可见范围授权（白名单）';
```

### 7.2 Rollback `20260513_120000_sop_chatbot_visibility_scope_rollback.sql`

```sql
-- +migrate Down

DROP TABLE IF EXISTS chatbot_visibility_grant;
DROP TABLE IF EXISTS sop_visibility_grant;
ALTER TABLE chatbot_config DROP COLUMN visibility_restricted;
ALTER TABLE sop_template DROP COLUMN visibility_restricted;
```

### 7.3 数据迁移诚实声明

**不需要数据迁移**。本功能为加字段 + 加表，默认值 `visibility_restricted=0` 意味着所有现有 SOP / chatbot 立刻处于"全部子用户可见"状态——这与现有行为完全一致，**零行为变化**。

子用户工作区列表查询的 visibility 过滤逻辑只在 `visibility_restricted=1` 时才查 grant 表，因此 migration 上线前后子用户体验无变化（直到父账户手动启用）。

### 7.4 上线顺序

无特殊上线顺序要求（与 `child-run-permission` 的"先 migration 再 code"不同）。原因：本功能默认 allow-all，即使 code 先部署而 migration 后跑，唯一异常是 `sop_template.visibility_restricted` 列缺失导致后端报错。但实际部署流程是 CI 在启动服务前跑 migration，所以这个理论风险不会发生。

---

## §8 验收标准追溯矩阵

PRD §4.2 的 22 条 AC → spec 章节映射：

| AC# | 描述 | Spec 覆盖 |
|-----|------|----------|
| AC-1 | 新增 2 张 grant 表 | §2.2, §2.3 |
| AC-2 | sop_template/chatbot_config 加 visibility_restricted | §2.1 |
| AC-3 | PUT /v1/sop/templates/:id/visibility | §3.3 |
| AC-4 | PUT /v1/chatbot/:id/visibility | §3.4 |
| AC-5 | GET /v1/sop/templates/:id/visibility | §3.2 |
| AC-6 | GET /v1/chatbot/:id/visibility | §3.4 |
| AC-7 | SOP 列表 visibility 过滤 | §4.2.1 |
| AC-8 | chatbot 列表 visibility 过滤 | §4.2.2 |
| AC-9 | 父账户列表不过滤 | §4.2.1 步骤 2 |
| AC-10 | DELETE 子用户级联清理 4 张表 | §5.2, §5.3 |
| AC-11 | 仅 owner 父账户能配置 | §4.1.6 + I-3 + §3.3 错误码 |
| AC-12 | 6 单元测试场景 | §10 验证策略 |
| AC-13 | SOP 编辑页加 VisibilityScopeCard | §6.1, §6.3 |
| AC-14 | chatbot 编辑页加 VisibilityScopeCard | §6.1, §6.3 |
| AC-15 | SubUserMultiSelectDialog 弹窗 | §6.2 |
| AC-16 | Confirm 保存 | §6.5 |
| AC-17 | 编辑页加载回显 | §6.4 loadVisibility |
| AC-18 | 开关从开到关确认提示 | §6.3 UI 结构 |
| AC-19 | 开关从关到开恢复历史 | §6.3 UI 结构 |
| AC-20 | 无子账户用户 UI 隐藏 | §6.3 短路 UX |
| AC-21 | 列表过滤纯后端 | §6.7 |
| AC-22 | 4 状态处理 | §6.2 4 状态 |

---

## §9 边界 case（PRD §4.3 → spec 实现细节）

| EC# | 场景 | 实现细节 |
|-----|------|---------|
| EC-1 | 选 3 子用户 → 删 1 子用户 → 弹窗只显示剩余 2 | §5.2 级联清理保证 grant 表内 sub_user_id 已不存在；§6.2 弹窗加载时 `getSubUsers()` 只返存活子用户 |
| EC-2 | 并发编辑同一 SOP | last-write-wins（不引入乐观锁）；ST2 全删全插事务保证最后一次 PUT 完全替换 |
| EC-3 | visibility_restricted=true 但 grant 0 条 | §2.5 I-2 允许；列表过滤将该 SOP 对所有子用户隐藏（白名单严格） |
| EC-4 | 提交不属于自己的 sub_user_id | §4.1.8 validateSubUsersBelongToCaller → ErrCrossParentSubUser |
| EC-5 | 已在 run 的 SOP, 父账户取消可见 | run 已有的 run_id 不撤回，列表入口隐藏；§4.3 已说明本功能不拦截 run path |
| EC-6 | 父账户删除 SOP / chatbot | **本 feature 内处理**（用户 2026-05-13 决策）：在既有 SOP/chatbot delete biz 中同事务调用 `CleanupSopVisibilityGrantsByEntity(tx, sopID)` / `CleanupChatbotVisibilityGrantsByEntity(tx, chatbotID)`，软删该实体的所有 grant 记录 |
| EC-7 | 编辑未保存退出 | 与现有编辑页一致，丢弃；§6.5 保存触发 PUT，未保存不持久化 |

**EC-6 实现细节**:
```go
// 新增 store 函数 (在 sop_visibility_grant.go 中)
func CleanupSopVisibilityGrantsByEntity(ctx context.Context, tx *gorm.DB, sopID uint) error {
    return tx.Where("sop_template_id=?", sopID).Delete(&SopVisibilityGrant{}).Error
}
// 对称: CleanupChatbotVisibilityGrantsByEntity

// 在既有 biz/sop.DeleteSopTemplate 的事务中调用 (S4 实施定位)
// 在既有 biz/chatbot.DeleteChatbot 的事务中调用 (S4 实施定位)
```

---

## §10 验证策略（S5 大纲，详细 task 在 S3 plan 末尾产出）

### 10.1 Playwright E2E（必需）

文件：`numind-web-v3/e2e/sop-chatbot-visibility-scope.spec.ts`

测试路径：
1. 父账户登录 → 进 SOP 编辑页 → 打开「仅指定子用户可见」开关 → 弹窗勾选 1 名子用户 A → 保存
2. 子用户 A 登录 → 工作区列表能看到该 SOP
3. 子用户 B（未勾选）登录 → 工作区列表**看不到**该 SOP
4. 父账户登录 → 取消勾选子用户 A → 保存
5. 子用户 A 重新登录 → 工作区列表**看不到**该 SOP
6. 父账户登录 → 关闭开关 → 保存 → 重新打开开关 → 弹窗中子用户 A 仍处于勾选状态（D3 保留语义）
7. chatbot 路径 1-6 对称重复

### 10.2 后端单元测试

文件：`numind-server/internal/numind/biz/sop/visibility_test.go` 等。

最少 12 个测试用例：
- 6 个场景（visibility 关闭 / 开启全选 / 开启部分选 / 开启零选 / 子用户级联清理 / 跨父账户越权配置）
- 4 象限矩阵（visible+allowed / visible+denied / hidden+allowed / hidden+denied）— 验证 §4.2 两层 gate 串行顺序
- 1 个并发 PUT 测试（验证 last-write-wins 不死锁）
- 1 个幂等测试（同一 PUT 连续 2 次，第二次无副作用）

### 10.3 gstack /qa 浏览器截图

4 个截图回归：
- SOP 模板编辑页 — VisibilityScopeCard 渲染
- 子用户工作区列表 — SOP 被过滤掉的状态
- chatbot 编辑页 — VisibilityScopeCard 渲染
- 子用户工作区列表 — chatbot 被过滤掉的状态

### 10.4 回归保护诚实声明

本功能涉及**权限主流程**（visibility × run-permission 两层 gate），**必须**有 Playwright E2E 保护，不接受仅靠 /qa 一次性验证。S3 plan 的 S5 验证策略 task 必须固化 E2E 实跑（NDF Rule 10）。

---

## §11 风险与缓解

| R# | 风险 | 概率 | 影响 | 缓解 |
|----|------|------|------|------|
| R1 | 两层 gate 串行顺序错位 (run-permission 先于 visibility) | 中 | 中 (语义反转, 但子用户最终看不到; 仍可能困惑) | §4.2.1 spec 显式锁顺序; 单元测试 4 象限矩阵覆盖 |
| R2 | 子用户级联清理失败 (4 表事务) | 低 | 高 (孤儿 grant 记录引用已删用户) | §5.3 单事务 + 幂等设计; CleanupVisibilityGrantsBySubUser 单元测试 |
| R3 | 列表查询性能退化 | 低 | 低 (本地 O(N) 过滤 + visibility_restricted 短路, 99% SOP 无需查 grant 表) | §4.2 短路逻辑; S5 性能验证可选 (本功能不在性能优化范围) |
| R4 | GORM `default:true` bool gotcha | 极低 | 低 (本字段 default:0, 不触发该 gotcha) | §2.1 显式说明; database.md §6 已有文档化 pattern |
| R5 | SubUserMultiSelectDialog 与既有 CustomersView 弹窗的耦合 | 低 | 低 (复用代码风格但不强依赖) | §6.2 独立组件设计; CustomersView 既有弹窗不动 |
| R6 | EC-6 实体删除时未清理 grant 表 | 中 | 低 (软删记录残留, 不影响行为) | S3 plan 决定是否在本 feature 内处理 |
| R7 | child-run-permission 现有逻辑被本功能引入的代码意外修改 | 中 | 高 (运行权限被弱化或加强) | S4 review 阶段必须验证既有 ListVisibleTemplatesWithPermission 测试不退化 |

---

## §12 不变量总清单

| # | 不变量 | 强制方式 | 测试覆盖 |
|---|--------|---------|---------|
| I-1 | visibility_restricted=0 时不查 grant 表 | §4.2 短路 | §10.2 |
| I-2 | visibility_restricted=1 + grant=0 → 白名单严格全拒 | §4.2 + §2.5 | §10.2 (开启零选) |
| I-3 | grant.parent_user_id == entity.owner | §4.1.6 校验 | §10.2 (越权测试) |
| I-4 | grant.parent_user_id == sub_user.parent_user_id | §4.1.8 校验 | §10.2 (越权测试) |
| I-5 | 软删的 grant 不计入白名单 | GORM DeletedAt | §10.2 |
| I-6 | (sub_user_id, entity_id, deleted_at IS NULL) 唯一 | DB 唯一索引 | §10.2 (重复授予测试) |
| I-7 | DeleteSubUser 后 4 张表无未软删记录 | §5.3 + 事务 | §10.2 (级联清理) |
| I-8 | DeleteSubUser 失败时整事务回滚 | store.WithTx | §10.2 |
| I-9 | visibility 过滤先于 run-permission 过滤 | §4.2 顺序固化 | §10.2 (4 象限) |
| I-10 | 父账户列表查询不应用 visibility 过滤 | §4.2.1 步骤 2 | §10.2 |

---

## §13 假设与遗留项

### 13.1 假设
1. chatbot 编辑页存在独立 Vue 文件（S4 实施时确认 `numind-web-v3/src/views/chatbot/` 下的编辑视图）
2. SOP 模板编辑页 store 既有或可新建（S4 实施时确认 store 文件位置）
3. `useUserStore().hasSubUsers` 已实现或可从 `getSubUsers()` 总数推断（S4 实施时确认）
4. `getSubUsers()` API（`GET /v1/customers/sub-users`）返回字段含 `nickname` 和 `phone`，符合 §6.2 弹窗渲染需求

### 13.2 遗留项（S3 plan 解决）
- ~~**EC-6**: 实体删除时是否清理 visibility 表~~ → **已 2026-05-13 锁定**：纳入本 feature，见 §9 EC-6 + §14 Phase 5
- **chatbot 编辑页文件名**: S4 实施时 grep 确认
- **store 与组件包结构最终命名**: S4 实施时按既有风格定型

### 13.3 不在本功能范围（明确遗留）
- 销售知识库的 visibility（S0 排除）
- Admin 端配置 visibility（S0 排除）
- 管理员查看任意 SOP 的 visibility 配置（admin 视角观察，未来扩展）
- 跨父账户的 SOP 分享 / 转让（产品不支持）

---

## §14 实施顺序建议（为 S3 plan 提供输入）

**Phase 1: 后端基础（schema + model + store）**
1. Migration（forward + rollback）
2. GORM model 文件
3. store 层 CRUD 方法

**Phase 2: 后端 biz**
4. UpdateSopVisibility / UpdateChatbotVisibility
5. GetSopVisibility / GetChatbotVisibility
6. ListSubUserVisibleSopIDs / ListSubUserVisibleChatbotIDs
7. IsSopVisibleToUser / IsChatbotVisibleToUser
8. CleanupVisibilityGrantsBySubUser + 接入既有 DeleteSubUser

**Phase 3: 后端 controller + router**
9. SOP visibility controller (GET/PUT)
10. chatbot visibility controller (GET/PUT)
11. router 注册

**Phase 4: 后端列表过滤接入**
12. ListVisibleTemplatesWithPermission 加 visibility 过滤层
13. ListVisibleChatbotsWithPermission 加 visibility 过滤层
14. 错误码定义集中加入

**Phase 5: 后端 EC-6 清理**（用户 2026-05-13 锁定纳入）
15. SOP delete biz 加 visibility 清理（CleanupSopVisibilityGrantsByEntity + 接入既有 DeleteSopTemplate 事务）
16. chatbot delete biz 加 visibility 清理（对称）

**Phase 6: 后端单元测试**
17. visibility biz 单元测试（12 用例）

**Phase 7: 前端基础组件**
18. SubUserMultiSelectDialog.vue
19. VisibilityScopeCard.vue
20. api/visibility.ts

**Phase 8: 前端编辑页接入**
21. SopTemplateEdit.vue 接入 VisibilityScopeCard
22. ChatbotEdit.vue 接入 VisibilityScopeCard
23. store 集成（SOP + chatbot）

**Phase 9: S5 验证策略**
24. Playwright E2E spec 编写
25. （末尾 task）NDF Rule 10 强制 S5 验证策略 task

预计 24-25 task。S3 plan 时可能合并/拆分。

---

## §15 涉及文件清单（先验）

### 后端 (numind-server)
| 文件 | 操作 |
|------|------|
| `migrations/20260513_120000_sop_chatbot_visibility_scope.sql` | 新建 |
| `migrations/20260513_120000_sop_chatbot_visibility_scope_rollback.sql` | 新建 |
| `internal/pkg/model/sop.go` | 修改 (加 VisibilityRestricted 字段) |
| `internal/pkg/model/chatbot.go` | 修改 (加 VisibilityRestricted 字段) |
| `internal/pkg/model/sop_visibility_grant.go` | 新建 |
| `internal/pkg/model/chatbot_visibility_grant.go` | 新建 |
| `internal/pkg/errno/code.go` | 修改 (加 4 个错误码) |
| `internal/numind/store/sop.go` 或 `sop_visibility_grant.go` | 修改/新建 |
| `internal/numind/store/chatbot.go` 或 `chatbot_visibility_grant.go` | 修改/新建 |
| `internal/numind/store/customer.go` | 修改 (加 CleanupVisibilityGrantsBySubUser) |
| `internal/numind/biz/sop/visibility.go` | 新建 (含 8 函数) |
| `internal/numind/biz/chatbot/visibility.go` | 新建 (对称 8 函数) |
| `internal/numind/biz/customer/customer.go` | 修改 (DeleteSubUser 调用 cleanup) |
| `internal/numind/biz/sop/sop.go` | 修改 (ListVisibleTemplatesWithPermission 加过滤层) |
| `internal/numind/biz/chatbot/chatbot.go` | 修改 (ListVisibleChatbotsWithPermission 加过滤层) |
| `internal/numind/controller/v1/sop/visibility.go` | 新建 (GetVisibility, UpdateVisibility) |
| `internal/numind/controller/v1/chatbot/visibility.go` | 新建 (对称) |
| `internal/numind/router.go` | 修改 (注册 4 端点) |
| 各 `_test.go` 文件 | 新建/修改 (12 单元测试用例) |

预计 ~16-18 后端文件改动。

### 前端 (numind-web-v3)
| 文件 | 操作 |
|------|------|
| `src/components/SubUserMultiSelectDialog.vue` | 新建 |
| `src/components/VisibilityScopeCard.vue` | 新建 |
| `src/api/visibility.ts`（或合并到 `sop.ts` / `chatbot.ts`） | 新建/修改 |
| `src/views/sop/SopTemplateEdit.vue`（S4 确认路径） | 修改 |
| `src/views/chatbot/ChatbotEdit.vue`（S4 确认路径） | 修改 |
| `src/stores/sopTemplateEdit.ts`（S4 确认） | 修改 |
| `src/stores/chatbotEdit.ts`（S4 确认） | 修改 |
| `e2e/sop-chatbot-visibility-scope.spec.ts` | 新建 |

预计 ~7-9 前端文件改动。

**总计**：23-27 文件。与 S1 估算 "14-18 文件" 略超出，主要是测试文件单独拆分。

---

## §16 准入清单（S2 gate 自检）

NDF S2 gate 要求 (§3 S2):
- [x] spec 覆盖 PRD 全部用户故事和验收标准 — §8 追溯矩阵完整
- [x] 多仓库 API 契约已定义 — §3 完整端点契约
- [x] AI 功能 trace topology — 不涉及 LLM 调用，N/A（§3 PRD §3 已声明）
- [ ] 人类确认设计方向 — **本 spec 提交审阅**

剩余两个 S2 gate 要求：spec 自检（下文）+ 人类审阅。

### 16.1 Spec 自检

| 检查项 | 状态 |
|--------|------|
| 占位符扫描（TBD / TODO / 不完整段落） | 通过（§13.2 有遗留项但已显式标注） |
| 内部一致性（架构 vs 功能描述无矛盾） | 通过（§4 + §6 串行 gate 与 §8 AC 矩阵一致） |
| 范围检查（适合单个 implementation plan） | 通过（24-25 task 与 membership-credits-redesign 24 task 同量级） |
| 歧义检查（每条 AC 单义） | 通过（§3 字段约束 + §5 事务序列 + §9 EC 全部具体化） |
