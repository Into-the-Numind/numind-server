# 即时标题 UX 改造 + 删除 bug 修复 — 设计+计划 (S0-S3 合并)

- Feature: `instant-title-ux`
- 日期: 2026-06-16
- 仓库: numind-server, numind-web-v3
- Track: Standard
- 前置: 承接 adaptive-session-titles（标题功能已上 dev）

## §1 需求（用户 2026-06-16）

1. **延迟建会话 + 发送时秒生成标题（chatbot + agent 一致）**：点"新对话"→ 只显示空白对话页，**左侧列表先不出现新项**；用户输入 prompt 点发送 → 立刻在左侧加一个**带微弱闪动动画的占位符**（代表标题生成中）+ 并行调用快速小模型从 **prompt** 秒生成标题 → 拿到后更新占位符；同时正常流式回答。
2. **修复 chatbot 删除会话功能**（dev 上不生效）。已取证：后端 DELETE 正常（HTTP200，行真删），**是前端问题**，须 Playwright/浏览器诊断。
3. **标题模型换 deepseek-v4-flash**（via dmxapi，非思考）。✅ 已完成：注册 ai_service id=30 + dmxapi route + session.title 指向它，dev 实测标题「新能源车出海东南亚机会」质量好。migration `20260616_140000`。

## §2 设计

### 2.1 标题生成时机：回复后 → 发送时（从 prompt）
- `biz/sessiontitle.Generate(ctx, userMsg, assistantMsg)` 支持 `assistantMsg==""`（只用 prompt 生成）。prompt 内容相应处理（无助手回复时只概括用户消息）。
- **新增端点**（前端发送时调，秒回标题）：
  - chatbot: `POST /v1/chatbot/sessions/:id/title` body `{prompt}` → `Generate(ctx, prompt, "")` → `UpdateTitleIfCurrent(id, title, defaultName)` → 返回 `{title}`。
  - agent: `POST /v1/agent-sessions/:id/title` body `{prompt}` → `Generate` → `UpdateSessionNameIfEmpty(sid, title)` → 返回 `{title}`。
- **保留回复后兜底**：现有 chatbot `maybeGenerateTitle`（stream.go）/ agent `maybeGenerateSessionTitle`（finalizeRun）不删——它们只在"标题仍是默认值"时触发，用 CAS 防覆盖。所以若前端 title 端点已设好标题（非默认），兜底自动跳过；若前端没调/失败，兜底补上。两者经 CAS 互不冲突。
- 计费：端点同样走 Generate 的剥离 billing ctx + 无 ContextFragments → pass-through 不计费。端点鉴权：user_token（校验 session 属于该用户）。

### 2.2 延迟建会话 + 占位符闪动（前端，chatbot）
- **draft 态**：点"新对话"→ `currentSession=null` + 进入 draft（显示空白对话页），**不** POST 建会话、**不** 进列表。
- **首次发送**（draft 态）`sendMessage`：
  1. 先 `createSession`(POST) 拿真实 session；
  2. 乐观把该 session 加入 `sessions` 列表并标 `titlePending=true`（渲染闪动 shimmer/pulse，标题位显"生成中"微动效）；
  3. **并行**：调 `POST .../title {prompt}` + 启动聊天流；
  4. title 返回 → 更新该 session.title + `titlePending=false`（停止闪动）；
  5. 后续发送：正常，不再建会话/加项/生成标题。
- store：`chatbot.ts` 加 `isDraft`/`titlePendingIds:Set<number>`（或 session 上挂 `_titlePending`）；新 action `startFirstMessage`（建会话+乐观入列+并行 title+stream）。
- 视图：`ChatbotChat.vue` "新对话"→ draft；session-item 渲染 pending 闪动；`createNewSession` 改为只切 draft 不建会话。

### 2.3 agent 同逻辑
- agent createRun 已支持 session_id 可选（前端可生成 UUID 或首发后端返回）。draft 态同 chatbot：点新会话不建 run、不入侧边栏；首发 createRun + 乐观占位闪动 + 调 `POST /v1/agent-sessions/:id/title` + 更新。
- `agentChat.ts` 加 draft + titlePending；`AgentChatView.vue` 占位闪动。

### 2.4 删除 bug（前端诊断优先）
- 后端已确认正常。先用 gstack/Playwright 在 dev 复现：点删除 → 确认弹窗 → 观察是否真发 DELETE / 列表是否刷新 / 有无 JS 错误。
- 按诊断结果修前端（可能：确认回调没接、删后没更新本地列表、或乐观删除被 refetch 覆盖等）。
- **bug-from-customer**：第一个 commit 是失败的复现测试（Playwright e2e 或 vitest store 测试）。

## §3 Task 计划（S4）
- **T-model**（后端，✅ 已做）：migration 注册 deepseek-v4-flash + session.title 指向。提交 migration 文件。
- **T-backend-title-endpoint**（后端）：`sessiontitle.Generate` 支持 prompt-only + chatbot/agent 两个 `/title` 端点（controller+biz+router）+ 单测。
- **T-fe-chatbot-ux**（前端）：chatbot draft + 延迟建会话 + 占位闪动 + 调 title 端点 + 实时更新 + vitest。
- **T-fe-agent-ux**（前端）：agent 同上 + vitest。
- **T-delete-bug**（前端，bug-from-customer）：浏览器诊断 → 复现测试(RED) → 修复 → 测试 PASS。
- **T-S5验证策略**：后端单测 + 前端 vitest + dev 浏览器 QA（新对话不立即入列 / 发送后占位闪动 / 标题秒更新 / 删除生效 / 标题用 deepseek-v4-flash）+ 计费 DB 核验。

## §4 风险
- 标题端点与回复后兜底的双触发 → CAS 已防（先到为准，第二者见非默认跳过）。
- draft 态 + 并行 title/stream 的竞态（标题更新 vs 流结束 refetch）→ 用 titlePending + CAS。
- 删除 bug 根因未定 → 必须浏览器诊断不静态猜（项目硬规则）。
- agent draft：createRun 的 session_id 来源（前端生成 vs 后端返回）需 S4 对齐既有 startNewRun。
