# 父账户自助费用对账页面 — 提案

> S1 工件。对应需求卡片 `requirements/parent-billing-report.md`。Standard 轨道。

## §1 方案概述 [客户可见]

给父账户在**「客户管理」页面内**增加一个「费用对账」入口。点击后进入一个**按月**的账单内页，列出当前登录父账户在所选月份给名下**所有子账号**开通的会员明细：

| 子账号 | 会员类型 | 开了多久 | 价格 | 开通时间 |
|--------|---------|---------|------|---------|
| 张三   | 月订阅   | 3 个月   | ¥297 | 06-03   |
| 李四   | 体验包   | 3 天     | ¥9.9 | 06-12   |
| …      | …        | …        | …    | …       |
| **本月合计** | | | **¥306.9** | |

默认展示当月，可切换月份查看历史。父账户月底对账时直接看这个页面即可核对应付给莫小派的金额，不再依赖运营/财务从后台导出。

数据与口径**完全沿用**现有管理端「B2B 月度结算报表」的同一套规则（价格按产品类型 + 月数重算，年付 12 月按 ¥949 折扣价，体验包 ¥9.9，加量包不计入对账），只是把范围从「所有父账户」收窄到「你自己」。

## §2 报价与周期 [客户可见]

- 预估工作量：**1.5 ~ 2 天**（后端 0.5 天高复用 + 前端 1 ~ 1.5 天）
- 报价：内部功能，无对外报价
- 交付时间线：S2 设计 → S3 计划 → S4 编码 → S5 本地验收 → S6 dev 验收，按 NDF 节奏推进

## §3 技术可行性 [AI 内部]

### 现有功能复用（复用度高 — 这是本功能轻量的根本原因）

- **biz 层**：`internal/numind/biz/b2b_billing/b2b_billing.go` 已实现完整金额归因逻辑：
  - Rule A（首月订阅）/ Rule B（跨月续费）/ trial 独立路径
  - 价格用 `membershipModel.PriceForMonths` **重算**而非读 `amount_cents`（规避历史脏数据，含年付 ¥949 折扣）
  - 迁移占位行（`idempotency_key LIKE 'migration-%'`）排除；booster 完全排除
- **响应结构**：`ParentBillingRow` + `GrantDetail`（`child_user_id` / `child_username` / `product_type` / `months` / `amount_cents` / `granted_at`）**恰好覆盖本需求要展示的全部字段**。
- **控制器模式**：`controller/v1/admin_b2b/billing_report.go` 是极简范例（取 `month` query → 调 biz → `core.WriteResponse`），用户端控制器照此写。
- **前端**：`src/views/CustomersView.vue`（route `/customers`，`meta.parentOnly: true` 已限父账户）+ `GrantMembershipModal.vue`（Teleport+Transition 弹窗范例）+ `src/api/parent.ts`（父账户 API 集中地）。

### 核心改动点

1. **biz 新增父账户作用域方法**（S2 定稿）：现有 `GetBillingReport(ctx, month)` 聚合**所有** `granter_user_id`。新增 `GetBillingReportForParent(ctx, month, parentUserID)`，把 `granter_user_id = ?` 过滤**下推到 SQL**（computeBilling 的 Rule A/B/trial 三处 Where 各加一个 granter 约束）。
   - **为什么不在控制器层 filter ByParent**：那样会把所有父账户的数据加载进内存再过滤，既有越权数据暴露风险又浪费查询。作用域必须在 SQL 层收窄。
2. **新增用户端端点**：`GET /v1/users/me/billing-report?month=YYYY-MM`，挂在 `router.go` 的 `authGroup`（user_token 中间件）。
   - ⚠️ **不可复用 admin 端点**：`/v1/admin/b2b-billing-report` 是 admin_token 作用域，C 端父账户无法访问——这正是本功能存在的理由。
3. **前端**：客户管理页**页面级**入口（非逐行 action）→ 内页/弹窗展示整月跨子账号明细 + 合计 + 月份切换。

### 技术风险

