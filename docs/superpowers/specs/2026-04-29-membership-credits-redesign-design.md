# 会员积分体系重构 — 技术设计 Spec

> **项目**：membership-credits-redesign
> **NDF Stage**：S2（技术设计）
> **创建日期**：2026-04-29
> **关联工件**：
> - S0 需求卡：`numind-server/requirements/membership-credits-redesign.md`
> - S1 提案+PRD：`numind-server/proposals/membership-credits-redesign-proposal.md`
> - 锁定决策日志：`numind-server/build-manifest.yaml` 中 `membership-credits-redesign` 条目
> **涉及仓库**：numind-server / numind-web-v3 / numind-admin-web

---

## 0. 文档说明

本 spec 是 PRD 的技术翻译，所有架构决策已锁定（见 build-manifest.yaml decisions 数组）。

**核心架构变更**：当前 `credit_package` 单表 + cron 状态机模型 → 5 张新表 + 时间驱动 + lazy 创建模型。

**5 张新表**：
1. `subscription` — 单行/用户的会员订阅状态
2. `trial_grant` — 每用户 lifetime 单次 trial 包
3. `credit_cycle` — 月度配额，懒创建
4. `user_booster_balance` — 单行/用户的 booster 余额，永不过期
5. `membership_event` — append-only 事件日志，B2B 账单与审计真相源

**关键设计原则**：
- 时间驱动而非状态机（无 cron 维护正确性）
- 半开区间 `[start, end)` 全系统统一
- 锁顺序固定：`user_id ASC → 表名字典序`
- LLM 调用 OUT-OF-tx，复用现有 Reserve/Reconcile
- legacy 字段保留单向只读
- anchor = `current_started_at`；`anchor_add_months` 应用层算
- 扣减优先级：trial → cycle → booster

---

## §1 概览

> 本章是 spec 的导览层：先固化贯穿全 spec 的不变量（§1.1），再列出本次重构会触碰的所有代码点（§1.2），最后用映射表把 PRD 的每条 AC/EC/US 钉在 spec 后续具体章节上（§1.3）。后续 §2-§10 的所有设计都必须满足本章列出的不变量；任何与之冲突的设计需回到本章修订。

---

### §1.1 设计原则与不变量

以下 13 条 invariant 在 spec 全程生效，编码时（S4）每个 task 的 reviewer 必须照此清单逐条检查。

**I-1（时间驱动而非状态机）**
会员、cycle、trial 的"是否在期"完全由 `now` 与时间字段（`expires_at`、`cycle_end`、`trial_grant.expires_at`）的比较得出。**禁止**引入 `status` 枚举（如 active / expired / pending）作为权威状态来源。所有"状态"概念都是从时间字段派生的视图，可在应用层实时计算，但不持久化为可写字段。

**I-2（无 cron 维护正确性）**
系统正确性不依赖任何后台定时任务。`reconcileBillingMode` / `ActivatePending` / `ExpireActive` / 月度 cycle 重置等定时任务全部移除。所有过期判断都是 `now` 与时间字段的比较；月度 cycle "用到才发"（懒创建）；订阅过期不需要任何主动操作，下一次访问时实时算出"已过期"。

**I-3（半开区间 [start, end) 全系统统一）**
所有时间区间一律用半开区间 `[start, end)`：cycle、subscription、trial_grant 全适用。判定 `now` 是否在区间内：`start <= now AND now < end`。**禁止**任何地方使用闭区间 `[start, end]` 或 `now <= end` —— 边界等值时刻视为已过期/已结束。这一规则与 EC-1、EC-2、EC-7 直接对应。

**I-4（锁顺序固定）**
任何跨表写事务必须按以下顺序获取行锁，**绝不允许**反向：
1. 先按 `user_id ASC` 排序（多用户场景）
2. 同一用户内按表名字典序：`credit_cycle` < `membership_event` < `subscription` < `trial_grant` < `user_booster_balance`

S4 编码阶段所有事务都必须显式注释锁顺序；reviewer 检查这一点视为 P0。

**I-5（LLM 调用 OUT-OF-tx）**
本次重构虽不直接发起 LLM 调用，但下游（SOP 执行扣分）会复用本次新建的扣减接口。**强制约定**：扣减事务（trial → cycle → booster 三表写入）只覆盖 DB 操作，LLM 调用必须在事务外执行，复用现有 Reserve/Reconcile 双阶段：Reserve 锁定预扣额度（事务 A），LLM 调用在事务外（无锁），完成后 Reconcile 多退少补（事务 B）。本规则与现有 `internal/numind/biz/credit/` Reserve/Reconcile 完全一致。

**I-6（legacy 字段单向只读化）**
`user.user_tier` / `user.tier_expires` / `user.monthly_sop_runs` 三个字段在新代码中**只读，绝不写入**。所有原本写这三个字段的位置（payment.go fulfillOrder、tier 升级路径、月度重置 cron）都要改写或删除。读取入口在迁移期保留以兼容历史 admin 报表；spec §10（迁移）会单独列出"何时可彻底 DROP 这三列"的判定条件。

**I-7（事务起点固定 timestamp）**
所有跨表写事务在事务开头取一次 `now := time.Now()`，整个事务内所有时间比较和写入都用这同一个 `now`。**禁止**在事务内多次调用 `time.Now()`（避免毫秒级漂移导致 cycle_end 边界判定不一致）。这一规则保障 EC-1/EC-2 的"边界那一秒"语义在事务内自洽。

**I-8（anchor 锚点 + 应用层 anchor_add_months）**
续费延期不使用 SQL `DATE_ADD INTERVAL N MONTH`（MySQL 行为不可靠，详见 R7）。改为：
- `anchor = subscription.current_started_at`（当前 sub 周期的锚点，过期再开时刷新）
- `anchor_add_months(anchor time.Time, n int) time.Time` 在 Go 应用层实现
- 算法语义：返回日期 day = `min(anchor.day, days_in_month(target_year, target_month))`
- 续费时 `expires_at = anchor_add_months(anchor, total_months_purchased)`，每次重算从 anchor 出发，**禁止**累加 AddDate（避免漂移）

**I-9（subscription / trial_grant 单行 per user）**
- `subscription` 表：`UNIQUE INDEX (user_id)`，每用户至多一行，过期再开覆盖原行（更新 `current_started_at` + `expires_at`，保留 `first_started_at`）
- `trial_grant` 表：`UNIQUE INDEX (user_id)`，强制 lifetime 单次，已存在行则 grant 返回 `ErrTrialAlreadyGranted`
- **禁止**多行版本 / 历史行追加模式 —— 历史变更通过 `membership_event` 审计流水追溯

**I-10（扣减优先级 trial → cycle → booster）**
所有扣减一律按此顺序消耗：
1. 先扣 `trial_grant.remaining_credits`（若该用户有 active trial）
2. 再扣 `credit_cycle.remaining_credits`（当前月度 cycle，懒创建）
3. 最后扣 `user_booster_balance.balance`（仅在用户有 active trial 或 active subscription 时可扣）

任一步扣空则进入下一步；三步合计仍不足则返回 `ErrInsufficientCredits`。本规则对应 AC-6 测试用例。

**I-11（booster 仅会员可用 + 永不过期）**
`user_booster_balance` 余额永不过期（无 `expires_at` 字段），但**冻结条件**：用户必须有 active trial 或 active subscription（`trial_grant.expires_at > now OR subscription.expires_at > now`）才能扣减或自购。条件不满足时：
- 余额保留（不清零、不重置）
- 扣减跳过 booster（视为 0 余额，AC-7）
- 自购返回 `ErrNotActiveMember`（AC-13c、EC-11）
- 父账户 grant booster 给非会员子账户返回 `ErrChildNotMember`（EC-5）

会员重新开通后自动解冻，余额可用（AC-8）。

**I-12（DATETIME(0) 精度 + 服务器 UTC+8）**
所有时间字段使用 `DATETIME(0)`（秒级精度，无毫秒）。服务器统一使用 UTC+8 时区（阿里云 / 火山引擎 / 部署环境一致），避免跨时区计算 day-of-month。Go 端获取时间 `time.Now()` 返回的 `time.Time` 默认携带 Local（UTC+8）；写入 DB 前不做时区转换。

**I-13（sub_granted vs sub_renewed 语义由状态机自动判定）**
`membership_event.event_type` 中 `sub_granted` 与 `sub_renewed` 的区分语义如下：
- `sub_granted` 当且仅当 subscription 行**新建**（首次开通）**或过期再开**（之前 expires_at <= now，本次刷新 current_started_at 重新起算）
- `sub_renewed` 当且仅当**在期 UPDATE expires_at**（之前 expires_at > now，本次仅延长 expires_at，current_started_at 不变）

两者由 `GrantOrRenewSubscription` 入口在事务内根据 `subscription` 当前状态**自动判定**写入哪个 event_type，**调用方不传 event_type 参数**——controller / API 层只表达"用户付了 N 个月"，事件类型完全由 biz 层状态机决定。这一规则保障 AC-21（B2B 账单切换日双口径拼接）能正确分类历史 vs 新事件。

---

### §1.2 受影响代码点清单

按 3 仓库分组，标注每个文件本次需要的动作类型：

#### numind-server（Go 后端）

| 文件 | 动作 | 说明 |
|---|---|---|
| `internal/pkg/model/credit.go` | **重写** | 新增 5 个 GORM model：`Subscription`、`TrialGrant`、`CreditCycle`、`UserBoosterBalance`、`MembershipEvent`；老的 `CreditPackage` 标记为只读迁移期保留 |
| `internal/numind/store/credit.go` | **重写** | 新增 5 个 store 接口（每张表一个），实现 CRUD + 锁定查询 + 段查询；老 store 方法标记 `Deprecated` |
| `internal/numind/biz/credit/credit.go` | **重写** | 余额查询、扣减入口（trial → cycle → booster 三表协作）、Reserve/Reconcile 双阶段保留但底层换表 |
| `internal/numind/biz/credit/grant_membership.go` | **重写** | parent grant trial / Pro / booster 三个路径合并为状态机：grant/renew 不区分，按 subscription 当前状态自动分流 |
| `internal/numind/biz/credit/cron_billing.go` | **删除** | I-2 要求无 cron；该文件包含的 reconcileBillingMode / ActivatePending / ExpireActive 全部移除 |
| `internal/numind/biz/payment/payment.go` | **改写** | `fulfillOrder` 内"订单 → 积分包"映射改为写新 5 表；移除所有 tier rank 判断（I-6） |
| `internal/numind/biz/b2b_billing/b2b_billing.go` | **改写** | 改读 `membership_event`；保留切换日双口径拼接逻辑（AC-21、AC-21a/b/c） |
| `internal/numind/biz/sop/sop.go` | **改写** | `CheckSopPermission` 改用新接口 `HasActiveMembership(ctx, uid)` 替代读取 `user_tier` |
| `internal/numind/router.go` | **改写** | `POST /v1/orders` 支持 booster quantity 字段；`GET /v1/credits/balance` 响应结构变更 |
| `internal/numind/admin_router.go` | **改写** | `POST /v1/users/children/:child_id/grant-membership` 路径已存在，行为按新规改写；新增 `GET /v1/admin/b2b-billing-report` 改读新表 |
| `internal/pkg/errno/credit.go`（或对应文件） | **新增** | 加入 5-7 个新错误码：`ErrTrialAlreadyGranted`、`ErrTrialNotAllowedForActivePro`、`ErrChildNotMember`、`ErrNotActiveMember`、`ErrBoosterQuantityExceedsLimit`、`ErrSubscriptionNotFound`（AC 错误码清单） |
| `migrations/YYYYMMDD_HHMMSS_membership_credits_redesign.sql` | **新增** | 创建 5 张新表 + 索引 + UNIQUE 约束；不直接 DROP 老表（DROP 留到 T+7d） |
| `migrations/YYYYMMDD_HHMMSS_legacy_user_field_readonly.sql` | **新增** | 保留 `user_tier` / `tier_expires` / `monthly_sop_runs` 列，仅在注释说明 deprecated |
| `scripts/2026-04-29-membership-credits-migration/dry-run.sql` | **新增** | 段合并 dry-run，输出每用户迁前/迁后总余额对比 |
| `scripts/2026-04-29-membership-credits-migration/apply.sql` | **新增** | 实际数据搬迁，按段合并算法写入 5 张新表 |
| `scripts/2026-04-29-membership-credits-migration/verify.sql` | **新增** | 对账 SQL：每用户迁前 credit_package 总余额 vs 迁后 5 表合计必须 0 差异 |
| `scripts/2026-04-29-membership-credits-migration/rollback.sql` | **新增** | 从 backup 表恢复 + 删除 5 表新写入行 |
| `internal/numind/controller/v1/credit/*.go` | **改写**（目录已存在）| 余额查询 controller 适配新响应结构；booster 自购 controller 加 quantity 校验入口；按现有目录拆分（balance.go / orders.go 等），非新建目录 |
| `internal/numind/controller/v1/admin_b2b/*.go` | **改写**（目录已存在）| B2B 月度账单 controller 适配双口径拼接；目录与 `admin/` 平级（注意：管理端 b2b 路由放 `admin_b2b/` 而非 `admin/` 子目录），改写现有文件而非新建目录 |
| `cmd/numind/main.go` / `cmd/numind-admin/main.go` | **改写** | 移除 cron 启动代码（I-2） |

#### numind-web-v3（Vue 用户端）

| 文件 | 动作 | 说明 |
|---|---|---|
| `src/api/credits.ts` | **改写** | 余额接口响应类型适配新结构（trial / cycle / booster 三段独立字段） |
| `src/stores/credits.ts` | **改写** | Pinia store 状态结构调整；新增 booster 冻结判定 computed |
| `src/views/CreditsView.vue`（或 `views/account/CreditsView.vue`）| **改写** | 余额页 UI 重写：3 张卡片（会员状态、积分余额三段、加量包入口） |
| `src/components/credits/BalanceCard.vue` | **新增** | 复用组件，展示一段积分（trial / cycle / booster 各一个实例） |
| `src/components/credits/BoosterPurchaseDialog.vue` | **新增** | booster 购买弹窗：1/5/10 快捷按钮 + 自定义输入框 + 总价实时计算 + 微信/支付宝下单跳转 |
| `src/views/CustomersView.vue` | **改写** | 客户管理列表新增"会员状态"列（综合显示 trial + Pro 叠加状态）；操作菜单"开通会员"弹窗内 trial 选项已购置灰 |
| `src/components/customers/GrantMembershipDialog.vue`（已存在或新增）| **改写** | 弹窗 UI 适配 trial 已购置灰逻辑；提交逻辑保持调 `POST /v1/users/children/:child_id/grant-membership`（API 不变） |
| `e2e/credits-balance.spec.ts` | **新增** | E2E：余额页 4 状态（loading/empty/error/success）渲染验证 |
| `e2e/booster-purchase.spec.ts` | **新增** | E2E：会员购买 booster 完整链路（选份数 → 下单 → 回调 → 余额刷新） |
| `e2e/booster-frozen.spec.ts` | **新增** | E2E：会员到期后 booster 冻结提示 + 入口禁用 |

#### numind-admin-web（Vue 管理端）

| 文件 | 动作 | 说明 |
|---|---|---|
| `src/views/B2BBilling.vue` | **新增** | B2B 月度账单页（路径 `/b2b-billing`）：月份选择器 + 父账户筛选 + 父账户分组主表 + 事件明细展开 + 总计 + CSV 导出 |
| `src/api/b2bBilling.ts` | **新增** | 调 `GET /v1/admin/b2b-billing-report?month=YYYY-MM` |
| `src/router/index.ts` | **改写** | 注册 `/b2b-billing` 路由 |
| `src/views/UsersView.vue`（如管理端有用户列表）| **改写** | 用户详情页若展示会员状态/积分，适配新接口 |

---

### §1.3 PRD AC/EC/US ↔ Spec 章节映射表

> 用途：Spec 每个后续章节应该精确"覆盖"PRD 的某些条目；reviewer 在 S4 验收时按此表反查"哪些条目在哪个章节兑现"。三个独立子表分别对应 AC（验收标准）、EC（边界情况）、US（用户故事）。
>
> **章节编号对照**：§2 数据模型 / §3 核心算法 / §4 并发与事务 / §5 API 契约 / §6 迁移策略 / §7 切换日双口径拼接 / §8 前端契约 / §9 验证策略 / §10 部署与回滚。

#### §1.3.1 AC（验收标准）→ Spec 章节

| PRD 编号 | 简述 | Spec 章节 |
|---|---|---|
| AC-1 | 新建 sub：`first_started_at` = `current_started_at` = now；`expires_at` = `anchor_add_months(current_started_at, N)` | §2.1 subscription 表 + §3.2 GrantOrRenewSubscription（new 分支） |
| AC-2 | 在期续费：`current_started_at` 不变、`expires_at` = `anchor_add_months(current_started_at, total_purchased + N)` | §3.1 anchor_add_months + §3.2 GrantOrRenewSubscription（renew 分支） |
| AC-3 | 过期再开：`current_started_at` = now、`first_started_at` 不变、`total_purchased` 重置为 N | §3.2 GrantOrRenewSubscription（expired-reopen 分支） |
| AC-4 | anchor-restore 算法测试覆盖 1/31 → 2/28 → 3/31 → 4/30 → 5/31 | §3.1 anchor_add_months 伪代码 + §9.1 单元测试 |
| AC-5 | trial_grant UNIQUE(user_id)；重复 grant 返回 `ErrTrialAlreadyGranted` | §2.2 trial_grant 表 + §3.3 GrantTrial + §5.7 错误码 |
| AC-6 | 扣减优先级测试：trial 200 + cycle 2000 + booster 1200 → 扣 250 后 trial 0 / cycle 1950 / booster 1200 等三组场景 | §3.5 DeductCredits + §9.1 单元测试 |
| AC-7 | 会员到期后扣减自动跳过 booster | §3.5 DeductCredits（booster 冻结判定）+ §3.6 GetMembershipState |
| AC-8 | 会员重新开通后 booster 自动解冻 | §3.5 DeductCredits（重新生效路径） |
| AC-9 | 月度 cycle 懒创建 + 并发 UNIQUE 索引保护 | §2.3 credit_cycle 表 + §3.4 ensureCurrentCycle + §4.4 懒创建并发证明 |
| AC-10 | sub 过期则同期 cycle 余额作废 | §3.4 ensureCurrentCycle（cycle.cycle_end ≤ sub.expires_at 约束）+ §3.5 DeductCredits |
| AC-11 | membership_event 写入带 idempotency_key；重复请求不重复入账 | §2.5 membership_event 表 + §4.5 idempotency_key 协议 |
| AC-12 | `POST /v1/users/children/:child_id/grant-membership` 支持 product_type ∈ {trial, monthly}, months ∈ [1,12] | §5.1 grant-membership 端点 + §5.7 错误码 |
| AC-13 | `POST /v1/orders` booster quantity ≥1 ≤10000；总额 = quantity × 2990 | §5.2 orders 端点 + §5.7 错误码（ErrBoosterQuantityExceedsLimit） |
| AC-13b | booster 总余额无上限；反作弊 backlog | §2.4 user_booster_balance 表（无上限注释） + 反作弊 out-of-scope（见 §1.4） |
| AC-13c | booster 自购前端禁用 + 后端兜底校验 | §5.2 POST /v1/orders 校验流程 + §5.7 错误码（ErrNotActiveMember）+ §8.2 购买弹窗禁用规则 |
| AC-14 | `GET /v1/credits/balance` 返回新结构（trial_remaining / cycle_remaining / cycle_end / booster_total / booster_usable / membership_state） | §5.3 balance 端点响应 schema + §3.7 GetBalance |
| AC-15 | `GET /v1/admin/b2b-billing-report?month=YYYY-MM` 改读 membership_event | §5.6 admin b2b-billing-report 端点 + §7 双口径拼接 |
| AC-16a | 父账户两 tab 不同 idempotency_key 续费 → expires_at += 2 | §4.5 idempotency_key 协议 + §9.2 并发压测用例 2a |
| AC-16b | 同一点击网络重发（同 idempotency_key）→ expires_at += 1，event 仅 1 条 | §4.5 idempotency_key UNIQUE + §9.2 并发压测用例 2b |
| AC-17 | 0.5 秒 5 次扣分请求 → cycle 1 行；扣减结果与单线程一致 | §3.4 ensureCurrentCycle + §4.4 懒创建并发证明 + §9.2 并发压测用例 1 |
| AC-18 | B2B 账单 SQL 在 100 万条 event 下查询 < 500ms | §2.5 membership_event 索引 + §2.6 索引策略 + §9.2 性能验证 |
| AC-19 | 迁移 dry-run 输出每用户迁前/迁后总余额对比，差额必为 0 | §6.1 段合并算法 + §6.2 dry-run.sql |
| AC-20 | 迁移 apply 后立即跑对账 SQL；任何差异触发 rollback | §6.2 verify.sql + §6.3 对账 SQL 模板 + §10.3 回滚决策矩阵 |
| AC-21 | B2B 账单切换日分界口径（选项 A） | §7 双口径拼接（整章） |
| AC-21a | 跨切换日字段映射规则常量化 | §7.1 字段映射表 |
| AC-21b | 跨切换日去重规则（复合键）| §7.2 复合键去重 |
| AC-21c | 跨切换日金额单位统一为 cents (int64) | §7.3 SQL 框架（金额单位归一） |
| AC-22 | 用户端余额页 4 状态都正确渲染 | §8.1 余额组件 4 状态处理 |
| AC-23 | booster 冻结状态前端显示余额 + 灰标 + CTA | §8.1 booster 冻结 UI |
| AC-24 | 父账户客户管理 trial 已购置灰 + hover 提示 | §8.3 客户管理页双状态显示 |
| AC-25 | admin B2B 账单页支持月份选择 + 分组展开 | §8.4 B2B 月度账单页 |

#### §1.3.2 EC（边界情况）→ Spec 章节

| PRD 编号 | 简述 | Spec 章节 |
|---|---|---|
| EC-1 | cycle_end 那一秒发起扣分 → 半开区间严格判定 | §3.4 ensureCurrentCycle（半开区间）+ §4.6 事务起点 ts |
| EC-2 | sub.expires_at 那一秒发起扣分 → 事务起点固定 ts | §4.6 事务起点固定 timestamp 模式 |
| EC-3 | 父账户给已有 trial（在期或历史已 grant）的子账户再次 grant trial | §3.3 GrantTrial step 2（trial_grant 表 UNIQUE 命中）+ §5.7 ErrTrialAlreadyGranted |
| EC-4 | 父账户给已在期 Pro 的子账户 grant trial | §3.3 GrantTrial step 3（subscription 在期判定）+ §5.7 ErrTrialNotAllowedForActivePro |
| EC-4b | 父账户给历史已购买过 trial 的子账户再 grant（即便当前已过期） | §3.3 GrantTrial step 2（trial_grant lifetime 单行）+ §5.7 ErrTrialAlreadyGranted |
| EC-5 | parent grant booster 给非会员子账户 → `ErrChildNotMember` | §5.2 POST /v1/orders 校验 + §5.7 错误码 |
| EC-6 | 买 12 月 Pro 从未登录使用 → 不预创建任何 cycle | §3.4 ensureCurrentCycle（懒创建）+ §2.3 credit_cycle 表 |
| EC-7 | cycle 边界精确语义：`cycle_end = min(anchor_add_months(...), sub.expires_at)` | §3.4 ensureCurrentCycle（cycle_end 计算）+ §3.1 anchor_add_months |
| EC-8 | 迁移期间用户扣分 → maintenance window 503 拒绝 | §6.4 maintenance window runbook + §10.1 maintenance mode 部署流 |
| EC-9 | event 写入失败但 sub 写入成功 → 同事务回滚 + 幂等键重试 | §4.2 事务边界 + §4.5 idempotency_key 协议 |
| EC-10 | trial + Pro 叠加期内退出又登录 → 状态正常切换 | §3.6 GetMembershipState（混合状态规则）+ §8.1 余额组件状态渲染 |
| EC-11 | booster 冻结期 API 兜底校验 → `ErrNotActiveMember` | §5.2 POST /v1/orders 兜底校验 + §5.7 错误码 + §8.2 购买弹窗禁用 |

#### §1.3.3 US（用户故事）→ Spec 章节

| PRD 编号 | 简述 | Spec 章节 |
|---|---|---|
| US-1 | C 端子账户登录后看到完整余额（会员状态 + trial + cycle + booster）| §5.3 GET /v1/credits/balance + §8.1 余额组件 |
| US-2 | 试用期内被 grant Pro，立即拥有 Pro 功能但前端展示仍"试用中" | §3.2 GrantOrRenewSubscription + §3.6 GetMembershipState（混合状态）+ §8.1 状态渲染 |
| US-3 | 已过期会员看到 booster 余额 + "需要会员"标记 + 续费 CTA | §3.7 GetBalance（booster_usable 字段）+ §8.1 booster 冻结 UI |
| US-4 | 会员自购 booster 多份，完整支付时序（下单 → 回调 → 刷新 → 失败重试）| §5.2 POST /v1/orders（quantity）+ §8.2 BoosterPurchaseDialog |
| US-5 | 父账户 grant trial / 开通+续费 Pro，trial 已购置灰；API 不区分 grant/renew | §5.1 grant-membership 端点 + §3.2 GrantOrRenewSubscription（自动分流） |
| US-6 | 父账户客户管理列表看子账户完整状态；booster 余额对父不可见（隐私） | §5.4 父账户子账户余额查询（booster 字段裁剪）+ §8.3 客户管理页 |
| US-7 | 父账户月底拿到本月所有 grant/续费/购买事件账单 | §5.6 b2b-billing-report 端点 + §7 双口径拼接 |
| US-8 | admin 查询历史 grant 事件（按时间/父账户/事件类型筛选） | §5.6 b2b-billing-report 筛选参数 + §8.4 B2BBilling 页筛选 UI |

---

### §1.4 Out-of-Scope（本次 spec 不实现，记录在 backlog）

- **退款流程**：trial / sub / booster 退款（PRD §5 决策已锁，不实现）。事件日志预留 `sub_revoked` event_type 但无 controller/biz 路径
- **到期提醒**：会员到期前的前端轮询提醒、邮件推送（PRD §5 决策已锁，不实现）
- **booster 反作弊与累计上限**：单用户余额无上限（决策 Q4），后续若发现刷单可加全局 booster 余额上限或单日购买频次限制（backlog）
- **legacy 字段物理 DROP**：`user.user_tier` / `user.tier_expires` / `user.monthly_sop_runs` 保留为只读字段；切换日 +30 天后视情况发起独立 feature 物理 DROP
- **credit_package 表 DROP**：保留只读 7 天用于应急对账；T+7 天后视情况 DROP（独立 feature）
- **年订阅产品（yearly product_type）**：当前只支持 monthly product_type（months 1-12 用 monthly 表达），yearly 单 SKU + 折扣价是后续产品扩展（backlog）

---

> 本章到此结束。后续章节按本章列出的 13 条 invariant 设计具体方案；任何与 invariant 冲突的设计应回到 §1.1 修订并触发 spec 重审。

---

## §2 数据模型

本节固化新会员积分体系的全部物理数据结构。共 5 张新表（`subscription` / `trial_grant` / `credit_cycle` / `user_booster_balance` / `membership_event`），1 个索引策略汇总，以及对历史 4 张旧表（`credit_package` / `credit_account` / `billing_account` / `legacy_tier_migration_backup_20260424`）的处置说明。

**全局约定**：

- 字符集：`utf8mb4` / `utf8mb4_0900_ai_ci`
- 引擎：`InnoDB`
- 时间精度：所有时间字段使用 `DATETIME(0)`（秒级精度），统一存 UTC 实际时间戳
- 主键：`BIGINT UNSIGNED AUTO_INCREMENT`
- 外键：本期不在 DB 层创建 `FOREIGN KEY` 约束（保持与现有项目一致——逻辑外键由应用层 + 索引保证），所有外键列建普通索引
- 命名：表名小写下划线、索引名 `idx_{tablealias}_{col1}_{col2}`、唯一索引 `uniq_{tablealias}_{col}`
- 钱：所有金额一律 `BIGINT NOT NULL DEFAULT 0`，单位 cents（与现有 `billing_account.balance_cents` 保持一致）
- 积分：所有积分一律 `INT NOT NULL DEFAULT 0`，单位个（与 200/2000/600 自然数一致；范围 `INT` 足够：单用户即便累计 100 万 booster 也只到 6 亿，远在 INT 上限内；唯一例外是 `user_booster_balance.credits_remaining` 用 `BIGINT` 留余量，详见 §2.4）
- 所有 DDL 文件可直接 `mysql {db} < migrations/2026MMDD_HHMMSS_*.sql` 执行，幂等使用 `IF NOT EXISTS`

---

### §2.1 subscription —— 用户订阅主表

#### 用途

每个曾经购买（含 grant）过 monthly Pro 的用户都恰好有一行。该行随用户的所有订阅生命周期变化原地更新，**不再每次续费/再开都新增一行**（与旧 `credit_package` 表"每个月卡一行"的模型本质区别）。

#### 完整 DDL

```sql
CREATE TABLE IF NOT EXISTS `subscription` (
  `id`                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`                  BIGINT UNSIGNED NOT NULL,
  `first_started_at`         DATETIME(0)     NOT NULL,
  `current_started_at`       DATETIME(0)     NOT NULL,
  `expires_at`               DATETIME(0)     NOT NULL,
  `total_months_purchased`   INT             NOT NULL,
  `source`                   ENUM('self_purchase','b2b_grant') NOT NULL DEFAULT 'b2b_grant',
  `granter_user_id`          BIGINT UNSIGNED          DEFAULT NULL,
  `created_at`               DATETIME(0)     NOT NULL,
  `updated_at`               DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_sub_user_id` (`user_id`),
  KEY `idx_sub_expires_at` (`expires_at`),
  KEY `idx_sub_granter_expires` (`granter_user_id`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='用户订阅主表，每个用户单行，原地更新';
```

#### 字段说明

| 字段 | 类型 | 用途 | 约束 | 默认值 | 可空 |
|---|---|---|---|---|---|
| `id` | BIGINT UNSIGNED | 主键 | PK / AUTO_INCREMENT | — | 否 |
| `user_id` | BIGINT UNSIGNED | 订阅归属用户 | UNIQUE（lifetime 单行） | — | 否 |
| `first_started_at` | DATETIME(0) | 用户**有史以来第一次**开通 Pro 的时刻，永不变动 | — | — | 否 |
| `current_started_at` | DATETIME(0) | 当前**连续订阅段**起始时刻；过期再开时更新为新的 `now()`；在期续费时**不变** | — | — | 否 |
| `expires_at` | DATETIME(0) | 当前订阅到期时刻；新开通时 = `anchor_add_months(current_started_at, N)`；续费时 = `anchor_add_months(current_started_at, total_months_purchased)`；**严格半开判断 `now < expires_at` 视为在期** | — | — | 否 |
| `total_months_purchased` | INT | 在**当前 sub 周期**（即从 `current_started_at` 起的连续段）内累计已购月数；过期再开时重置为新购月数 | `>0` 不变量（应用层校验） | 无（NOT NULL 不带 DEFAULT，强制应用层显式 INSERT） | 否 |
| `source` | ENUM | `self_purchase` 表示用户自购、`b2b_grant` 表示父账户帮开 | ENUM 二选一 | `b2b_grant` | 否 |
| `granter_user_id` | BIGINT UNSIGNED | `source='b2b_grant'` 时记录开通该订阅的父账户 ID；`self_purchase` 时为 NULL | — | NULL | 是 |
| `created_at` | DATETIME(0) | 行创建时刻 | 由应用层填 `now()` | — | 否 |
| `updated_at` | DATETIME(0) | 行最近一次更新时刻 | 由应用层每次 UPDATE 时显式 SET | — | 否 |

> **B2C 自购允许性说明**：当前已锁定决策（§5 PRD 权限矩阵）C 端不允许自购 monthly Pro，`source='self_purchase'` 在 monthly 场景下**不会被任何代码路径写入**。enum 仍保留该值是为对齐 `trial_grant` / `membership_event` / `user_booster_balance` 同名字段（schema 一致性 > 微小冗余），未来若开放 C 端自购也无需改 schema。

#### 索引清单

| 名称 | 类型 | 列 | 用途 |
|---|---|---|---|
| `PRIMARY` | 主键 | `id` | 标准主键 |
| `uniq_sub_user_id` | UNIQUE | `user_id` | lifetime 单行约束；并发新建时由该索引兜底防双行 |
| `idx_sub_expires_at` | 普通 | `expires_at` | admin 查询"近期到期会员"批量统计；非强制每查询用 |
| `idx_sub_granter_expires` | 普通 | `(granter_user_id, expires_at)` | B2B 父账户视角："这个父账户帮哪些子账户开通了 Pro，以及哪些尚在期"——复合索引避免父账户子账户列表查询时回表过滤 expires_at |

#### 不变量

- `current_started_at <= expires_at`（应用层每次 UPDATE 后自检，单元测试覆盖）
- `first_started_at <= current_started_at`（**首次创建时相等**，过期再开时严格小于）
- `total_months_purchased > 0`（行存在即代表至少买过 1 个月；应用层 INSERT/UPDATE 前校验）
- `(source='b2b_grant') XOR (granter_user_id IS NULL)` 反向蕴含（应用层校验，不在 DB 层加 CHECK 约束以保留运维灵活性）
- `expires_at` 永远是某次 `anchor_add_months` 调用的精确返回值，不会出现"应用层手算的奇怪日期"
- `user_id` 不变（用户不会换账户绑定订阅）

---

### §2.2 trial_grant —— 试用包记录

#### 用途

每个用户**最多有一行**（lifetime 单次的物理强制载体）。试用包不像订阅那样有"续期"概念——只有"granted"和"已 granted 过"两个状态。

#### 完整 DDL

```sql
CREATE TABLE IF NOT EXISTS `trial_grant` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `granted_at`          DATETIME(0)     NOT NULL,
  `expires_at`          DATETIME(0)     NOT NULL,
  `credits_remaining`   INT             NOT NULL DEFAULT 200,
  `source`              ENUM('self_purchase','b2b_grant') NOT NULL DEFAULT 'b2b_grant',
  `granter_user_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `created_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_trial_user_id` (`user_id`),
  KEY `idx_trial_expires_at` (`expires_at`),
  KEY `idx_trial_granter_expires` (`granter_user_id`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='试用包记录，每个用户 lifetime 单行';
```

#### 字段说明

| 字段 | 类型 | 用途 | 约束 | 默认值 | 可空 |
|---|---|---|---|---|---|
| `id` | BIGINT UNSIGNED | 主键 | PK / AUTO_INCREMENT | — | 否 |
| `user_id` | BIGINT UNSIGNED | 试用包归属用户 | UNIQUE（lifetime 单行） | — | 否 |
| `granted_at` | DATETIME(0) | 试用包发放时刻 | — | — | 否 |
| `expires_at` | DATETIME(0) | 试用包到期时刻 = `granted_at + 3 days`（应用层固定 3 天，不接受自定义） | — | — | 否 |
| `credits_remaining` | INT | 试用积分剩余；扣减优先级最高 | `>= 0` 不变量 | 200 | 否 |
| `source` | ENUM | 同 §2.1，目前 grant 路径只产生 `b2b_grant`；C 端自购被 `ErrSelfPurchaseDisabled` 拒绝 | — | `b2b_grant` | 否 |
| `granter_user_id` | BIGINT UNSIGNED | 父账户 ID | — | NULL | 是 |
| `created_at` | DATETIME(0) | 行创建时刻；与 `granted_at` 永远相等（行只会创建不会更新） | — | — | 否 |

> **无 `updated_at` 字段**：该表行创建后**永不更新除 `credits_remaining` 之外**的字段。`credits_remaining` 的变化由扣减事务的 `UPDATE` 语句直接修改，并通过 `membership_event`（如未来加 deduction 类事件）+ `usage_record` 间接审计，不需要单独 `updated_at`。

#### 索引清单

| 名称 | 类型 | 列 | 用途 |
|---|---|---|---|
| `PRIMARY` | 主键 | `id` | 标准主键 |
| `uniq_trial_user_id` | UNIQUE | `user_id` | **lifetime 单次的物理强制点**——重复 grant 由该索引返回 1062 错误，应用层捕获并返回 `ErrTrialAlreadyGranted` |
| `idx_trial_expires_at` | 普通 | `expires_at` | 用户余额查询时配合 `now < expires_at` 判断试用是否在期 |
| `idx_trial_granter_expires` | 普通 | `(granter_user_id, expires_at)` | B2B 月度账单按父账户聚合，并支持"父账户在期 trial 子账户"视图——复合索引避免回表 |

#### 不变量

- `credits_remaining >= 0`（扣减事务保证，永远不允许负值）
- `granted_at <= expires_at`（应用层 INSERT 前校验；正常情况 `expires_at = granted_at + 3 * 24h`）
- `expires_at = granted_at + 3 days`（固定 3 天，应用层校验；不接受调用方自定义）
- `(source='b2b_grant') XOR (granter_user_id IS NULL)`（应用层校验）
- 行一旦创建，`granted_at` / `expires_at` / `source` / `granter_user_id` 永不更新

---

### §2.3 credit_cycle —— 月度积分周期

#### 用途

记录用户在订阅期内的**每一个月度积分发放周期**。**懒创建**：用户 buy 12 个月 Pro 后从未登录，则 12 个月内 0 行；用户首次扣分时才插入"当前 cycle"行。同一订阅周期内每个月最多一行（UNIQUE 索引保证）。

#### 完整 DDL

```sql
CREATE TABLE IF NOT EXISTS `credit_cycle` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `subscription_id`     BIGINT UNSIGNED NOT NULL,
  `cycle_start`         DATETIME(0)     NOT NULL,
  `cycle_end`           DATETIME(0)     NOT NULL,
  `credits_granted`     INT             NOT NULL DEFAULT 0,
  `credits_remaining`   INT             NOT NULL DEFAULT 0,
  `created_at`          DATETIME(0)     NOT NULL,
  `updated_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_cycle_user_start` (`user_id`, `cycle_start`),
  KEY `idx_cycle_user_end` (`user_id`, `cycle_end`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='月度积分周期，懒创建';
```

#### 字段说明

| 字段 | 类型 | 用途 | 约束 | 默认值 | 可空 |
|---|---|---|---|---|---|
| `id` | BIGINT UNSIGNED | 主键 | PK / AUTO_INCREMENT | — | 否 |
| `user_id` | BIGINT UNSIGNED | 周期归属用户 | 与 `cycle_start` 复合 UNIQUE | — | 否 |
| `subscription_id` | BIGINT UNSIGNED | 关联到 `subscription.id`；周期挂在某个具体的 sub 行下 | — | — | 否 |
| `cycle_start` | DATETIME(0) | 周期起始时刻；= `anchor_add_months(subscription.current_started_at, cycle_index)`；与 `user_id` 复合 UNIQUE 防并发双插 | — | — | 否 |
| `cycle_end` | DATETIME(0) | 周期结束时刻；= `min(anchor_add_months(subscription.current_started_at, cycle_index + 1), subscription.expires_at)` | `>= cycle_start` | — | 否 |
| `credits_granted` | INT | 本周期发放的积分总额；目前产品配置 = 2000，但 schema 不承担产品语义——应用层（biz/credit/）从产品配置常量显式注入 INSERT；保留字段为未来"不同档位月卡"留接口 | `> 0` 不变量（应用层校验：INSERT 时不允许 0） | 0 | 否 |
| `credits_remaining` | INT | 本周期剩余可用积分；扣减优先级中等（trial 之后、booster 之前）；INSERT 时由应用层显式设为 = `credits_granted` | `>= 0` 不变量 | 0 | 否 |
| `created_at` | DATETIME(0) | 懒创建时刻；与 `cycle_start` **不一定相等**——cycle_start 可以是过去时（用户 1 月开通、5 月才首次扣分时 cycle_start 仍是 5/1） | — | — | 否 |
| `updated_at` | DATETIME(0) | 最近一次扣减时刻 | — | — | 否 |

> **关于 `cycle_index` 的语义**：`cycle_index` 是从 `subscription.current_started_at` 起算的从 0 开始的整数。`cycle_index=0` 是订阅的第 1 个月、`cycle_index=11` 是第 12 个月。该值**不存数据库**，由应用层根据 `now()` 与 `subscription.current_started_at` 通过 `anchor_add_months` 反算得出（详见 §3 行为契约的 cycle 计算算法）。

> **半开区间约定**：`cycle_end` 是**排除值**——`cycle_start <= now < cycle_end` 才视为在该 cycle 内。这与 §1 EC-7 一致。

#### 索引清单

| 名称 | 类型 | 列 | 用途 |
|---|---|---|---|
| `PRIMARY` | 主键 | `id` | 标准主键 |
| `uniq_cycle_user_start` | UNIQUE | `(user_id, cycle_start)` | **并发懒创建的物理强制点**——多端同秒首次扣分时由该索引保证只插一行；应用层走 `INSERT ... ON DUPLICATE KEY UPDATE id=id` + 重新 SELECT |
| `idx_cycle_user_end` | 普通 | `(user_id, cycle_end)` | 用户余额查询定位"当前在期 cycle"——`WHERE user_id=? AND cycle_end > now()`；所有读路径（含订阅过期作废判断）均通过 `(user_id, ...)` 入手，无需 `subscription_id` 单列索引 |

#### 不变量

- `cycle_start < cycle_end`（应用层 INSERT 前校验；只有当 `subscription.expires_at <= cycle_start` 时这行不应存在，由懒创建逻辑兜底）
- `credits_remaining >= 0` 且 `credits_remaining <= credits_granted`（扣减事务保证）
- `cycle_start = anchor_add_months(subscription.current_started_at, cycle_index)` 对某个非负整数 `cycle_index` 成立
- `cycle_end = min(anchor_add_months(subscription.current_started_at, cycle_index + 1), subscription.expires_at)`
- `subscription_id` 引用的行必须存在且 `subscription.user_id == credit_cycle.user_id`（应用层每次 INSERT 时校验）
- 同一 `(user_id, subscription_id)` 下，所有 cycle 行的 `(cycle_start, cycle_end)` 区间**两两不重叠且无空隙**（懒创建算法保证）

---

### §2.4 user_booster_balance —— 加量包余额

#### 用途

每个用户**最多一行**。加量包多次购买**只递增 `credits_remaining`**，不创建新行；永不过期；仅在用户处于 active 会员（trial 或 sub 在期）时可用，扣减时由应用层判断会员状态后决定是否走该行。

#### 完整 DDL

```sql
CREATE TABLE IF NOT EXISTS `user_booster_balance` (
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `credits_remaining`   BIGINT          NOT NULL DEFAULT 0,
  `updated_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`user_id`),
  KEY `idx_booster_updated_at` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='加量包余额，永不过期、单用户单行';
```

#### 字段说明

| 字段 | 类型 | 用途 | 约束 | 默认值 | 可空 |
|---|---|---|---|---|---|
| `user_id` | BIGINT UNSIGNED | 余额归属用户；**直接做主键**（无 `id` 列） | PRIMARY KEY | — | 否 |
| `credits_remaining` | BIGINT | 加量包剩余积分；**单笔订单 quantity 上限 10000**（决策 Q2），单笔最多入账 600 × 10000 = 6,000,000；多次累计无上限（决策 Q4），故用 BIGINT 留余量 | `>= 0` 不变量 | 0 | 否 |
| `updated_at` | DATETIME(0) | 最近一次入账或扣减时刻 | — | — | 否 |

> **没有 `created_at` / `id`**：本表是"用户级聚合视图"——`user_id` 直接作主键；行不会被 DELETE，只会从无到有 INSERT 一次后反复 UPDATE。
>
> **没有 `expires_at`**：决策 Q4 + §1 已锁定"booster 永不过期"；冻结/解冻由应用层根据用户当前会员状态判断（详见 §3.X 扣减算法），不在该表落字段。

#### 索引清单

| 名称 | 类型 | 列 | 用途 |
|---|---|---|---|
| `PRIMARY` | 主键 | `user_id` | 单行查找（O(1) 等价） |
| `idx_booster_updated_at` | 普通 | `updated_at` | admin 维度统计（如"近 30 天有 booster 余额变动的用户数"），非必需但成本低 |

#### 不变量

- `credits_remaining >= 0`
- `user_id` 一旦写入，行永远存在（不 DELETE）
- 入账：`UPDATE ... SET credits_remaining = credits_remaining + (quantity * 600), updated_at = NOW()`，永远 `+=`
- 扣减：仅当 `(trial_grant.expires_at > now() OR subscription.expires_at > now())` 时才允许；该判断在应用层完成，DB 层不感知
- 不存在"行存在 + credits_remaining = 0"的特殊语义，0 余额行视同无 booster

---

### §2.5 membership_event —— 会员事件流（append-only）

#### 用途

**所有**会员相关的入账动作（开通试用、开通/续费 Pro、购买加量包）的**完整事件流水**。append-only：写入即不可改、不可删（除运维 archive）。是 B2B 月度账单（§AC-15 / §AC-18）和未来财务对账的**唯一权威数据源**。

#### 完整 DDL

```sql
CREATE TABLE IF NOT EXISTS `membership_event` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `event_type`          ENUM(
                          'trial_granted',
                          'sub_granted',
                          'sub_renewed',
                          'booster_granted'
                        ) NOT NULL,
  `product_type`        ENUM('trial','monthly','booster') NOT NULL,
  `months`              TINYINT UNSIGNED         DEFAULT NULL,
  `quantity`            SMALLINT UNSIGNED        DEFAULT NULL,
  `amount_cents`        BIGINT          NOT NULL DEFAULT 0,
  `source`              ENUM('self_purchase','b2b_grant') NOT NULL,
  `granter_user_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `idempotency_key`     VARCHAR(64)              DEFAULT NULL,
  `subscription_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `occurred_at`         DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_event_idempotency_key` (`idempotency_key`),
  KEY `idx_event_user_occurred` (`user_id`, `occurred_at`),
  KEY `idx_event_granter_occurred` (`granter_user_id`, `occurred_at`),
  KEY `idx_event_type_occurred` (`event_type`, `occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='会员事件流水，append-only，B2B 账单唯一数据源';
```

#### 字段说明

| 字段 | 类型 | 用途 | 约束 | 默认值 | 可空 |
|---|---|---|---|---|---|
| `id` | BIGINT UNSIGNED | 主键 | PK / AUTO_INCREMENT | — | 否 |
| `user_id` | BIGINT UNSIGNED | 事件影响的子账户用户 | — | — | 否 |
| `event_type` | ENUM | `trial_granted` / `sub_granted` / `sub_renewed` / `booster_granted` 四类；`sub_granted` 表示"新建 subscription 行或过期再开"，`sub_renewed` 表示"在期续费延期" | — | — | 否 |
| `product_type` | ENUM | 三选一：`trial` / `monthly` / `booster`；与 `event_type` 一一对应（trial_granted ↔ trial、sub_granted/sub_renewed ↔ monthly、booster_granted ↔ booster） | — | — | 否 |
| `months` | TINYINT UNSIGNED | 仅 `monthly` 事件填写；表示本次操作"购买/续费"的月数 N（业务上限 12，TINYINT UNSIGNED 范围 0-255 充分覆盖） | `monthly` 事件 NOT NULL，且 `1 <= months <= 12`；其他必须 NULL（应用层校验） | NULL | 是 |
| `quantity` | SMALLINT UNSIGNED | 仅 `booster` 事件填写；表示本次购买的份数（业务上限 10000，SMALLINT UNSIGNED 范围 0-65535 充分覆盖） | `booster` 事件 NOT NULL，且 `1 <= quantity <= 10000`；其他必须 NULL（应用层校验） | NULL | 是 |
| `amount_cents` | BIGINT | 本事件对应的金额（cents 单位）；`b2b_grant` 仍记账（按"父账户应付金额"），月末聚合作为对公结算口径；`self_purchase` 同样记账（按用户实付） | `>= 0` 不变量 | 0 | 否 |
| `source` | ENUM | 同 §2.1 | — | — | 否 |
| `granter_user_id` | BIGINT UNSIGNED | `source='b2b_grant'` 时记录父账户 ID；`self_purchase` 为 NULL | — | NULL | 是 |
| `idempotency_key` | VARCHAR(64) | 兜底防重入键；写入时由 controller / biz 层算出（payment 回调用支付平台订单号、grant API 用客户端传入的 key），UNIQUE 索引保证 1062 错误捕获后转 idempotent return | UNIQUE | NULL | 是 |
| `subscription_id` | BIGINT UNSIGNED | `event_type ∈ {sub_granted, sub_renewed}` 时关联到 `subscription.id`；其他事件为 NULL | — | NULL | 是 |
| `occurred_at` | DATETIME(0) | 事件发生时刻；事件流水的"业务时间"，在分布式时钟下以 server `now()` 为准 | — | — | 否 |

> **idempotency_key 可为 NULL 但 UNIQUE**：MySQL UNIQUE 索引允许多个 NULL 值（不冲突），所以"无 idempotency_key 的事件"可以无限多条共存；只有真正提供 idempotency_key 的写入才走 UNIQUE 防重入路径。这是 MySQL 标准行为，与 PostgreSQL 不同（PG 也允许多 NULL，但 nulls-not-distinct 选项另说），应用层不依赖该差异。

> **append-only 的物理强制**：本表**不创建 UPDATE / DELETE 路径**——biz 层只暴露 `Append(event)` 接口，store 层只写 `Create`。运维 archive（按月份切分历史表）由 ops 团队走 OOB 流程，不进 biz 代码。

#### 索引清单

| 名称 | 类型 | 列 | 用途 |
|---|---|---|---|
| `PRIMARY` | 主键 | `id` | 标准主键 |
| `uniq_event_idempotency_key` | UNIQUE | `idempotency_key` | 防重入；NULL 不参与冲突 |
| `idx_event_user_occurred` | 普通 | `(user_id, occurred_at)` | 用户事件查询（"我这个月有哪些会员动作"），覆盖 US-1 余额页面相关明细 |
| `idx_event_granter_occurred` | 普通 | `(granter_user_id, occurred_at)` | **B2B 月度账单核心索引**——`WHERE granter_user_id=? AND occurred_at >= ? AND occurred_at < ?`；100 万事件下 < 500ms（AC-18） |
| `idx_event_type_occurred` | 普通 | `(event_type, occurred_at)` | admin 全局统计（"近 30 天 sub_renewed 事件总数 / 金额总和"） |

#### 不变量

- `amount_cents >= 0`
- `event_type` 与 `product_type` 必须配对（应用层校验）：
  - `trial_granted` ↔ `trial`
  - `sub_granted` ↔ `monthly`
  - `sub_renewed` ↔ `monthly`
  - `booster_granted` ↔ `booster`
- `event_type='trial_granted'` 时 `months IS NULL AND quantity IS NULL`
- `event_type IN ('sub_granted','sub_renewed')` 时 `months IS NOT NULL AND quantity IS NULL`
- `event_type='booster_granted'` 时 `months IS NULL AND quantity IS NOT NULL`
- `event_type IN ('sub_granted','sub_renewed')` 时 `subscription_id IS NOT NULL`
- `(source='b2b_grant') XOR (granter_user_id IS NULL)`
- 行写入后永不更新、永不删除（append-only）
- `occurred_at` 单调由 server 时钟决定，乱序写入允许（旧事件后到不影响业务）
- **应用层禁止主动写 NULL `idempotency_key`**：所有 controller/biz 入口必须保证写入时 `idempotency_key` 非空——payment 回调用支付平台订单号、grant API 用客户端 header / 请求体传入 key、批量补录场景必须显式构造合成 key（如 `"backfill-" + sha256(user_id + ":" + occurred_at + ":" + event_type)`）。NULL 仅作为 schema 兼容兜底（MySQL 多 NULL 不冲突），生产路径不应出现 NULL 行；DB 层不加 NOT NULL 约束是为应急 ops（手动补 patch）保留逃生通道

---

### §2.6 索引策略汇总

#### 全部索引一览（5 表合计 13 个非主键索引 + 5 个主键）

| 表 | 索引名 | 类型 | 列 | 主用途 | 性能预期 |
|---|---|---|---|---|---|
| `subscription` | `PRIMARY` | 主键 | `id` | 主键查找 | <1ms |
| `subscription` | `uniq_sub_user_id` | UNIQUE | `user_id` | 单用户查 sub / 防双插 | <1ms |
| `subscription` | `idx_sub_expires_at` | 普通 | `expires_at` | 范围扫描"近 N 天到期" | 100 万行下 <50ms |
| `subscription` | `idx_sub_granter_expires` | 普通 | `(granter_user_id, expires_at)` | 父账户视角列出子账户订阅 + 在期过滤 | <50ms |
| `trial_grant` | `PRIMARY` | 主键 | `id` | 主键查找 | <1ms |
| `trial_grant` | `uniq_trial_user_id` | UNIQUE | `user_id` | lifetime 单次防 | <1ms |
| `trial_grant` | `idx_trial_expires_at` | 普通 | `expires_at` | 在期 trial 统计 | <50ms |
| `trial_grant` | `idx_trial_granter_expires` | 普通 | `(granter_user_id, expires_at)` | B2B 账单 trial 行 + 在期过滤 | <50ms |
| `credit_cycle` | `PRIMARY` | 主键 | `id` | 主键查找 | <1ms |
| `credit_cycle` | `uniq_cycle_user_start` | UNIQUE | `(user_id, cycle_start)` | 并发懒创建防双行 | <1ms |
| `credit_cycle` | `idx_cycle_user_end` | 普通 | `(user_id, cycle_end)` | 当前在期 cycle 定位（覆盖所有读路径，含订阅过期作废判断） | <5ms |
| `user_booster_balance` | `PRIMARY` | 主键 | `user_id` | 单用户余额 | <1ms |
| `user_booster_balance` | `idx_booster_updated_at` | 普通 | `updated_at` | 运营时间维度统计 | <100ms（10 万用户级） |
| `membership_event` | `PRIMARY` | 主键 | `id` | 主键查找 | <1ms |
| `membership_event` | `uniq_event_idempotency_key` | UNIQUE | `idempotency_key` | 防重入 | <1ms |
| `membership_event` | `idx_event_user_occurred` | 普通 | `(user_id, occurred_at)` | 用户事件流 | <50ms |
| `membership_event` | `idx_event_granter_occurred` | 普通 | `(granter_user_id, occurred_at)` | **B2B 月度账单（关键路径）** | **100 万事件下 < 500ms（AC-18 验收）** |
| `membership_event` | `idx_event_type_occurred` | 普通 | `(event_type, occurred_at)` | admin 全局统计 | <200ms |

#### 核心查询对应索引（性能契约）

| 查询场景 | SQL 大致形式 | 命中索引 | 期望响应时间 |
|---|---|---|---|
| 用户登录后查会员状态 + 余额 | `subscription` / `trial_grant` / `user_booster_balance` 各按 `user_id` 查；`credit_cycle` 按 `(user_id, cycle_end)` 查当前 cycle | 各表 PK / UNIQUE | 4 次查询合计 <10ms |
| B2B 月度账单（核心场景）| `SELECT ... FROM membership_event WHERE granter_user_id=? AND occurred_at >= ? AND occurred_at < ?` | `idx_event_granter_occurred` | **<500ms @ 100 万事件**（AC-18） |
| 用户事件流明细 | `SELECT ... FROM membership_event WHERE user_id=? ORDER BY occurred_at DESC LIMIT 50` | `idx_event_user_occurred` | <50ms |
| admin 近 30 天 sub_renewed 统计 | `SELECT COUNT(*), SUM(amount_cents) FROM membership_event WHERE event_type='sub_renewed' AND occurred_at >= ?` | `idx_event_type_occurred` | <200ms |
| 父账户客户管理列表 | `SELECT * FROM subscription WHERE granter_user_id=?` + `SELECT * FROM trial_grant WHERE granter_user_id=?` | `idx_sub_granter_expires` / `idx_trial_granter_expires` | <50ms |
| 扣减事务（最高频）| 单用户 `subscription` / `trial_grant` / `credit_cycle` / `user_booster_balance` 各 `SELECT ... FOR UPDATE` | 4 表 PK / UNIQUE | 含锁开销 <20ms（无并发争用） |

#### 索引体积估算

按以下粗略数据量假设（prod 1 年级别）：

- 用户数：10 万
- 平均每用户订阅周期：每月 1 次 sub_renewed 事件 → `membership_event` 1 年量级 ~100 万行
- `subscription` ~5 万行（半数用户曾买过 Pro）
- `trial_grant` ~3 万行
- `credit_cycle` ~50 万行（5 万 Pro 用户 × 10 个月平均 cycle）
- `user_booster_balance` ~5 万行

每张表的索引总开销估算：

| 表 | 主表行 | 索引数 | 索引总大小估算 |
|---|---|---|---|
| `subscription` | 5 万 | 4 | <30 MB |
| `trial_grant` | 3 万 | 4 | <20 MB |
| `credit_cycle` | 50 万 | 3 | <150 MB |
| `user_booster_balance` | 5 万 | 2 | <10 MB |
| `membership_event` | 100 万 | 5 | <500 MB |

总计 InnoDB 占用 prod 期内约 **1 GB 量级**，远低于现有 MySQL 实例容量上限。

---

### §2.7 旧表保留与读路径切换

#### 保留策略总览

切换日（T 时刻）执行迁移脚本后，旧表进入"只读冻结"状态：

| 旧表 | 切换日后状态 | 读路径 | 写路径 | DROP 时间 |
|---|---|---|---|---|
| `credit_package` | **只读冻结** | 仅切换日跨月账单合并使用（AC-21a/b）；其他代码 0 引用 | 新代码 0 写入；切换日后任何 INSERT/UPDATE/DELETE 视为 bug | **T+7 天**后 DROP |
| `credit_account` | **只读冻结** | 0 引用 | 0 写入 | T+7 天后 DROP |
| `billing_account` | **只读冻结** | 0 引用 | 0 写入 | T+7 天后 DROP |
| `legacy_tier_migration_backup_20260424` | **保留不动** | ops 应急 | 0 写入 | 不处理（4 月 24 日历史备份，与本次迁移正交） |
| `credit_reservation` | **持续使用（非旧表）** | Reserve/Reconcile 双阶段 LLM 调用预扣的承载表（I-5），新代码继续读写 | 新代码持续写入（Reserve 创建、Reconcile 多退少补） | **不 DROP**（与本次重构正交，是扣减事务的核心基础设施） |
| `credit_reservation_item` | **持续使用（非旧表）** | Reservation 的明细行（按 trial / cycle / booster 三段分别记录预扣额度），新代码继续读写 | 新代码持续写入 | **不 DROP**（与 `credit_reservation` 同生命周期） |

> **观察期机制**：T 时刻完成迁移后，旧表保留 7 天作为应急回滚 + 客服查证窗口（决策 Q5、§6 部署节奏）。期间 `git revert` 配合 `rollback.sql` 可在 24 小时内安全回滚（详见 §6 回滚方案，本节不展开）。T+7 天后由 ops 走标准 DROP 流程。

#### 切换日代码层面的具体动作

**第 1 类：路径删除（biz 层）**

以下代码点**切换日生效的版本中直接删除**（不再调用、不再 import）：

- `internal/numind/biz/credit/cron_billing.go` —— 整文件删除（`reconcileBillingMode` / `ActivatePending` / `ExpireActive` 三个 cron job 全部移除）
- `cmd/numind/main.go` 中 cron 注册代码 —— 删除对应注册行
- `internal/numind/biz/credit/credit.go` 中所有读 `credit_package` 表的函数 —— 重写为读 §2.1-§2.5 新表

**第 2 类：读路径切换（账单合并）**

`internal/numind/biz/b2b_billing/b2b_billing.go` 在跨切换日的当月账单查询时，按 AC-21a/b/c 双口径拼接：

```
old_rows := SELECT FROM credit_package WHERE granter_user_id=? AND activated_at IN [month_start, T_cutoff)
new_rows := SELECT FROM membership_event WHERE granter_user_id=? AND occurred_at IN [T_cutoff, month_end)
merged   := dedupe(old_rows.map(toEventDTO) ++ new_rows, key=(granter_user_id, child_user_id, time, product_type))
```

字段映射（AC-21a）在常量 `package b2b_billing var legacyToEventTypeMap` 中固化：

```go
var legacyToEventTypeMap = map[string]string{
    "trial":        "trial_granted",
    "subscription": "sub_granted",   // 切换日前老表无 sub_renewed 概念，全部映射 sub_granted
    "booster":      "booster_granted",
}
```

切换日次月起，旧 `credit_package` 不再出现在任何账单查询路径上。

**第 3 类：legacy 字段处理**

旧 `numind_user.user_tier` / `numind_user.tier_expires` 字段在切换日**保留 schema 但不再变化**：

- 切换日前的 cron 已删除（第 1 类），不会再有"过期自动降级 free"的写入
- 切换日代码读路径全部改为 `HasActiveSubscription(ctx, userID)` / `HasActiveTrial(ctx, userID)`（详见 §3 行为契约）
- 字段保留是为应急回滚——一旦回滚，老 cron 重启后能直接读字段恢复运行
- T+7 天 DROP 旧表批次中**不包含**这两个字段；它们的 DROP 留待后续 feature 处理

#### legacy_tier_migration_backup_20260424 处置说明

该表是 4 月 24 日"legacy_tier 24 用户迁移到 credits 制"操作的备份表（参见 `scripts/2026-04-24-legacy-tier-migration/`）。

- 与本次重构**正交**：本次重构是把 `credit_package` 等"双制并存过渡表"换为"5 表标准模型"，与 4 月 24 日的 legacy_tier 迁移属于不同层次的演进
- 该表**保留不动**，本次迁移脚本不读、不写、不 DROP
- 后续如需 DROP 由 ops 走独立流程，与本次重构解耦

#### DROP 时序（运维操作清单）

T+7 天 DROP 操作的 SQL（由 ops 在确认观察期无异常后执行）：

```sql
-- 在 prod backup 验证完整后执行
DROP TABLE IF EXISTS `credit_package`;
DROP TABLE IF EXISTS `credit_account`;
DROP TABLE IF EXISTS `billing_account`;

-- 注：legacy_tier_migration_backup_20260424 不在本次 DROP 列表中
-- 注：numind_user.user_tier / tier_expires 字段不在本次 DROP 列表中
```

DROP 前置条件：

- T+7 天内 0 次回滚触发
- 每日对账 SQL（每用户 5 张新表合计 vs 历史快照）连续 7 天 0 差异
- 应用层日志 grep `credit_package` 命中数 = 0（通过 dev/qa/prod 全量日志确认无残留读路径）

满足三项后由 ops 在 maintenance window 执行 DROP，无需 maintenance mode（旧表已 0 引用）。

---

**§2 数据模型部分结束**——后续 §3 行为契约将基于本节的 5 张表 + 索引，固化每个核心动作（grant trial / grant Pro / 续费 / 扣减 / 懒创建 cycle / B2B 账单查询）的事务边界、锁顺序、SQL 模板与并发策略。

---

## §3 核心算法（Go 伪代码）

> 本章节是 spec 的算法骨架。所有伪代码遵循 §2 数据模型与 §5 锁定决策；不可直接编译，但 SQL 表达式与锁顺序必须 1:1 落地。所有金额、积分一律 `int64`（积分单位 = credits，分单位 = cents）。半开区间 `[start, end)` 通用。事务起点 `txNow := time.Now().UTC()` 在事务最开始一次性获取，整事务复用，不允许中途再调 `time.Now()`。

### 通用约定

- **锁顺序硬规则**：跨多行加锁时，先按 `user_id ASC` 排，同 user_id 再按表名字典序排（`credit_cycle` < `subscription` < `trial_grant` < `user_booster_balance`）。任何事务内若需要锁多个用户的行（仅 grant/booster 路径会出现 parent + child），必须按 `user_id` 升序加锁。
- **幂等键**：客户端通过 HTTP header `Idempotency-Key`（UUID v4）传入；biz 层接到后透传。所有 grant 类操作在 `membership_event` 表上对 `(idempotency_key)` 建 UNIQUE，重复写返回 `nil` 并 SELECT 既有事件返回上次结果。
- **错误约定**：所有 sentinel error 在 `internal/pkg/errno/membership.go` 注册；biz 函数返回 `*biz.MembershipResult, error`，error 包装时用 `fmt.Errorf("biz/membership.X: %w", err)`。
- **LLM 调用 OUT-OF-tx**：DeductCredits 不发起任何 LLM 请求；它只在 Reserve/Reconcile 内部被复用（Reserve 在估算阶段、Reconcile 在结算阶段调用），LLM HTTP 在 Reserve 与 Reconcile 之间，事务已 commit。

---

### §3.1 anchor_add_months — 锚点月加法

**函数签名**

```go
// AnchorAddMonths 在锚点 anchor 上加 n 个月，返回的目标日期 day =
// min(anchor.day, days_in_month(target_year, target_month))。
// 时分秒纳秒与 anchor 一致，时区使用 anchor.Location()。
// n 必须 >= 0；n == 0 返回 anchor 原值。
func AnchorAddMonths(anchor time.Time, n int) time.Time
```

**输入/输出契约**

- **anchor**: 周期锚点（即 `subscription.current_started_at`），UTC 推荐但允许任意时区——返回值 Location 与 anchor 一致。
- **n**: 累计月数（`total_months_purchased`），`>= 0`。
- **返回**: 锚点加 n 月后的精确时间点。
- **错误**: 不返回 error；非法输入（n < 0）panic（属于编码 bug，由调用方保证）。

**核心 Go 实现**（必须可编译落地）

```go
package datemath

import "time"

// AnchorAddMonths 在 anchor 上加 n 个月，遵循 anchor 的 day 锚点规则：
// 目标 day = min(anchor.Day(), daysInMonth(targetYear, targetMonth))。
// 这是为了避免 1/31 + 1 month 漂移到 3/3（time.AddDate 的标准行为）。
func AnchorAddMonths(anchor time.Time, n int) time.Time {
    if n < 0 {
        panic("AnchorAddMonths: n must be >= 0")
    }
    if n == 0 {
        return anchor
    }

    // 拆解 anchor 的日历分量
    y, m, d := anchor.Date()
    hh, mm, ss := anchor.Clock()
    nsec := anchor.Nanosecond()
    loc := anchor.Location()

    // 目标 (year, month) — month 用 int 计算，可能 > 12 或 <= 0（n>=0 不会负）
    totalMonths := int(m) + n
    targetYear := y + (totalMonths-1)/12
    targetMonth := time.Month(((totalMonths-1)%12 + 12) % 12 + 1)

    // 计算目标月份的总天数（time.Date(_, M+1, 0) 自动归一化为 M 月最后一天）
    lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, loc).Day()

    targetDay := d
    if targetDay > lastDay {
        targetDay = lastDay
    }

    return time.Date(targetYear, targetMonth, targetDay, hh, mm, ss, nsec, loc)
}
```

**关键不变量**

- INV-1: `AnchorAddMonths(a, 0) == a`（恒等）
- INV-2: `AnchorAddMonths(a, n).Day() <= a.Day()`（day 只会保持或缩小，永不超出 anchor.Day()）
- INV-3: `AnchorAddMonths(a, n)` 的小时/分钟/秒/纳秒 = a 的对应分量（不漂移时分秒）

**测试用例提示**

1. **31 日序列**：anchor = `2026-01-31 10:00:00 UTC`
   - n=1 → `2026-02-28 10:00:00`（2 月 28 天）
   - n=2 → `2026-03-31 10:00:00`（3 月 31 天，恢复到 31）
   - n=3 → `2026-04-30 10:00:00`（4 月 30 天）
   - n=4 → `2026-05-31 10:00:00`（5 月 31 天，恢复）
   - n=12 → `2027-01-31 10:00:00`
   - n=13 → `2027-02-28 10:00:00`（2027 平年）
2. **闰年 29 日**：anchor = `2024-02-29` → n=12 → `2025-02-28`（非闰年只能取 28）→ n=24 → `2026-02-28` → n=48 → `2028-02-29`（再回到闰年时，目标月 lastDay=29，min(29,29)=29，回到 29 日）。
3. **跨年回环**：anchor = `2026-12-15 23:59:59 UTC` → n=1 → `2027-01-15 23:59:59`；n=13 → `2028-01-15 23:59:59`

---

### §3.2 GrantOrRenewSubscription — 开通/续费 Pro（开通+续费一体）

**函数签名**

```go
func (s *MembershipService) GrantOrRenewSubscription(
    ctx context.Context,
    parentID uint64,        // 操作发起者（granter，必须为父账户 root）
    childID uint64,         // 被开通的子账户
    productType string,     // 必须为 "monthly"（trial 走 §3.3）
    months int,             // [1, 12]
    idempotencyKey string,  // 客户端 UUID v4，必填
) (*GrantResult, error)
```

`GrantResult` 字段：`{ EventID uint64; SubscriptionID uint64; FirstStartedAt time.Time; CurrentStartedAt time.Time; ExpiresAt time.Time; TotalMonthsPurchased int; Scenario string /* "new" | "renew" | "reopen" */ }`

**输入/输出契约**

- **入参验证**（先于事务）：
  - parentID == childID → `ErrSelfPurchaseDisabled`
  - parentID 非 root（`parent_user_id != null`）→ `ErrSelfPurchaseDisabled`
  - childID 的 `parent_user_id != parentID` → `ErrParentChildRelation`
  - productType != "monthly" → `ErrInvalidProductType`
  - months ∉ [1,12] → `ErrInvalidMonths`
  - idempotencyKey 为空 → `ErrBind`
- **返回**：成功返回 `GrantResult`；幂等命中返回上次的 `GrantResult` + nil error（透明重放）。
- **可能错误**：上述参数错误 / `ErrSubscriptionNotFound`（理论不出现，兜底）/ DB 错误。

**核心 Go 伪代码**

```go
func (s *MembershipService) GrantOrRenewSubscription(
    ctx context.Context, parentID, childID uint64,
    productType string, months int, idempotencyKey string,
) (*GrantResult, error) {

    // -- 入参快速校验（略，见上）
    if err := validateGrantInput(parentID, childID, productType, months, idempotencyKey); err != nil {
        return nil, err
    }

    var result *GrantResult
    err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        txNow := time.Now().UTC()

        // [1] 幂等检查：如果 membership_event 已有同 idempotency_key，校验请求体一致后返回上次结果
        var existing model.MembershipEvent
        err := tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error
        if err == nil {
            // Stripe 标准：同 idempotency_key 但请求体不同 → 409
            if existing.UserID != childID ||
                existing.ProductType != productType ||
                existing.Months != months {
                return errno.ErrIdempotencyKeyConflict
            }
            result = decodeGrantResultFromEvent(&existing)
            return nil // 幂等命中（请求体一致），提前返回
        }
        if !errors.Is(err, gorm.ErrRecordNotFound) {
            return fmt.Errorf("idempotency lookup: %w", err)
        }

        // [2] 锁 child 的 subscription 行（SELECT ... FOR UPDATE）
        // 锁顺序：仅锁 child，单一表，无需其他锁
        var sub model.Subscription
        err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("user_id = ?", childID).
            Take(&sub).Error

        scenario := ""
        switch {
        case errors.Is(err, gorm.ErrRecordNotFound):
            // -------- 场景 A: 新开通 --------
            scenario = "new"
            sub = model.Subscription{
                UserID:               childID,
                FirstStartedAt:       txNow,
                CurrentStartedAt:     txNow,
                TotalMonthsPurchased: months,
                ExpiresAt:            AnchorAddMonths(txNow, months),
                Source:               "b2b_grant",
                GranterUserID:        &parentID,
            }
            if err := tx.Create(&sub).Error; err != nil {
                return fmt.Errorf("create sub: %w", err)
            }

        case err != nil:
            return fmt.Errorf("lock sub: %w", err)

        case sub.ExpiresAt.After(txNow):
            // -------- 场景 B: 在期续费 --------
            // anchor 不变 = sub.CurrentStartedAt；first_started_at 不变
            // total_months_purchased 累加，expires_at 从 anchor 重新计算（避免漂移）
            scenario = "renew"
            sub.TotalMonthsPurchased += months
            sub.ExpiresAt = AnchorAddMonths(sub.CurrentStartedAt, sub.TotalMonthsPurchased)
            // granter / source 保持原值（不覆盖第一次开通的归属）
            if err := tx.Model(&sub).Updates(map[string]any{
                "total_months_purchased": sub.TotalMonthsPurchased,
                "expires_at":             sub.ExpiresAt,
            }).Error; err != nil {
                return fmt.Errorf("update sub renew: %w", err)
            }

        default:
            // -------- 场景 C: 已过期再开 --------
            // current_started_at 重置为 txNow（新一轮），first_started_at 不变（保留首开归属）
            // total_months_purchased 重置为 months（重新计数新周期）
            scenario = "reopen"
            sub.CurrentStartedAt = txNow
            sub.TotalMonthsPurchased = months
            sub.ExpiresAt = AnchorAddMonths(txNow, months)
            sub.Source = "b2b_grant"
            sub.GranterUserID = &parentID
            if err := tx.Model(&sub).Updates(map[string]any{
                "current_started_at":     sub.CurrentStartedAt,
                "total_months_purchased": sub.TotalMonthsPurchased,
                "expires_at":             sub.ExpiresAt,
                "source":                 sub.Source,
                "granter_user_id":        sub.GranterUserID,
            }).Error; err != nil {
                return fmt.Errorf("update sub reopen: %w", err)
            }
            // 清理上一轮 sub 已过期的 cycle 行：保持 GetBalance 干净 + 不污染 invariant I9
            if err := tx.Where("user_id = ? AND cycle_end <= ?", childID, txNow).
                Delete(&model.CreditCycle{}).Error; err != nil {
                return fmt.Errorf("cleanup stale cycles on reopen: %w", err)
            }
        }

        // [3] 写 membership_event（UNIQUE on idempotency_key 兜底并发）
        eventType := "sub_granted"
        if scenario == "renew" {
            eventType = "sub_renewed"
        } else if scenario == "reopen" {
            eventType = "sub_reopened"
        }
        evt := model.MembershipEvent{
            IdempotencyKey: idempotencyKey,
            EventType:      eventType,
            UserID:         childID,
            GranterUserID:  &parentID,
            ProductType:    productType,
            Months:         months,
            AmountCents:    months * monthlyPriceCents, // 仅记账，B2B grant 不走支付
            OccurredAt:     txNow,
            PayloadJSON:    encodeGrantPayload(&sub),
        }
        if err := tx.Create(&evt).Error; err != nil {
            // UNIQUE 冲突 → 并发同一 idempotency_key，重新 SELECT 校验请求体后返回既有结果
            if isUniqueViolation(err, "uk_membership_event_idem") {
                _ = tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error
                if existing.UserID != childID ||
                    existing.ProductType != productType ||
                    existing.Months != months {
                    return errno.ErrIdempotencyKeyConflict
                }
                result = decodeGrantResultFromEvent(&existing)
                return nil
            }
            return fmt.Errorf("insert event: %w", err)
        }

        result = &GrantResult{
            EventID:              evt.ID,
            SubscriptionID:       sub.ID,
            FirstStartedAt:       sub.FirstStartedAt,
            CurrentStartedAt:     sub.CurrentStartedAt,
            ExpiresAt:            sub.ExpiresAt,
            TotalMonthsPurchased: sub.TotalMonthsPurchased,
            Scenario:             scenario,
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return result, nil
}
```

**SQL 表达式（三场景）**

| 场景 | first_started_at | current_started_at | total_months_purchased | expires_at |
|------|------------------|--------------------|------------------------|------------|
| A 新开通 | `:txNow` | `:txNow` | `:months` | `AnchorAddMonths(:txNow, :months)` |
| B 在期续费 | 不变 | 不变 | `+= :months` | `AnchorAddMonths(current_started_at, total_months_purchased)` |
| C 过期再开 | 不变 | `:txNow` | `:months`（重置） | `AnchorAddMonths(:txNow, :months)` |

**关键不变量**

- INV-4: 任意时刻 `subscription.expires_at = AnchorAddMonths(current_started_at, total_months_purchased)`（每次写都从 anchor 重算，禁止 `expires_at += AddDate(0, months, 0)`）
- INV-5: `first_started_at <= current_started_at`（首开时间永远 <= 当前周期起点）
- INV-6: 同一 `idempotency_key` 在 `membership_event` 表只允许一行（UNIQUE 强制）

**测试用例提示**

1. **场景 A（新开通）**：parent=10, child=20, months=3, txNow=2026-04-29 10:00 → 期望 `first_started_at == current_started_at == 2026-04-29 10:00`，`expires_at == 2026-07-29 10:00`，`total_months_purchased == 3`，event_type = `sub_granted`
2. **场景 B（在期续费 + day 漂移防护）**：已有 sub `current_started_at=2026-01-31, expires_at=2026-04-30, total_months_purchased=3`（即 1/31 开通 3 月，4/30 到期），续费 1 个月 → `total_months_purchased=4` → `expires_at = AnchorAddMonths(2026-01-31, 4) = 2026-05-31`（不是 2026-05-30，证明 anchor 锁住 day=31）
3. **幂等重放**：同一 idempotencyKey 调用两次，第二次必须返回与第一次完全相同的 `GrantResult`，`subscription.expires_at` 与 `total_months_purchased` 不再变化，`membership_event` 表只有一行

---

### §3.3 GrantTrial — 开通试用（lifetime 单次）

**函数签名**

```go
func (s *MembershipService) GrantTrial(
    ctx context.Context,
    parentID uint64,
    childID uint64,
    idempotencyKey string,
) (*GrantTrialResult, error)
```

`GrantTrialResult` 字段：`{ EventID uint64; TrialGrantID uint64; GrantedAt time.Time; ExpiresAt time.Time; CreditsGranted int64 /* 200 */ }`

**输入/输出契约**

- **入参验证**：parentID 必须为 root；childID.parent_user_id == parentID；idempotencyKey 非空。
- **返回**：成功 → `GrantTrialResult`（trial 200 积分，3 天有效）；幂等命中 → 上次结果。
- **可能错误**：
  - 子账户已有 trial_grant 行（任何状态） → `ErrTrialAlreadyGranted`（EC-3）
  - 子账户当前有 active subscription → `ErrTrialNotAllowedForActivePro`（EC-4 / Q1）
  - parent/child 关系不符 → `ErrParentChildRelation`

**校验顺序硬规则（决策 Q1 锁定）**

> **加锁顺序按 §4.1 字典序：subscription（字典序在前）先 SELECT FOR UPDATE，trial_grant 后 SELECT FOR UPDATE。语义校验顺序保持 trial_grant 检查在前（lifetime 单次优先于 active sub 排除），即先锁后查、查序与锁序解耦。**

1. 先 lock `subscription` FOR UPDATE（字典序在前；用于后续 active 判定）
2. 再 lock `trial_grant` FOR UPDATE（lifetime 单次的物理强制点）
3. 语义校验 #1：`trial_grant` 任何记录存在 → `ErrTrialAlreadyGranted`
4. 语义校验 #2：`subscription` 在期（`expires_at > txNow`）→ `ErrTrialNotAllowedForActivePro`
5. 都通过 → 创建 `trial_grant` + 写 event

**核心 Go 伪代码**

```go
func (s *MembershipService) GrantTrial(
    ctx context.Context, parentID, childID uint64, idempotencyKey string,
) (*GrantTrialResult, error) {
    if err := validateTrialInput(parentID, childID, idempotencyKey); err != nil {
        return nil, err
    }

    const trialCredits = int64(200)
    const trialDuration = 3 * 24 * time.Hour

    var result *GrantTrialResult
    err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        txNow := time.Now().UTC()

        // [1] 幂等命中（校验请求体一致：本函数请求体仅 childID，无 productType/months 变体）
        var existing model.MembershipEvent
        if err := tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error; err == nil {
            // Stripe 标准：同 idempotency_key 但 user_id/event_type 不一致 → 409
            if existing.UserID != childID || existing.EventType != "trial_granted" {
                return errno.ErrIdempotencyKeyConflict
            }
            result = decodeTrialResultFromEvent(&existing)
            return nil
        } else if !errors.Is(err, gorm.ErrRecordNotFound) {
            return fmt.Errorf("idempotency lookup: %w", err)
        }

        // [2] 按 §4.1 字典序加锁：先 subscription（字典序在前）再 trial_grant
        var sub model.Subscription
        subErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("user_id = ?", childID).
            Take(&sub).Error
        if subErr != nil && !errors.Is(subErr, gorm.ErrRecordNotFound) {
            return fmt.Errorf("lock sub: %w", subErr)
        }

        var existingTrial model.TrialGrant
        trialErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("user_id = ?", childID).
            Take(&existingTrial).Error
        if trialErr != nil && !errors.Is(trialErr, gorm.ErrRecordNotFound) {
            return fmt.Errorf("lock trial: %w", trialErr)
        }

        // [3] 语义校验 #1：trial_grant 已存在 → 拒绝（lifetime 单次）
        if trialErr == nil {
            return errno.ErrTrialAlreadyGranted
        }

        // [4] 语义校验 #2：当前有 active subscription → 拒绝
        if subErr == nil && sub.ExpiresAt.After(txNow) {
            return errno.ErrTrialNotAllowedForActivePro
        }

        // [5] 创建 trial_grant
        trial := model.TrialGrant{
            UserID:         childID,
            GrantedAt:      txNow,
            ExpiresAt:      txNow.Add(trialDuration),
            CreditsGranted: trialCredits,
            CreditsRemain:  trialCredits,
            GranterUserID:  &parentID,
        }
        if err := tx.Create(&trial).Error; err != nil {
            // 区分两类 UNIQUE 冲突：trial 已存在 vs 幂等并发兜底
            if isUniqueViolation(err, "uniq_trial_user") {
                return errno.ErrTrialAlreadyGranted
            }
            return fmt.Errorf("create trial: %w", err)
        }

        // [6] 写 membership_event
        evt := model.MembershipEvent{
            IdempotencyKey: idempotencyKey,
            EventType:      "trial_granted",
            UserID:         childID,
            GranterUserID:  &parentID,
            ProductType:    "trial",
            Months:         0,
            AmountCents:    0, // trial 不计费
            OccurredAt:     txNow,
            PayloadJSON:    encodeTrialPayload(&trial),
        }
        if err := tx.Create(&evt).Error; err != nil {
            if isUniqueViolation(err, "uk_membership_event_idem") {
                _ = tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error
                if existing.UserID != childID || existing.EventType != "trial_granted" {
                    return errno.ErrIdempotencyKeyConflict
                }
                result = decodeTrialResultFromEvent(&existing)
                return nil
            }
            return fmt.Errorf("insert event: %w", err)
        }

        result = &GrantTrialResult{
            EventID:        evt.ID,
            TrialGrantID:   trial.ID,
            GrantedAt:      trial.GrantedAt,
            ExpiresAt:      trial.ExpiresAt,
            CreditsGranted: trialCredits,
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return result, nil
}
```

**关键不变量**

- INV-7: `trial_grant.UNIQUE(user_id)`，每个 user 永远只能有 1 行 trial（lifetime）
- INV-8: `trial_grant.expires_at = granted_at + 3*24h`（固定 3 天，与 months 参数解耦）
- INV-9: trial 创建时 `credits_remain == credits_granted == 200`

**测试用例提示**

1. **正常 grant**：parent=10, child=20（无 trial、无 sub）→ 创建 trial 行 + event 行；`credits_remain=200`，`expires_at = grantedAt+72h`
2. **重复 grant 拒绝**：第一次成功，第二次（不同 idempotencyKey）调用 → `ErrTrialAlreadyGranted`
3. **有在期 Pro 拒绝**：child 已有 sub `expires_at=2026-06-01`，txNow=2026-05-01 → 调用 GrantTrial 返回 `ErrTrialNotAllowedForActivePro`（验证校验顺序：必须 trial_grant 检查在前、sub 检查在后；本场景 trial_grant 表为空，sub 检查命中）

---

### §3.4 ensureCurrentCycle — 当前 cycle 懒创建

**函数签名**

```go
// ensureCurrentCycle 在事务中保证调用者 user 有一行覆盖 txNow 的 cycle，
// 返回该 cycle（已 SELECT FOR UPDATE 锁定，调用方可直接 update credits_remain）。
// sub 必须是已 lock 的同一事务内的 subscription 副本。
func (s *MembershipService) ensureCurrentCycle(
    tx *gorm.DB,
    userID uint64,
    sub *model.Subscription,
    txNow time.Time,
) (*model.CreditCycle, error)
```

**输入/输出契约**

- **前置条件**：调用方已在同一事务中 lock `subscription` 行；`sub.expires_at > txNow`（否则不应进入 cycle 路径）。
- **返回**：覆盖 `txNow` 的 `credit_cycle` 行（含 SELECT FOR UPDATE 锁），可直接 update credits_remain。
- **错误**：DB 错误 / sub 已过期（防御性 check） → `ErrSubscriptionExpired`（cross-ref §5.7 错误码清单需补充该 sentinel）。

**核心算法（决策：ON CONFLICT DO NOTHING + 重新 SELECT FOR UPDATE）**

```go
func (s *MembershipService) ensureCurrentCycle(
    tx *gorm.DB, userID uint64, sub *model.Subscription, txNow time.Time,
) (*model.CreditCycle, error) {

    // [1] 计算 cycle 索引：从 current_started_at 起，每 anchor_add_months 一段
    // 找到最小 i 使得 anchor_add_months(current_started_at, i+1) > txNow
    // 注：cycleIndex 仅用于 Go 端推算 cycleStart/cycleEnd；不存数据库、不出现在 SQL 里
    cycleIndex := computeCycleIndex(sub.CurrentStartedAt, txNow)

    cycleStart := AnchorAddMonths(sub.CurrentStartedAt, cycleIndex)
    cycleEndRaw := AnchorAddMonths(sub.CurrentStartedAt, cycleIndex+1)
    cycleEnd := cycleEndRaw
    if sub.ExpiresAt.Before(cycleEnd) {
        cycleEnd = sub.ExpiresAt // EC-7: cycle_end = min(anchor+i+1, sub.expires_at)
    }

    // 防御：txNow 必须在 [cycleStart, cycleEnd) 内（sub 已过期 / 时间错乱兜底）
    if !txNow.Before(cycleEnd) || txNow.Before(cycleStart) {
        return nil, errno.ErrSubscriptionExpired
    }

    // [2] 尝试 SELECT FOR UPDATE 现有 cycle（按 (user_id, cycle_start) UNIQUE）
    var cycle model.CreditCycle
    err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
        Where("user_id = ? AND cycle_start = ?", userID, cycleStart).
        Take(&cycle).Error
    if err == nil {
        return &cycle, nil
    }
    if !errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("select cycle: %w", err)
    }

    // [3] 不存在 → INSERT ... ON CONFLICT DO NOTHING（GORM 在 MySQL 下翻译为 INSERT IGNORE，仅对 UNIQUE 冲突生效）
    newCycle := model.CreditCycle{
        UserID:           userID,
        SubscriptionID:   sub.ID,
        CycleStart:       cycleStart,
        CycleEnd:         cycleEnd,
        CreditsGranted:   monthlyCreditsQuota, // 2000
        CreditsRemain:    monthlyCreditsQuota,
    }
    err = tx.Clauses(clause.OnConflict{
        Columns:   []clause.Column{{Name: "user_id"}, {Name: "cycle_start"}},
        DoNothing: true,
    }).Create(&newCycle).Error
    if err != nil {
        return nil, fmt.Errorf("insert cycle: %w", err)
    }

    // [4] 不论 INSERT 命中还是被并发抢先，重新 SELECT FOR UPDATE 拿权威行
    err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
        Where("user_id = ? AND cycle_start = ?", userID, cycleStart).
        Take(&cycle).Error
    if err != nil {
        return nil, fmt.Errorf("re-select cycle after upsert: %w", err)
    }
    return &cycle, nil
}

// computeCycleIndex 找到最小非负整数 i 使得
// AnchorAddMonths(start, i+1) > now （即 now ∈ [start+i, start+i+1)）
// 等价于 i = monthsBetween(start, now)，其中 monthsBetween 用 anchor 语义。
func computeCycleIndex(start, now time.Time) int {
    if !now.After(start) {
        return 0
    }
    // 二分 / 直接迭代：迭代版本最稳，cycle 数 <= 12
    for i := 0; i < 240; i++ {
        next := AnchorAddMonths(start, i+1)
        if next.After(now) {
            return i
        }
    }
    return 0 // unreachable for valid sub
}
```

**关键不变量**

- INV-10: 同一 `(user_id, cycle_start)` 永远只有 1 行（DB UNIQUE `uniq_cycle_user_start` 强制 + ON CONFLICT DO NOTHING）
- INV-11: `cycle_end <= subscription.expires_at`（cycle 不能超出 sub 期）
- INV-12: 函数返回时 cycle 行已 `SELECT FOR UPDATE` 锁定，调用方可安全 update

**测试用例提示**

1. **首次扣分懒创建**：sub 1/15 开通 3 月，txNow=1/15 14:00，无 cycle → 返回新建 cycle_index=0，cycle_start=1/15 00:00（实际为 sub.current_started_at），cycle_end=2/15，credits_remain=2000
2. **第二月扣分**：sub 1/31 开通 3 月（expires_at=4/30），txNow=2/20 → cycle_index=1，cycle_start=AnchorAddMonths(1/31,1)=2/28，cycle_end=AnchorAddMonths(1/31,2)=3/31
3. **并发懒创建**：两个请求同时进入 ensureCurrentCycle，目标 cycleStart 行不存在，A 先 INSERT 成功 / B 触发 ON CONFLICT DO NOTHING 然后重新 SELECT FOR UPDATE 等待 A commit；最终 cycle 表恰好 1 行（UNIQUE on `user_id, cycle_start` 强制），A 和 B 都返回同一行

---

### §3.5 DeductCredits — 三类积分扣减

**函数签名**

```go
// DeductCredits 按硬规则扣减优先级 trial → cycle → booster。
// 返回 detail 描述每类扣减了多少；amount 全额覆盖时返回 nil error。
// 不足扣减返回 ErrInsufficientCredits（事务回滚）。
//
// 注意：本函数 OUT-OF-tx 给 LLM 调用使用——Reserve 阶段调用扣预扣额，
// Reconcile 阶段调用补差。LLM HTTP 调用在两次 DeductCredits 之间。
func (s *MembershipService) DeductCredits(
    ctx context.Context,
    userID uint64,
    amount int64, // 必须 > 0
) (*DeductDetail, error)
```

`DeductDetail` 字段：`{ FromTrial, FromCycle, FromBooster int64; CycleIndex int; FrozenBoosterReason string /* 若 sub 过期则填 "membership_expired" */ }`

**输入/输出契约**

- **入参**：amount > 0；userID 必须存在。
- **返回**：`DeductDetail`（三段加总 = amount）。
- **错误**：amount <= 0 → `ErrBind`；总余额不足 → `ErrInsufficientCredits`。

**锁顺序（硬规则，与 §4.1 字典序对齐）**

1. 单一 user 的扣分场景：仅锁 `userID` 相关行
2. 表内字典序：`credit_cycle` < `membership_event` < `subscription` < `trial_grant` < `user_booster_balance`
3. **实际执行顺序（严格按字典序）**：
   - (1) lock `credit_cycle` 通过 `ensureCurrentCycle`（含 SELECT FOR UPDATE，可能 INSERT + 重 SELECT FOR UPDATE）
   - (2) lock `subscription` FOR UPDATE
   - (3) lock `trial_grant` FOR UPDATE
   - (4) lock `user_booster_balance` FOR UPDATE
   - (5) Go 端按扣减优先级 trial → cycle → booster 计算各表扣减量
   - (6) UPDATE 各表 `credits_remain`
   - (7) `membership_event` 仅在 grant/renew 路径写入；扣减路径不写 event（usage_record 由上层 Reserve/Reconcile 写入）
4. **特别说明**：`ensureCurrentCycle` 需要读 `sub.CurrentStartedAt` 计算 cycleStart——但 sub 是字典序在 cycle 之后的表。解决：(1) 第一次 lock cycle 之前用普通 SELECT 读一份 sub 快照（不加锁，仅用于推算 cycleStart）；(2) 完成 ensureCurrentCycle 后再 SELECT FOR UPDATE 锁 sub。两次读取之间 sub 不变（同事务可重复读），cycleStart 推算结果一致。

**Booster 冻结逻辑（AC-7）**

- 若 `subscription` 不存在 OR `subscription.expires_at <= txNow` AND `trial_grant.expires_at <= txNow` → booster 不可扣，跳过该步骤
- 即 active = (sub 在期 OR trial 在期)。active 才解冻。

**核心 Go 伪代码**

```go
func (s *MembershipService) DeductCredits(
    ctx context.Context, userID uint64, amount int64,
) (*DeductDetail, error) {
    if amount <= 0 {
        return nil, errno.ErrBind.SetMessage("amount must be positive")
    }

    var detail *DeductDetail
    err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        txNow := time.Now().UTC()
        remaining := amount
        d := &DeductDetail{}

        // ========================================================
        // 加锁顺序严格按 §4.1 字典序：cycle < subscription < trial < booster
        // ========================================================

        // ---- 预读 sub 快照（不加锁，仅用于 ensureCurrentCycle 推算 cycleStart）----
        // 字典序硬规则不允许在 cycle 之前 lock sub；但 ensureCurrentCycle 需要 sub.CurrentStartedAt。
        // 解决：先做不加锁 SELECT 读一份快照，在同事务可重复读保证下其值稳定；后续步骤再正式 lock sub。
        var subSnapshot *model.Subscription
        {
            var s0 model.Subscription
            if err := tx.Where("user_id = ?", userID).Take(&s0).Error; err == nil {
                subSnapshot = &s0
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return fmt.Errorf("read sub snapshot: %w", err)
            }
        }
        snapshotSubActive := subSnapshot != nil && subSnapshot.ExpiresAt.After(txNow)

        // ======== STEP 1: lock credit_cycle（字典序最前；仅当 sub 在期）========
        var cycle *model.CreditCycle
        if snapshotSubActive {
            c, err := s.ensureCurrentCycle(tx, userID, subSnapshot, txNow)
            if err != nil {
                return err
            }
            cycle = c
        }

        // ======== STEP 2: lock subscription FOR UPDATE ========
        var sub *model.Subscription
        {
            var subRow model.Subscription
            err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
                Where("user_id = ?", userID).
                Take(&subRow).Error
            if err == nil {
                sub = &subRow
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return fmt.Errorf("lock sub: %w", err)
            }
        }
        subActive := sub != nil && sub.ExpiresAt.After(txNow)

        // ======== STEP 3: lock trial_grant FOR UPDATE ========
        var trial *model.TrialGrant
        {
            var trialRow model.TrialGrant
            err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
                Where("user_id = ?", userID).
                Take(&trialRow).Error
            if err == nil {
                trial = &trialRow
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return fmt.Errorf("lock trial: %w", err)
            }
        }
        trialActive := trial != nil && trial.ExpiresAt.After(txNow) && trial.CreditsRemain > 0

        // ======== STEP 4: lock user_booster_balance FOR UPDATE（始终 lock；冻结判定在扣减步骤）========
        var booster *model.UserBoosterBalance
        {
            var b0 model.UserBoosterBalance
            err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
                Where("user_id = ?", userID).
                Take(&b0).Error
            if err == nil {
                booster = &b0
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return fmt.Errorf("lock booster: %w", err)
            }
        }

        // ========================================================
        // STEP 5: Go 端按扣减优先级 trial → cycle → booster 计算各表扣减量
        // ========================================================

        // 优先级 1 — 扣 trial
        if trialActive && remaining > 0 {
            take := min64(remaining, trial.CreditsRemain)
            d.FromTrial = take
            remaining -= take
            trial.CreditsRemain -= take
        }

        // 优先级 2 — 扣 cycle（仅当 sub 在期）
        if remaining > 0 && subActive && cycle != nil && cycle.CreditsRemain > 0 {
            take := min64(remaining, cycle.CreditsRemain)
            d.FromCycle = take
            d.CycleIndex = computeCycleIndex(sub.CurrentStartedAt, txNow) // 仅审计用，不存 DB
            remaining -= take
            cycle.CreditsRemain -= take
        }

        // 优先级 3 — 扣 booster（受会员状态门禁，AC-7：会员过期时 booster 不可扣）
        active := subActive || trialActive
        if remaining > 0 {
            if !active {
                // booster 冻结：会员过期时不论 trial/cycle 是否有余额，booster 一律不可扣
                d.FrozenBoosterReason = "membership_expired"
                return errno.ErrInsufficientCredits
            }
            if booster == nil || booster.CreditsRemain <= 0 || booster.CreditsRemain < remaining {
                return errno.ErrInsufficientCredits
            }
            d.FromBooster = remaining
            booster.CreditsRemain -= remaining
            remaining = 0
        }

        if remaining > 0 {
            return errno.ErrInsufficientCredits
        }

        // ========================================================
        // STEP 6: UPDATE 各表 credits_remain（仅写实际扣减过的行）
        // ========================================================
        if d.FromTrial > 0 {
            if err := tx.Model(trial).
                UpdateColumn("credits_remain", trial.CreditsRemain).Error; err != nil {
                return fmt.Errorf("update trial: %w", err)
            }
        }
        if d.FromCycle > 0 {
            if err := tx.Model(cycle).
                UpdateColumn("credits_remain", cycle.CreditsRemain).Error; err != nil {
                return fmt.Errorf("update cycle: %w", err)
            }
        }
        if d.FromBooster > 0 {
            if err := tx.Model(booster).
                UpdateColumn("credits_remain", booster.CreditsRemain).Error; err != nil {
                return fmt.Errorf("update booster: %w", err)
            }
        }

        // STEP 7: membership_event — 扣减路径不写 event（usage_record 由上层 Reserve/Reconcile 写入；UNIQUE 兜底由 grant/renew 路径承担）
        detail = d
        return nil
    })
    if err != nil {
        return nil, err
    }
    return detail, nil
}
```

**关键不变量**

- INV-13: 扣减后 `FromTrial + FromCycle + FromBooster == amount`（成功路径）
- INV-14: 任何 `credits_remain` 字段更新后 >= 0（数据库 CHECK 兜底）
- INV-15: 当 `subActive == false && trialActive == false`，booster 行的 `credits_remain` 在本事务中绝不被修改（冻结）

**测试用例提示**

1. **AC-6 序列**（trial 200 + cycle 2000 + booster 1200）：
   - 扣 250 → `FromTrial=200, FromCycle=50, FromBooster=0` → 余额 (0, 1950, 1200)
   - 再扣 1950 → `FromTrial=0, FromCycle=1950, FromBooster=0` → 余额 (0, 0, 1200)
   - 再扣 500 → `FromTrial=0, FromCycle=0, FromBooster=500` → 余额 (0, 0, 700)
2. **AC-7 booster 冻结**：sub 已过期 + booster 1200，扣 100 → 返回 `ErrInsufficientCredits`，booster 行的 credits_remain 仍为 1200（事务回滚）
3. **AC-8 booster 解冻**：sub 过期后 booster=1200，调用 GrantOrRenewSubscription 重新开通 → 再扣 100 → `FromBooster=100`，booster 余额 1100

---

### §3.6 GetMembershipState — 会员显示状态

**函数签名**

```go
// GetMembershipState 计算用户在 now 时刻的展示状态（C 端用户视角）。
// 该函数不写库，纯只读查询；可以在 transaction 外调用。
func (s *MembershipService) GetMembershipState(
    ctx context.Context,
    userID uint64,
    now time.Time,
) (*MembershipState, error)
```

`MembershipState` 字段：

```go
type MembershipState struct {
    DisplayState     string     // "free" | "trial" | "pro"
    TrialActive      bool       // trial 在期
    SubActive        bool       // subscription 在期
    TrialExpiresAt   *time.Time // 仅 TrialActive 为 true 时填
    SubExpiresAt     *time.Time // 仅 SubActive 为 true 时填
    SubFirstStartedAt *time.Time // 父账户视角：用户首次开通 Pro 的时间
    BoosterFrozen    bool       // !TrialActive && !SubActive 时为 true
}
```

**显示状态规则（决策锁定）**

```
if TrialActive:
    DisplayState = "trial"
elif SubActive:
    DisplayState = "pro"
else:
    DisplayState = "free"
```

> **注**：US-2 场景下，trial 和 pro 同时在期时，C 端用户显示"trial"（保留试用感知）；父账户客户管理列表显示"试用中 + Pro 已开通"双标（控制权在前端组件，这里只返回原子字段）。

**核心 Go 伪代码**

```go
func (s *MembershipService) GetMembershipState(
    ctx context.Context, userID uint64, now time.Time,
) (*MembershipState, error) {
    state := &MembershipState{DisplayState: "free"}

    var trial model.TrialGrant
    err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&trial).Error
    if err == nil {
        state.TrialActive = trial.ExpiresAt.After(now) && trial.CreditsRemain > 0
        if state.TrialActive {
            t := trial.ExpiresAt
            state.TrialExpiresAt = &t
        }
    } else if !errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("query trial: %w", err)
    }

    var sub model.Subscription
    err = s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&sub).Error
    if err == nil {
        state.SubActive = sub.ExpiresAt.After(now)
        first := sub.FirstStartedAt
        state.SubFirstStartedAt = &first
        if state.SubActive {
            e := sub.ExpiresAt
            state.SubExpiresAt = &e
        }
    } else if !errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("query sub: %w", err)
    }

    switch {
    case state.TrialActive:
        state.DisplayState = "trial"
    case state.SubActive:
        state.DisplayState = "pro"
    default:
        state.DisplayState = "free"
    }

    state.BoosterFrozen = !state.TrialActive && !state.SubActive
    return state, nil
}
```

**关键不变量**

- INV-16: `DisplayState ∈ {"free", "trial", "pro"}`
- INV-17: `BoosterFrozen == true` 当且仅当 `TrialActive == false && SubActive == false`
- INV-18: 函数纯只读，不写任何表；并发调用安全

**测试用例提示**

1. **trial 优先**：trial 在期 + sub 也在期 → `DisplayState="trial"`，TrialActive=true，SubActive=true，BoosterFrozen=false
2. **过期 → free**：trial 已过期 + 无 sub → `DisplayState="free"`，BoosterFrozen=true
3. **pro 单独**：无 trial + sub 在期 → `DisplayState="pro"`，TrialActive=false，SubActive=true，BoosterFrozen=false

---

### §3.7 GetBalance — 完整余额视图

**函数签名**

```go
// GetBalance 返回用户当前完整余额视图，对齐 AC-14 接口契约。
// 该函数会触发 cycle 懒加载查询（但不写入），即"如果当前应有 cycle 则在事务中读取"。
// 注意：cycle 行可能不存在（用户从未扣过分），此时 cycle_remaining = 月度配额（2000）。
func (s *MembershipService) GetBalance(
    ctx context.Context,
    userID uint64,
) (*BalanceView, error)
```

`BalanceView` 字段：

```go
type BalanceView struct {
    TrialRemaining   int64      // trial 剩余
    CycleRemaining   int64      // 当前月 cycle 剩余（无行时返回月度配额）
    CycleEnd         *time.Time // 当前 cycle 结束时间（仅 sub 在期时填）
    BoosterTotal     int64      // booster 余额（不区分冻结）
    BoosterUsable    int64      // 实际可用 booster（冻结时为 0）
    MembershipState  string     // 同 §3.6 DisplayState
    SubExpiresAt     *time.Time
    TrialExpiresAt   *time.Time
}
```

**输入/输出契约**

- **入参**：userID 必须存在
- **返回**：`BalanceView`（所有字段总是非 nil 但 int64 默认 0）
- **错误**：DB 错误

**核心 Go 伪代码**

```go
func (s *MembershipService) GetBalance(
    ctx context.Context, userID uint64,
) (*BalanceView, error) {
    now := time.Now().UTC()
    view := &BalanceView{}

    // [1] 复用 GetMembershipState 拿状态
    state, err := s.GetMembershipState(ctx, userID, now)
    if err != nil {
        return nil, err
    }
    view.MembershipState = state.DisplayState
    view.TrialExpiresAt = state.TrialExpiresAt
    view.SubExpiresAt = state.SubExpiresAt

    // [2] trial_remaining
    var trial model.TrialGrant
    err = s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&trial).Error
    if err == nil && trial.ExpiresAt.After(now) {
        view.TrialRemaining = trial.CreditsRemain
    }

    // [3] cycle_remaining + cycle_end（仅 sub 在期时计算；不创建行，仅查询）
    // 复用 state.SubExpiresAt 作为 sub.ExpiresAt 来源，避免重复查询 sub 表
    if state.SubActive && state.SubExpiresAt != nil {
        var sub model.Subscription
        if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&sub).Error; err == nil {
            cycleIndex := computeCycleIndex(sub.CurrentStartedAt, now)
            cycleStart := AnchorAddMonths(sub.CurrentStartedAt, cycleIndex)
            cycleEndRaw := AnchorAddMonths(sub.CurrentStartedAt, cycleIndex+1)
            cycleEnd := cycleEndRaw
            if state.SubExpiresAt.Before(cycleEnd) {
                cycleEnd = *state.SubExpiresAt
            }
            // 边界兜底：sub 已过期但 cycleEnd 仍未过期的极端时序错乱场景，跳过 cycle 视图
            if cycleEnd.Before(now) {
                return view, nil
            }
            view.CycleEnd = &cycleEnd

            var cycle model.CreditCycle
            err := s.db.WithContext(ctx).
                Where("user_id = ? AND cycle_start = ?", userID, cycleStart).
                Take(&cycle).Error
            if err == nil {
                view.CycleRemaining = cycle.CreditsRemain
            } else if errors.Is(err, gorm.ErrRecordNotFound) {
                // 行未懒创建 → 默认月度配额
                view.CycleRemaining = monthlyCreditsQuota
            } else {
                return nil, fmt.Errorf("query cycle: %w", err)
            }
        }
    }

    // [4] booster_total / booster_usable
    var booster model.UserBoosterBalance
    err = s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&booster).Error
    if err == nil {
        view.BoosterTotal = booster.CreditsRemain
        if !state.BoosterFrozen {
            view.BoosterUsable = booster.CreditsRemain
        }
        // BoosterFrozen 时 BoosterUsable 保持 0
    } else if !errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("query booster: %w", err)
    }

    return view, nil
}
```

**关键不变量**

- INV-19: `BoosterUsable <= BoosterTotal`；冻结时 `BoosterUsable == 0` 且 `BoosterTotal` 仍真实显示
- INV-20: `CycleRemaining` 在 cycle 行未创建时返回月度配额（用户体感"满格"）
- INV-21: `MembershipState ∈ {"free","trial","pro"}` 与 §3.6 一致

**测试用例提示**

1. **会员 + 无 cycle 行**：sub 1/15 开通 3 月，从未扣分 → `CycleRemaining=2000`，`CycleEnd=2/15`，`MembershipState="pro"`
2. **冻结 booster**：sub 已过期 + booster 600 → `BoosterTotal=600, BoosterUsable=0, MembershipState="free"`
3. **trial 用户**：trial 200 已用 50 + 无 sub → `TrialRemaining=150, MembershipState="trial", BoosterUsable=0`（无 booster 行）

---

### §3.8 与现有 Reserve / Reconcile 的接入点

**复用契约（决策锁定）**：本次重构**不改动** `creditService.Reserve` 与 `creditService.Reconcile` 的对外签名（见 `internal/numind/biz/credit/credit_service.go:93,103`）：

```go
Reserve(ctx, user, op, estimatedCents, coefID, *idempotencyKey) (*Reservation, error)
Reconcile(ctx, reservationID, actualCostCents) error
```

仅替换其内部 `creditsImpl` 的扣减实现：

- `creditsImpl.Reserve` 内部调用 `MembershipService.DeductCredits(ctx, userID, estimatedCredits)`，并写 reservation 行。LLM HTTP 调用发生在 Reserve 返回后、Reconcile 调用前，**不在事务内**。
- `creditsImpl.Reconcile` 根据 `actualCostCents` vs `reservation.estimatedCents` 的差额：
  - actual < estimated → 调用退款路径（按 trial → cycle → booster 反向回填，复用同事务模型）
  - actual > estimated → 调用 `MembershipService.DeductCredits(ctx, userID, delta)` 补差
  - actual == estimated → noop

**事务边界**：DeductCredits 自带事务；Reserve/Reconcile 在外层不再开事务（避免嵌套）。所有锁顺序由 DeductCredits 内部保证。

> 完整 Reserve/Reconcile 改造细节在 §4 Reservation 接入章节展开（本节仅声明接入点）。

---

## §4 并发与事务

本章节规范本次重构在并发与事务层面的所有硬约定。所有 biz 函数实现必须遵守此章节列出的锁顺序、事务边界、幂等键协议和事务起点 timestamp 模式；任何对此规则的偏离都需在 spec 修订记录中显式声明。

涉及的 5 张新表：

- `credit_cycle`（月度积分周期）
- `membership_event`（会员事件流水，事件维度计费/审计）
- `subscription`（订阅主表）
- `trial_grant`（试用包，UNIQUE on user_id 强制 lifetime 单次）
- `user_booster_balance`（加量包余额）

锁顺序约定中将使用上述表名的字典序。

---

### §4.1 全局锁顺序约定

#### 4.1.1 总规则

**所有事务在获取行锁时，必须按以下顺序：**

1. **第一维度**：按 `user_id` 升序排序（ASC）。事务涉及多个 user 时，先锁 user_id 较小者的所有行，再锁 user_id 较大者的所有行。
2. **第二维度**：在同一个 user_id 内，按下表的字典序（lexicographic）锁表：

```
credit_cycle  <  membership_event  <  subscription  <  trial_grant  <  user_booster_balance
```

3. **第三维度**：同一个 user_id + 同一张表内出现多行（如多个 cycle 或多个 booster 行）时，按该表的主键 `id` 升序锁。

> 强约束：**所有 biz 函数必须使用统一的 `lockMembershipRows(tx, userID, tables...)` helper**，由 helper 内部按字典序排序并依次发起 `SELECT ... FOR UPDATE`。禁止 biz 代码裸写 SELECT FOR UPDATE 跳过 helper。

#### 4.1.2 典型场景 A：扣减（deduct credits）

扣减场景涉及单个 user_id 的 3 张表：`trial_grant` / `credit_cycle` / `user_booster_balance`。按字典序：`credit_cycle < trial_grant < user_booster_balance`。

```go
// 伪代码：deduct credits 的加锁顺序
func deductCredits(ctx context.Context, userID uint, amount int) error {
    txNow := time.Now()  // 见 §4.6 事务起点固定
    return db.Transaction(func(tx *gorm.DB) error {
        // 步骤 1: 懒创建当前 cycle（见 §4.4 证明）
        cycle, err := ensureCurrentCycle(tx, userID, txNow)
        if err != nil { return err }

        // 步骤 2: 按字典序加锁
        // 2a) 先锁 credit_cycle（字典序最小）
        if err := tx.Set("gorm:query_option", "FOR UPDATE").
            First(&cycle, cycle.ID).Error; err != nil {
            return err
        }
        // 2b) 然后锁 trial_grant
        var trial model.TrialGrant
        if err := tx.Set("gorm:query_option", "FOR UPDATE").
            Where("user_id = ?", userID).First(&trial).Error; err != nil &&
            !errors.Is(err, gorm.ErrRecordNotFound) {
            return err
        }
        // 2c) 最后锁 user_booster_balance
        var booster model.UserBoosterBalance
        if err := tx.Set("gorm:query_option", "FOR UPDATE").
            Where("user_id = ?", userID).First(&booster).Error; err != nil &&
            !errors.Is(err, gorm.ErrRecordNotFound) {
            return err
        }

        // 步骤 3: 执行扣减算法（trial → cycle → booster 优先级，见 AC-6）
        // 注意：扣减优先级与锁顺序无关，锁顺序只为防死锁
        return applyDeduction(tx, &trial, &cycle, &booster, amount, txNow)
    })
}
```

#### 4.1.3 典型场景 B：grant 会员（父账户给子账户开通）

涉及两个 user_id（父 + 子）+ 多张表（subscription/trial_grant/membership_event）。按 user_id ASC：

```go
// 伪代码：grant 会员加锁顺序
func grantMembership(ctx context.Context, parentUserID, childUserID uint,
                     productType string, months int, idemKey string) error {
    txNow := time.Now()
    // 注意：parent 和 child 的 user_id 大小不固定，必须按数值排序
    firstUID, secondUID := orderUIDs(parentUserID, childUserID)

    return db.Transaction(func(tx *gorm.DB) error {
        // 步骤 1: 锁 user_id 较小者的所有相关行（按字典序：subscription < trial_grant）
        if err := lockUserRows(tx, firstUID,
            "subscription", "trial_grant"); err != nil {
            return err
        }
        // 步骤 2: 锁 user_id 较大者的所有相关行
        if err := lockUserRows(tx, secondUID,
            "subscription", "trial_grant"); err != nil {
            return err
        }

        // 步骤 3: 业务逻辑（grant trial 或 grant subscription）
        // ... 校验 + 写入 membership_event（带 idempotency_key UNIQUE）
        return writeMembershipEvent(tx, ..., idemKey, txNow)
    })
}
```

#### 4.1.4 锁顺序的形式化无死锁证明

**定理**：若所有事务按 §4.1.1 的总规则加锁，则系统中任意两个并发事务之间不会发生循环等待（cyclic wait），从而不会发生死锁。

**证明（反证法）**：

假设存在循环等待，记参与循环的事务为 T1, T2, ..., Tn, T1。即 T1 等 T2 持有的锁 L1，T2 等 T3 持有的锁 L2，……，Tn 等 T1 持有的锁 Ln。

- 设 Ti 持有的最大锁的"锁键"为 ki（锁键定义为三元组 `(user_id, table_name, row_id)` 的字典序值）。
- Ti 等待的锁的锁键必然 > ki（因为 Ti 已经按升序加完了 ≤ ki 的所有锁，正在请求下一个更大的锁）。
- 因此沿循环走一圈：k1 < k2 < ... < kn < k1，矛盾。

故假设不成立，系统中不存在循环等待，无死锁。∎

**关键前提**（必须在 code review 阶段确认）：

1. 所有事务路径都通过 `lockMembershipRows` helper 加锁；
2. helper 内部按 §4.1.1 的字典序逐项发 SELECT FOR UPDATE；
3. 没有任何 biz 代码绕过 helper 直接拿 GORM 锁；
4. ensureCurrentCycle 的 ON CONFLICT DO NOTHING 模式（§4.4）不破坏锁顺序——它只在 cycle 行不存在时插入，不持有跨行长锁。

---

### §4.2 事务边界 + LLM OUT-OF-tx + Reserve/Reconcile 集成

#### 4.2.1 总原则：LLM 调用绝不在事务内

任何 LLM 调用（aiservice.Chat / Embed / Rerank 等）都**禁止**包在 `db.Transaction(...)` 内。原因：

1. LLM 调用 P95 ≥ 5 秒，长事务会持有行锁导致并发崩溃；
2. LLM 失败率高（超时、限流、内容审核），事务回滚成本高于业务损失；
3. Reserve/Reconcile 双阶段就是为了把 LLM 调用从事务里挪出来设计的。

#### 4.2.2 Reserve 阶段（事务内，扣减预留）

```go
// 伪代码：Reserve 阶段——预留积分
func ReserveCredits(ctx context.Context, userID uint, estCredits int) (
    reservationID uint64, err error) {

    txNow := time.Now()
    err = db.Transaction(func(tx *gorm.DB) error {
        // 1) 加锁顺序严格按 §4.1 字典序：cycle < subscription < trial_grant < user_booster_balance
        //    （与 §3.5 DeductCredits 的锁顺序保持一致）

        // 1a) 预读 sub 快照（不加锁，仅用于 ensureCurrentCycle 推算 cycleStart；§3.5 同模式）
        var subSnapshot *model.Subscription
        {
            var s0 model.Subscription
            if err := tx.Where("user_id = ?", userID).Take(&s0).Error; err == nil {
                subSnapshot = &s0
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return err
            }
        }
        snapshotSubActive := subSnapshot != nil && subSnapshot.ExpiresAt.After(txNow)

        // 1b) lock credit_cycle（字典序最前；仅当 sub 在期）
        var cycle *model.CreditCycle
        if snapshotSubActive {
            c, err := ensureCurrentCycle(tx, userID, subSnapshot, txNow)
            if err != nil { return err }
            cycle = c
        }

        // 1c) lock subscription FOR UPDATE
        var sub *model.Subscription
        {
            var s0 model.Subscription
            err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
                Where("user_id = ?", userID).First(&s0).Error
            if err == nil {
                sub = &s0
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return err
            }
        }
        subActive := sub != nil && sub.ExpiresAt.After(txNow)

        // 1d) lock trial_grant FOR UPDATE
        var trial *model.TrialGrant
        {
            var t0 model.TrialGrant
            err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
                Where("user_id = ?", userID).First(&t0).Error
            if err == nil {
                trial = &t0
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return err
            }
        }
        trialActive := trial != nil && trial.ExpiresAt.After(txNow) && trial.CreditsRemaining > 0

        // 1e) lock user_booster_balance FOR UPDATE
        var booster *model.UserBoosterBalance
        {
            var b0 model.UserBoosterBalance
            err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
                Where("user_id = ?", userID).First(&b0).Error
            if err == nil {
                booster = &b0
            } else if !errors.Is(err, gorm.ErrRecordNotFound) {
                return err
            }
        }

        // 2) 会员状态判定：booster 冻结条件（AC-7）独立成步骤
        //    会员过期时 booster 不可扣，**不论 trial/cycle 余额是否为 0**
        active := subActive || trialActive
        canUseBooster := active  // 仅当 active=true 时 booster 解冻

        // 3) 按优先级 trial → cycle → booster 预扣（splitDeduction 内部接受 canUseBooster 门禁）
        deducted := splitDeduction(estCredits, trial, cycle, booster, canUseBooster)
        if deducted.Total < estCredits {
            return errno.ErrInsufficientCredits
        }

        // 4) 持久化更新（三表的 credits_remaining 都减去对应份额）
        if err := persistDeduction(tx, trial, cycle, booster, deducted); err != nil {
            return err
        }

        // 5) 写 credit_reservation 行（status=reserved, est_credits, splits 字段记录三表预扣份额）
        var cycleID uint64
        if cycle != nil { cycleID = cycle.ID }
        res := model.CreditReservation{
            UserID:        userID,
            EstCredits:    estCredits,
            TrialDeducted: deducted.Trial,
            CycleDeducted: deducted.Cycle,
            CycleID:       cycleID,
            BoosterDeducted: deducted.Booster,
            Status:        "reserved",
            CreatedAt:     txNow,
        }
        if err := tx.Create(&res).Error; err != nil { return err }
        reservationID = res.ID
        return nil
    })
    return
}
```

#### 4.2.3 LLM 调用（NO TRANSACTION）

```go
// 伪代码：实际 LLM 调用——绝不开事务
func executeAndReconcile(ctx context.Context, userID uint, reservationID uint64,
                         estCredits int, llmReq aiservice.ChatReq) error {

    // 关键：此处没有 db.Transaction 包裹
    resp, err := aiservice.Chat(ctx, llmReq)
    actualCredits := computeActualCredits(resp, err)

    // 失败也要 Reconcile：LLM 失败 → actualCredits = 0 → 全额退款
    return ReconcileCredits(ctx, userID, reservationID, actualCredits)
}
```

#### 4.2.4 Reconcile 阶段（事务内，多退少补）

```go
// 伪代码：Reconcile——根据实际消耗调账
func ReconcileCredits(ctx context.Context, userID uint, reservationID uint64,
                      actualCredits int) error {

    txNow := time.Now()
    return db.Transaction(func(tx *gorm.DB) error {
        // 1) SELECT FOR UPDATE 拿 reservation
        var res model.CreditReservation
        if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            First(&res, reservationID).Error; err != nil { return err }
        if res.Status != "reserved" {
            return nil  // 幂等：已 reconciled 直接返回
        }

        delta := actualCredits - res.EstCredits  // >0 少扣需补，<0 多扣需退

        // 2) 按 §4.1 锁顺序加锁三表
        // ... 加锁省略

        // 3) 退还（delta < 0）：按"先 booster 退、再 cycle、最后 trial"的逆序退
        //    补扣（delta > 0）：按 trial → cycle → booster 的扣减顺序
        if delta < 0 {
            refund(-delta, &res, &trial, &cycle, &booster)
        } else if delta > 0 {
            if !canDeductMore(delta, &trial, &cycle, &booster) {
                // 余额不足：记 reservation.status='reconciled_partial'，告警
            } else {
                applyDeduction(delta, &trial, &cycle, &booster, txNow)
            }
        }

        // 4) 更新 reservation status
        res.Status = "reconciled"
        res.ActualCredits = actualCredits
        res.ReconciledAt = txNow
        return tx.Save(&res).Error
    })
}
```

#### 4.2.5 事务边界硬规则汇总

| 阶段 | 是否在事务内 | 持锁时长目标 |
|------|------------|------------|
| Reserve（预扣） | 是 | < 50ms |
| LLM 调用 | **绝对不允许** | N/A |
| Reconcile（对账） | 是 | < 50ms |
| grant 会员 | 是 | < 50ms |
| 续费 | 是 | < 50ms |
| 创建订单 | 是 | < 50ms |
| 支付回调 fulfill | 是 | < 100ms（含 booster 入账） |

任何超过 100ms 的事务都必须在 review 阶段被 P0 阻断。

---

### §4.3 续费 lost update 防护

#### 4.3.1 问题场景

父账户在两个浏览器 tab 同时点"给子账户续费 1 个月"，两次请求几乎同时到达后端。如果不防护：

- T1 读 `expires_at = 2026-06-01`，计算新 `expires_at = 2026-07-01`
- T2 读 `expires_at = 2026-06-01`（与 T1 同时读），计算新 `expires_at = 2026-07-01`
- T1 UPDATE 写入 `2026-07-01`
- T2 UPDATE 写入 `2026-07-01`（覆盖！）
- 最终结果：父账户付了 2 个月的钱，子账户只多了 1 个月。这就是 lost update。

#### 4.3.2 方案对比

| 方案 | 机制 | 优点 | 缺点 |
|------|------|------|------|
| **A：SELECT FOR UPDATE + Go 端计算** | 事务内先 `SELECT ... FOR UPDATE` 锁 sub 行，Go 端读出 `expires_at` 后计算新值，再 UPDATE | 直观、易于添加复杂业务规则（如校验 anchor）；可读性高 | 多一次往返；持锁稍长（仍 < 50ms） |
| **B：纯 SQL OCC（CAS）表达式** | `UPDATE subscription SET expires_at = anchor_add_months(...) WHERE id = ? AND expires_at = old_value`，影响行数 = 0 表示冲突，重试 | 单条 SQL，无持锁问题 | anchor-restore 是 Go 函数，无法在 SQL 表达；改成 SQL 表达式必须用 MySQL 原生 DATE_ADD 但已锁定决策"anchor 算法在 Go 应用层"（R7）；语义复杂时易写错 |

**推荐方案 A**。理由：

1. anchor-restore 算法（AC-4）必须在 Go 端运行（R7 锁定决策）；方案 B 强行用 SQL 写 anchor 算法会破坏决策一致性。
2. SELECT FOR UPDATE 持锁 < 50ms，不构成性能问题。
3. 续费路径还需要写 membership_event、可能更新 first_started_at（过期再开），事务化是天然的。
4. 配合幂等键（§4.5）实现网络重发的二次防护，方案 A 与幂等键组合最稳健。

#### 4.3.3 方案 A 复用 §3.2 GrantOrRenewSubscription

完整 Go 伪代码见 §3.2。本节关注**防 lost update 的关键步骤**——所有续费 / 开通 / 过期再开走同一函数，事务体最顶部 `SELECT ... FOR UPDATE` 锁 `subscription` 行（与 §4.1 字典序兼容：subscription 是该事务内字典序最大的表，无前置锁），从读取 `expires_at` 到 UPDATE 之间整个区间都在事务持锁内，并发请求被 InnoDB 串行化。

下面 ASCII 时序图展示两次并发续费如何通过 SELECT FOR UPDATE + 不同 idem_key 完成正确累加：

```
                Time
                 │
T1 (tab A)       │  T2 (tab B)
─────────────────┼─────────────────
BEGIN            │  BEGIN
SELECT FOR UPDATE│  SELECT FOR UPDATE  ← 等待 T1 释放
sub.expires=6/01 │
total=3          │
                 │
计算 new_expires │
 = anchor+(3+1)  │
 = 7/01          │
UPDATE sub SET   │
 expires=7/01    │
 total=4         │
INSERT event(K1) │
COMMIT           │
                 │  ← 此时 T2 拿到锁，重新 SELECT 看到最新值
                 │  sub.expires=7/01, total=4
                 │  计算 new_expires = anchor+(4+1) = 8/01
                 │  UPDATE sub SET expires=8/01, total=5
                 │  INSERT event(K2) ← 不同 idem_key，UNIQUE 不冲突
                 │  COMMIT
```

最终：父账户付了 2 次钱 → sub 累加 2 个月 → membership_event 留 2 行。**未发生 lost update**。

若 T2 是 T1 的网络重发（同 idem_key），则 §3.2 [1] 幂等检查命中，直接返回 T1 结果，事务退出，不重复扣减。

#### 4.3.4 验收（参见 AC-16a / AC-16b）

- **AC-16a**：两个 tab 各自携带不同 idem_key → 两次都进入事务、各自 SELECT FOR UPDATE 串行化 → 最终 expires_at += 2 个月，membership_event 留两条；
- **AC-16b**：同一次点击的网络重发（同 idem_key）→ 第二次 INSERT membership_event 命中 UNIQUE 冲突 → 当作幂等成功直接返回，最终 expires_at 只 += 1 个月。

---

### §4.4 cycle 懒创建并发证明

#### 4.4.1 设计目标

- credit_cycle 行**首次扣分时**才创建（lazy），无需 cron 提前预热；
- 同一 user 同一 cycle_start 只允许 1 行（UNIQUE 索引强制）；
- 多个并发请求同时进入 ensureCurrentCycle 时：
  - 不报错给上游
  - 不创建多行
  - 不丢失任何一次扣减
  - 不死锁

#### 4.4.2 ensureCurrentCycle 的标准模式

```go
// 伪代码：cycle 懒创建——GORM clause.OnConflict + 重新 SELECT FOR UPDATE
// 与 §3.4 的 ensureCurrentCycle 实现完全一致（保持单一真相）
func ensureCurrentCycle(tx *gorm.DB, userID uint, sub *model.Subscription, txNow time.Time) (
    *model.CreditCycle, error) {

    // 1) 计算当前 cycle_start / cycle_end（半开区间，见 EC-7）
    cycleIdx := computeCycleIndex(sub.CurrentStartedAt, txNow)
    cycleStart := anchorAddMonths(sub.CurrentStartedAt, cycleIdx)
    cycleEnd := minTime(anchorAddMonths(sub.CurrentStartedAt, cycleIdx+1), sub.ExpiresAt)

    // 2) 试 INSERT（GORM clause.OnConflict + DoNothing；UNIQUE(user_id, cycle_start) 保证只插一行）
    newCycle := model.CreditCycle{
        UserID:           userID,
        SubscriptionID:   sub.ID,
        CycleStart:       cycleStart,
        CycleEnd:         cycleEnd,
        CreditsGranted:   2000,
        CreditsRemain:    2000,
    }
    if err := tx.Clauses(clause.OnConflict{
        Columns:   []clause.Column{{Name: "user_id"}, {Name: "cycle_start"}},
        DoNothing: true,
    }).Create(&newCycle).Error; err != nil {
        return nil, err
    }
    // 注：GORM 在 MySQL 下将上述 OnConflict{DoNothing:true} 翻译为 `INSERT IGNORE INTO ...`，
    //     仅对 UNIQUE/PRIMARY KEY 冲突生效（与 ON DUPLICATE KEY UPDATE id=id 等价、且更显式）

    // 3) 重新 SELECT FOR UPDATE 拿到行（无论是 INSERT 还是已存在）
    var cycle model.CreditCycle
    if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
        Where("user_id = ? AND cycle_start = ?", userID, cycleStart).
        First(&cycle).Error; err != nil {
        return nil, err
    }
    return &cycle, nil
}
```

#### 4.4.3 并发证明（编号步骤展示）

考虑两个并发请求 R1、R2 同时调用 `ensureCurrentCycle(userID=42, txNow=T)`，且数据库内不存在该 cycle 行。两请求各自在独立事务中。

| 步骤 | R1 | R2 | DB 状态 |
|------|----|----|---------|
| 1 | 计算 cycleStart=2026-04-01 | 计算 cycleStart=2026-04-01 | （无 cycle 行） |
| 2 | 发出 INSERT IGNORE（GORM OnConflict DoNothing 翻译） | 同时发出相同的 INSERT IGNORE | InnoDB 把两个请求按到达顺序串行化 |
| 3 | INSERT 成功，新行 id=100，持有 row lock | 等待（被 R1 的 INSERT intent lock 阻塞） | 1 行 (id=100, user_id=42) |
| 4 | 继续后续 SELECT FOR UPDATE id=100，重入持锁 OK | 仍等待 | 1 行 |
| 5 | R1 在事务内继续做 deduct，最终 commit | R1 commit 释放锁，R2 INSERT IGNORE 命中 UNIQUE 冲突 → no-op（不修改行） | 1 行 |
| 6 | R1 已结束 | R2 SELECT FOR UPDATE WHERE user_id=42 AND cycle_start=2026-04-01 → 拿到 id=100 行的 X 锁 | 1 行 |
| 7 | — | R2 在事务内继续做 deduct（基于 R1 已 commit 后的最新 credits_remaining） | 1 行 |
| 8 | — | R2 commit | 1 行，credits_remaining 反映两次扣减 |

**关键属性**：

1. **不重复创建**：UNIQUE(user_id, cycle_start) 保证 step 5 的第二次 INSERT 不会插入新行；
2. **不死锁**：R2 的等待属于"等同一行的 INSERT lock"，单方向等待，不形成循环；
3. **不丢失扣减**：R2 在 step 6 拿到的是 R1 commit 后的最新 credits_remaining，扣减基于最新值进行；
4. **乐观语义**：INSERT IGNORE（GORM OnConflict DoNothing）是 idempotent no-op，不修改任何字段，避免误更新 credits_granted。

#### 4.4.4 边界场景

- **R1 失败回滚**：step 5 R1 rollback 后，刚插入的行随 R1 事务回滚被自动删除；R2 的 INSERT 会重新成为"首次插入"成功，行为正常。
- **超过 sub.expires_at**：step 1 getActiveSub 返回 nil → ensureCurrentCycle 返回 ErrSubscriptionExpired，不创建任何 cycle。
- **MySQL 死锁回滚**：极端情况下若 InnoDB 检测到死锁（不应发生但需兜底），返回特定错误码后由上层重试 1 次（与 R2 风险缓解一致）。

---

### §4.5 idempotency_key HTTP header 协议

#### 4.5.1 协议定义

为支持网络层重发场景下的幂等性，写操作类 API 接受 HTTP header `Idempotency-Key`（首字母大写 + 连字符，参考 Stripe API 规范）。

**适用范围**：

| API | 是否要求 idem_key |
|-----|-------------------|
| `POST /v1/users/children/:child_id/grant-membership` | **必须** |
| `POST /v1/orders` | **必须** |
| `POST /v1/orders/:id/payment-callback`（支付回调） | **必须**（网关重投） |
| `POST /v1/credits/reserve` | **必须** |
| `POST /v1/credits/reconcile` | **必须** |
| 读类（GET）API | 不需要 |

**生成约定**：

- 客户端生成 UUIDv4（128-bit），格式如 `550e8400-e29b-41d4-a716-446655440000`；
- 浏览器一次"用户点击"在其生命周期内复用同一个 UUID（即使因网络抖动产生多次 HTTP 重发）；
- 不同的用户操作必须使用不同的 UUID（避免误判为重发）；
- 服务端校验：长度 36 字符，正则 `^[0-9a-fA-F-]{36}$`，不合法返回 `ErrInvalidIdempotencyKey`。

#### 4.5.2 服务端实现：middleware 提取

```go
// 伪代码：middleware 提取 Idempotency-Key 到 gin.Context
func IdempotencyKeyMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader("Idempotency-Key")
        if key != "" {
            if !isValidUUID(key) {
                core.WriteResponse(c, errno.ErrInvalidIdempotencyKey.SetMessage(
                    "Idempotency-Key 必须是 UUIDv4 格式"), nil)
                c.Abort()
                return
            }
            c.Set("idempotency_key", key)
        }
        c.Next()
    }
}
```

#### 4.5.3 Controller 层处理模板

```go
// 伪代码：grant 会员的 controller，演示 idem_key UNIQUE 冲突处理
func GrantMembership(c *gin.Context) {
    var req struct {
        ProductType string `json:"product_type" binding:"required,oneof=trial monthly"`
        Months      int    `json:"months" binding:"omitempty,min=1,max=12"`
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }

    childID, err := strconv.Atoi(c.Param("child_id"))
    if err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage("child_id 非法"), nil)
        return
    }

    parentID := c.GetUint("userID")

    // 1) 提取 idempotency_key（middleware 已校验合法性）
    idemKey, ok := c.Get("idempotency_key")
    if !ok {
        core.WriteResponse(c, errno.ErrIdempotencyKeyRequired.SetMessage(
            "写操作必须携带 Idempotency-Key header"), nil)
        return
    }

    // 2) 调用 biz 层
    err = biz.GrantMembership(c.Request.Context(),
        parentID, uint(childID), req.ProductType, req.Months, idemKey.(string))

    // 3) 处理幂等结果（biz 层在 UNIQUE 冲突时返回 nil + 已存在 event 的 ID）
    if err != nil {
        if errors.Is(err, biz.ErrIdemKeyConflictDifferentBody) {
            // Stripe 标准：同 idem_key 但请求体不同 → 409 Conflict
            core.WriteResponse(c,
                errno.ErrIdempotencyKeyConflict.SetMessage(
                    "同一 Idempotency-Key 不能用于不同请求"), nil)
            return
        }
        core.WriteResponse(c, err, nil)
        return
    }

    // 4) 成功（无论是首次还是重发）都返回 200 + 同样的响应体
    core.WriteResponse(c, nil, gin.H{"granted": true})
}
```

#### 4.5.4 biz 层 UNIQUE 冲突处理

```go
// 伪代码：biz 层 UNIQUE 冲突的"幂等成功"处理
func GrantMembership(ctx context.Context, parentUID, childUID uint,
                     productType string, months int, idemKey string) error {

    return db.Transaction(func(tx *gorm.DB) error {
        // 1) 提前查 membership_event：是否已有该 idem_key
        var existing model.MembershipEvent
        err := tx.Where("idempotency_key = ?", idemKey).First(&existing).Error
        if err == nil {
            // 已存在：校验请求体一致性（防止误用 idem_key 做不同请求）
            if existing.ChildUserID != childUID ||
                existing.ProductType != productType ||
                existing.Months != months {
                return ErrIdemKeyConflictDifferentBody
            }
            return nil  // 完全匹配 → 幂等成功
        }
        if !errors.Is(err, gorm.ErrRecordNotFound) {
            return err
        }

        // 2) 按 §4.1 锁顺序 + 业务逻辑写入
        // ... 省略
        evt := model.MembershipEvent{IdempotencyKey: idemKey, ...}
        if err := tx.Create(&evt).Error; err != nil {
            // UNIQUE 冲突：可能是另一个并发请求恰好同时写入
            if isUniqueViolation(err, "uk_membership_event_idem") {
                // 重新读取并校验一致性
                var concurrent model.MembershipEvent
                if e2 := tx.Where("idempotency_key = ?", idemKey).
                    First(&concurrent).Error; e2 == nil {
                    if concurrent.ChildUserID != childUID ||
                        concurrent.ProductType != productType {
                        return ErrIdemKeyConflictDifferentBody
                    }
                    return nil  // 幂等成功
                }
                return e2
            }
            return err
        }
        return nil
    })
}
```

#### 4.5.5 关键规则汇总

- 服务端**永远返回 200** 当 idem_key 命中已存在事件且请求体一致（不返回 409，不返回 4xx）；
- 服务端返回 **409 ErrIdempotencyKeyConflict** 仅当 idem_key 已存在但请求体不一致（Stripe 标准）；
- DB 层 UNIQUE 索引名固化为 `uk_membership_event_idem`，便于代码精确判定冲突类型；
- 客户端不应基于响应体判断"是新开通还是重发"——服务端响应体应当对二者一致。

---

### §4.6 事务起点固定 timestamp 模式

#### 4.6.1 强制约定

**所有 biz 函数事务开始时必须在事务体最顶部声明：**

```go
txNow := time.Now()
```

事务内**所有**与时间相关的判断、写入、对比，必须使用 `txNow`，**禁止**重新调用 `time.Now()`。

#### 4.6.2 为什么必须固定

事务执行可能跨越数百毫秒（含锁等待 + 多次 SQL 往返）。如果事务内反复调用 `time.Now()`，可能出现：

1. 同一个事务内"sub.expires_at > now"判定时是 true，写 membership_event 时 occurred_at 取的另一个 now 已经超过 sub.expires_at → 数据不一致；
2. 边界用例（EC-1/EC-2）测试时不可重现：同一组输入第一次跑过、第二次跑挂；
3. cycle_start / cycle_end 的半开区间判定如果对比 now 不一致，可能漏扣或双扣。

#### 4.6.3 正模式

```go
// ✅ 正模式：事务起点固定 txNow，全事务统一使用
func GrantTrial(ctx context.Context, parentUID, childUID uint, idemKey string) error {
    return db.Transaction(func(tx *gorm.DB) error {
        txNow := time.Now()  // 事务起点固定

        // 校验 1：trial_grant 是否已存在
        var existing model.TrialGrant
        if err := tx.Where("user_id = ?", childUID).First(&existing).Error; err == nil {
            return errno.ErrTrialAlreadyGranted
        }

        // 校验 2：sub 是否在期（用 txNow，不再调 time.Now()）
        var sub model.Subscription
        err := tx.Where("user_id = ? AND expires_at > ?", childUID, txNow).
            First(&sub).Error
        if err == nil {
            return errno.ErrTrialNotAllowedForActivePro
        }

        // 写 trial_grant，所有时间字段都用 txNow
        trial := model.TrialGrant{
            UserID:           childUID,
            CreditsTotal:     200,
            CreditsRemaining: 200,
            ActivatedAt:      txNow,
            ExpiresAt:        txNow.Add(3 * 24 * time.Hour),  // 固定 3 天
            CreatedAt:        txNow,
        }
        if err := tx.Create(&trial).Error; err != nil { return err }

        // 写 membership_event，occurred_at 也用 txNow
        evt := model.MembershipEvent{
            EventType:      "trial_granted",
            GranterUserID:  parentUID,
            ChildUserID:    childUID,
            ProductType:    "trial",
            IdempotencyKey: idemKey,
            OccurredAt:     txNow,  // 与 trial.ActivatedAt 保持一致
        }
        return tx.Create(&evt).Error
    })
}
```

#### 4.6.4 反模式（禁止）

```go
// ❌ 反模式 1：事务内多次调 time.Now()
func BadGrantTrial(ctx context.Context, ...) error {
    return db.Transaction(func(tx *gorm.DB) error {
        // 校验时间
        now1 := time.Now()
        if err := tx.Where("expires_at > ?", now1).First(&sub).Error; ...

        // 写入时间——可能与 now1 相差几十 ms
        trial := model.TrialGrant{ActivatedAt: time.Now()}  // ⚠️ 又一次 time.Now()
        // ...
        evt := model.MembershipEvent{OccurredAt: time.Now()}  // ⚠️ 第三次
        // ...
    })
}

// ❌ 反模式 2：事务外算 ts、事务内算另一个 ts
func BadDeduct(ctx context.Context, ...) error {
    nowOuter := time.Now()  // 事务外算的，距离事务真正开始可能差几毫秒到几秒
    return db.Transaction(func(tx *gorm.DB) error {
        nowInner := time.Now()  // 事务内又算了一遍，与 nowOuter 不等
        // ... 用 nowOuter 判定 expires，用 nowInner 写入 occurred_at →
        // 两个时间不一致，对账时数据无法关联
    })
}
```

#### 4.6.5 例外（极少数允许）

只有以下两种情况允许在事务内多次取 time：

1. **测量持锁时长**用于 metrics（不参与业务判定）：

```go
return db.Transaction(func(tx *gorm.DB) error {
    txNow := time.Now()
    // ... 业务逻辑
    metrics.TxDuration.Observe(time.Since(txNow).Seconds())  // ✅ 仅用于监控
    return nil
})
```

2. **事务内调用外部服务的超时控制**（事务内不应调外部服务，所以基本不会发生）。

#### 4.6.6 review 阶段检查项

- 事务体内**任何** time 来源不是 txNow → **P0**（包括但不限于：第二次 `time.Now()` 调用、其他时钟源 `time.Since(...)` 用于业务判定、传给辅助函数的 ts 入参不是 txNow——例如 `anchor_add_months(otherTs, n)` 而不是 `anchor_add_months(txNow, n)` 或基于 sub.CurrentStartedAt 的派生值；helper 内部读 `time.Now()` 而非接受 txNow 参数也属此项）；
- 事务起点 `txNow` 未在事务体最顶部声明 → P1；
- 事务外算 ts 后传入事务内使用 → P1（应改为事务内重新算 txNow，且事务外的 ts 不得参与事务内业务判定）；
- model 字段写入时直接用 `time.Now()` 而非 txNow → P0；
- 测量持锁时长用于 metrics（仅监控，不参与业务判定）是唯一允许的 `time.Since(txNow)` 用法（§4.6.5）。

---

### §4 小结：硬规则核对清单

实现 S4 编码阶段时，每个 task 完成后 reviewer 必须对照下表逐项核对：

- [ ] 所有 SELECT FOR UPDATE 通过 lockMembershipRows helper 发起，未裸写
- [ ] 锁顺序符合 user_id ASC + 表字典序（credit_cycle < membership_event < subscription < trial_grant < user_booster_balance）
- [ ] 事务内**没有**任何 LLM 调用（aiservice.* 在事务体外）
- [ ] Reserve / Reconcile 双阶段事务持锁均 < 50ms
- [ ] 续费路径使用 SELECT FOR UPDATE + Go 端计算 + UPDATE 模式，**不**用纯 SQL OCC（违反 R7）
- [ ] ensureCurrentCycle 使用 ON DUPLICATE KEY UPDATE id=id + 重新 SELECT FOR UPDATE 模式
- [ ] 所有写操作 API 在 controller 层校验 Idempotency-Key 存在与合法
- [ ] 所有 membership_event INSERT 都带 idempotency_key 字段
- [ ] UNIQUE 冲突 + 请求体一致 → 返回 200（幂等成功）；UNIQUE 冲突 + 请求体不一致 → 返回 409
- [ ] biz 函数事务体最顶部声明 `txNow := time.Now()`，事务内**唯一**的时间源
- [ ] 所有 model 时间字段（ActivatedAt / ExpiresAt / OccurredAt / CreatedAt）使用 txNow

任意一条未满足 = P0 阻断进入 §5（验证策略）。

---

## §5 API 契约

本节定义会员积分体系重构后所有对外暴露的 HTTP 端点。所有端点遵循 `.claude/rules/api-design.md` 规约：

- 统一响应：`core.WriteResponse(c, err, data)` → `{"code": 0, "message": "ok", "data": ...}`
- 用户端鉴权：`AuthMiddleware()`，从 `current_user` context 提取 `*model.User`
- 管理端鉴权：`AdminAuthMiddleware()`
- Controller 仅做参数绑定 / auth 提取 / 调用 biz / 格式化响应。业务逻辑全部委托至 `internal/numind/biz/credit/`。

幂等性约定：所有写操作端点（grant、order、payment notify）支持 HTTP header `Idempotency-Key`（客户端生成 UUID v4，64 字符上限）。后端将其作为 `membership_event.idempotency_key` 列上的 UNIQUE 索引去重键。同 key 重放：第一次成功后续返回**首次结果（200 + 原 data）**，状态不变。

---

### §5.1 `POST /v1/users/children/:child_id/grant-membership`

父账户为子账户开通试用包或开通/续费 Pro 月卡。**API 不区分 grant 与 renew**——后端依据子账户当前 subscription 状态自动判定（无 / 已过期 → 新开通；在期 → 续费延期）。trial 路径走独立逻辑（trial_grant 表 lifetime UNIQUE）。本端点**不走支付**，记入 `membership_event.source='b2b_grant'`，月底 admin 端 `b2b-billing-report` 聚合给财务做对公结算。

**HTTP**：`POST /v1/users/children/:child_id/grant-membership`

**Header**：

| Header | 必填 | 说明 |
|---|---|---|
| `Authorization` | 是 | `Bearer <user_token>`（父账户 token，AuthMiddleware 验证） |
| `Idempotency-Key` | 是 | 客户端 UUID v4，长度 ≤ 64。后端写入 `membership_event.idempotency_key` 用于去重 |
| `Content-Type` | 是 | `application/json` |

**Path 参数**：

| 字段 | 类型 | 必填 | 约束 | 说明 |
|---|---|---|---|---|
| `child_id` | uint | 是 | > 0 | 目标子账户 user_id；biz 层校验 `child.parent_user_id == currentUser.ID` |

**Body**：

```json
{
  "product_type": "trial",      // required, oneof=trial monthly
  "months": 3,                  // optional; trial 必须为 0/缺省，monthly 必须 ∈ [1,12]
  "reason": "客户申请新开通"     // optional, max=500
}
```

| 字段 | 类型 | 必填 | 约束 | 说明 |
|---|---|---|---|---|
| `product_type` | string | 是 | `oneof=trial monthly` | 产品类型 |
| `months` | int | 视情况 | `min=1, max=12` 当 `product_type=monthly` 时必填；`product_type=trial` 时被忽略 | 月数 |
| `reason` | string | 否 | `max=500` | 父账户备注，写入 `membership_event.metadata` |

**成功响应（200）**：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "child_user_id": 1234,
    "product_type": "monthly",
    "months": 3,
    "event_id": 98765,                            // membership_event.id
    "membership_state": {
      "has_active_subscription": true,
      "subscription_first_started_at": "2026-04-01T00:00:00Z",
      "subscription_current_started_at": "2026-04-01T00:00:00Z",
      "subscription_expires_at": "2026-07-01T00:00:00Z",   // 已包括本次 +N 月延期
      "total_months_purchased": 3
    }
  }
}
```

trial 路径成功响应：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "child_user_id": 1234,
    "product_type": "trial",
    "months": 0,
    "event_id": 98766,
    "membership_state": {
      "has_active_trial": true,
      "trial_granted_at": "2026-04-29T03:14:22Z",
      "trial_expires_at": "2026-05-02T03:14:22Z"
    }
  }
}
```

**失败响应**：

| HTTP | Code | Message | 触发场景 | 关联 |
|---|---|---|---|---|
| 400 | `InvalidParameter.BindError` | 请求参数错误 | body 不符合 binding tag | — |
| 400 | `InvalidParameter` | 月数必须在 1-12 之间 | `product_type=monthly` 且 `months ∉ [1,12]` | AC-12 |
| 400 | `InvalidParameter` | trial 不接受 months 参数 | `product_type=trial` 且 `months > 0` | AC-12 |
| 401 | `AuthFailure.TokenInvalid` | Token was invalid | 无 token / token 过期 | — |
| 403 | `AuthFailure.Forbidden` | 该子账户不属于当前账户 | child.parent_user_id != currentUser.ID | EC-3 |
| 404 | `User.NotFound` | 子账户不存在 | child_id 无对应 user 行 | — |
| 409 | `Trial.AlreadyGranted` | 该账户已购买过试用包 | trial_grant 表已有该 user_id 行（任意状态） | EC-3 / Q1 |
| 409 | `Trial.NotAllowedForActivePro` | 在期 Pro 用户不可再开通试用 | grant trial 时 child 当前有 active subscription | EC-4 / Q1 |
| 500 | `InternalError` | 内部错误 | 兜底 | — |

**权限要求**：

- 必须是用户端 token（user_token），非 admin token
- 必须是父账户身份：`current_user.ParentUserID == nil`（biz 层兜底校验，前端 UI 也会 gate）
- `child.parent_user_id` 必须等于 `current_user.ID`

**幂等性**：

- 同 `Idempotency-Key` 重放 → 返回首次成功结果，subscription / trial_grant 不会被二次延期
- 不同 `Idempotency-Key` 视为不同 grant（即使 body 完全相同）→ 续费多月（AC-16a）

**调用示例**：

```bash
curl -X POST "https://api.example.com/v1/users/children/1234/grant-membership" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Idempotency-Key: 7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e6f" \
  -H "Content-Type: application/json" \
  -d '{"product_type":"monthly","months":3,"reason":"客户Q2续费"}'
```

**关联 PRD**：US-2 / US-5 / AC-1 / AC-2 / AC-3 / AC-5 / AC-11 / AC-12 / AC-16a / AC-16b / EC-3 / EC-4 / EC-4b

---

### §5.2 `POST /v1/orders`

booster 加量包下单入口。trial 与 Pro **不走此接口**——它们只能通过父账户 grant（§5.1）。

**payer 语义**：本接口**不接受 `payer_id` 字段**。付款人（payer）始终 = HTTP 请求 token 的主体（即 `current_user`）；body 中的 `user_id` 字段直接表达受益人（Beneficiary）。

**自购 vs 代付**判定纯由 token 主体与 body `user_id` 关系决定（biz 层）：

1. **C 端自购 booster**：`body.user_id == current_user.ID`。要求受益人当前 active member（trial 或 sub 任一在期）
2. **父账户为子账户代购 booster**（D3 决策：API 允许但前端不暴露入口）：`body.user_id != current_user.ID`，要求 `subUser.ParentUserID == current_user.ID` 且 child 当前 active member

**HTTP**：`POST /v1/orders`

**Header**：

| Header | 必填 | 说明 |
|---|---|---|
| `Authorization` | 是 | `Bearer <user_token>`（付款人 token） |
| `Idempotency-Key` | 是 | 客户端 UUID v4，≤ 64 字符。后端用作订单幂等键 |
| `Content-Type` | 是 | `application/json` |

**Body**：

```json
{
  "user_id": 1234,                  // required, 受益人 user_id（自购时 = payer.ID）
  "product_type": "booster",        // required, 必须为 booster；trial/monthly 直接返回 ErrSelfPurchaseDisabled
  "quantity": 3,                    // required, ≥1, ≤10000；总价 = quantity × 2990 cents
  "pay_channel": "wechat"           // required, oneof=wechat alipay
}
```

| 字段 | 类型 | 必填 | 约束 | 说明 |
|---|---|---|---|---|
| `user_id` | uint | 是 | > 0 | 加量包归属用户。自购时 = payer.ID；代付时必须是 payer 的子账户 |
| `product_type` | string | 是 | `oneof=booster` | 当前接口只接受 booster；trial/monthly 路径走 `/v1/users/children/:id/grant-membership` |
| `quantity` | int | 是 | `min=1, max=10000` | 加量包份数；> 10000 返回 `ErrBoosterQuantityExceedsLimit`（Q2） |
| `pay_channel` | string | 是 | `oneof=wechat alipay` | 支付渠道 |

**成功响应（200）**：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "order_id": 567890,
    "out_trade_no": "BOOST20260429031422567890",
    "user_id": 1234,
    "payer_id": 1234,
    "product_type": "booster",
    "quantity": 3,
    "amount_cents": 8970,             // = 3 × 2990
    "pay_channel": "wechat",
    "status": "pending",              // 等待支付回调
    "pay_params": {                   // 客户端 SDK 唤起支付所需参数
      "appid": "wx...",
      "noncestr": "...",
      "package": "Sign=WXPay",
      "partnerid": "...",
      "prepayid": "...",
      "timestamp": "1730248462",
      "sign": "..."
    },
    "created_at": "2026-04-29T03:14:22Z"
  }
}
```

**失败响应**：

| HTTP | Code | Message | 触发场景 |
|---|---|---|---|
| 400 | `InvalidParameter.BindError` | 请求参数错误 | body 不符合 binding tag |
| 400 | `Booster.QuantityExceedsLimit` | 单次最多购买 10000 份 | `quantity > 10000`（AC-13 / Q2） |
| 400 | `InvalidParameter` | 不支持的产品类型 | `product_type` 非 booster（trial/monthly 走 grant） |
| 401 | `AuthFailure.TokenInvalid` | Token was invalid | 无 token |
| 403 | `Membership.SelfPurchaseDisabled` | 请联系管理员开通会员 | C 端自购 trial/monthly（防御性，本接口实际不接受这两个 product_type） |
| 403 | `AuthFailure.Forbidden` | 无权为该用户创建订单 | 代付时 child.parent_user_id != payer.ID |
| 403 | `Membership.NotActive` | 需要在期会员才能购买加量包 | 受益人当前无 active subscription/trial（AC-13c / EC-11） |
| 500 | `InternalError` | 内部错误 | 兜底 |

**权限要求**：

- 用户端 token
- 自购：受益人 = payer 本人
- 代付：受益人 = payer 的直接子账户（`subUser.ParentUserID == payer.ID`）
- 受益人必须有 active 会员状态（trial 或 subscription 任一在期）

**幂等性**：

- 同 Idempotency-Key 重放 → 返回首次创建的 order（含同一份 pay_params），不会创建第二个订单
- payment notify 回调路径（`/v1/payment/wechat/notify` / `/v1/payment/alipay/notify`）独立幂等，由 `out_trade_no` 去重

**调用示例**：

```bash
curl -X POST "https://api.example.com/v1/orders" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Idempotency-Key: 7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e6f" \
  -H "Content-Type: application/json" \
  -d '{"user_id":1234,"product_type":"booster","quantity":3,"pay_channel":"wechat"}'
```

**关联 PRD**：US-4 / AC-13 / AC-13b / AC-13c / EC-11

---

### §5.3 `GET /v1/credits/balance`

用户查询自己**完整**的积分余额，包含 booster（与父账户视角相对，本端点是子账户视角）。

**HTTP**：`GET /v1/credits/balance`

**Header**：

| Header | 必填 | 说明 |
|---|---|---|
| `Authorization` | 是 | `Bearer <user_token>` |

**Query 参数**：无

**成功响应（200）**：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "user_id": 1234,
    "membership_state": {
      "has_active_trial": true,
      "trial_granted_at": "2026-04-27T00:00:00Z",
      "trial_expires_at": "2026-04-30T00:00:00Z",
      "has_active_subscription": true,
      "subscription_first_started_at": "2026-04-29T00:00:00Z",
      "subscription_current_started_at": "2026-04-29T00:00:00Z",
      "subscription_expires_at": "2026-07-29T00:00:00Z",
      "total_months_purchased": 3
    },
    "trial_remaining": 150,                       // trial_grant.credits_remaining，无 trial 则 0
    "cycle_remaining": 1850,                      // 当前 cycle.credits_remaining，无 active sub 则 0
    "cycle_start": "2026-04-29T00:00:00Z",        // 当前 cycle 起点；无 cycle 则 null（懒创建未触发）
    "cycle_end": "2026-05-29T00:00:00Z",          // min(anchor_add_months(cycle_start, 1), sub.expires_at)
    "booster_total": 1800,                        // booster_remain 合计
    "booster_usable": 1800,                       // 当前可用 booster；冻结时 = 0 但 booster_total 仍展示
    "booster_frozen": false,                      // true 时前端展示锁标 + "需要会员" CTA
    "next_refill_at": "2026-05-29T00:00:00Z"      // = cycle_end，下次月度刷新点（无 sub 则 null）
  }
}
```

**字段语义**：

- `membership_state` 由 biz 层 `ComputeMembershipState(ctx, userID, now)` 实时计算（时间驱动，§3 算法）
- `cycle_remaining` 在 cycle 未懒创建时返回当前 cycle 的"应得额度"（2000）减去 0 = 2000，等待第一次扣分时才真正落库（EC-6 / AC-9）
- `booster_frozen` = `!has_active_subscription && !has_active_trial && booster_total > 0`（AC-7 / AC-8）

**失败响应**：

| HTTP | Code | Message | 触发场景 |
|---|---|---|---|
| 401 | `AuthFailure.TokenInvalid` | Token was invalid | 无 token |
| 500 | `InternalError` | 内部错误 | 兜底 |

**权限要求**：用户端 token，仅查询自己

**幂等性**：纯读，幂等

**关联 PRD**：US-1 / US-3 / AC-14 / AC-22 / AC-23 / EC-11

---

### §5.4 `GET /v1/users/children/:child_id/balance`

父账户查询子账户的余额。**不含 booster**（D3 决策：父账户负责开通会员，booster 是子账户日常自助消耗，仅子账户本人和 admin 可见）。响应中**移除** `booster_*` 与 `next_refill_at`（避免泄漏 booster 余额）。

**HTTP**：`GET /v1/users/children/:child_id/balance`

**Header**：

| Header | 必填 | 说明 |
|---|---|---|
| `Authorization` | 是 | `Bearer <user_token>`（父账户 token） |

**Path 参数**：

| 字段 | 类型 | 必填 | 约束 | 说明 |
|---|---|---|---|---|
| `child_id` | uint | 是 | > 0 | 子账户 user_id；biz 层校验 parent-child |

**Query 参数**：无

**成功响应（200）**：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "user_id": 1234,
    "membership_state": {
      "has_active_trial": true,
      "trial_granted_at": "2026-04-27T00:00:00Z",
      "trial_expires_at": "2026-04-30T00:00:00Z",
      "has_active_subscription": true,
      "subscription_first_started_at": "2026-04-29T00:00:00Z",
      "subscription_current_started_at": "2026-04-29T00:00:00Z",
      "subscription_expires_at": "2026-07-29T00:00:00Z",
      "total_months_purchased": 3
    },
    "trial_remaining": 150,
    "cycle_remaining": 1850,
    "cycle_start": "2026-04-29T00:00:00Z",
    "cycle_end": "2026-05-29T00:00:00Z"
    // 注意：无 booster_total / booster_usable / booster_frozen / next_refill_at
  }
}
```

**失败响应**：

| HTTP | Code | Message | 触发场景 |
|---|---|---|---|
| 401 | `AuthFailure.TokenInvalid` | Token was invalid | 无 token |
| 403 | `AuthFailure.Forbidden` | 该子账户不属于当前账户 | child.parent_user_id != currentUser.ID |
| 404 | `User.NotFound` | 子账户不存在 | child_id 无对应 user |
| 500 | `InternalError` | 内部错误 | 兜底 |

**权限要求**：用户端 token，且是 child 的直接父账户

**幂等性**：纯读，幂等

**关联 PRD**：US-6 / 权限规则表"查询子账户余额（不含 booster）"

---

### §5.5 `GET /v1/admin/users/:user_id/balance`

Admin 查询任意用户的**完整**余额，含 booster（admin 视角无隐私边界）。

**HTTP**：`GET /v1/admin/users/:user_id/balance`

**Header**：

| Header | 必填 | 说明 |
|---|---|---|
| `Authorization` | 是 | `Bearer <admin_token>`（AdminAuthMiddleware） |

**Path 参数**：

| 字段 | 类型 | 必填 | 约束 | 说明 |
|---|---|---|---|---|
| `user_id` | uint | 是 | > 0 | 任意用户 ID |

**成功响应（200）**：响应结构与 §5.3 完全一致（包含 booster 全部字段）。

**失败响应**：

| HTTP | Code | Message | 触发场景 |
|---|---|---|---|
| 401 | `AuthFailure.TokenInvalid` | Token was invalid | 无/错误 admin token |
| 404 | `User.NotFound` | 用户不存在 | user_id 无对应 user |
| 500 | `InternalError` | 内部错误 | 兜底 |

**权限要求**：管理端 token

**幂等性**：纯读，幂等

**关联 PRD**：US-8 / 权限规则表"Admin 查询任意用户完整余额"

---

### §5.6 `GET /v1/admin/b2b-billing-report`

B2B 月度账单：按月聚合所有父账户帮子账户开通的事件。**新口径走 `membership_event` 表**（替代当前 `b2b_billing.go` 扫 `credit_package` 的实现）。跨切换日的当月走双口径拼接（详见 §7）。

**HTTP**：`GET /v1/admin/b2b-billing-report`

**Header**：

| Header | 必填 | 说明 |
|---|---|---|
| `Authorization` | 是 | `Bearer <admin_token>` |

**Query 参数**：

| 字段 | 类型 | 必填 | 约束 | 说明 |
|---|---|---|---|---|
| `month` | string | 是 | `^\d{4}-(0[1-9]|1[0-2])$` | 账单月份，UTC 时区 |
| `granter_user_id` | uint | 否 | > 0 | 仅查指定父账户的账单（缺省 = 所有父账户） |

**成功响应（200）**：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "month": "2026-04",
    "cutover_date": "2026-06-03T00:00:00Z",
    "source": "legacy_only",
    "by_parent": [
      {
        "parent_user_id": 100,
        "parent_username": "alice_corp",
        "events_count": 5,
        "amount_cents": 49500,
        "details": [
          {
            "event_id": 12345,
            "child_user_id": 1234,
            "child_username": "child_a",
            "event_type": "sub_granted",
            "product_type": "monthly",
            "months": 3,
            "quantity": null,
            "amount_cents": 29700,
            "occurred_at": "2026-04-15T08:30:00Z",
            "source": "b2b_grant",
            "idempotency_key": "7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e6f"
          },
          {
            "event_id": 12346,
            "child_user_id": 1235,
            "child_username": "child_b",
            "event_type": "trial_granted",
            "product_type": "trial",
            "months": null,
            "quantity": null,
            "amount_cents": 990,
            "occurred_at": "2026-04-16T09:15:00Z",
            "source": "b2b_grant",
            "idempotency_key": "..."
          }
        ]
      }
    ],
    "total_amount_cents": 49500,
    "total_events_count": 5,
    "active_parents_count": 1
  }
}
```

**字段语义**：

- `cutover_date`：系统配置的切换日 UTC 时间戳；用于前端展示"跨切换日月份"提示
- `source`：枚举 `legacy_only` / `cutover_split` / `new_only`
  - `legacy_only`：`month_end <= cutover_date` → 全部走 credit_package 旧口径（§7.5）
  - `cutover_split`：`month_start < cutover_date < month_end` → 双口径拼接（§7.3）
  - `new_only`：`month_start >= cutover_date` → 全部走 membership_event 新口径（§7.6）
- `event_type` 枚举：`trial_granted` / `sub_granted` / `sub_renewed` / `booster_granted`（B2B 报表中 booster 来自父账户代付路径）
- `months` / `quantity` 字段语义（与 §2.5 schema 一致）：
  - `sub_granted` / `sub_renewed`：`months` 非 NULL（1~12），`quantity` 为 `null`
  - `booster_granted`（含老口径 booster_purchased）：`quantity` 非 NULL（≥1），`months` 为 `null`
  - `trial_granted`：两者都为 `null`（前端展示空缺即可，不要回退到 `0`）

**失败响应**：

| HTTP | Code | Message | 触发场景 |
|---|---|---|---|
| 400 | `InvalidParameter.BindError` | month 参数必填，格式 YYYY-MM | 缺失 / 格式错 |
| 401 | `AuthFailure.TokenInvalid` | Token was invalid | 无 admin token |
| 500 | `InternalError` | 内部错误 | SQL 错误 / cutover_date 配置缺失 |

**权限要求**：管理端 token

**幂等性**：纯读，幂等

**性能要求**：100 万行 membership_event 下响应 < 500ms（依靠 `idx_me_granter_occurred (granter_user_id, occurred_at)` + `(occurred_at)` 索引）（AC-18）

**关联 PRD**：US-7 / US-8 / AC-15 / AC-18 / AC-21 / AC-21a / AC-21b / AC-21c / AC-25

---

### §5.7 错误码完整清单

新增 / 复用错误码全部归口在 `internal/pkg/errno/credits.go`（已有的扩展）+ `internal/pkg/errno/code.go`（通用）：

| Go 常量名 | HTTP | 业务码 | 中文 message | i18n key | 触发场景 |
|---|---|---|---|---|---|
| `ErrSelfPurchaseDisabled` | 403 | `Membership.SelfPurchaseDisabled` | 请联系管理员开通会员 | `errno.membership.self_purchase_disabled` | C 端自购 trial/monthly Pro |
| `ErrTrialAlreadyGranted` | 409 | `Trial.AlreadyGranted` | 该账户已购买过试用包 | `errno.trial.already_granted` | trial_grant 表 UNIQUE on user_id 命中 / EC-3 |
| `ErrTrialNotAllowedForActivePro` | 409 | `Trial.NotAllowedForActivePro` | 在期 Pro 用户不可再开通试用 | `errno.trial.not_allowed_for_active_pro` | grant trial 时 child 已有 active subscription / EC-4 / Q1 |
| `ErrChildNotMember` | 403 | `Membership.ChildNotMember` | 子账户当前无在期会员 | `errno.membership.child_not_member` | 父账户代购 booster 给非会员子账户 / EC-5 |
| `ErrNotActiveMember` | 403 | `Membership.NotActive` | 需要在期会员才能购买加量包 | `errno.membership.not_active` | C 端自购 booster 时无 trial 也无 active sub / EC-11 / AC-13c |
| `ErrBoosterQuantityExceedsLimit` | 400 | `Booster.QuantityExceedsLimit` | 单次最多购买 10000 份 | `errno.booster.quantity_exceeds_limit` | booster 订单 quantity > 10000 / Q2 / AC-13 |
| `ErrInsufficientCredits` | 402 | `Credits.Insufficient` | 积分不足 | `errno.credits.insufficient` | 三类积分合计不足；含会员到期 booster 冻结后无可扣资源 / AC-7 |
| `ErrSubscriptionNotFound` | 404 | `Subscription.NotFound` | 未找到订阅记录 | `errno.subscription.not_found` | 兜底：操作 subscription 但无记录 |
| `ErrInvalidProductType` | 400 | `InvalidParameter` | 不支持的产品类型 | `errno.invalid_product_type` | grant/order 传非法 product_type |
| `ErrInvalidMonths` | 400 | `InvalidParameter` | 月数必须在 1-12 之间 | `errno.invalid_months` | grant Pro 时 months ∉ [1,12] |
| `ErrParentChildRelation` | 403 | `AuthFailure.Forbidden` | 该子账户不属于当前账户 | `errno.parent_child_relation` | parent-child 关系不成立 |
| `ErrSubscriptionExpired` | 410 | `Subscription.Expired` | 订阅已过期 | `errno.subscription.expired` | 扣减时 `subscription.expires_at <= now`（事务内时间驱动判定，§3.4 / §3.5） |
| `ErrIdempotencyKeyConflict` | 409 | `Idempotency.KeyConflict` | 幂等键冲突（请求体不一致） | `errno.idempotency.key_conflict` | 同 `Idempotency-Key` 但请求体（user_id / product_type / months / quantity 等）不一致；详见 §3.2 / §3.3 / §4.5 |

**实现要求**：

- 全部常量定义在 `internal/pkg/errno/credits.go`，沿用现有 `&Errno{HTTP, Code, Message}` 结构
- i18n key 集中在前端 `numind-web-v3/src/i18n/zh-CN/errors.ts` + `numind-admin-web/src/i18n/zh-CN/errors.ts`
- biz 层定义 sentinel error（`ErrTrialAlreadyGranted = errors.New("...")`），controller 层统一通过 `mapGrantError(err)`/`mapOrderError(err)` 映射到 `*errno.Errno`（参考现有 `parent_grant/grant.go:18-41`）

---

### §5.8 路由注册位置

**用户端 `internal/numind/router.go`**：

| 端点 | 注册位置 | 变更类型 | 行数变更 |
|---|---|---|---|
| `POST /v1/users/children/:child_id/grant-membership` | `B2B2C 会员赋予` 块（`childGroup` 内 `grant-membership` 路由处） | **修改 controller**：复用 `parent_grant.GrantMembership`，接受 `Idempotency-Key` header → biz 写入 `membership_event` | controller 内部改动 ~10 行；router 不变 |
| `POST /v1/orders` | `订单管理` 块（`orderGroup`） | **修改 controller + biz**：新增 `quantity` 字段绑定与校验；biz 层 booster 路径写入 `booster_grant` + `membership_event` | controller +5 行，router 不变 |
| `GET /v1/credits/balance` | `积分查询` 块（`creditsGroup` B2C 块） | **修改 controller + biz**：返回结构按 §5.3 重写 | controller ~30 行重写，router 不变 |
| `GET /v1/users/children/:child_id/balance` | `B2B2C 会员赋予` 块附近（`childGroup`） | **新增**：`authGroup.GET("/users/children/:child_id/balance", parentGrantCtrl.GetChildBalance)` | router +1 行；新增 controller 方法 ~40 行 |

**管理端 `internal/numind/admin_router.go`**：

| 端点 | 注册位置 | 变更类型 | 行数变更 |
|---|---|---|---|
| `GET /v1/admin/users/:user_id/balance` | `积分管理` 块（`adminCreditCtrl` 注册附近） | **新增**：`adminGroup.GET("/users/:user_id/balance", adminCreditCtrl.GetUserBalance)` | router +1 行；admin_credit controller +1 方法 ~30 行 |
| `GET /v1/admin/b2b-billing-report` | `B2B 月度结算报表` 块（`adminB2BCtrl` 注册附近） | **修改 biz**：`b2b_billing.go` 改读 `membership_event`，加入 cutover 双口径分支；router 不变 | biz 重写 ~200 行（含 §7 SQL）；router 不变 |

**总计 router 变更**：用户端 +1 行，管理端 +1 行（极小）。biz/controller 改动量集中。

---

### §5.9 Controller 层职责（强制规约）

按 `.claude/rules/api-design.md §6` 规约，本节所有 controller 仅负责：

1. **参数绑定**：`c.ShouldBindJSON(&req)` / `c.ShouldBindQuery(&req)` / `c.Param("id")` + `strconv.ParseUint`
2. **Auth 上下文提取**：`middleware.GetCurrentUser(c)` 或 `c.Get("current_user")`
3. **Idempotency-Key 提取**：`c.GetHeader("Idempotency-Key")`，**校验非空 + 长度 ≤ 64**，传给 biz
4. **调用 biz**：`creditBiz.GrantMembership(c, req)`，传 `*gin.Context` 作 `context.Context`
5. **格式化响应**：`core.WriteResponse(c, err, data)` + `mapXxxError(err)` 映射 sentinel 到 errno

**禁止行为**：

- ❌ Controller 直接读 `model.User` / `model.Subscription` / 任何 GORM 调用
- ❌ Controller 内做 parent-child 校验（biz 层做）
- ❌ Controller 内做 trial 已购校验（biz 层做）
- ❌ Controller 内组装 `membership_event` 行（biz 层做）

**模式参考**：见 `internal/numind/controller/v1/parent_grant/grant.go`（已是合规范本，本次仅扩展支持 `Idempotency-Key` header 提取）。

---

### §5.10 现有代码冲突点

实施前需要修改/废弃的现有代码点（不超过 5 条）：

1. **`internal/numind/biz/b2b_billing/b2b_billing.go`**：当前实现扫 `credit_package` 表，新口径需切换到 `membership_event`。**冲突点**：现有 `amountForPackage` 函数硬编码 trial=990 / subscription=9900，新口径 `membership_event.amount_cents` 已是真实金额，无需再算。**处理**：保留 legacy 分支函数（`getLegacyReport`）用于切换日前历史月（§7.5），新增 `getNewReport`（§7.6）+ `getCutoverSplitReport`（§7.3），`GetBillingReport` 主函数按 cutover_date 分发。
2. **`internal/numind/biz/credit/credit.go`**：现有 `GetBalance` 返回老结构（`balance/sub_*/booster_*`），controller 同时返回新老字段做向后兼容。**冲突点**：本次重构后老字段语义改变（`sub_total` 没意义了，sub 不再存储 credits），需要新建 `GetBalanceV2` biz 方法返回 §5.3 结构，**老字段平滑下线**——前端切到 V2 后下个 release 删除老字段。
3. **`internal/numind/biz/credit/grant_membership.go`**：现有逻辑写 `credit_package` 表 + `tier_change_log`，新逻辑写 `subscription` / `trial_grant` / `cycle`（lazy）/ `membership_event`。**冲突点**：grant_membership 函数签名保留，但内部完全重写；保留 `tier_change_log` 写入用于审计兼容。
4. **`internal/numind/biz/payment/payment.go` `fulfillOrder`**：当前 booster 回调写 `credit_package` 行，新逻辑写 `booster_grant` + `membership_event`。**冲突点**：`out_trade_no` 唯一键复用，无破坏。
5. **`internal/numind/biz/credit/cron_billing.go`**：完全删除（时间驱动后无 cron）。**冲突点**：`cmd/numind/main.go` 启动时注册了 cron job，需同步移除注册行（约 3-5 行）。

---


---

## §6 迁移策略

### 概述

本次迁移把"双制并存"过渡架构（`credit_package` 单表 + `user_tier`/`tier_expires` 字段）一次性切换到 5 张新表（`subscription` / `trial_grant` / `credit_cycle` / `user_booster_balance` / `membership_event`）。决策 Q5 锁定为**一次性全量切换**，不分阶段灰度。本节定义段合并算法、4 件套迁移脚本框架、对账 SQL invariant、maintenance window runbook。

迁移工作的核心约束：

1. **非负净增（恩泽性迁移）**：迁前每个用户 `credit_package` 总积分（pre_total）≤ 迁后 5 张新表合计积分（post_total）。具体策略：所有迁入在期会员**赠送本月剩余 2000 cycle 配额**（不论旧 cycle 是否已扣分），因此 post 通常 >= pre。`post < pre` 视为数据丢失，立即 rollback。**这是与传统"严格守恒"迁移的关键区别**——产品决策选择避免老用户感知到迁移期被"扣回积分"，trade-off 是单用户最多多得 2000 积分（一次性赠送）。详见 §6.2.3 Invariant 1。
2. **零权益漂移**：迁前 `subscription package.expires_at` = 迁后 `subscription.expires_at`（精确到秒）
3. **零 grant 历史丢失**：迁前每条 `credit_package`（活跃 + 历史）→ 迁后 `membership_event` 至少有 1 条对应事件
4. **trial 单次性保留**：迁前用户若有任何 trial 包（任何状态），迁后 `trial_grant` 必有对应行，UNIQUE 约束生效
5. **一致性原子性**：所有迁移 SQL 必须在单一事务内（`START TRANSACTION` ... `COMMIT`），任何 step 失败即整体回滚

历史参考：`scripts/2026-04-24-legacy-tier-migration/` 已成功迁移 24 个 `legacy_tier` 用户，本次迁移复用其"4 件套 + backup 表 + 单事务"框架，但业务复杂度（5 张新表 + 段合并 + 多积分类型）显著高于历史迁移。

---

### §6.1 段合并算法

#### 6.1.1 问题陈述

旧 `credit_package` 表里同一用户可能有多行 `type='subscription'` 且 `status='active'` 的记录（按月切分，每次续费产生新行）。例如某用户 2025-10-01 开通 1 个月 Pro，2025-11-01 续费 1 个月，2026-01-15 又续费 2 个月。旧表会有 3 行 subscription package：

| activated_at | expires_at | total | remain |
|---|---|---|---|
| 2025-10-01 | 2025-11-01 | 2000 | 0 |
| 2025-11-01 | 2025-12-01 | 2000 | 0 |
| 2026-01-15 | 2026-03-15 | 4000 | 1234 |

注意 2025-12-01 → 2026-01-15 之间有 1.5 个月空档（用户停付），这是**两个独立的订阅段**：段 1 是 [10-01, 12-01)（合并的 2 个月），段 2 是 [01-15, 03-15)。

新表 `subscription` 设计为**每用户至多 1 行 active**（`UNIQUE on user_id WHERE status='active'`），因此必须做"连续段合并"：

- 相邻行如果 `prev.expires_at >= next.activated_at`（前一段未过期或刚到期立即续）→ 合并为一段
- 相邻行如果 `prev.expires_at < next.activated_at`（中间有空档）→ 视为独立段，前段视为历史不活跃

迁移规则：**只取最后一段未过期的作为新 `subscription` 行**。所有历史段（含未过期最后段之前的合并段）反向重建为 `membership_event` 行。

#### 6.1.2 段合并 SQL（CTE + ROW_NUMBER + LAG + 分段标记）

```sql
-- 段合并核心 CTE
WITH ordered_subs AS (
  SELECT
    user_id,
    id AS pkg_id,
    activated_at,
    expires_at,
    total_credits,
    remain_credits,
    grant_source,
    granter_user_id,
    order_id,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY activated_at) AS rn,
    LAG(expires_at) OVER (PARTITION BY user_id ORDER BY activated_at) AS prev_expires_at
  FROM credit_package
  WHERE type = 'subscription'
    AND status IN ('active', 'expired', 'consumed')  -- 不含 cancelled/refunded
),
-- 标记段边界：当前行的 activated_at > prev_expires_at（有空档）即开启新段
segment_marked AS (
  SELECT
    *,
    CASE
      WHEN prev_expires_at IS NULL THEN 1  -- 首行必为新段
      WHEN activated_at > prev_expires_at THEN 1  -- 有空档 → 新段
      ELSE 0
    END AS is_new_segment
  FROM ordered_subs
),
-- 累加 segment_id：同段所有行共享同一 segment_id
segmented AS (
  SELECT
    *,
    SUM(is_new_segment) OVER (PARTITION BY user_id ORDER BY rn) AS segment_id
  FROM segment_marked
),
-- 每段聚合：取段内首行的 activated_at + 段内尾行的 expires_at
segment_summary AS (
  SELECT
    user_id,
    segment_id,
    MIN(activated_at) AS segment_start,
    MAX(expires_at) AS segment_end,
    SUM(total_credits) AS total_segment_credits,
    -- remain 只取段内最后一行（其余段早已用完或过期）
    -- 但本系统中 remain 在新表已无意义（新表用 cycle 表算余额）
    COUNT(*) AS package_count_in_segment
  FROM segmented
  GROUP BY user_id, segment_id
),
-- 找每用户最后一段
last_segment AS (
  SELECT
    user_id,
    segment_id,
    segment_start,
    segment_end,
    total_segment_credits,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY segment_id DESC) AS seg_rn
  FROM segment_summary
)
-- 仅取每用户最后一段，且段尾未过期
SELECT
  user_id,
  segment_start AS first_started_at,
  segment_start AS current_started_at,  -- 简化：最后一段视为当前 cycle anchor
  segment_end AS expires_at
FROM last_segment
WHERE seg_rn = 1
  AND segment_end > NOW();  -- 仅未过期的最后段进 subscription 表
```

**注意点**：

- `LAG` 在 MySQL 8.0+ 可用；本项目 prod 用 MySQL 8.0，可直接使用
- `prev_expires_at` 比较使用 `>` 而非 `>=`，因为 `expires_at` 与下一个 `activated_at` 完全相等表示无缝续费（合并），有 1 秒间隔以上视为空档（独立段）
- 段内 `total_segment_credits` 在新表无直接对应字段（新表用 cycle 算月配额），但保留用于 `membership_event` 反向重建时的金额还原
- 上面的 SQL 仅返回**应进入 `subscription` 表的最后一段**；前面所有段都进入 `membership_event` 历史表

#### 6.1.3 Go 校验脚本伪代码

dry-run 阶段必须用 Go 程序复算段合并结果，避免纯 SQL 难以对账的问题。脚本目标：扫描每个用户所有 `subscription` package，按算法计算预期 `subscription` 行（或无行），与 SQL 输出对照，差异立即报警。

```go
// scripts/2026-05-XX-membership-redesign-migration/dry_run/verify_segment_merge.go
package main

import (
    "context"
    "fmt"
    "sort"
    "time"
    // ... GORM imports
)

type SubPackage struct {
    UserID       uint
    PkgID        uint64
    ActivatedAt  time.Time
    ExpiresAt    time.Time
    TotalCredits int
    GrantSource  string
}

type SegmentResult struct {
    UserID            uint
    SegmentStart      time.Time
    SegmentEnd        time.Time
    PackageCount      int
    IsLastSegment     bool
    ShouldEnterNewTbl bool
}

func MergeSegments(pkgs []SubPackage) []SegmentResult {
    if len(pkgs) == 0 {
        return nil
    }
    // 按 ActivatedAt 升序排序
    sort.Slice(pkgs, func(i, j int) bool {
        return pkgs[i].ActivatedAt.Before(pkgs[j].ActivatedAt)
    })

    var segments []SegmentResult
    cur := SegmentResult{
        UserID:       pkgs[0].UserID,
        SegmentStart: pkgs[0].ActivatedAt,
        SegmentEnd:   pkgs[0].ExpiresAt,
        PackageCount: 1,
    }
    for i := 1; i < len(pkgs); i++ {
        // 关键判定：前一段尾 >= 当前活跃头 → 合并
        if !pkgs[i].ActivatedAt.After(cur.SegmentEnd) {
            // 合并：扩展段尾
            if pkgs[i].ExpiresAt.After(cur.SegmentEnd) {
                cur.SegmentEnd = pkgs[i].ExpiresAt
            }
            cur.PackageCount++
        } else {
            // 空档 → 关闭当前段，开启新段
            segments = append(segments, cur)
            cur = SegmentResult{
                UserID:       pkgs[i].UserID,
                SegmentStart: pkgs[i].ActivatedAt,
                SegmentEnd:   pkgs[i].ExpiresAt,
                PackageCount: 1,
            }
        }
    }
    segments = append(segments, cur)

    // 标记最后一段且未过期 → 进入新 subscription 表
    now := time.Now()
    for i := range segments {
        if i == len(segments)-1 && segments[i].SegmentEnd.After(now) {
            segments[i].IsLastSegment = true
            segments[i].ShouldEnterNewTbl = true
        }
    }
    return segments
}

// 对账 main loop
func RunDryRun(ctx context.Context, db *gorm.DB) (*Report, error) {
    var users []uint
    db.Raw(`
        SELECT DISTINCT user_id FROM credit_package
        WHERE type='subscription' AND status IN ('active','expired','consumed')
    `).Scan(&users)

    report := &Report{}
    for _, uid := range users {
        var pkgs []SubPackage
        db.Raw(`
            SELECT user_id, id AS pkg_id, activated_at, expires_at,
                   total_credits, grant_source
            FROM credit_package
            WHERE user_id = ? AND type='subscription'
              AND status IN ('active','expired','consumed')
        `, uid).Scan(&pkgs)

        // Go 算法计算
        goSegs := MergeSegments(pkgs)

        // SQL 算法结果（从 dry-run 临时表读）
        var sqlResult struct {
            UserID            uint
            FirstStartedAt    time.Time
            ExpiresAt         time.Time
        }
        db.Raw(`SELECT * FROM tmp_segment_merge_result WHERE user_id = ?`, uid).
            Scan(&sqlResult)

        // 对照
        var goLast *SegmentResult
        for i := range goSegs {
            if goSegs[i].ShouldEnterNewTbl {
                goLast = &goSegs[i]
            }
        }

        if goLast == nil && sqlResult.UserID == 0 {
            // 一致：都判定无 active subscription
            continue
        }
        if goLast == nil || sqlResult.UserID == 0 {
            report.AddDiff(uid, "Go vs SQL 判定 active 不一致")
            continue
        }
        if !goLast.SegmentStart.Equal(sqlResult.FirstStartedAt) ||
           !goLast.SegmentEnd.Equal(sqlResult.ExpiresAt) {
            report.AddDiff(uid, fmt.Sprintf(
                "段边界不一致 Go=[%v,%v] SQL=[%v,%v]",
                goLast.SegmentStart, goLast.SegmentEnd,
                sqlResult.FirstStartedAt, sqlResult.ExpiresAt))
        }
    }
    return report, nil
}
```

**输出报表样例**：

```
=== Segment Merge Dry-Run Report ===
Total users with subscription packages: 1247
Users with 1 segment (clean):           1180
Users with 2+ segments (gap exists):      67
Users entering new subscription table:   892  (last segment unexpired)
Users staying free (last segment expired): 355

DIFFS DETECTED: 0    ← 必须为 0 才能进入 apply 阶段
```

dry-run 输出的 diff 数必须为 0，否则立即停止迁移、人工排查算法或 SQL bug。

---

### §6.2 4 件套脚本规范

参考 `scripts/2026-04-24-legacy-tier-migration/` 结构，本次迁移目录为 `scripts/2026-05-XX-membership-redesign-migration/`，包含 4 个脚本：

```
scripts/2026-05-XX-membership-redesign-migration/
├── 01-dry-run.sql        # 只读，输出迁前迁后预期对照
├── 02-apply.sql          # 单事务，所有写入操作
├── 03-verify.sql         # 迁后立即对账，invariant 检查
├── 04-rollback.sql       # 应急回滚，从 backup 恢复
└── dry_run/              # Go 校验脚本（段合并复算）
    └── verify_segment_merge.go
```

#### 6.2.1 01-dry-run.sql（只读）

**目标**：在不修改任何数据的前提下，输出每用户"迁前 `credit_package` 总积分" + "预计迁后 5 张新表预期值" + "差额"。差额必须为 0，否则迁移被禁止。

```sql
-- 01-dry-run.sql
-- 只读，输出预期迁移结果
-- Run: docker exec -i numind-mysql-prod mysql -uroot -p$PASS numind-prod < 01-dry-run.sql

-- ----------------------------------------------------------------------
-- Section A: 创建临时表存放预期结果（DROP IF EXISTS 防重跑残留）
-- ----------------------------------------------------------------------
DROP TEMPORARY TABLE IF EXISTS tmp_segment_merge_result;
CREATE TEMPORARY TABLE tmp_segment_merge_result (
  user_id            BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  first_started_at   DATETIME(3) NOT NULL,
  current_started_at DATETIME(3) NOT NULL,
  expires_at         DATETIME(3) NOT NULL,
  total_segment_credits INT
) ENGINE=Memory;

-- 插入段合并结果（§6.1.2 完整 CTE 此处展开）
INSERT INTO tmp_segment_merge_result
WITH ordered_subs AS ( /* ... §6.1.2 同 ... */ ),
     segment_marked AS ( /* ... */ ),
     segmented AS ( /* ... */ ),
     segment_summary AS ( /* ... */ ),
     last_segment AS ( /* ... */ )
SELECT user_id, segment_start, segment_start, segment_end, total_segment_credits
FROM last_segment
WHERE seg_rn = 1 AND segment_end > NOW();

-- ----------------------------------------------------------------------
-- Section B: 计算预期 trial_grant 行
-- ----------------------------------------------------------------------
DROP TEMPORARY TABLE IF EXISTS tmp_trial_grant_expected;
CREATE TEMPORARY TABLE tmp_trial_grant_expected (
  user_id           BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  granted_at        DATETIME(3) NOT NULL,
  expires_at        DATETIME(3) NOT NULL,
  credits_total     INT NOT NULL DEFAULT 200,
  credits_remaining INT NOT NULL,
  granter_user_id   BIGINT UNSIGNED
) ENGINE=Memory;

INSERT INTO tmp_trial_grant_expected
SELECT
  user_id,
  MIN(activated_at) AS granted_at,
  MAX(expires_at) AS expires_at,
  200 AS credits_total,
  -- 取该用户最新一行 trial 包的 remain
  (SELECT remain_credits FROM credit_package
   WHERE user_id = cp.user_id AND type='trial'
   ORDER BY activated_at DESC LIMIT 1) AS credits_remaining,
  MAX(granter_user_id) AS granter_user_id
FROM credit_package cp
WHERE type='trial'
GROUP BY user_id;

-- ----------------------------------------------------------------------
-- Section C: 计算预期 user_booster_balance 行（聚合所有 booster 到单 balance）
-- ----------------------------------------------------------------------
DROP TEMPORARY TABLE IF EXISTS tmp_booster_balance_expected;
CREATE TEMPORARY TABLE tmp_booster_balance_expected (
  user_id           BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  credits_remaining INT NOT NULL,
  total_purchased   INT NOT NULL,
  last_purchase_at  DATETIME(3)
) ENGINE=Memory;

INSERT INTO tmp_booster_balance_expected
SELECT
  user_id,
  -- 仅 active 状态包的 remain（已过期 booster 视为作废，不带入新表）
  -- 决策：旧 booster 90 天会过期；过期则 remain 不进新表
  -- 但 total_purchased 是历史累计（含过期）
  COALESCE(SUM(CASE WHEN status='active' AND expires_at > NOW()
                    THEN remain_credits ELSE 0 END), 0) AS credits_remaining,
  COALESCE(SUM(total_credits), 0) AS total_purchased,
  MAX(activated_at) AS last_purchase_at
FROM credit_package
WHERE type='booster'
GROUP BY user_id
HAVING credits_remaining > 0 OR total_purchased > 0;

-- ----------------------------------------------------------------------
-- Section D: 对账 — 迁前 vs 迁后总余额差额
-- ----------------------------------------------------------------------
SELECT
  u.id AS user_id,
  u.username,
  -- 迁前：credit_package 所有 active 包 remain 总和
  COALESCE((
    SELECT SUM(remain_credits)
    FROM credit_package
    WHERE user_id = u.id
      AND status='active'
      AND expires_at > NOW()
  ), 0) AS pre_total_credits,
  -- 迁后：trial_remaining + 当前 cycle (= 2000 if 在期 sub) + booster_remaining
  COALESCE((SELECT credits_remaining FROM tmp_trial_grant_expected
            WHERE user_id = u.id AND expires_at > NOW()), 0)
  + CASE WHEN EXISTS (SELECT 1 FROM tmp_segment_merge_result
                       WHERE user_id = u.id) THEN 2000 ELSE 0 END
  + COALESCE((SELECT credits_remaining FROM tmp_booster_balance_expected
              WHERE user_id = u.id), 0) AS post_total_credits,
  -- 差额（必须 0）
  (post_total_credits - pre_total_credits) AS delta
FROM user u
WHERE EXISTS (SELECT 1 FROM credit_package WHERE user_id = u.id)
HAVING delta != 0
ORDER BY ABS(delta) DESC;

-- ----------------------------------------------------------------------
-- Section E: 输出汇总指标（管控阈值）
-- ----------------------------------------------------------------------
SELECT 'Total users with packages'      AS metric,
       COUNT(DISTINCT user_id) AS value FROM credit_package
UNION ALL
SELECT 'Expected subscription rows',
       COUNT(*) FROM tmp_segment_merge_result
UNION ALL
SELECT 'Expected trial_grant rows',
       COUNT(*) FROM tmp_trial_grant_expected
UNION ALL
SELECT 'Expected booster_balance rows',
       COUNT(*) FROM tmp_booster_balance_expected
UNION ALL
SELECT 'Users with delta != 0 (BLOCKER)',
       COUNT(*) FROM (
         /* Section D 子查询 */
       ) blockers;

SELECT 'DRY-RUN COMPLETE — review delta report before running 02-apply.sql' AS status;
```

**通过条件**：

- Section D 输出 0 行（没有差额非 0 的用户）
- Section E 的 `Users with delta != 0 (BLOCKER)` = 0
- Go 校验脚本输出 `DIFFS DETECTED: 0`

任一不满足则禁止进入 apply 阶段，必须人工介入排查。

#### 6.2.2 02-apply.sql（单事务写入）

**目标**：在 `START TRANSACTION` ... `COMMIT` 包裹下完成所有写入：backup 旧数据、INSERT 新表、UPDATE user 字段、反向重建 membership_event。

```sql
-- 02-apply.sql
-- 文件结构：
--   Step 0: backup CREATE TABLE + INSERT（事务外，DDL implicit commit 安全）
--   Step 1~6: 单事务 INSERT/UPDATE
-- MySQL 默认在 CLI 出错时 abort，事务自动 rollback

-- ----------------------------------------------------------------------
-- Step 0: 创建 backup 表 + 灌数据（事务之外！DDL 在 InnoDB 触发 implicit commit）
-- ----------------------------------------------------------------------
DROP TABLE IF EXISTS membership_redesign_backup_credit_package_20260520;
CREATE TABLE membership_redesign_backup_credit_package_20260520 LIKE credit_package;
INSERT INTO membership_redesign_backup_credit_package_20260520 SELECT * FROM credit_package;

-- 校验 backup 行数 = 源表行数（runbook Step 3 必须验证）
SELECT
  (SELECT COUNT(*) FROM credit_package)                                        AS source_count,
  (SELECT COUNT(*) FROM membership_redesign_backup_credit_package_20260520)    AS backup_count;
-- 期望两者相等；不等则停止迁移

-- ----------------------------------------------------------------------
-- Step 1: 开启事务 + 锚定 apply_start_ts（rollback Step 6.5 apply_log 用）
-- ----------------------------------------------------------------------
START TRANSACTION;
SET @apply_start_ts = NOW(3);

-- ----------------------------------------------------------------------
-- Step 2: 段合并 → 写入 subscription
-- ----------------------------------------------------------------------
INSERT INTO subscription
  (user_id, status, first_started_at, current_started_at, expires_at,
   total_months_purchased, granter_user_id, created_at, updated_at)
WITH ordered_subs AS ( /* §6.1.2 CTE */ ),
     segment_marked AS ( /* ... */ ),
     segmented AS ( /* ... */ ),
     segment_summary AS ( /* ... */ ),
     last_segment AS ( /* ... */ )
SELECT
  user_id,
  'active',
  segment_start,
  segment_start,                      -- 简化为 anchor = segment_start
  segment_end,
  -- total_months_purchased：基于段内天数向上取整到月，防 TIMESTAMPDIFF(MONTH) 整月截断
  -- 例如 [1/15, 4/14) = 89 天 → CEIL(89/30) = 3 个月（正确）
  -- 用 TIMESTAMPDIFF(MONTH) 会得到 2（截断）
  GREATEST(1, CEIL(TIMESTAMPDIFF(DAY, segment_start, segment_end) / 30.0)),
  -- 取段内最后一行的 granter_user_id
  (SELECT granter_user_id FROM credit_package
   WHERE user_id = ls.user_id AND type='subscription'
   ORDER BY activated_at DESC LIMIT 1),
  NOW(3), NOW(3)
FROM last_segment ls
WHERE seg_rn = 1 AND segment_end > NOW();

-- ----------------------------------------------------------------------
-- Step 3: trial 拆分 → 写入 trial_grant
--         一个 user 只能有 1 行 trial_grant（UNIQUE on user_id）
--         旧表中若有多行（理论不该发生）取最早一行
-- ----------------------------------------------------------------------
INSERT INTO trial_grant
  (user_id, granted_at, expires_at, credits_total, credits_remaining,
   granter_user_id, source_package_id, created_at)
SELECT
  cp.user_id,
  cp.activated_at,
  cp.expires_at,
  cp.total_credits,
  cp.remain_credits,
  cp.granter_user_id,
  cp.id,
  NOW(3)
FROM credit_package cp
INNER JOIN (
  -- 每个 user 只取 type='trial' 的最早一行
  SELECT user_id, MIN(activated_at) AS first_trial_at
  FROM credit_package
  WHERE type='trial'
  GROUP BY user_id
) first_trial ON first_trial.user_id = cp.user_id
              AND first_trial.first_trial_at = cp.activated_at
WHERE cp.type='trial';

-- ----------------------------------------------------------------------
-- Step 4: booster 聚合 → 写入 user_booster_balance
--         过期 booster 余额作废（remain 不带入新表，但保留事件历史）
-- ----------------------------------------------------------------------
INSERT INTO user_booster_balance
  (user_id, credits_remaining, total_purchased, last_purchase_at,
   created_at, updated_at)
SELECT
  user_id,
  COALESCE(SUM(CASE WHEN status='active' AND expires_at > NOW()
                    THEN remain_credits ELSE 0 END), 0),
  COALESCE(SUM(total_credits), 0),
  MAX(activated_at),
  NOW(3), NOW(3)
FROM credit_package
WHERE type='booster'
GROUP BY user_id
HAVING credits_remaining > 0 OR total_purchased > 0;

-- ----------------------------------------------------------------------
-- Step 5: 反向重建 membership_event
--         每条 credit_package 历史 → 1 条 membership_event
--         事件类型映射：
--           type=trial         → event_type='trial_granted'
--           type=subscription  → event_type='sub_granted' (首次) / 'sub_renewed' (续费)
--           type=booster       → event_type='booster_purchased'
-- ----------------------------------------------------------------------
-- 注意：months 和 quantity 是两个独立列（schema 见 §2.5）。事件类型决定哪一列有值：
--   sub_granted/sub_renewed → months 非 NULL，quantity NULL
--   booster_purchased       → quantity 非 NULL，months NULL
--   trial_granted           → 两者都 NULL
INSERT INTO membership_event
  (user_id, granter_user_id, event_type, product_type, months, quantity,
   amount_cents, occurred_at, idempotency_key, source_package_id, created_at)
WITH numbered_subs AS (
  SELECT
    cp.*,
    -- 嵌入 §6.1.2 的 segment_id 计算（与 Step 2 段合并语义保持一致）
    -- 同段第一行 → sub_granted；同段后续行 → sub_renewed
    ROW_NUMBER() OVER (PARTITION BY cp.user_id ORDER BY cp.activated_at) AS rn_user,
    LAG(cp.expires_at) OVER (PARTITION BY cp.user_id ORDER BY cp.activated_at) AS prev_expires_at
  FROM credit_package cp
  WHERE cp.type='subscription'
),
sub_with_segment AS (
  SELECT
    *,
    SUM(CASE
      WHEN prev_expires_at IS NULL THEN 1
      WHEN activated_at > prev_expires_at THEN 1
      ELSE 0
    END) OVER (PARTITION BY user_id ORDER BY rn_user) AS segment_id,
    ROW_NUMBER() OVER (
      PARTITION BY user_id, SUM(CASE
        WHEN prev_expires_at IS NULL THEN 1
        WHEN activated_at > prev_expires_at THEN 1
        ELSE 0
      END) OVER (PARTITION BY user_id ORDER BY rn_user)
      ORDER BY activated_at
    ) AS rn_in_segment
  FROM numbered_subs
)
SELECT
  cp.user_id,
  cp.granter_user_id,
  CASE
    WHEN cp.type='trial' THEN 'trial_granted'
    WHEN cp.type='booster' THEN 'booster_purchased'
    WHEN cp.type='subscription' AND swseg.rn_in_segment = 1
      THEN 'sub_granted'
    WHEN cp.type='subscription' THEN 'sub_renewed'
  END AS event_type,
  cp.type AS product_type,
  -- months：仅 subscription 事件填值，其他事件为 NULL
  CASE
    WHEN cp.type='subscription' THEN
      GREATEST(1, CEIL(TIMESTAMPDIFF(DAY, cp.activated_at, cp.expires_at) / 30.0))
    ELSE NULL
  END AS months,
  -- quantity：仅 booster 事件填值，其他事件为 NULL
  -- 用 CEIL 兜底防整除截断（dry-run 已校验 total_credits % 600 = 0）
  CASE
    WHEN cp.type='booster' THEN CEIL(cp.total_credits / 600.0)
    ELSE NULL
  END AS quantity,
  -- 金额：从订单表回查；订单不存在则置 0（grant 类）
  COALESCE((
    SELECT amount_cents FROM `order` WHERE id = cp.order_id
  ), 0) AS amount_cents,
  cp.activated_at AS occurred_at,
  -- 历史迁移 idempotency_key 使用固定前缀 + pkg_id 保证唯一
  CONCAT('migration-20260520-pkg-', cp.id) AS idempotency_key,
  cp.id AS source_package_id,
  NOW(3)
FROM credit_package cp
LEFT JOIN sub_with_segment swseg
  ON swseg.id = cp.id AND cp.type = 'subscription'
ORDER BY cp.user_id, cp.activated_at;

-- ----------------------------------------------------------------------
-- Step 6: 不预创建 credit_cycle —— 决策保持"懒创建"
--         切换后用户首次扣分时由 biz 层创建当前月 cycle
--         迁移期不主动创建，避免迁移期产生半成品 cycle
-- ----------------------------------------------------------------------
-- (intentionally empty)

-- ----------------------------------------------------------------------
-- Step 6.5: 写入 apply_log（rollback 反向定位本次迁移写入行的依据）
--         避免 04-rollback 用 created_at 范围判定（无法区分迁移行 vs 用户活动行）
-- ----------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS membership_redesign_apply_log_20260520 (
  table_name VARCHAR(64) NOT NULL,
  row_id     BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (table_name, row_id)
) ENGINE=InnoDB;

INSERT INTO membership_redesign_apply_log_20260520 (table_name, row_id)
SELECT 'subscription', id FROM subscription
WHERE created_at >= @apply_start_ts
UNION ALL
SELECT 'trial_grant', id FROM trial_grant
WHERE source_package_id IS NOT NULL
UNION ALL
SELECT 'user_booster_balance', id FROM user_booster_balance
WHERE created_at >= @apply_start_ts
UNION ALL
SELECT 'membership_event', id FROM membership_event
WHERE idempotency_key LIKE 'migration-20260520-%';

-- ----------------------------------------------------------------------
-- Step 7: 清理 user 表 legacy 字段（可选，决策保留只读 7 天）
--         本次不清，留待 T+7 天后单独跑 cleanup script
-- ----------------------------------------------------------------------
-- UPDATE user SET billing_mode = 'credits' WHERE billing_mode = 'legacy_tier';
-- (上面这步在 4 月 24 日老迁移已完成，本次迁移所有用户 billing_mode 已是 credits)

COMMIT;

SELECT 'APPLY COMPLETE — run 03-verify.sql IMMEDIATELY' AS status;
```

**关键设计**：

- backup 表 `CREATE TABLE` + `INSERT INTO ... SELECT` 必须放在 `START TRANSACTION` **之前**（InnoDB 在事务内执行 DDL 会触发 implicit commit，破坏事务原子性）。02-apply.sql 的真实文件结构如下（**不是注释约定，是可执行 SQL**）：

  ```sql
  -- 02-apply.sql 顶部（事务之前）
  CREATE TABLE membership_redesign_backup_credit_package_20260520 LIKE credit_package;
  INSERT INTO membership_redesign_backup_credit_package_20260520 SELECT * FROM credit_package;
  -- (其它需备份的表同样的 CREATE + INSERT 对，全部位于事务外)

  START TRANSACTION;
  -- Step 2~6 业务写入
  COMMIT;
  ```

  §6.4 maintenance window runbook Step 3 必须先确认 backup 行数 = 源表行数（独立校验），再开启事务。

- Step 2 段合并：`total_months_purchased` 用 `CEIL(TIMESTAMPDIFF(DAY, segment_start, segment_end) / 30.0)` 计算，避免 MySQL `TIMESTAMPDIFF(MONTH, ...)` 整月截断（例：[1/15, 4/14) 会被算成 2 个月，应为 3）。dry-run Section D 余额对账可间接发现该计算偏差（若续费段月数被低估，post_total 中的 cycle 数量也会偏少 → 触发 delta != 0 报警）。
- Step 5 反向重建 `membership_event`：用嵌入的 `sub_with_segment` CTE 计算每个 subscription 包在所属合并段内的位置——**段内第 1 行写 `sub_granted`，段内第 2 行起写 `sub_renewed`**。这与 §6.1.2 段合并语义一致：相邻续费同段视为续费，空档后再开通视为新 grant。
- Step 5 booster `quantity` 用 `CEIL(cp.total_credits / 600.0)` 兜底防整除截断；dry-run 阶段（01-dry-run.sql Section X）必须额外校验 `cp.type='booster' AND cp.total_credits % 600 != 0` 命中数 = 0，命中则在 dry-run 报告中高亮要求人工排查（理论上不该出现，因为业务侧 booster 包总分必定是 600 的倍数）。
- Step 5 的 `idempotency_key` 用 `migration-20260520-pkg-{id}` 格式，未来如果需要再次回灌不会冲突
- Step 6 显式不创建 cycle 行，保持懒创建语义
- 任何一步失败，整个事务回滚；MySQL CLI 默认 abort-on-error

#### 6.2.3 03-verify.sql（迁后立即对账）

**目标**：在 02-apply 提交后立即跑，对每个用户做 invariant 检查。任何 invariant 违反即触发 04-rollback。

```sql
-- 03-verify.sql
-- 必须在 02-apply.sql 提交后立即运行
-- 任何 invariant 违反 → 立即跑 04-rollback.sql

-- ----------------------------------------------------------------------
-- Invariant 1: 迁前迁后积分**非负净增**（恩泽性迁移策略）
--
-- 注意：这里**不是严格的"差额=0"守恒**。本次迁移采用恩泽性策略——
-- 所有迁入用户**赠送本月剩余 2000 配额**（不论旧 cycle 是否已扣分），
-- 因此 post_total 通常 >= pre_total。Invariant 1 仅校验
-- "post_total < pre_total"（净减少 = 数据丢失）；post_total >= pre_total 视为合规。
--
-- 产品决策（trade-off）：
--   - 选择恩泽迁移：迁前已扣分用户感知不到迁移期被"扣回去"，避免投诉
--   - 选择严格守恒：post 必须等于 pre，但旧月已扣 N 积分需在新表 cycle 减去 N
--                  → 体感为"开通会员当天可用积分被打折"，运营压力高
--   - 决策：恩泽迁移；预算成本 = 在期会员数 × 已扣均值 × 0（赠送）
--   - 上限：单用户最多多得 2000 积分（仅 1 次，不会持续）
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_1_NON_NEGATIVE_NET_DELTA' AS check_name,
       COUNT(*) AS violation_count
FROM (
  SELECT
    u.id AS user_id,
    -- 迁前：旧 active 包 remain 总和
    COALESCE((
      SELECT SUM(remain_credits) FROM membership_redesign_backup_credit_package_20260520
      WHERE user_id = u.id AND status='active' AND expires_at > NOW()
    ), 0) AS pre_total,
    -- 迁后：trial.remain + (有 sub 则赠送本月 2000 cycle) + booster.remain
    COALESCE((SELECT credits_remaining FROM trial_grant
              WHERE user_id = u.id AND expires_at > NOW()), 0)
    + CASE WHEN EXISTS (SELECT 1 FROM subscription
                         WHERE user_id = u.id AND expires_at > NOW()) THEN 2000 ELSE 0 END
    + COALESCE((SELECT credits_remaining FROM user_booster_balance
                WHERE user_id = u.id), 0) AS post_total
  FROM user u
  WHERE EXISTS (SELECT 1 FROM membership_redesign_backup_credit_package_20260520
                WHERE user_id = u.id)
) calc
WHERE post_total < pre_total;
-- 期望 violation_count = 0（即不允许任何用户在迁移后净积分变少）；非 0 → ROLLBACK
-- 允许 post_total > pre_total（本月赠送配额）

-- ----------------------------------------------------------------------
-- Invariant 2: subscription.expires_at = 旧表 active sub 段尾
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_2_SUB_EXPIRES_MATCH' AS check_name,
       COUNT(*) AS violation_count
FROM subscription s
LEFT JOIN (
  -- 旧表中 user 最大的 expires_at（活跃 sub 段尾）
  SELECT user_id, MAX(expires_at) AS old_max_expires
  FROM membership_redesign_backup_credit_package_20260520
  WHERE type='subscription' AND status='active' AND expires_at > NOW()
  GROUP BY user_id
) old ON old.user_id = s.user_id
WHERE old.user_id IS NULL                      -- 新表有但旧表无（异常）
   OR ABS(TIMESTAMPDIFF(SECOND, s.expires_at, old.old_max_expires)) > 1;
-- 容忍 1 秒舍入误差

-- ----------------------------------------------------------------------
-- Invariant 3: trial_grant.credits_remaining = 旧表 trial 包 remain
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_3_TRIAL_REMAIN_MATCH' AS check_name,
       COUNT(*) AS violation_count
FROM trial_grant tg
LEFT JOIN (
  SELECT user_id, remain_credits AS old_remain,
         ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY activated_at DESC) AS rn
  FROM membership_redesign_backup_credit_package_20260520
  WHERE type='trial'
) old ON old.user_id = tg.user_id AND old.rn = 1
WHERE old.user_id IS NULL OR tg.credits_remaining != old.old_remain;

-- ----------------------------------------------------------------------
-- Invariant 4: user_booster_balance.credits_remaining = SUM(旧 active booster.remain)
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_4_BOOSTER_REMAIN_SUM' AS check_name,
       COUNT(*) AS violation_count
FROM user_booster_balance ubb
LEFT JOIN (
  SELECT user_id, COALESCE(SUM(remain_credits), 0) AS old_sum
  FROM membership_redesign_backup_credit_package_20260520
  WHERE type='booster' AND status='active' AND expires_at > NOW()
  GROUP BY user_id
) old ON old.user_id = ubb.user_id
WHERE old.old_sum IS NULL OR ubb.credits_remaining != old.old_sum;

-- ----------------------------------------------------------------------
-- Invariant 5a (I5-migration-day): 迁移当天 COUNT(membership_event) >= COUNT(旧 grant 记录)
--   仅在迁移当天 03-verify.sql 执行；守护期不能用此判定
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_5A_EVENT_COUNT_FLOOR_MIGRATION_DAY' AS check_name,
       COUNT(*) AS violation_count
FROM (
  SELECT
    u.id AS user_id,
    (SELECT COUNT(*) FROM membership_event WHERE user_id = u.id) AS new_evt_cnt,
    (SELECT COUNT(*) FROM membership_redesign_backup_credit_package_20260520
     WHERE user_id = u.id) AS old_pkg_cnt
  FROM user u
  WHERE EXISTS (SELECT 1 FROM membership_redesign_backup_credit_package_20260520
                WHERE user_id = u.id)
) calc
WHERE new_evt_cnt < old_pkg_cnt;

-- ----------------------------------------------------------------------
-- Invariant 5b (I5-guard-period): 守护期每日 event 增量 ≥ 当日新 grant 操作数
--   守护期 daily-verify.sql 用此判定；窗口期 = 当天 [00:00, 24:00)
--   新 grant 操作数：当日 subscription / trial_grant / user_booster_balance 的 INSERT
--                   及 subscription.expires_at 的 UPDATE（续费）合计
--   每个 grant 操作必须产生 ≥1 条 membership_event
-- ----------------------------------------------------------------------
SET @window_start = DATE_SUB(NOW(), INTERVAL 1 DAY);
SET @window_end   = NOW();

SELECT 'INVARIANT_5B_EVENT_DELTA_VS_GRANT_OPS_DAILY' AS check_name,
       CASE WHEN event_delta >= grant_ops THEN 0 ELSE 1 END AS violation_count
FROM (
  SELECT
    (SELECT COUNT(*) FROM membership_event
     WHERE created_at >= @window_start AND created_at < @window_end) AS event_delta,
    (
      (SELECT COUNT(*) FROM subscription
       WHERE created_at >= @window_start AND created_at < @window_end)
      + (SELECT COUNT(*) FROM subscription
         WHERE updated_at >= @window_start AND updated_at < @window_end
           AND created_at < @window_start)  -- 续费 UPDATE（区分新建）
      + (SELECT COUNT(*) FROM trial_grant
         WHERE created_at >= @window_start AND created_at < @window_end)
      + (SELECT COUNT(*) FROM user_booster_balance
         WHERE updated_at >= @window_start AND updated_at < @window_end)
    ) AS grant_ops
) calc;
-- 期望 violation_count = 0；非 0 表示当日有 grant 操作未产生对应 event（biz 层漏写）

-- ----------------------------------------------------------------------
-- Invariant 6: trial_grant UNIQUE on user_id（schema 级别保证，但显式校验）
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_6_TRIAL_UNIQUE' AS check_name,
       COUNT(*) AS violation_count
FROM (
  SELECT user_id, COUNT(*) AS cnt
  FROM trial_grant
  GROUP BY user_id
  HAVING cnt > 1
) dup;

-- ----------------------------------------------------------------------
-- Invariant 7: subscription UNIQUE on (user_id, status='active')
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_7_ACTIVE_SUB_UNIQUE' AS check_name,
       COUNT(*) AS violation_count
FROM (
  SELECT user_id, COUNT(*) AS cnt
  FROM subscription
  WHERE status = 'active'
  GROUP BY user_id
  HAVING cnt > 1
) dup;

-- ----------------------------------------------------------------------
-- Invariant 8: 没有 orphan 行（user_id 在 user 表必须存在）
-- ----------------------------------------------------------------------
SELECT 'INVARIANT_8_NO_ORPHAN' AS check_name,
       SUM(violation_count) AS violation_count
FROM (
  SELECT COUNT(*) AS violation_count FROM subscription s
  LEFT JOIN user u ON u.id = s.user_id WHERE u.id IS NULL
  UNION ALL
  SELECT COUNT(*) FROM trial_grant tg
  LEFT JOIN user u ON u.id = tg.user_id WHERE u.id IS NULL
  UNION ALL
  SELECT COUNT(*) FROM user_booster_balance ubb
  LEFT JOIN user u ON u.id = ubb.user_id WHERE u.id IS NULL
  UNION ALL
  SELECT COUNT(*) FROM membership_event me
  LEFT JOIN user u ON u.id = me.user_id WHERE u.id IS NULL
) o;

-- ----------------------------------------------------------------------
-- 汇总报表
-- ----------------------------------------------------------------------
SELECT 'VERIFY COMPLETE — all violation_count fields MUST be 0' AS status;
```

任何 invariant 的 `violation_count > 0` → 立即跑 `04-rollback.sql`。

#### 6.2.4 04-rollback.sql（应急回滚）

**目标**：从 backup 表反向恢复 `credit_package`，删除 5 张新表中本次迁移写入的行。仅在 T+0~T+24h 内可执行；T+24h 后用户已产生新数据，rollback 等同删除付费记录，需双签审批。

```sql
-- 04-rollback.sql
-- 仅在迁移失败或 T+24h 内事故时使用
-- 警告：T+24h 后执行会丢失新数据（订单、grant、扣减）
--
-- 设计原则：rollback 必须能精确识别"本次迁移写入的行" vs "切换后用户活动写入的行"
-- 实现方式：02-apply.sql 在 backup 表里记录所有迁移写入行的主键 ID（写入完成后回灌），
-- 04-rollback 用反向 JOIN 删除，不依赖 created_at 范围判定，不再硬编码切换日字符串。
--
-- 具体落地（在 02-apply.sql Step 6 之后追加）：
--   CREATE TABLE membership_redesign_apply_log_20260520 (
--     table_name VARCHAR(64), row_id BIGINT, PRIMARY KEY(table_name, row_id)
--   );
--   INSERT INTO membership_redesign_apply_log_20260520
--     SELECT 'subscription', id FROM subscription WHERE created_at >= @apply_start_ts
--     UNION ALL SELECT 'trial_grant', id FROM trial_grant WHERE created_at >= @apply_start_ts
--     UNION ALL SELECT 'user_booster_balance', id FROM user_booster_balance
--                       WHERE created_at >= @apply_start_ts
--     UNION ALL SELECT 'membership_event', id FROM membership_event
--                       WHERE idempotency_key LIKE 'migration-20260520-%';
--   （@apply_start_ts 在 02-apply.sql Step 1 START TRANSACTION 后立即 SET 为 NOW(3)）
--
-- rollback 通过 apply_log 反向定位被删除的行，cutover_date 不再以字符串硬编码出现。

START TRANSACTION;

-- ----------------------------------------------------------------------
-- Step 1: 删除新表中本次迁移写入的行（基于 apply_log 反向 JOIN）
--         判定标准：(table_name, id) 在 apply_log 中即为迁移写入行
-- ----------------------------------------------------------------------
DELETE me FROM membership_event me
  INNER JOIN membership_redesign_apply_log_20260520 al
    ON al.table_name = 'membership_event' AND al.row_id = me.id;
-- 兜底（若 apply_log 缺失）：按 idempotency_key 前缀清理
DELETE FROM membership_event
WHERE idempotency_key LIKE 'migration-20260520-%';

DELETE s FROM subscription s
  INNER JOIN membership_redesign_apply_log_20260520 al
    ON al.table_name = 'subscription' AND al.row_id = s.id;

DELETE tg FROM trial_grant tg
  INNER JOIN membership_redesign_apply_log_20260520 al
    ON al.table_name = 'trial_grant' AND al.row_id = tg.id;
-- 兜底
DELETE FROM trial_grant WHERE source_package_id IS NOT NULL;

DELETE ubb FROM user_booster_balance ubb
  INNER JOIN membership_redesign_apply_log_20260520 al
    ON al.table_name = 'user_booster_balance' AND al.row_id = ubb.id;

-- credit_cycle 在 02-apply 中并未写入（懒创建在切换后），此处删除"切换日之后由懒创建产生的所有行"
-- 用 cutover_date 配置参数化（DBA 在跑 04-rollback 时通过环境变量设置）：
--   SET @cutover_ts = '2026-05-20 02:00:00';   -- 改为 maintenance window 起点
DELETE FROM credit_cycle WHERE created_at >= @cutover_ts;
-- 注意：T+24h 内此操作会删除少数用户首次扣分懒创建的 cycle；usage_record 由 forward fix 处理

-- ----------------------------------------------------------------------
-- Step 2: 从 backup 恢复 credit_package（如果迁移途中改了它）
--         本次迁移 02-apply 没有 UPDATE/DELETE credit_package，仅 backup
--         理论上不需要恢复；但保留此 step 以备 future 迁移修改源表
-- ----------------------------------------------------------------------
-- (intentionally empty for this migration)

-- ----------------------------------------------------------------------
-- Step 3: 警告日志
-- ----------------------------------------------------------------------
SELECT 'ROLLBACK COMPLETE — credit_package preserved as-is, new tables cleared' AS status,
       'NEXT: git revert deployment commit, restart service to load old code' AS next_step;

COMMIT;
```

**注意点**：

- `DELETE` 用 `created_at` 范围判定迁移行；T+24h 内"新数据"也可能落在这个范围内 → 因此 rollback 不区分迁移行 vs 用户后续行，全删
- 这是**有意的丢失**：rollback 等于"假装迁移没发生"，T+24h 内用户活跃度低，影响小
- T+24h 后此脚本不可直接跑，必须人工评估损失并决定是否 forward fix

---

### §6.3 对账 SQL 模板

每用户必须满足的核心 invariant 在 §6.2.3 已展开。本节提供"批量执行 + 报告生成"的工程化模板，用于：

1. 迁前 dry-run 对账（01-dry-run.sql Section D）
2. 迁后立即 verify（03-verify.sql 全部）
3. T+0~T+7 天每日对账（守护期定时跑）

#### 6.3.1 报告格式

```
=== Migration Verify Report — 2026-05-20 03:15:42 ===

[INVARIANT_1] Balance Conservation
  Total users checked: 1247
  Violations: 0
  Status: PASS

[INVARIANT_2] Subscription Expires Match
  Total subscription rows: 892
  Violations: 0
  Status: PASS

[INVARIANT_3] Trial Remain Match
  Total trial_grant rows: 156
  Violations: 0
  Status: PASS

[INVARIANT_4] Booster Remain Sum
  Total booster_balance rows: 312
  Violations: 0
  Status: PASS

[INVARIANT_5A] Event Count Floor (Migration Day)
  Total users with events: 1247
  Violations: 0 (every user has events_count >= old_packages_count)
  Status: PASS

[INVARIANT_5B] Event Delta vs Grant Ops (Guard Period — daily only, skipped on migration day)
  Status: SKIPPED (run by daily-verify.sql in T+0~T+7d window)

[INVARIANT_6] Trial Unique
  Violations: 0
  Status: PASS

[INVARIANT_7] Active Subscription Unique
  Violations: 0
  Status: PASS

[INVARIANT_8] No Orphan
  Violations: 0
  Status: PASS

=== OVERALL: PASS — proceed with deployment ===
```

任何 invariant FAIL → 整体 FAIL，立即触发 rollback。

#### 6.3.2 守护期定时对账

T+0~T+7 天，每日凌晨跑简化版 verify（仅 invariant 1+5b+8，覆盖余额非负净增 + 当日事件增量 ≥ grant 操作数 + 数据完整）：

```sql
-- daily-verify.sql (cron: 0 3 * * *)
-- 简化版守护期对账，runtime < 30 秒

-- 每日只看"最近 24 小时新写入"行，不重扫全量
SELECT
  'daily_check_' || DATE(NOW()) AS check_id,
  (SELECT COUNT(*) FROM membership_event WHERE created_at > NOW() - INTERVAL 1 DAY) AS new_events,
  (SELECT COUNT(*) FROM subscription WHERE updated_at > NOW() - INTERVAL 1 DAY) AS sub_updates,
  (SELECT COUNT(*) FROM credit_cycle WHERE created_at > NOW() - INTERVAL 1 DAY) AS new_cycles,
  -- 检查是否有"扣减总和 > 用户当时余额"的异常（不可能但兜底）
  (SELECT COUNT(*) FROM (
    SELECT user_id, SUM(credits_deducted) AS total_deducted
    FROM usage_record
    WHERE created_at > NOW() - INTERVAL 1 DAY
    GROUP BY user_id
    HAVING total_deducted > 5000  -- 单日异常阈值
  ) anomaly) AS deduction_anomaly;
```

阈值：日异常事件 = 0、subscription 更新数与回调日志数一致、credit_cycle 创建数 ≈ DAU 的子集。任何指标偏离预期 ±20% 触发人工审查。

---

### §6.4 Maintenance Window 操作步骤（详细 runbook）

总耗时：5-10 分钟（P50）；P95 ≤ 12 分钟；P99 ≤ 15 分钟。建议时间：凌晨 2:00-4:00（用户活跃度最低）。

**时间预算与熔断阈值**（按 step 锚定，超时立即熔断 + rollback）：

| Step | 操作 | P50 预算 | 熔断阈值（硬上限） | 触发熔断后动作 |
|---|---|---|---|---|
| 1 | 部署 maintenance mode | 30s | 90s | 跑 04-rollback、解除 maintenance、取消切换 |
| 2 | 等流量稳定 503 | 30s | 60s | 同上 |
| 3.1 | 跑 dry-run | 60s | 180s | 同上（dry-run 慢 = prod 表数据量超预期） |
| 3.2 | 跑 02-apply.sql | 120s | 300s（**5 分钟**） | 触发 02-apply 内事务自动 rollback；若 SQL 仍卡 → 强制 KILL session + 04-rollback |
| 3.3 | 跑 03-verify.sql | 60s | 180s | 任 invariant 失败 → 04-rollback |
| 4 | 部署正常版本 | 90s | 180s | 回滚到 maintenance 镜像 + 04-rollback |
| 5 | 解除 maintenance | 30s | 60s | 同上 |
| 6 | smoke test | 90s | 180s | smoke 失败 → 回滚镜像 + 04-rollback |
| **整体** | T 时刻 → 全恢复 | **~10 分钟** | **15 分钟硬熔断** | **超过 15 分钟整体熔断**：rollback 一切，恢复 maintenance 状态，公告"今日切换取消，定下次窗口" |

**熔断检查点**：每个 step 完成时记录 timestamp 到飞书机器人；超过 P95 但未到熔断阈值 → 飞书告警但继续；超过熔断阈值 → 立即按上表执行 rollback 动作。

#### T-1 day（迁移前一天）

| 时间 | 操作 | 工件 |
|---|---|---|
| 09:00 | 拉取 prod 数据库快照（mysqldump → 备份机） | `prod-snapshot-20260519.sql` |
| 10:00 | 在备份机还原快照到独立 MySQL 实例 | `mysql-staging:3307` |
| 11:00 | 在 staging 跑 01-dry-run.sql | dry-run 报告 |
| 12:00 | 跑 Go 段合并校验脚本 | `verify_segment_merge.go` 输出 0 diffs |
| 14:00 | 跑 02-apply.sql + 03-verify.sql 全流程 | verify 全 PASS |
| 15:00 | 演练 04-rollback.sql 验证可用 | rollback 后 staging 状态恢复 |
| 16:00 | 邮件 + 飞书通知运营/产品/客服"明日 02:00 维护窗口" | 通知存档 |
| 17:00 | 更新 prod 站公告毛玻璃浮层"明日凌晨维护" | 前端配置 |

#### T 时刻（执行迁移，5-10 分钟）

**Step 1 — 部署 maintenance mode 镜像（02:00:00）**

```bash
# CI/CD 在 develop 分支已有 maintenance mode 镜像 tag
ssh prod 'docker stack deploy -c docker-compose.maintenance.yaml numind-prod'

# 中间件配置：所有 POST/PUT/PATCH/DELETE 返回 503
# GET/HEAD 返回缓存的"维护中"页面
```

**Step 2 — 等流量稳定 503（02:00:30，约 30 秒）**

```bash
# 监控请求日志，确认所有写请求返回 503
ssh prod 'tail -f /var/log/numind/access.log | grep -v "GET\|HEAD"'
# 30 秒内观察到非 GET 请求都是 503 → 进入下一步
```

**截图 1**：管理端发起一个写请求，截图 503 响应

**Step 3 — 跑 4 件套脚本（02:01:00 ~ 02:06:00，约 5 分钟）**

```bash
ssh prod << 'EOF'
cd /opt/numind/migrations/2026-05-XX-membership-redesign-migration

# 3.1 dry-run（重跑 prod 实时数据，确认无 last-minute drift）
docker exec -i numind-mysql-prod mysql -uroot -p$DB_PASS numind-prod < 01-dry-run.sql > dry-run-prod-$(date +%Y%m%d).log
# 检查输出：Section D 0 violations、Section E 所有 metric 在预期

# 3.2 apply（单事务）
docker exec -i numind-mysql-prod mysql -uroot -p$DB_PASS numind-prod < 02-apply.sql
# 检查 stdout: "APPLY COMPLETE"

# 3.3 verify（立即对账）
docker exec -i numind-mysql-prod mysql -uroot -p$DB_PASS numind-prod < 03-verify.sql > verify-prod-$(date +%Y%m%d).log
# 检查每个 invariant 的 violation_count = 0
EOF
```

**截图 2**：dry-run 报告（0 violations）
**截图 3**：apply 完成日志
**截图 4**：verify 报告（所有 invariant PASS）

**异常分支**：

- 任意 invariant violation_count > 0 → 立即跑 04-rollback.sql + 解除 maintenance + 取消本次切换
- apply 中途事务失败 → MySQL 自动 rollback，重新跑 dry-run 排查原因，本次切换取消

**Step 4 — 部署正常版本（02:06:30 ~ 02:08:00）**

```bash
ssh prod << 'EOF'
# 部署新代码（biz 用 5 张新表，无 cron）
docker stack deploy -c docker-compose.prod.yaml numind-prod

# 等容器健康
docker service ls | grep numind-prod
sleep 30
EOF
```

**关键校验**：

```bash
# 4.1 确认无 cron job 在跑（reconcileBillingMode / ActivatePending / ExpireActive 已删除）
ssh prod 'docker logs numind-prod_numind --tail 100 | grep -i cron'
# 期望：无 cron 启动日志

# 4.2 确认服务监听端口正常
curl http://prod:9095/health
# 期望：200 OK
```

**Step 5 — 解除 maintenance mode（02:08:00）**

```bash
ssh prod 'docker stack deploy -c docker-compose.prod.yaml numind-prod'
# 同 Step 4 命令；本步骤实质是确认正常 compose 已替换 maintenance compose
```

**截图 5**：解除后访问首页，正常加载

**Step 6 — Smoke test 关键 API（02:08:30 ~ 02:10:00）**

```bash
# 6.1 用户登录
TOKEN=$(curl -X POST https://prod/v1/web/login -d '{"username":"$E2E_USERNAME","password":"$E2E_PASSWORD"}' | jq -r .data.token)

# 6.2 余额查询（应返回新结构）
curl https://prod/v1/credits/balance -H "Authorization: Bearer $TOKEN"
# 期望：返回 {trial_remaining, cycle_remaining, cycle_end, booster_total, booster_usable, membership_state}

# 6.3 父账户客户列表（双状态显示）
curl https://prod/v1/users/children -H "Authorization: Bearer $TOKEN_PARENT"
# 期望：每个子账户带 membership_state 字段

# 6.4 admin B2B 账单
curl https://prod/v1/admin/b2b-billing-report?month=2026-05 -H "Authorization: Bearer $ADMIN_TOKEN"
# 期望：返回新口径事件级明细
```

**截图 6**：关键 API 调用结果，确认数据结构正确

**总耗时**：5-10 分钟（02:00:00 → 02:10:00）

#### T+0 ~ T+7 天（守护期）

- 每日凌晨 03:00 自动跑 daily-verify.sql，结果发飞书机器人
- T+1 早 09:00：人工查看夜间日志，确认无异常
- T+7 天：评估 DROP credit_package 表（决策：保留只读 30 天后再 DROP）

#### 截图记录归档

所有截图统一存入 `docs/migration-evidence/2026-05-20-membership-redesign/`：

- `01-pre-503-traffic.png` — 维护前正常流量
- `02-503-response.png` — 维护中写请求 503
- `03-dry-run-report.png` — dry-run 报告
- `04-apply-complete.png` — apply 成功日志
- `05-verify-pass.png` — verify 全 PASS
- `06-post-deploy-health.png` — 部署后服务健康
- `07-smoke-test-results.png` — smoke test 截图
- `08-after-30-days.png` — 30 天后再次 verify（可选）

---

### §6.5 数据完整性 invariant 清单

完整 invariant 集合（迁移阶段 + 运行期都需保证）。每条带 SQL 验证 + 失败处理。

| # | Invariant | 适用阶段 | SQL 验证 | 失败处理 |
|---|---|---|---|---|
| **I1** | 迁前迁后非负净增（恩泽迁移）：每用户 post_total ≥ pre_total，其中 post = trial.remain + (有 sub 时 2000) + booster.remain，pre = SUM(旧 active 包.remain)。**不允许 post < pre**（数据丢失） | 迁移 | §6.2.3 Invariant 1 | 立即 rollback，停止迁移 |
| **I2** | subscription.expires_at 等于旧表段尾 | 迁移 | §6.2.3 Invariant 2，容忍 1 秒舍入 | 立即 rollback |
| **I3** | trial_grant.credits_remaining 等于旧 trial 包 remain | 迁移 | §6.2.3 Invariant 3 | 立即 rollback |
| **I4** | user_booster_balance.credits_remaining = SUM(旧 active booster.remain) | 迁移 | §6.2.3 Invariant 4 | 立即 rollback |
| **I5a (migration-day)** | 迁移当天：COUNT(membership_event) >= COUNT(旧 grant 记录) | 迁移当天（03-verify.sql 一次性） | §6.2.3 Invariant 5a | 迁移期 rollback |
| **I5b (guard-period)** | 守护期：当日 event 增量 ≥ 当日新 grant 操作数（subscription INSERT + UPDATE 续费 + trial_grant INSERT + booster UPDATE 合计） | 运行期（daily-verify.sql 滑动窗） | §6.2.3 Invariant 5b | 失败处置：按 §3.2/§3.3 漏写排查；不允许"用户没投诉就不修" |
| **I6** | trial_grant 表 UNIQUE on user_id（DB 约束 + 业务校验） | 迁移 + 运行期 | `SELECT user_id FROM trial_grant GROUP BY user_id HAVING COUNT(*) > 1` 期望 0 行 | DB 约束保证；业务侧 ErrTrialAlreadyGranted |
| **I7** | subscription 表每用户 UNIQUE on (user_id, status='active')（部分索引） | 迁移 + 运行期 | `SELECT user_id FROM subscription WHERE status='active' GROUP BY user_id HAVING COUNT(*) > 1` 期望 0 行 | 续费场景必须 UPDATE 而非 INSERT；biz 层加锁 |
| **I8** | 不存在 orphan 行（所有外键 user_id 在 user 表必须存在） | 迁移 + 运行期 | §6.2.3 Invariant 8 | 迁移期 rollback；运行期人工修复或软删除 |
| **I9** | credit_cycle 严格在 subscription 期内：cycle.cycle_start >= sub.current_started_at AND cycle.cycle_end <= sub.expires_at | 运行期 | `SELECT cc.id FROM credit_cycle cc JOIN subscription s ON s.user_id = cc.user_id WHERE cc.cycle_start < s.current_started_at OR cc.cycle_end > s.expires_at` 期望 0 行 | biz 创建 cycle 时必须 SELECT FOR UPDATE 锁 sub；查到则报 P0 |
| **I10** | membership_event.idempotency_key UNIQUE | 运行期 | `SELECT idempotency_key FROM membership_event GROUP BY idempotency_key HAVING COUNT(*) > 1` 期望 0 行 | DB 约束保证；业务幂等中间件保证客户端不会发送重复 key |
| **I11** | booster 冻结一致性：用户无 active sub 且无 active trial 时，扣减必须跳过 booster | 运行期 | 测试用例 + 抽查日志 | E2E 用例覆盖；biz 层加 if 判断 |
| **I12** | 每月 cycle 行数：cycle 表中 (user_id, cycle_start) 在同一 subscription 周期内 UNIQUE | 运行期 | `SELECT user_id, cycle_start FROM credit_cycle WHERE subscription_id = X GROUP BY user_id, cycle_start HAVING COUNT(*) > 1` 期望 0 行 | DB 部分唯一索引 + ON CONFLICT DO NOTHING |

#### 失败响应 SOP

**迁移期 invariant FAIL（I1~I8 任一）**：

1. 立即停止后续操作
2. 跑 04-rollback.sql
3. git revert 部署 commit
4. 服务重启回老代码
5. 解除 maintenance mode
6. 飞书通知群"迁移失败已 rollback"
7. 邮件运营/产品"今日切换取消，定下次窗口"
8. 复盘：在 staging 复现该 invariant violation，修补后重新调度

**运行期 invariant FAIL（I9~I12）**：

1. 飞书机器人告警 → 当班工程师接手
2. 评估影响范围：是单用户 bug 还是系统性问题？
3. 单用户 bug → 临时手工修补该用户数据 + 排查 biz 层漏洞 → hotfix
4. 系统性问题（多用户）→ 启动 incident SOP → 评估是否需暂停服务

**绝对禁止**：

- "下次再说" — 数据完整性问题不可推迟，每多一秒都在恶化
- "可能是 SQL bug" — 必须给出"代码问题 vs SQL 问题"的明确判定，不能模糊处理
- "用户没投诉就不管" — invariant 是硬约束，不是用户感知就能 override 的

---

### §6 总结

本节固化了：

1. **段合并算法**：CTE + ROW_NUMBER + LAG + 分段标记的 SQL 实现 + Go 校验脚本伪代码
2. **4 件套迁移脚本**：dry-run（只读 + 预算迁后值）、apply（单事务全量写入）、verify（8 条 invariant 立即对账）、rollback（24h 内可执行的 backup 反向恢复）
3. **对账模板**：批量 invariant 检查 + 标准化报告格式 + 守护期定时对账
4. **Maintenance window runbook**：T-1 day 演练、T 时刻 6 步 5-10 分钟切换、T+7 天观察
5. **13 条 invariant**（含 I5 拆分为 I5a/I5b）：覆盖迁移期 + 运行期，每条 SQL 验证 + 失败 SOP

**进入 S3 前提**：本 spec §6 评审通过 + S2 reviewer 确认段合并算法正确性 + 4 件套脚本框架可工程化实现。S3 plan 阶段会把本节拆出 3-4 个独立 task：迁移脚本编写（2-3 人天）、Go 校验脚本编写（1 人天）、staging 演练（1 人天）。

---

## §7 切换日双口径拼接（B2B 账单）

本节解决 R5 风险：切换日（一次性全量上线）前后 B2B 账单的数据来源不同（旧：`credit_package`；新：`membership_event`），跨切换日的当月账单必须双口径拼接才能完整覆盖。

**总体策略**：以系统配置 `cutover_date`（UTC timestamp）为分界，按月份选择查询路径：

| 月份范围 | 数据源 | 实现 |
|---|---|---|
| `month_end <= cutover_date` | 纯老口径，扫 credit_package | §7.5 |
| `month_start < cutover_date < month_end` | 双口径 UNION，按复合键去重 | §7.3 |
| `month_start >= cutover_date` | 纯新口径，扫 membership_event | §7.6 |

切换日**之前**的历史月（包括上线前的所有月份）账单逻辑**永久锁定**，不随 membership_event 后续变化而改变——这保证了财务对账的可重现性。

---

### §7.1 字段映射表

老口径行（`credit_package`，仅 `grant_source='b2b_grant'`）映射到新口径行（`membership_event`）的字段对应：

| credit_package 字段 | membership_event 字段 | 转换规则 |
|---|---|---|
| `id` | `legacy_package_id` | 直接拷贝（仅在拼接视图中保留，实表不存） |
| `user_id` | `child_user_id` | 直接拷贝 |
| `granter_user_id` | `granter_user_id` | 直接拷贝；不为空才纳入（B2B 账单要求） |
| `grant_source` | `source` | `'b2b_grant'` → `'b2b_grant'` 一对一 |
| `type='trial'` | `event_type='trial_granted'`, `product_type='trial'` | 类型映射 |
| `type='subscription'` | `event_type='sub_granted'`, `product_type='monthly'` | 类型映射；老口径**每月一行**，新口径**每次 grant 一行**（详见去重） |
| `type='booster'` | `event_type='booster_granted'`, `product_type='booster'` | 类型映射；老口径单订单可能拆多行（每月一份），新口径单订单**一行 + quantity** |
| `activated_at` | `occurred_at` | 直接拷贝（老口径以激活时间为账单时间） |
| `total_credits` | `quantity` (booster) / `months` (sub) / 0 (trial) | trial 总额固定 200，不入 quantity；sub 老口径每行 = 1 月，quantity 字段按 0 处理；booster 老口径每行 = 1 份 |
| —（无对应） | `amount_cents` | trial → 990；subscription → 9900；booster → 2990。**与 `b2b_billing.go::amountForPackage` 一致**，定义为常量 `LegacyAmountTrial / LegacyAmountSubMonth / LegacyAmountBoosterUnit` |
| `created_at` | `created_at` | 直接拷贝 |
| —（无对应） | `idempotency_key` | 老行回填为 `legacy_pkg_<credit_package.id>`（保证 UNIQUE 全局可识别且不与新行冲突） |
| —（无对应） | `metadata` | 老行回填为 `{"legacy_migration": true, "legacy_package_id": <id>}` |

**边界 case 处理**：

- **老口径 subscription 拆月**：老逻辑下买 3 月 Pro 会创建 3 行 credit_package（每月一行）。映射到新口径时，仍然产出 3 个 sub_granted/sub_renewed 事件（首行 sub_granted，后续 sub_renewed），`amount_cents=9900`、`months=1`。**保留老语义**避免历史账单金额改变。
- **老口径 trial**：单行（200 积分 / 3 天），映射为单条 trial_granted 事件，`amount_cents=990`。
- **老口径 booster**：每份独立行（quantity 概念在老口径不存在），按行映射为 booster_granted 事件，`amount_cents=2990, quantity=1`。
- **老口径 `granter_user_id IS NULL`**：自购或非 B2B 路径，**不纳入 B2B 账单**（与现有 `b2b_billing.go:122-127` 行为一致）。
- **跨切换日同一笔**：极端情况，迁移瞬间老表与新表同时存在同一笔 grant（迁移脚本写 membership_event 但未删 credit_package）。复合键去重时**优先采纳新口径**（详见 §7.2）。

---

### §7.2 复合键去重规则

跨切换日月份双口径 UNION 后，可能出现"同一笔 grant 两表都有"的情况（迁移脚本运行瞬间）。去重策略：

**复合键定义**：`(granter_user_id, child_user_id, occurred_at_truncated_to_second, product_type, months, quantity)`

- `occurred_at_truncated_to_second`：`DATE_FORMAT(occurred_at, '%Y-%m-%d %H:%i:%s')`，截断到秒避免微秒抖动
- `months`：sub 事件取 months 列值（老口径恒为 1，因为每月一行）；booster/trial 事件该列为 NULL
- `quantity`：booster 事件取 quantity 列值；sub/trial 事件该列为 NULL
- 复合键比较：NULL 与 NULL 视为同值（`COALESCE(months, -1) = COALESCE(months, -1) AND COALESCE(quantity, -1) = COALESCE(quantity, -1)`），SQL 实现见 §7.3

**优先级规则**：

1. 同一复合键下，若同时存在 legacy 行与 new 行 → **优先保留 new 行**（来自 membership_event）
2. 同一复合键下仅有 legacy 行 → 保留 legacy 行（用 §7.1 字段映射转换）
3. 同一复合键下仅有 new 行 → 保留 new 行
4. 不同复合键 → 各自独立保留（视为不同事件）

**SQL 实现**：CTE + `ROW_NUMBER() OVER (PARTITION BY <key> ORDER BY source_priority ASC)` ，其中 `source_priority` 设 `new=1, legacy=2`，`WHERE rn=1` 取头条。

---

### §7.3 SQL 框架（跨切换日月份）

**完整可执行的 SQL 模板**（MySQL 8.0）：

```sql
-- 参数：
--   :month_start          月初 UTC timestamp，例如 '2026-06-01 00:00:00'
--   :month_end            月末 UTC timestamp（开区间），例如 '2026-07-01 00:00:00'
--   :cutover_date         切换日 UTC timestamp，例如 '2026-06-03 02:00:00'
--   :granter_filter       可选父账户过滤；NULL 表示不过滤
WITH
-- (1) 老口径：切换日之前激活的 b2b_grant 行
legacy_events AS (
    SELECT
        cp.id                                              AS legacy_package_id,
        NULL                                               AS event_id,
        cp.granter_user_id                                 AS granter_user_id,
        cp.user_id                                         AS child_user_id,
        CASE cp.type
            WHEN 'trial'        THEN 'trial_granted'
            WHEN 'subscription' THEN 'sub_granted'
            WHEN 'booster'      THEN 'booster_granted'
        END                                                AS event_type,
        CASE cp.type
            WHEN 'trial'        THEN 'trial'
            WHEN 'subscription' THEN 'monthly'
            WHEN 'booster'      THEN 'booster'
        END                                                AS product_type,
        -- months / quantity 与 §2.5 schema 保持一致（不适用时 NULL，不是 0）
        CASE cp.type
            WHEN 'subscription' THEN 1
            ELSE NULL
        END                                                AS months,
        CASE cp.type
            WHEN 'booster'      THEN 1
            ELSE NULL
        END                                                AS quantity,
        CASE cp.type
            WHEN 'trial'        THEN 990
            WHEN 'subscription' THEN 9900
            WHEN 'booster'      THEN 2990
            ELSE 0
        END                                                AS amount_cents,
        cp.activated_at                                    AS occurred_at,
        'b2b_grant'                                        AS source,
        CONCAT('legacy_pkg_', cp.id)                       AS idempotency_key,
        2                                                  AS source_priority   -- legacy 优先级低
    FROM credit_package cp
    WHERE cp.grant_source = 'b2b_grant'
      AND cp.granter_user_id IS NOT NULL
      AND cp.activated_at >= :month_start
      AND cp.activated_at < LEAST(:cutover_date, :month_end)
      AND (:granter_filter IS NULL OR cp.granter_user_id = :granter_filter)
),
-- (2) 新口径：切换日及之后发生的 b2b_grant 事件
new_events AS (
    SELECT
        NULL                                               AS legacy_package_id,
        me.id                                              AS event_id,
        me.granter_user_id                                 AS granter_user_id,
        me.child_user_id                                   AS child_user_id,
        me.event_type                                      AS event_type,
        me.product_type                                    AS product_type,
        me.months                                          AS months,
        me.quantity                                        AS quantity,
        me.amount_cents                                    AS amount_cents,
        me.occurred_at                                     AS occurred_at,
        me.source                                          AS source,
        me.idempotency_key                                 AS idempotency_key,
        1                                                  AS source_priority   -- new 优先级高
    FROM membership_event me
    WHERE me.source = 'b2b_grant'
      AND me.granter_user_id IS NOT NULL
      AND me.occurred_at >= GREATEST(:cutover_date, :month_start)
      AND me.occurred_at < :month_end
      AND (:granter_filter IS NULL OR me.granter_user_id = :granter_filter)
),
-- (3) UNION + 复合键去重
unioned AS (
    SELECT * FROM legacy_events
    UNION ALL
    SELECT * FROM new_events
),
deduped AS (
    SELECT
        legacy_package_id, event_id, granter_user_id, child_user_id,
        event_type, product_type, months, quantity, amount_cents,
        occurred_at, source, idempotency_key,
        ROW_NUMBER() OVER (
            PARTITION BY
                granter_user_id,
                child_user_id,
                DATE_FORMAT(occurred_at, '%Y-%m-%d %H:%i:%s'),
                product_type,
                -- months / quantity 各自参与去重；NULL 与 NULL 视为同值（COALESCE 兜底）
                COALESCE(months,   -1),
                COALESCE(quantity, -1)
            ORDER BY source_priority ASC, occurred_at ASC
        ) AS rn
    FROM unioned
)
-- (4) 最终输出：父账户聚合 + 明细 JSON_ARRAYAGG
SELECT
    d.granter_user_id                              AS parent_user_id,
    u.username                                     AS parent_username,
    COUNT(*)                                       AS events_count,
    SUM(d.amount_cents)                            AS amount_cents,
    JSON_ARRAYAGG(
        JSON_OBJECT(
            'event_id',         d.event_id,
            'legacy_package_id', d.legacy_package_id,
            'child_user_id',    d.child_user_id,
            'child_username',   cu.username,
            'event_type',       d.event_type,
            'product_type',     d.product_type,
            'months',           d.months,
            'quantity',         d.quantity,
            'amount_cents',     d.amount_cents,
            'occurred_at',      DATE_FORMAT(d.occurred_at, '%Y-%m-%dT%H:%i:%sZ'),
            'source',           d.source,
            'idempotency_key',  d.idempotency_key
        )
    )                                              AS details_json
FROM deduped d
LEFT JOIN user  u  ON u.id  = d.granter_user_id
LEFT JOIN user  cu ON cu.id = d.child_user_id
WHERE d.rn = 1
GROUP BY d.granter_user_id, u.username
ORDER BY d.granter_user_id ASC;
```

**实现说明**：

- biz 层用此 SQL 拿到 `[]ParentBillingRow`，再聚合 `total_amount_cents` / `total_events_count` / `active_parents_count`
- `JSON_ARRAYAGG` 输出按 occurred_at 排序：可在子查询中 `ORDER BY occurred_at` 后 `JSON_ARRAYAGG`（MySQL 8.0.14+ 行为依赖于优化器，必要时改为应用层排序）
- biz 层用 `gorm.Raw(...).Scan(&rows)` 执行；`details_json` 字段用 `json.RawMessage` 接收，再 `json.Unmarshal` 到 `[]GrantDetail`

**索引依赖**（详见 §2 数据模型）：

- `credit_package`：现有 `idx_grant_source_activated_at(grant_source, activated_at)` 可加速 legacy 查询；若不存在则新增
- `membership_event`：新增 `idx_me_granter_occurred(granter_user_id, occurred_at)` + `idx_me_source_occurred(source, occurred_at)`

---

### §7.4 切换日参数化

**配置位置**：`config_*.yaml` 新增配置项 `billing.cutover_date`，格式 ISO-8601 RFC3339 UTC：

```yaml
billing:
  cutover_date: "2026-06-03T02:00:00Z"   # 一次性切换的 maintenance window 起点 UTC
```

**优先级**：环境变量 `BILLING_CUTOVER_DATE` > yaml 配置（**两者必有其一，缺失则启动失败**）。

**严格强制**：`cutover_date` 是必填配置项，**禁止零值/空字符串作为"系统从未切换"的隐式语义**——这种语义会让"配置漏写"和"全新部署"两种语义混淆，造成对账歧义。

- **服务启动时校验**：`main.go` 启动校验若 `cfg.Billing.CutoverDate.IsZero()` 则 `log.Fatal("billing.cutover_date is required, configure in yaml or via BILLING_CUTOVER_DATE env var")` 拒绝启动
- **admin UI**：在 `/b2b-billing` 页面顶部显式展示当前 `cutover_date`（财务可见），未配置时显示红色警示条
- **全新部署场景**：若全新部署希望所有月份都走 `new_only`，需**显式配置** `cutover_date` 为某个早于服务上线的固定日期（推荐 `2020-01-01T00:00:00Z`），不依赖零值兜底

**biz 层访问**：

```go
type B2BBillingConfig struct {
    CutoverDate time.Time   // 来自 viper.GetTime("billing.cutover_date")，启动时校验非零
}

func (b *b2bBillingBiz) chooseSource(monthStart, monthEnd, cutover time.Time) string {
    // cutover 在启动期已校验非零，此处不再处理 IsZero
    if !monthStart.Before(cutover) {
        return "new_only"
    }
    if !monthEnd.After(cutover) {
        return "legacy_only"
    }
    return "cutover_split"
}
```

**dispatch**：`GetBillingReport` 主函数根据 `chooseSource` 结果分发到 `getLegacyReport` / `getNewReport` / `getCutoverSplitReport`，三个内部方法返回相同的 `*B2BBillingReport` 结构。

**变更控制**：cutover_date 配置一旦写入 prod，**禁止再修改**（修改会让历史月份账单结果跳变）。建议加入 `config_prod.yaml` 后通过 git 提交固化，并在 admin UI 显式展示 cutover_date（财务/运营可见）。

---

### §7.5 切换日**之前**月份账单生成（永久锁定）

切换日之前的历史月份完全沿用 `internal/numind/biz/b2b_billing/b2b_billing.go` 现有逻辑，**不做任何修改**。仅在 dispatch 层包装为 `getLegacyReport(ctx, monthStart, monthEnd)`：

```go
func (b *b2bBillingBiz) getLegacyReport(ctx context.Context, monthStart, monthEnd time.Time) (*B2BBillingReport, error) {
    // 完全复用现有 GetBillingReport 主体逻辑（line 78-170）
    // 仅 month 字符串展示从 monthStart.Format("2006-01") 重新生成
    // 关键：保留 amountForPackage / productTypeForPackage 的原始价格语义
    // 关键：保留 granter_user_id IS NULL 的 skip 处理
    var pkgs []model.CreditPackage
    if err := b.ds.DB().WithContext(ctx).
        Where("grant_source = ? AND activated_at >= ? AND activated_at < ?",
            model.GrantSourceB2BGrant, monthStart, monthEnd).
        Order("activated_at ASC").
        Find(&pkgs).Error; err != nil {
        return nil, fmt.Errorf("getLegacyReport: query packages: %w", err)
    }
    // ... 其余逻辑同现有 b2b_billing.go ...
}
```

**响应字段映射**（兼容新结构）：

| 现有字段 | 新结构字段 | 备注 |
|---|---|---|
| `ParentBillingRow.GrantsCount` | `events_count` | 字段重命名 |
| `GrantDetail.GrantedAt` | `occurred_at` | 字段重命名 |
| `GrantDetail.ProductType` | `product_type` | 不变 |
| `GrantDetail.Months` | `months` | 不变（trial=0, sub=1） |
| —（无） | `event_type` | 老口径回填：trial→trial_granted, monthly→sub_granted |
| —（无） | `event_id` | 老口径置 `null`，前端识别 |
| —（无） | `legacy_package_id` | 老口径填 credit_package.id |
| —（无） | `quantity` | 老口径置 0（老 booster 路径未支持） |
| —（无） | `idempotency_key` | 老口径置 `legacy_pkg_<id>` |

**永久锁定保证**：切换日之前的 credit_package 数据在迁移完成后**只读**，不会被新代码修改。只要 `credit_package` 表存在（保留 7 天后视情况 DROP），历史账单结果就完全可重现。**若切换日之前的 credit_package 表被 DROP，需要先把历史账单 dump 到独立归档表 `b2b_billing_archive` 永久保留**——此项作为部署 checklist 强制项。

**Owner / S3 task**：S3 plan 阶段必须新增独立 task：「定义 `b2b_billing_archive` 表 schema + dump 脚本 + admin 报表 fallback 路径」，包含以下子项：

- `b2b_billing_archive` 表 schema：列与 §7.6 `details_json` 输出一一对应，按 `(month, parent_user_id)` 复合主键
- dump 脚本：`scripts/2026-MM-XX-archive-legacy-billing/dump.go`，遍历切换日之前所有月份，调用 `getLegacyReport` 后写入 archive 表
- admin 报表 fallback：`b2b_billing.go::GetBillingReport` 在 `credit_package` 表已 DROP 后，对历史月份直接读 `b2b_billing_archive`（无需重算）
- 此 task 在 T+7d 前必须完成（即 credit_package 候选 DROP 之前）
- Owner 在 S3 plan 中显式分配（不留"待定"）

---

### §7.6 切换日**之后**月份账单生成（纯新口径）

```sql
-- 参数：
--   :month_start, :month_end, :granter_filter
SELECT
    me.granter_user_id                             AS parent_user_id,
    u.username                                     AS parent_username,
    COUNT(*)                                       AS events_count,
    SUM(me.amount_cents)                           AS amount_cents,
    JSON_ARRAYAGG(
        JSON_OBJECT(
            'event_id',         me.id,
            'child_user_id',    me.child_user_id,
            'child_username',   cu.username,
            'event_type',       me.event_type,
            'product_type',     me.product_type,
            'months',           me.months,
            'quantity',         me.quantity,
            'amount_cents',     me.amount_cents,
            'occurred_at',      DATE_FORMAT(me.occurred_at, '%Y-%m-%dT%H:%i:%sZ'),
            'source',           me.source,
            'idempotency_key',  me.idempotency_key
        )
    )                                              AS details_json
FROM membership_event me
LEFT JOIN user u  ON u.id  = me.granter_user_id
LEFT JOIN user cu ON cu.id = me.child_user_id
WHERE me.source = 'b2b_grant'
  AND me.granter_user_id IS NOT NULL
  AND me.occurred_at >= :month_start
  AND me.occurred_at < :month_end
  AND (:granter_filter IS NULL OR me.granter_user_id = :granter_filter)
GROUP BY me.granter_user_id, u.username
ORDER BY me.granter_user_id ASC;
```

**说明**：

- 此 SQL 与 §7.3 的 `new_events` CTE 加聚合后等价，单独抽出避免 `cutover_split` 模式的 CTE / ROW_NUMBER 开销
- `event_type` 直接来自 membership_event 列，不需要派生
- `amount_cents` 来自 membership_event 列，是 grant 时刻定价的真实金额，与 grant 当时的产品价格强一致——**这是新口径的关键优势**：以后调价不会让历史账单跳变
- biz 层 `getNewReport(ctx, monthStart, monthEnd, granterFilter)` 直接执行此 SQL → 返回 `*B2BBillingReport`

**性能**：100 万行 membership_event 下，依赖 `idx_me_source_occurred(source, occurred_at)` + `idx_me_granter_occurred(granter_user_id, occurred_at)`，单月查询应 < 500ms（AC-18）。`granter_filter` 非空时进一步收敛到单父账户。

**输出聚合**：biz 层执行 SQL 后，遍历结果计算：

```go
total := int64(0)
totalEvents := 0
activeParents := len(rows)
for _, r := range rows {
    total += r.AmountCents
    totalEvents += r.EventsCount
}
return &B2BBillingReport{
    Month:             monthStr,
    CutoverDate:       cfg.CutoverDate,
    Source:            "new_only",
    ByParent:          rows,
    TotalAmountCents:  total,
    TotalEventsCount:  totalEvents,
    ActiveParentsCount: activeParents,
}
```

---

---

## §8 前端契约

> 本章定义 numind-web-v3（用户端）和 numind-admin-web（管理端）在会员积分体系重构后的全部前端契约：Pinia store 形态、TypeScript interface、组件交互规则、错误码到文案的映射、以及旧 UI 元素的移除清单。所有 Pinia store 必须遵守 `.claude/rules/frontend-state.md` 的 setup syntax + axios 封装规则；所有异步视图必须处理 4 状态（loading / empty / error / success）。

---

### §8.1 numind-web-v3 余额组件（路径 `/credits`）

子账户用户登录后访问 `/credits`，看到自己的会员状态、积分余额、加量包余额。本节定义对应的 store + component 契约。

#### §8.1.1 BalanceDTO TypeScript interface

来自 §5.3 `GET /v1/credits/balance` 响应（口径与后端 1:1 对应，字段命名采用 snake_case 透传后端 JSON 不做转换）。**注意 §5.3 实际响应中 `membership_state` 是嵌套对象**，前端不应该平铺；纯渲染态枚举（`'free' | 'trial' | 'pro'`）由前端 store getter `displayState` 派生（见 §8.1.2）：

```typescript
// src/api/credits.ts

// 与 §5.3 nested membership_state 对象 1:1 对应
export interface MembershipStateDTO {
  has_active_trial: boolean
  trial_granted_at: string | null              // ISO 8601 带时区，无 trial 时为 null
  trial_expires_at: string | null              // ISO 8601 带时区，无 trial 时为 null
  has_active_subscription: boolean
  subscription_first_started_at: string | null
  subscription_current_started_at: string | null
  subscription_expires_at: string | null       // ISO 8601 带时区，无 sub 时为 null
  total_months_purchased: number               // 无 sub 时为 0
}

export interface BalanceDTO {
  user_id: number

  // 会员状态机（嵌套对象，§5.3 锁定）
  membership_state: MembershipStateDTO

  // 试用积分（lifetime 单次 200，3 天）
  trial_remaining: number            // 当前剩余，已过期返回 0

  // Pro 月度 cycle 积分（懒创建）
  cycle_remaining: number            // 当月剩余，无 active sub 时返回 0
  cycle_start: string | null         // 当前 cycle 起点；无 cycle 则 null
  cycle_end: string | null           // ISO 8601，无 cycle 时为 null

  // 加量包积分（永不过期，但会员到期后冻结）
  booster_total: number              // 账上总余额（无视冻结）
  booster_usable: number             // 当前可用余额；冻结时 = 0，可用时 = booster_total
  booster_frozen: boolean            // booster_total > 0 且无 active sub/trial 时为 true

  next_refill_at: string | null      // = cycle_end，下次月度刷新点；无 sub 则 null
}

export interface BalanceResponse {
  code: number
  message: string
  data: BalanceDTO
}

export const getBalance = () => request.get<BalanceResponse>('/v1/credits/balance')
```

#### §8.1.2 Pinia store 结构（`src/stores/credits.ts`）

```typescript
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { getBalance } from '@/api/credits'
import type { BalanceDTO } from '@/api/credits'

// 前端派生的纯渲染态枚举（不出现在后端响应中）
export type DisplayState = 'free' | 'trial' | 'pro'

export const useCreditsStore = defineStore('credits', () => {
  // ===== State =====
  const balance = ref<BalanceDTO | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // ===== Getters =====
  // 派生：has_active_trial || has_active_subscription → 是会员
  const isMember = computed(() => {
    const ms = balance.value?.membership_state
    return !!ms && (ms.has_active_trial || ms.has_active_subscription)
  })

  // 三态展示规则（与 §3.6 GetMembershipState 显示规则一致）：
  // trial 在期一律遮蔽 pro → 'trial'；仅 sub 在期 → 'pro'；都无 → 'free'
  const displayState = computed<DisplayState>(() => {
    const ms = balance.value?.membership_state
    if (!ms) return 'free'
    if (ms.has_active_trial) return 'trial'           // trial+pro overlap 也走这条
    if (ms.has_active_subscription) return 'pro'
    return 'free'
  })

  const isBoosterFrozen = computed(() => balance.value?.booster_frozen ?? false)

  // 到期日 getter（屏蔽嵌套结构对组件层的暴露）
  const trialExpiresAt = computed(() => balance.value?.membership_state?.trial_expires_at ?? null)
  const proExpiresAt   = computed(() => balance.value?.membership_state?.subscription_expires_at ?? null)

  // ===== Actions =====
  async function fetchBalance() {
    loading.value = true
    error.value = null
    try {
      const res = await getBalance()
      balance.value = res.data.data
    } catch (e: any) {
      error.value = e?.response?.data?.message ?? '加载余额失败'
    } finally {
      loading.value = false
    }
  }

  function reset() {
    balance.value = null
    loading.value = false
    error.value = null
  }

  return {
    balance, loading, error,
    isMember, displayState, isBoosterFrozen, trialExpiresAt, proExpiresAt,
    fetchBalance, reset,
  }
})
```

**调用规则**：
- 路由 `/credits` 进入时调用 `fetchBalance()` 一次，不轮询
- 购买 booster 成功 / 父账户为本账户开通会员后由 caller 主动 `fetchBalance()` 刷新
- logout 时由 user store 调用 `reset()` 清状态

#### §8.1.3 三卡片布局

页面纵向堆叠 3 张卡片，遵循 `@DESIGN.md §7` 自研组件规范，禁用外部 UI 框架。

| 卡片 | 内容 | 渲染条件 |
|------|------|---------|
| 卡片 1：会员状态 | 徽章 + 文案 + 到期日 | 始终展示 |
| 卡片 2：积分余额 | trial / cycle / booster 三栏分列 | 始终展示 |
| 卡片 3：购买加量包 | 入口按钮（仅会员可点）+ 引导文案 | 始终展示，非会员置灰 |

#### §8.1.4 会员状态 3 种渲染规则

`displayState` 决定徽章颜色与主文案；副文案显示到期日。

| displayState | 徽章颜色 | 主文案 | 副文案 |
|---|---|---|---|
| `free` | 灰 (token: `surface-muted`) | 免费用户 | 开通会员解锁全部功能 |
| `trial` | 蓝 (token: `accent-info`) | 试用中 | store getter `trialExpiresAt` 到期 |
| `pro` | 金 (token: `accent-premium`) | Pro 会员 | store getter `proExpiresAt`（即 `subscription_expires_at`）到期 |

**关键规则**：当 trial 与 sub 同时在期（`has_active_trial && has_active_subscription`，叠加期），`displayState` getter 返回 `'trial'`（trial 优先遮蔽），子账户视角统一渲染为蓝色徽章，**不向子账户暴露 Pro 已开通的细节**。Pro 信息仅父账户在 `/customers` 页面可见（见 §8.3）。该规则与 §3.6 GetMembershipState 的"trial 在期一律遮蔽 pro"显示规则一致。

#### §8.1.5 到期日格式化

- 后端返回 ISO 8601 带时区字符串（如 `2026-05-15T00:00:00+08:00`）
- 前端使用 `dayjs` 或 `Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai' })` parse 后渲染为 `YYYY-MM-DD`
- 若值为 `null`，渲染 `—`（占位符），不渲染 `1970-01-01`
- 时区规则统一：所有用户端日期一律按 UTC+8 渲染（见 §8.5）

#### §8.1.6 Booster 冻结 UI

当 `isBoosterFrozen === true`（即 `booster_total > 0` 且 `!has_active_trial && !has_active_subscription`，等价于 `displayState === 'free'`）：

- 卡片 2 第三栏「加量包」数字渲染为灰色（token: `text-muted`）
- 数字旁加锁标 icon（自研 `IconLock`）
- 数字下方文案「需要开通会员后才能使用」
- 行内 CTA「开通会员」，点击跳转至 §8.2 购买引导（若无父账户绑定，跳转至帮助页"如何开通会员"）

非冻结状态：直接渲染 `booster_usable` 数字，颜色 token `text-default`。

#### §8.1.7 4 状态处理

| 状态 | 触发条件 | 渲染 |
|------|---------|------|
| loading | `loading === true` 且 `balance === null` | 三张骨架屏卡片（自研 `Skeleton` 组件） |
| empty | 不出现 | 用户必有 free 状态，empty 视为异常 |
| error | `error !== null` | 红色 toast 显示 `error` 文案 + 行内「重试」按钮调用 `fetchBalance()` |
| success | `balance !== null` 且 `loading === false` | 正常 3 卡片渲染 |

---

### §8.2 numind-web-v3 booster 购买弹窗

#### §8.2.1 触发与可见性

- 入口：`/credits` 页卡片 3 「购买加量包」按钮
- 仅在 `isMember === true` 时可点击；非会员状态按钮置灰，hover 提示「开通会员后可购买加量包」
- 后端兜底：即便绕过前端，后端会返回 `ErrNotActiveMember`（见 §8.6）

#### §8.2.2 弹窗组件结构

弹窗使用自研 `Modal` 组件，禁止 Element Plus / Ant Design Vue。组件路径建议 `src/components/credits/BoosterPurchaseModal.vue`。

字段：
- 数量选择：横向 3 个快捷按钮 `1 份` / `5 份` / `10 份` + 自定义数字输入框
- 输入框默认值 `1`，点击快捷按钮高亮该按钮 + 同步填入输入框
- 单价显示：`¥29.9 / 份`
- 实时总价：`{quantity} × ¥29.9 = ¥{total}`，total 千分位分隔（如 `¥299,000.00`）
- 提交按钮 `立即购买`

#### §8.2.3 验证规则（client-side，blur 触发）

- 必须为正整数（拒绝 `0`、负数、小数、非数字字符串）
- 超过 10000：输入框红框（token: `border-error`）+ 行内错误「单次最多购买 10000 份」+ 禁用提交按钮
- 输入合法时，错误清除，提交按钮可点

后端兜底：超限返回 `ErrBoosterQuantityExceedsLimit`（AC-13），即使前端绕过校验也安全。

#### §8.2.4 提交流程

1. 点击「立即购买」→ 调 `POST /v1/orders`，body `{ user_id, product_type: 'booster', quantity: N, pay_channel: 'wechat' }`，header 必带 `Idempotency-Key: <uuid>`（与 §8.3.5 同样的 uuid 生成方式，每次点击生成新 key，保证网络重传幂等）
2. 后端返回 §5.2 锁定的 `{ order_id, out_trade_no, status: 'pending', pay_params: { appid, noncestr, package, partnerid, prepayid, timestamp, sign }, ... }` → 前端用 `pay_params` 唤起微信支付（H5 / JSAPI / 小程序 SDK）；若改走支付宝，`pay_channel='alipay'` 时 `pay_params` schema 由后端按支付宝规范返回（详见 §5.2）
3. 前端轮询 `GET /v1/orders/:order_id/status`，间隔 2 秒，最长 30 秒
4. **成功路径**（`status === 'paid'`）：
   - 关闭弹窗
   - Toast 绿色提示「购买成功，加量包已到账」
   - 调用 `creditsStore.fetchBalance()` 刷新余额
5. **失败路径**（`status === 'failed'` / 后端返回非 0 code）：
   - 弹窗内显示行内错误 + 「重试」CTA
   - 重试时不重新下单，重新打开弹窗让用户重新点击「立即购买」生成新订单（新 Idempotency-Key）
6. **超时路径**（轮询 30 秒未到 `paid` 也未 `failed`）：
   - 弹窗内显示「订单处理中，请稍后刷新页面」
   - 提供「关闭」按钮，关闭后用户可手动 `fetchBalance()` 或刷新页面

> **依赖标注（S3 plan 阶段补全）**：步骤 3 引用的 `GET /v1/orders/:order_id/status` 端点在 §5 范围内尚未定义。S3 plan 必须加 task：在 numind-server 补该端点，响应至少含 `{ order_id, status: 'pending'|'paid'|'failed'|'cancelled', paid_at: string|null, amount_cents }`，权限要求 = 受益人 token 或父账户 token。

#### §8.2.5 4 状态

- loading：提交中（按钮 spinner + 禁用）
- empty：N/A（弹窗内必有数量输入）
- error：行内错误 + 重试 CTA
- success：toast + 刷新余额

---

### §8.3 numind-web-v3 客户管理页双状态显示（路径 `/customers`）

父账户访问 `/customers` 管理子账户。本节定义客户列表的会员状态展示与「开通会员」操作的契约。

#### §8.3.1 列表新增列「会员状态」

由 `GET /v1/users/children` 列表端点返回的 `ChildSummaryDTO` 渲染。字段命名严格对齐 §5.4 单查端点（`GET /v1/users/children/:child_id/balance`）已锁定的 nested `membership_state` 结构：

```typescript
interface ChildSummaryDTO {
  user_id: number
  name: string
  // 与 §5.4 单查端点 nested membership_state 结构 1:1 对齐（前端 store 共用解构逻辑）
  membership_state: {
    has_active_trial: boolean
    trial_expires_at: string | null
    has_active_subscription: boolean
    subscription_expires_at: string | null
  }
  has_used_trial: boolean        // 由 trial_grant 表 EXISTS 计算（即使过期也为 true）—— 「开通会员」弹窗 trial tab 置灰判定用
  cycle_remaining: number        // 父账户可见
  // 注意：不返回 booster_total / booster_usable，父账户不可见（§8.3.3 隐私边界）
}
```

> **依赖标注（S3 plan 阶段补全）**：本 §8.3 引用的 `GET /v1/users/children` 列表端点在 §5 范围内尚未单独定义（§5 仅有 §5.4 单查端点）。S3 plan 必须加 task：在 numind-server 补 `GET /v1/users/children` 端点，响应字段以本节 `ChildSummaryDTO` 为准；`has_used_trial` 同时在 §5.4 单查响应里追加（admin 端 / GrantMembershipModal 复用同一字段）。

#### §8.3.2 4 种渲染规则

> 简记：`mt = membership_state.has_active_trial`，`ms = membership_state.has_active_subscription`。日期字段同样从 `membership_state.trial_expires_at` / `membership_state.subscription_expires_at` 读取。

| 子账户状态 | 徽章 | 文案 |
|---|---|---|
| `!mt && !ms` | 灰 | 免费用户 |
| `mt && !ms` | 蓝 | 试用中（trial_expires_at YYYY-MM-DD 到期） |
| `mt && ms` | 紫色双标（试用蓝 + Pro 金） | 试用中 + Pro 已开通（试用 trial_expires_at / Pro subscription_expires_at） |
| `!mt && ms` | 金 | Pro 会员（subscription_expires_at YYYY-MM-DD 到期） |

日期格式化规则同 §8.5。

#### §8.3.3 隐私边界：不显示 booster 余额

父账户**不可见**子账户的 booster 余额（即使 admin 端可见）。理由：
- booster 是子账户日常自助消耗
- 父账户只负责开通主会员（trial / Pro）
- API 层不返回该字段，前端无法绕过显示

任何在 `/customers` 列表展示 booster 余额的代码（如有）必须移除。

#### §8.3.4 「开通会员」按钮 + 操作弹窗

每行客户末尾「开通会员」按钮 → 弹出 `GrantMembershipModal`：

- 顶部 tab：`体验包` / `Pro 会员`
- **体验包 tab**：
  - 显示「赠送 200 积分，3 天有效期，¥0」
  - 若该子账户 `has_used_trial === true`（来自 `ChildSummaryDTO.has_used_trial`，由 trial_grant 表 EXISTS 查询）→ 整个 tab 内容置灰，显示「该账户已使用过体验包」hover 提示
- **Pro tab**：
  - 月数选择：`1` ~ `12` 月（横向数字选择，复用现有 `MonthSelector` 组件）
  - 显示对应金额：`{months} × 标准单价 = ¥{total}`
  - 在期续费提示：若 `membership_state.has_active_subscription === true`，文案改为「续费延期 {months} 个月，新到期日 YYYY-MM-DD」
- 提交按钮「确认开通」

#### §8.3.5 提交契约

调用 `POST /v1/users/children/:child_id/grant-membership`，body：

```typescript
{
  product_type: 'trial' | 'monthly',
  months?: number,            // monthly 必填，trial 不传
}
```

**关键：必须带客户端生成的 Idempotency-Key UUID**（解决 AC-16 lost update）：

```typescript
import { v4 as uuidv4 } from 'uuid'

const idempotencyKey = uuidv4()  // 每次点击「确认开通」生成新 key
await request.post(
  `/v1/users/children/${childId}/grant-membership`,
  { product_type, months },
  { headers: { 'Idempotency-Key': idempotencyKey } }
)
```

- 用户两个 tab 同时点 → 两次点击产生两个不同 key → 两次都成功延期（AC-16a）
- 单次点击的网络层重传 → 同一 key → 后端去重，仅延期一次（AC-16b）

#### §8.3.6 后端响应分支与 Toast 文案

后端响应 `data.event_type` 字段决定成功 toast 文案：

| event_type | toast 文案 |
|---|---|
| `trial_granted` | 已为 {child_name} 开通体验包，3 天有效期 |
| `sub_granted` | 已为 {child_name} 开通 Pro 会员 {months} 个月，{pro_expires_at} 到期 |
| `sub_renewed` | 已为 {child_name} 续费 Pro 会员 {months} 个月，新到期日 {pro_expires_at} |

提交完成后必须调用 `customersStore.fetchChildren()` 刷新列表，列表行的会员状态徽章应立即更新。

#### §8.3.7 4 状态

- loading：弹窗按钮 spinner
- empty：N/A
- error：行内错误（错误码映射见 §8.6）+ 「重试」CTA
- success：toast + 刷新列表 + 关闭弹窗

---

### §8.4 numind-admin-web B2B 月度账单页（路径 `/b2b-billing`）

admin 端新增页面，对接 `GET /v1/admin/b2b-billing-report?month=YYYY-MM`。

#### §8.4.1 顶部筛选区

- **月份选择器**：`MonthPicker` 组件（自研），默认本月，可选范围 `2025-01` 至当前月
- **父账户筛选**：可选下拉，从 `GET /v1/admin/parent-users` 拉取列表；不选 = 全部父账户

筛选变化时立即触发数据刷新（无需「查询」按钮）。

#### §8.4.2 主表（DataTable，遵守 §3 硬规则 1）

> 管理端必须用 `DataTable` 表格布局，禁止用卡片网格代替。

主表分组按父账户聚合，每组一个折叠节点：

```
父账户：A 公司（granter_user_id=123）  本月小计：¥1,247.00 (5 笔)  [展开]
父账户：B 公司（granter_user_id=456）  本月小计：¥598.00 (2 笔)   [折叠]
   ├─ 2026-04-12  小明（子）  开通 Pro    Pro      3 个月        ¥299.00
   └─ 2026-04-25  小红（子）  购买加量包  Booster  10 份         ¥299.00
```

展开后显示该父账户本月所有事件明细行，列：

| 列 | 字段 | 渲染 |
|---|---|---|
| 日期 | `occurred_at` | YYYY-MM-DD HH:mm（UTC+8） |
| 子账户 | `child_user_id` → 显示名称 | 名称 + (uid) |
| 事件类型 | `event_type` | trial_granted / sub_granted / sub_renewed / booster_purchased（中文映射「开通体验」/「开通 Pro」/「续费 Pro」/「购买加量包」） |
| 产品 | `product_type` | trial / monthly / booster |
| 月数或数量 | `months` 或 `quantity` | trial 显示「3 天」、monthly 显示「N 月」、booster 显示「N 份」 |
| 金额 | `amount_cents` | 分转元，¥X.XX 格式（千分位） |

#### §8.4.3 底部汇总

固定页脚展示：
- 总金额：`¥{sum} 元`（千分位）
- 事件数：`{count} 笔`
- 活跃父账户数：`{distinct_granter_count} 个`

汇总数据来自后端响应的 `summary` 字段，前端不在客户端计算（避免分页时口径漂移）。

#### §8.4.4 CSV 导出

按钮「导出 CSV」→ 调用 `GET /v1/admin/b2b-billing-report.csv?month=YYYY-MM&granter_user_id=...`。

- 编码：UTF-8 with BOM（Excel 打开中文不乱码）
- 文件名：`b2b-billing-{month}-{granter_id_or_all}.csv`
- 列与主表一致，但金额导出为元（不含 ¥ 符号）

实现：前端调接口拿 blob，通过 `URL.createObjectURL` + `<a download>` 触发下载，不通过 navigator.clipboard。

#### §8.4.5 4 状态

- loading：DataTable 骨架屏
- empty：本月无事件 → 中央展示「本月无 B2B 账单事件」+ icon
- error：toast + 重试按钮
- success：正常表格

---

### §8.5 时区与日期格式化

#### §8.5.1 通用规则

- 后端所有时间字段返回 ISO 8601 带时区字符串（如 `2026-05-15T00:00:00+08:00`）
- 前端**统一按 UTC+8 渲染**，不读取浏览器本地时区（避免国外用户看到 UTC 日期错位）

#### §8.5.2 推荐实现

**钉死：方案 A（dayjs + utc + timezone 插件）。** 项目已有 dayjs 依赖，无需新增。两个仓库共用相同 API 但各自封装一份 utils（保持仓库独立）：

- 用户端：`numind-web-v3/src/utils/datetime.ts`
- 管理端：`numind-admin-web/src/utils/datetime.ts`

```typescript
// src/utils/datetime.ts
import dayjs from 'dayjs'
import utc from 'dayjs/plugin/utc'
import timezone from 'dayjs/plugin/timezone'

dayjs.extend(utc)
dayjs.extend(timezone)

export const formatDate = (iso: string | null): string =>
  iso ? dayjs(iso).tz('Asia/Shanghai').format('YYYY-MM-DD') : '—'

export const formatDateTime = (iso: string | null): string =>
  iso ? dayjs(iso).tz('Asia/Shanghai').format('YYYY-MM-DD HH:mm') : '—'
```

> **不可使用**：原生 `Intl.DateTimeFormat`、`new Date(...).toLocaleDateString()`、moment.js。所有日期渲染必须经过 `formatDate` / `formatDateTime`，禁止在组件内部直接 import dayjs 自行格式化（避免格式漂移）。

#### §8.5.3 已知坑

- 直接 `new Date('2026-05-15').toLocaleDateString()` 会按浏览器时区渲染，**禁止**
- 后端如返回不带时区的字符串（如 `2026-05-15 00:00:00`），前端必须当作 UTC+8 强制 parse，不要让浏览器猜
- `null` 一律渲染为 `—`（em-dash），不要渲染空字符串或 `1970-01-01`

---

### §8.6 错误码 → i18n 文案映射

§4 PRD 错误码清单的 11 条 + §5.7 新增的 2 条 sentinel 全部覆盖。两套文案：用户端友好（暴露给 C 端 / 父账户）/ admin 端技术（admin 调试用，含上下文 ID）。

> **key 命名约定**：i18n key **严格对齐 §5.7 已锁定的 Go 常量名**（如 `ErrSelfPurchaseDisabled`、`ErrSubscriptionExpired`、`ErrIdempotencyKeyConflict`）。**禁止**使用点分式命名（如 `Membership.SelfPurchaseDisabled`）作为 i18n map 的 key——后者是后端响应中可选的 `code_text` 字段，不在前端字典内做映射。

#### §8.6.1 用户端友好文案（numind-web-v3）

```typescript
// src/i18n/credits-errors.ts
export const userErrorMap: Record<string, string> = {
  ErrSelfPurchaseDisabled:        '会员需由父账户为您开通，请联系您的管理员',
  ErrTrialAlreadyGranted:         '该账户已使用过体验包',
  ErrTrialNotAllowedForActivePro: '该账户已是 Pro 会员，无需开通体验包',
  ErrChildNotMember:              '该子账户暂未开通会员，无法购买加量包',
  ErrNotActiveMember:             '请先开通会员后再购买加量包',
  ErrBoosterQuantityExceedsLimit: '单次最多购买 10000 份',
  ErrInsufficientCredits:         '积分不足，请联系父账户开通会员或购买加量包',
  ErrSubscriptionNotFound:        '未找到会员记录',
  ErrSubscriptionExpired:         '会员已到期，请联系您的管理员续费',
  ErrInvalidProductType:          '请选择有效的产品类型',
  ErrInvalidMonths:               '会员月数必须在 1-12 之间',
  ErrParentChildRelation:         '账户关系无效，请联系客服',
  ErrIdempotencyKeyConflict:      '操作冲突，请刷新页面后重试',
}
```

#### §8.6.2 admin 端技术文案（numind-admin-web）

```typescript
// src/i18n/credits-errors.ts
export const adminErrorMap: Record<string, (ctx: any) => string> = {
  ErrSelfPurchaseDisabled:        () => '自购被拒：C 端不允许直接购买 Pro/trial',
  ErrTrialAlreadyGranted:         (ctx) => `trial 已存在，user_id=${ctx.user_id}`,
  ErrTrialNotAllowedForActivePro: (ctx) => `用户已有在期 Pro 订阅，user_id=${ctx.user_id}, expires_at=${ctx.expires_at}`,
  ErrChildNotMember:              (ctx) => `子账户非会员状态，child_user_id=${ctx.child_user_id}`,
  ErrNotActiveMember:             (ctx) => `账户无在期 trial/sub，user_id=${ctx.user_id}`,
  ErrBoosterQuantityExceedsLimit: (ctx) => `quantity=${ctx.quantity} 超过上限 10000`,
  ErrInsufficientCredits:         (ctx) => `余额不足：trial=${ctx.trial}, cycle=${ctx.cycle}, booster_usable=${ctx.booster}`,
  ErrSubscriptionNotFound:        (ctx) => `未找到 subscription，user_id=${ctx.user_id}`,
  ErrSubscriptionExpired:         (ctx) => `subscription 已过期：user_id=${ctx.user_id}, expires_at=${ctx.expires_at}`,
  ErrInvalidProductType:          (ctx) => `非法 product_type=${ctx.product_type}`,
  ErrInvalidMonths:               (ctx) => `非法 months=${ctx.months}（合法范围 1-12）`,
  ErrParentChildRelation:         (ctx) => `parent_user_id=${ctx.parent} 与 child_user_id=${ctx.child} 无父子关系`,
  ErrIdempotencyKeyConflict:      (ctx) => `Idempotency-Key 冲突：key=${ctx.key}，请求体与首次不一致`,
}
```

#### §8.6.3 调用约定

axios 响应拦截器统一抓 `code !== 0`，**用 `errno` 常量名（字符串形式）作为 map key**：

```typescript
// src/api/request.ts (新增)
import { userErrorMap } from '@/i18n/credits-errors'  // 用户端
// import { adminErrorMap } from '@/i18n/credits-errors'  // admin 端用 adminErrorMap

instance.interceptors.response.use(
  (resp) => {
    if (resp.data?.code && resp.data.code !== 0) {
      // 后端响应 schema（§5 锁定）：{ code: <数值码>, message: <中文兜底>, errno: <Go常量名 string>, ... }
      // 优先用 errno 字符串匹配字典；fallback 用后端 message 兜底
      const errnoKey: string | undefined = resp.data.errno
      const friendly = (errnoKey && userErrorMap[errnoKey]) ?? resp.data.message ?? '操作失败'
      // 抛给业务层 catch 显示 toast
      return Promise.reject({ ...resp, message: friendly })
    }
    return resp
  },
  ...
)
```

**严禁**：在业务组件里硬编码错误文案。所有 13 条错误码（§4 PRD 11 条 + §5.7 新增 2 条）必须走 map。

---

### §8.7 旧 UI 元素移除清单

切换到新会员体系后，以下旧 UI 元素必须从前端移除（一次性切换决策 Q5 不留并存期）。

> **S3 plan task 阶段必须先跑 grep 校准移除清单**：
> ```bash
> grep -rn 'user_tier\|monthly_sop_runs\|tier_expires\|userTier\|tierExpires\|monthlySopRuns' \
>   numind-web-v3/src numind-admin-web/src
> ```
> 把实际命中点逐一登记进 S3 plan 的移除 task；本节列出的文件名是基于代码结构的初步预估，**以 grep 实际结果为准**。漏 grep 直接按列表删除的，会被 S4 review 打回。

#### §8.7.1 numind-web-v3

**`src/views/CustomersView.vue`**（父账户客户管理页）：
- 移除 legacy tier rank 显示：`user.user_tier` 字段不再读取，对应「等级：standard」「rank=2」等列删除
- 移除「X / 20 次」运行次数显示：`monthly_sop_runs` 字段后端虽保留只读用于历史数据，**前端 UI 不再展示**
- 移除老 booster 购买入口（如 `<BuyOldBoosterButton>` 之类的旧组件，整体删除）

**`src/views/AccountView.vue`** / **`src/views/CreditsView.vue`**（用户中心 / 余额页）：
- 移除老的「我的等级 free/trial/standard/premium」徽章
- 移除「本月剩余 SOP 次数 X / 20」展示
- 移除老 booster 余额（90 天到期）的展示，整页改为 §8.1 三卡片

**`src/stores/user.ts`**：
- 移除 `userTier` / `tierExpires` / `monthlySopRuns` 字段（如有），改为仅保留 §8.1 BalanceDTO 衍生的状态
- legacy tier 相关 getter（如 `isTrialUser` / `isStandardUser` 用 user_tier 字段）删除，统一改用 `creditsStore.displayState`

#### §8.7.2 numind-admin-web

**`src/views/UsersView.vue`** 等管理用户列表页：
- 移除「admin tier 升级控件」（手动设置 user_tier 的下拉，不在本次范围）
- 移除老的「会员到期日」单字段展示（改为读 subscription.expires_at + trial_grant.expires_at）
- legacy tier rank 列删除

**`src/views/B2BBillingView.vue`**（如已有老版）：
- 全页废弃，重写为 §8.4 新口径

#### §8.7.3 验收

- 全局 grep `user_tier` / `userTier` / `monthly_sop_runs` / `monthlySopRuns`，所有命中点必须经过 review：
  - 后端只读字段保留（迁移历史展示用）→ 不读
  - 业务逻辑判断 → 改用 `HasActiveSubscription` / `HasActiveTrial`（来自后端 BalanceDTO）
- 全局 grep 老 booster 组件名（如 `OldBoosterCard`、`LegacyBoosterModal`）→ 必须不存在
- legacy tier 相关 i18n 文案（「免费用户」「试用版」「标准版」「高级版」字典 entry）保留中性文案，不再读 user_tier 字段决定渲染

---

> §8 前端契约结束。下一节 §9 进入数据迁移与切换流程。

---

## §9 验证策略（S5 输入）

> 本章节定义本次重构在 S5 自动验收阶段的验证清单。所有用例需在 S4 编码阶段同步产出代码或脚本，S5 阶段执行并产出验收报告。原则：**单元测试覆盖算法正确性 + 并发压测覆盖竞态 + 迁移演练覆盖数据一致性 + E2E 覆盖关键用户路径 + 浏览器 QA 覆盖 UI 体验 + 可观测性回归保证 trace 不退化。**

### §9.1 单元测试覆盖（Go 后端）

#### 9.1.1 测试目标与覆盖率

| 层 | 覆盖目标 | 覆盖率门槛 | 测试基础设施 |
|----|----------|-----------|--------------|
| `internal/numind/biz/credit/` | 算法正确性 + 业务规则短路 + 错误码 | ≥ 80%（行覆盖率） | mock IStore（参考项目现有 biz 层 mock 模式） |
| `internal/numind/store/credit.go` | 5 张新表 CRUD + UNIQUE 约束 + 锁行为 | ≥ 70% | in-memory SQLite + AutoMigrate（`newTestDB(t)` 模式，参考 `.claude/rules/database.md` GORM gotcha 章节）|
| `internal/numind/biz/payment/` | fulfillOrder 改写后的"订单 → 5 表写入"映射 | ≥ 80% | mock IStore + 手工构造 Order |
| `internal/numind/biz/b2b_billing/` | 月度账单 SQL 聚合 + 跨切换日双口径拼接 | ≥ 75% | in-memory SQLite + 预填 fixture |

> 测试运行：S4 阶段每 task 后跑 `go test ./...`（轻量），S5 阶段跑 `task test`（含 race detection + coverage 报告）。`task lint` 在 commit 前执行（参考 `.claude/rules/testing.md` §1）。

#### 9.1.2 biz 层测试用例清单

按算法 / 函数维度组织，每个算法 ≥ 3 case，关键算法（anchor、deduct）需覆盖完整边界。表驱动测试为主（参考项目命名规范 `TestFunctionName_Scenario`）。

##### 9.1.2.1 anchor_add_months（10 个边界 case，必须全过）

签名：`anchor_add_months(anchor time.Time, n int) time.Time`，规则 `day = min(anchor.day, days_in_month(target_year, target_month))`。

| case | anchor | n | expected | 说明 |
|------|--------|---|----------|------|
| AC-01 | 2026-01-31 | 1 | 2026-02-28 | 1/31 → 2 月仅 28 天，落 2/28 |
| AC-02 | 2026-01-31 | 2 | 2026-03-31 | anchor 不变，3 月有 31 日，恢复到 31 |
| AC-03 | 2026-01-31 | 3 | 2026-04-30 | 4 月仅 30 天，落 4/30 |
| AC-04 | 2026-01-31 | 4 | 2026-05-31 | 5 月有 31 日，恢复 |
| AC-05 | 2024-02-29 | 1 | 2024-03-29 | 闰年 2/29 → 3/29（普通月份正常 +1 月）|
| AC-06 | 2024-02-29 | 12 | 2025-02-28 | 闰年 2/29 → 次年非闰年 2/28 |
| AC-07 | 2026-03-15 | 6 | 2026-09-15 | 月中正常 +6 月 |
| AC-08 | 2026-12-31 | 1 | 2027-01-31 | 年跨界 |
| AC-09 | 2026-05-31 | 1 | 2026-06-30 | 31 日落 30 日：5/31 → 6/30（连续 31 日小月切换链 1） |
| AC-10 | 2026-07-31 | 1 | 2026-08-31 | 31 日恢复：7/31 → 8/31（大月相邻无回退，验证 anchor 不被前一次回退污染） |
| AC-11 | 2026-08-31 | 1 | 2026-09-30 | 大小月切换：8/31 → 9/30 |

测试名：`TestAnchorAddMonths_Boundaries`（表驱动）。

##### 9.1.2.2 GrantOrRenewSubscription 三场景（每场景 ≥ 3 子 case）

| 场景 | case | 前置状态 | 操作 | 期望 |
|------|------|----------|------|------|
| **new（新开通）** | NEW-01 | 子账户无 subscription 记录 | grant 1 月 | 创建 subscription，`first_started_at = current_started_at = now`，`expires_at = anchor_add_months(now, 1)`，`total_months_purchased = 1`，membership_event 1 条 `sub_granted` |
| | NEW-02 | 子账户曾有 subscription 但已过期 | grant 3 月 | `current_started_at = now`，`first_started_at` 不变（保留首次记录），`expires_at = anchor_add_months(now, 3)`，`total_months_purchased = 3`（重置） |
| | NEW-03 | 子账户从未存在 sub 行 | grant 12 月 | 同 NEW-01 但 n=12 |
| **renew in 期** | REN-01 | 1/31 开通 1 月（expires=2/28），2/15 续 1 月 | grant 1 月 | `current_started_at = 1/31` 不变，`first_started_at` 不变，`total_months_purchased = 2`，`expires_at = anchor_add_months(1/31, 2) = 3/31` |
| | REN-02 | 同 REN-01，再续 2 月 | grant 2 月 | `total_months_purchased = 4`，`expires_at = anchor_add_months(1/31, 4) = 5/31` |
| | REN-03 | 续费在 sub.expires_at 前 1 秒发起 | grant 1 月 | 应被视为在期续费（事务起点 ts < expires_at），不切到 NEW 路径 |
| **renew expired** | EXP-01 | 1/31 开通 1 月（expires=2/28），3/15 再开 | grant 1 月 | 走 NEW-02 路径：`current_started_at = 3/15`，`first_started_at` 保留 1/31，`total_months_purchased` 重置为 1 |
| | EXP-02 | 已过期 6 个月才开 | grant 3 月 | 同上但 n=3 |
| | EXP-03 | 在 expires_at 那一秒发起（边界） | grant 1 月 | 因半开区间 `[start, expires)` 严格判定，那一秒已过期，走 NEW 路径 |

测试名：`TestGrantOrRenewSubscription_NewUser` / `_RenewInPeriod` / `_RenewAfterExpired`。

##### 9.1.2.3 GrantTrial 校验顺序（EC-3/EC-4 短路链）

| case | 前置状态 | 期望返回 | 验证点 |
|------|----------|----------|--------|
| TRI-01 | trial_grant 表已有该 user 行（任何状态） | `ErrTrialAlreadyGranted` | 短路返回，不查 subscription |
| TRI-02 | trial_grant 无记录，subscription active | `ErrTrialNotAllowedForActivePro` | 校验顺序：先查 trial_grant 通过，再查 sub 触发 |
| TRI-03 | trial_grant 无记录，subscription 不存在 | success | 创建 trial_grant 行，membership_event 1 条 `trial_granted` |
| TRI-04 | trial_grant 无记录，subscription 已过期 | success | 同 TRI-03（已过期 sub 不阻断 trial） |
| TRI-05 | 校验顺序回归：构造同时满足 trial 已存 + sub active 的状态 | `ErrTrialAlreadyGranted`（先返回） | 严格按 EC-3/EC-4 顺序，不能先返回 sub 错误 |

测试名：`TestGrantTrial_AlreadyGranted` / `_ActiveSubBlocks` / `_FreshGrant` / `_ExpiredSubAllows` / `_ShortCircuitOrder`。

##### 9.1.2.4 ensureCurrentCycle 懒创建（≥ 4 case）

| case | 前置状态 | 操作 | 期望 |
|------|----------|------|------|
| CYC-01 | 用户在期会员，cycle 表无任何行 | ensureCurrentCycle | 创建第 0 期 cycle，`cycle_start = current_started_at`，`cycle_end = min(anchor_add_months(start, 1), sub.expires_at)`，`cycle_remaining = 2000` |
| CYC-02 | 用户已有当月 cycle | ensureCurrentCycle | 直接返回现有 cycle，不创建 |
| CYC-03 | 用户跨月：上月 cycle 存在但 cycle_end < now | ensureCurrentCycle | 创建下一期 cycle，`cycle_start = 上期 cycle_end`，`cycle_remaining = 2000`（新月度配额） |
| CYC-04 | sub 已过期 | ensureCurrentCycle | 返回 `ErrSubscriptionExpired`（或同等错误），不创建 cycle |
| CYC-05 | 跨月边界：1/31 开通 1 月（cycle_start=1/31, cycle_end 受 min(2/28, sub.expires_at)=2/28 约束） | ensureCurrentCycle (3/1) | 3/1 时 sub.expires_at=2/28 已过期，走 CYC-04（返回 ErrSubscriptionExpired，不创建新 cycle） |

测试名：`TestEnsureCurrentCycle_FirstTime` / `_AlreadyExists` / `_CrossMonth` / `_SubExpired` / `_CycleEndConstraint`。

##### 9.1.2.5 DeductCredits 优先级（≥ 5 case，覆盖 AC-6/AC-7/AC-8）

| case | 前置（trial/cycle/booster） | 扣 | 期望（trial/cycle/booster） | 备注 |
|------|------------------------------|-----|------------------------------|------|
| DED-01 | 200 / 2000 / 1200 | 250 | 0 / 1950 / 1200 | trial 用尽转 cycle |
| DED-02 | 0 / 2000 / 1200 | 1950 | 0 / 50 / 1200 | 仅扣 cycle |
| DED-03 | 0 / 50 / 1200 | 500 | 0 / 0 / 750 | cycle 用尽转 booster |
| DED-04 | 200 / 2000 / 1200 | 3500 | 0 / 0 / 100 | 三段连续扣 |
| DED-05 | 0 / 0 / 1200，sub 已过期 | 100 | 不变（返回 `ErrInsufficientCredits`） | booster 冻结跳过（AC-7） |
| DED-06 | 0 / 0 / 0 | 1 | 不变（`ErrInsufficientCredits`） | 全用尽 |
| DED-07 | 0 / 0 / 1200，sub 重新开通后 | 100 | 0 / 0 / 1100（cycle 懒创建后） | booster 自动解冻（AC-8）|
| DED-08 | 200 / 2000 / 0 | 200 | 0 / 2000 / 0 | trial 精确扣完 |

测试名：`TestDeductCredits_PriorityOrder` / `_BoosterFrozenWhenExpired` / `_BoosterUnfrozenAfterRenew`。

##### 9.1.2.6 GetMembershipState 三状态（≥ 3 case）

| case | 前置 | 期望 state | 说明 |
|------|------|-----------|------|
| STATE-01 | trial 在期 + sub 在期 | 子账户视角 `trial`，父账户视角 `trial+pro` 双标 | 用户端只显 trial（US-2），父账户端显双状态（US-6） |
| STATE-02 | trial 已过期 + sub 在期 | `pro` | 仅 sub |
| STATE-03 | trial 已过期 + sub 已过期 | `free` | 全过期 |
| STATE-04 | trial 在期 + sub 不存在 | `trial` | 仅 trial |
| STATE-05 | trial 边界：expires_at 那一秒 | `free`（半开区间） | 严格判定 |

测试名：`TestGetMembershipState_TrialPlusPro` / `_ProOnly` / `_AllExpired` / `_TrialOnly` / `_BoundaryExpiry`。

#### 9.1.3 store 层测试用例清单

##### 9.1.3.1 5 张新表 CRUD（每表 ≥ 4 case）

每张表（subscription / trial_grant / cycle / booster_balance / membership_event）覆盖：Create / Get / Update / Delete + 一个 query 用例（按 user_id 列表）。

| 表 | 关键 query 测试 |
|----|----------------|
| subscription | `GetActiveByUserID`（now < expires_at），`GetByUserIDIncludingExpired` |
| trial_grant | `ExistsByUserID`（无论状态） |
| cycle | `GetCurrentByUserID`（now ∈ [cycle_start, cycle_end)），`ListByUserIDDescending` |
| booster_balance | `GetByUserID`（单行） |
| membership_event | `ListByGranterAndMonth`，`GetByIdempotencyKey` |

##### 9.1.3.2 UNIQUE 约束行为

| 约束 | 测试用例 | 期望 |
|------|----------|------|
| `trial_grant.UNIQUE(user_id)` | 对同一 user_id 连续 INSERT 两行 | 第二行返回 MySQL Error 1062（duplicate）；in-memory SQLite 等同 UNIQUE constraint failed |
| `membership_event.UNIQUE(idempotency_key)` | 同 idempotency_key INSERT 两次 | 第二次失败 |
| `cycle.UNIQUE(user_id, cycle_start)` | 同 (user, cycle_start) INSERT 两次 | 第二次失败 |
| `booster_balance.UNIQUE(user_id)` | 一个 user 仅一行 | 第二次 INSERT 失败 |

##### 9.1.3.3 ON CONFLICT DO NOTHING + 重新 SELECT 行为

模拟 ensureCurrentCycle 并发场景的核心模式：

```go
func TestCycleStore_ConflictThenSelect(t *testing.T) {
    db := newTestDB(t)
    s := store.NewCreditStore(db)
    // Goroutine A INSERT (user=1, cycle_start=2026-04-01)
    require.NoError(t, s.UpsertCycleIgnoreConflict(ctx, &model.Cycle{...}))
    // Goroutine B INSERT same (user=1, cycle_start=2026-04-01) — should NOT error, NO row inserted
    require.NoError(t, s.UpsertCycleIgnoreConflict(ctx, &model.Cycle{...}))
    // Both should SELECT the same row
    cycA, _ := s.GetCurrentByUserID(ctx, 1)
    cycB, _ := s.GetCurrentByUserID(ctx, 1)
    assert.Equal(t, cycA.ID, cycB.ID)
    var count int64
    db.Model(&model.Cycle{}).Where("user_id=?", 1).Count(&count)
    assert.Equal(t, int64(1), count)
}
```

##### 9.1.3.4 SELECT FOR UPDATE 锁行为

由于 in-memory SQLite 锁语义与 MySQL InnoDB 不完全一致，此测试需在 docker-compose 起的 MySQL 8.0 容器中跑（CI 环境 + 本地可选）：

```go
func TestSubscriptionStore_SelectForUpdate(t *testing.T) {
    if testing.Short() { t.Skip("requires real MySQL") }
    // 起两个 goroutine，A 先 BEGIN + SELECT FOR UPDATE，B 也 SELECT FOR UPDATE
    // 验证 B 阻塞 ≥ A 持锁时间，A COMMIT 后 B 立即返回
}
```

#### 9.1.4 测试基础设施

```go
// internal/numind/store/credit_test_helpers.go
func newTestDB(t *testing.T) *gorm.DB {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.AutoMigrate(
        &model.Subscription{}, &model.TrialGrant{},
        &model.Cycle{}, &model.BoosterBalance{}, &model.MembershipEvent{},
    ))
    t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
    return db
}
```

参考 `.claude/rules/database.md` 的 GORM `default:true` bool gotcha：本次涉及 `subscription.is_active`（如有）、`booster_balance.is_frozen` 等 bool 字段需特别注意。每个 Create 路径需有"flag=false 应持久化为 false"的回归测试。

---

### §9.2 并发压测用例

> 本节验证 R2/R3/R4 风险（懒创建竞态 / lost update / 长事务死锁）的实战防御。每个用例给 Go test + goroutine + WaitGroup 实现思路。运行环境：docker-compose 起的 MySQL 8.0（in-memory SQLite 不能完整复现 InnoDB 锁语义）。

#### 9.2.1 用例 1：单用户 10 并发扣分

**目标**：验证 cycle 懒创建唯一 + 余额一致 + 无错误返回。

**前置**：
- user=1，sub.expires_at = now+30d
- cycle 表无任何行
- booster_balance = 0

**实现**：
```go
func TestConcurrent_SingleUserDeduct10(t *testing.T) {
    db := newMySQLTestDB(t) // docker-compose
    biz := credit.New(db)
    setupUserWithActiveSub(t, db, 1)

    var wg sync.WaitGroup
    var errCount atomic.Int32
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            if err := biz.DeductCredits(ctx, 1, 10, "trace-"+uuid.New().String()); err != nil {
                errCount.Add(1)
            }
        }()
    }
    wg.Wait()

    // 断言
    assert.Equal(t, int32(0), errCount.Load(), "no errors expected")
    var cycCount int64
    db.Model(&model.Cycle{}).Where("user_id=?", 1).Count(&cycCount)
    assert.Equal(t, int64(1), cycCount, "exactly one cycle created")
    cyc, _ := credit.GetCurrentCycle(ctx, 1)
    assert.Equal(t, 2000-100, cyc.CycleRemaining, "deducted exactly 100 (10 * 10)")
}
```

**验证点**：
- cycle 表只有 1 行（UNIQUE(user_id, cycle_start) 保证）
- cycle_remaining 精确 = 2000 - 100
- 无 goroutine 返回错误
- 重试上限 1 次（spec 锁定）：observe metrics
- **metric 上报基线断言**（与 §10.4.1 关键 metric 对齐）：测试结束读取 prometheus testutil 或 expvar 计数器，验证：
  - `cycle_lazy_create_conflict_count` ≥ 9（10 并发，1 个 INSERT 落库 + 9 个 ON CONFLICT 命中）
  - `deduct_success_count` = 10
  - `deduct_total_count` = 10
  - `cycle_lazy_create_conflict_rate = conflict / (conflict + insert) ≈ 90%`，但与 §10.4.1 ≤ 5% 阈值的对照口径不同（§10.4.1 是 prod 流量平均值，本测试是极端构造），需在测试注释中明确这一点

#### 9.2.2 用例 2：父账户 5 并发续费同一子账户

##### 9.2.2.1 子用例 2a：5 个不同 idempotency_key（验证 AC-16a）

**目标**：5 次续费各自生效，expires_at 累加 5 个月。

**实现**：
```go
func TestConcurrent_ParentRenew5DifferentKeys(t *testing.T) {
    db := newMySQLTestDB(t)
    biz := credit.New(db)
    setupChildWithSub(t, db, parentID=10, childID=1, expires=now+30d)

    var wg sync.WaitGroup
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            req := credit.GrantReq{
                ChildID: 1, ProductType: "monthly", Months: 1,
                IdempotencyKey: fmt.Sprintf("click-%d", idx),
            }
            _, err := biz.GrantOrRenew(ctx, parentID=10, req)
            assert.NoError(t, err)
        }(i)
    }
    wg.Wait()

    // 断言
    sub := getSubByUserID(t, db, 1)
    expected := anchorAddMonths(sub.CurrentStartedAt, 1+5) // 原 1 月 + 5 次续费
    assert.WithinDuration(t, expected, sub.ExpiresAt, time.Second)
    assert.Equal(t, 6, sub.TotalMonthsPurchased)

    var eventCount int64
    db.Model(&model.MembershipEvent{}).
        Where("child_user_id=? AND event_type=?", 1, "sub_renewed").
        Count(&eventCount)
    assert.Equal(t, int64(5), eventCount, "5 distinct events")
}
```

##### 9.2.2.2 子用例 2b：5 个相同 idempotency_key（验证 AC-16b）

**目标**：只生效 1 次，membership_event 只 1 条。

**实现**：
```go
func TestConcurrent_ParentRenew5SameKey(t *testing.T) {
    db := newMySQLTestDB(t)
    biz := credit.New(db)
    setupChildWithSub(t, db, parentID=10, childID=1, expires=now+30d)

    sameKey := "click-once"
    var wg sync.WaitGroup
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            req := credit.GrantReq{ChildID:1, ProductType:"monthly", Months:1, IdempotencyKey: sameKey}
            biz.GrantOrRenew(ctx, parentID=10, req) // err 可能是 ErrIdempotentReplay or nil，都接受
        }()
    }
    wg.Wait()

    sub := getSubByUserID(t, db, 1)
    expected := anchorAddMonths(sub.CurrentStartedAt, 1+1) // 原 1 月 + 1 次续费
    assert.WithinDuration(t, expected, sub.ExpiresAt, time.Second)
    assert.Equal(t, 2, sub.TotalMonthsPurchased)

    var eventCount int64
    db.Model(&model.MembershipEvent{}).
        Where("idempotency_key=?", sameKey).Count(&eventCount)
    assert.Equal(t, int64(1), eventCount, "exactly 1 event by UNIQUE constraint")
}
```

#### 9.2.3 用例 3：跨用户混合并发 grant/deduct

**目标**：验证锁顺序（user_id ASC + 表名字典序）无死锁。

**实现**：
```go
func TestConcurrent_MixedGrantDeduct(t *testing.T) {
    db := newMySQLTestDB(t)
    biz := credit.New(db)
    // 100 个不同子账户，每个有 active sub
    for i := 1; i <= 100; i++ {
        setupChildWithSub(t, db, parentID=10, childID=i, expires=now+30d)
    }

    var wg sync.WaitGroup
    for g := 0; g < 10; g++ {
        wg.Add(1)
        go func(gid int) {
            defer wg.Done()
            for u := 1; u <= 100; u++ {
                if (u+gid) % 2 == 0 {
                    biz.DeductCredits(ctx, u, 10, fmt.Sprintf("g%d-u%d", gid, u))
                } else {
                    req := credit.GrantReq{ChildID:u, ProductType:"monthly", Months:1,
                        IdempotencyKey: fmt.Sprintf("g%d-u%d-k", gid, u)}
                    biz.GrantOrRenew(ctx, 10, req)
                }
            }
        }(g)
    }

    done := make(chan struct{})
    go func() { wg.Wait(); close(done) }()
    select {
    case <-done:
        // 成功完成
    case <-time.After(60 * time.Second):
        t.Fatal("deadlock suspected: not all goroutines finished in 60s")
    }
}
```

**验证点**：
- 60 秒超时哨兵：若死锁则 fail
- 检查 InnoDB `SHOW ENGINE INNODB STATUS` 中无 `LATEST DETECTED DEADLOCK`（CI 后置脚本）
- 各用户最终 sub.expires_at 与扣分余额自洽

#### 9.2.4 用例 4：会员到期边界并发

**目标**：用户 A 在 sub.expires_at 那一秒发起扣分，同时父账户给他续 1 个月。验证最终状态一致。

**实现**：
```go
func TestConcurrent_ExpiryBoundaryDeductVsRenew(t *testing.T) {
    db := newMySQLTestDB(t)
    biz := credit.New(db)
    expiry := time.Now().Add(2 * time.Second).Truncate(time.Second)
    setupChildWithSubAt(t, db, parentID=10, childID=1, expires=expiry)
    setupCycleAt(t, db, userID=1, remaining=100)
    setupBoosterBalance(t, db, userID=1, balance=600)

    // 等到 expires_at - 50ms 时启动两个 goroutine
    waitUntil(expiry.Add(-50 * time.Millisecond))

    var wg sync.WaitGroup
    wg.Add(2)
    var deductErr, renewErr error
    go func() {
        defer wg.Done()
        deductErr = biz.DeductCredits(ctx, 1, 50, "race-deduct")
    }()
    go func() {
        defer wg.Done()
        req := credit.GrantReq{ChildID:1, ProductType:"monthly", Months:1, IdempotencyKey:"race-renew"}
        _, renewErr = biz.GrantOrRenew(ctx, 10, req)
    }()
    wg.Wait()

    // 最终状态可能两种合法分支：
    // 分支 A：deduct 在 expires_at 前到达 → 扣 cycle 50；renew 后 expires_at += 1m，cycle 仍存
    //          预期 snapshot：cycle_remaining=50, booster_balance=600（不动），
    //                         membership_event 含 1 条 deduct(amount=50, source=cycle) + 1 条 sub_renewed
    // 分支 B：deduct 在 expires_at 后到达 → ErrInsufficientCredits（booster 冻结，不扣）；
    //          随后 renew 创建新 sub period（走 NEW-02 路径），新 cycle 由下次扣分懒创建
    //          预期 snapshot：cycle 表无新行（renew 不预创建 cycle）, booster_balance=600（不动），
    //                         membership_event 仅 1 条 sub_granted（deduct 因失败不写 deduct 事件）
    sub := getSubByUserID(t, db, 1)
    bb := getBoosterByUserID(t, db, 1)

    // 不变量（两分支都成立）
    assert.True(t, sub.ExpiresAt.After(time.Now()), "after both ops, sub must be active")

    // 用 membership_event 累计扣减额作为分支判别（精确，避免 booster_balance 在两分支都=600 的歧义）
    var deductSum int
    db.Raw(`SELECT COALESCE(SUM(amount),0) FROM membership_event
            WHERE child_user_id=? AND event_type='deduct'`, 1).Scan(&deductSum)

    if deductErr == nil {
        // 分支 A
        assert.Equal(t, 50, deductSum, "branch A: cycle deducted exactly 50")
        cyc, _ := credit.GetCurrentCycle(ctx, 1)
        assert.NotNil(t, cyc, "branch A: cycle row exists")
        assert.Equal(t, 50, cyc.CycleRemaining, "branch A: cycle remaining = 100-50")
        assert.Equal(t, 600, bb.Balance, "branch A: booster untouched")
    } else {
        // 分支 B
        assert.ErrorIs(t, deductErr, errno.ErrInsufficientCredits, "branch B: deduct must fail with ErrInsufficientCredits")
        assert.Equal(t, 0, deductSum, "branch B: no deduct event recorded")
        var cycCount int64
        db.Model(&model.Cycle{}).Where("user_id=?", 1).Count(&cycCount)
        assert.LessOrEqual(t, cycCount, int64(1), "branch B: at most the pre-existing cycle row remains")
        assert.Equal(t, 600, bb.Balance, "branch B: booster frozen, untouched")
    }
    assert.NoError(t, renewErr, "renew always succeeds in both branches")
}
```

**验证点**：
- 不存在"扣了一半"的中间态
- sub 续费后处于 active
- booster 不在到期边界被错误扣减

---

### §9.3 迁移演练

> 验证 R1（段合并 bug）+ R6（一次性切换数据漂移）。配套 `scripts/2026-04-29-membership-credits-redesign/` 4 件套：dry-run.sql / apply.sql / verify.sql / rollback.sql。参考 `scripts/2026-04-24-legacy-tier-migration/` 模板。

#### 9.3.1 dry-run（生产快照）

**目的**：迁移前安全演练，不产生任何 DDL/DML 副作用，仅产出对比报表。

**输入**：从 prod 拉取 credit_package 表快照（脱敏后）+ user 表关联字段 → 灌入 staging MySQL。

**执行**：
```bash
# 1. 拉快照
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
    'mysqldump --single-transaction numind credit_package user customer > /tmp/snapshot.sql'
scp ... # 拉到本地
# 2. 灌入 staging
mysql -h staging-host < snapshot.sql
# 3. 跑 dry-run
mysql -h staging-host < scripts/2026-04-29-membership-credits-redesign/dry-run.sql > dry-run-report.txt
```

**对比报表内容**（dry-run.sql 输出）：
```
| user_id | pre_credit_package_total | predicted_5_table_total | diff | first_started_at_predicted | current_started_at_predicted | expires_at_predicted | total_months_purchased_predicted |
|---------|--------------------------|------------------------|------|----------------------------|-------------------------------|----------------------|-----------------------------------|
| 1       | 2000                     | 2000                   | 0    | 2026-01-15                 | 2026-01-15                    | 2026-04-15           | 3                                 |
| ...                                                                                                                                                                                          |
| TOTAL   | XXX                      | XXX                    | 0    |                            |                               |                      |                                   |
```

**通过条件**：所有用户 `diff = 0`，TOTAL 行 `diff = 0`。任何非零差额必须人工排查并固化为段合并算法的额外测试 case 后才能进 apply 阶段。

**演练记录格式**（产出到 `docs/migration-runbook/2026-04-29-dry-run-report.md`）：
- dry-run 输出表格（前 100 用户 + diff != 0 的全部）
- 总用户数、总积分、总 booster 数据量统计
- 段合并算法触发分支统计（连续段 / 不连续段 / 空段）
- 异常用户清单（如有）+ 排查结论

#### 9.3.2 apply（staging 全量演练）

**目的**：在与 prod 等规模的 staging 环境验证 apply.sql 完整性 + 性能。

**步骤**：
1. backup 表创建：`CREATE TABLE credit_package_backup_20260429 AS SELECT * FROM credit_package;`
2. 5 张新表 schema migration（idempotent，IF NOT EXISTS）
3. 段合并迁移 SQL：从 credit_package 按 user 聚合 → INSERT 到 5 张新表
4. 跑 verify.sql 对账
5. 测量 apply 耗时（spec 要求 ≤ 5 分钟，满足 maintenance window 要求）

**通过条件**：
- apply 耗时 ≤ 5 分钟（实际 prod 数据规模）
- verify.sql 输出所有用户 diff = 0
- 5 张新表行数符合预期（subscription 行数 = 历史 active+expired 用户数；trial_grant 行数 = 历史 trial 购买用户数；以此类推）

#### 9.3.3 rollback 演练（每月一次保持脚本可用）

**目的**：rollback.sql 不能写完就放着——3 个月后真的要回滚时发现 schema 已飘移会出大事。

**演练频率**：每月 1 日凌晨 2 点，自动跑一次 rollback 演练（CI cron）。

**承载位置**：`.github/workflows/rollback-drill.yml`（基于 numind-server 仓库），`schedule: cron: "0 2 1 * *"`。workflow 步骤：
1. 起 docker-compose MySQL 容器
2. 灌入 staging 脱敏快照
3. 跑 apply.sql → 模拟扣分 → rollback.sql
4. checkout 切换前 git tag 验证老代码可启动 + 跑通一次 SOP 扣分
5. 任一步失败 → 工作群通知（webhook），并自动 attach workflow log artifact

**步骤**：
1. 在 staging：apply → 触发模拟扣分（生成新 5 表数据）→ rollback
2. 验证：rollback 后 credit_package = backup 表内容；5 张新表为空（或保留 schema 但行被清空）；老代码（git checkout pre-migration-tag）能正常读 credit_package 跑 SOP

**通过条件**：
- rollback 耗时 ≤ 2 分钟
- 老代码版本能在 rollback 后启动并跑通一次 SOP 扣分

#### 9.3.4 演练记录归档

每次演练（dry-run / apply / rollback）产出报告，归档到 `docs/migration-runbook/`：

```
docs/migration-runbook/
├── 2026-04-29-dry-run-report.md
├── 2026-05-01-staging-apply-report.md  
├── 2026-05-15-rollback-drill-report.md  
├── 2026-06-15-rollback-drill-report.md  
└── 2026-XX-XX-prod-apply-report.md      # 真正 prod 切换的报告
```

每份报告字段：执行时间 / 执行人 / 输入数据规模 / 耗时 / verify 结果 / 异常 / 结论。

---

### §9.4 E2E 关键路径（Playwright）

> 5 条路径，覆盖 US-1 ~ US-7 中可被 E2E 自动化验证的关键场景。E2E 文件位于 `numind-web-v3/e2e/membership-credits-redesign.spec.ts`。登录凭据从 `$E2E_USERNAME` / `$E2E_PASSWORD` 读取（参考 `.claude/rules/testing.md` §2）。

#### 9.4.1 路径 1：父账户给子账户开通试用

**步骤**：
1. 浏览器访问 `$LOCAL_SITE_URL`（S5）/ `$DEV_SITE_URL`（S6）
2. 用 `$E2E_USERNAME` / `$E2E_PASSWORD` 登录父账户（已配置在 fixtures 中标记为 parent role）
3. 导航到 `/customers` 客户管理页
4. 找到一个 free 状态子账户（fixture 准备：`child_for_trial_e2e`）
5. 点击 action 菜单 → "开通会员" → 弹窗
6. 选择"开通试用"提交
7. 等待 toast 成功 + 列表刷新

**验证点**：
- API 调用：`POST /v1/users/children/:child_id/grant-membership` body `{product_type:"trial"}` → 200
- 数据库：trial_grant 表新增一行（granter_user_id = 父账户 id）
- 前端：列表行的"会员状态"列变为"试用中（YYYY-MM-DD 到期）"，蓝色标
- membership_event 表新增 1 条 `trial_granted`，idempotency_key 非空

**清理**：测试结束删除 trial_grant 行（恢复子账户状态）。

#### 9.4.2 路径 2：父账户给同一子账户开 1 月 Pro（trial+pro 叠加）

**前置**：路径 1 完成（子账户处于 trial 状态）。

**步骤**：
1. 父账户登录 → 客户管理页 → 同一子账户 → "开通会员"
2. 选 Pro，月数 = 1，提交
3. 等待 toast 成功

**验证点**：
- API：`POST /v1/users/children/:child_id/grant-membership` body `{product_type:"monthly", months:1}` → 200
- 数据库：subscription 表新增一行（first_started_at = current_started_at = now，total_months_purchased = 1，granter_user_id = 父账户）
- 前端父账户视角：列表行变为 "试用中 + Pro 已开通（试用 YYYY-MM-DD 到期 / Pro YYYY-MM-DD 到期）"，紫色双标
- 用户端视角：登出父账户、用 fixture 子账户凭据登录 → `/credits` 页只显示"试用中"（US-2 隐藏 Pro 标识）
- membership_event 新增 1 条 `sub_granted`

#### 9.4.3 路径 3：用户端登录购买 1 份 booster（含 mock 支付回调）

**前置**：子账户处于 trial 或 sub active 状态（路径 2 完成或独立 fixture）。

**步骤**：
1. 用子账户登录 `$LOCAL_SITE_URL`
2. 导航到 `/credits` 余额页
3. 点击"购买加量包"卡片 CTA
4. 弹窗中数量保留默认 = 1
5. 点击"购买"按钮
6. 监听网络：拦截 `POST /v1/orders` 响应（应包含 §5.2 锁定的 `pay_params.prepayid` 等字段）
7. 模拟支付成功回调（**实现见下方 mock 入口设计**）
8. 等待前端轮询余额刷新（≤ 10 秒）

> **mock 支付回调入口（S3 plan 阶段加 task 落地）**：
> 直接 `POST /v1/payment/wechat/notify` 在 prod 路径会被微信签名校验拒绝，且让 E2E 持有微信生产私钥不安全。S3 plan 必须二选一：
> - **方案 A（推荐）**：在 numind-server 增设 `POST /v1/admin/test-only/fulfill-order/:order_id`（admin token 鉴权 + 仅 dev/qa 注册，prod build 通过 `//go:build !prod` 编译标签或环境变量 gate 排除）。该端点直接调用 `biz/payment.fulfillOrder(orderID)` 跳过签名校验。
> - **方案 B**：在 `biz/payment/fulfillOrder` 加测试钩子，当 `os.Getenv("NUMIND_E2E_BYPASS_PAY_SIG")=="1"` 时跳过微信签名校验；对应环境变量仅在 dev/qa 注入，prod 永不设置。
>
> 任选其一，但必须在 spec / S3 plan 里明确记录所选方案，并在 §10 部署章节核查 prod 环境无该入口/无该 env。E2E 测试代码引用统一的 `mockPayOrder(orderId)` helper，未来切方案不需要改测试。

**验证点**：
- API：`POST /v1/orders` body `{product_type:"booster", quantity:1}` → 200，total_amount_cents = 2990
- 数据库：booster_balance 行 balance += 600
- 前端：余额卡片"加量包积分"数值更新为 +600（baseline + 600）
- toast：成功提示
- membership_event 新增 1 条 `booster_purchased`

#### 9.4.4 路径 4：用户买 booster 量超 10000（前端拦截 + 后端兜底）

**步骤**：
1. 子账户登录 → `/credits` → 购买加量包弹窗
2. 自定义输入框输入 `10001`
3. **前端检查**：输入框红框 + 行内错误"单次最多购买 10000 份"，提交按钮置灰
4. 假设用户绕过前端限制（用 Playwright 解除 disabled，强制点击）：
5. 监听网络：`POST /v1/orders` body `{product_type:"booster", quantity:10001}`
6. **后端检查**：返回 400 + `ErrBoosterQuantityExceedsLimit`

**验证点**：
- 前端 4 状态正确展示（error 状态：红框 + 错误文案）
- 后端兜底：即使前端被绕过，order 表无新订单创建
- 用户余额无变化

#### 9.4.5 路径 5：会员到期边界 booster 自动冻结 UI

**前置**：fixture 准备一个用户：sub.expires_at = now（即将到期或已过期），booster_balance.balance > 0。

**步骤**：
1. 该用户登录 → `/credits`
2. 等待页面渲染完成

**验证点**：
- 余额卡片"加量包积分"数值仍显示原值（数据未清零）
- 数值附近有锁形图标 + 灰色文字"需要开通会员后才能使用"
- 行内 CTA 按钮"立即开通会员" → 点击后跳转到联系父账户的提示页（C 端不可自购）
- 购买加量包卡片置灰 + 提示"开通会员后可购买"
- 后端调用 `POST /v1/orders booster` → 返回 `ErrNotActiveMember`（兜底）

#### 9.4.6 E2E 运行命令

```bash
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
    LOCAL_SITE_URL=$LOCAL_SITE_URL \
    npm run test:e2e -- membership-credits-redesign.spec.ts
```

#### 9.4.7 E2E 测试纪律

- 每条路径需独立 fixture，不依赖其他路径的执行结果（可任意顺序）
- 测试结束 cleanup 通过 admin API 重置 fixture 用户状态
- 失败时使用 `e2e/helpers/diagnose.ts` `createDiagnostics` 抓取 console + network + screenshot 上传 CI artifact

---

### §9.5 浏览器 QA（gstack /qa）

> 用于 S5 阶段的视觉与交互验收。AI 通过 gstack 浏览器自动操作（导航、点击、输入、截图），将关键页面与基线对照。

#### 9.5.1 何时跑

- **S5 自动验收阶段**：访问 `$LOCAL_SITE_URL`，跑完整 QA 清单
- **S6 部署 dev 后**：访问 `$DEV_SITE_URL`，跑回归 QA 验证 dev 环境一致

#### 9.5.2 覆盖清单

| 页面 | 关键验证点 | 截图基线 |
|------|-----------|----------|
| 父账户客户管理页 `/customers` | 子账户列表"会员状态"列双状态显示（trial+pro 紫色双标 / 仅 trial 蓝标 / 仅 pro 金标 / free 灰标） | `customers-list-with-states.png` |
| 用户端 `/credits` 余额页 - 在期会员 | 三类积分独立展示（试用 / 本月 / 加量包），布局符合 `@DESIGN.md` | `credits-active-member.png` |
| 用户端 `/credits` 余额页 - booster 冻结 | 冻结视觉处理（灰色 + 锁标 + CTA） | `credits-booster-frozen.png` |
| 用户端 booster 购买弹窗 | 1/5/10 快捷按钮 + 自定义输入框 + 实时总价 | `booster-purchase-modal.png` |
| 用户端 booster 购买弹窗 - 超限 | 输入 10001 时红框 + 错误文案 + 按钮置灰 | `booster-purchase-over-limit.png` |
| admin 端 B2B 月度账单页 `/b2b-billing` | 月份选择 + 父账户分组 + 事件明细展开 + 总计行 + CSV 导出按钮 | `b2b-billing-monthly.png` |

#### 9.5.3 基线管理

- **首次跑**：生成 baseline 截图，存到 `e2e/baselines/membership-redesign/`
- **后续回归**：gstack /qa 自动 diff，差异 > 阈值（默认 0.5%）→ 标记为视觉回归 bug
- **基线更新**：UI 改动是有意行为时，主控 AI 评估后用新截图覆盖 baseline，commit 时单独标注 "vision baseline updated"

#### 9.5.4 登录步骤

AI 用 gstack 浏览器导航到登录页 → 在用户名/密码框分别填入 `$E2E_USERNAME` / `$E2E_PASSWORD` → 点击登录 → 登录成功后再操作目标页面。所有 URL 与凭据从环境变量读取，禁止硬编码（参考 `.claude/rules/testing.md` §2.5）。

#### 9.5.5 调用方式

S5 阶段主控 AI 直接调用 gstack `/qa` skill，传入覆盖清单。skill 自动产出截图 + diff 报告 + 失败项清单。

---

### §9.6 可观测性验证

> 本功能不直接调 LLM，但 SOP 执行的扣分链路被本次重构改写。需保证 SOP 执行的 Langfuse trace 完整性不退化（参考 `.claude/rules/ai-service.md` §1）。

#### 9.6.1 回归测试目标

- SOP 执行流程的 Langfuse trace 包含：
  - 1 个 trace（操作名 = SOP 执行名）
  - ≥ 1 个 generation（每个 LLM 调用）
  - ≥ 1 个 span（prompt 构建 / 向量检索 / 后处理等非 LLM 子操作）
- DeductCredits 调用作为非 LLM 子操作，应作为 span 记录在 trace 内（or 作为 generation 的 metadata，二选一明确）
- LLM 调用必须在 OUT-OF-tx（spec R4 锁定）：trace 中 generation 的时间戳应位于 deduct 事务的 BEGIN 之外

#### 9.6.2 验证步骤

**步骤 1**：S5 环境跑一次完整 SOP（用 fixture 用户）
- 期望：用户配额扣减成功、SOP 输出正常

**步骤 2**：从 Langfuse 拉取该次 SOP 的 trace
- 通过 Langfuse API：`GET /api/public/traces?userId=<fixture-user>&limit=1`
- 或通过本地 dev Langfuse 实例 UI 检查

**步骤 3**：验证 trace 结构完整性
- trace.id 非空
- trace.userId = fixture user
- trace.tags 包含预期标签
- 至少 1 个 generation：`generation.model` ∈ 已注册模型清单（参考 `.claude/rules/ai-service.md` §2 提供商与模型清单），`generation.usage.promptTokens > 0`，`generation.usage.completionTokens > 0`
- spans 列表覆盖 prompt 构建 / retrieval / deduction（如设计为 span）

#### 9.6.3 deduction 改写后 trace 不退化

**对比基线**：S4 编码前跑一次 SOP，记录 baseline trace 结构（截图 + JSON dump 存到 `docs/migration-runbook/baseline-trace-pre-refactor.json`）。

**S5 阶段**：跑同样的 SOP，对比 trace JSON，差异点须解释清楚：
- 允许：trace 结构新增 span（如 deduction span）
- 禁止：generation 数量减少、span 总数减少（不应因重构丢失观测点）
- 禁止：generation.usage 字段缺失或 0

#### 9.6.4 实现路径建议

deduction 调用的 span 嵌入模式（在 biz/credit/credit.go 中）：

```go
func DeductCredits(ctx context.Context, userID uint, amount int, traceMeta string) error {
    if tc := langfuse.FromContext(ctx); tc != nil {
        spanID := langfuse.SpanID()
        langfuse.CreateSpan(tc.TraceID, spanID,
            langfuse.WithSpanParent(tc.ParentObservationID),
            langfuse.WithSpanName("credit-deduction"),
            langfuse.WithSpanInput(map[string]interface{}{"user_id": userID, "amount": amount}),
        )
        defer langfuse.EndSpan(spanID)
    }
    // ... actual deduction logic ...
}
```

**优雅降级**：所有 langfuse 调用包在 `if tc != nil` 内（参考 `.claude/rules/ai-service.md` §1 关键规则）。Langfuse 禁用时业务不受影响。

#### 9.6.5 异常路径 trace 验证

DeductCredits 返回 `ErrInsufficientCredits` 时，span 应正确记录 error output：

```go
if err != nil {
    if tc := langfuse.FromContext(ctx); tc != nil {
        // 在 EndSpan 前记录 error 到 span output
        langfuse.UpdateSpan(spanID,
            langfuse.WithSpanOutput(map[string]string{"error": err.Error()}),
            langfuse.WithSpanLevel("ERROR"),
        )
    }
}
```

S5 阶段构造一个故意失败的 SOP（用户余额不足）→ 验证 Langfuse 中 trace.level = ERROR + span output 含 error 字段。

---

### §9.7 验证策略汇总与 S5 执行清单

| 验证类型 | 工件 | S4 阶段产出 | S5 阶段执行 | 通过门槛 |
|----------|------|-------------|-------------|----------|
| 单元测试（biz） | `*_test.go` | 每个 task 后产出 | `task test` 全跑 | 覆盖率 ≥ 80%，全过 |
| 单元测试（store） | `*_test.go` | 每个 task 后产出 | `task test` 全跑 | 覆盖率 ≥ 70%，全过 |
| 并发压测 | `concurrent_test.go` | S4 末期 1 个 task 集中产出 | docker-compose MySQL 跑 4 个用例 | 4 个用例全过，无死锁 |
| 迁移 dry-run | `dry-run.sql` + report | S4 中期 | prod 快照灌 staging 跑 | 所有用户 diff = 0 |
| 迁移 apply | `apply.sql` + report | 同上 | staging 全量跑 | 耗时 ≤ 5min，verify 全过 |
| 迁移 rollback | `rollback.sql` | 同上 | staging 演练 | 耗时 ≤ 2min，老代码可启 |
| E2E（Playwright） | `*.spec.ts` | S4 末期 | `npm run test:e2e` | 5 条路径全过 |
| 浏览器 QA（gstack /qa） | 覆盖清单 | S5 即时执行 | gstack `/qa` skill | 6 个截图与基线一致或人工批准更新 |
| Langfuse 回归 | baseline trace JSON | S4 编码前抓 baseline | S5 抓新 trace 对比 | generation 数 ≥ baseline，无字段缺失 |

#### 9.7.1 S5 通过条件（NDF Gate）

S5 验收 Gate 必须满足：
- 单元测试：`task test` 全过 + 覆盖率达标
- 并发压测：4 个用例全过 + 无死锁
- 迁移演练：dry-run 报告 diff=0 + apply 演练通过 + rollback 演练通过
- E2E：5 条路径全过
- 浏览器 QA：6 个关键页面截图与基线一致（或差异已人工批准）
- Langfuse 回归：trace 结构无退化

任何一项不通过 → S5 阻塞，回到 S4 修复 → 重新 S5 验收（参考 `.claude/rules/ndf-enforcement.md` 规则 6/7）。

#### 9.7.2 高风险业务的回归保护诚实声明

本次重构涉及支付（订单与回调）、权限（B2B 父子关系）、会员等级（subscription/trial/cycle/booster），属于 `.claude/rules/ndf-enforcement.md` 规则 10 中明确要求 Playwright E2E 的高风险业务。**因此 §9.4 的 5 条 E2E 路径不可缩减，且需作为持久化测试代码进入仓库**。gstack `/qa` 仅作为视觉与交互的补充验证，不替代 E2E 的回归保护作用。

---

*S2 spec §9 验证策略 — 完*

---

## §10 部署与回滚

> 本章固化"一次性全量切换"决策（Q5）下的具体执行 SOP，覆盖部署流、切换日 runbook、回滚决策矩阵、监控告警四个子节。所有时间节点、操作步骤、决策阈值均作为硬规则写入，事故时按此 SOP 执行，不留人为犹豫空间。

---

### §10.1 maintenance mode 部署流

#### §10.1.1 maintenance mode 镜像构建

切换日的核心隔离机制是 **maintenance mode 镜像** —— 与正常版本同源，仅通过环境变量 `MAINTENANCE_MODE=true` 启用一个全局拒写中间件。它的目的是：在迁移脚本运行的 5–10 分钟窗口内，让用户的写请求得到一个明确的、可重试的拒绝响应（503 + Retry-After），而不是连接超时、数据写入半路、新老表互相污染。

**镜像构建要点**：

- 同一份代码 + Dockerfile，仅打两个 tag：`numind-server:vX.Y.Z`（正常版本）和 `numind-server:vX.Y.Z-maintenance`（环境变量预设版本）
- 保持镜像哈希在两个 tag 间几乎一致，方便回滚比对
- maintenance 镜像的环境变量在 deployment 配置层注入（不是 hardcode 到镜像），便于本地复现：`MAINTENANCE_MODE=true ./numind`

#### §10.1.2 maintenance 中间件行为

中间件挂在 `internal/pkg/middleware/maintenance.go`（新文件，本次随 spec 落地），对所有路由生效，位于 JWT 鉴权之前（不需要解析 token 也能拦截）。

**行为定义**：

- 读取启动时的环境变量 `MAINTENANCE_MODE`
- 对 `GET / HEAD / OPTIONS` 请求：直接放行（健康检查、监控探针、CORS 预检不能受影响）
- **支付回调路径硬豁免**（即使是 POST 也直接放行）：`/v1/payment/wechat/notify` / `/v1/payment/alipay/notify`。理由：(1) 支付平台不会因 maintenance 暂停推送；(2) 拒绝回调会导致用户支付成功但订单未 fulfill，必须人工对账补偿；(3) `payment` 表幂等性已保证（`out_trade_no` 唯一索引），即使迁移期间收到回调也不会污染新表（fulfillOrder 落表前会检查 idempotency）；(4) 真有不一致的极端窗口期，可由 T+0 ~ T+24h 高频对账（§10.2.4）发现并补救
- 对其它方法（`POST / PUT / PATCH / DELETE`）：直接返回 503，不进入 controller，不消耗任何 store 调用
- 503 响应必须携带 `Retry-After: 600` header（建议值 600 秒 / 10 分钟，足够覆盖最坏情况下的迁移 + 重启窗口）
- 响应 body 用统一格式：`{"code": 50301, "message": "系统维护中，请 5-10 分钟后重试", "data": null}`（新增错误码 `ErrSystemMaintenance` = 50301）

**Go 中间件伪代码**：

```go
// internal/pkg/middleware/maintenance.go
package middleware

import (
    "net/http"
    "os"
    "strconv"
    "strings"

    "github.com/gin-gonic/gin"
    "numind-server/internal/pkg/core"
    "numind-server/internal/pkg/errno"
)

var maintenanceEnabled = os.Getenv("MAINTENANCE_MODE") == "true"

// 支付回调路径白名单：maintenance 期间也必须放行（详见 §10.1.2 行为定义）
var paymentWebhookPrefixes = []string{
    "/v1/payment/wechat/notify",
    "/v1/payment/alipay/notify",
}

func isPaymentWebhook(path string) bool {
    for _, p := range paymentWebhookPrefixes {
        if strings.HasPrefix(path, p) {
            return true
        }
    }
    return false
}

func MaintenanceMode() gin.HandlerFunc {
    return func(c *gin.Context) {
        if !maintenanceEnabled {
            c.Next()
            return
        }
        // 支付回调硬豁免（即使 POST 也放行）
        if isPaymentWebhook(c.Request.URL.Path) {
            c.Next()
            return
        }
        // 健康检查 / 监控 / CORS 预检不拦截
        switch c.Request.Method {
        case http.MethodGet, http.MethodHead, http.MethodOptions:
            c.Next()
            return
        }
        // 拒绝其余写请求
        c.Header("Retry-After", strconv.Itoa(600))
        c.AbortWithStatusJSON(
            http.StatusServiceUnavailable,
            gin.H{"code": errno.ErrSystemMaintenance.Code,
                "message": "系统维护中，请 5-10 分钟后重试",
                "data": nil},
        )
    }
}
```

**注册位置**：`internal/numind/router.go` 和 `internal/numind/admin_router.go` 中，紧贴 `r.Use(gin.Recovery(), gin.Logger())` 之后、所有 JWT/Auth 中间件之前。

#### §10.1.3 部署顺序与每环境验证

部署严格按 dev → qa → prod 顺序，每环境验证通过才能进下一个：

| 环境 | 操作 | 验证清单 | 等待期 |
|---|---|---|---|
| **dev** (`49.233.219.254:9091`) | push develop 触发 CI/CD | (1) maintenance 中间件按预期返回 503 + Retry-After<br>(2) 迁移脚本 dry-run 输出 0 差异<br>(3) E2E 关键路径全部通过<br>(4) gstack `/qa` 浏览器 QA 通过 | ≥ 24 小时观察 |
| **qa** (`49.233.219.254:9093`) | push release 触发 CI/CD | (1) 同 dev 全部通过<br>(2) 并发压测：500 并发懒创建 cycle，UNIQUE 索引零冲突落库<br>(3) 父账户两 tab 续费幂等键测试通过 | ≥ 48 小时观察 |
| **prod** (`129.28.125.51:9095`) | tag v* 触发部署，按 §10.2 runbook 执行 | (1) 切换日 smoke test 通过<br>(2) 对账 SQL 0 差异<br>(3) 关键 API 99 分位延迟达标 | T+0 至 T+7d 持续观察 |

**强制等待期不可缩短**：dev 24h、qa 48h 是为了让任何隐性 bug 在低流量下显形（特别是 cycle 懒创建的偶发竞态、跨 cycle_end 边界扣减）。

#### §10.1.4 prod 切换 step-by-step

prod 部署不是简单的镜像替换，而是一个 5 步序列。每步都有明确的进入条件和退出条件：

| Step | 操作 | 进入条件 | 退出条件 | 监控指标 |
|---|---|---|---|---|
| **Step 1** | 部署 maintenance 镜像到 prod，等流量稳定 503 | qa 验证通过 + T-1 公告已发 + backup 表 schema 已建 | 503 比例稳定 ≥ 99%（约 30 秒） | 5xx 比例、503 数量 |
| **Step 2** | 跑 4 件套迁移脚本：dry-run → apply → verify | Step 1 出口 | dry-run 0 差异 + apply 完成 + verify SQL 0 差异 | 脚本 stdout、关键 user 抽样对账 |
| **Step 3** | 部署正常版本镜像（环境变量去掉 `MAINTENANCE_MODE`） | Step 2 出口 | 服务进程全部完成 rolling restart | pod 状态、版本号 |
| **Step 4** | 解除 maintenance（确认所有实例都不再带 maintenance env） | Step 3 出口 | 所有实例确认环境变量已切换 | 实例配置 diff |
| **Step 5** | smoke test 关键 API：`GET /v1/credits/balance` / `POST /v1/users/children/:id/grant-membership`（dry user） | Step 4 出口 | 所有 API 返回 2xx + 数据合理 | 响应码、响应体内容、延迟 |

**切换窗口估时**：5–10 分钟整体（脚本 2–5 分钟 + 服务重启 2–5 分钟）。前后留 5 分钟 buffer，对外公告 15 分钟维护窗口。

**关键约束**：

- 老 cron（`reconcileBillingMode` / `ActivatePending` / `ExpireActive`）在切换瞬间停止，由 Step 3 部署的新代码不再注册这些 cron 任务实现，而非手动 kill 进程
- 切换瞬间老 cron 全部摘除是为了避免新老表互相污染（老 cron 写 credit_package、新代码写 5 张新表）
- 任何 Step 失败 → 立即触发 §10.3 回滚决策矩阵

---

### §10.2 切换日 runbook（详细 step-by-step）

> 本节是切换日操作员手中的执行清单。每个时间点的操作、负责人、check 项都明确。打印此章节作为切换日手边参考。

#### §10.2.1 T-7 天：dev/qa 充分压测 + 迁移演练

| 项目 | 负责人 | 验收 |
|---|---|---|
| dev 环境完成 §10.1.3 全部验证 | 主操作员 | 24h 观察期无新增 bug |
| qa 环境完成并发压测：500 并发懒创建 cycle，零 UNIQUE 索引冲突落库 | 主操作员 | 压测脚本输出全 PASS |
| qa 环境跑迁移演练：从 prod 数据脱敏快照导入 qa，跑 4 件套脚本，dry-run + apply + verify + rollback 全流程 | 主操作员 | 迁移演练脚本 stdout 0 差异 |
| 对账 SQL 在 100 万级 membership_event 数据下查询 < 500ms | 主操作员 | EXPLAIN 走 `(granter_user_id, occurred_at)` 索引 |

**演练脚本必须真跑 rollback**，不能只跑前三步。rollback 没演练过 = 切换日真要回滚时 100% 出新问题。

#### §10.2.2 T-1 天：prod 准备 + 公告 + 排班

| 时间 | 操作 | 负责人 |
|---|---|---|
| 上午 10:00 | prod 准备 backup 表 schema：`credit_package_backup_<切换日 YYYYMMDD>`，与原表同 schema，预留空间 | 主操作员 |
| 上午 11:00 | 跑一次 prod dry-run（**不 apply**），输出每用户迁前后总余额对比表 | 主操作员 |
| 中午 12:00 | 确认 dry-run 输出 0 差异；任何差异 → 暂停切换，回到 spec 调整段合并算法 | 主操作员 + backup 观察员 |
| 下午 14:00 | 前端 banner 公告维护窗口：「莫小派将于 4/30 凌晨 03:00–03:15 进行系统升级，期间无法新建/修改数据，查询不受影响」 | 产品 |
| 下午 14:00 | 微信群通知所有 B2B 父账户：含维护时间 + 影响范围 + 联系方式 | 运营 |
| 下午 17:00 | 排班确认：1 主操作员（执行脚本）+ 1 backup 观察员（监控 + 决策回滚）；切换日凌晨 02:30 上线 | 主操作员 |
| 下午 18:00 | 准备好 §10.3 回滚决策矩阵打印件 + rollback.sql 路径快捷方式 | 主操作员 |

#### §10.2.3 T 时刻（凌晨 03:00 低峰期）

> 选择凌晨 03:00 是数据观测得出的活跃低谷期（< 10 QPS）。如果业务后续扩张到海外时区，需重新评估窗口。

| 时间 | 操作 | 详细说明 | 验收 |
|---|---|---|---|
| **03:00** | 部署 maintenance 版本（Step 1） | 触发 prod CD，注入 `MAINTENANCE_MODE=true` 环境变量；rolling restart 至全部 pod 切换 | pod 全部 ready；前端访问写接口返回 503 + Retry-After |
| **03:01** | 等流量 503 稳定 | 监控 5xx 比例（写请求）应 ≥ 99%；如果有 1% 仍 2xx → 排查是否所有 pod 都生效 | 5xx 写请求比例 ≥ 99% 持续 30 秒 |
| **03:02** | 跑迁移脚本（Step 2） | 命令：`./migrate.sh dry-run` → 检查输出 → `./migrate.sh apply` → 检查 → `./migrate.sh verify` → 检查 | 三步全部输出 0 差异；任一步差异立即触发 rollback |
| **03:07** | 部署正常版本（Step 3）+ 等 rolling restart 完成（Step 4） | 触发 prod CD，去掉 `MAINTENANCE_MODE` 环境变量；等待所有 pod rolling restart 完成 + 健康检查通过；同时确认老 cron 进程已停止（新代码不注册） | (1) 所有 pod 状态 = ready 且版本号显示新版（2）所有实例环境变量 diff 显示无 `MAINTENANCE_MODE`（3）`ps -ef \| grep cron_billing` 无残留 |
| **03:10** | smoke test（Step 5） | 用预先准备的测试账号跑：(1) `GET /v1/credits/balance` (2) 父账户 `POST /v1/users/children/:id/grant-membership` (trial) (3) 子账户消费扣减 (4) 父账户 `POST /v1/orders` 买 booster | 4 个 API 全部 2xx + 数据合理 |

> Step 3 与 Step 4 在 §10.1.4 表中是逻辑分步（部署 → 等待 + 校验），但实际操作上是连续动作：部署正常版本镜像后必须等到所有 pod 完成 rolling restart 且健康检查通过才算 Step 3 出口；而出口的判定本身就是 Step 4 的"确认所有实例环境变量已切换 + 老 cron 已停"。这两步在切换日 runbook 里合并执行，不再设独立时间点，避免 03:07 和 03:09 之间出现"已部署但未确认"的灰色区间。

**操作员执行原则**：

- 每一步等出口条件满足才进入下一步，不能并行
- 任何监控数字异常 → 暂停 + 询问 backup 观察员 + 视情况触发 §10.3
- 03:15 之前所有步骤必须完成；超过 03:15 仍未完成 smoke test → 立即触发 §10.3 回滚

#### §10.2.4 T+0 ~ T+24h：高频对账 + 监控

| 频率 | 操作 | 异常处理 |
|---|---|---|
| 每 5 分钟（前 30 分钟） | 监控关键 API 错误率、延迟、503 率 | 任何指标超阈值 → 触发 §10.4 告警 SOP |
| 每 30 分钟（前 6 小时） | 跑对账 SQL：抽样 100 个用户，迁前 credit_package 余额 vs 迁后 5 表合计 | 任何用户差异 ≠ 0 → 立即触发 §10.3 回滚 |
| 每 1 小时（前 24 小时） | 全量对账：扫所有用户的余额一致性 | 任何用户差异 ≠ 0 → 立即触发 §10.3 回滚 |
| 实时 | 监控错误日志（zap 输出，关键字 `ErrInsufficientCredits` / `ErrTrialAlreadyGranted` / SQL deadlock） | deadlock > 0 立即排查；其他错误码异常增长 → 排查业务 bug |

**Backup 观察员职责**：在主操作员监控之外，独立每 1 小时抽 5 个用户人工核对：账户中心积分余额展示 vs DB 实际记录。这是兜底防止"对账 SQL 本身有 bug"的双盲检查。

#### §10.2.5 T+1d ~ T+7d：每日对账 + 用户反馈监控

| 频率 | 操作 | 触发回滚条件 |
|---|---|---|
| 每日 10:00 | 跑全量对账 SQL | 任何用户差异 ≠ 0 → 评估 §10.3 T+24h ~ T+7d 窗口决策 |
| 每日 10:00 | 检查客服工单：按"积分"/"会员"/"扣分"关键字筛选 | 1 小时内 > 10 起类似投诉 → 触发 §10.4 告警 |
| 每日 17:00 | 检查 prod 错误日志聚合 | 错误率突变 → 触发 §10.4 告警 |

#### §10.2.6 T+7d：DROP credit_package（可后续 feature）

T+7d 之后 credit_package 表可以 DROP，但**强烈建议作为后续独立 feature 处理**，理由：

- 7 天内未爆出问题，意味着新表稳定可信，老表可以归档
- DROP 是不可逆的物理删除，应该走独立 NDF 流程（哪怕是 hotfix track）
- 归档前先做一次完整 dump 到对象存储（兜底数据保留 90 天）

如果运营/财务需要继续看历史账单，credit_package 表保持只读直到下个季度财报完成。

---

### §10.3 回滚决策矩阵

> 本节继承 proposal §6 的时间窗口分段，扩展为 runbook 级别的 SOP。事故时按此 SOP 执行，不留人为犹豫空间。

#### §10.3.1 触发条件清单

任何一条命中即进入回滚决策流程（**注意：进入决策流程 ≠ 立即回滚**，是否回滚由 §10.3.2 时间窗口矩阵决定）：

| 触发条件 | 阈值 | 检测方式 |
|---|---|---|
| **数据不一致**：每用户 invariant 检查失败 | 任意用户差异 ≠ 0 | 对账 SQL（每 1 小时） |
| **API 错误率超标** | > 5%（持续 5 分钟） | 监控指标 |
| **关键路径 E2E 失败** | grant / deduct / balance 任一不通 | 自动 E2E + 客服反馈 |
| **用户投诉激增** | 1 小时内 > 10 起类似问题 | 客服工单系统 |
| **SQL deadlock 持续** | 5 分钟内 > 5 次 | prod 错误日志 |
| **smoke test 失败** | 切换日 Step 5 任一 API 失败 | runbook 检查 |

任何触发条件命中后，主操作员**必须**：

1. 立即在工作群 @backup 观察员，通知触发条件
2. 执行 §10.3.2 时间窗口矩阵决策
3. 决策结果不可由单人独断，必须主操作员 + backup 观察员双方确认
4. 决策记录写入故障复盘文档（哪怕最终决定不回滚）

#### §10.3.2 回滚分段决策矩阵

| 时间窗口 | 默认决策 | 操作步骤 | 决策 SOP |
|---|---|---|---|
| **T+0 ~ T+24h** | **回滚优先** | 1. 部署 maintenance 镜像（重新进入维护窗口）<br>2. 跑 `rollback.sql`：从 backup 表恢复 credit_package + 删除新表迁移行<br>3. `git revert` 上线 commit + 推送 develop（或直接部署上一版本镜像）<br>4. 部署旧版本镜像（rolling restart）<br>5. 解除 maintenance + smoke test 老接口<br>6. 老 cron 自动恢复（旧代码注册） | 触发 → 双人确认 → 5 分钟内启动回滚；不需要审批，"事后复盘 > 事中犹豫" |
| **T+24h ~ T+7d** | **forward fix 优先** | 评估两条路径：<br>**路径 A（hotfix）**：仅代码 bug → 紧急修补丁 + 测试 + 发布<br>**路径 B（回滚）**：数据完整性已破坏，无法 hotfix → 接受 N 天数据丢失（订单、grant、扣减）→ 走完整回滚流程 | 触发 → 双人确认 → 评估路径 A 可行性（≤ 30 分钟）→ 路径 A 不可行才走路径 B；路径 B 需运营 + 产品 + CTO 三签 |
| **T+7d 之后** | **只能 forward fix** | 1. 立即冻结相关功能（如必要，临时禁用 grant/deduct）<br>2. 紧急 hotfix 或临时降级<br>3. 数据修复脚本（针对具体问题）<br>4. backup 表已归档清理，物理上无法整体回滚 | 触发 → 不再讨论回滚 → 直接进入 hotfix 流程；hotfix 失败接受功能临时不可用 |

**关键决策原则**（事故时遵循，不再讨论）：

- 回滚一旦执行 = 丢失"切换日至回滚日"期间所有用户产生的新数据；T+24h 内损失可控（用户活跃度低），超过 24h 损失急剧增加
- T+24h 是分水岭：之前回滚成本低，之后 forward fix 成本低
- T+7d 是物理截止：backup 表归档清理，回滚 = 删除付费记录 = 严重事故
- 任何回滚操作前必须再做一次"决策双确认"（双人独立决策一致才执行）
- 路径 B（24h-7d 回滚）需三签是**故意设计**——这一档区间反复回滚的代价巨大，必须有明确权责

#### §10.3.3 运维 checklist（每个时间窗口）

**T+0 ~ T+24h checklist**（默认回滚）：

- [ ] 双人确认触发条件成立
- [ ] 在工作群通报：「回滚启动 - 触发原因 - 预计影响时间」
- [ ] 部署 maintenance 镜像（5xx 比例稳定）
- [ ] 跑 rollback.sql，输出每用户恢复结果
- [ ] 验证 backup 表数据已恢复到 credit_package
- [ ] git revert 上线 commit（保留切换日 commit 在 history 中以便分析）
- [ ] 部署旧版本镜像
- [ ] 解除 maintenance
- [ ] smoke test 老接口（balance / grant 老 API）
- [ ] 验证老 cron 已恢复（启动后 5 分钟内有 cron 日志）
- [ ] 通知客服恢复正常服务
- [ ] 启动故障复盘流程（48h 内出复盘文档）

**T+24h ~ T+7d 路径 A checklist**（hotfix）：

- [ ] 双人确认触发条件成立 + 评估 hotfix 可行性
- [ ] 在 feature/hotfix-XXX 分支开发紧急补丁
- [ ] 通过 task lint + go test ./...
- [ ] 在 dev 环境验证 hotfix 解决问题
- [ ] 走快速 review（双人 review，跳过完整 NDF S2/S3）
- [ ] 部署 prod，监控指标恢复
- [ ] 触发 hotfix 后续完整 NDF 补流程（补 spec / plan）

**T+24h ~ T+7d 路径 B checklist**（回滚）：

- [ ] 双人确认 + 三签审批（运营 + 产品 + CTO）
- [ ] 公告用户：「系统将于 X 时间回滚，[切换日至回滚日] 期间产生的订单/积分变动将丢失，预计补偿措施为 Y」
- [ ] 走 T+0 ~ T+24h checklist 后续步骤
- [ ] 准备数据补偿方案（订单全额退款 / 积分人工补发）

**T+7d 之后 checklist**（只能 forward fix）：

- [ ] 双人确认问题严重程度
- [ ] 评估临时降级方案（如禁用 grant 直到修复完成）
- [ ] 紧急 hotfix 或数据修复脚本
- [ ] 走 NDF hotfix track 完整流程

---

### §10.4 监控与告警

> 监控是切换日的"眼睛"，告警是触发 §10.3 的输入。本节定义关键 metric、告警阈值、监控位置。

#### §10.4.1 关键 metric

| 类别 | metric | 目标 | 计算方式 |
|---|---|---|---|
| **业务成功率** | grant 成功率 | ≥ 99.5% | `POST /v1/users/children/:id/grant-membership` 200 占比（5 分钟滚动） |
| | 扣减成功率 | ≥ 99.5% | biz `DeductCredits` 业务成功占比（成功 / 调用总数，排除 `ErrInsufficientCredits` 这类预期失败） |
| **并发健康度** | cycle 懒创建并发冲突率 | ≤ 5% | `ON CONFLICT DO NOTHING` 后重新 SELECT 命中已存在行 / 总创建尝试 |
| | SQL deadlock 出现率 | ≤ 0.1% | prod 错误日志包含 "Deadlock found" 计数 / 总 SQL 调用 |
| **延迟 (99 分位)** | grant API | ≤ 500ms | API 响应时间 |
| | balance API | ≤ 200ms | API 响应时间（高频读） |
| | deduct biz 调用 | ≤ 300ms | biz 函数耗时（不含 LLM 调用） |
| **数据一致性** | 每用户余额 invariant | 100% | 对账 SQL：每用户 trial_grant + cycle + booster 总余额匹配 membership_event 累积 |

#### §10.4.2 告警阈值

| 严重度 | 触发条件 | 通知方式 | 响应时限 |
|---|---|---|---|
| **紧急（P0）** | grant 成功率连续 5 分钟 < 99% | 工作群 @所有人 + 短信主操作员 | 5 分钟内响应 |
| **紧急（P0）** | 扣减成功率连续 5 分钟 < 99% | 工作群 @所有人 + 短信主操作员 | 5 分钟内响应 |
| **紧急（P0）** | 任何用户对账 SQL 差异 ≠ 0 | 工作群 @所有人 + 短信主操作员 | 立即响应（→ §10.3 决策） |
| **警告（P1）** | API 99 分位延迟连续 5 分钟超阈值 1.5x | 工作群 @主操作员 + @backup | 30 分钟内响应 |
| **警告（P1）** | SQL deadlock 5 分钟内 > 5 次 | 工作群 @主操作员 | 1 小时内排查 |
| **信息（P2）** | 任何 SQL deadlock 出现 | 日志聚合记录 | 仅记录，不阻塞 |
| **信息（P2）** | cycle 懒创建并发冲突率 > 5% 但 < 10% | 工作群提醒 | 24 小时内分析 |

**告警升级规则**：

- 同一 P0 告警 5 分钟内未响应 → 自动升级，电话呼叫 backup 观察员
- 同一 P1 告警 30 分钟内未响应 → 自动升级为 P0
- 切换日（T+0 ~ T+7d）所有 P1 默认升级为 P0 处理优先级（窗口期不容忍延迟）

#### §10.4.3 监控位置

| 监控项 | 位置 | 备注 |
|---|---|---|
| **API metric**（成功率、延迟、QPS） | Grafana / 现有 Prometheus 实例（如有；否则用 nginx access log + 日志聚合替代） | 5 分钟滚动窗口；按 endpoint 分组 |
| **业务 metric**（grant 成功率、扣减成功率、cycle 冲突率） | 自打点：biz 层埋 metric 上报 + Grafana panel | 切换日前 T-3 天打点完成并跑通 |
| **SQL 健康**（deadlock、slow query） | MySQL slow_log + error_log | 切换日前确保 slow_log 阈值设为 1 秒 |
| **错误日志聚合** | prod logs（zap 输出） + 日志聚合系统 | 关键字订阅：`ErrInsufficientCredits`、`ErrTrialAlreadyGranted`、`Deadlock found`、`MAINTENANCE_MODE` |
| **业务对账**（数据一致性） | 定时任务跑对账 SQL → 输出报表到工作群 | 切换日 T+0 起每 30 分钟一次，T+1d 起每日一次 |
| **Langfuse trace** | 现有 Langfuse 实例 | 本功能不直接调 LLM，但下游 SOP 链路应保持 trace 完整；切换后抽样验证 SOP 执行的 trace 不退化 |

**监控 dashboard 准备**：

- T-7 天：在 Grafana 建一个专用 dashboard `Membership Credits Redesign - Switch Window`，集成本节所有 metric
- T-3 天：**打点验证脚本**——跑 `numind-server/scripts/2026-04-29-membership-credits-redesign/verify-metrics.sh`，自动构造 grant / deduct / cycle conflict 三类事件各 10 次，校验 Prometheus / Grafana 中对应 metric 计数器递增的差值与构造数量一致。脚本未通过 → dashboard panel 配置或 biz 层埋点有 bug，T+0 前必修
- T-1 天：测试 dashboard，确认所有 panel 数据流通
- T+0 切换日：dashboard 投屏到主操作员显示器，全程可视

**与 §9.2 用例 1 的对应**：§9.2 用例 1 在单元测试层验证 `cycle_lazy_create_conflict_count` 计数器递增，本节 verify-metrics.sh 在 prod-like 部署层验证同一计数器能从应用进程上报到 Prometheus 抓取端点。两层验证缺一不可。

#### §10.4.4 切换后基线观察

T+7d 通过后视为基线达成。后续作为 SLO 长期监控：

- grant 成功率 SLO ≥ 99.5%（月度）
- 扣减成功率 SLO ≥ 99.5%（月度）
- balance API p99 ≤ 200ms（月度）
- 任何破坏 SLO 的 incident → 走标准 NDF 流程修复

切换日积累的 metric 数据归档保存（≥ 90 天），作为后续类似切换的参考基线。

---

> §10 部署与回滚章节完。下一阶段（S3 plan）需将本章拆为独立 task：`maintenance 中间件实现` / `迁移脚本 4 件套` / `监控 dashboard 配置` / `切换日 runbook 演练`。
