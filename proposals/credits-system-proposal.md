# 积分计费系统（会员 + 积分 + 加量包）— 提案

## §1 方案概述 [客户可见]

将计费体系从旧的"会员阶梯制按次数"平滑迁移到新的"会员 + 积分 + 加量包"体系。老会员无感知（自然到期后进入新制），新制上线当天起所有新购会员按新规则执行。

**新制对用户的价值：**
- **用多少付多少**：不再是"20 次/月"的模糊额度，而是按真实 AI 资源消耗扣减积分
- **运行前可预知成本**：SOP 运行前展示预估消耗的积分，用户知情决定是否执行
- **加量包救急**：会员当月积分耗尽后，可 ¥29.9 购买 600 积分（3 个月有效期）继续使用

**对业务方的价值：**
- 消除旧制下"重度用户击穿毛利率"的隐患
- 开辟会员费之外的第二条收入曲线（加量包）
- 支持后续运营精细化（不同模型成本透明、可差异化定价）

**迁移策略（Grandfathering，老会员零感知）：**
- 现有 `standard` / `premium` 会员在到期前继续按旧规则（20 次/月或无限次）
- 到期降级为 `free` 后，下次购买（续费或新购）一律进入新制
- 现有 `trial` 会员（3 天体验）正常到期，后续购买进入新制
- **不支持提前续费**：确保每个用户有干净的旧制→新制切换时点

## §2 工作量与时间线 [客户可见]

- 预估工作量：**18-20 天**（AI 辅助开发，跨 3 仓库）
- 交付时间线：**2026-05-12** 上线 dev 环境
- Beta 观察期：上线后 2-4 周收集真实扣减数据，运营侧可调整估算系数和定价规则

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 模块 | 复用内容 | 改造点 |
|------|---------|--------|
| 计费基础设施 | `BillingAccount.balance_cents`（¥0.01 = 1 积分 已成为隐性约定）、`UsageRecord`（含 token 计数、异步 cost 计算）、`pricing_rule` 表 | 需新增 `credit_estimation_coefficient` 配置表（每模型的 completion/prompt 比值）；把 `2000` 硬编码抽到 `membership_config` 表 |
| 积分账户 | `CreditPackage` 表（已区分 `subscription` / `addon` 类型、已有 `ActivatedAt` / `ExpiresAt`）、`credit.DeductCredits()` | 扩展 `Type` 加入加量包区分；改造 Deduct 支持"预扣 + 对账"两阶段；新增扣减优先级（会员 > 加量包） |
| Langfuse 可观测性 | 现有 trace/generation 贯穿 SOP / SalesRAG 的所有 LLM 调用 | 新增 `span:credit-estimate` 和 `span:credit-reconcile` 标注预扣和对账事件 |
| 订单支付 | `ProductTypeMonthly` / `ProductTypeYearly` 已打通订单→充值链路 | 新增 `ProductTypeAddon` 类型及其校验（仅会员可购）；扩展 `RechargeWithOrderTx` 支持加量包 |
| 用户 tier 体系 | `sub_user.tier` / `tier_expires` 的到期自动降级逻辑 | 新增 `billing_mode enum('legacy_tier', 'credits')` 字段，权限检查分派到不同 biz 函数 |
| 前端账户中心 | 现有会员中心页、订单列表 | 新增"积分余额"卡片（分会员/加量包两档显示+倒计时）、加量包购买入口 |
| 前端 SOP 运行页 | 现有 SOP 启动流程 | 运行前展示"预估消耗 XX 积分"的确认条 |

### 技术风险

