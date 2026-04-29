# 会员积分体系重构 — 提案

## §1 方案概述 [客户可见]

### 解决什么问题

当前的会员积分系统是几个月前快速搭建的"双制并存"过渡架构。它能用，但已经积累了几个让人头疼的硬伤：

- **空档问题**：会员到期、月度积分刷新、续费这些时刻，系统依赖一个每小时跑一次的后台任务推进状态。后台任务挂掉一次，用户就会"会员显示已到期但 tier 还是 pro"或"续费成功但积分没到"。
- **状态机和到期日不同步**：用户的等级字段 (`user_tier`) 和到期日 (`tier_expires`) 是两个独立字段，理论上要靠后台任务对齐，实际经常出现"在期但显示过期"或反之的诡异中间态。
- **客服困扰**：每加一个新功能（B2B 父账户帮开会员、加量包、试用期），代码里就多一层 if 判断，bug 修起来越来越费劲。

### 改完什么样

我们把整个会员体系换成**业界主流 SaaS（Stripe / Apple Subscription）的标准模型**——"时间驱动 + 懒创建"：
- 用户的会员状态完全由"今天这一秒"和"到期日"实时算出来，不再需要后台任务推进
- 续费就是把到期日往后挪 N 个月，永远不创建第二条订阅记录
- 月度积分配额"用到才发"，从不预先发了等过期
- 加量包永不过期、可叠加购买，但只在会员状态下能用

### 用户感知的变化

| 场景 | 现在的体验 | 重构后的体验 |
|---|---|---|
| 续费 | 旧月卡和新月卡两条记录，余额展示混乱 | 到期日直接延后，余额无缝衔接 |
| 加量包 | 90 天到期，到期前焦虑使用 | 永不过期，会员状态下随用随扣 |
| 月度刷新 | 凌晨 cron 跑完才刷新（可能延迟） | 到点立即刷新，无空档 |
| 试用升级 Pro | 试用与正式期边界混乱 | 试用期内立即激活 Pro，无缝过渡 |
| 父账户管理子账户 | 看不到子账户完整会员状态 | 一眼看清"试用中 + Pro 已开通"叠加状态 |

### 客户可见的核心规则（与已锁定决策一致）

1. **三种积分**：试用包（200 / 3 天 / lifetime 单次）、Pro 月卡（2000 / 月）、加量包（600 / 不过期 / 仅会员可用）
2. **试用 + Pro 叠加**：试用期内可买 Pro，Pro 立即激活；扣分先扣试用、再扣 Pro 月度、最后扣加量包
3. **续费即延期**：到期日往后挪，不重置；过期再开则重新计时
4. **加量包永不过期**：会员状态下使用，会员到期暂时冻结余额，重新开通自动解冻
5. **不实现退款**、**不实现到期提醒**（本次范围外）

---

## §2 报价与周期 [客户可见]

> 内部重构项目，按工时估算，无对外报价。

| 阶段 | 工作内容 | 估时 |
|---|---|---|
| S2 spec 设计 | 5 张表 schema、API 契约、并发与锁策略、迁移脚本框架 | 2.5 天 |
| S3 plan 拆分 | 把 spec 拆成 10-14 个原子 task | 0.5 天 |
| S4 编码（后端） | schema migration + 5 表 model/store/biz 重写（共 9+ 文件）+ payment.go 改写 + cron 移除 + 段合并迁移 + 切换日双口径拼接 | 9-10 天 |
| S4 编码（前端 v3）| 余额接口适配 + booster 冻结提示 UI + 购买弹窗（含 1/5/10 + 自定义输入）+ 完整支付时序 | 2.5 天 |
| S4 编码（admin）| B2B 月度账单页改新口径 + 客户管理页双状态显示 | 1.5 天 |
| 数据迁移 | dry-run + 段合并算法 + 对账 SQL + 4 件套脚本（dry-run/apply/verify/rollback） | 3 天 |
| S5 自动验收 | E2E + 浏览器 QA + 并发压测 | 1.5 天 |
| S6/S7 部署 | dev → qa → prod 一次性切换（含 maintenance window） | 1 天（含等待 CI） |

**总计估时**：约 **20-22 个工作日**（不含人工验收等待时间），按 4-5 周排期较稳妥。

**交付时间线**：
- 假设 5 月 6 日开工 → S4 完成约 5 月 25 日 → S5/S6/S7 完成约 6 月 3 日（prod 上线）
- 部署策略：一次性全量切换（决策 Q5 锁定），含 5-10 分钟 maintenance window

