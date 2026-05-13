# 会员积分体系重构 — S5 验证策略

> **项目**：membership-credits-redesign  
> **NDF 阶段**：S5 验证（前置：S4 实现完毕、两阶段 review 全过）  
> **创建日期**：2026-04-29  
> **关联工件**：  
> - 设计 Spec：`numind-server/docs/superpowers/specs/2026-04-29-membership-credits-redesign-design.md`  
> - 实现 Plan：`numind-server/docs/superpowers/plans/2026-04-29-membership-credits-redesign-plan.md`  
> - Build Manifest：`numind-server/build-manifest.yaml` 条目 `membership-credits-redesign`  

**根据 NDF Rule 10**，所有高风险业务（支付、权限、会员等级）必须在 S4 → S5 gate 处制定详细验证策略。本文档是 S5 gate 输入，且 S3 plan 审查时由独立 reviewer 同步审查本策略合理性。

---

## 一、验证方式（三件套）

### 1.1 Playwright E2E（持久化测试代码）— 主回归保护

在 `numind-web-v3/e2e/` 新增文件 `membership-credits-redesign.spec.ts`，覆盖 6 条关键路径（§2）。

**特点**：
- 产生持久化的 .ts 代码文件，提交到 git
- 未来代码改动时自动运行，保证回归
- 通过对标一致的 Playwright 基础设施（auth.setup.ts 复用、helper 统一）

**运行命令**（S5 执行）：
```bash
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- membership-credits-redesign.spec.ts
# 期望：退出码 0，所有 6 条路径 test case PASSED
```

### 1.2 gstack /qa（一次性视觉与交互验证）— 视觉 QA 与快速迭代反馈

使用 Claude Code `gstack` skill 进行人机混合验证，重点覆盖：
- 页面布局、间距、颜色、排版
- 交互反馈（toast、modal、loading 态）
- 跨浏览器/屏幕尺寸一致性（手机/平板/桌面）

**特点**：
- 不产生自动化代码，属一次性视觉验证
- 截图 baseline 入 git，用于未来视觉对标（但需人工批准 diff）
- gstack 本身不参与自动化 test pipeline，仅 S5 验收期使用

**诚实声明**（NDF Rule 10 补充）：  
gstack /qa 是一次性验证工具，不产生持久化测试。其截图 baseline 可用于未来视觉回归对标，但并非自动化测试代码。若该功能视觉大改（如重构 UI 组件），需要手动重新 baseline，无法自动检测回归。**因此 gstack /qa 不能替代 Playwright E2E 的逻辑功能保护。**

### 1.3 Go 单元测试 + 并发压测（持久化）— 算法 + 竞态防御

在 `numind-server/internal/numind/biz/` 和 `internal/numind/store/` 新增/扩展 `*_test.go`，覆盖：
- 核心业务函数（GrantMembership, DeductCredits, RenewSubscription 等，spec §3）
- 并发场景（锁顺序、idempotency 幂等性、死锁检测，spec §4）

**运行命令**（S5 执行）：
```bash
# 轻量测试（S4 编码阶段每 task 后跑）
go test ./...

# 完整测试（S5 最终验收）
task test
# 输出：race detection pass + coverage ≥ 80% (biz) / 70% (store)
```

**并发压测**（spec §9.2 四个用例）：
- TC-1：子账户同时在 2 个终端创建 booster 订单 → idempotency key 去重验证
- TC-2：父账户同时在 2 个浏览器 tab 对同一子账户续费 → subscription.expires_at 正确累加 / 去重
- TC-3：多个 SOP 实例并发扣分同一用户的 trial/cycle/booster → 优先级顺序 + 余额一致性
- TC-4：membership_event 2000 行并发写入 + 同时查询 → 无死锁 + 查询结果完整

运行方式：docker-compose MySQL 本地部署，60s 超时哨兵，任一死锁/超时 → P0 阻塞。

---

## 二、选择理由（为什么不能省）

会员积分体系是**最高风险业务逻辑**，涉及三个核心约束：

### 2.1 支付路径 — 不允许退款