| 风险 | 等级 | 缓解方案 |
|------|------|---------|
| R2 字符数估算精度不可控（completion token 方差大） | **高** | **S3 plan Task 1 必须是数据 spike**：基于 `usage_record` 历史数据统计各模型的 `completion_tokens / prompt_tokens` 均值和标准差，导出成 `credit_estimation_coefficient` 表初始值。后续 2-4 周观察期继续 calibration |
| `billing_mode` 字段分支逻辑穿透多处（权限检查、扣减、余额展示） | 中 | 抽象 `CreditService` 统一接口，`legacy_tier` 和 `credits` 两个实现；上层调用方只知道接口不关心具体分支 |
| 预扣与实际成本不一致导致账务漂移 | 中 | 每次 LLM 调用完成后在 billing recorder 中计算差额：actual_cost > reserved → 继续扣；actual_cost < reserved → 退还到原账户（同优先级原则） |
| 加量包 3 月过期涉及定时批量清理 | 低 | 使用 lazy check：扣减时检查 ExpiresAt，过期直接视为 0；额外一个 daily cron 做 status 字段标记（不做资金变动，只维持数据整洁） |
| 旧制用户"未消费额度"是否允许主动切换到新制 | 中 | **本次不做主动切换入口**（保留未来功能）；到期自然迁移是唯一路径，避免 UX 复杂度爆炸 |
| 管理端数据迁移工具（为现有会员生成 billing_mode） | 低 | 一次性脚本：所有现有非 free 用户 billing_mode 设为 `legacy_tier`；free 用户设为 `credits`（反正不能用） |

### 涉及仓库

- [x] numind-server（DB migration、biz/credit、biz/billing、biz/sop 扣减路径、controller、store）
- [x] numind-web-v3（加量包购买页、积分余额展示、SOP 运行前预估条、积分不足弹窗）
- [x] numind-admin-web（定价规则管理 UI、估算系数管理 UI、历史用户迁移工具、积分调试工具）

### AI 可观测性（功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：是（本功能不新增 LLM 调用，但**改造了所有 LLM 调用的扣减路径**）
- **复用现有 trace**：SOP/SalesRAG/SubAgent 等已有 trace 不变
- **新增 span**（在现有 trace 内，标注扣减生命周期）：
  - `span:credit-estimate`（LLM 调用前）：`credits_reserved`、`estimated_prompt_tokens`、`estimated_completion_tokens`、`estimation_model_coefficient_version`
  - `span:credit-reconcile`（LLM 调用后）：`actual_credits`、`delta`（退还或补扣）、`reconcile_direction`
- **关键元数据**：`billing_mode`（legacy_tier / credits）、`deducted_from`（subscription / addon / mixed）、`user_id`
- 这些 span 支撑后续估算系数 calibration 和财务对账

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

**C 端用户（所有等级）：**
- 作为 free 用户，我需要看到"非会员不可使用 SOP/不可购买加量包"的明确提示和升级入口，以便我知道如何开始使用
- 作为新购会员（新制），我需要每月自动获得 2000 积分，并能在账户中心清晰看到"会员积分剩余"和"本月过期时间"
- 作为新制会员，我需要在 SOP 运行前看到"预估消耗 XX 积分"的确认条，以便决定是否执行
- 作为新制会员，我需要在积分接近耗尽时收到提示（低于 20% 或固定阈值），并能一键购买加量包
- 作为新制会员，我需要购买 ¥29.9 / 600 积分 / 3 月有效期的加量包，并看到加量包余额、购买时间、过期时间
- 作为新制会员，我需要在扣减时优先扣会员积分，会员积分不足时自动扣加量包积分
- 作为旧制老会员（legacy_tier），我的使用体验完全不变，直到会员到期

**B 端父用户（B2B2C 场景）：**
- 作为 B 端父用户，本次不改变 B2B2C 分账逻辑（C 端用户消耗积分算在 C 端用户头上）

**管理员（内部运维）：**
- 作为管理员，我需要在管理端配置"每模型的估算系数"并可随时调整（不改代码即可更新）
- 作为管理员，我需要在管理端调整会员月度积分额度（如未来从 2000 改为 2500），改动需记录审计日志
- 作为管理员，我需要一个"用户积分调试工具"：查询用户积分明细、手动发放/回收积分（带理由记录）
- 作为管理员，我需要一个一次性迁移工具为所有现有用户写入 `billing_mode` 初始值

### 验收标准