---

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 现有模块/能力 | 如何复用 |
|---|---|
| `internal/numind/biz/credit/` Reserve/Reconcile 双阶段扣减 | 完全保留，是本次扣减算法的载体 |
| `internal/numind/biz/payment/payment.go` 微信/支付宝下单与回调 | 保留下单/回调框架，仅改写 `fulfillOrder` 的"订单 → 积分包"映射 |
| `internal/pkg/httpclient/` 连接池 + 重试 | 无关，不改 |
| `internal/pkg/errno/` 错误码体系 | 复用，新增 5-7 个新错误码 |
| `internal/pkg/log/` Zap 日志 | 复用 |
| `internal/numind/store/customer.go` 父子账户关系查询 | 复用（grant 路径仍需校验 parent-child） |
| GORM model 模式（model + TableName + tags） | 复用 |
| `internal/pkg/aiservice/` 路由层 | 无关 |
| Langfuse 追踪 | 无关（本功能不涉及 LLM 调用） |
| `scripts/2026-04-24-legacy-tier-migration/` 迁移脚本模板 | 高度复用——backup 表 + 事务包裹 + dry-run/apply/verify/rollback 四件套是本次新迁移的范本 |

### 技术风险

| 风险 | 缓解方案 |
|---|---|
| **R1：迁移段合并 bug** —— 若用户历史有不连续订阅段（开-停-再开），错误聚合会让用户白得免费时间或丢权益 | 段合并算法在 spec 中以伪代码 + 测试用例固化；dry-run 阶段全量比对每个用户迁移前后的"应享权益总和" |
| **R2：并发懒创建 cycle 竞态** —— 用户多端同秒扣分可能让 UNIQUE 索引报错或扣分失败 | ON CONFLICT DO NOTHING + 重新 SELECT FOR UPDATE 模式；spec 明确"重试上限 1 次"；S4 加压力测试用例 |
| **R3：续费 lost update** —— 父账户在两个 admin tab 同时点续费 | 改用 SQL 表达式（`UPDATE ... SET expires_at = ...`）配合幂等键，spec 层固定 |
| **R4：长事务超时** —— 扣分事务跨 trial/cycle/booster 三表 | spec 强制约定 LLM 调用 OUT-OF-tx；锁顺序固定 user_id ASC + 表名字典序 |
| **R5：B2B 账单口径切换** —— 历史账单与新账单口径不同 | 切换日前账单按老口径锁定，切换日后纯走 membership_event |
| **R6：一次性切换数据漂移** —— maintenance window 期间任何漏处理用户都会导致迁前后余额不一致 | maintenance window 内严格执行 4 件套脚本（dry-run/apply/verify/rollback）+ 每用户级对账 SQL；切换瞬间老 cron 全部摘除避免新老表互相污染 |
| **R7：MySQL `DATE_ADD INTERVAL N MONTH` anchor 行为不可靠** | 已锁定决策：anchor-restore 算法在 Go 应用层实现，SQL 只接收完整时间戳 |
| **R8：legacy 字段并行只读但代码读取分支没切干净** | spec 列出所有读 `user_tier` / `tier_expires` 的代码点，逐一改写为 `HasActiveSubscription` / `HasActiveTrial` |
| **R9：父账户客户管理页 UI 双状态显示设计** | S2 spec 阶段输出 wireframe，S3 plan 阶段拆出独立 task |

### 涉及仓库

- [x] **numind-server** —— schema 5 表 + biz/store 重写 + payment.go + cron 移除 + 迁移脚本
- [x] **numind-web-v3** —— 余额接口适配 + booster 冻结提示 + 续费体验
- [x] **numind-admin-web** —— B2B 月度账单页 + 客户管理页双状态

### AI 可观测性

- [ ] 涉及 LLM 调用：**否**（计费层重构，不触发 AI 调用）
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：N/A

注：本功能不直接调 LLM，但下游 SOP 执行时的扣分链路会被本功能改写——SOP 执行的 Langfuse trace 应当**正常保留**，spec 阶段需验证 deduction 改写后 trace 完整性不退化。

---

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

**US-1（C 端用户 - 普通运营场景）**：作为子账户用户，我需要在登录后看到"我的会员状态、当前积分余额、本月配额、加量包余额"，以便我能判断什么时候要让父账户帮我续费。

