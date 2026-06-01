# 父账户自助费用对账页面 — 技术设计 Spec

> NDF S2 工件。Feature: `parent-billing-report`（Standard）。
> 上游：`requirements/parent-billing-report.md`（S0）、`proposals/parent-billing-report-proposal.md`（S1）。
> 仓库：numind-server + numind-web-v3。日期：2026-06-01。

## 1. 概述

为**父账户**（`User.ParentUserID == nil`）在用户端「客户管理」页内提供一个**按月**的自助费用对账内页：列出当前登录父账户在所选月份给名下所有子账号开通的会员明细（子账号 / 产品类型 / 月数 / 价格 / 开通时间）+ 本月合计金额。

这是管理端 `GET /v1/admin/b2b-billing-report?month=YYYY-MM`（按所有父账户聚合，财务对公结算）的**父账户自助版**：同一数据源、同一金额归因内核，作用域收窄到「当前登录父账户自己」。

**核心设计原则：单一真相源。** 父账户视图与 admin 结算版**复用同一 `computeBilling` 内核**，绝不复制 Rule A/B/trial 逻辑，保证两端金额口径永不漂移。

## 2. 架构决策（已确认）

| # | 决策 | 选择 | 理由 |
|---|------|------|------|
| D1 | 后端作用域方式 | **参数化复用 `computeBilling`**（方案 A）| 单一真相源；作用域在 SQL 层收窄，不加载他人数据；改动最小 |
| D2 | 端点 | 新增**用户端** `GET /v1/users/me/billing-report`（user_token）| admin 端点是 admin_token 作用域，C 端访问不到——本功能存在的根本原因 |
| D3 | 父账户 id 来源 | 仅 `c.GetUint("userID")` | 禁止客户端传参指定 parent id，杜绝越权 |
| D4 | 非父账户访问 | **403 ErrPermissionDenied** | 功能语义上父账户专属，比返回空更诚实（用户确认）|
| D5 | 时间范围 | 单月（`?month=YYYY-MM`，默认当月）| 用户确认，与 admin 口径一致，不做多月列表 |
| D6 | 入口/页面形态 | **子路由 `/customers/billing` 内页**，从客户管理页页面级按钮进入 | 用户确认；账单数据适合独立视图；不新增顶级导航 |
| D7 | booster 加量包 | **排除** | 与结算口径一致（子账户自购，不由父账户承担）|
| D8 | 视图粒度 | **父账户级整月汇总**（跨所有子账号 + 合计）| 对账场景；非逐子账号 action |

## 3. API 契约（多仓库硬要求 — 后端按此实现，前端按此调用）

### 3.1 请求

```
GET /v1/users/me/billing-report?month=YYYY-MM
Authorization: Bearer <user_token>     # authGroup，user_token 中间件
Query:
  month   string  必填  严格 YYYY-MM（零填充月，复用 biz parseMonth 正则校验）
```

父账户 id 由中间件注入的 `c.GetUint("userID")` 决定，**不出现在请求参数中**。

### 3.2 成功响应（200）

```jsonc
{
  "code": 0,
  "message": "ok",
  "data": {
    "month": "2026-06",
    "parent_user_id": 12,
    "grants_count": 3,
    "total_amount_cents": 30690,
    "details": [
      {
        "child_user_id": 34,
        "child_username": "张三",
        "product_type": "monthly",   // trial | monthly
        "months": 3,                  // trial 为 0（前端展示"3 天"）
        "amount_cents": 29700,        // 后端 PriceForMonths 重算，前端不二次计算
        "granted_at": "2026-06-03T08:00:00Z"
      },
      {
        "child_user_id": 56,
        "child_username": "李四",
        "product_type": "trial",
        "months": 0,
        "amount_cents": 990,
        "granted_at": "2026-06-12T08:00:00Z"
      }
    ]
  }
}
```

- `details` 按 `granted_at` 升序（沿用 biz 现有 stable sort）。
- 金额单位 cents（分）；前端统一 ÷100 以 ¥ 元展示。

### 3.3 错误响应

| 场景 | errno | HTTP | message |
|------|-------|------|---------|
| month 缺失 | `ErrBind` | 400 | "month 参数必填，格式 YYYY-MM" |
| month 格式非法 | `ErrBind`（biz parseMonth 返回 err，controller 映射）| 400 | 友好格式提示 |
| 非父账户（子账号）访问 | 见下方 errno 说明 | 403 | "仅父账户可查看费用对账" |
| 该月无任何开通 | — | 200 | `details:[]`, `total_amount_cents:0`, `grants_count:0`（空，非错误）|
| 内部错误 | `InternalServerError` | 500 | err.Error() |

