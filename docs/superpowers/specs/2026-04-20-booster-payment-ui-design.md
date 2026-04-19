# Booster Payment UI — S2 Spec

**Feature ID:** `booster-payment-ui`
**Track:** Standard (轻量)
**Repo:** numind-web-v3（仅前端，后端 0 改动）
**Date:** 2026-04-20

---

## §0 背景与边界

credits-system 后端支付链路在 S7-done 已完整：下单 → 微信 Native/支付宝 Page → 回调 `fulfillOrder` → `RechargeWithOrderTx` 下发 `credit_package{type='booster', total=600, expires=+90d}`。

**本 spec 只设计前端。** 后端代码、DB schema、API 均不动。

**范围**：`BoosterPurchaseCard.vue` 的 `@purchase` 事件 → 打开 `PaymentQRModal` → 下单 + 轮询 + 展示 QR → 成功 / 超时 / 错误处理 → 关闭弹窗后刷新余额。

---

## §1 API 契约（前端消费）

### 1.1 下单

```
POST /v1/orders
Body: { user_id, product_type: "booster", months: 0, pay_channel: "wechat"|"alipay" }
200 Response: Order {
  id: number           // 订单 ID（用于轮询）
  order_no: string     // 展示给用户（可选）
  pay_status: "pending"
  code_url: string     // 微信：weixin:// 二维码字符串；支付宝：https://openapi.alipay.com/... 跳转 URL
  amount: 2990         // 分，展示 ¥29.90
  expired_at: ISO8601  // 后端给的 30min 过期时刻，但前端用自己的 5min 倒计时
  pay_channel: "wechat"|"alipay"
}
```

**前端调用位置：** `PaymentQRModal.vue` 的 `createBoosterOrder()` 函数，复用 `src/api/orders.ts:createOrder()`。

### 1.2 轮询订单状态

```
GET /v1/orders/:id
200 Response: Order { pay_status: "pending"|"paid"|"closed"|"refunded", ...其余同上 }
```

**前端调用位置：** `PaymentQRModal.vue` 的 `pollOrderStatus()`，复用 `src/api/orders.ts:getOrder()`。

### 1.3 刷新余额

```
GET /v1/credits/balance  （已有）
```

**前端调用位置：** `useCreditsStore().fetchBalance()`（已有），支付成功后调一次。

---

## §2 前端架构

### 2.1 新建文件（1 个）

```
numind-web-v3/src/components/credit/PaymentQRModal.vue
```

### 2.2 修改文件（2 个）

| 文件 | 改动 |
|------|------|
| `numind-web-v3/src/views/SettingsView.vue` | `handleBoosterPurchase()` 打开 modal（替换 toast） + 引入 `PaymentQRModal` + 成功回调 `refreshCreditsBalance()` |
| `numind-web-v3/src/components/credit/BoosterPurchaseCard.vue` | **不改**（`@purchase` 事件契约保持） |

### 2.3 Props / Events 契约

```ts
// PaymentQRModal.vue
interface Props {
  open: boolean          // v-model:open 控制显示
}
interface Emits {
  (e: 'update:open', v: boolean): void
  (e: 'paid'): void      // 支付成功触发（SettingsView 收到后刷新余额）
}
```

**设计理由：** 不把 `userId / product / amount` 作为 props 传入 — Modal 内部自己决定（因为固定是当前登录用户买 booster ¥29.9）。如果将来支持其他订单类型，再改 props。YAGNI。

---

## §3 状态机（核心设计）

```
State: idle | creating | pending | paid | expired | error | closed

idle
  ├─ open=true + mount → creating
  └─ open=false → (stay idle)

creating          // 调用 POST /v1/orders
  ├─ success → pending (start polling + countdown)
  ├─ network/api error → error
  └─ user cancel (close) → closed

pending           // 轮询 GET /v1/orders/:id 每 2s
  ├─ response.pay_status='paid' → paid
  ├─ response.pay_status='closed' → expired
  ├─ response.pay_status='refunded' → expired (treat as failed)
  ├─ countdown = 0 → expired (stop polling)
  ├─ 连续 3 次网络错误 → error (pause polling, show retry)
  ├─ user switch tab wechat<->alipay → (tab 切换不改 state，复用同订单 code_url 无效 → 决策见 §4)
  └─ user close → closed

paid
  ├─ auto trigger: emit('paid') → 250ms 成功动画 → emit('update:open', false)

expired
  └─ click "重新下单" → creating (create new order)

error
  └─ click "重试" → creating (create new order)

closed            // 清理：stop polling, stop countdown
  └─ (modal unmount)
```

