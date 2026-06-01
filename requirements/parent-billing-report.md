# 父账户自助费用对账页面（Parent Billing Report）

## 来源
- 提出人：用户（产品 owner）
- 提出日期：2026-06-01

## 需求描述

> 需要给父用户开发一个费用查看页面，用来展示每个月每个父用户下，给哪些账号开了什么会员，开了多久，价格是多少，这样能方便父用户对账。

结构化理解：

- 这是一个**用户端**（numind-web-v3）功能，面向**父账户**（`parent_user_id IS NULL` 的顶层账户）。
- 父账户登录后能看到一个**按月**的费用明细页面，列出本月（或所选月份）自己通过「帮开通会员」给名下**子账号**开通的所有会员记录：
  - 子账号（用户名 / ID）
  - 开了什么会员（产品类型：体验包 trial / 月订阅 monthly）
  - 开了多久（月数；trial 为固定 3 天）
  - 价格（该笔开通对应的金额）
  - 开通时间
- 页面需要给出**本月合计金额**，方便父账户和莫小派对公结算时核对。

## 业务目标

让父账户能**自助查看**自己名下的 B2B 开通费用明细，月底对账时不必依赖运营/财务从 admin 端导出。这是已有 admin 端 `GET /v1/admin/b2b-billing-report?month=YYYY-MM`（按父账户聚合，财务对公结算用）的**父账户自助版**——同一份数据，作用域收窄到「当前登录父账户自己」。

## 优先级

中。非紧急生产事故，但提升父账户对账体验、减少运营人工导出负担。

## Triage

- **推荐轨道：Standard**
- **分类理由**（5 条标准逐条）：
  1. 数据库 schema 变更：**否**（数据已存在于 `membership_event` / `subscription` / `trial_grant`，无需建表/加列）
  2. 新增 API 端点：**是**（需新增**用户端**对账接口；现有 `/v1/admin/b2b-billing-report` 是 admin_token 作用域，C 端父账户访问不到）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（跨两仓库：server 端 biz/controller/router + web-v3 端 页面/api/route/类型，约 6-8 文件）
  5. 高风险业务逻辑（支付/权限）：**是**（属于会员/计费域；且必须做权限隔离——父账户只能看自己 `granter_user_id` 名下的记录，绝不能越权看别的父账户）
- **人类决定：确认 Standard**（用户 2026-06-01 已在档位判定中确认）

## 涉及仓库

- `numind-server` — 新增用户端父账户对账 API
- `numind-web-v3` — 新增父账户费用查看页面

## 备注（S1/S2 待澄清 + 复用线索）

### 高复用线索
- 现有 `internal/numind/biz/b2b_billing/b2b_billing.go` 已实现完整的金额归因逻辑（Rule A 首月订阅 / Rule B 跨月续费 / trial 独立路径 / 价格用 `PriceForMonths` 重算而非读 `amount_cents`，规避历史脏数据）。
- 现有响应结构 `ParentBillingRow` + `GrantDetail`（child_user_id / child_username / product_type / months / amount_cents / granted_at）**恰好就是本需求要展示的字段**。
- 父账户自助版核心改动：在 biz 层把作用域从「所有 `granter_user_id`」收窄到「当前登录父账户的 user_id」，复用同一套 computeBilling/buildReport 逻辑。
- 价格常量与年付折扣（12 月 = ¥949 而非 ¥1188）已集中在 `internal/pkg/model/membership/`，前端展示价格直接用后端返回的 `amount_cents`，不要前端二次计算。

### 权限模式
- 父账户判定：`isParentAccount(u) = u.ParentUserID == nil`（见 `biz/credit/credit_service.go:157`）。
- 用户端 grant-membership 端点已挂在 `authGroup`（user_token 中间件，`router.go:283`）；本接口走同一鉴权 + 父账户校验（非父账户访问应拒绝或返回空）。

### 已与用户对齐的决策（2026-06-01）
1. **时间范围**：✅ **单月查看（选月份，默认当月）**。与 admin 版 `?month=YYYY-MM` 口径一致，实现最轻。不做多月历史列表。
2. **入口/页面形态**：✅ **放进用户端「客户管理」页面内，跳转内页（inner page / drawer / 子视图）查看，不做独立顶级页面**。复用客户管理已有导航与上下文。

### 仍待 S1/S2 决策（默认值已给，S1 提案确认）
3. **空状态**：父账户某月没有任何开通记录时的空状态 + 文案（默认：空状态卡片 + 引导文案）。
4. **Booster 加量包**：admin 结算版**完全排除 booster**（子账户自购，不由父账户承担）。父账户对账页**默认同样只展示 trial + 月订阅，排除 booster**（保持与结算口径一致）。
5. **金额口径**：展示「本月合计」总额；后端返回 `amount_cents`，前端统一 ¥ 元展示。
