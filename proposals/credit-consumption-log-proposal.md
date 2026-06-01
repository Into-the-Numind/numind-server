# 额度消耗记录（Credit Consumption Log） — 提案

> S1 工件。对应需求卡片 `requirements/credit-consumption-log.md`。Standard 轨道。

## §1 方案概述 [客户可见]

给**所有账户**在**设置页**新增一个「**额度消耗记录**」入口（一行可点击的设置项，放在「积分与加量包」区块下方）。点击弹出一个**弹窗**，里面是当前登录用户自己的积分消耗流水：

| 时间 | 动作 | 消耗积分 |
|------|------|---------|
| 06-01 14:32 | SOP 执行 | 18 |
| 06-01 11:05 | 销售对话 | 6 |
| 05-31 22:14 | SOP 执行 | 23 |
| … | … | … |

- **每条 = 一次真实动作**：一次 SOP 执行 / 一次销售对话 = 一行，展示「做了什么」「什么时候」「花了多少积分」。
- **只展示「平账后的真实记录」**：系统的扣费是「先按估算预扣 → 完成后按实际用量对账多退少补」两阶段；本列表**只展示对账完成（reconciled）后的最终真实扣费**，不展示预扣估算的中间态，所以用户看到的每个数字都是**真实结算值**。
- 列表按时间倒序，支持翻页。与现有「余额」展示互补——余额回答「还剩多少积分」，本功能回答「积分花在哪了」。

## §2 报价与周期 [客户可见]

- 预估工作量：**1.5 ~ 2 天**（后端 0.5 ~ 0.75 天：新增一个只读查询接口；前端 1 ~ 1.25 天：设置页入口 + 弹窗 + 复用 DataTable）
- 报价：内部功能，无对外报价
- 交付时间线：S2 设计 → S3 计划 → S4 编码 → S5 本地验收 → S6 dev 验收，按 NDF 节奏推进

## §3 技术可行性 [AI 内部]

### 关键设计决定：数据源用 `credit_reservation`（不是聚合 credit_transaction）

S1 调研推翻了需求卡片里「聚合 credit_transaction 行」的初步设想，找到了更干净的数据源：

- **`credit_reservation` 表就是「每动作一行」的天然 SOT**（`internal/pkg/model/credit_reservation.go:10`，model 注释明确：「一次 LLM 调用级操作会生成一条 Reservation，Finalize 时切换终态」）。字段恰好覆盖需求三要素 + 口径：
  - `Operation`（动作：`sop_run` / `salesrag_chat` / `agent_test` 等）
  - `CreatedAt`（动作时间，毫秒精度）
  - `Status` 枚举 `reserved → reconciled | refunded | expired` —— **「平账后」= `status='reconciled'`**
  - `ReservedCredits` + `Delta`（→ 净真实扣费 = `ReservedCredits + COALESCE(Delta,0)`，多退少补后的最终值）/ `ActualCostCents` / `ReconciledAt`
  - `UserID`（已有 `idx_user_status` 复合索引，查询高效）
- **为什么不用 credit_transaction 聚合**：`DeductCreditsTx`（`biz/membership/cycle.go:273-350`）写 credit_transaction 行时**只设** `operation`（如 `reserve:sop_run`），**不写** `reservation_id` / `biz_ref`（这些字段在 model 里存在但该路径留空）。所以 credit_transaction **缺少把同一次动作的 reserve 行 + reconcile 行归并起来的稳定 grouping key**，方案 A 无法在 credit_transaction 上干净实现。`credit_reservation` 没有这个问题——它本来就是一行一动作。

→ **方案 A（用户 S1-D2 已选）= 查 `credit_reservation WHERE user_id=? AND status='reconciled' ORDER BY created_at DESC`**，天然一动作一行、天然只含平账后真实记录、天然有动作名和时间。零 schema 变更。

### 现有功能复用

