# Agent ReAct 流式化 — Requirement Card

## 来源
- 提出人：陈志宇
- 提出日期：2026-05-27
- 触发事件：dev 上 agent run 41/40/38/42 连续出现 model_error。Hotfix 已修了 timeout，但用户看到的体验仍然是「转圈几分钟然后蹦出一坨答案」——既不知道进度，也不知道在做什么

## 需求描述

> "AI 一边流式回复，一边更新状态，一边还能看到它使用工具"

参考 Manus 截图与 Claude Code 源码（`/Users/zhiyuchen/Downloads/ClaudeCode/src`）。用户进 agent 聊天页面后，应该看到：

1. **流式 LLM 输出**：每一步 LLM 生成的文本（无论是中间的"接下来我要去查作业帮的资料"还是最终答案）逐字出现，不是等几分钟后整段砸出来。
2. **状态变化可见**：每个 ReAct step 边界、tool call 开始/结束、出错/恢复，都立刻反馈到 UI。
3. **工具调用露出**：用户能看到 agent 调了什么工具、参数大概是什么、结果摘要是什么，可展开 / 折叠。

## 业务目标

- **降低弃用率**：长轮询体验下，用户经常在 60-120s 静默时关闭页面或重复点击触发新 run。流式化后，每秒都有反馈，用户愿意等。
- **提高信任**：用户能"看见 agent 在思考"，对最终结果接受度更高（vs. 黑盒输出）。
- **建立 agent-mode 产品差异化**：竞品（豆包、ChatGPT、Manus）均已流式化。我们停留在轮询会被定性为"老一代体验"。
- **减少 model_error 误报**：上一波 Hotfix 把 timeout 提到 180s 缓解了误杀，但根因之一是非流式 chat 必须等服务器完整生成后才返 header。流式 chat header 立刻返回，timeout 设置容忍度更高，再叠加 SSE 推送，从根本上消除"60s 没动静就当死了"的代码路径。

## 优先级

**高**。已经发生客户可见 bug（model_error）+ 体验远落后竞品 + 影响 agent-mode V1.5 整体可用性判断。

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**（narration 已 in-memory；不需要新表。可能加 `agent_run.streaming_protocol_version` 字段做向后兼容，但非必需）
  2. 新增 API 端点：**是**（新增 SSE 流式 endpoint，例如 `GET /v1/agent-runs/:id/stream` 或对现有 POST `/v1/agent-runs` 扩展 Accept: text/event-stream）
  3. 新外部服务集成：**否**（沿用现有 aiservice/dmxapi）
  4. 影响文件数：**>3**（预估 10-15 个：后端 runner+adapter+controller+router；前端 store+composable+view+多个新组件）
  5. 高风险业务逻辑：**否**（不涉及支付/权限/积分扣减；仅改 transport + UI）
- 人类决定：**Standard**（5 条里 2 条不满足 Hotfix；且涉及跨仓库 + ReAct 内核改造，必须走完整 S0-S7）

## 备注

### 同期已落地的修复（已 ship）
- [b4349bf5](https://github.com/Into-the-Numind/numind-server/commit/b4349bf5) bumped dmxapi `ResponseHeaderTimeout` 60s → 180s
- [648d16d4](https://github.com/Into-the-Numind/numind-server/commit/648d16d4) 把 LLM 失败的 underlying error 落到 `agent_run.terminal_metadata`
- [67cb4e1d](https://github.com/Into-the-Numind/numind-server/commit/67cb4e1d) dmxapi `doPost` MaxRetries 0 → 3

这三个是症状缓解。本 feature 是根因解决：把同步 + 轮询架构换成流式 + SSE。

### 参考实现来源
- **Claude Code** (`/Users/zhiyuchen/Downloads/ClaudeCode/src`)：query.ts 是 async generator 的 ReAct loop，QueryEngine.ts 是消费者；UI 端 components/messages/* 按事件类型分组件渲染。这是行业最成熟的实现，S1 / S2 直接照搬其架构。
- **现有 chatbot 流式实现**（`internal/numind/controller/v1/chatbot/chatbot.go` + `numind-web-v3/src/api/sales.ts`）：SSE 写入 + getReader/TextDecoder 消费已经成熟，可直接复用。
- **已有 narration 系统**（`internal/numind/biz/narration/`）：6 种 state 事件 + per-run 内存 channel + 前端 ToolGroupMessage 聚合渲染。本 feature 不抛弃 narration，而是把它从"轮询出口"换成"SSE 出口"，并新增 LLM 文本事件类型。

### 已确认 NOT 在本 feature 范围内
- ❌ 流式给管理端（admin-web）的 agent 监控视图。管理端目前没有 agent 实时查看页，未来需要时另开 feature。
- ❌ 多 agent 并发会话窗口。当前只解决单个 agent run 的流式渲染。
- ❌ 重新设计 agent 整体 UX（消息气泡样式、卡片折叠交互、动效）。本 feature 把数据流打通，UX 细节调优另列 follow-up。
- ❌ ali / volc adapter 的同等 streaming 升级。当前 agent.run 路由到 dmxapi/deepseek-v4-pro，其他 adapter 没遇到这个问题。若以后切换主路由再补。
