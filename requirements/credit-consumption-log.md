# 额度消耗记录（Credit Consumption Log）

## 来源
- 提出人：用户（产品 owner）
- 提出日期：2026-06-01

## 需求描述

> 需要给所有账户都开通一个额度消耗记录，放在设置页里面作为一个点击弹窗，需要记录用户每一个消耗额度的动作、时间和消费的数量，只需要呈现平账后的真实记录。

结构化理解：

- 这是一个**用户端**（numind-web-v3）功能，面向**所有账户**（不区分父/子，每个登录用户看自己的）。
- 入口在**设置页**，以**点击弹窗（modal/dialog）**形态呈现，**不是独立页面**。
- 弹窗内是一份**额度消耗流水**，每条记录展示三要素：
  - **动作**：这次消耗对应什么操作（如「SOP 执行」「销售对话」等人类可读名称）
  - **时间**：消耗发生的时间
  - **消耗数量**：这次扣了多少积分（credits）
- **数据口径**：只呈现「**平账后的真实记录**」——即 Reserve/Reconcile 两阶段对账**完成后的最终真实消耗**，而非预扣（reserve）估算的中间态。

## 业务目标

让每个用户能**自助、透明地查看**自己的积分消耗去向：什么时候、做了什么操作、花了多少积分。提升计费透明度与用户信任，减少「积分怎么没的」类咨询。与现有「余额查询」（`GET /v1/credits/balance` 只给三池聚合余额）互补——余额回答「还剩多少」，本功能回答「花在哪了」。

## 优先级

中。非紧急生产事故，属用户体验 / 计费透明度提升。

## Triage

- **推荐轨道：Standard**（用户 2026-06-01 档位判定已确认）
- **分类理由**（5 条标准逐条）：
  1. 数据库 schema 变更：**否**（`credit_transaction` 表已存在并含所需全部字段：`user_id` / `created_at` / `amount`(负=消耗) / `operation` / `source_type` / `biz_ref_type`+`biz_ref_id`，见 `internal/pkg/model/credit.go:56-77`，无需建表/加列）
  2. 新增 API 端点：**是**（需新增用户端消耗流水接口；现有 `GET /v1/credits/balance` 只返回聚合余额，无明细流水接口）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（跨两仓库：server 端 controller/biz/store 复用/router + web-v3 端 设置页接入/弹窗组件/api/类型，约 6-8 文件）
  5. 高风险业务逻辑（支付/权限）：**是**（属计费 SOT 域；且必须做严格的「只看自己」越权隔离——任何用户只能看自己 `user_id` 名下的 `credit_transaction`，绝不能越权看他人）
- **人类决定：确认 Standard**（用户 2026-06-01 已在档位判定中确认）

## 涉及仓库

- `numind-server` — 新增用户端「我的额度消耗流水」API
- `numind-web-v3` — 设置页新增「额度消耗记录」入口 + 弹窗组件

## 备注（高复用线索 + S1/S2 待澄清）

### 高复用线索（本 session 已调研，file:line 已核实）
- **数据源**：`credit_transaction` 表（`internal/pkg/model/credit.go:56-77`）。`Amount int64`（**负值 = 消耗**，正值 = 退款/返还）；`Operation string`（如 `reserve:sop_run` / `reconcile:sop_run` / `reserve:salesrag_chat`）；`CreatedAt`（含复合索引 `idx_ct_user_created`）；`SourceType *string`(`trial`/`cycle`/`booster`/NULL)；`BizRefType`+`BizRefID`（关联 `sop_run` / `sales_session`）。
- **store 方法已存在**：`creditStore.ListTransactionsByUser(ctx, userID, offset, limit) ([]CreditTransaction, int64, error)`（`internal/numind/store/credit.go:227-243`），已按 `created_at DESC` 排序 + 分页 + 返回 total。可直接复用，可能仅需加「只取消耗」过滤。
- **biz 落点**：`MembershipService`（`internal/numind/biz/membership/`，`GetBalance` 在 `state.go:130-206`）或 credit 相关 biz——新增「列出我的消耗记录」biz 方法。
- **路由模式**：`authGroup.GET("/credits/...", ...)`（`router.go:223-233`，与 `/credits/balance` 同组、同 `AuthMiddleware` 用户鉴权）。
- **既有相似 UI 参考**：`controller/v1/user_billing/billing.go:59-125`（基于 `usage_record` 的 ListRecords），可参考其分页响应形态。

### 仍待 S1/S2 决策（默认值已给，S1 提案确认）
1. **「平账后的真实记录」精确口径（最关键设计点）**：因 Reserve/Reconcile 两阶段，单次动作会产生 `reserve:` 行（预扣估算）+ `reconcile:` 调整行（多退少补）。需明确：
   - 方案 A（推荐）：**每个动作聚合成一行**，展示最终结算额（按 reservation / `biz_ref` 合并 reserve+reconcile+refund 求和），符合「真实记录」直觉，无中间态噪音。
   - 方案 B：只过滤掉未对账的 reserve，展示对账后明细行。
   - → 默认推荐 A，待 S2 技术设计确认聚合实现（是否需 store 层 GROUP BY 或 biz 层归并）。
2. **「动作」人类可读名映射**：DB 里 `operation` 是机读值（`reserve:sop_run` 等），无预存中文名。需 `operation` → 中文展示名映射（如 `sop_run`→「SOP 执行」、`salesrag_chat`→「销售对话」）。默认放**后端**返回展示名（前端只渲染），避免前端维护映射散落。
3. **分页 / 数量**：弹窗内是否分页 / 无限滚动 / 默认最近 N 条？默认：分页（`page`/`page_size`，复用 `ListTransactionsByUser` 已有 offset/limit），弹窗内默认展示第 1 页（如 20 条）+ 翻页。
4. **来源池区分**：是否按 trial/cycle/booster 标注每条消耗来源？默认：**不强制区分**，先只展示 动作/时间/数量 三要素（按需 S1 加来源标签）。
5. **历史 / 遗留行**：`source_type=NULL` 的 legacy/debt 行是否展示？默认：展示（只要 `amount<0` 且属于该用户的真实消耗）。
6. **空状态**：用户从未消耗过积分时弹窗的空状态 + 文案（默认：空状态插画/文案「暂无额度消耗记录」）。
7. **金额单位**：`amount` 是 credits 积分整数，直接以「积分」展示（消耗显示为正数 + 「消耗」语义，或带负号，S1 确认展示形态）。