> **errno 选用（已核实）**：现有 `errno.ErrForbidden`（403, code.go:29）可直接复用。项目已有先例 `errno.ErrVisibilityPermissionDenied`（code.go:46，注释明确「子用户尝试调用父账户专属端点」，并刻意与 ErrForbidden 区分以便滥用单独告警）——本端点是**完全类比**的父账户专属端点。
> **默认**：复用 `ErrForbidden`（最小改动）。**可选**（S3 决定）：按 `ErrVisibilityPermissionDenied` 先例新增专用 `ErrBillingReportPermissionDenied` 便于监控。两者皆非「ad-hoc 临造码」，符合 errno 规范。

## 4. 后端设计（numind-server）

### 4.1 biz 层 — `internal/numind/biz/b2b_billing/b2b_billing.go`

**(a) `computeBilling` 增加可选 granter 过滤**

```go
// granterUserID: nil = 所有父账户（admin 结算版，行为不变）；
//                &id = 仅该父账户（父账户自助版）。
func (b *b2bBillingBiz) computeBilling(ctx context.Context, start, end time.Time, granterUserID *uint) ([]grantEvent, error)
```

三处 SQL `Where`（subsA / subsB / trials）在 `granterUserID != nil` 时各追加 `AND granter_user_id = ?`。现有调用 `GetBillingReport` 改为传 `nil`（零行为变化）。

**(b) 新增 slim 响应结构**

```go
// ParentBillingReport 是单个父账户的月度对账视图（父账户自助版）。
type ParentBillingReport struct {
    Month            string        `json:"month"`
    ParentUserID     uint          `json:"parent_user_id"`
    GrantsCount      int           `json:"grants_count"`
    TotalAmountCents int64         `json:"total_amount_cents"`
    Details          []GrantDetail `json:"details"`   // 复用现有 GrantDetail
}
```

**(c) 接口新增方法**

```go
type IB2BBillingBiz interface {
    GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error)            // admin（不变）
    GetBillingReportForParent(ctx context.Context, month string, parentUserID uint) (*ParentBillingReport, error)  // 新增
}
```

`GetBillingReportForParent` 实现：`parseMonth` → `computeBilling(ctx, start, end, &parentUserID)` → 组装 `ParentBillingReport`（username 查询复用 buildReport 内的 lookup 逻辑，或抽一个共用小函数）。空事件时返回 `Details:[]`、合计 0。

> 复用注意：`GetBillingReport`（admin）内部 `buildReport` 仍按 ByParent 分组；父账户版只需 Details 扁平 + 合计，可直接在 events 上聚合，无需 by-parent 分组。username lookup 逻辑（buildReport 中的 user IN 查询）可抽成 `lookupUsernames(ctx, ids)` 共用，避免重复。

### 4.2 controller — `internal/numind/controller/v1/`（新文件）

镜像 `admin_b2b/billing_report.go`，但：
1. `userID := c.GetUint("userID")`（auth 上下文）
2. 校验父账户：查 `User`，`ParentUserID != nil` → `core.WriteResponse(c, errno.ErrPermissionDenied..., nil)` 返回（D4）
3. `month := c.Query("month")` 必填校验
4. 调 `biz.GetBillingReportForParent(c, month, userID)` → `core.WriteResponse(c, nil, report)`

> 父账户校验（已核实可行）：通过 `ds.Users().GetByID(ctx, parentUserID)`（store/user.go:53）拿 User 记录，判 `ParentUserID == nil`（沿用 `biz/credit` 的 `isParentAccount` 语义）。**定稿：biz 层校验**——在 `GetBillingReportForParent` 内做父账户判定，非父账户返回包级 sentinel error（如 `ErrNotParentAccount`）；controller 用 `errors.Is` 映射为 403 `ErrForbidden`（业务规则归 biz，符合分层规范）。

### 4.3 路由 — `internal/numind/router.go`

在 `authGroup` 下注册（紧邻现有 `/users/children*` 系列）：

```go
authGroup.GET("/users/me/billing-report", parentBillingCtrl.GetMyBillingReport)
```

controller 构造：复用 `b2b_billing.New(store.S)`（或 `NewWithCutover`，与 admin 一致）。

### 4.4 后端测试（biz 单测，go test）

| 测试 | 断言 |
|------|------|
| `TestGetBillingReportForParent_ScopedToParent` | 两个父账户各有 grant，A 只看到 A 的，看不到 B 的（**越权隔离**）|
| `TestGetBillingReportForParent_AmountMatchesAdmin` | 单父账户合计 == admin `GetBillingReport` 该父账户 `ByParent` row 合计（**口径一致**）|
| `TestGetBillingReportForParent_EmptyMonth` | 无 grant → `Details:[]`, total 0, grants 0，不报错 |
| `TestGetBillingReportForParent_TrialAndMonthly` | trial(¥9.9) + monthly(N×¥99 / 年付¥949) 金额正确，booster 不出现 |
| `TestGetBillingReportForParent_InvalidMonth` | 非法 month → err |
| `TestComputeBilling_NilFilterUnchanged` | granterUserID=nil 时结果与重构前一致（admin 回归保护）|

