# Chatbot 会话改名与置顶 — 提案 + PRD

> **状态**：S1 (待客户/产品 owner 确认提案)
> **NDF 版本**：1.1
> **创建日期**：2026-05-13
> **来自**：S0 requirement card (`requirements/chatbot-session-rename-pin.md`) + gstack `/office-hours` builder 模式 S1 产品思考

---

## §1 方案概述 [客户可见]

### 一句话描述

在用户端 chatbot 对话页（点进某一个 AI 助手后的对话窗口）的**左侧会话记录列表**里，给每条对话加上**改名**和**置顶**两个能力：

- **改名**：用户可以把"新对话"这种系统默认名字改成自己看得懂的标签，比如"客户 A 试用咨询"、"出海产品定价讨论"
- **置顶**：用户可以把重要的对话固定在列表顶部，不会被新对话挤下去

### 触发交互

每条对话**鼠标悬停时右侧显示一个「⋯」更多按钮**，点击弹出菜单含「重命名 / 置顶 / 取消置顶 / 删除」四项（"删除"原本就有，这次顺带统一进菜单里）。

### 排序规则

- 置顶的对话在顶部，按**最近一次置顶时间倒序**（新置顶的在最上面）
- 未置顶的对话在置顶组下方，**按最近活跃时间倒序**（和现在一样）

### 范围明确

- ✅ 只做用户端 chatbot 对话页（点进 AI 助手后那个聊天窗口）
- ❌ 不含销售知识库对话页（SalesView，那是另一个产品入口）
- ❌ 不含 SOP 运行历史
- ❌ 不含管理端

### 用户价值

1. **找回失踪的对话**：在同一个 chatbot 下积累几十条 session 后，重要对话被新建对话挤下去；置顶让用户保留 2-5 条核心对话长期固定在顶部
2. **看一眼就知道是哪个对话**：默认名"新对话"无法识别，改名让用户用自己的语言标注
3. **基础完成度对齐**：微信、ChatGPT、Claude、飞书等所有主流对话产品都有这两个能力，缺失会被感知为"产品不够成熟"

---

## §2 报价与周期 [客户可见]

- **预估工作量**：**1-1.5 工作日**（已经走 MVP 压缩路径；详见 §4）
- **报价**：N/A — 内部产品 backlog 项，不涉及外部客户报价
- **交付时间线**：S1 通过后，预计：
  - S2 spec：0.5 天
  - S3 plan：0.5 天
  - S4 实施 + review：1-1.5 天
  - S5 自动验收：0.5 天
  - S6 人工验收 + S7 部署：0.5 天
  - **总计：约 3-4 工作日跨越整个 S1→S7**（其中纯实施 S4 占 1-1.5 天）

---

## §3 技术可行性 [AI 内部]

### 现有功能复用（高复用率）

| 现有能力 | 在本 feature 中的角色 |
|---------|---------------------|
| `chatbot_session.title` 字段（`size:200`）| 改名直接覆盖此字段，**零 schema 变更** |
| `chatbot_session.deleted_at` 软删除（GORM） | 软删 session 自然不出现在排序列表，无需特殊处理 |
| `chatbot_session.updated_at` 字段 | 现有列表排序键，本 feature 保留为"非置顶组"次序 |
| 用户端 `chatbotSessions` 计算属性 (`ChatbotChat.vue:50-52`) | 已经做了 client-side filter (chatbot_id)，本 feature 把 filter **下推到后端** |
| 现有 `deleteChatbotSession` API + UI 流程 | 本 feature 顺带把"删除"按钮统一搬入新「…」菜单 |
| `ConfirmModal` 组件 | 改名输入弹窗直接复用，**无需新建 RenameModal** |
| Pinia `useChatbotStore` setup 模式 | 加 `renameSession` / `togglePin` actions，遵循现有 pattern |

**复用率结论**：本 feature 是高复用率项目，**唯一新增**的持久化字段是 `pinned_at TIMESTAMP NULL`（单字段承担"是否置顶"+"置顶时间排序"双重信息）。

### 技术风险

| 风险 | 等级 | 缓解 |
|------|------|------|
| GORM `default:` 标签陷阱（database.md §6 记录的踩坑） | **Low** | 本 feature 新增字段为 nullable `*time.Time`，**不存在** default:true bool 的陷阱。改名操作用 `UpdateColumn` 显式列更新，绕过 `updated_at` 自动刷新 |
| 排序 SQL 性能（pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC） | **Low** | session 表按 user_id+chatbot_id 已有 index；置顶记录数在单 chatbot 内通常 <20，全表扫描代价极低；不需额外 index |
| 并发改名（两 tab 同时改）| **Low** | 业务可接受 last-write-wins，不需 ETag。store 用 `UpdateColumn("title", ...)` 即可 |
| 跨 chatbot session 列表 perf bug（顺带修复）| **Mitigation, not risk** | 当前 `ListSessions` 返回用户所有 chatbot 的 session client-filter — 本 feature 顺手加 `chatbot_id` query 参数下推到 DB，**这是隐藏价值** |

