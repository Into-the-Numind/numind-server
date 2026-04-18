# 积分计费系统完善 — 提案（v2，基于 prod 现状审计修订）

> **修订说明（2026-04-18）**：S1 审计发现积分基础设施 80% 已在 prod（`credit_package`/`credit_account`/`pricing_rule` 表、`RechargeWithOrderTx` 已支持 booster、`DeductCredits` 已 FIFO 扣减、Admin 积分管理已实装、Trial ¥9.9/200/3 天已完整落地）。本 proposal 聚焦**真实缺口**，非从零搭建。

## §1 方案概述 [客户可见]

**背景：** 新积分机制已部分上线 prod（2000 积分/月会员、¥9.9/200 积分/3 天 trial、¥29.9/600 积分/3 月 booster 加量包的骨架已运行），但存在若干影响业务闭环的缺口需要完善。

**本次完善围绕 6 个目标：**

1. **补齐扣减漏洞**：SalesRAG 知识库对话当前**完全未扣积分**（prod 环境漏洞，立即止血）
2. **加量包会员门槛**：当前任何用户都可购买 booster，需加"必须有效订阅会员"校验
3. **精确计费**：从 operation 级固定预估值（SOP 固定扣 20 积分）升级为 R2 字符数估算 + 事后对账，按真实消耗扣减
4. **显性化 billing_mode**：在 `sub_user` 表新增计费模式字段，支持 `legacy_tier`（旧次数制老会员）和 `credits`（新积分制）双制并存，老会员到期后自然迁移
5. **补齐前端 UI**：账户中心积分余额卡、加量包购买入口（含非会员阻断）、SOP 运行前预估条、积分不足弹窗——当前**前端积分 UI 几乎为零**
6. **顺手清理**：卡片生成功能已是死代码（审计确认），移除 `card_config.go` 孤立文件 + 更新 `CLAUDE.md` 过时描述

**对用户价值：**
- 运行 SOP 前知情预估成本（不会扣莫名数字）
- 会员月底积分耗尽可用加量包续命
- 老会员零感知（legacy_tier 到期前完全按旧规则）

**对业务价值：**
- 消除 SalesRAG 不扣费的 prod 漏洞
- 加量包会员门槛保护会员产品定位
- R2 估算 + 对账为未来差异化定价打底

## §2 工作量与时间线 [客户可见]

- 预估工作量：**12-15 天**（AI 辅助开发，跨 3 仓库）
- 交付时间线：**2026-05-08 前上线 dev 环境**
- Beta 观察期：上线后 2-4 周收集真实扣减数据，运营侧可 calibrate R2 系数

## §3 技术可行性 [AI 内部]

### 已在 prod 可直接复用（审计于 2026-04-18 确认）

| 模块 | 证据 |
|------|------|
| `credit_account` / `credit_package` / `credit_transaction` / `pricing_rule` 四张表 | `migrations/add_credits_system.sql:16-32`, `migrations/seed_pricing_rules.sql` |
| `ProductTypeBooster`（¥29.9 / 600 积分 / 90 天） | `model/order.go:40`, `biz/credit/credit.go:260-272`（RechargeWithOrderTx 已支持） |
| `ProductTypeTrial`（¥9.9 / 200 积分 / 3 天） | `model/order.go:56-75`, `biz/credit/credit.go:205-217` |
| `DeductCredits` FIFO by ExpiresAt（天然优先级：会员积分月底过期 > booster 3 月过期，扣减顺序已对） | `biz/credit/credit.go` |
| `GetBalance` + `GetQuotaBreakdown`（已按 subscription/booster 分类返回） | `biz/credit/credit.go`, `numind-web-v3/src/api/credits.ts` |
| Admin 用户积分查询 + 手动充值弹窗 | `numind-admin-web/src/views/CreditUsersView.vue` |
| 订单支付闭环 | `biz/payment/`, `RechargeWithOrderTx` 分月 pending+active 激活逻辑 |

> **关键观察：** `DeductCredits` 当前 FIFO 按 `expires_at` 升序扣减——因为会员积分（月底过期）比 booster（3 月过期）先到期，**扣减顺序天然就是"会员 > 加量包"**，D4 决策无需新增代码，只需验收测试确认。

### 本次改造清单

