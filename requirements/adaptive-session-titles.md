# 会话自适应标题 + Agent 历史列表完整化 (adaptive-session-titles)

## 来源
- 提出人：用户（产品负责人）
- 提出日期：2026-06-16

## 需求描述
> 检查 chatbot 和 agent mode 两种模式，把每个会话的标题改成自适应。目前所有会话标题都一样，是很差的用户体验，要改成"第一次指令输出后，就直接能自适应生成和内容相关的标题"。
>
> 附带修复一个小 bug：agent mode 只能展示 5 个会话历史，应该展示以前创建过的所有会话历史，并且最新的在最上面。

## 业务目标
1. **可识别性**：用户在会话列表里能凭标题区分不同对话，而不是看到一列完全相同的标题（chatbot=智能体名、agent=空），降低"找回上次那段对话"的认知成本。
2. **历史可达性**：agent mode 用户能看到自己全部历史会话（当前侧边栏被截断到 5 个），最新在最上，符合直觉。
3. 对齐主流 AI 产品（ChatGPT / Claude）的会话管理体验，提升留存与专业观感。

## 优先级
中（体验缺陷，非阻断性故障；但直接影响日常使用观感）

## 调研结论（已读代码，附证据）

### Chatbot 模式
- 表 `chatbot_session`，标题字段 `Title`（VARCHAR200），创建时写死为智能体名 `config.Name`（`biz/chatbot/chatbot.go:377`）→ 同一智能体下所有会话标题相同。
- 无任何自动生成逻辑；已有手动改名接口 `RenameSession`（`PUT /v1/chatbot/sessions/:id/rename`）。
- 首轮收尾点：SSE `ChatStream`（`biz/chatbot/stream.go`），消息持久化后、`done` 事件前。
- 历史列表 `GET /v1/chatbot/sessions`：offset/limit 默认 20、max 100，排序 `updated_at DESC`（单智能体含 pinned 优先）。前端无 5 限制，但默认只拉首页 20 条、无"加载更多"。

### Agent 模式
- 表 `agent_run`，标题字段 `session_name`（VARCHAR255，默认空字符串），创建时从上一轮 run 继承（首轮为空）。
- 无自动生成逻辑；已有 store 方法 `UpdateSessionName`。
- 首轮收尾点：`biz/agent/runner.go finalizeRun`（transcript 落库后）。
- **"只显示 5 个" bug 根因**：侧边栏"最近会话"四层硬编码 limit=5——前端 `stores/agentChat.ts:273` `listRecentSessions(5)` → `api/agent.ts:47` 默认 5 → 后端 `controller/v1/agent/student_query.go:84` `DefaultQuery("limit","5")` → `biz/agent/student_query.go:243` `limit=5`。另有一个不限量的"全部历史"页 `AgentHistoryView.vue`（`listAllHistorySessions()`）但入口不显眼。后端排序 `is_pinned DESC, started_at DESC`（最新在上，已正确）。

## 产品决策（用户 2026-06-16 已确认）
1. **标题生成方式**：LLM 智能摘要——首轮对话结束后用便宜小模型（qwen-turbo）生成 6-12 字概括标题，走系统路径**不扣用户积分**。
2. **Agent 历史列表**：侧边栏直接展示全部历史会话，最新在最上、可滚动。
3. **Chatbot 历史**：一并完善，让两种模式历史列表都能完整查看，体验一致。
4. **开发档位**：Standard 主干流程。

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**（title/session_name 字段已存在）
  2. 新增 API 端点：**否**（标题生成挂在既有 stream/finalize 流程内；历史列表改既有端点参数；可能需要 chatbot store 一个 UpdateTitle 方法，但不新增对外端点）
  3. 新外部服务集成：**否**（复用 aiservice 统一入口）
  4. 影响文件数：**>3**（跨 numind-server + numind-web-v3 两仓库，含新增标题生成 helper + 两模式收尾点接入 + 前端历史列表改造）
  5. 高风险业务逻辑（支付/权限）：**否**（标题生成走系统路径不计费；不触碰会员/权限判定）
- 人类决定：**确认 Standard**（用户 2026-06-16 确认）

## 备注
- 标题自动生成必须**只在标题仍为默认值/空时**写入，绝不覆盖用户手动改名（两模式都有 rename）。
- 标题生成的 LLM 调用必须经 `aiservice` 统一入口（Langfuse trace + 路由降级），但通过 SkipDeduction/系统路径避免向用户计费——具体计费豁免方式在 S2 设计确定。
- 当前活跃的无关 feature：`agent-output-refine`（S3，同样改 agent 前端组件，需在 S2/S3 注意文件归属避免冲突）。
