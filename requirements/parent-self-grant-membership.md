# 父账户自助开通会员（Parent Self-Grant Membership）

## 来源
- 提出人：zhiyu
- 提出日期：2026-04-20

## 需求描述

当前 B2B2C 会员体系下，父账户（`parent_user_id IS NULL`）无法给自己开通会员：

- `POST /v1/orders` 自购路径对 trial/monthly/yearly 全部拒绝（`ErrMembershipSelfPurchaseDisabled`），不区分父/子账户
- `POST /v1/users/children/:child_id/grant-membership` 只能给子账户开通（`child.parent_user_id == parent.id` 硬校验）
- `PUT /v1/admin/users/:id/tier`（admin 开通）**功能残废**：只改 `user_tier` 字段，不发 `credit_package`、不写 `action_log`、不写 `TierChangeLog`，credits 制用户开通后积分余额为 0

父账户目前实际上**无法获得会员资格**，只能通过 admin 手工改数据库（账务无痕）。

### 客户诉求

父账户进入用户端"客户管理"页后：
1. 自己账号作为列表第一行出现（带"我"标识）
2. 点击"帮升级会员" → 弹出**和给子账户开通完全一样的弹窗**（trial/monthly + 月数 + 理由）
3. 确认后直接开通（**不弹支付二维码、不走微信/支付宝**）
4. 后台完整记录（`credit_package` + `action_log`），月末 B2B 对公结算自动聚合

## 业务目标

1. **打通父账户的上车路径**：当前父账户会员开通是业务盲区，销售/admin 不介入则拿不到会员资格
2. **统一 B 端客户账务**：父账户自用 + 给员工开通，在财务视角统一归属到"该 B 端客户本月消耗"
3. **对齐用户体验**：父账户的会员管理和子账户管理共用一套交互，降低学习成本
4. **复用成熟代码路径**：`GrantMembership` biz 已经跑通（tests 9/9 PASS），放开一条校验即可支持 self-grant，风险可控

## 优先级

**高** — 阻塞父账户正常使用产品（credits 消费需在期会员），影响 B 端客户转化。

## Triage

- **推荐轨道：Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**（`credit_package.grant_source` + `granter_user_id` 字段已存在）
  2. 新增 API 端点：**否**（复用 `POST /v1/users/children/:child_id/grant-membership` + `GET /v1/customers/sub-users`）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（backend biz + backend store + frontend view + tests + 可能 B2B report SQL，预估 4-5 文件）
  5. 高风险业务逻辑（支付/权限）：**是**（会员开通 + 放开 parent-child 关系校验，B2B2C 权限模型扩展）
- **人类决定**：确认走 Standard，要求加速推进

## 业务决策（对话中已穷尽，直接封存）

| 议题 | 决策 |
|------|------|
| 父账户买 booster | **选 a**：booster 走现有支付链路（扫码付款），不进"帮升级"弹窗 |
| 父账户防滥用 | **复用即可**：沿用现有 trial 终身一次 + monthly 在期不可重开的校验 |
| "monthly" 语义 | **新 credits 机制**（`ProductType='monthly'` → `credit_package.type='subscription'`，2000 积分/月），不是 legacy tier |
| self-grant 归因 | **复用 `grant_source='b2b_grant'`**，不新增枚举值；通过 `user_id == granter_user_id` 天然识别"自用 vs 分发" |
| admin 残废接口 | **不在本需求范围**。`/v1/admin/users/:id/tier` 的修复作为后续独立 hotfix 处理 |

## 备注

- 客户管理页（`numind-web-v3/src/views/CustomersView.vue`）已有 `handleMenuGrantMembership` 和 tier-dialog 组件，前端改动聚焦"让自己行也能点 action + 识别'我'"
- 后端 biz 改动是最小风险点：`grant_membership.go:79-81` 的父子校验需要在 `child_id == parent_id` 时放行
- 列表 SQL 从 `WHERE parent_user_id = ?` 改为 `WHERE parent_user_id = ? OR id = ?`，排序自己置顶
- 月末 B2B 报表（`GET /v1/admin/b2b-billing-report?month=YYYY-MM`）因按 `granter_user_id` 聚合，本改动**自动生效**；可选增强：加 CASE 区分 `self` vs `delegate` 明细