| # | 改造点 | 预估 |
|---|--------|------|
| 1 | 新增 `sub_user.billing_mode enum('legacy_tier','credits')` 字段 + migration | 0.5 天 |
| 2 | 新增 `credit_reservation` 表（预扣记录） | 0.5 天 |
| 3 | 新增 `credit_estimation_coefficient` 表（R2 系数配置） | 0.5 天 |
| 4 | **R2 数据 spike**：基于 prod `usage_record` 历史算各模型 completion/prompt 比值，导出为 coefficient 表 seed | 1 天 |
| 5 | `biz/credit.EstimateCredits`（字符数 × 系数估算） | 1 天 |
| 6 | `biz/credit.ReserveCredits` + `ReconcileReservation`（预扣+对账两阶段） | 1.5 天 |
| 7 | `CanPerformAIOperation` 增加 `billing_mode` 分支（legacy_tier 走旧 `CanRunSOP`，credits 走积分校验） | 0.5 天 |
| 8 | `biz/payment` 新增 `ProductTypeBooster` 的会员资格校验 | 0.5 天 |
| 9 | SOP 执行 (`sop.go:722`) + SOP Chat (`sop.go:1402`) 扣减路径改造为 Reserve + Reconcile | 1.5 天 |
| 10 | **SalesRAG Chat 接入扣减**（prod 漏洞修复：当前 `salesrag.go` 未调用 DeductCredits） | 1 天 |
| 11 | Langfuse 新增 `span:credit-estimate` + `span:credit-reconcile` | 0.5 天 |
| 12 | 管理端"估算系数管理" UI（DataTable CRUD + 审计日志） | 1 天 |
| 13 | 用户端 UI：账户中心余额卡 + SOP 运行前预估条 + 积分不足弹窗 + 加量包购买入口 | 3 天 |
| 14 | 一次性数据迁移脚本：现有非 free 用户 `billing_mode='legacy_tier'`，其余 `credits` | 0.5 天 |
| 15 | **卡片残留清理**：删除 `card_config.go` + 更新 `CLAUDE.md` L11 | 0.5 天 |
| 16 | Playwright E2E（6 条关键路径） | 2 天 |

**合计：14 天**（预留 1 天 buffer 到上限 15 天）

### 技术风险

| 风险 | 等级 | 缓解 |
|------|------|------|
| R2 字符数估算精度不可控（completion token 方差大） | 中 | S3 plan Task 1 = 数据 spike 基于 prod 历史算 model 级均值+标准差；估算时加 safety_buffer（如 20%）向上取整 |
| `billing_mode` 分支逻辑穿透权限检查/扣减/UI 多处 | 中 | 抽象 `CreditService` 接口，`legacyTierImpl` 和 `creditsImpl` 两个实现；上层调用只认接口 |
| 预扣后 SOP 失败 / 用户取消 / 跨月完成 | 中 | ReconcileReservation 幂等（防重复）；跨月退还读"扣减时原账户"（credit_package_id 硬绑定），过期账户视为无操作 |
| SalesRAG 扣减接入后老会员可见费用上涨 | 低 | **参见 P4e 决策**——建议 legacy_tier 下 SalesRAG 保持免费 |

### 涉及仓库

- [x] numind-server（数据层 + biz/credit + biz/payment + 扣减路径 + SalesRAG 集成 + 卡片清理 + 迁移脚本）
- [x] numind-web-v3（余额卡、预估条、购买入口、积分不足弹窗）
- [x] numind-admin-web（估算系数管理 UI）

### AI 可观测性（涉及 LLM 调用）

- [x] 本次**不新增 LLM 调用**，但改造了所有现有 LLM 调用的扣减生命周期
- **复用现有 trace**（SOP / SalesRAG / SubAgent 等 trace 不变）
- **在现有 trace 内新增 2 类 span：**
  - `span:credit-estimate`（LLM 调用前）：`credits_reserved`, `estimated_prompt_tokens`, `estimated_completion_tokens`, `coefficient_version`, `billing_mode`, `from_subscription_credits`, `from_booster_credits`
  - `span:credit-reconcile`（LLM 调用后）：`actual_credits`, `delta`, `reconcile_direction`（refund/topup/noop）, `refunded_to_subscription`, `refunded_to_booster`
- 元数据支撑后续系数 calibration 和财务对账

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