**US-2（C 端用户 - 试用升级场景）**：作为试用期用户，当父账户给我开通了 Pro，我需要立即拥有 Pro 的所有功能（包括购买加量包），但前端展示仍保留"试用中"状态直到试用期自然结束。

**US-3（C 端用户 - 加量包冻结场景）**：作为已过期会员，我需要看到我的加量包余额仍在账户里且明确标注"需要开通会员后才能使用"，以便我决定是续费还是暂时不用。

**US-4（C 端用户 - 自购加量包）**：作为在期会员，我需要能在前端一次性购买 1 份或多份加量包（比如一次买 3 份 = 1800 积分），购买后立即可用。完整支付时序：选份数 → 点击购买 → 微信/支付宝下单弹窗 → 用户支付 → 回调成功 → 前端 toast 成功并自动刷新余额；任何失败给行内重试 CTA + 客服指引；已支付但未到账（轮询 ≥ 30 秒未刷新）显示"订单处理中，请稍后刷新页面"。

**US-5（父账户 - 开通会员）**：作为父账户，我需要在"客户管理"页面对任意子账户做"开通试用"、"开通/续费 Pro 1-12 个月"的操作；试用项已使用过应自动置灰且提示"该账户已购买过试用包"。**API 不区分 grant 与 renew**——后端根据当前 subscription 状态自动判定（已过期/无记录 → 新开通，在期 → 续费延期），前端只需传 `product_type` + `months`。

**US-6（父账户 - 状态可视）**：作为父账户，我需要在客户管理列表里一眼看清每个子账户当前的完整会员状态（含试用 + Pro 叠加期）、到期日、cycle 当月剩余积分。**Booster 余额对父账户不可见**（隐私边界：父账户负责开通会员，booster 是子账户日常自助消耗，仅子账户本人和 admin 可见）。

**US-7（父账户 - B2B 月度账单）**：作为父账户，我需要在月底获得一份本月所有"开通/续费/购买加量包"动作的账单明细 + 汇总金额，按事件维度展示（哪天给哪个子账户开了什么、花了多少）。

**US-8（运营/财务）**：作为系统管理员（admin），我需要查询历史 grant 事件，按时间、按父账户、按事件类型做检索，以便处理客服争议或财务对账。

### 验收标准

#### 数据正确性

- [ ] **AC-1**：新建 subscription 时 `first_started_at` 与 `current_started_at` 同时被设置为 `now`；`expires_at` = `anchor_add_months(current_started_at, N)`
- [ ] **AC-2**：在期续费时 `current_started_at` 不变、`first_started_at` 不变、新 `expires_at` = `anchor_add_months(current_started_at, total_months_purchased + N)`，其中 `total_months_purchased` = 当前 sub 周期内已购买月数。**关键：anchor 锚点固定为 `current_started_at`，每次重算 expires_at 都从 anchor 出发**，避免反复 AddDate 累积漂移
- [ ] **AC-3**：过期再开时 `current_started_at` = `now`、`first_started_at` 不变、`expires_at` = `anchor_add_months(now, N)`；当前 sub 周期的 `total_months_purchased` 重置为 N
- [ ] **AC-4**：anchor-restore 算法测试覆盖 1/31 → 2/28 → 3/31 → 4/30 → 5/31 完整序列。算法签名：`anchor_add_months(anchor_date time.Time, n int) time.Time` —— 返回的日期 day 为 `min(anchor_date.day, days_in_month(target_year, target_month))`
- [ ] **AC-5**：trial_grant 表 UNIQUE(user_id) 强制 lifetime 单次；对已购买（任何状态）的用户重复 grant 返回 `ErrTrialAlreadyGranted`
- [ ] **AC-6**：扣减优先级测试：trial 200 + cycle 2000 + booster 1200，扣 250 后 → trial 0 / cycle 1950 / booster 1200；继续扣 1950 → trial 0 / cycle 0 / booster 1200；继续扣 500 → trial 0 / cycle 0 / booster 700
- [ ] **AC-7**：会员到期后扣减自动跳过 booster（即使 booster 余额 > 0，扣减返回 `ErrInsufficientCredits`）
- [ ] **AC-8**：会员重新开通后 booster 自动解冻可用
- [ ] **AC-9**：每月 cycle 在用户首次扣分时懒创建，并发请求场景下永远只创建一行（UNIQUE 索引保证）
- [ ] **AC-10**：subscription 过期则同期所有 cycle 余额作废（cycle.cycle_end 受 sub.expires_at 约束）
- [ ] **AC-11**：membership_event 任何写入都带 idempotency_key；重复请求不会重复入账