### 涉及仓库

- [x] **numind-server**（Go 后端）— migration + model + store + biz + controller + router
- [x] **numind-web-v3**（Vue 用户前端）— api + store + ChatbotChat.vue 内联交互
- [ ] numind-admin-web（管理端）— **不涉及**

### AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：**否** （本 feature 仅 session metadata CRUD，不触发任何 LLM/Embedding/Rerank/OCR/ASR 调用）
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：N/A

### 工作量估算（按仓库 / 按阶段）

| 阶段 | 工作量 | 内容 |
|------|--------|------|
| S2 spec | 0.5 d | 写 design spec：DDL、API 契约、SQL 排序、前端交互细节、边界 case |
| S3 plan | 0.5 d | 拆 4-6 个 task：migration / store+biz / controller+router / api+store / UI |
| S4 实施 | 1-1.5 d | 后端 ~3-4 个 commit + 前端 ~2-3 个 commit；每 task 后两阶段 review |
| S5 验收 | 0.5 d | 本地 task lint + go test + npm lint + type-check + Playwright E2E 关键路径 + Langfuse 回归（NO_LLM_CHANGE 仍要快速 sanity） |
| S6+S7 | 0.5 d | merge develop → CI dev → 人工验收 → release/tag → prod |
| **总计** | **3-4 d** | 跨越 S1→S7 完整流程；其中纯 S4 编码为 1-1.5 d |

---

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### §4.0 S1 必决策项的明确答案（来自 S0 reviewer 升级）

| 决策项 | 锁定答案 | 理由 |
|--------|---------|------|
| **D1 置顶/排序作用域** | **per-chatbot**（本期 + 顺带修后端 perf bug） | 用户原话"在某个 chatbot 页面"；置顶语义只在同一 chatbot 内才有意义；顺带加 `chatbot_id` query 参数修 ListSessions 的 N 倍数据返回 perf 问题 |
| **D2 改名/置顶是否更新 `updated_at`** | **不更新** | metadata 操作不应影响活跃排序；用 `UpdateColumn("title", ...)` 和 `UpdateColumn("pinned_at", ...)` 显式列更新，绕开 GORM `updated_at` 自动刷新 |
| **D3 置顶数量上限** | **v1 不设上限**（MVP 压缩） | 用户基数小，滥用风险极低；v1 不限省 ~0.5 d 工作量（无需 ErrPinLimitExceeded errno + 前端边界 UI）；如未来 UI 出现真实拥挤再加硬上限 + 折叠组 |

### §4.1 用户故事

- 作为 **C 端父账户用户**，我需要 **改名当前 chatbot 下的某条对话**，以便 **用我自己的语言标注它，下次能快速识别（"哦这是关于客户 A 的那次咨询"）**
- 作为 **C 端父账户用户**，我需要 **置顶当前 chatbot 下的某条对话**，以便 **它不会被新建对话挤到列表底部**
- 作为 **C 端父账户用户**，我需要 **取消置顶某条对话**，以便 **它回到按活跃时间排序的常规组**
- 作为 **C 端父账户用户**，我希望 **改名和置顶通过 hover 显示「⋯」按钮触发**，以便 **常态下列表不被次要 UI 元素污染**
- 作为 **C 端父账户用户**，我希望 **删除按钮也搬入这个「⋯」菜单**，以便 **会话级操作统一在一个入口**

### §4.2 验收标准（具体、可度量）

#### AC-1 改名

- [ ] **AC-1.1** `PUT /v1/chatbot/sessions/:id/rename` 端点存在，请求 body `{"title": "..."}`，响应 `{"code": 0, "data": {"id": N, "title": "..."}}`
- [ ] **AC-1.2** title 长度限制：trim 后 1~200 字符；空字符串或纯空白字符返回 `ErrBind` "标题不能为空"
- [ ] **AC-1.3** 非本人 session 返回 403（`ownership check by user_id`）
- [ ] **AC-1.4** 改名成功后 `updated_at` **不变**（验证 SQL：改名前查 updated_at；改名后再查；应相等）
- [ ] **AC-1.5** 前端：hover session 显示「⋯」按钮 → 点击弹下拉菜单 → 点「重命名」→ 弹出输入弹窗（复用 `ConfirmModal`）→ 输入新名 → 确认 → 列表中该 session title 立即更新（optimistic UI + revalidate）

#### AC-2 置顶

