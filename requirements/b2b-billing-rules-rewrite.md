# B2B 月度结算报表逻辑重写 + 年付定价修复 + admin 404 修复

## 来源
- 提出人：用户（产品 owner，也是财务对账实际使用方）
- 提出日期：2026-05-20
- 触发：5 月 prod B2B 报表复核发现 3 个问题

## 需求描述

`GET /v1/admin/b2b-billing-report?month=YYYY-MM` 当前直接累加 `membership_event.amount_cents`，导致 3 类错误：

1. **占位行被算钱**：4-30 那次 `credit_package → membership_event` 迁移给年付用户拆出了 11 个未来日期的 `sub_renewed` 占位行（idempotency_key 前缀 `migration-`）。这些行 occurred_at 落到未来某月时，会被该月报表当成新 grant 重复计费
2. **年付价格错**：grant 写 `amount_cents = months × 9900`，所以 12 月年付被记成 ¥1188，但实际年付价是 ¥949
3. **doubling-bug 历史残留 amount=0**：4-30 ~ 5-13 期间的 B2B 赠送 amount_cents 被错写成 0，报表直接 sum 这些 0 → 漏算（而不是按 ¥99/月推导）

附带 admin-web 端 bug：sidebar 路径 `/admin/b2b-billing` 与 router 实际注册路径 `/b2b-billing` 不匹配，点击 sidebar 进入 → 404。

## 业务目标

让月底跑 `GET /v1/admin/b2b-billing-report?month=2026-05` 能直接拿来对公开发票，不需要人工修正。

## 新规则（已与 owner 对齐）

**双层规则**：

- **规则 A**：对每个 `subscription`，若 `first_started_at` 落在目标月内 → 该月结算 `total_months_purchased` × 单价
- **规则 B**：若 `first_started_at` 不在目标月内 AND `subscription.updated_at` 在目标月 → 查该用户本月内 `idempotency_key NOT LIKE 'migration-%'` 的 sub_granted/sub_renewed 事件，sum 这些事件的 months 并按单价计算

**单价**：
- 1 月 = ¥99
- N 月（2-11）= N × ¥99
- 12 月（年付）= **¥949（不是 12 × 99 = ¥1188）**

**Trial**：完全独立路径，按 `trial_grant.granted_at` 落在目标月 → 算 ¥9.9。

**Booster**：完全排除在结算外。

**结算方向**：莫小派向父账户付钱（不是反向）。

## 优先级

高。本月底（5-30/31）需要跑准确的对公账单。

## Triage

- **推荐轨道：Hotfix**
- **分类理由**（5 条标准逐条）：
  1. DB schema 变更：**否**（只改代码 + 修单个前端字符串）
  2. 新增 API 端点：**否**（修改现有 `/v1/admin/b2b-billing-report`）
  3. 新外部服务集成：**否**
  4. 影响文件数：**4**（边界）—— `b2b_billing.go` + `subscription.go` + `b2b_billing_test.go` + `AdminSidebar.vue`
  5. 高风险业务逻辑：**边界**（B2B 对账是财务相关；但报表只读、不直接触发支付、人工 review 才出账单）
- **人类决定**：**Hotfix**，超出 1 个文件 + 边界财务风险，但用户拍板（speed + 规则已对齐）。
- **强制门**：H2 完成后必须跑两阶段 reviewer（不简化）。H3 必须用 prod 5 月真实数据手算对比验证。

## 文件改动清单

### numind-server
1. `internal/numind/biz/b2b_billing/b2b_billing.go`
   - 重写 `getNewEvents()` 为 `computeMonthlyBilling(month)`
   - 实现规则 A：扫 subscription 表 first_started_at 落在目标月
   - 实现规则 B：跨月续费补漏（first_started_at < 月头 AND updated_at 在月内 AND 非 migration- 事件存在）
   - 加 `priceForMonths(months int) int64` helper（months==12 → 94900，else → months×9900）
   - trial 单独路径（扫 trial_grant.granted_at）
   - `amount_cents` 字段不再读，全部由 months + product_type 推导

2. `internal/numind/biz/membership/subscription.go`
   - 新增 `const annualPriceCents = 94900` // ¥949 年付
   - line 187 `AmountCents` 计算分支：months==12 写 94900；其他 months × 9900

3. `internal/numind/biz/b2b_billing/b2b_billing_test.go`
   - 新增/更新测试覆盖：
     - 卉卉场景：5 月年付 → ¥949（一次结清），6-12 月该用户 0 结算
     - sandy 场景：subscription.total_months=1（被人工改过）→ ¥99（不算 events 里的 14 个月）
     - 跨月续费场景：4 月开通 1 月，5 月续 2 月 → 5 月 ¥198
     - 占位行干扰场景：用户在 4 月开年付，5 月看到她的占位 sub_renewed → 0 结算
     - admin_calibration 触发 updated_at 但无真实续费 → 0 结算
     - Trial 场景：trial_grant 落在 5 月 → ¥9.9

### numind-admin-web
4. `src/components/layout/AdminSidebar.vue:88`
   - `path: "/admin/b2b-billing"` → `path: "/b2b-billing"`（与 router 实际路径对齐）

## 验收标准（H3 检查项）

1. 跑 `GET /v1/admin/b2b-billing-report?month=2026-05` 返回的金额能与手算结果一致（差异 ≤ ¥1 容差为四舍五入）
2. 手算 prod 5 月数据：parent 30 应为
   - 16 个 1 月用户 × ¥99 = ¥1,584
   - 1 个年付 × ¥949 = ¥949
   - 5 个 trial × ¥9.9 = ¥49.5
   - **parent 30 合计 = ¥2,582.50**
   - parent 1 (admin) 待独立验证
3. 跑 6 月预演（mock 数据或直接看返回）：卉卉/406/411 的占位行 occurred_at 在 6 月 → 不应出现在 6 月结算
4. admin-web 点击 sidebar "B2B 月度结算" 能正常进入页面，无 404
5. 单元测试覆盖率：b2b_billing 包 ≥ 80%

## 不在范围

- 历史 amount_cents=0 数据回填（新逻辑不读这字段，回填无意义）
- doubling bug 在 5-19 后的复发监控（已通过 5-14 后所有事件 months=1 正确证实修复，不再监控）
- B2B billing 报表 UI 优化（仅修 404）
- "实际收入" booster 统计的独立呈现（本次 hotfix 不做，遗留为后续 standard feature）
- 跨月人工调整 subscription 时的事件 idempotency_key 改写规范（操作规范文档，遗留）

## 关联背景

- 此前 hotfix `drop-billing-account-dead-table` (v2.1.29, 已上线) 与本次无依赖关系
- 此前 micro fix `membership_event` 17 行 months doubling 修复（已直接在 prod 完成）— 本次不再回头处理 406/411 的 14 行残留（新逻辑不依赖 months 字段为 1 还是 2，因为 migration- 前缀的事件全部跳过）