**新制核心流程：**
- [ ] 新购 `standard` / `premium` 会员后，`sub_user.billing_mode = 'credits'`，并自动发放 2000 积分（按月数重复发放）
- [ ] 会员积分的 `ExpiresAt` = 当月月末，月底自动清零（不累积）
- [ ] 加量包购买成功后，积分 `ExpiresAt` = 购买时刻 + 3 个月
- [ ] SOP 运行前调用估算函数，展示"预估消耗 XX 积分"确认条，用户确认后才真正发起
- [ ] LLM 调用前预扣积分（优先会员 > 加量包），调用完成后 reconcile 差额（多退少补，退还遵循同优先级）
- [ ] 积分不足时拒绝运行，弹出"积分不足 + 购买加量包"弹窗

**Grandfathering：**
- [ ] 现有所有非 free 用户迁移脚本执行后，`billing_mode = 'legacy_tier'`
- [ ] `legacy_tier` 用户的 SOP 运行走旧规则（20 次/月或无限），不触发积分扣减
- [ ] 到期降级为 free 时，`billing_mode` 不变（还是 legacy_tier，因为 free 无所谓）；**下次购买**时 `billing_mode` 切换为 `credits`

**加量包会员门槛：**
- [ ] free 用户访问加量包购买页：UI 不展示加量包商品卡片，展示"成为会员后可购买"提示
- [ ] 非会员直接 POST 加量包订单 API：返回 403 with error code
- [ ] `legacy_tier` 会员**也可购买加量包**（购买后他们即成为混合状态：按旧制运行 + 加量包积分仅在进入新制后才能使用）——**或** `legacy_tier` 禁止购买加量包（需在 S1 Gate 由客户拍板）
- [ ] `credits` 会员到期降级为 free 后，**已购买未使用的加量包积分依然可用**（直到 3 月过期），但不能购买新的加量包

**R2 估算 + 对账：**
- [ ] 估算系数从 `credit_estimation_coefficient` 表读取（每个 provider+model 组合一行），不硬编码
- [ ] 预扣值 = 估算 prompt_tokens × 系数 + 估算 completion_tokens × 系数，向上取整为积分
- [ ] 调用完成后触发 reconcile：actual_cost 从 `pricing_rule` 计算得出，与预扣值比对后多退少补
- [ ] 退还/补扣遵循原扣减优先级（会员积分先退还，加量包次之）
- [ ] Langfuse 必须有 `credit-estimate` 和 `credit-reconcile` 两个 span 配对

**管理端：**
- [ ] 管理端"AI 服务管理"增加"估算系数"tab，CRUD 各模型的系数行
- [ ] 管理端"会员配置"页可修改月度积分额度（修改仅影响此后发放，已发放不变）
- [ ] 管理端"积分调试"页可按 user_id 查询所有 credit_package、手动发放/回收积分（必填原因）
- [ ] 管理端"数据迁移"页有"执行 billing_mode 初始化"按钮（一次性，有幂等性保护）

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 用户月底 23:59 开始一次长 SOP，跨月完成 | 预扣发生在月底前（扣会员积分），reconcile 发生在跨月后（会员积分已清零）→ reconcile 退还逻辑需读取"扣减时原账户"而非"当前账户"，退还到已清零的账户视为无操作 |
| 预扣后 SOP 中途失败 | 失败时立即触发 reconcile（本质是 refund），差额 = reserved（因为实际消耗部分可能很小但仍要记录真实 cost） |
| 用户在预扣和实际调用之间手动取消 | 同上：立即 refund，actual_cost = 0 |
| 加量包购买成功但支付回调丢失 | 沿用现有订单兜底机制（订单对账任务） |
| `legacy_tier` 用户在新制上线**当天**正好会员到期 | 到期时刻严格切换：`billing_mode` 在 tier_expires 精确时刻由后台任务切换 |
| `credits` 用户的 2000 积分在月中被调整（如管理员手动追加） | 管理员追加积分时指定目标账户（会员或加量包），不触发优先级重算 |
| 同一次 LLM 调用需同时扣会员 + 加量包积分（混扣） | 扣减函数支持"拆单"：先扣会员剩余，不足部分从加量包扣；reconcile 按原扣减比例退还 |
| 多个加量包共存（3 个月过期的在不同时间购买） | 同优先级内按 **FIFO**（最早过期的先扣）—— 避免"用户账户里有 3 个加量包但最老那个眼看过期没用上"的浪费 |
| 非会员直接访问"加量包购买"页 URL | 前端路由守卫拦截 + 后端 API 再次校验（双保险） |

