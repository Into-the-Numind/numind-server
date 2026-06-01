# 额度消耗记录（Credit Consumption Log） — 技术设计 Spec

> NDF Standard S2 工件。对应 `requirements/credit-consumption-log.md` + `proposals/credit-consumption-log-proposal.md`。
> 日期：2026-06-01。涉及仓库：numind-server + numind-web-v3。

## 1. 概述与目标

给**所有登录用户**在用户端**设置页**提供一个「积分消耗记录」入口，点击弹出弹窗，按时间倒序展示该用户**自己**每一次消耗积分的 **动作 / 时间 / 消耗数量**，且**只呈现对账完成（平账后）的真实记录**。与 `GET /v1/credits/balance`（聚合余额）互补：余额答「还剩多少」，本功能答「花在哪了」。

**非目标（本期不做）**：管理端视图、跨用户聚合、未平账（reserve 估算）展示、运行余额（balance_after）列、按动作/时间筛选、导出、下钻 token 明细。

## 2. 数据源（关键决定）

数据源 = **`credit_reservation` 表**（`internal/pkg/model/credit_reservation.go:10`），**不是**聚合 `credit_transaction`。

理由：`credit_reservation` 本就是「一次 LLM 级动作 = 一行」的 SOT（model 注释明示），并带状态机 `reserved → reconciled | refunded | expired` 与对账后真实成本字段；而 `DeductCreditsTx`（`biz/membership/cycle.go:273-350`）写 `credit_transaction` 时不写 `reservation_id`/`biz_ref`，缺少把同一动作的 reserve+reconcile 行归并的稳定 grouping key，无法干净实现「每动作一行」。

### 2.1 字段映射（credit_reservation → 展示）

| 展示项 | 来源字段 | 说明 |
|---|---|---|
| 动作（机读） | `Operation` | 存的是**裸** operation（如 `sop_run`），非 `reserve:sop_run` 前缀（见 `credit_service.go:500`）|
| 动作（中文名） | 后端映射 `Operation` → label | 见 §5 映射表 |
| 时间 | `CreatedAt` | 毫秒精度，动作发起时间 |
| 消耗积分 | `ActualCostCents` | 对账后真实净扣减；见 §2.2 |

### 2.2 「真实扣费」取值口径（已从代码核实）

`Reconcile`（`credit_service.go:684-697`）：
```
multiplier      = row.UserTypeMultiplier (<=0 视为 1.0)
adjustedActual  = round(actualCostCents * multiplier), 下限 0
delta           = adjustedActual - row.ReservedCredits
// 终态写回：row.ActualCostCents = adjustedActual, row.Delta = delta, status = reconciled
```
因此对 `status='reconciled'` 的行：**展示「消耗积分」= `ActualCostCents`**（恒等于 `ReservedCredits + Delta`，即真实净扣减的 credits）。

### 2.3 过滤口径

```sql
WHERE user_id = ?
  AND status = 'reconciled'        -- 只取平账后；排除 reserved（未平账）/ refunded（操作失败全退，真实成本 0）/ expired
  AND actual_cost_cents > 0        -- 去掉 0 成本噪音
ORDER BY created_at DESC
LIMIT ? OFFSET ?
```
利用现成复合索引 `idx_user_status`(user_id, status, created_at)。

## 3. API 契约（跨仓库锁点）

```
GET /v1/credits/consumption-log
Auth:   user_token（authGroup，AuthMiddleware）
Query:  page       (int, 1-based, default 1)
        page_size  (int, default 20, max 100)
```

**成功响应**（经 `core.WriteResponse(c, nil, data)` 包络为 `{code:0, message:"ok", data:{...}}`）：
```jsonc
{
  "list": [
    {
      "id": 12345,                       // reservation id（前端 row key）
      "action": "sop_run",               // 机读 operation（前端可用于图标/未来筛选）
      "action_label": "SOP 执行",         // 后端给的中文展示名
      "credits": 18,                     // 正整数：本次真实消耗的积分
      "created_at": "2026-06-01T14:32:05.123Z"
    }
  ],
  "total": 137                           // 满足过滤条件的总条数（前端分页用）
}
```

**错误**：未登录/失效 token → 401（现有中间件）；page/page_size 非法 → `errno.ErrBind`/clamp（见 §6）。

## 4. 后端分层（numind-server）

单向依赖 controller → biz → store。

