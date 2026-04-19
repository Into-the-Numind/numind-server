# 加量包购买支付弹窗 — 提案

## §1 方案概述 [客户可见]

在账户设置页为 credits 制会员打通加量包自助购买通道：点击 "立即购买" → 弹窗展示付款二维码（微信扫码）或跳转支付宝收银台 → 支付完成后弹窗自动关闭 → 积分余额实时刷新 → 加量包 600 积分/90 天到账。

预期效果：**商业化链路闭环**，用户不再需要联系客服人工开通。

## §2 报价与周期 [客户可见]

- 预估工作量：1–1.5 天（仅前端）
- 报价：N/A（内部）
- 交付时间线：2026-04-20 当日完成 S4，2026-04-21 验收部署

## §3 技术可行性 [AI 内部]

### 现有功能复用（关键：后端 0 改动）

| 环节 | 复用 | 位置 |
|------|------|------|
| 订单创建 | `POST /v1/orders`（`product_type=booster`） | `numind-server/internal/numind/biz/payment/payment.go:85-211` |
| 微信支付 | Native Pay 返回 `code_url` | `payment.go:175` |
| 支付宝支付 | Page Pay 返回 form URL（同样存 `CodeURL` 字段） | `payment.go:179-187` |
| 订单状态查询 | `GET /v1/orders/:id` 返回 `pay_status`（pending/paid/refunded/closed） | `controller/v1/order/order.go:113-141` |
| 支付回调 → 加量包下发 | `/v1/payment/{wechat,alipay}/notify` → `RechargeWithOrderTx` | `biz/credit/credit.go:273-350` |
| 订单超时自动关闭 | 30min expired_at + cron `CloseExpiredOrders` | `store/order.go:109-118`, `payment.go:203` |
| 前端 QR 渲染 | `qrcode@1.5.4` 已装，`XhsBindModal.vue:88-95` 有示例 | `package.json:29` |
| 前端订单 API wrapper | `src/api/orders.ts` 已有 `getOrder(id)` | `api/orders.ts:11-25` |

### 技术风险

| 风险 | 缓解 |
|------|------|
| 前端轮询过频浪费请求 | 2s 间隔 + 5min 倒计时上限（后端 30min 过期，前端更短以提前提示） |
| 用户关闭弹窗但订单仍 pending | 不前端主动取消，让后端 cron 30min 后自动关闭；下次点击如发现同用户存在 pending booster 订单，直接复用（待 S2 确认） |
| 支付宝 PagePay 是完整页面，不能 iframe | 新标签页打开支付宝收银台 + 原弹窗继续轮询 |
| 轮询出现 network error | 指数退避（2s → 4s → 8s → 停，显示"网络异常，请刷新"） |
| 用户快速连点 "立即购买" 造成重复下单 | 下单期间按钮 disabled + loading |
| 支付完成但回调延迟 | 轮询到 `paid` 才关闭弹窗并 `creditsStore.fetchBalance()` refresh |

### 涉及仓库
- [ ] numind-server（0 改动，除非 S2 发现必须）
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性
- 涉及 LLM 调用：**否** — N/A

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为 **credits 制在期会员**，我需要在设置页直接扫码购买加量包，以便不用联系客服就能补充积分
- 作为 **credits 制非会员 / 已过期用户**，我点击按钮应看到"请先开通会员"的清晰引导（沿用 BoosterPurchaseCard 已有四态逻辑，不需要新加）
- 作为 **legacy_tier 老用户**，我不应看到加量包购买入口（沿用已有四态禁用逻辑）

### 验收标准
- [ ] 在期会员 credits 制用户：点击 "立即购买" → 弹窗在 500ms 内展示
- [ ] 微信 tab：展示 256×256 二维码 + "打开微信扫一扫" 文案 + 剩余支付时间倒计时
- [ ] 支付宝 tab：点击 "前往支付宝付款" → 新标签页打开支付宝收银台 → 原弹窗显示 "正在等待支付宝付款完成..." + 倒计时
- [ ] 轮询 `GET /v1/orders/:id`，每 2 秒一次，直到 `pay_status in ('paid','refunded','closed')` 或倒计时归零
- [ ] `paid`：弹窗自动关闭 + 全局 toast "加量包购买成功！600 积分已到账" + `creditsStore.fetchBalance()` 刷新余额
- [ ] `closed`（超时）：弹窗显示 "订单已过期，请重新发起" + "重新下单" 按钮
- [ ] 倒计时（前端 5min）到期：停止轮询，显示 "支付超时" + 可选择重新下单（创建新订单）
- [ ] 关闭弹窗（ESC / 点击遮罩 / 关闭按钮）：停止轮询，不提示（订单让后端 cron 自然过期）
- [ ] 下单 API 失败（余额系统错误、网络错误）：弹窗不展示 QR，转显示错误信息 + "重试" 按钮

### 边界情况
- 用户在支付宝 tab 已打开支付宝后切回来点 "切换到微信"：保持同一订单，不重新下单（支付宝 Page URL 和微信 code_url 后端同一订单返回同一 CodeURL，需 S2 确认是否分别下两单或一单多通道）
- 轮询时用户网络断开：连续失败 3 次后暂停轮询，显示 "网络异常，请刷新" + 手动重试按钮
- 用户已有 pending booster 订单又点击购买：**S2 决策点**（方案 A 复用现有订单 / 方案 B 关闭旧订单再下新单）
- 支付完成瞬间用户关闭弹窗：后端回调仍会正常执行（幂等），下次打开账户中心余额已更新
- 并发：用户在 A 浏览器支付成功的同时在 B 浏览器也下了单 → 后端 pricing + 订单是独立 order_id，不冲突；B 单会自然过期

### 权限规则
- **仅 credits 制 + 在期会员** 可点击 "立即购买"（`billing_mode='credits'` + `HasActiveSubscription`）
- 已由 `BoosterPurchaseCard.vue` 四态实现，本功能**不新增权限检查**
- 后端 `/v1/orders` 对 `product_type=booster` 本身**允许** C 端自购（Q1.5 豁免），但会员检查仍在 biz 层执行

### UI 行为规格
- **页面位置**：`/settings` → "我的积分" section → `BoosterPurchaseCard` 组件
- **触发**：`SettingsView.vue:296` 的 `handleBoosterPurchase()` 改为打开 `PaymentQRModal`
- **布局**：
  - Modal 居中，宽度 480px，头部 "购买加量包 · ¥29.9 / 600 积分"
  - Tab 切换：微信 / 支付宝（默认微信）
  - 微信 tab：QR 256×256 居中 + 文案 "打开微信扫一扫完成支付"
  - 支付宝 tab：大按钮 "前往支付宝付款" + 次级文案 "点击后将在新标签页打开收银台"
  - 共同底部：倒计时（mm:ss）+ "支付完成后页面会自动更新"
- **交互**：
  - 点击 "立即购买" → button 进入 loading → 创建订单 → 轮询启动 → 展示 QR
  - Tab 切换不重新下单（见边界情况 S2 决策）
  - 支付完成：250ms 成功动画 → 弹窗淡出 → 刷新余额 → toast
- **状态处理**：
  - Loading（下单中）：Modal 内居中 spinner + "正在生成订单..."
  - Success（paid）：绿色勾号动画 250ms → 关闭弹窗
  - Error（下单失败）：红色文案 + retry 按钮
  - Empty：N/A（弹窗不展示空态）
  - 倒计时归零 / closed：黄色提示 + "重新下单" 按钮
