# Booster Payment UI — S3 Task Plan

**Feature:** `booster-payment-ui`
**Spec:** `numind-server/docs/superpowers/specs/2026-04-20-booster-payment-ui-design.md`
**Repo:** numind-web-v3 only
**Branch:** `feature/booster-payment-ui`
**Date:** 2026-04-20

---

## §0 执行约束

- **仅前端**：所有 task 在 `numind-web-v3/` 下执行；后端 0 改动
- **严格串行**：每 task commit 后须通过 2 阶段 review（spec-compliance + code-quality Sonnet subagent），review PASS 后才能启动下一 task（CLAUDE.md Rule 6）
- **原子性**：每 task 完成后 `numind-web-v3` 可独立编译 + lint 通过
- **不装新依赖**：复用已有 `qrcode@1.5.4`；不引入外部 UI 库（CLAUDE.md §5 硬规则）

---

## §1 Task 列表（总 5 个）

### Task 1：PaymentQRModal 骨架 + 状态机

**目标：** 新建 `PaymentQRModal.vue`，实现 §3 状态机的基础结构 + Props/Emits 契约 + 生命周期（timer 清理）。**此 task 不接真实 API**（mock 订单对象）以验证状态机本身。

**文件：**
- `numind-web-v3/src/components/credit/PaymentQRModal.vue`（新建）

**实现要点：**
1. `<script setup lang="ts">` + Composition API
2. Props: `{ open: boolean }`，Emits: `{ 'update:open': (v: boolean) => void, 'paid': () => void }`
3. 内部 state ref: `state: 'idle'|'creating'|'pending'|'paid'|'expired'|'error'|'closed'`
4. 内部 ref: `order: Order | null`, `countdown: number`（秒），`activeTab: 'wechat'|'alipay'`，`pollFailureCount: number`
5. `watch(() => props.open, (isOpen) => isOpen ? startFlow() : cleanup())`
6. `onBeforeUnmount(cleanup)` 兜底清理 timer
7. Timer 管理：`pollTimer: number | null`, `countdownTimer: number | null`
8. Mock `createOrder()` 返回假订单（下个 task 换真 API），`pollOrderStatus()` 先留空
9. 状态机 transition 函数：`transitionTo(next: State)` 清理旧 state 资源 + 设置新 state 资源
10. Template：基础 Modal 容器（不追求 UI 精致，下 task 做）；展示当前 state 文字便于调试

**验收：**
- `npm run type-check` + `npm run lint` 全绿
- 手动：在任意页面 `<PaymentQRModal v-model:open="show" />`，toggle `show` 后弹窗显示/隐藏，state 文本切换

**不包含（推迟到后续 task）：**
- 真实 API 调用
- QR 渲染 / 支付宝跳转
- UI 样式对齐 DESIGN.md

**预估 LOC：** 150-180

---

### Task 2：接入真实 API（下单 + 轮询）

**目标：** 把 Task 1 的 mock 替换成真实 `createOrder` / `getOrder` 调用，实现完整的 creating → pending → paid/expired/error 流转。

**文件：**
- `numind-web-v3/src/components/credit/PaymentQRModal.vue`（修改）

**实现要点：**
1. Import `createOrder, getOrder` from `@/api/orders.ts`
2. `async function createBoosterOrder(channel: 'wechat' | 'alipay')`:
   - 从 `useUserStore()` 取 `user_id`
   - POST `{ user_id, product_type: 'booster', months: 0, pay_channel: channel }`
   - 成功 → `order.value = res.data`, `transitionTo('pending')`
   - 失败 → `errorMsg.value = err.message`, `transitionTo('error')`
3. `async function pollOrderStatus()`:
   - 去重 flag `isPolling` 防止请求叠压
   - `const res = await getOrder(order.value.id)`
   - `if (res.data.pay_status === 'paid') transitionTo('paid')`
   - `if (res.data.pay_status === 'closed' || 'refunded') transitionTo('expired')`
   - 网络错误 → `pollFailureCount++`，≥3 次 → `transitionTo('error')`