### 4.1 store 层（新增只读方法）
文件 `internal/numind/store/credit.go`（creditStore），新增：
```go
// ListReconciledReservationsByUser 返回某用户已平账（status=reconciled, actual_cost_cents>0）
// 的预扣记录，按 created_at DESC 分页，返回该过滤下的总数。只读。
func (s *creditStore) ListReconciledReservationsByUser(
    ctx context.Context, userID uint, offset, limit int,
) ([]model.CreditReservation, int64, error)
```
- 用 GORM query builder（禁裸 SQL，database.md §3）；`Where("user_id = ? AND status = ? AND actual_cost_cents > ?", userID, "reconciled", 0)`。
- 先 `Count` 再 `Offset/Limit/Order("created_at DESC").Find`。
- 加进 `IXxxStore` 接口（若 creditStore 有 interface 定义则同步）。

### 4.2 biz 层（新增方法）
包 `internal/numind/biz/membership/`（与 `GetBalance` 同包，`state.go`）。新增（建议新文件 `consumption_log.go`）：
```go
type ConsumptionLogItem struct {
    ID          uint64    // reservation id
    Action      string    // 裸 operation
    ActionLabel string    // 中文名（map 命中）或回退裸 operation
    Credits     int64     // = ActualCostCents
    CreatedAt   time.Time
}

func (s *MembershipService) ListConsumptionLog(
    ctx context.Context, userID uint, page, pageSize int,
) (items []ConsumptionLogItem, total int64, err error)
```
- 归一化分页：`page<1→1`；`pageSize<1→20`；`pageSize>100→100`；`offset=(page-1)*pageSize`。
- 调 store → 逐行映射，`ActionLabel = operationLabel(Operation)`（§5）；`Credits = *ActualCostCents`（reconciled 行必非 nil；防御性 nil→跳过/0）。
- 错误用 `fmt.Errorf("ListConsumptionLog: %w", err)` 包装。

### 4.3 controller 层
包 `internal/numind/controller/v1/credit/`（现有 creditCtrl）。新增 handler：
```go
func (c *CreditController) ListConsumptionLog(ctx *gin.Context)
```
- `userID := ctx.GetUint("userID")`（**仅从 auth 上下文取，绝不接受客户端传 id**）。
- 绑定 `page` / `page_size`（`ShouldBindQuery` 或 `Query`+`strconv.Atoi`，失败用默认值不报错）。
- 调 `membershipSvc.ListConsumptionLog(...)` → `core.WriteResponse(ctx, err, gin.H{"list": items, "total": total})`。
- 错误分支不向 C 端泄露内部 err 细节（通用文案）。

### 4.4 router
`internal/numind/router.go`（authGroup 段，`/credits/balance` 旁）：
```go
authGroup.GET("/credits/consumption-log", creditCtrl.ListConsumptionLog)
```
creditCtrl 已通过 `.WithMembershipSvc(membershipSvc)` 持有 membership service（router.go:223-233 现状）。

## 5. 动作 → 中文名映射（后端维护）

定义在 biz 层一个 `map[string]string`（如 `consumption_log.go` 的包级 var）。`operationLabel(op)` 命中返回中文名，未命中**回退返回裸 op 字符串**（不报错，向前兼容未来新 operation）。

| operation | action_label |
|---|---|
| `sop_run` | SOP 执行 |
| `sop_chat` | SOP 对话 |
| `salesrag_chat` | 销售对话 |
| `chatbot_chat` | 智能对话 |
| `profile_analysis` | 客户画像分析 |
| `file_parse` | 文件解析 |
| `style_analysis` | 风格分析 |
| `ocr` | 文字识别 |
| `agent_test` | 智能体运行 |
| 其它/未来 | 回退裸 operation |

> operation 全集来自 `internal/numind/biz/credit/types.go:10-17`（+ `agent_test`）。

## 6. 边界情况

- 无任何 reconciled 记录 → `list:[]`, `total:0` → 前端空状态（「暂无积分消耗记录」），非报错。
- 只有 `reserved`（未平账）记录 → 不出现（过滤排除）。
- `refunded`（操作失败全退）/ `expired` → 不出现。
- `actual_cost_cents = 0` 的 reconciled 行 → 不出现（`>0` 过滤）。
- 未知/新增 operation → 中文名回退裸值，不崩。
- page/page_size 非法或越界 → 归一化（page<1→1, pageSize clamp 到 [1,100]），不 500。
- 同毫秒多笔 → 各自独立行，按 created_at DESC + id 稳定排序（store 可加 `id DESC` 次级排序保证确定性）。

## 7. 权限与安全

