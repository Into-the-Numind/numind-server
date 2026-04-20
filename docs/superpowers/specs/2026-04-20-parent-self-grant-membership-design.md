# 父账户自助开通会员 — 技术 Spec

- **Feature ID**: parent-self-grant-membership
- **NDF Stage**: S2 (Technical Design)
- **Created**: 2026-04-20
- **Supersedes PRD**: `numind-server/proposals/parent-self-grant-membership-proposal.md`

## §1 目标与边界

### 目标

父账户（`parent_user_id IS NULL`）可以在用户端"客户管理"页面像管理子账户一样为**自己**开通会员，不走支付、不扫码，后台完整记录供月末 B2B 对公结算按 `granter_user_id` 自动聚合。

### 明确边界（不做的事）

- 不新增 API 端点
- 不新增数据库字段 / migration
- 不改 payment 链路（`POST /v1/orders` 的 `ErrMembershipSelfPurchaseDisabled` 保持不变）
- 不改 admin `/v1/admin/users/:id/tier` 残废接口（另 hotfix）
- 不改前端任何代码（S1 PRD 的 "我" 标识已废弃，见 §8 决策演进）
- 不改 B2B 报表 SQL（现有按 `granter_user_id` 聚合自动生效；明细 CASE 作独立增强需求）

## §2 架构概览

```
前端                               后端
CustomersView.vue (零改动)         router.go (零改动)
  v-for SubUser in list    ←──    └─ ListSubUsers handler
  ├─ 自然渲染自己行               customer_biz.ListSubUsers (零改动)
  ├─ handleMenuGrantMembership   └─ store/customer.go
  └─ tier-dialog (零改动)            ListSubUsers WHERE OR id=:parent
                                     ORDER BY CASE 置顶自己

CustomersView.vue                   parent_grant controller (零改动)
  submitGrant → grantChildMembership ─→ credit/grant_membership.go
                                        ├─ validateGrantProductType (零改动)
                                        ├─ verify parent-child (【改动】加 self-grant 分支)
                                        ├─ anti-duplicate (零改动)
                                        └─ tx: credit_package + action_log (零改动)
```

**总改动面**：2 个后端文件 + 对应单测。前端零改动。

## §3 详细设计

### §3.1 Backend biz — `grant_membership.go`

**文件**：`numind-server/internal/numind/biz/credit/grant_membership.go`
**改动行**：79-81

**改动前**：

```go
// Step 2: verify parent-child relationship (spec Q1: child must belong to caller)
child, err := b.ds.Users().GetByID(ctx, req.ChildUserID)
if err != nil {
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return fmt.Errorf("%w: child_id=%d", ErrGrantChildNotFound, req.ChildUserID)
    }
    return fmt.Errorf("GrantMembership: get child user %d: %w", req.ChildUserID, err)
}
if child.ParentUserID == nil || *child.ParentUserID != req.ParentUserID {
    return fmt.Errorf("%w: child=%d parent=%d", ErrGrantForbidden, req.ChildUserID, req.ParentUserID)
}
```

**改动后**：

```go
// Step 2: verify parent-child relationship or self-grant authorization
child, err := b.ds.Users().GetByID(ctx, req.ChildUserID)
if err != nil {
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return fmt.Errorf("%w: child_id=%d", ErrGrantChildNotFound, req.ChildUserID)
    }
    return fmt.Errorf("GrantMembership: get child user %d: %w", req.ChildUserID, err)
}
if req.ChildUserID == req.ParentUserID {
    // Self-grant: 仅允许父账户（parent_user_id IS NULL）给自己开通。
    // 子账户 (parent_user_id != NULL) 禁止自开通，防越权。
    if child.ParentUserID != nil {
        return fmt.Errorf("%w: caller=%d is a sub-user, self-grant only allowed for parent accounts",
            ErrGrantForbidden, req.ParentUserID)
    }
    // 放行：父账户 self-grant
} else {
    // Delegate-grant: 目标必须是 caller 的子账户
    if child.ParentUserID == nil || *child.ParentUserID != req.ParentUserID {
        return fmt.Errorf("%w: child=%d parent=%d", ErrGrantForbidden, req.ChildUserID, req.ParentUserID)
    }
}
```