**C 端用户：**
- 作为 **free** 用户，我访问 SOP 或加量包购买入口时看到"成为会员解锁"引导和升级路径
- 作为 **trial 新制用户**（¥9.9/200 积分/3 天），我和新制会员规则一致，只是额度更小、3 天到期，到期自然降级到 free
- 作为 **credits 会员**，我在账户中心看到"会员积分 X/2000（本月 MM-DD 过期）" + "加量包积分 Y（最早 YYYY-MM-DD 过期）"两档独立展示
- 作为 **credits 会员**，我运行 SOP 前看到"预估消耗 XX 积分（当前余额 YY）"，决定是否执行
- 作为 **credits 会员**，我积分不足时看到弹窗一键跳转加量包购买
- 作为 **credits 会员**，我运行 SalesRAG 知识库对话会按真实消耗扣积分（现在是免费 bug）
- 作为 **legacy_tier 老会员**，我的使用体验完全不变，到期降级 free 后下次购买进入 credits 新制

**管理员：**
- 作为管理员，我可以配置每个模型的 R2 估算系数（provider+model 粒度），修改后实时生效，带审计日志
- 作为管理员，我可以一次性执行数据迁移脚本（幂等）为所有现有用户写入 `billing_mode` 初始值
- 作为管理员，现有的用户积分查询和手动充值功能**完全保留**

### 验收标准

**核心扣减流程（新）：**
- [ ] `billing_mode='credits'` 用户运行 SOP：Reserve → LLM 调用 → Reconcile（多退少补）；退还回原扣减的 credit_package（精确到 package_id）
- [ ] `billing_mode='legacy_tier'` 用户运行 SOP：走现有 `CanRunSOP()` 旧规则，**不触发积分预扣/对账**
- [ ] 前端 SOP 启动前调用 `POST /v1/credits/estimate`，返回预估值 + 当前余额
- [ ] 余额不足时后端返回 4xx + 明确 error code，前端弹 `ConfirmModal` 跳转加量包购买

**SalesRAG 扣减（prod 漏洞修复）：**
- [ ] SalesRAG Chat 路径（`biz/salesrag/salesrag.go`）接入 Reserve + Reconcile
- [ ] Langfuse 配对 `credit-estimate` / `credit-reconcile` span
- [ ] 余额不足时 SalesRAG Chat 拒绝（与 SOP 一致）
- [ ] **legacy_tier 用户的 SalesRAG 行为**：待 P4e 决策（见末尾）

**加量包会员门槛：**
- [ ] `ProductTypeBooster` 订单创建前校验：必须有未过期订阅（`credit_package.type='subscription'` + `status='active'` + `ExpiresAt > now`）
- [ ] 非会员创建 booster 订单：返回 403 + 明确 error code
- [ ] 前端购买卡片：非会员看到灰态 + "成为会员后可购买" + 会员跳转入口
- [ ] legacy_tier 会员**不可**购买 booster（P4a 决策）
- [ ] trial 用户**不可**购买 booster（trial 是过渡态，不应叠加加量）

**Billing Mode + Grandfathering：**
- [ ] 一次性迁移：所有现有非 free 用户 → `legacy_tier`；其余 → `credits`
- [ ] legacy_tier 到期降级 free 时 billing_mode 保持不变（free 无所谓）
- [ ] legacy_tier 降级 free 后**下次购买**时 billing_mode 切换为 credits
- [ ] 不支持提前续费：在期用户 POST 同类或更低类型订单被拒

**R2 估算 + 对账：**
- [ ] 估算系数从 `credit_estimation_coefficient` 表读取（按 provider+model+version），不硬编码
- [ ] 预估公式：`ceil(prompt_chars × char_to_token_ratio × (1 + completion_prompt_ratio)) × unit_price + safety_buffer_pct`
- [ ] LLM 调用完成后，billing recorder 计算 actual_cost，与 reservation 比对
- [ ] Reconcile 幂等（用 reservation_id 防重复）
- [ ] 数据 spike 产出的 seed 系数带 provenance（统计 SQL、样本数、时间范围记录在 migration 注释）

**UI：**
- [ ] 账户中心三态余额卡：credits（双档）/ legacy_tier（次数用量）/ free（升级引导）；trial 用 credits 相同模板但额度 200 + 倒计时
- [ ] SOP 运行前预估条：credits/trial 展示，legacy_tier 不展示（保持旧 UX）
- [ ] 加量包购买入口：账户中心余额卡下方 + 积分不足弹窗内
- [ ] 管理端估算系数 CRUD：`DataTable`（硬规则），修改需填 `change_reason`

**清理：**
- [ ] 删除 `numind-server` 中未使用的 `card_config.go`
- [ ] `CLAUDE.md` §1 核心功能列表移除"卡片生成（Markdown → 图片）"

### 边界情况

