# Chatbot 会话改名与置顶

## 来源
- 提出人：用户（产品 owner）
- 提出日期：2026-05-13

## 需求描述

原话：

> 在某一个具体的 chatbot 页面，左边是对话记录的列表，这些列表的名称需要能够支持改名和置顶

结构化描述：

在用户端 chatbot 对话页 (`views/chatbot/ChatbotChat.vue`) 的左侧会话列表中，每条会话支持：

1. **改名**：用户可以修改会话名称（覆盖 `chatbot_session.title`，复用现有字段）
2. **置顶**：用户可以置顶任意条数的会话；置顶组排在非置顶组前面，组内按"置顶时间"倒序；非置顶组延续现有"按 updated_at 倒序"排序
3. **触发交互**：每条 session hover 时右侧显示「…」更多按钮，点击弹出下拉菜单含「重命名」「置顶 / 取消置顶」「删除」（"删除"为现有功能，本次顺带统一进入菜单）

## 业务目标

1. **高频会话快速定位**：用户在同一个 chatbot 下会积累几十甚至上百条会话，按 updated_at 排序后高频/重要会话很容易被新建会话挤下去；置顶让用户保留 2-5 条核心会话长期固定在顶部
2. **改善会话识别**：当前会话默认名称只是 `'新对话'`（无 title 时）+ 系统生成的 title，用户无法在视觉上快速识别"这个是关于 X 客户的对话"；改名让用户用自己的语言标注
3. **对齐主流聊天产品的心智**：微信、ChatGPT、Claude、飞书所有主流对话产品都有置顶/重命名能力，缺失会被感知为产品基础完成度不足

## 优先级

中。是 UX 加速器和基础完成度补全，不阻塞业务流程；但属于聊天类产品的基础功能，长期缺失会持续累积小摩擦。

## Triage

- **推荐轨道：Standard**
- **分类理由**（5 条标准逐条）：
  1. 数据库 schema 变更：**是**（`chatbot_session` 加 `pinned_at` 字段，nullable timestamp；置顶 = 写入时间戳，取消 = 置 NULL；同时承担"是否置顶"和"置顶时间排序"双重信息，避免 bool + timestamp 双字段冗余）
  2. 新增 API 端点：**是**（`PUT /v1/chatbot/sessions/:id/rename` + `PUT /v1/chatbot/sessions/:id/pin`，两端点；现有 `GET /v1/chatbot/sessions` 列表查询的 ORDER BY 子句需更新）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（numind-server: model + store + biz + controller + router + migration ≈ 6 文件；numind-web-v3: api/chatbot.ts + stores/chatbot.ts + ChatbotChat.vue + 可能新增一个 SessionContextMenu 组件 + types/config.ts ≈ 4-5 文件）
  5. 高风险业务逻辑（支付/权限）：**否**（仅会话级 metadata 操作，无积分/权限耦合）
- **人类决定**：**确认 Standard**。走 S0→S7 完整流程。

## 范围锁定（Triage 阶段已确认）

- **目标页面**：仅用户端 chatbot 对话页 (`numind-web-v3/src/views/chatbot/ChatbotChat.vue`)。**不含** SalesRAG 销售对话页 (`SalesView.vue`)，即使语义类似也作为独立功能后续单独提出
- **目标实体**：仅 `chatbot_session`。**不含** `sop_run`（SOP 运行历史不在本次范围）
- **置顶规则**：可置顶任意多条；置顶组按"最近一次置顶操作"时间倒序；新置顶的会插到置顶组顶部
- **触发交互**：hover 显示「…」按钮 + 下拉菜单（不是右键，不是 inline 双击编辑）；下拉菜单与"删除"共用同一个组件

## S1 必决策项（PRD 阶段必须明确，不可推到 S2）

> 以下来自 S0 reviewer subagent 发现，从"待探索"升级为"必决策"。S1 PRD 的 §AC（验收标准）节必须为每条给出明确答案。

1. **【关键】置顶/排序作用域：per-chatbot 还是 cross-chatbot？**
   - 后端现有 `ListSessions` (store/chatbot_session.go:54) 仅按 `user_id` 过滤，**返回用户在所有 chatbot 下的会话混合列表**
   - 前端 `ChatbotChat.vue:50-52` 用 `computed` + client-side `.filter(s => s.chatbot_id === chatbotId.value)` 在浏览器侧筛出当前 chatbot 的 sessions
   - 用户语义大概率是 **per-chatbot**（"在某一个具体的 chatbot 页面"原话），但需要 S1 显式锁定。如锁定 per-chatbot，则后端列表 API 必须新加 `chatbot_id` 查询参数，并把 client-side filter 下推到 DB（顺带修复性能问题：N 个 chatbot 时返回 N 倍数据）
   - 如锁定 cross-chatbot，则置顶语义变为"全局会话置顶"，UI 需要在 session 卡片上标明所属 chatbot
   - **建议：per-chatbot**（贴近用户原话、性能更好、置顶语义更直观）

2. **改名/置顶时是否更新 `updated_at`？**
   - 建议：**不更新**。改名/置顶是 metadata 操作，不应让会话刷到非置顶组顶部
   - 但需在 S1 锁定，因为有用户心智冲突风险（改名后用户可能期待"刷"到顶部）
   - 实现层面：store 方法用 `UpdateColumn`（GORM 跳过 updated_at 自动刷新）

3. **置顶数量上限**
   - 建议：硬上限 **20 条**。理由：超过 20 条置顶组会占满侧边栏导致非置顶组完全不可见；20 条是聊天产品行业惯例（微信/钉钉同量级）
   - 触达上限时返回 `ErrPinLimitExceeded` 错误码
   - 或锁定为"不设上限"+ 前端 UI 加置顶组折叠（更复杂，不推荐 S1 选）

## 待 S1 探索的边界 case（次要，可推到 S2）

- 改名的长度上限：复用现有 `title` 字段 `size:200`，前端是否再硬截更短（如 50/100 字）？空字符串是否合法（建议：trim 后空 = 还原默认"新对话"）？
- 会话被删除（软删 `DeletedAt`）时 `pinned_at` 是否保留？（建议：保留原值，软删自然不出现在列表）
- 改名是否要写审计日志？（建议：不需要，会话名修改频次高且无安全合规需求）
- 多端同步：用户在 A 设备置顶，B 设备同步刷新需要时间——可接受最终一致（无 websocket 推送）
- 父子账户的可见性：会话属个人级数据（`user_id` 维度），不涉及父子账户共享——确认无 B2B2C 复杂度
- 并发改名冲突：两个 tab 同时改名最后写入胜出（last-write-wins），无需 ETag

## S0 Review 记录

- 2026-05-13 独立 reviewer subagent（Sonnet，NDF Rule 6 同款独立审查模式）审查通过：VERDICT = PASS_WITH_CONCERNS，0 P0，3 P1，2 P2
- P1 全部接受并写入本卡片"S1 必决策项"节
- P2 之 manifest current_task 措辞过时已修正
- P2 之后端文件数估算（6 → 6-7）记录但不修，S1 工作量估算时校准