4. `startPolling()` setInterval 2000ms
5. `startCountdown()` 初始 300（5min），每 1s -1，到 0 → `transitionTo('expired')`
6. `transitionTo('paid')` → 250ms 后 `emit('paid'); emit('update:open', false)`
7. **前置确认**：动手前先读 `numind-web-v3/src/api/orders.ts` 确认 `createOrder(req): Promise<ApiResponse<Order>>` 和 `getOrder(id): Promise<ApiResponse<Order>>` 均存在且类型完整；若 `Order` 接口缺 `code_url / pay_status / expired_at` 等字段则先补 API 层类型（S1 explore 结果显示已存在）
8. `handleRetry()` / `handleReorder()` 触发 `createBoosterOrder(activeTab)`
9. **Tab 切换 watch 挪到 Task 3**（UI 层有 tab 组件再加 watch，避免本 task "能过 type-check 但无 UI 可触发" 的假原子性）

**验收：**
- `npm run type-check` + `npm run lint` 全绿
- 手动：登录 dev 环境 credits 制会员账号 → 点击购买 → dev tools 看到 POST /v1/orders 请求 → 返回后轮询 GET /v1/orders/:id 每 2s 一次
- 人为制造错误（断网）验证 error state
- tab 切换验证推迟到 Task 3

**预估 LOC：** 新增 120-150（总文件约 280-330）

---

### Task 3：QR 渲染 + 支付宝跳转 + UI 对齐 DESIGN.md

**目标：** 把 Task 1-2 的调试 UI 替换为生产级界面，对齐 DESIGN.md token 体系。

**文件：**
- `numind-web-v3/src/components/credit/PaymentQRModal.vue`（修改）

**实现要点：**
0. **前置动作**：动手前先 `grep -r "defineComponent\|ConfirmModal\|Dialog\|Modal" numind-web-v3/src/components` 确认是否有已有的 Modal/Dialog 基础组件。若有则复用（比如 `ConfirmModal.vue` 的外壳可能可抽离），若无则走 `<Teleport to="body">` + 固定定位 div + 遮罩的 fallback 路径
1. 微信 tab：
   - Import `QRCode from 'qrcode'`
   - `watch(() => order.value?.code_url, async (url) => { if (url && activeTab.value === 'wechat') qrDataUrl.value = await QRCode.toDataURL(url, { width: 256, margin: 2 }) })`
   - Template `<img :src="qrDataUrl" alt="付款二维码" width="256" height="256">`
2. 支付宝 tab：
   - 大按钮 "前往支付宝付款" → `@click="window.open(order.code_url, '_blank', 'noopener')"`
   - 次级文案 "点击后将在新标签页打开收银台"
3. 6 个 state 各自的 UI（按 spec §5.2 表格）：
   - creating: spinner + "正在生成订单..."
   - pending: QR/按钮 + 倒计时 "剩余 mm:ss"
   - paid: 绿勾动画 + "支付成功！"
   - expired: 黄警示 + "订单已过期" + "重新下单" 按钮
   - error: 红错误 + 消息 + "重试" 按钮
4. Tab 切换组件（不引入 UI 库）：两个 button 并排，active 状态用 `--color-brand` 下划线
5. **Tab 切换 watch**（从 Task 2 挪来）：`watch(activeTab, (n, o) => { if (n !== o && state.value === 'pending') { cleanup(); createBoosterOrder(n) } })`
6. **防重复下单**（spec §6 #13）：按钮 `disabled` 绑定 `state.value !== 'idle' && state.value !== 'expired' && state.value !== 'error'`（仅在 "待启动 / 可重启" 状态下允许点击，避免 pending/creating 期间再触发）
7. 倒计时：`<time class="font-mono">{{ mm }}:{{ ss }}</time>`，mm/ss 补零
8. 价格 chip：`<span class="price-chip">¥29.90 · 600 积分 · 90 天有效</span>`
9. Scoped CSS 使用 `var(--space-*)`, `var(--color-*)`, `var(--font-*)` token
10. 关闭方式：ESC 键（`useEventListener`）/ 点击遮罩 / 关闭按钮

**验收：**
- `npm run type-check` + `npm run lint` 全绿
- 本地 dev 跑：视觉符合 spec §5.1 布局；暗色模式若项目支持需验证（查 DESIGN.md）
- 无 console warning
- 手动制造 6 个 state 逐一肉眼过关（可用 Vue devtools 改 state ref 值）