| 场景 | 处理 |
|------|------|
| 月底 23:59 开始长 SOP、跨月完成 | Reserve 在月底前（扣 expiring subscription pkg），Reconcile 跨月后退还到已过期 pkg → 视为无操作（不补偿） |
| 预扣后 LLM 调用失败 / 网络断开 | Reconcile with actual_cost=0 → 全额退还到原 pkg |
| 用户手动取消 SOP 执行（前端主动调取消） | 同上 |
| `billing_mode='legacy_tier'` 用户调 `POST /v1/credits/estimate` | 返回 `{estimated_credits: 0, skip_deduction: true}` |
| 非会员创建 booster 订单 | 返回 403 with code `MEMBERSHIP_REQUIRED` |
| `credit_reservation` 超过 24 小时未 reconcile | 后台 cron 扫描 → 假定调用失败 → 自动 refund |
| 用户有多个 subscription pkg（续费叠加） | FIFO by ExpiresAt 已覆盖 |
| 用户有多个 booster pkg（多次购买） | FIFO by ExpiresAt 已覆盖，最早过期先扣 |
| Admin 改系数时有 reservation 在飞 | reservation 记录 `coefficient_version`，Reconcile 用 reserved 当时的 version 不受新系数影响 |
| 会员到期精确时刻 + 正在运行 SOP | Reserve 发生在"到期前"按 credits，Reconcile 在"到期后"退还到已 expired pkg → 视为无操作 |

### 权限规则（修订：trial 归新制 credits）

| 操作 | free | **trial (credits)** | standard/premium (legacy_tier) | standard/premium (credits) | admin |
|------|------|---------------------|-------------------------------|----------------------------|-------|
| 运行 SOP | ❌ | ✅（扣 credits） | ✅（旧制次数 20/月 或 无限） | ✅（扣 credits） | ✅ |
| 运行 SalesRAG Chat | ❌ | ✅（扣 credits） | **P4e 待定** | ✅（扣 credits） | ✅ |
| 购买会员（monthly/yearly） | ✅（→credits） | ❌（trial 不可重复购，可升级 standard/premium） | ❌（在期禁续费） | ❌（在期禁续费） | — |
| 购买加量包 booster | ❌ | ❌ | ❌（P4a 决策） | ✅ | — |
| 运行 SOP 前预估 API | ❌ | ✅ | 返回 skip_deduction=true | ✅ | — |
| 配置估算系数 | ❌ | ❌ | ❌ | ❌ | ✅ |

### UI 行为规格

**账户中心积分余额卡（`numind-web-v3`）：**
- 位置：现有账户中心页顶部
- **credits 用户**：双档展示——"会员积分 X / 2000"（副标题"本月 MM-DD 过期"+ 倒计时） + "加量包积分 Y"（若有，副标题"最早 YYYY-MM-DD 过期"）
- **trial 用户**（credits 新制）：单档"体验积分 X / 200"（副标题"3 天到期，还剩 XX 小时" + CTA"升级为正式会员"）
- **legacy_tier 用户**：不展示积分卡，展示原有"本月已用 X / 20"或"无限"
- **free 用户**：展示"成为会员解锁 AI 能力"引导 + 会员购买入口
- 状态处理：loading 骨架 / error 重试

**加量包购买入口（`numind-web-v3`）：**
- 位置 1：账户中心余额卡下方
- 位置 2：SOP 积分不足弹窗内
- **credits 会员**：卡片（¥29.9 / 600 积分 / 3 个月）+ 购买按钮
- **free / trial / legacy_tier**：灰态卡片 + "需成为正式会员后购买" + 会员购买跳转
- 点击购买：走现有订单流程

**SOP 运行前预估条（`numind-web-v3`）：**
- 位置：SOP 启动按钮上方
- **credits/trial**：展示"预估消耗 XX 积分 | 当前余额 YY"
- 若预估 > 余额：按钮禁用 + 显示"积分不足，购买加量包"
- **legacy_tier**：不展示（保持旧 UX）

**积分不足弹窗：**
- 触发：后端返回 insufficient_credits error code
- 使用 `ConfirmModal`（硬规则）
- 内容：显示缺多少积分 + 当前余额 + "购买加量包 / 取消"两按钮

**管理端估算系数管理（`numind-admin-web`）：**
- 位置："AI 服务管理"下新增"估算系数"tab
- 布局：`DataTable`（provider / model / char_to_token_ratio / completion_prompt_ratio / safety_buffer_pct / version / updated_by / updated_at）
- 操作：新增行 / 编辑 / 停用；修改时必填 `change_reason`（写入审计日志）
- 无定价规则 CRUD UI（复用现有 SQL 维护，不在本次 scope）