**其它逻辑保持不变**：

- Step 3 anti-duplicate 检查（`HasTrialPackage` / `HasActiveSubscription`）对 `ChildUserID` 执行——self-grant 时等于对父账户自己执行，语义天然正确
- Step 4 `GetOrCreateAccount(ctx, ChildUserID)` 为父账户创建 credit_account（若不存在）
- Step 5 事务内：
  - 创建 `credit_package`（`UserID = ChildUserID = 父ID`, `GranterUserID = ParentUserID = 父ID`, `GrantSource = 'b2b_grant'`, `OrderID = NULL`）
  - `UpdateBalance` 更新父账户的 credit_account 余额
  - `billing_mode legacy_tier → credits` 切换条件 `WHERE id = ChildUserID AND billing_mode='legacy_tier'`（对父账户同样适用）
  - `action_log` 写入：`UserID = ParentUserID`（操作者=父）, `TargetID = ChildUserID`（被操作者=父）, `Action = 'grant_membership'`

**self-grant 时 `action_log` 的语义**：`user_id == target_id == 父ID`，detail JSON 含 `product_type/months/reason/package_ids`。审计时可通过 `user_id == target_id` 识别 self-grant 记录。

### §3.2 Backend store — `store/customer.go`

**文件**：`numind-server/internal/numind/store/customer.go`
**改动函数**：`ListSubUsers`（第 60-75 行）

**改动前**：

```go
query := c.db.WithContext(ctx).Model(&model.User{}).Where("parent_user_id = ?", parentUserID)

if err := query.Count(&total).Error; err != nil {
    return nil, 0, err
}

if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&users).Error; err != nil {
    return nil, 0, err
}
```

**改动后**：

```go
query := c.db.WithContext(ctx).Model(&model.User{}).
    Where("parent_user_id = ? OR id = ?", parentUserID, parentUserID)

if err := query.Count(&total).Error; err != nil {
    return nil, 0, err
}

// 父账户自己永远置顶；其它子账户按 created_at DESC（保持原有顺序）
orderClause := fmt.Sprintf("CASE WHEN id = %d THEN 0 ELSE 1 END, created_at DESC", parentUserID)
if err := query.Offset(offset).Limit(limit).Order(orderClause).Find(&users).Error; err != nil {
    return nil, 0, err
}
```

**注意**：
- `parentUserID` 是 `uint` 类型，`fmt.Sprintf` 拼接安全（无 SQL 注入风险，类型不可被字符串污染）
- 或改为 `Order("CASE WHEN id = ? THEN 0 ELSE 1 END, created_at DESC").Order(...)`——但 GORM `Order` 的参数化写法需验证，**保守选 `Sprintf` + 类型保证**
- `total` 计数包含父自己，前端分页 total 自动正确

### §3.3 Frontend — `CustomersView.vue`

**改动**：**无。零代码改动。**

**依据**：
- `ListSubUsers` 返回 `[]model.User`，父账户也是 User 记录，天然符合现有 `SubUser` 类型
- v-for 循环自动渲染多出的一行
- action 菜单的展开、点击逻辑与行内容无耦合
- `handleMenuGrantMembership(user)` 和 `submitGrant()` 用 `user.user_id ?? user.id` 作为 `childId` 调用 `grantChildMembership`，API 层面父自开和给子开一致
- Toast 文案 `已为 ${nickname} 开通 ${label}` 对自己行显示"已为 {自己昵称} 开通..."，用户已确认保留原样

### §3.4 API 契约

#### `POST /v1/users/children/:child_id/grant-membership`

**契约不变**。语义扩展：