### 权限规则

| 操作 | free | trial (legacy) | standard / premium (legacy_tier) | standard / premium (credits) | admin |
|------|------|----------------|----------------------------------|------------------------------|-------|
| 运行 SOP | ❌ | ✅（≤10 次） | ✅（旧制计数） | ✅（扣积分） | ✅ |
| 查看账户中心 | ✅ | ✅ | ✅ | ✅ | ✅ |
| 购买会员 | ✅ → 进入新制 | ✅ → 进入新制 | ❌（到期后才能续） | ❌（到期后才能续） | — |
| 购买加量包 | ❌ | ❌ | **待客户 S1 Gate 拍板** | ✅ | — |
| 使用加量包积分 | ✅（已买且未过期） | ✅（已买且未过期） | ❌（旧制不扣积分） | ✅ | — |
| 配置估算系数 | ❌ | ❌ | ❌ | ❌ | ✅ |
| 手动调整用户积分 | ❌ | ❌ | ❌ | ❌ | ✅ |

### UI 行为规格

**账户中心（C 端 `numind-web-v3`）：**
- 页面位置：现有账户中心页顶部新增"积分余额"卡片
- `credits` 用户：展示两个数字 `会员积分 X / 2000`（带"月末清零"副标题+倒计时）和 `加量包积分 Y`（若有，带"最早 YYYY-MM-DD 过期"副标题）
- `legacy_tier` 用户：不展示积分余额卡片，展示原有的"本月已用 X / 20"或"无限"
- free 用户：展示"成为会员解锁 AI 能力"引导
- 状态处理：loading 骨架屏 / error 重试

**加量包购买入口（C 端）：**
- 位置：账户中心"积分余额"卡片下方 + SOP 执行"积分不足"弹窗
- 非会员看到：灰态加量包卡片 + "成为会员后可购买"提示 + 跳转会员购买
- 会员看到：加量包卡片（¥29.9 / 600 积分 / 3 个月）+ 购买按钮
- 点击购买：走现有支付流程，支付成功后账户中心实时更新

**SOP 运行前预估条（C 端）：**
- 位置：SOP 启动按钮上方
- 展示："预估消耗 XX 积分（当前余额 YY）" + 启动按钮
- 如果预估 > 余额：按钮禁用 + 提示"积分不足，购买加量包"
- `legacy_tier` 用户不展示该条（保持旧 UX）

**积分不足弹窗（C 端）：**
- 触发：运行 SOP 时后端返回积分不足
- 使用 `ConfirmModal`（硬规则）
- 内容：差多少积分 + "购买加量包 / 取消"两个按钮

**管理端 — 估算系数管理（`numind-admin-web`）：**
- 页面位置："AI 服务管理"下新增"估算系数"tab
- 布局：表格列表（provider / model / completion_prompt_ratio / updated_by / updated_at），使用 `DataTable`（硬规则）
- 操作：编辑数值、新增行、删除行；改动需填 change_reason

**管理端 — 会员配置：**
- 页面位置：管理端"全局配置"页
- 布局：表单（月度积分额度、加量包价格、加量包积分数、加量包有效期月数）
- 提交：记录 audit log；修改后展示 banner"已修改，仅影响此后发放"

**管理端 — 积分调试工具：**
- 页面位置：管理端"用户管理"→ 点击用户 → "积分明细"tab
- 布局：表格展示所有 CreditPackage（类型、初始/剩余、激活/过期、订单 ID、状态）
- 操作："手动发放积分"按钮（表单：数量、类型 subscription/addon、过期时间、原因）+ "回收积分"按钮

**管理端 — 数据迁移工具：**
- 页面位置：管理端"系统工具"→ "数据迁移"tab
- 布局：按钮"执行 billing_mode 初始化"+ 说明文字 + 执行结果展示
- 幂等性：按钮仅在所有用户 billing_mode 为 NULL 时可用；执行后按钮不可用

### 数据模型概要