### 数据模型（仅列新增/修改）

**修改现有表：**
- `sub_user`：新增 `billing_mode enum('legacy_tier','credits') NOT NULL DEFAULT 'credits'`
- `credit_package`：确认现有 `status` 字段，如无则新增 `enum('active','expired','revoked')`

**新增表：**

| 表 | 核心字段 | 说明 |
|----|---------|------|
| `credit_estimation_coefficient` | id, provider, model, char_to_token_ratio, completion_prompt_ratio, safety_buffer_pct, version, is_active, updated_by, updated_at, change_reason | R2 系数配置，按 version 冻结 |
| `credit_reservation` | id, user_id, reference_type, reference_id, reserved_credits, reserved_from_packages (JSON: [{package_id, credits}]), coefficient_version, status enum('reserved','reconciled','refunded','expired'), actual_cost_cents, delta, created_at, reconciled_at | 预扣记录；`reserved_from_packages` 记录具体从哪些 pkg 扣的，Reconcile 按原路退还 |

**复用现有表（不新增）：**
- `credit_package`（type: trial/subscription/booster 均已支持）
- `credit_account` / `credit_transaction` / `pricing_rule` / `usage_record` / `billing_record` / `user_billing`

### API 端点（仅列新增/修改）

**C 端新增：**
- `POST /v1/credits/estimate` — body: `{operation, reference_id, prompt_preview}`；返回 `{estimated_credits, current_balance, breakdown, skip_deduction}`

**管理端新增：**
- `GET/POST/PUT/DELETE /v1/admin/estimation-coefficients` — CRUD + 审计日志
- `POST /v1/admin/migrations/billing-mode-init` — 一次性幂等执行

**现有端点需改造：**
- `POST /v1/orders`（productType=booster 路径）：新增会员资格校验；否则逻辑不变

**完全不动的（已在 prod）：**
- `GET /v1/credits/balance`
- `GET /v1/admin/credits/users` / `GET /v1/admin/credits/users/:id`
- `POST /v1/admin/credits/users/:id/recharge`

### 内部 biz API（新增）

```go
biz/credit.EstimateCredits(ctx, operation string, promptChars int, modelKey string) (credits int64, coefVersion int, err error)
biz/credit.ReserveCredits(ctx, userID uint, credits int64, refType, refID string, coefVersion int) (*Reservation, error)
biz/credit.ReconcileReservation(ctx, reservationID uint64, actualCostCents int64) error
```

### S5 验证策略

**必须 Playwright E2E 持久回归**（符合 `.claude/rules/ndf-enforcement.md` 规则 10），覆盖 6 条关键路径：
1. 新购 credits 会员 → 账户中心展示 2000 积分 → 跑一个 SOP → 扣减正确（reserved → reconciled 全链路）
2. credits 会员购买 booster → 余额双档展示 → 跑 SOP 跨池扣减（会员先扣完才扣 booster）
3. 非会员（free/trial/legacy_tier）尝试购买 booster → API 返回 403 + 前端灰态
4. legacy_tier 老会员跑 SOP → 走旧制 CanRunSOP 不扣积分（向后兼容）
5. SalesRAG Chat 扣减新路径 → credits 用户扣减正确 + Langfuse span 完整
6. Trial 完整生命周期：¥9.9 购买 → 跑 SOP 扣 credits → 3 天后到期降级 free → 再次购买进入 credits

### S1 Gate 待客户裁决的开放问题（修订后剩 1 个）

原 P4a/P4b/P4c/P4d 已在本轮讨论确认：
- P4a ✅ legacy_tier 会员不可买 booster
- P4b ✅ 卡片生成是死代码（审计确认），顺手清理合入本 scope
- P4c ✅ 加量包过期用 Lazy check + daily cron 标记
- P4d ✅ Trial 是新制（不是 legacy_tier），本 proposal 权限表已修正

**P4e（本轮审计新发现）：legacy_tier 用户的 SalesRAG 扣减策略**

审计发现 SalesRAG Chat 当前完全未扣积分（prod 漏洞）。修复时 legacy_tier 老会员（旧次数制）在 SalesRAG 的扣减方式需明确：

- **A**：legacy_tier 的 SalesRAG 保持免费（完整零感知，保护老会员体验）
- **B**：legacy_tier 的 SalesRAG 也扣 credits（可能从未扣升级到扣，引发老用户感知）
- **🎯 建议 A**：legacy_tier 是"即将 sunset 的过渡态"，不引入新感知；到期迁移到 credits 后统一开始收费
