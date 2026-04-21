# HomeView 统一 Locked 卡片 UI — 提案

## §1 方案概述 [客户可见]

销售智能体被父账号关闭时，HomeView 首页的卡片会显示一把锁；但被关闭的"智能体（chatbot）"或"SOP 工作流"在首页**直接消失**，子账号都不知道有这个能力存在。

本方案统一 3 类卡片的视觉语言：
- **所有被拒资源都在首页正常显示**（不隐藏、不变灰）
- **右上角加一把锁**（复用当前销售智能体锁的样式）
- **点击 → 弹"未开通，请联系管理员"**（保持现状）

父账号自己永远看得到全部，不受影响。

## §2 报价与周期 [客户可见]

- 预估工作量：**0.5 天**（现状超出预期地接近目标——后端无需过滤逻辑改动，只是 DTO 加字段）
- 报价：对内需求，不单独计价
- 交付时间线：当天完成 S0 → S6

## §3 技术可行性 [AI 内部]

### 现状大发现（S1 阶段核实后更新了 S0 Triage 假设）

| 资源 | 后端 list 行为（当前） | 前端点击行为（当前） |
|------|----------------------|-------------------|
| SOP `/v1/sop/templates` | **返回全部**（HomeView.vue:338-362 直接 map，不过滤） | 点击调 `/sop/templates/:id/check-permission`（HomeView.vue:324） |
| chatbot `listVisibleChatbots` | **返回全部 published**（test 注释："与 SOP 对称，不按白名单隐藏"，`biz/chatbot/chatbot_test.go:5`） | 点击调 `checkChatbotPermission(id)`（HomeView.vue:417） |
| sales-agent（固定单卡片） | N/A | `mounted` 时查 `checkSalesPermission()` → `hasSalesPermission` state |

**原 Triage R1 / R3 作废**：后端本来就不过滤。child-run-permission 2026-04-20 实际的设计是"子账号看到全部 published，点击时 gate"，不是"按白名单过滤列表"（我之前读 spec 时误解了）。

### 真正的改动 delta

| # | 项 | 改动 | 原因 |
|---|-----|------|------|
| C1 | `GET /v1/sop/templates` DTO | 每个 template 加 `has_permission: bool` 字段 | 让前端不用对每个卡片循环调 `/check-permission` (N 次 round-trip 慢) |
| C2 | `listVisibleChatbots` DTO | 每个 chatbot 加 `has_permission: bool` | 同上 |
| C3 | HomeView SOP `v-for` | 加 `:class="{ 'no-permission': !workflow.hasPermission }"` + lock badge 条件渲染 | 复用 sales-agent 现有模式 |
| C4 | HomeView chatbot `v-for` | 同 C3 | 同上 |
| C5 | HomeView CSS `.feature-card.no-permission` | **删除** `opacity: 0.5` + `filter: grayscale(0.35)`（HomeView.vue:535-536） | D5 修正灰化 |
| C6 | TS types（`api/sop.ts`、`api/chatbot.ts`） | 加 `has_permission: boolean` 到 response 接口 | type-check 通过 |

### 技术风险（大幅缩小）

| # | 风险 | 缓解 |
|---|------|------|
| R1 | batch `has_permission` 计算 N+1 查询 | 子账号：一次 `SELECT template_id FROM user_template_permission WHERE sub_user_id = ? AND deleted_at IS NULL` → Go set，O(N) 本地比对；父账号：跳过查询 fill `true`。两端都是 1 query |
| R2 | API 破坏性变更 vs 新字段 | **加字段非破坏**：Go JSON marshaling 忽略老前端未消费的字段；前端 TS 接口加可选字段 `has_permission?: boolean` 兼容本次未发布到 prod 时的 dev 版本 |
| R3 | `has_permission` 字段名一致性 | 对齐既有 `/check-permission` 端点已用的 `{has_permission: bool}` 响应（sales_rag.go:1037），同名语义 |
| R4 | 点击时 `/check-permission` 二次查询是否冗余？ | **保留**作为防御纵深：list 返回瞬态、点击真执行之间有时间窗（父账号可能刚撤权），二次 check 是 race-guard。参考 sales-agent 模式已 proven 可用 |

### 涉及仓库
- [x] numind-server（后端 DTO 改动）
- [x] numind-web-v3（前端 HomeView + API type）
- [ ] numind-admin-web

### AI 可观测性
**N/A** — 本 feature 是权限状态展示，不触发任何 LLM 调用

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