### 3.1 计时器生命周期

- `pollTimer`：`setInterval 2000ms` 在进入 `pending` 时启动，离开 `pending` 时清理
- `countdownTimer`：`setInterval 1000ms` 在进入 `pending` 时启动，离开 `pending` 时清理
- `watchEffect(() => open)` 监听关闭 → 强制切到 `closed` state 并清理 timer
- `onBeforeUnmount` 兜底清理 timer（防内存泄漏）

### 3.2 轮询请求去重

- 使用 `isPolling` flag，若上次请求未返回则跳过本次 tick（避免 2s 间隔但请求耗时 5s 时叠压）
- 组件 unmount 时丢弃飞行中的请求响应（检查 `state.value === 'pending'` 再 setState）

---

## §4 微信 / 支付宝两种支付方式的处理差异

### 4.1 微信 Native Pay（默认 tab）

- `code_url` 形如 `weixin://wxpay/bizpayurl?pr=xxx`
- 前端用 `qrcode@1.5.4` 的 `QRCode.toDataURL(code_url, { width: 256, margin: 2 })` 渲染 `<img>`
- 用户打开微信扫码 → 微信发起支付 → 后端收到微信回调 → 前端轮询到 `paid`

### 4.2 支付宝 Page Pay

- `code_url` 是 `https://openapi.alipay.com/gateway.do?...&biz_content=...`
- **不做 iframe 内嵌**（支付宝收银台会 frame-break）
- 点 "前往支付宝付款" 按钮 → `window.open(order.code_url, '_blank', 'noopener')` → 支付宝收银台新标签
- 原弹窗继续轮询 → 用户完成支付 → 后端支付宝回调 → 前端轮询到 `paid`
- 用户可关闭支付宝 tab 回到原站，原弹窗自动关闭

### 4.3 Tab 切换重下单（关键决策）

**问题：** 用户在 "微信" tab 下了单（`code_url` 是微信二维码），切到 "支付宝" tab 怎么办？旧订单的 `code_url` 不能给支付宝扫。

**决策：选方案 B — Tab 切换立即重下单。**

- 方案 A（复用订单）：❌ 不可行，后端下单时已绑定 `pay_channel`，同一订单 `code_url` 只对一个通道有效
- 方案 B（重下单）：✅ Tab 切换时 → 当前订单作废（前端停止轮询，后端 cron 30min 自然过期）→ 按新通道下新单
- 方案 C（同时下两单）：❌ 用户支付时两单只能付一个，另一个浪费后端订单号资源

**实现：** `watch(activeTab, (newTab, oldTab) => { if (newTab !== oldTab && state === 'pending') createOrder(newTab) })`

### 4.4 已有 pending booster 订单的处理

**结论：不做检测，每次点击下新单。** 理由：
- GET /v1/orders 不支持 filter by `product_type` / `pay_status`，前端实现成本高
- 后端 cron `CloseExpiredOrders` 30min 自动关闭旧单
- 同一用户同时有 2 个 pending booster 订单不冲突（order_id 独立）
- 用户如果真的想复用，自己会回到已开的标签页继续支付

---

## §5 UI 规格（对齐 DESIGN.md）

### 5.1 布局

```
┌────────────────────────────────────────┐
│  购买加量包                       ×    │  ← header: 600 weight, 16px
├────────────────────────────────────────┤
│  ¥29.90  ·  600 积分  ·  90 天有效     │  ← subtle-surface chip
├────────────────────────────────────────┤
│  ┌───────┐ ┌───────┐                   │  ← Tab 切换
│  │ 微信  │ │ 支付宝│                   │
│  └───────┘ └───────┘                   │
├────────────────────────────────────────┤
│                                        │
│          [ 256×256 QR ]                │  ← 微信 tab 内容
│                                        │
│        打开微信扫一扫完成支付            │
│                                        │
│        剩余 04:37                       │  ← mono 字体倒计时
├────────────────────────────────────────┤
│  支付完成后页面会自动刷新，无需手动操作    │  ← hint footer, 12px muted
└────────────────────────────────────────┘
```

**支付宝 tab：** QR 位置替换为大按钮 "前往支付宝付款" + 次级文案 "点击后将在新标签页打开收银台"。其余相同。

### 5.2 State 对应 UI

| State | 主区域 | 底部 |
|-------|--------|------|
| creating | spinner + "正在生成订单..." | 倒计时占位 --:-- |
| pending | QR / 跳转按钮 | "剩余 mm:ss" |
| paid | 绿色勾号动画 + "支付成功！" | 隐藏 |
| expired | 黄色警示 + "订单已过期，请重新下单" + 按钮 | 隐藏 |
| error | 红色错误 + 错误消息 + "重试" 按钮 | 隐藏 |