| 场景 | `child_id` | caller `parent_user_id` | 后端行为 |
|------|-----------|------------------------|---------|
| 父自开 | `== caller.id` | `IS NULL` | ✅ 放行 |
| 父给子开 | `!= caller.id`, `child.parent_user_id == caller.id` | `IS NULL` | ✅ 放行（原行为） |
| 子自开 | `== caller.id` | `!= NULL` | ❌ `ErrGrantForbidden` (403) |
| 子给别人开 | `!= caller.id` | `!= NULL` | ❌ `ErrGrantForbidden` (403) |
| 父 A 给父 B 开 | `== B.id`, `B.parent_user_id IS NULL` | `IS NULL`（A） | ❌ `ErrGrantForbidden` (403) — 原校验 `child.parent_user_id == caller.id` 失败 |
| child_id 不存在 | — | — | ❌ `ErrGrantChildNotFound` (404) |

**防越权证明**（穷举父 A 给父 B 的场景）：
- `ChildUserID(B) != ParentUserID(A)` → 走 else 分支
- `child.ParentUserID == nil`（B 是父账户） → `child.ParentUserID == nil` 命中，返回 `ErrGrantForbidden` ✅

#### `GET /v1/customers/sub-users`

**契约不变**。响应扩展：

- `data.list` 首行为父账户自己（当 caller 是父账户时）
- `data.total` 含父自己
- 响应字段无新增（纯 `User` 数组，前端无需识别 `is_self`）

### §3.5 数据库

**零 schema 变更**。

写入表：`credit_package`, `action_log`, `credit_account`（已有 schema 支持 self-grant 语义）。

**字段语义**（self-grant 时）：

```sql
-- credit_package
user_id         = 父ID (grantee)
granter_user_id = 父ID (granter，与 user_id 相同)
grant_source    = 'b2b_grant'
order_id        = NULL
type            = 'trial' | 'subscription'
total_credits   = 200 (trial) | 2000 (monthly, per month)
remain_credits  = total_credits
activated_at    = now (trial/first-month) | now + N months (subsequent)
expires_at      = activated_at + 3 days (trial) | + 1 month (monthly)
status          = 'active' (first) | 'pending' (subsequent months)

-- action_log
user_id    = 父ID (操作者)
target     = 'user'
target_id  = 父ID (被操作者，self-grant 时与 user_id 相同)
action     = 'grant_membership'
detail     = '{"product_type":"...","months":N,"reason":"...","package_ids":[...]}'
```

**月末 B2B 报表**（`GET /v1/admin/b2b-billing-report?month=YYYY-MM`）自动归属：

```sql
-- 现有报表 SQL（简化示意）
SELECT granter_user_id, ... 
FROM credit_package 
WHERE grant_source='b2b_grant' 
  AND activated_at BETWEEN ... AND ...
GROUP BY granter_user_id
```

self-grant 记录 `granter_user_id = 父ID`，与父给子开通的记录一起归入父账户（B 端客户）名下汇总。

## §4 越权防线（S1 三条硬覆盖）

| # | 威胁 | 防线 | 测试 |
|---|------|------|------|
| 1 | 子账户自开通 | `grant_membership.go` self-grant 分支判断 `child.ParentUserID != nil` | `TestGrantMembership_SubUserSelfGrant_Rejected` |
| 2 | 父 A 跨父开通给 B | `grant_membership.go` else 分支原有 `child.ParentUserID == nil` 命中拒绝 | `TestGrantMembership_CrossParentGrant_Rejected` |
| 3 | 无效 child_id | `GetByID` 返回 `ErrGrantChildNotFound`（HTTP 404） | `TestGrantMembership_ChildNotExists_Rejected`（既有，无需新增） |

**额外覆盖**：
- 子账户尝试给其父账户开通（`ChildID != ParentID`）→ 走 else 分支，`child.ParentUserID == parent_id(of child caller)`，不等于 `ParentUserID(= sub-user's id)` → 拒绝 ✅
- 子账户尝试给兄弟子账户开通 → 同上，拒绝 ✅

## §5 测试策略

### §5.1 单元测试扩展（`grant_membership_test.go`）

**新增 7 个 case**：