- **作为未授权子账号**，我希望在首页看到**所有**父账号允许下来的能力（包括我没权限的，带锁标识），而不是被莫名其妙地"隐藏"，这样我知道该去找父账号开通哪些
- **作为父账号**，我不应该看到任何锁标识（我永远有全部权限），UX 与当前一致
- **作为子账号点击锁卡片**，应该得到清晰的"未开通，请联系管理员"提示（**保持现有行为不变**）

### 验收标准

- [ ] **AS-1** 父账号访问 `/` → 3 类卡片均正常渲染，无任何 lock badge
- [ ] **AS-2** 子账号无 sales_agent 授权 → sales-agent 卡片**正常颜色**（不变灰）+ 右上角 lock badge
- [ ] **AS-3** 子账号无某 chatbot 授权 → 该 chatbot 卡片**正常颜色** + 右上角 lock badge
- [ ] **AS-4** 子账号无某 SOP template 授权 → 该 SOP 卡片**正常颜色** + 右上角 lock badge
- [ ] **AS-5** 子账号点击任何 locked 卡片 → 弹出 permission modal 显示相应提示（保持现有 modal 样式）
- [ ] **AS-6** 子账号点击 unlocked 卡片 → 正常进入该页面（无回归）
- [ ] **AS-7** 后端 `/v1/sop/templates` 返回 payload 每项含 `has_permission: boolean`（父账号全 true）
- [ ] **AS-8** 后端 `GET /v1/chatbot/list`（或等效）返回 payload 每项含 `has_permission: boolean`（父账号全 true）
- [ ] **AS-9** 点击时的二次 `/check-permission` 调用**保留**（race-guard），行为无变化
- [ ] **AS-10** HomeView CSS 中 `.no-permission { opacity / grayscale }` 样式已删除（grep 结果空）

### 边界情况

- **E1** 父账号撤权瞬间，子账号首页已加载（显示 unlocked）→ 点击时 `/check-permission` 返回 false → 弹提示，不进入页面（race-guard 生效）
- **E2** 未登录用户访问 `/` → 被 router guard 重定向到 `/login`，不适用（与本 feature 无关）
- **E3** list API 返回时 `has_permission` 字段缺失（老后端 + 新前端组合）→ 前端用 `?? true` fallback（避免意外 lock-all），与"向后兼容"原则一致
- **E4** 超多 locked 卡片（父账号授权很少）→ 首页布局是现有 flex-wrap，多个卡片自然换行，无 UX 塌陷

### 权限规则

- **父账号**（`parent_user_id IS NULL`）：`has_permission` 永远 true（biz 层复用现有 `HasTemplatePermission` / `HasChatbotPermission` / `HasFeaturePermission` 的父账号分支）
- **子账号**：按对应白名单表判断
- **安全 gate 不变**：运行端点（`POST /v1/sop/run`, `/v1/chatbot/*/chat`, `/v1/sales-rag/*`）的 middleware / biz 层 permission check 全部保留，本 feature 只是**增加可见性信号**，不改安全边界

### UI 行为规格

- **页面位置**：`/`（HomeView）
- **布局要求**：flex-wrap 卡片网格（现有，不动）
- **Lock badge 位置**：卡片右上角（复用 sales-agent 现有 `.lock-badge` SVG + CSS，line 111-134 + 585）
- **交互模式**：点击卡片 → `showPermissionModal = true` + `permissionMessage`（已存在）
- **状态处理**：
  - loading：`launchingWorkflowKey === ...` 已存在
  - empty：空状态（模板/chatbot 列表空）已存在
  - error：fetch 失败时 `console.error` 已存在
  - permission-denied：**本 feature 新加** — 卡片正常色 + lock badge
  - permission-granted：当前行为不变

## §5 预期改动面（粗估）

- 后端：2 个 DTO + biz 层 batch permission 计算辅助（可复用 store 的 `ListUserTemplatePermissions` / `ListUserChatbots`）→ ~60 行 Go 代码
- 前端：HomeView.vue 3 处小改（SOP loop + chatbot loop + CSS 删除）+ 2 个 type 补字段 → ~40 行 TS/Vue
- 测试：2 后端单测（SOP DTO fields、chatbot DTO fields）+ 1 前端 E2E（HomeView denied cards 可见 + lock + 点击）
- **总计 ~100 行有效改动 + ~150 行测试**