- **决策**：spec §1.4 明确声明"**不实现退款**"
- **风险**：booster 购买经微信支付回调后 → fulfillOrder 更新 5 张表（subscription / trial_grant / credit_cycle / user_booster_balance / membership_event）
- **影响**：一旦扣减逻辑有 bug（如重复扣分、计数错误、余额不一致），**线上无法靠退款修复**，只能人工介入 + 手工退款 + 数据修复，成本极高
- **必须保护**：Playwright E2E 必须覆盖 booster 购买→支付回调→余额更新的完整链路，保证上线后不出幺蛾子

### 2.2 会员等级权限 — 控制物资流向

- **决策**：trial/pro 状态决定 booster 是否**冻结**（spec §1.1 I-11）；决定子账户能否消费月度 cycle（spec §3.3）
- **风险**：状态判定错误直接影响计费
  - 案例 A：trial 误判为过期 → booster 冻结 → 用户误以为丢积分 → 投诉
  - 案例 B：subscription.expires_at 边界判定错（闭区间 vs 半开区间）→ 用户在到期那一秒仍能用 → 漏计费
- **必须保护**：Playwright E2E 需精准验证边界（比如 expires_at = NOW()，到期那一刻 booster 必须冻结；重新开通后立即解冻）

### 2.3 B2B 关系 — 父账户代开通 + 月度对公结算

- **决策**：父账户有权为子账户 grant 会员（3 种包：trial / monthly / booster），子账户**不能自购会员**（ErrMembershipSelfPurchaseDisabled）
- **风险**：月末财务对账时，需按 granter_user_id 聚合每个父账户为其子账户花费的总额，用于对公结算
- **影响**：如果 membership_event 的 granter_user_id 记录错误、或者重复扣分导致 membership_event 多行，会直接影响对公发票金额
- **必须保护**：Playwright E2E 需验证 B2B grant 流程（父账户开通） + 存储的 granter_user_id 正确；Go 单元测试需验证 membership_event 幂等性（相同 idempotency_key 不重复 INSERT）

### 2.4 NDF Rule 10 硬性要求

NDF Rule 10 明确：
> 若功能涉及支付、权限、会员等级等高风险业务逻辑，reviewer 应要求写 **Playwright E2E**（持久化测试代码）。gstack /qa 仅一次性验证，不产生持久化测试代码，无自动化回归保护——不能替代 E2E。

**本功能命中所有三个风险点**，因此三件套（E2E + gstack + Go test）都不可缩减。

---

## 三、6 条关键用户路径

### 路径 1：父账户给子账户开通试用包（spec §9.4.1）

**场景**：B2B2C 场景，父账户为其子账户首次激活 trial。

**用户操作**：
1. 父账户登录
2. 进入「客户管理」
3. 找到 free 子账户，点击行的 action 菜单
4. 选「开通会员」
5. 弹出 GrantMembershipModal，默认选 trial（体验包）
6. 点「确认开通」，生成 Idempotency-Key（UUID）
7. API 调用 `POST /v1/users/children/:child_id/grant-membership { product_type:"trial" }`

**验证点**：
- **API 返回**：200，`code: 0, message: "ok"`
- **DB - trial_grant**：新增 1 行，`user_id=child_id, granter_user_id=parent_id, granted_at=now, expires_at=now+3d, credits_total=200, credits_remaining=200`
- **前端 - 管理端列表**：该行会员状态变为「试用中（2026-05-02 到期）」，蓝标，action 菜单变「续费 / 升级」
- **DB - membership_event**：新增 1 条，`type:'trial_granted', user_id=child_id, granter_user_id=parent_id, idempotency_key=<生成的UUID>, created_at=now`

**Playwright 实现**：mock parent/child 账户，调用 API，DB 查表，前端 navigate、locator 取值。

---

### 路径 2：父账户给同一子账户续费 Pro（trial+pro 叠加）（spec §9.4.2）

**前置条件**：路径 1 已完成，子账户当前有 active trial。

**场景**：trial 在期，父账户决定升级为 Pro（月订阅）。两者**并存不冲突**（spec §1.2 决策）——trial 优先扣，trial 到期清零。

**用户操作**：
1. 父账户登录
2. 进入「客户管理」
3. 找该子账户，点 action 菜单
4. 选「开通会员」（或「升级」）
5. GrantMembershipModal 切到「月订阅」标签
6. 输入「1」个月
7. 点「确认开通」