#### 接口与契约

- [ ] **AC-12**：`POST /v1/users/children/:child_id/grant-membership` 支持 product_type ∈ {trial, monthly}, months ∈ [1,12]，trial 不接受 months 参数
- [ ] **AC-13**：`POST /v1/orders` 创建 booster 订单时支持 quantity 字段（≥1，≤10000），订单总额 = `quantity × 2990`；超过 10000 返回 `ErrBoosterQuantityExceedsLimit`（决策 Q2 锁定）
- [ ] **AC-13b**：booster 总余额无上限（决策 Q4 锁定）；deduct 后余额可任意大。**反作弊与累计上限不在本次 scope 内**，记录到 backlog 后续评估
- [ ] **AC-13c**：用户处于"会员到期 / booster 冻结"状态时，前端购买入口禁用（按钮置灰）；后端兜底校验同 booster 使用门禁（无在期 trial/sub 则返回 `ErrNotActiveMember`）
- [ ] **AC-14**：`GET /v1/credits/balance` 返回新结构（trial_remaining / cycle_remaining / cycle_end / booster_total / booster_usable / membership_state）
- [ ] **AC-15**：`GET /v1/admin/b2b-billing-report?month=YYYY-MM` 改读 membership_event，返回事件级明细 + 父账户汇总

#### 性能与并发

- [ ] **AC-16a**：父账户两个 tab 同时点续费 1 个月，**两次请求携带不同 idempotency_key**（不同点击各自计算）→ 最终 expires_at += 2 个月；membership_event 留两条 sub_renewed
- [ ] **AC-16b**：同一次点击产生的请求被网络层重发（同一 idempotency_key）→ 最终 expires_at += 1 个月；membership_event 表只有一条 sub_renewed（UNIQUE on idempotency_key 阻止第二次写入）
- [ ] **AC-17**：用户客户端 0.5 秒内连发 5 次扣分请求，cycle 表里只有 1 行，扣减结果与单线程一致
- [ ] **AC-18**：B2B 月度账单 SQL 在 100 万条 membership_event 下查询响应 < 500ms（依靠 (granter_user_id, occurred_at) 索引）

#### 迁移与切换

- [ ] **AC-19**：迁移 dry-run 输出每个用户"迁前 credit_package 总余额"和"迁后 5 表合计余额"对比表，差额必须为 0
- [ ] **AC-20**：迁移 apply 后立即跑对账 SQL —— 每用户的 credit_package（迁前） vs 5 张新表（迁后）总余额、到期日、grant 来源必须 1:1 对应；任何差异立即触发 rollback
- [ ] **AC-21**：B2B 账单采用**切换日分界口径**（决策 Q3 锁定 - 选项 A）。切换日前历史账单永久锁定（老口径，扫 credit_package），切换日及之后账单走新口径（扫 membership_event）。**跨切换日的当月**账单（即上线月）需双口径拼接生成
- [ ] **AC-21a**：跨切换日当月账单**字段映射规则**：老口径 credit_package 行的 `grant_source/type/activated_at/total_credits × 单价` → 映射为新口径 membership_event 的 `event_type/product_type/occurred_at/amount_cents`。映射表在 spec 中固化为常量（如 `subscription package` → `sub_granted` event）
- [ ] **AC-21b**：跨切换日当月账单**去重规则**：老口径行（切换日前 activated_at）+ 新口径行（切换日后 occurred_at），按 `(granter_user_id, child_user_id, activated_at/occurred_at, product_type)` 复合键去重；若同时出现新老口径同一笔（极端：迁移瞬间），优先采纳新口径
- [ ] **AC-21c**：跨切换日当月账单**金额单位统一**：所有金额一律 `cents` (int64)，老口径若有 `decimal` 字段需 × 100 转 cents

#### 前端

- [ ] **AC-22**：用户端余额页 4 状态都正确渲染（loading / empty / error / success），文案与 UI 视觉对齐 `@DESIGN.md`
- [ ] **AC-23**：booster 冻结状态下，前端余额页显示余额 + 灰色"需要会员"标记 + 跳转开通会员的 CTA
- [ ] **AC-24**：父账户客户管理页 trial 选项，已购买过 trial 的子账户置灰且 hover 提示"该账户已购买过试用包"
- [ ] **AC-25**：admin 端 B2B 账单页支持月份选择，按父账户分组、按事件类型展开明细