- user_token 鉴权；**所有账户**可用（不分父/子，各看各的）。
- user_id 仅来自 `c.GetUint("userID")`；store 查询强制 `user_id = <authUserID>`；无任何客户端可控的用户标识入参 → 杜绝越权。
- 无管理端入口。

## 8. 前端设计（numind-web-v3）

### 8.1 入口（用户已定）
`src/views/SettingsView.vue`「积分与加量包」section：把 section 头改为 flex 行，`section-label`（左）+ 可点击「积分消耗记录」入口（右）。点击打开弹窗。
```html
<div class="settings-section">
  <div class="section-header">                 <!-- 新增：flex space-between -->
    <div class="section-label">积分与加量包</div>
    <button class="section-action" @click="logOpen = true">积分消耗记录 ›</button>
  </div>
  <div class="credit-grid"> … </div>
</div>
<CreditConsumptionLogModal v-model:open="logOpen" />
```

### 8.2 弹窗组件
新建 `src/components/credit/CreditConsumptionLogModal.vue`：
- 复用 `src/components/common/ConfirmModal.vue` 的 Teleport + Transition + 遮罩点击/ESC 关闭模式（自研组件，禁外部 UI 框架）。
- 标题「积分消耗记录」。内嵌 `src/components/common/DataTable.vue`：
  - columns：`时间`（created_at，格式化 YYYY-MM-DD HH:mm）/ `动作`（action_label）/ `消耗积分`（credits，右对齐，**正整数展示**如「18」，列头/语义已表明是消耗，不加负号）。
  - props 传 `data` / `total` / `page` / `pageSize` / `loading` / `emptyText="暂无积分消耗记录"`；监听 `update:page` 翻页。
- 打开（`open` 变 true）即拉第 1 页；翻页拉对应页。

### 8.3 api 层
`src/api/credits.ts` 新增（镜像 `getCreditBalance`，走 `request` 封装）：
```ts
export interface ConsumptionLogItem {
  id: number; action: string; action_label: string; credits: number; created_at: string
}
export interface ConsumptionLogResp { list: ConsumptionLogItem[]; total: number }
export const getConsumptionLog = (page = 1, pageSize = 20) =>
  request.get<ConsumptionLogResp>('/v1/credits/consumption-log', { params: { page, page_size: pageSize } })
```

### 8.4 状态
新建轻量 `src/stores/consumptionLog.ts`（Pinia setup 语法）：`records / total / page / pageSize / loading`，action `fetch(page)`（`finally` 关 loading；错误 toast）。单一职责，不膨胀 credits store。

### 8.5 四状态（硬规则 ui-ux.md #2）
loading（DataTable spinner）/ empty（空状态文案）/ error（toast + retry）/ success。

## 9. 测试计划（S5 验证策略在 S3 plan 定稿）

后端单测（biz + store，mock/内存 SQLite）：
1. **越权隔离**：用户 A、B 各有 reconciled reservation，A 调接口只见 A 的，绝不见 B。
2. **对账一致性**：构造一笔走 Reserve→Reconcile 的 reservation，断言展示 `credits == ActualCostCents == ReservedCredits+Delta`，且 == 该用户 credit_transaction 净扣减绝对值（单 reservation 场景可对账）。
3. **过滤正确**：reserved / refunded / expired / actual_cost_cents=0 的行不出现。
4. **分页**：total 正确；offset/limit 生效；page/page_size 归一化。
5. **未知 operation**：action_label 回退裸值。

前端（高风险计费域，倾向 **Playwright E2E** 做持久回归）：设置页入口可见 → 点击 → 弹窗出现 → 列表渲染 → 空状态 → 翻页。S3 plan 的独立「S5 验证策略」task 定稿（规则 10）。

## 10. 决策记录

- **S0-D1**（user）：受众=所有账户；入口=设置页弹窗；展示 动作/时间/数量；口径=仅平账后真实记录。
- **S1-D2**（user）：「真实记录」口径=每动作一行（方案 A）。
- **S2-D3**（user 2026-06-01）：入口放「积分与加量包」section 头**右侧**，文案=「积分消耗记录」，点击弹窗。
- **S2-D4**（ai，代码核实）：数据源=`credit_reservation`（非 credit_transaction 聚合）；消耗值=`ActualCostCents`（status=reconciled, >0）。
- **S2-D5**（ai）：动作中文名后端维护，未知回退裸值。
- **S2-D6**（ai 推荐）：前端独立 `consumptionLog` store。