- **store 层**：`internal/numind/store/credit.go` 已有 credit 系列查询（`ListTransactionsByUser` 等），但**无 reservation 的 list-by-user 方法**（只有 `SumByReservationIDs`，credit.go:250）。需**新增一个只读 store 方法** `ListReconciledByUser(ctx, userID, offset, limit) ([]CreditReservation, int64, error)`（按 `status='reconciled'` + `user_id` 过滤、`created_at DESC`、分页、返回 total）。
- **biz 层**：落点 `internal/numind/biz/membership/`（`GetBalance` 同包，`state.go:130`）或 credit biz——新增「列出我的消耗记录」biz 方法，做 `operation → 中文展示名` 映射 + 净额计算 + DTO 组装。
- **路由**：`authGroup.GET("/credits/consumption-log", ...)`（`router.go:223-233`，与 `/credits/balance` 同组、同 `AuthMiddleware` user_token 鉴权）。
- **前端**（numind-web-v3，复用度高）：
  - 设置页 `src/views/SettingsView.vue`（route `/settings`）新增一行 `.settings-row-action` 入口。
  - 弹窗复用 `src/components/common/ConfirmModal.vue` 的 Teleport+Transition 模式，新建 `CreditConsumptionLogModal.vue`。
  - 列表复用 `src/components/common/DataTable.vue`（自带分页 / loading / empty / 列配置）。
  - API 加到 `src/api/credits.ts`（镜像 `getCreditBalance` 模式，走 `request` 封装）。
  - 状态：新建轻量 `src/stores/consumptionLog.ts` 或挂 `src/stores/credits.ts`（S2 定）。

### 技术风险

| 风险 | 缓解 |
|------|------|
| **越权**（A 看到 B 的消耗）| user_id **只从 auth 上下文 `c.GetUint("userID")` 取**，绝不接受客户端传 id；store SQL 强制 `user_id = <authUserID>` |
| **真实扣费的字段/单位口径**（`ActualCostCents` 名为 cents 实为 credits-mode 复用；含 `UserTypeMultiplier` 折算）| S2 必须定准「净真实扣费」取值公式（候选：`ReservedCredits + Delta`）+ 写后端单测断言一笔 reconciled reservation 的展示额 == 该笔实际扣的 credits 之和（与 credit_transaction 对账核对一次） |
| **动作名映射**（DB `operation` 是机读值，无中文名）| 后端维护 `operation → 中文名` 映射，前端只渲染；未知 operation 回退展示原值，不报错 |
| 状态口径（`refunded`=操作失败全退，真实成本 0；`reserved`=未平账；`expired`）| 默认只取 `reconciled`。`refunded`（未真实消耗）、`reserved`（未平账）、`expired` 一律不展示——与「只呈现平账后真实记录」一致。S2 确认 |
| 分页性能 | 复用 `idx_user_status`(user_id,status,created_at) 复合索引，offset/limit 分页 |

### 涉及仓库
- [x] numind-server（reservation 只读 store 方法 + 消耗记录 biz + 用户端 controller + router 注册）
- [x] numind-web-v3（设置页入口 + 消耗记录弹窗 + api + store + DataTable 接入）
- [ ] numind-admin-web（不涉及）

### AI 可观测性（如功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：**否**。纯 DB 只读查询，无任何 aiservice 调用。Trace / Generation **N/A**。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事
- 作为**任意登录用户**，我需要在设置页点开一个弹窗，按时间倒序看到我每一次消耗积分的**动作、时间、消耗数量**，且看到的都是**对账后的真实扣费**，以便我清楚自己的积分花在了哪些操作上、是否合理。