### 边界情况

- **EC-1**：用户在 cycle_end 那一秒发起扣分 —— 半开区间 `[cycle_start, cycle_end)` 严格判定，不会扣到下个 cycle 也不会双扣
- **EC-2**：用户在 sub.expires_at 那一秒发起扣分 —— 事务起点固定 ts，全事务用同一个判断
- **EC-3 / EC-4 / EC-4b 校验顺序**（grant trial 路径，按下列顺序短路返回）：
  1. **先查 trial_grant 表**：若该子账户已有任何 trial_grant 行（任何状态）→ 返回 `ErrTrialAlreadyGranted`
  2. trial_grant 无记录 → 再查 subscription：若子账户当前有 active subscription（`expires_at > now`）→ 返回 `ErrTrialNotAllowedForActivePro`（决策 Q1）
  3. 都通过 → 创建 trial_grant 行
- **EC-5**：父账户 grant booster 给会员已过期/未开通的子账户 —— 拒绝并返回 `ErrChildNotMember`（subscription/trial_grant 任一在期才允许）
- **EC-6**：用户买 12 个月 Pro，从未登录使用 —— 不会预创建任何 cycle，直到第一次扣分时才懒创建当前月 cycle
- **EC-7**：cycle 边界精确语义（**S1 锁定，不留到 S2**）：
  - `cycle_end = min(anchor_add_months(current_started_at, cycle_index + 1), subscription.expires_at)`
  - 半开区间 `[cycle_start, cycle_end)` 严格判定
  - 例：1/31 开通 1 月 Pro，sub.expires_at = 2/28，cycle_index=0 的 cycle_end = min(2/28, 2/28) = 2/28；扣分 2/28 23:59:30 时 `now < cycle_end == false`（边界相等被排除）→ 此请求视为已过期，不再扣分
- **EC-8**：迁移期间用户发起扣分 —— 一次性切换决策 Q5 下，迁移期处于 maintenance window（5-10 分钟），所有写请求被拒绝（503）；切换完成后用户立即走新表
- **EC-9**：membership_event 写入失败但 subscription 写入成功 —— 同事务回滚；幂等键保证重试安全
- **EC-10**：用户买 trial + Pro 叠加期内退出账户登录又重新登录 —— 显示状态正常切换（free → trial → trial+pro 父视角 / trial 子视角）
- **EC-11**：用户处于 booster 冻结期，前端购买 booster 入口禁用；若用户绕过前端直接调 API → 后端返回 `ErrNotActiveMember`

### 错误码清单（S2 spec 阶段写入 errno 包）

| 错误码 | HTTP | 触发场景 | 引用 |
|---|---|---|---|
| `ErrSelfPurchaseDisabled` | 403 | C 端自购 trial/monthly Pro | 已存在 |
| `ErrTrialAlreadyGranted` | 409 | trial_grant 表已有该 user 行 | 新增 / EC-3 |
| `ErrTrialNotAllowedForActivePro` | 409 | grant trial 时该 user 已有 active subscription | 新增 / EC-4 / Q1 |
| `ErrChildNotMember` | 403 | parent grant booster 给非会员子账户 | 新增 / EC-5 |
| `ErrNotActiveMember` | 403 | C 端自购 booster 时无 active 会员（trial/sub 均无） | 新增 / EC-11 / AC-13c |
| `ErrBoosterQuantityExceedsLimit` | 400 | booster 单笔订单 quantity > 10000 | 新增 / Q2 / AC-13 |
| `ErrInsufficientCredits` | 402 | 三类积分合计扣减不足以覆盖请求量 | 已存在 |
| `ErrSubscriptionNotFound` | 404 | 查询 / 操作 subscription 但无记录 | 新增（兜底） |
| `ErrInvalidProductType` | 400 | grant 或 order 传了非法 product_type | 已存在（复用） |
| `ErrInvalidMonths` | 400 | grant Pro 时 months ∉ [1,12] | 已存在（复用） |
| `ErrParentChildRelation` | 403 | parent_user_id 与 child_user_id 不构成关系 | 已存在（复用） |

完整错误码 + Go 常量名 + i18n 文案在 S2 spec 中固化。

### 权限规则