- [ ] **AC-2.1** `PUT /v1/chatbot/sessions/:id/pin` 端点存在，请求 body `{"pinned": true | false}`，响应 `{"code": 0, "data": {"id": N, "pinned_at": "2026-05-13T10:00:00+08:00" | null}}`
- [ ] **AC-2.2** `pinned: true` → 写入 `pinned_at = NOW()`；`pinned: false` → 写入 `pinned_at = NULL`
- [ ] **AC-2.3** **重复置顶（已置顶 session 再点击置顶）**：刷新 `pinned_at = NOW()`（实现"置顶时间倒序"= 最近一次置顶操作在前）
- [ ] **AC-2.4** 非本人 session 返回 403
- [ ] **AC-2.5** 置顶/取消置顶成功后 `updated_at` **不变**
- [ ] **AC-2.6** 前端：菜单显示「置顶」（当前非置顶时）或「取消置顶」（当前已置顶时）；点击后列表立即重新排序

#### AC-3 列表排序与作用域

- [ ] **AC-3.1** `GET /v1/chatbot/sessions?chatbot_id=N&page=1&page_size=20` 端点支持新 query 参数 `chatbot_id`
- [ ] **AC-3.2** 缺 `chatbot_id` 时**保持现有行为**（cross-chatbot 混排，向后兼容；前端调用方主动传入 `chatbot_id` 才启用新行为）
- [ ] **AC-3.3** 排序 SQL：`ORDER BY pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC`（置顶组在前按 pinned_at DESC，非置顶组在后按 updated_at DESC）
- [ ] **AC-3.4** 前端 `chatbotSessions` 计算属性**移除 client-side `.filter(s => s.chatbot_id === chatbotId.value)`**，因为后端已经按 chatbot_id 过滤；保留可选的 client-side dedupe 防御
- [ ] **AC-3.5** 列表 UI 上**置顶组与非置顶组之间无视觉分隔线**（v1 MVP；如未来出现拥挤再加 v2 分组）

#### AC-4 删除（顺带整合）

- [ ] **AC-4.1** 现有 `DELETE /v1/chatbot/sessions/:id` 行为不变
- [ ] **AC-4.2** 前端「删除」按钮从原位置（hover 直接出现的 trash icon）**迁入新「⋯」菜单**，复用现有 `ConfirmModal` 删除确认流程
- [ ] **AC-4.3** 软删除的 session 即使曾被置顶，列表也不显示（现有行为，`gorm.DeletedAt` 自动过滤）

### §4.3 边界情况

| 情况 | 处理 |
|------|------|
| 改名传入超长 title（>200） | 后端 `ErrBind` "标题最长 200 字符" |
| 改名传入纯 emoji 或纯标点（合法 UTF-8）| 通过；不做"必须含字母"等限制 |
| 改名/置顶请求时 session 已被删除 | 返回 404 `ErrNotFound` "会话不存在" |
| 同一 session 短时间内两次置顶（防抖前的双击） | 两次 `pinned_at` 都写入，后者覆盖前者；语义安全 |
| 用户 A 改用户 B 的 session | 403（biz 层 ownership 校验） |
| 跨设备同步（A 设备置顶 → B 设备）| 最终一致，无 websocket 推送；B 设备下次 `fetchSessions()` 时看到（**接受**） |
| 父子账户 | session 是个人级数据 (`user_id` 维度)，**不涉及** B2B2C 共享；父账户看不到子账户 session，子账户看不到父账户 session（现有行为，本 feature 不改） |
| 并发改名（两 tab）| last-write-wins，无 ETag（接受） |

### §4.4 权限规则

- 用户端 `user_token` 中间件保护；**所有用户等级**（free/trial/standard/premium/credits）均可使用
- ownership 检查：仅 `session.user_id == ctx.userID` 的用户可改名/置顶/删除该 session
- 管理端：**不涉及**
- 与 `child-run-permission` 共存：本 feature 是 session-level metadata 操作，**不**触发运行权限检查
- 与 `sop-chatbot-visibility-scope` 共存：本 feature 作用在 session list 层，**不**与 chatbot list 的 visibility 过滤交叉（reviewer 已验证）

### §4.5 UI 行为规格

- **页面位置**：`numind-web-v3/src/views/chatbot/ChatbotChat.vue`，左侧 `.sidebar > .session-list`
- **布局要求**：现有列表布局完全保留，仅增加：
  - 每个 session item 右侧 hover 时显示 `<button>` 按钮（22×22px，灰色 `⋯` icon）
  - 点击按钮弹出 dropdown 菜单（绝对定位于按钮下方，content: 重命名 / 置顶 (or 取消置顶) / 删除）