**验证点**：
- **API 返回**：200
- **DB - subscription**：新增 1 行，`user_id=child_id, status='active', first_started_at=current_started_at=now, expires_at=now+1month（用 anchor_add_months 算），total_months_purchased=1, granter_user_id=parent_id`
- **前端 - 管理端，父账户视角**：会员状态变为「试用中 + Pro 已开通」（紫色双标，spec §8.1.3）
- **前端 - 用户端，子账户视角**（登出父账户、用子账户身份进入 `/credits` 页）：仅显示「试用中（2026-05-02 到期）」，**遮蔽 Pro**（spec §8.1.4 I-2）
- **DB - membership_event**：新增 1 条，`type:'sub_granted', user_id=child_id, granter_user_id=parent_id, months_or_quantity=1, idempotency_key=<新UUID>`

**Playwright 实现**：登父账户、进管理端、操作 grant Pro；登出、登子账户、进用户端 `/credits` 验证仅显示 trial；DB 查 subscription + membership_event。

---

### 路径 3：子账户购买 booster + mock 支付回调（spec §9.4.3）

**场景**：用户在用户端 `/credits` 页购买加量包（booster）。

**技术约束**：生产支付走微信，E2E 无法真实支付。spec §9.4.3 给出二选一方案：
- **方案 A**：新增 admin 端点 `POST /v1/admin/test-only/fulfill-order/:order_id`（build tag gate `dev_qa_only`），E2E 直接调
- **方案 B**：环境变量 `NUMIND_E2E_BYPASS_PAY_SIG=1`（仅 dev/qa），支付回调检查 false，自动 fulfill
  
本 plan §Task 3 必须选定方案。E2E 用统一 helper `mockPayOrder(orderId string)` 封装，未来切方案不改测试代码。

**用户操作**（假设选方案 A）：
1. 子账户登录用户端
2. 进入「额度中心」（`/credits` 路由）
3. 点「购买加量包」卡片
4. 弹出购买 modal：数量输入框（默认 1）
5. 点「支付」（前端生成 Idempotency-Key）
6. API `POST /v1/orders { product_type:"booster", quantity:1, idempotency_key:... }`→ 返回 order，total_amount_cents=2990（1份×¥29.9）
7. **E2E 调用 helper**：`mockPayOrder(orderId)` → `POST /v1/admin/test-only/fulfill-order/:order_id`
8. 前端 poll 或 webhook 更新，余额卡片 booster 数值 += 600

**验证点**：
- **API - 创建订单**：`POST /v1/orders`，返回 200，order.total_amount_cents=2990
- **DB - order**：新增 1 行，status='pending'（支付前）
- **API - fulfill order**（mock）：`POST /v1/admin/test-only/fulfill-order/:order_id` 返回 200
- **DB - order**：status 变 'fulfilled'
- **DB - user_booster_balance**：该用户行的 balance += 600；若用户首次购买则新增 1 行
- **DB - membership_event**：新增 1 条，`type:'booster_purchased', user_id=user_id, months_or_quantity=1, idempotency_key=<订单生成时 key>`
- **前端 - `/credits` 页**：加量包余额卡片数值从 X 变成 X+600，toast「购买成功」

**Playwright 实现**：
```typescript
// 伪代码
test('booster purchase and mock pay', async ({ page, apiContext }) => {
  await loginAsUser(page, credentials)
  await page.goto('/credits')
  
  // 创建订单
  const orderRes = await apiContext.post('/v1/orders', {
    data: { product_type: 'booster', quantity: 1, idempotency_key: generateUUID() }
  })
  const orderId = (await orderRes.json()).data.id
  
  // Mock 支付
  await mockPayOrder(apiContext, orderId)
  
  // 验证 DB 和前端
  const balanceAfter = await checkBoosterBalance(db, userId)
  expect(balanceAfter).toBe(balanceBefore + 600)
  
  await page.reload()
  const displayedBalance = await page.locator('.booster-balance').textContent()
  expect(displayedBalance).toContain('600')
})
```

---

### 路径 4：单笔 booster 超 10000 拦截（spec §9.4.4）

**场景**：防止恶意大额订单或用户误操作。quantity 上限 10000。

**用户操作**：
1. 用户在购买 modal 中输入 10001
2. 前端校验触发：红框高亮 + 错误提示「单次最多购买 10000 份」+ 提交按钮 disabled

**验证点**（前端）：
- 输入框显示红框，错误文本可见
- 提交按钮 disabled