| 操作 | 用户端可调 | 父账户可调（B2B） | Admin 可调 |
|---|---|---|---|
| 自购 trial | ❌ ErrSelfPurchaseDisabled | — | — |
| 自购 monthly Pro | ❌ ErrSelfPurchaseDisabled | — | — |
| 自购 booster（含多份）| ✅ 需会员状态 | — | — |
| Grant trial 给子账户 | — | ✅ 1 次 lifetime | ✅ 同 |
| Grant Pro 给子账户（开通+续费一体）| — | ✅ 1-12 月 | ✅ 同 |
| Grant booster 给子账户 | — | ✅ 子账户需会员 | ✅ 同 |
| 查询自己完整余额（含 booster） | ✅ | — | ✅ 任意用户 |
| 查询子账户余额（**不含 booster**） | — | ✅ 仅子账户 | — |
| 查询子账户 booster 余额 | — | ❌ | ✅ |
| 查询 B2B 月度账单 | — | ✅ 自己的账单 | ✅ 全部账单 |

### UI 行为规格

#### 用户端（numind-web-v3）

**页面位置**：账户中心 → 我的积分（路径 `/credits` 或 `/account/credits`）

**布局**：纵向卡片堆叠
- 卡片 1：当前会员状态（free/trial/pro，徽章式展示）+ 到期日
- 卡片 2：积分余额（试用积分 + 本月积分 + 加量包积分，分别独立展示）
- 卡片 3：购买加量包入口（仅会员可见，否则灰显并提示"开通会员后可购买"）
  - 数量选择（决策 Q2 锁定）：横向 3 个快捷按钮 `1 份` / `5 份` / `10 份` + 自定义数字输入框（默认 1）
  - 验证规则：必须为正整数；输入超过 10000 时输入框红框 + 行内错误"单次最多购买 10000 份"，禁用提交按钮
  - 实时显示总价：`{quantity} × ¥29.9 = ¥{total}`

**交互模式**：
- 余额数据每页加载时拉一次，不轮询
- booster 冻结状态：余额数字灰色 + 锁标 + 文案"需要开通会员后才能使用"+ 行内 CTA 跳转开通

**状态处理**：
- loading：骨架屏（`@DESIGN.md §7` 组件）
- empty：从不出现（用户必有 free 状态）
- error：toast 提示 + 重试按钮
- success：正常渲染

#### 父账户客户管理页（numind-web-v3）

**位置**：客户管理（路径 `/customers`）

**列表新增列**：会员状态（综合显示）
- "免费用户"（灰）
- "试用中（YYYY-MM-DD 到期）"（蓝）
- "试用中 + Pro 已开通（试用 YYYY-MM-DD 到期 / Pro YYYY-MM-DD 到期）"（紫色双标）
- "Pro 会员（YYYY-MM-DD 到期）"（金）

**操作菜单**：原有"开通会员"按钮弹出操作弹窗
- Trial 选项：已购买过则置灰，hover 提示
- Pro 选项：选月数 1-12，复用现有 UI
- 提交后调 `POST /v1/users/children/:child_id/grant-membership`

#### Admin 端（numind-admin-web）

**B2B 月度账单页**（路径 `/b2b-billing`）

**布局**：
- 顶部：月份选择器（默认本月）+ 父账户筛选（可选）
- 主表：父账户分组，点击展开看本月所有事件明细
- 每行事件：日期 / 子账户 / 事件类型（开通/续费/加量包）/ 产品 / 月数或数量 / 金额
- 底部：本月总计（金额、事件数、活跃父账户数）

**导出**：支持 CSV 导出整月账单（财务对接用）

---

## §5 关键决策记录（2026-04-29 全部锁定）

| # | 主题 | 决策 |
|---|------|------|
| Q1 | 已在期 Pro 用户能否再被 grant trial | **不允许**，返回 `ErrTrialNotAllowedForActivePro`；trial lifetime 单次由 trial_grant 表 UNIQUE on user_id 强制 |
| Q2 | 单次购买 booster 上限 | 单笔订单 quantity 上限 **10000 份**；前端 1/5/10 快捷按钮 + 自定义输入框；超过 10000 才报错 |
| Q3 | B2B 账单口径切换 | **选项 A：切换日分界**。切换日前历史账单永久锁定（旧口径扫 credit_package），切换日及之后走新口径（扫 membership_event）；跨切换日的当月账单需双口径拼接 |
| Q4 | 单用户 booster 总余额上限 | **不设上限** |
| Q5 | 灰度策略 | **一次性全量**，不分阶段。部署前 dev/qa 充分压测，回滚靠 rollback.sql + git revert |