非父账户 403 校验在 controller/biz 层，由 biz sentinel error 单测 + S5 E2E 覆盖。

## 5. 前端设计（numind-web-v3）

### 5.1 路由 — `src/router/index.ts`

```ts
{
  path: '/customers/billing',
  name: 'customers-billing',
  component: () => import('@/views/CustomersBillingView.vue'),
  meta: { title: '费用对账', requiresAuth: true, parentOnly: true },
}
```

### 5.2 内页 — `src/views/CustomersBillingView.vue`（新）

- **月份选择器**：默认当月；不允许选未来月。优先原生 `<input type="month">`（最轻，符合禁用外部 UI 框架硬规则）；如样式需统一则自研简单 select。
- **明细表**：DataTable 风格（沿用 CustomersView 表格样式）。列：子账号 / 会员类型 / 时长（monthly 显示「N 个月」，trial 显示「3 天」）/ 金额（¥）/ 开通时间。
- **合计**：底部合计行或卡片，展示本月合计 ¥。
- **四状态**（ui-ux.md 硬规则 #2）：loading（skeleton/spinner）/ empty（空状态卡片 + 文案 + 合计 ¥0）/ error（含 retry）/ success。
- 返回客户管理的导航（面包屑/返回按钮）。

### 5.3 入口 — `src/views/CustomersView.vue`

在 hero / 工具栏区域加**页面级**按钮「费用对账」→ `router.push('/customers/billing')`（**非**逐行 action 菜单项）。

### 5.4 API 层 — `src/api/parent.ts`

```ts
export interface ParentBillingDetail {
  child_user_id: number
  child_username: string
  product_type: 'trial' | 'monthly'
  months: number
  amount_cents: number
  granted_at: string
}
export interface ParentBillingReport {
  month: string
  parent_user_id: number
  grants_count: number
  total_amount_cents: number
  details: ParentBillingDetail[]
}
export const getParentBillingReport = (month: string) =>
  request.get<ApiResponse<ParentBillingReport>>('/v1/users/me/billing-report', { params: { month } })
```

所有请求经 `src/api/request.ts`（frontend-state.md 硬规则）。

### 5.5 状态管理

内页 local state（`ref`）即可，不新建 Pinia store（沿用 CustomersView 局部状态模式）。

## 6. 安全 / 权限（首要风险）

- **越权防护**：父账户 id 只来自 auth 上下文；biz SQL 强制 `granter_user_id = <authUserID>`。客户端无法指定他人 id。
- **父账户限定**：后端 biz/controller 校验 `ParentUserID == nil`，非父账户 403；前端路由 `parentOnly` guard 双重保证（前端 guard 仅 UX，后端是真正屏障）。
- 无新增敏感数据落库；无 PII 新增暴露面（用户名本就在客户管理可见）。

## 7. 边界情况

- 该月无开通 → 空状态（合计 ¥0），不报错。
- 仅 trial、无订阅 → 正常显示 trial 行。
- 跨月续费（Rule B）→ 归属到续费发生月。
- booster → 不出现。
- 月份非法 / 缺失 → 400 友好提示。
- 子账号访问接口 → 403。

## 8. AI 可观测性

**N/A** — 纯 DB 查询报表，无 aiservice / LLM 调用，无需 Langfuse trace。

## 9. 验收标准映射（对齐 PRD §4）

spec 覆盖 PRD 全部验收标准：入口（§5.3）、默认当月（§5.2）、切月（§5.2）、明细字段（§3.2）、合计（§3.2/§5.2）、口径一致（§4.4 单测）、越权隔离（§4.4 单测 + §6）、子账号 403（§4.2/§4.4）、非法 month 400（§3.3）、空状态（§5.2/§7）。

## 10. S5 验证策略预告（S3 plan 将固化为独立 task）

- 后端：`go test ./internal/numind/biz/b2b_billing/...`（含越权 + 口径一致回归测试，永久留存）
- 前端关键路径：Playwright E2E **或** gstack /qa —— 父账户登录 → 客户管理 → 点「费用对账」→ 进 `/customers/billing` → 默认当月有数据 → 切月 → 合计正确 → 空月空状态。
- **因涉及计费/会员高风险域**（testing.md / 规则 10），倾向 **Playwright E2E** 留持久回归保护，而非一次性 gstack /qa。S3 reviewer 把关。