**预估 LOC：** 新增 200-250（含 CSS；最终文件约 480-580）

---

### Task 4：SettingsView 接入 + 成功回调 + S5 验证策略执行

**目标：** 把 `PaymentQRModal` 挂到 `SettingsView`，替换 toast stub；支付成功触发余额刷新；执行 S5 验证策略（见 §2）。

**文件：**
- `numind-web-v3/src/views/SettingsView.vue`（修改）

**实现要点：**
1. Import `PaymentQRModal`
2. 新 ref: `showPaymentModal = ref(false)`
3. 修改 `handleBoosterPurchase()`：
   ```ts
   function handleBoosterPurchase(): void {
     showPaymentModal.value = true
   }
   ```
4. Template 底部加：
   ```vue
   <PaymentQRModal v-model:open="showPaymentModal" @paid="handleBoosterPaid" />
   ```
5. 新增 `handleBoosterPaid()`:
   ```ts
   async function handleBoosterPaid(): Promise<void> {
     await creditsStore.fetchBalance()
     notifications.success('加量包购买成功！600 积分已到账，有效期 90 天')
   }
   ```
6. 删除原 TODO 注释

**验收（S5 策略在此 task 内执行）：**
- `npm run lint && npm run type-check` 全绿
- **手动 E2E 验证**（见 §2）

**预估 LOC：** +15 修改（SettingsView 原 380 行，改完约 395 行）

---

### Task 5：PaymentQRModal 状态机 Vitest 单测

**目标：** 为状态机 transition 写单元测试，作为未来回归保护的唯一自动化抓手（手动 E2E 不持久）。

**文件：**
- `numind-web-v3/src/components/credit/__tests__/PaymentQRModal.spec.ts`（新建，或相邻位置按项目约定）

**实现要点：**
1. Mock `@/api/orders.ts` 的 `createOrder` / `getOrder`
2. Mock timers：`vi.useFakeTimers()`
3. 测试路径（最少 6 条）：
   - T1: mount + open=true → createOrder 被调用 → state='pending'
   - T2: pending 下 getOrder 返回 paid → state='paid' → 250ms 后 emit('paid') + emit('update:open', false)
   - T3: pending 下倒计时归零 → state='expired'
   - T4: pending 下连续 3 次 getOrder 网络错误 → state='error'
   - T5: pending 下 Tab 切换（模拟改 activeTab ref） → 旧 order 停止轮询 + 新 createOrder 被调用
   - T6: open=true → open=false → timer 被清理（vi.clearAllTimers 前后 hasPendingTimers 验证）
4. 不测 UI 渲染细节（交给手动 E2E）

**验收：**
- `npm run test:unit` 或项目对应命令全绿
- `npm run lint` 全绿
- 覆盖率报告（若项目有）：PaymentQRModal.vue 状态机部分 > 80%

**预估 LOC：** 150-200

---

## §2 S5 验证策略（Rule 10 要求独立 task）

### 2.1 选用方式：**手动 E2E + 单元测试（状态机）**

**不用：**
- ❌ Playwright E2E：需要 mock 真实支付回调，mock 复杂度高于收益
- ❌ gstack `/qa`：支付弹窗依赖微信/支付宝实际回调，无法在自动化 QA 内完成支付动作

**用：**
- ✅ **手动 E2E**：用户（zchen27）在 dev 环境登录 credits 会员账号，真实扫码支付 ¥29.9 → 验证加量包到账
- ✅ **Vitest 单元测试**（触及真钱，默认包含，不是可选）：为 PaymentQRModal 的状态机 transition 写 1 个文件 unit test（mock API 层，验证 creating → pending → paid / expired / error 路径）。无真支付依赖，未来回归保护的唯一抓手

### 2.2 关键用户路径清单（手动 E2E 必走）