**验证点**（后端兜底）：
- Playwright 强行解除 disabled（`page.locator('button').evaluate(btn => btn.disabled = false)`），点击
- API 返回 400，`code: <ErrBoosterQuantityExceedsLimit>, message: "单次购买数量不可超过 10000"`
- **DB - order**：无新订单
- **DB - user_booster_balance**：balance 无变化

**Playwright 实现**：
```typescript
test('booster quantity limit', async ({ page, apiContext }) => {
  // 输入 10001
  await page.fill('.quantity-input', '10001')
  
  // 前端校验
  await expect(page.locator('.error-message')).toContainText('最多购买 10000')
  await expect(page.locator('button:has-text("支付")')).toBeDisabled()
  
  // 后端兜底：强制禁用 disabled
  await page.locator('button:has-text("支付")').evaluate(btn => btn.disabled = false)
  
  const response = await page.locator('button:has-text("支付")').click()
  // 期望 400 + ErrBoosterQuantityExceedsLimit
})
```

---

### 路径 5：会员到期 booster 自动冻结 UI（spec §9.4.5）

**场景**：用户 subscription 过期（或 trial 过期且无 pro），booster 余额**保留但冻结**（spec §1.1 I-11）。用户无法消费，UI 灰显并提示需要激活会员。

**Fixture**：
```sql
-- 子账户 user_id=C，subscription.expires_at=NOW()（即将过期边界）
-- user_booster_balance(user_id=C).balance = 600
```

**验证点**（前端 `/credits` 页）：
- 加量包余额卡片：数字 600 + 灰色样式 + 锁标（icon）
- 卡片下方文案：「需要开通会员后才能使用」
- 行内 CTA「立即开通会员」按钮，点击后跳转到「联系父账户」提示页（C 端 ErrMembershipSelfPurchaseDisabled，无法自购）
- 购买加量包卡片整体置灰 disabled，提交按钮 disabled

**验证点**（后端）：
- 调用 `POST /v1/orders { product_type:"booster", ... }` → 返回 400
- 错误码 `ErrNotActiveMember`，message: "需要激活会员后才能购买加量包"
- **DB - order**：无新订单
- **DB - user_booster_balance**：balance 无变化（保留不清零）

**Playwright 实现**：
```typescript
test('booster frozen when membership expired', async ({ page, db }) => {
  // fixture: set subscription.expires_at = NOW()
  await db.query(`UPDATE subscription SET expires_at=NOW() WHERE user_id=?`, [userId])
  
  await loginAsUser(page, childCredentials)
  await page.goto('/credits')
  
  // 验证 UI
  const balanceText = await page.locator('.booster-balance').textContent()
  expect(balanceText).toContain('600')
  expect(page.locator('.booster-card')).toHaveCSS('opacity', '0.5') // 灰显
  
  const lockIcon = page.locator('.booster-card .lock-icon')
  await expect(lockIcon).toBeVisible()
  
  const contactBtn = page.locator('.booster-card :has-text("立即开通会员")')
  await contactBtn.click()
  // 期望导航到 /contact-parent-for-membership 或类似提示页
  
  // 验证后端兜底
  const response = await page.context().request.post('/v1/orders', {
    data: { product_type: 'booster', quantity: 1 }
  })
  expect(response.status()).toBe(400)
  const body = await response.json()
  expect(body.code).toBe(ErrNotActiveMember)
})
```

**边界细节**（spec §1.3 I-3，半开区间 [start, end)）：
- `expires_at = 2026-05-02 10:00:00`，当前时刻 2026-05-02 09:59:59 → 仍 active
- `expires_at = 2026-05-02 10:00:00`，当前时刻 2026-05-02 10:00:00 → 已 expired（边界等值视为过期）
- 实现需用 `now < expires_at` 判定（不是 `now <= expires_at`）

---

### 路径 6（新增，覆盖 AC-16）：父账户两个 tab 同时续费，idempotency 矩阵

**场景**：验证 idempotency 幂等性。父账户在浏览器两个 tab 同时打开同一子账户的续费 modal，分别点击「确认续费」。

**用例 6a（不同 idempotency_key）**：
- Tab A 和 Tab B 各自生成不同 UUID → `POST /v1/users/children/:child_id/grant-membership` 两次，key 不同
- **期望行为**：两次都成功，subscription.expires_at 累加（`+1month` + `+1month` = `+2month`），total_months_purchased=2
- **验证**：
  - `subscription.expires_at` 增加 60 天（±1 天宽容，因月长不同）
  - subscription.total_months_purchased = 2
  - `membership_event` 新增 2 条 `sub_renewed`，idempotency_key 不同