| 风险 | 缓解 |
|------|------|
| **越权**（父账户 A 看到父账户 B 的数据）| 父账户 id **只从 auth 上下文 `c.GetUint("userID")` 取**，绝不接受客户端传入的 parent id；biz SQL 强制 `granter_user_id = <authUserID>` |
| 非父账户（子账号）访问该端点 | 控制器校验 `isParentAccount`（`ParentUserID == nil`，见 `biz/credit/credit_service.go:157`）；非父账户返回空报表或 403 |
| 金额口径与 admin 结算版漂移 | biz 复用同一 `computeBilling` 内核 + 同一 `PriceForMonths`，不复制逻辑 |
| 月份格式非法 | 复用 biz 现有 `parseMonth` 严格 `YYYY-MM` 校验 |

### 涉及仓库
- [x] numind-server（biz 父账户作用域方法 + 用户端 controller + router 注册）
- [x] numind-web-v3（客户管理内的费用对账入口 + 内页 + api + 月份选择）
- [ ] numind-admin-web（不涉及）

### AI 可观测性（如功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：**否**。纯 DB 查询报表，无任何 aiservice 调用。Trace/Generation **N/A**。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事
- 作为**父账户**，我需要在客户管理里按月查看名下所有子账号的会员开通明细（子账号 / 会员类型 / 时长 / 价格 / 开通时间）与**本月合计金额**，以便月底和莫小派对账，核对应付费用。

### 验收标准
- [ ] 父账户登录用户端 → 客户管理页有「费用对账」入口（页面级，非逐行）
- [ ] 点击进入按月账单内页，默认显示**当月**
- [ ] 可切换月份（`YYYY-MM`），切换后刷新该月数据
- [ ] 列表展示该父账户在所选月份每一笔开通：子账号用户名、产品类型（体验包/月订阅）、月数（trial 显示 3 天）、金额（¥ 元）、开通时间
- [ ] 展示**本月合计金额**
- [ ] 金额口径与 admin `b2b-billing-report` **一致**（同月同父账户，两端合计金额相等）— 后端单测断言
- [ ] 父账户只能看到**自己**名下的记录（越权测试：构造两个父账户，互相看不到对方数据）
- [ ] 子账号（非父账户）调用接口 → 返回空报表或被拒（不报 500）
- [ ] 非法 month 参数 → 400 友好错误（不 500）

### 边界情况
- 该月无任何开通 → 空状态视图（友好文案 + 合计 ¥0），非报错
- 父账户只开过 trial（无订阅）→ 正常显示 trial 行
- 跨月续费（Rule B）→ 正确归属到续费发生的月份
- 加量包（booster）→ **不出现**在对账列表（与结算口径一致）
- 月份选择器不允许选未来月份（可选约束，S2 定）

### 权限规则
- **用户端**（user_token），仅**父账户**（`ParentUserID == nil`）可用
- 父账户 id 仅来自 auth 上下文，禁止客户端传参指定
- 子账号无此入口（前端 + 后端双重保证）

### UI 行为规格
- **页面位置**：用户端「客户管理」页（`/customers`）**内部**，页面级入口（如 hero/工具栏的「费用对账」按钮）。**不新增顶级导航、不做独立顶级页面**（用户已明确）。
- **内页形态**：候选两种——(a) 大号 Modal（复用 CustomersView 现有 Teleport+Transition 弹窗模式）；(b) 子路由 `/customers/billing`（内页）。**S2 技术设计定稿**（倾向 b 子路由内页，账单数据量适合独立视图；待 S2 brainstorming 评估两端工作量）。
- **布局**：月份选择器（顶部）+ 账单明细表（DataTable 风格）+ 合计行/卡片
- **交互**：选月份 → 拉取该月数据；导出（PDF/CSV）**不在本期范围**（未来增强）
- **状态处理**：loading（skeleton/spinner）/ empty（空状态 + 文案 + 合计 ¥0）/ error（含 retry）/ success — 四状态齐全（硬规则 ui-ux.md #2）

## §5 产品思考（office-hours 式 forcing questions）

- **需求真实性**：父账户当前无法自助对账，依赖运营从 admin 后台导出——这是真实摩擦。该功能把对账能力直接交到付费决策方手里。
- **最窄楔子**：单月、只读、复用现有结算内核、零 schema 变更。不碰支付、不碰定价逻辑、不做导出。是能独立交付价值的最小切片。
- **10 星版（本期不做，记录为未来增强）**：月度账单 PDF/邮件推送、多月趋势图、对账状态标记（已结/未结）、与对公开票打通。本期先把「看得到」做扎实。
- **不做**：多月历史列表（用户已选单月）、独立顶级页面（用户已选客户管理内）、booster 纳入（与结算口径冲突）。