1. **`TestGrantMembership_SelfGrant_Trial_Success`**
   - Arrange: 父账户 P (parent_user_id=NULL)，无既有 trial
   - Act: `GrantMembership({ParentUserID: P.id, ChildUserID: P.id, ProductType: "trial"})`
   - Assert:
     - no error
     - `credit_package` 有 1 行：`user_id=P.id, granter_user_id=P.id, grant_source='b2b_grant', type='trial', total_credits=200, status='active'`
     - `credit_account.balance` 增加 200
     - `action_log` 有 1 行：`user_id=P.id, target_id=P.id, action='grant_membership', detail.product_type='trial'`

2. **`TestGrantMembership_SelfGrant_Monthly_ThreeMonths_CreatesThreePackages`**
   - Arrange: 父账户 P，无既有 subscription
   - Act: `GrantMembership({ParentUserID: P.id, ChildUserID: P.id, ProductType: "monthly", Months: 3})`
   - Assert: 3 行 credit_package，第一行 status='active' 另两行 status='pending'，`granter_user_id=P.id` 均一致

3. **`TestGrantMembership_SubUserSelfGrant_Rejected`**
   - Arrange: 子账户 C (parent_user_id=P.id)
   - Act: `GrantMembership({ParentUserID: C.id, ChildUserID: C.id, ProductType: "trial"})`
   - Assert: 错误为 `ErrGrantForbidden`

4. **`TestGrantMembership_CrossParentGrant_Rejected`**（对应越权防线 #2）
   - Arrange: 父账户 A (parent_user_id=NULL) 和父账户 B (parent_user_id=NULL)
   - Act: `GrantMembership({ParentUserID: A.id, ChildUserID: B.id, ProductType: "trial"})`
   - Assert: 错误为 `ErrGrantForbidden`

5. **`TestGrantMembership_SelfGrant_BillingModeSwitch`**
   - Arrange: 父账户 P，`billing_mode='legacy_tier'`
   - Act: `GrantMembership({ParentUserID: P.id, ChildUserID: P.id, ProductType: "trial"})`
   - Assert: P 的 `billing_mode` 已切换到 `'credits'`

6. **`TestGrantMembership_SelfGrant_TrialAlreadyPurchased_Rejected`**
   - Arrange: 父账户 P 已有 trial credit_package
   - Act: `GrantMembership({ParentUserID: P.id, ChildUserID: P.id, ProductType: "trial"})`
   - Assert: 错误为 `ErrGrantTrialAlreadyPurchased`

7. **`TestGrantMembership_SelfGrant_ActiveSubscription_Rejected`**
   - Arrange: 父账户 P 有在期 subscription
   - Act: `GrantMembership({ParentUserID: P.id, ChildUserID: P.id, ProductType: "monthly", Months: 1})`
   - Assert: 错误为 `ErrGrantActiveSubscription`

### §5.2 Store 测试（`store/customer_test.go` 如有；若无则创建）

**新增 1 个 case**：

- **`TestListSubUsers_IncludesParentSelf_Ordered`**
  - Arrange: 父账户 P，子账户 C1 (created_at 较早), C2 (created_at 较晚)，以及不相关的父账户 X 和其子账户 Y
  - Act: `ListSubUsers(ctx, P.id, 0, 10)`
  - Assert:
    - `total == 3`（P, C1, C2）
    - `users[0].id == P.id`（父自己置顶）
    - `users[1].id == C2.id`（created_at DESC）
    - `users[2].id == C1.id`
    - 不包含 X, Y

### §5.3 S5 验证策略（NDF §10 规则）

**选择：Playwright E2E**

**理由**：
- 涉及会员开通 + 权限放开，属"高风险业务逻辑"（NDF §10 明确要求写 Playwright）
- 需要覆盖前端渲染 + API 调用 + 后端持久化的完整链路
- 未来修改（如 UI 重构、API 版本升级）需要自动回归保护
- 非 gstack `/qa` 一次性验证（单次截图无回归保护）

**E2E 测试文件**：`numind-web-v3/e2e/parent-self-grant.spec.ts`

**关键路径**：

1. **`parent-can-see-self-in-customer-list`**
   - 父账户登录 → 访问 `/customers`
   - 断言：列表第一行是父账户自己（匹配 nickname/username）