**用例 6b（相同 idempotency_key）**：
- Playwright 拦截两次网络请求，强制使用同一个 Idempotency-Key（相同 UUID）
- **期望行为**：第二次请求返回 409 Conflict 或 200（幂等重放），subscription.expires_at 仅累加一次（`+1month`），total_months_purchased=1
- **验证**：
  - 第二次请求返回 409 或 200（spec §5.5 决策：应返回何状态码，S3 plan 明确）
  - subscription.expires_at 仅增加 30 天
  - subscription.total_months_purchased = 1
  - `membership_event` 仅 1 条 `sub_renewed`（UNIQUE(idempotency_key) 约束去重）

**Playwright 实现**（伪代码）：
```typescript
test('idempotency matrix - different keys', async ({ page, apiContext }) => {
  // 模拟两个 tab
  const page2 = await context.newPage()
  
  // Tab A: key1
  const key1 = generateUUID()
  const res1 = await apiContext.post(`/v1/users/children/${childId}/grant-membership`, {
    data: { product_type: 'monthly', months: 1, idempotency_key: key1 }
  })
  expect(res1.status()).toBe(200)
  
  // Tab B: key2（不同）
  const key2 = generateUUID()
  const res2 = await apiContext.post(`/v1/users/children/${childId}/grant-membership`, {
    data: { product_type: 'monthly', months: 1, idempotency_key: key2 }
  })
  expect(res2.status()).toBe(200)
  
  // DB 验证：expires_at += 2 month
  const sub = await db.query('SELECT * FROM subscription WHERE user_id=?', [childId])
  const deltaDays = (sub.expires_at - originalExpiresAt) / (1000 * 60 * 60 * 24)
  expect(deltaDays).toBeCloseTo(60, 1) // ±1 天
  expect(sub.total_months_purchased).toBe(2)
  
  const events = await db.query('SELECT * FROM membership_event WHERE idempotency_key IN (?, ?)', [key1, key2])
  expect(events.length).toBe(2)
})

test('idempotency matrix - same key', async ({ apiContext, page }) => {
  const key = generateUUID()
  
  // 第一次请求
  const res1 = await apiContext.post(`/v1/users/children/${childId}/grant-membership`, {
    data: { product_type: 'monthly', months: 1, idempotency_key: key }
  })
  expect(res1.status()).toBe(200)
  
  // 重放第二次（同 key）
  const res2 = await apiContext.post(`/v1/users/children/${childId}/grant-membership`, {
    data: { product_type: 'monthly', months: 1, idempotency_key: key }
  })
  // 幂等：返回 409 Conflict 或 200（取决 spec 决策）
  expect([200, 409]).toContain(res2.status())
  
  // DB 验证：expires_at 仅累加 1 month
  const sub = await db.query('SELECT * FROM subscription WHERE user_id=?', [childId])
  const deltaDays = (sub.expires_at - originalExpiresAt) / (1000 * 60 * 60 * 24)
  expect(deltaDays).toBeCloseTo(30, 1) // ±1 天
  expect(sub.total_months_purchased).toBe(1)
  
  const events = await db.query('SELECT * FROM membership_event WHERE idempotency_key=?', [key])
  expect(events.length).toBe(1) // UNIQUE 约束仅 1 行
})
```

---

## 四、S5 执行清单（Gate 检查项）

按 `.claude/rules/ndf-enforcement.md` 规则 6，所有项目**必须全部 PASS**。任一 FAIL → 阻塞 S6，回到 S4 修复 + 重新 review + 重新 S5。

### 后端
- [ ] `task lint` 全过（golangci-lint 0 warning）
- [ ] `go test ./...` 全过（轻量，~30s）
- [ ] `task test` 全过（race detection + coverage）
  - [ ] biz 层覆盖率 ≥ 80%
  - [ ] store 层覆盖率 ≥ 70%
  - [ ] 无 race condition 检出