## §6 部署与回滚

### 部署节奏（一次性全量）

1. **dev 环境**：S5 验证通过后立即部署，至少 24 小时观察期
2. **qa 环境**：dev 稳定后部署，跑 E2E + 浏览器 QA + 并发压测
3. **prod 环境**：qa 通过 + 用户确认 → 打 tag 触发部署
4. 部署当天**全量切换**，所有用户瞬间走新代码、新表
5. 切换瞬间**老 cron 停止运行**（reconcileBillingMode / ActivatePending / ExpireActive 全部摘掉）
6. 切换后 7 天保留 credit_package 表只读访问（应急对账用），7 天后视情况 DROP

### 数据迁移与切换原子性

切换日的执行顺序（写入 spec 阶段固化）：
1. **T-1 天**：dev/qa 充分验证，prod 下毛玻璃公告"维护窗口"
2. **T 时刻**（建议凌晨低峰期）：
   - Step 1：service maintenance mode（拒绝写请求 5-10 分钟）
   - Step 2：跑迁移脚本（4 件套：dry-run / apply / verify / rollback），把 credit_package 数据按段合并算法写入 5 张新表
   - Step 3：跑对账 SQL（每用户迁前 vs 迁后总余额必须 0 差异）
   - Step 4：服务重启（新代码 + 新表生效，老 cron 不再启动）
   - Step 5：解除 maintenance mode
3. **T+0 ~ T+7 天**：observation period，每日跑对账 SQL；任何异常立即触发 rollback
4. **T+7 天**：DROP credit_package 表（可延后到下个 feature 处理）

### 回滚方案

回滚决策按时间窗口分段（事故时按此 SOP 执行，不留人为犹豫）：

| 时间窗口 | 默认决策 | 操作步骤 |
|---|---|---|
| **T+0 ~ T+24h** | **回滚优先** | 1. 触发 rollback.sql：从 backup 表恢复 credit_package + 删除 5 张新表的迁移行<br>2. git revert 上线 commit<br>3. 重启服务（老代码 + 老表）<br>4. 老 cron 自动恢复 |
| **T+24h ~ T+7d** | **forward fix 优先** | 评估：是否仅代码 bug？是 → 紧急修补丁 + hotfix 上线；否（数据完整性已破坏）→ 回滚但接受 N 天数据丢失，**需运营/产品双签审批** |
| **T+7d 之后** | **只能 forward fix** | 此时累积新数据已无法回灌老表，回滚等同删除用户付费记录；无论何种 P0 都只能向前修复 |

**关键原则**：
- 回滚一旦执行 = 丢失"切换日至回滚日"期间用户产生的新数据（订单、grant、扣减），属严重事故
- T+24h 内回滚相对安全（用户活跃度低、影响小）；超过 24h 强烈倾向 forward fix
- T+7 天后 backup 表归档清理，物理上无法回滚

---

## §7 附录

- 设计完整脉络：本次 session 对话记录（含两轮 ultrathink 分析、两次并行 subagent review）
- S0 需求卡：`numind-server/requirements/membership-credits-redesign.md`
- 历史迁移参考：`scripts/2026-04-24-legacy-tier-migration/`（4 月 24 日已完成 24 个 legacy 用户迁移）
- 受影响的现有代码点（spec 阶段精确化）：
  - `internal/numind/biz/credit/credit.go` 重写
  - `internal/numind/biz/credit/grant_membership.go` 重写
  - `internal/numind/biz/payment/payment.go` 改写 `fulfillOrder` + 移除 tier rank 判断
  - `internal/numind/biz/credit/cron_billing.go` 删除（无 cron）
  - `internal/numind/biz/b2b_billing/b2b_billing.go` 改读 membership_event
  - `internal/numind/biz/sop/sop.go` `CheckSopPermission` 改用新接口
  - `internal/pkg/model/credit.go` 新增 5 个 model
  - `internal/numind/store/credit.go` 新增 5 个 store
  - `internal/numind/router.go` / `admin_router.go` 路由增改
  - 前端：`numind-web-v3/src/api/credits.ts` / `stores/credits.ts` / `views/CustomersView.vue` / 新增 booster 购买弹窗 / 新增 BalanceCard 组件
  - admin：`numind-admin-web/src/views/B2BBilling.vue`（新页面）