2. **`parent-self-grant-trial-success`**
   - 父账户（未曾开过 trial）登录 → `/customers` → 点击自己行 → action 菜单 → "帮升级会员" → 弹窗选 trial → 确认
   - 断言：toast 成功，列表刷新后自己行显示会员状态

3. **`parent-grant-child-regression`**（回归保护）
   - 父账户登录 → `/customers` → 点击子账户行 → "帮升级会员" → 弹窗选 monthly 1 月 → 确认
   - 断言：toast 成功，子账户行显示会员状态
   - （确保本次改动未破坏原有子账户开通功能）

## §6 观测性

**LLM 调用**：本功能完全不涉及 LLM。`.claude/rules/ai-service.md` 的 Langfuse 追踪要求不适用。

**应用日志**（现有）：
- `grant_membership.go:177-183` 已有 `log.Infow("B2B membership granted", ...)`，self-grant 时 `parent_user_id == child_user_id`，日志语义仍然正确

**审计追溯**：`action_log` 表为权威审计源，`user_id == target_id` 的记录可识别 self-grant。

## §7 回滚计划

**触发条件**：
- self-grant 后出现数据异常（如越权、重复开通）
- B2B 报表错误归属

**回滚步骤**：

1. `git revert` merge commit
2. 受影响数据的补救：
   - 清理错误写入的 `credit_package`：`DELETE FROM credit_package WHERE user_id = granter_user_id AND grant_source='b2b_grant' AND created_at > <deploy_time>` —— 仅当确认无合法 self-grant 时执行
   - 回滚受影响父账户的 `credit_account.balance`
3. 通知已开通的父账户

**不提供**专门的 data migration rollback：本改动无 schema 变更。

## §8 决策演进

| 决策 | 状态 | 原因 |
|------|------|------|
| S1 PRD 提到"父自己行加'我'标识" | **废弃** | 用户反问"如果不需要额外样式，是不是就不需要区分是不是自己？"。前端零改动更简洁，后端 API 响应无需新增 `is_self` flag |
| 列表排序方式 | **置顶自己** | `ORDER BY CASE WHEN id = :parent_id THEN 0 ELSE 1 END, created_at DESC`；父自己永远第一行 |
| API 响应结构 | **不变** | 纯 `User` 数组；前端不区分 self |
| admin 残废接口修复 | **不在本 spec 范围** | 独立 hotfix |

## §9 与 PRD 验收标准的对照

参照 `numind-server/proposals/parent-self-grant-membership-proposal.md` §4 验收标准：

| PRD 验收项 | Spec 覆盖位置 |
|-----------|--------------|
| 父账户登录后自己行出现在列表 | §3.2 WHERE OR id=:parent + §5.2 测试 |
| 自己行的昵称旁显示 **"我"** 标识 | **废弃**（§8 决策演进） |
| 自己行的 action 菜单"帮升级会员"可点击 | §3.3 前端零改动，v-for 自然渲染 |
| 点击打开同一弹窗 | §3.3 复用 |
| 确认调用 `POST /v1/users/children/:child_id/grant-membership` | §3.4 API 契约 |
| 成功后 toast + 列表刷新 | §3.3 现有 `submitGrant` 逻辑 |
| 不出现支付二维码 | §3.4 grant 路径不走 payment |
| 后端允许 `child_id == parent.id` self-grant | §3.1 改动 |
| credit_package 字段 | §3.5 字段语义 |
| action_log 字段 | §3.5 字段语义 |
| sub-users 返回含父自己 | §3.2 改动 |
| trial 终身一次 | §5.1 case #6 |
| monthly 在期不可重开 | §5.1 case #7 |
| legacy_tier → credits 切换 | §5.1 case #5 |
| 子账户不能自开 | §4 防线 #1 + §5.1 case #3 |
| 父 A 不能跨父开 B | §4 防线 #2 + §5.1 case #4 |
| child_id 不存在 404 | §4 防线 #3 + 既有 test |
| B2B 报表自动聚合 self-grant | §3.5 SQL 语义 |

**全部 17 项 PRD 验收点在 spec 中有对应章节**（1 项已作为决策演进废弃并记录理由）。