### 5.3 Toast 消息

- 支付成功：`notifications.success('加量包购买成功！600 积分已到账，有效期 90 天')`
- 下单失败：`notifications.error(err.message || '下单失败，请稍后重试')`
- 不展示 pending / expired 的 toast（弹窗内状态已足够表达）

### 5.4 DESIGN.md 对齐

- 使用已有 `ConfirmModal` 的 dialog 容器结构 或 类似 modal 组件（待 S3 task 明确）
- 字体：主标题 `--font-size-base` 16px weight 600；价格 chip `--font-size-sm`；倒计时 `--font-mono`
- 间距：padding `--space-6` (24px)，header/body/footer 之间 `--space-4` (16px)
- 颜色：成功绿 `--color-success`，警告黄 `--color-warning`，错误红 `--color-danger`

---

## §6 边界情况矩阵

| # | 场景 | 处理 |
|---|------|------|
| 1 | 用户余额系统故障，下单 500 | → error state，展示后端返回的 message |
| 2 | 用户非 credits 会员（通常按钮已禁用，但 API 被绕过调） | 后端 biz 层已有检查返回业务错误 → error state |
| 3 | 网络断开：单次 404 / timeout | 失败计数 +1，继续下次轮询 |
| 4 | 网络断开：连续 3 次失败 | → error state，显示 "网络异常" + 重试按钮 |
| 5 | 轮询返回 `refunded` | 极少见（支付后立即退款），按 `expired` 处理 |
| 6 | 轮询返回 `closed`（后端 cron 关闭） | → expired state |
| 7 | 前端 5min 倒计时归零但后端仍 pending | 停止轮询，展示 expired UI；用户可点击重新下单（此时后端订单会自然过期 25min 后） |
| 8 | 用户支付中关闭弹窗 | 停止轮询；用户下次打开账户页会看到余额已更新（若回调已到） |
| 9 | 用户多标签页同时下单 | 两个独立 order_id，两边轮询；任一成功，另一成为孤儿订单，30min 后 cron 关闭 |
| 10 | Tab 切换瞬间网络错误 | 新订单下单失败 → error state；旧订单已停止轮询（正常孤立） |
| 11 | 弹窗打开瞬间用户断网 | createOrder 失败 → error state |
| 12 | 支付成功回调延迟（后端 > 2s） | 前端继续轮询，直到收到 paid 或超时 |
| 13 | 用户点击 "立即购买" 快速连点 | Button `disabled` 绑定 `state !== 'idle'`，防重复下单 |

---

## §7 可观测性

- **前端不新增 Langfuse trace**（支付链路后端已有 trace，前端轮询属于纯 UI 行为）
- **console 日志**（dev 模式）：订单创建 / 状态变化 / 倒计时归零 / tab 切换，便于调试
- **Sentry / 错误上报**（如项目已接入）：下单失败、连续轮询失败 → 捕获为 warning

---

## §8 测试策略（验证计划的详细版见 S3 plan 的 S5 task）

- **单元测试**：状态机 transition 矩阵（creating → pending / pending → paid / pending → expired / 连续错误 → error）
- **E2E**：待 S3 的 S5 验证策略 task 决定（gstack `/qa` vs Playwright）

---

## §9 不做 / 推迟

- ❌ 不做 "查询当前用户是否有 pending booster 订单并复用" — 前端实现成本 > 收益
- ❌ 不做 WebSocket 实时推送 — 轮询足够（支付场景 1-2 秒延迟可接受）
- ❌ 不做支付方式记忆（cookie 记住用户上次选微信 or 支付宝）— YAGNI
- ⏭ 推迟：订单历史页面（已在 admin 端，C 端暂不需要）

---

## §10 完工定义（DoD）

- [ ] `PaymentQRModal.vue` 实现 §3 状态机（creating/pending/paid/expired/error/closed 六态）
- [ ] 微信 tab QR 渲染 256×256
- [ ] 支付宝 tab 跳转新标签
- [ ] Tab 切换重下单 + 清理旧 timer
- [ ] 5min 倒计时 + 2s 轮询，关闭弹窗立即清理
- [ ] `SettingsView.vue` 的 `handleBoosterPurchase` 改为打开 modal；`paid` 事件 → `creditsStore.fetchBalance()` + toast
- [ ] 全 13 条边界矩阵处理到位
- [ ] DESIGN.md token 对齐，不引入外部 UI 库
- [ ] `npm run lint && npm run type-check` 通过