| # | 路径 | 期望结果 |
|---|------|----------|
| P1 | credits 会员 → 设置页 → 点 "立即购买" | 弹窗打开，微信 tab QR 显示，倒计时 5:00 开始 |
| P2 | 微信扫码完成支付（实付 ¥29.9） | 弹窗 2s 内切到 "支付成功" 绿勾 → 自动关闭 → 全局 toast → 余额 +600 积分 |
| P3 | 切到支付宝 tab | 旧订单停止轮询，新订单下成功；按钮 "前往支付宝付款" 显示 |
| P4 | 点 "前往支付宝付款" → 新标签打开 → 完成支付（或取消） | 原弹窗轮询到 paid 时自动关闭；若取消支付，弹窗继续 pending 直到倒计时归零 → expired |
| P5 | 打开弹窗后立即关闭（不支付） | 订单留 pending，30min 后后端 cron 关闭；不影响下次购买 |
| P6 | 余额不足 / 非会员（理论不可达，因按钮已禁用） | API 报错 → error state 显示后端消息 |
| P7 | 倒计时归零 | 弹窗切 expired，展示 "重新下单" 按钮；点击后创建新订单 |
| P8 | 已打开弹窗后断网 | 连续 3 次轮询失败 → error state；重新联网后点 "重试" 创建新单 |

### 2.3 诚实声明

- **gstack /qa 的局限**：支付扫码环节需真人操作，gstack 无法模拟。手动 E2E 的 P1/P3/P5/P7/P8 可视化覆盖，P2/P4 需真实支付（少量真钱成本）。
- **回归保护缺口**：手动 E2E 不产生持久测试。未来此功能修改需重新跑一遍 8 条路径。如果 Task 4 有余力，**建议补 Vitest 状态机单测**（不依赖真支付，2s 内可跑），至少保证状态转换逻辑不回归。
- **Risk tier 评估**：触及真钱，理论上应写 Playwright E2E。但由于需要 mock 支付回调（后端回调 → 下发 CreditPackage → 前端轮询到 paid）链路复杂，mock 成本 > 一次性手动验证收益。**如 reviewer 坚持要 E2E，就在 Task 4 加 Playwright**（预估 +1 小时）。

---

## §3 Task 依赖图

```
Task 1 (状态机骨架)
  ↓
Task 2 (真实 API)
  ↓
Task 3 (QR + UI + tab 切换 + 防重复)
  ↓
Task 4 (SettingsView 接入 + S5 手动 E2E)
  ↓
Task 5 (Vitest 状态机单测)
```

Task 1-4 纯串行（同文件）。Task 5 依赖 Task 4 完成后的稳定 API，也可在 Task 3 后并行，但为避免早期 mock 失效，建议放 Task 4 之后。

---

## §4 Review 要求（每 task 后强制）

按 CLAUDE.md Rule 6：

1. **Spec Compliance Review**（Sonnet subagent）：
   - 对照 spec §3 状态机 / §5 UI 规格 / §6 边界矩阵
   - Task 1 check：状态机 transition 完整；Task 2 check：API 调用参数正确；Task 3 check：UI 对齐 DESIGN.md；Task 4 check：余额刷新 + toast
2. **Code Quality Review**（Sonnet subagent）：
   - 无 `any` 类型；timer 无泄漏；error 有捕获；无引入外部 UI 库；Vue 3 setup 规范

**发现 P0 → 阻塞，立即修复**
**发现 P1 → 立即修复**
**发现 P2 → 能现修就现修，不留债**

---

## §5 合并 & 部署（S6/S7 归档预告）

- 每 task commit 到 `feature/booster-payment-ui`
- 4 个 task 全部 PASS 后：`/commit-merge-push` → 本地 merge 到 develop → push → dev 自动部署
- S5 在本地 dev 跑（`$LOCAL_SITE_URL`）；S6 部署后在 dev 服务器再跑一轮 P1-P8 冒烟

---

## §6 预估时间

| Task | 时长估计 |
|------|---------|
| Task 1 | 30min |
| Task 2 | 45min |
| Task 3 | 60-90min（UI 对齐 + tab 切换 + 防重复下单）|
| Task 4 | 40-60min（含 S5 手动 E2E P1-P8，P2/P4 需等真实扫码支付）|
| Vitest 单测（状态机，默认包含） | 45min |
| **合计** | 3.5-4.5 小时 |

若 reviewer 坚持 Playwright E2E：+60min（但不推荐，见 §2.3）。