**修改现有表：**
- `sub_user`：新增 `billing_mode enum('legacy_tier', 'credits') NOT NULL DEFAULT 'credits'`
- `credit_package.Type`：扩展枚举，明确区分 `subscription_membership`（会员积分）和 `addon_package`（加量包）
- `credit_package`：新增 `status` 字段（active/expired/revoked）

**新增表：**
| 表名 | 核心字段 | 说明 |
|------|---------|------|
| `membership_config` | id, key（如 `monthly_credits`, `addon_price_cents`, `addon_credits`, `addon_validity_months`）, value, updated_by, updated_at | 运营可编辑配置 |
| `credit_estimation_coefficient` | id, provider, model, completion_prompt_ratio, prompt_char_to_token_ratio, version, updated_by, updated_at | R2 估算系数 |
| `credit_reservation` | id, user_id, reference_type（sop_run/sop_chat/salesrag/...）, reference_id, reserved_credits, reserved_from_subscription, reserved_from_addon, status（reserved/reconciled/refunded）, created_at, reconciled_at | 预扣记录，事后对账依据 |
| `tier_change_log`（可能已存在，待确认） | user_id, from_tier, to_tier, from_billing_mode, to_billing_mode, reason, created_at | 扩展现有 TierChangeLog 以记录 billing_mode 切换 |

### API 端点概要（新增）

**C 端（`/v1/credits/*`）：**
- `GET /v1/credits/balance` — 返回当前用户积分明细（会员积分、加量包积分、各自过期时间）
- `POST /v1/credits/estimate` — 接收 operation + context（如 sop_template_id），返回预估积分数
- `POST /v1/credits/addon-orders` — 创建加量包订单（会员资格校验）
- `GET /v1/credits/packages` — 返回用户所有 CreditPackage 列表（历史记录）

**管理端（`/v1/admin/*`）：**
- `POST/GET/PUT/DELETE /v1/admin/estimation-coefficients` — 估算系数 CRUD
- `GET/PUT /v1/admin/membership-config` — 会员配置读取/更新（带 audit log）
- `POST /v1/admin/users/:id/credits/grant` — 手动发放积分
- `POST /v1/admin/users/:id/credits/revoke` — 手动回收积分
- `GET /v1/admin/users/:id/credit-packages` — 查看用户所有积分包
- `POST /v1/admin/migrations/billing-mode-init` — 一次性迁移工具（幂等）

**内部调用（供 biz/sop 等复用，不暴露 HTTP）：**
- `biz/credit.EstimateCredits(ctx, operation, prompt string, modelKey string) (int64, error)`
- `biz/credit.ReserveCredits(ctx, userID, credits, referenceType, referenceID) (*Reservation, error)`
- `biz/credit.ReconcileReservation(ctx, reservationID, actualCostCents int64) error`

### S5 验证策略（预判，S3 将正式定义）

**必须 Playwright E2E 持久回归**（参照 `.claude/rules/ndf-enforcement.md` 规则 10）：
- 涉及支付、权限、会员等级的高风险业务逻辑
- 关键 E2E 路径：新购会员后积分余额正确 / 会员运行 SOP 扣减正确 / 会员购买加量包成功 / 积分耗尽后弹窗 / 旧制用户运行无积分扣减 / 非会员购买加量包被拒绝

### 需要客户在 S1 Gate 拍板的开放问题

1. **P4a：`legacy_tier` 会员是否可购买加量包？**
   - 可以：混合使用（旧制跑 SOP + 加量包积分等到期后再用）
   - 不可以：加量包只卖给 `credits` 用户
   - **建议：不可以**（避免状态混乱，加量包只在新制内有语义）
2. **P4b：卡片生成是否走 LLM？**
   - 需确认 `biz/sop` 中卡片生成路径是否涉及 LLM 调用。如是：需计费。如否：不计费
   - **建议：S3 plan Task 2 做代码审计确认**
3. **P4c：加量包过期清理归属**
   - Lazy check（扣减时判断过期）+ daily cron 做 status 字段标记
   - **建议：采用此方案**（免除实时批量删除风险）
4. **P4d：首次上线时已有的 `trial` 用户如何处理？**
   - 维持 `legacy_tier`（沿用旧 trial 规则到期）
   - **建议：采用此方案**
