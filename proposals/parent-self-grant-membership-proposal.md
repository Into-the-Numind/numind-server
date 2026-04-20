# 父账户自助开通会员 — 提案

## §1 方案概述 [客户可见]

父账户（B 端客户主账号）进入用户端"客户管理"页后，**自己的账号以第一行出现**（带"我"标识），可以像管理子账户一样直接点击 **"帮升级会员"** 打开开通弹窗——选择 trial/monthly + 月数 + 理由，**确认后立即开通**，不扫码不付款。

**后台完整记录**：每次开通都在 `credit_package` 表写一行（标记 `grant_source='b2b_grant'` + `granter_user_id=父账户ID`），同时写 `action_log` 审计记录。月末 B2B 对公结算自动按父账户（B 端客户）聚合，财务拉一次报表即可看到"这个 B 端客户本月消耗 = 父自用 + 所有子账户消耗"的汇总金额。

**预期效果**：
- 父账户不再依赖 admin 人工开通，自助即可上车
- 管理父账户会员 + 管理子账户会员共用同一套 UI 交互，学习成本为零
- 账务清晰：一个 B 端客户 = 一条月度结算汇总 = 一张对公发票

## §2 报价与周期 [客户可见]

- 预估工作量：1 个工作日（加速模式）
- 报价：内部优化，不单独计费
- 交付时间线：S0→S6 预计 2026-04-20 内完成，S7 部署视 release 窗口

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 现有模块 | 复用方式 |
|---------|---------|
| `creditBiz.GrantMembership`（grant_membership.go:65） | 放开父子关系校验（79-81 行）允许 `child_id == parent_id`，其它逻辑（防重复、billing_mode 切换、credit_package 写入、action_log）完全复用 |
| `POST /v1/users/children/:child_id/grant-membership`（router.go:253） | API 契约不变；只是允许 `child_id` 为调用者自己 |
| `GET /v1/customers/sub-users`（router.go:232） | query 扩展：`WHERE parent_user_id = ? OR id = ?`（第二个参数是父自己） |
| `CustomersView.vue` tier-dialog | 前端弹窗组件零改动，仅表格行增加"我"标识和自己行允许 action |
| B2B 月度报表 `GET /v1/admin/b2b-billing-report?month=YYYY-MM` | 按 `granter_user_id` 聚合的逻辑不变，父自开包自动并入统计 |
| `credit_package` 表 schema | `grant_source` / `granter_user_id` 字段已存在，无 migration |

### 技术风险

| 风险 | 缓解 |
|------|------|
| **越权**：放开父子校验后，是否有路径让 A 给 B 开通？ | spec 明确校验链：`child_id == parent_id` 允许 self-grant；`child_id != parent_id` 仍走原有 `child.parent_user_id == parent.id` 严校验。**不能绕过原有 B→C 校验** |
| **billing_mode 切换**：父账户如果是 legacy_tier，开通后会自动切 credits | 现有行为一致（子账户首次被开通也会切），无额外风险。decision log 写清楚 |
| **报表歧义**：self-grant 和 delegate-grant 在 `grant_source='b2b_grant'` 下不区分 | 通过 `user_id == granter_user_id` 天然识别，报表层可加 CASE 字段做明细（可选，不阻塞主流程） |
| **前端"自己行"点击 action**：现有代码可能对 self-row 做了过滤 | S2 spec 时逐条检查 CustomersView.vue 的 action 菜单展开逻辑，必要时加显式放行 |
| **测试覆盖**：self-grant 需要新增 test case 覆盖 trial/monthly 成功 + 不同父账户不能互相开通 + 非父账户（parent_user_id != NULL）不能 self-grant | S3 plan 中列出具体 test case，grant_membership_test.go 扩展 |

### 涉及仓库
- [x] numind-server（biz + store + tests）
- [x] numind-web-v3（CustomersView.vue）
- [ ] numind-admin-web（不涉及；admin 残废接口的修复另开 hotfix）

### AI 可观测性
- 涉及 LLM 调用：**否**
- 本功能是账务管理路径，完全不触发 LLM
- N/A

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

1. **US-1（父账户自助开通）**：作为父账户用户（`parent_user_id IS NULL` 的 B 端客户主账号），我需要在客户管理页看到自己账号并为自己开通会员，以便不依赖 admin 人工介入即可使用 credits 制产品

2. **US-2（体验一致性）**：作为已经学会给子账户开通的父账户用户，我需要给自己开通会员的交互路径和给子账户开通**完全一致**（同一个页面、同一个按钮、同一个弹窗），以便零学习成本上手

3. **US-3（财务视角）**：作为财务人员，我需要月末拉一张 B2B 报表就能看到每个父账户（B 端客户）的本月消耗汇总（含父自用 + 所有子），以便按 B 端客户开具一张对公发票

### 验收标准