### 验收标准
- [ ] 任意账户登录用户端 → 设置页有「额度消耗记录」入口（位于「积分与加量包」区块下方的可点击行）
- [ ] 点击 → 弹出弹窗（Teleport，遮罩点击/ESC 可关），无独立路由页
- [ ] 弹窗内按 `created_at` 倒序展示消耗流水，每条含：时间、动作（中文名）、消耗积分
- [ ] **每条 = 一次动作**（一 reservation 一行），**只含 `status='reconciled'` 的记录**（未平账 reserved / 全退 refunded / expired 均不出现）
- [ ] 展示的「消耗积分」== 该笔动作对账后真实扣减的 credits（后端单测断言与 credit_transaction 该 reservation 的净额一致）
- [ ] 用户只能看到**自己**的记录（越权测试：构造两用户，互相看不到对方记录；user_id 仅来自 token）
- [ ] 分页可用（page/page_size，默认每页 20，返回 total）
- [ ] 未登录 / token 失效 → 401（走现有 axios 拦截器跳登录）

### 边界情况
- 从未消耗过积分（无任何 reconciled reservation）→ 空状态视图（友好文案「暂无额度消耗记录」），非报错
- 只有未平账的 reserved 记录（刚发起操作还没对账）→ 列表为空 / 不显示这些行（口径=只看平账后）
- operation 为未知/新增类型 → 回退展示原始 operation 字符串，不崩
- 同一秒多笔动作 → 各自独立行（reservation 各一行，毫秒级 created_at 排序）
- 历史 legacy 数据（如有 `status` 缺失/异常的老行）→ 不在 `reconciled` 过滤内，自然排除

### 权限规则
- **用户端**（user_token），**所有账户**可用（不区分父/子，各看各的）
- user_id 仅来自 auth 上下文，禁止客户端传参指定他人
- 无管理端入口（admin 侧不在本期范围）

### UI 行为规格
- **页面位置**：用户端设置页 `SettingsView.vue`（`/settings`），「积分与加量包」区块下方新增 `.settings-row-action` 入口行（图标 + 文案 + chevron，复用现有登出按钮那一行的样式结构）
- **形态**：点击弹**弹窗 Modal**（复用 `ConfirmModal.vue` 的 Teleport+Transition；自研组件，禁止外部 UI 框架——硬规则 ui-ux.md #5），**非独立页面**（用户已明确）
- **布局**：弹窗标题「额度消耗记录」+ `DataTable`（列：时间 / 动作 / 消耗积分）+ 底部分页器
- **交互**：打开弹窗即拉第 1 页；翻页拉对应页；关闭弹窗
- **状态处理**：loading（DataTable skeleton/spinner）/ empty（空状态 + 文案）/ error（含 retry）/ success — 四状态齐全（硬规则 ui-ux.md #2）

## §5 产品思考（office-hours 式 forcing questions）

- **需求真实性**：积分制下用户最常见的困惑是「我的积分怎么没的」。把每次消耗的动作/时间/数量摊开，是计费透明度的基本盘，直接降低信任摩擦与客服咨询。
- **最窄楔子**：只读、单弹窗、复用 `credit_reservation` 现成数据 + 前端现成 DataTable/Modal、零 schema 变更、不碰扣费逻辑本身。是能独立交付价值的最小切片。
- **关键正确性 > 花哨**：本功能的灵魂是「数字必须真实可信」。所以 S1 把「真实扣费取值口径」「只取 reconciled」「越权隔离」列为 S2 必锁项 + 后端单测断言对账一致——宁可朴素，不可有一个错数字。
- **10 星版（本期不做，记录为未来增强）**：按动作类型筛选 / 时间范围筛选、导出 CSV、展示来源池（trial/cycle/booster）标签、点击一行下钻到该次操作详情（token 用量/模型）、运行余额（balance_after）列。本期先把「动作/时间/真实数量」三要素做扎实可信。
- **不做**：管理端视图、跨用户聚合（那是 admin b2b-billing-report 的事）、把未平账的 reserve 估算也展示（违背「真实记录」口径）、运行余额列（每条历史行的当时余额需逐行回算，成本高且易错，移到未来增强）。