- [ ] 4 条并发压测用例（spec §9.2）全过
  - [ ] TC-1 idempotency 去重（2 并发订单，相同 key 结果一致）
  - [ ] TC-2 父账户 2 tab 续费，不同 key 累加，相同 key 去重
  - [ ] TC-3 多 SOP 实例并发扣分（trial → cycle → booster 顺序正确，余额一致）
  - [ ] TC-4 membership_event 2000 行并发写 + 并发查（无死锁，查询结果完整）
  - 所有用例 60s 超时哨兵无触发

### 前端 - 用户端
- [ ] `cd numind-web-v3 && npm run lint` 0 error
- [ ] `npm run type-check` 0 error
- [ ] `npm run test -- --run`（vitest）全过
- [ ] E2E 6 条路径全过
  ```bash
  E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- membership-credits-redesign.spec.ts
  ```
  - 退出码 0
  - 6 个 test case 全 PASSED：路径 1~6

### 前端 - 管理端
- [ ] `cd numind-admin-web && npm run lint` 0 error
- [ ] `npm run type-check` 0 error
- [ ] `npm run test -- --run` 全过

### gstack /qa 浏览器 QA
- [ ] 6 个关键页面视觉对标（spec §9.5.2 清单）：
  - [ ] 用户端 `/credits` 页面（余额卡片、booster 购买卡片）
  - [ ] 用户端 booster 购买 modal
  - [ ] 管理端「客户管理」列表（会员状态列）
  - [ ] 管理端「GrantMembershipModal」（trial/monthly 标签页、输入框、按钮）
  - [ ] 用户端 booster 购买完成 toast
  - [ ] 用户端会员过期灰显状态
- [ ] 截图 baseline 提交到 git：`numind-web-v3/e2e/baselines/membership-redesign/`
- [ ] 已有 baseline 与新截图无预期外差异（或差异已人工批准）

### 迁移演练（本地 staging 演练，非生产）

#### dry-run 阶段
- [ ] 执行：`scripts/2026-05-XX-membership-redesign-migration/01-dry-run.sql`
- [ ] 输出：所有用户 delta = 0，TOTAL diff = 0（§6.2.1 条件）
- [ ] Go 校验脚本：`DIFFS DETECTED: 0`
- [ ] 报告归档：`docs/migration-runbook/2026-05-XX-dry-run-report.md`

#### apply 阶段
- [ ] 执行：`02-apply.sql` 在 staging MySQL 单事务提交
- [ ] 耗时 ≤ 5 分钟
- [ ] 备份表行数 = 源表行数（Step 0 校验）
- [ ] 执行 verify：`03-verify.sql` 全过（5 个 invariant 检查）
- [ ] 报告归档：`docs/migration-runbook/2026-05-XX-apply-report.md`

#### rollback 演练
- [ ] 执行 rollback 脚本：`04-rollback.sql`
- [ ] 耗时 ≤ 2 分钟
- [ ] git checkout 迁移前代码标签，启动应用
- [ ] 跑 1 次 SOP 扣分，验证老代码可正常运行（不卡在新表查询）
- [ ] 报告归档：`docs/migration-runbook/2026-05-XX-rollback-report.md`

### 可观测性回归（Langfuse trace 一致性）
- [ ] 跑 1 个 SOP 任务（fixture 用户 + 固定 SOP 模板），记录 trace ID
- [ ] Langfuse 中该 trace 的 generation 数 ≥ baseline（无削减）
- [ ] 所有 generation 的 `usage`（prompt_tokens, completion_tokens）字段完整，无缺失
- [ ] DeductCredits 操作（如果触发扣分）对应的 span 时间戳在 LLM generation **事务外**（spec §1.1 I-5）
- [ ] 异常路径（余额不足拒绝扣分）：trace 中对应 span output 含 `error: "ErrInsufficientCredits"`，level=ERROR
- [ ] trace 结构（span 层级、观测点）与迁移前无差异
- [ ] 与迁移前 baseline trace JSON 对比，无退化

---

## 五、问题分级与处理规则

按 `.claude/rules/ndf-enforcement.md` 规则 7：

### P0（Critical）
- 定义：功能完全不工作、数据一致性破坏、payment/billing 金额错误、booster 冻结判定错误
- 例子：subscription.expires_at 边界判定反向、membership_event idempotency_key 重复、DeductCredits 扣错表顺序
- **立即修复后重新验收**，不允许推迟