**前端（numind-web-v3）**：
- [ ] 父账户登录后，进入 `/customers` 页面，列表第一行是自己
- [ ] 自己行的昵称旁显示 **"我"** 标识（badge 或 label，和子账户行视觉区分）
- [ ] 自己行的 action 菜单点击展开后，**"帮升级会员"** 项可见且可点击（与子账户行一致）
- [ ] 点击"帮升级会员" → 弹出和给子账户开通**完全一样**的 tier-dialog（trial/monthly 单选 + monthly 月数下拉 1-12 + 理由输入框）
- [ ] 点击"确认" → 调用 `POST /v1/users/children/:child_id/grant-membership`（`child_id` = 父自己 ID）
- [ ] 成功后 toast 提示"开通成功"，列表刷新，自己行的会员状态/有效期/积分余额同步更新
- [ ] **不出现**支付二维码、不跳转微信/支付宝

**后端（numind-server）**：
- [ ] `POST /v1/users/children/:child_id/grant-membership` 当 `child_id == parent.id` 时**放行**并正常写入
- [ ] 写入的 `credit_package`：`user_id=父ID`, `granter_user_id=父ID`, `grant_source='b2b_grant'`, `order_id=NULL`, 类型和积分额度按 trial/monthly 规则
- [ ] 写入的 `action_log`：`user_id=父ID`（操作者）, `target=user`, `target_id=父ID`（被操作者同为父自己）, `action='grant_membership'`, `detail` JSON 含 product_type/months/reason/package_ids
- [ ] `GET /v1/customers/sub-users` 返回列表中包含父自己（在第一位或标记位置），其它子账户依次排列
- [ ] 父账户自开 trial 触发 `ErrGrantTrialAlreadyPurchased`（终身一次）
- [ ] 父账户自开 monthly 在期内触发 `ErrGrantActiveSubscription`（防提前续费）
- [ ] 父账户 legacy_tier → credits 的 billing_mode 切换在 self-grant 时同样生效

**越权防线**：
- [ ] 非父账户（`parent_user_id IS NOT NULL`）调用 `POST /v1/users/children/:child_id/grant-membership` 且 `child_id == 自己ID` 时**必须拒绝**（子账户不能自己给自己开通）
- [ ] 父账户 A 调用 `POST /v1/users/children/:child_id/grant-membership` 且 `child_id == 父账户 B 的 ID` 时**必须拒绝**（跨父账户不能互相开通）
- [ ] 父账户 A 调用且 `child_id == A 的某个非法 ID`（不存在）时返回 404

**账务（月末对公结算）**：
- [ ] `GET /v1/admin/b2b-billing-report?month=YYYY-MM` 返回的结果中，父账户 X 的 `total_packages` 包含 X 自开的 + X 给每个子账户开的**全部 credit_package**
- [ ] 该报表数据源仅过滤 `grant_source='b2b_grant'`，self-grant 和 delegate-grant 统一归入

### 边界情况

- **并发**：父账户在两个浏览器标签页同时点击"帮升级 monthly" → 第二个请求因 `HasActiveSubscription` 校验返回 `ErrGrantActiveSubscription`
- **数据异常**：`parent_user_id IS NULL` 但 `id` 实际存在子账户关系（数据损坏）→ 现有 GrantMembership 在事务内完成，不影响
- **空列表**：全新父账户还没有子账户 → `GET /v1/customers/sub-users` 仍返回至少一行（自己），前端有值可渲染
- **会员到期**：父账户 monthly 到期后，次月可再次自开（现有 `HasActiveSubscription=false` 即放行）
- **子账户被删除 / soft-delete**：不影响父账户 self-grant 流程

### 权限规则

| 用户类型 | 可自助开通会员？ | 接口路径 |
|---------|----------------|---------|
| **父账户**（`parent_user_id IS NULL`） | ✅ 是，本需求放开 | `POST /v1/users/children/:child_id/grant-membership` (child_id=self) |
| **子账户**（`parent_user_id IS NOT NULL`） | ❌ 否，必须由父账户开通 | 原规则不变 |
| **管理员**（admin 系统） | 不受本需求影响 | `/v1/admin/users/:id/tier`（残废，另 hotfix） |

管理端（`/v1/admin/*`）**不参与**本需求的路径。

### UI 行为规格

**页面位置**：用户端 `/customers`（`CustomersView.vue`）

**布局要求**：
- 列表采用**表格**（已是现状，符合 `.claude/rules/ui-ux.md` 硬规则 1）
- 父账户自己的行置顶（按 `id` 排序时因父账户 id 最小天然置顶；或显式 ORDER BY `CASE WHEN id = :parent_id THEN 0 ELSE 1 END, id`）

**交互模式**：
- 自己行的**昵称列**显示 `{nickname} [我]` 的形式（`[我]` 为 pill/badge 样式，与子账户行视觉区分）
- **所有现有列**（会员状态、有效期、积分余额、消耗统计等）对自己行同样渲染
- Action 菜单（三点按钮）展开后，"帮升级会员"项对自己行**显示且可点击**
- 点击"帮升级会员" → 打开现有 tier-dialog 组件（零改动）
- 确认后 API 调用路径和成功态与子账户行完全一致

**状态处理**：
- loading：列表载入中显示 skeleton（复用现有）
- empty：不适用（至少有自己一行）
- error：API 失败显示 retry 按钮（复用现有）
- success：toast 提示 + 列表刷新（复用现有）

**视觉区分"我"**：
- 浅色背景行 + 昵称后跟 `[我]` badge（小字、低饱和色）
- **不做**颜色反差（避免给人"特殊待遇"错觉，保持 B 端客户体验中性）