- **交互模式**：
  - hover session item → 「⋯」按钮 fade-in（CSS transition 150ms）
  - 点击「⋯」→ menu open；点击 menu 外 → menu close（document click listener）
  - 点击「重命名」→ menu close，弹出 ConfirmModal（type=input）输入新 title，回车或点确认 → 调用 API
  - 点击「置顶/取消置顶」→ 立即调用 API（无二次确认），optimistic UI 立即重排
  - 点击「删除」→ 现有 ConfirmModal 删除确认流程（保留现状）
- **状态处理**：
  - **loading**（API 调用中）：菜单项灰显，防重复点击
  - **empty**（无 session）：现有 empty state 不变
  - **error**（API 失败）：toast 显示 `response.data.message`，optimistic UI 回滚

### §4.6 不在本 feature 范围内（重要 — 防 scope creep）

- ❌ 销售知识库对话页（SalesView.vue）的 rename/pin
- ❌ SOP 运行历史的 rename/pin
- ❌ Session 全文搜索功能（如本期用户反馈"找不到 session"频次高，单独立 feature 评估）
- ❌ 跨 chatbot 全局 session 索引页（如有需求，单独 feature）
- ❌ 置顶硬上限 / 折叠分组（v1 不限；如未来真实出现拥挤再 v2 加）
- ❌ SessionContextMenu 抽离为可复用组件（v1 内联在 ChatbotChat.vue 即可；如其他页要复用再单独抽）
- ❌ 改名审计日志（频次高且无合规需求，不写 audit）
- ❌ 多端 websocket 实时同步（接受最终一致）

---

## §5 office-hours 思考记录（供 S2 spec / S3 plan 阶段参考）

### Phase 3 前提挑战结论

| Premise | 用户立场 | 落入 PRD 的处理 |
|---------|---------|---------------|
| **P1** rename+pin 是 MVP，search 作为 v2 预留 | **拒绝**（用户认为原意就是 rename+pin，不预设 v2） | §4.6 标注 search "不在本 feature 范围内"；不写"v2 自然延伸"等暗示性语言 |
| **P2** per-chatbot scope + 顺带修 ListSessions perf bug | **接受** | §4.0 D1 锁定 per-chatbot；AC-3 含 `chatbot_id` 参数验收 |
| **P3** 现在做 + MVP 压缩到 1-1.5 天 | **接受** | §4.0 D3 锁定不设置顶上限；§4.6 标注组件抽离/错误码集中不在本 feature 范围 |

### Phase 4 方案对比结论

| Approach | Effort | Completeness | 选择 |
|----------|--------|--------------|------|
| **A 极简 MVP** | 1-1.5 d | 8/10 | ✅ **锁定** |
| B 健壮版（S0 原方案）| 2-2.5 d | 10/10 | ❌ 与 P3 MVP 压缩冲突 |
| C 三 icon hover | 1.5 d | 6/10 | ❌ 偏离用户 Triage 阶段明确选的「⋯」menu |

---

## §6 开放问题（S2 spec 阶段必答）

1. **改名输入弹窗 UX 细节**：复用现有 `ConfirmModal` 还是临时 type=input 模式？前端模式细节由 S2 锁定
2. **排序 SQL 数据库兼容性**：`ORDER BY pinned_at IS NULL ASC` 在 MySQL 8 上验证可用（NULL 排序行为依赖 dialect），S2 需要验证 prod 用 MySQL 版本
3. **toggle pin 按钮文案**：是 "置顶" / "取消置顶" 二态切换，还是统一 "Toggle 置顶"？S2 锁定
4. **菜单组件实现细节**：是用 native `<div>` + Vue 的 v-show + document click listener，还是直接复用现有的 `Dropdown.vue`（如有）？S2 决定

---

## §7 成功标准（S6 验收时验证）

- [ ] 用户在 ChatbotChat.vue 左侧 hover 任意 session 看到「⋯」按钮，点击弹菜单
- [ ] 重命名功能可正常工作，新名立即显示在列表
- [ ] 置顶/取消置顶可正常工作，列表立即按新规则排序
- [ ] 软删除功能在「⋯」菜单内可用，行为与现状一致
- [ ] 后端 ListSessions API 在 `chatbot_id` 参数下只返回该 chatbot 的 session（perf bug 顺带修复验证）
- [ ] 改名/置顶/取消置顶**不刷新** session 的 `updated_at`（验证：操作前后查 `updated_at` 应相等）

---

## §8 依赖与阻塞

- ✅ 无外部 blocker
- ✅ 不依赖任何未上线 feature
- ⚠️ 与 `sop-chatbot-visibility-scope` 共存：本 feature S4 期间，visibility-scope 也在 S4，二者并行无 git 冲突（不同文件路径），但 manifest 同步要注意（用 worktree 隔离已验证可行）