### P1（Important）
- 定义：功能基本可用但有缺陷、性能显著下降、UI 不符合规范、中间表字段缺失
- 例子：booster 购买成功但 toast 未显示、race condition 检出但未必现场复现、trial+pro 叠加逻辑有分支路径未覆盖
- **立即修复后重新验收**

### P2（Minor）
- 定义：细节优化、注释完善、非关键性能、文案精调
- 例子：错误消息文案可改进、测试 case 可更全面但已覆盖主流程、约束检查可更早失败但当前足够
- **能现修则现修**（当前修复成本最低，改日修复上下文丢失）
  - 仅当修复依赖外部条件（如等待另一功能上线）才记录推迟 + 注明理由
  - **禁止**无理由推迟

### 修复后重新进 S5

修复提交后：
1. 回到 S4（更新 manifest `stage: S4`）
2. 派 subagent 对修改内容跑 spec compliance + code quality 两阶段 review
3. review 全过后进 S5（更新 manifest `stage: S5`）
4. 重跑触发问题的 S5 执行项

---

## 六、S5 通过后产出物

### 6.1 验证报告
**文件**：`numind-server/docs/superpowers/specs/2026-04-29-membership-credits-redesign-s5-report.md`

**内容**：
- 执行日期、执行人、环境（local/staging）
- 各执行项结果（PASS / FAIL / SKIPPED，含输出摘要）
- 6 条 E2E 路径的截图证据
- 迁移 dry-run / apply / rollback 三份报告（嵌入或链接）
- Langfuse trace 对比（baseline vs 迁移后），JSON dump

### 6.2 Git 提交
- E2E 文件：`numind-web-v3/e2e/membership-credits-redesign.spec.ts` ✓（已在 S4）
- gstack baseline 截图：`numind-web-v3/e2e/baselines/membership-redesign/*.png`
- 迁移报告：`docs/migration-runbook/2026-05-XX-{dry-run,apply,rollback}-report.md`
- 验证报告：`docs/superpowers/specs/2026-04-29-membership-credits-redesign-s5-report.md`

### 6.3 Manifest 更新
```yaml
features:
  - id: membership-credits-redesign
    stage: S6  # 从 S3 → S4 → S5 → S6
    progress:
      total_tasks: 24
      completed_tasks: 24
      reviewed_tasks: 24
    last_verified:
      at: "2026-0X-XX..."
      s5_passed: true
      lint: pass
      type_check: pass
      tests: pass (biz 82%, store 75%)
      race: pass
      e2e: pass (6/6 paths)
      migration: pass (dry-run / apply / rollback)
      observability: pass
```

---

## 七、预期工作量与时间表

| 阶段 | 任务 | 预期耗时 |
|------|------|----------|
| 前期准备 | fixture DB setup、E2E 框架搭建 | 0.5h |
| E2E 编码 | 6 条路径 + helper 函数 | 4h |
| 单元测试 | biz/store 覆盖率冲刺、mock 补全 | 2.5h |
| 并发压测 | 4 条 TC 设计 + docker-compose + 调试 | 2h |
| gstack QA | 6 个页面截图 + baseline | 1.5h |
| 迁移演练 | dry-run / apply / rollback / 报告 | 2h |
| 可观测性 | trace 对比、JSON dump | 1h |
| **总计** | | **~13.5h** |

实际可能 ±50% 波动（如发现并发 bug 需调试）。

---

## 八、参考文献

- **NDF Rule 10**：`.claude/rules/ndf-enforcement.md` §Rule 10（S5 验证策略 + P0/P1/P2 分级）
- **设计 Spec**：`2026-04-29-membership-credits-redesign-design.md` §1.1（13 条 invariant）、§3（算法）、§4（并发）、§5（API）、§6（迁移）、§9（验证策略草稿）
- **实现 Plan**：`2026-04-29-membership-credits-redesign-plan.md` §Task 23（本 task）
- **测试规范**：`.claude/rules/testing.md` §1/2/2.5（Go unit test / Playwright E2E / gstack /qa）
- **业务逻辑**：`.claude/rules/business-logic.md` §1-5（tier/membership/credits）
- **前端 UX**：`.claude/rules/ui-ux.md` §1-5（4 态处理、dialog、表单验证）

---

**文档结束。**  
本策略文档作为 S5 gate 输入，S3 plan review 时由独立 reviewer 同步审查合理性。执行时按第四章清单逐项验收。
