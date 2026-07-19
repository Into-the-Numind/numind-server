# Agent 运行界面精简与真实停止设计

**Feature:** `agent-runtime-ui-simplify`  
**Date:** 2026-07-19  
**Repository:** `numind-web-v3`  
**Status:** S2 design approved by the user's explicit authorization to execute this Standard feature through Dev.

## 1. 问题与目标

当前 Agent 对话页有三处重复或干扰信息：输入中的积分预估、欢迎区的“🤖 + Agent 名称”标签、顶部的“取消任务”按钮。此外，输入区方形停止键只中止本地 SSE，不能保证服务端 Agent run 停止；用户看到停止后，后台任务仍可能继续。

本设计在不修改计费、权限或后端 API 的前提下，令输入框成为唯一、真实的停止入口，并缩小“新内容”回到底部控件。

### 非目标

- 不改动 `Reserve/Reconcile`、预估 API 或真实积分扣减逻辑。
- 不新增、删除或修改后端端点。
- 不改动 Agent 执行、工具调用、会话持久化或流协议。
- 不移除顶部的 Agent 名称与移动端侧边栏开关；待删除的是欢迎区内的 emoji + 名称 pill。

## 2. 方案比较与决策

| 方案 | 描述 | 结论 |
| --- | --- | --- |
| A | 仅删除顶部取消按钮，保留输入 stop 的 `AbortController.abort()` | 拒绝：断开页面流不等于取消后端 run，形成假停止。 |
| B | 保留顶部取消按钮，同时在输入区继续仅中断 SSE | 拒绝：行为仍不一致，且界面保留重复停止入口。 |
| C（采用） | 输入 stop 统一执行后端取消和本地流清理，删除顶部取消按钮 | 采用：一个入口、一套真实状态、复用既有 API/store。 |

## 3. 组件与数据流

```mermaid
sequenceDiagram
  participant U as 用户
  participant I as AgentInputArea
  participant V as AgentChatView
  participant S as AgentChatStore
  participant API as cancelRun API
  participant SSE as useAgentStream

  U->>I: 点击方形停止键
  I->>V: emit('stop')
  V->>S: cancelCurrent() [当前 run 存在]
  S->>API: POST /v1/agent-runs/:id/cancel
  API-->>S: 取消成功
  S->>S: currentRun.status = cancelled
  V->>SSE: stop()，关闭本地 SSE
  V->>V: 停止 narration/polling，显示已取消反馈
```

### 3.1 `AgentInputArea.vue`

- 删除 `estimate` prop、`estimate-request` emit、500ms debounce 及预估展示/CSS。
- 输入变化不再调用 `estimateRun`；附件上传、字数提示、发送和停止行为不变。
- 当前 run 为 `pending`/`running` 且不在取消中时显示 `Square` 图标停止键，而非只依赖 `isStreaming`。这覆盖 SSE 409 回退轮询、刷新后的状态轮询和仍在运行的历史会话。
- 尚未收到 `stream_start`、没有可信 `run_id` 的短暂请求窗口不显示可用的停止键；发送按钮保持禁用，避免提供无法取消的假操作。

### 3.2 `AgentChatView.vue`

- 删除 `handleEstimateRequest` 及两个 `estimate`/`estimate-request` 绑定。
- 新增唯一 `handleStop()`，取代两处直接 `@stop="stopStream"`：
  1. 若已从 `stream_start` 取得 `store.currentRun`，先调用既有 `runCtrl.cancel()`。
  2. 取消成功后，调用 `stopStream()`、停止 narration 与 polling，并展示既有“已取消任务”提示。
  3. 取消接口失败时，显示错误提示且不伪造 `cancelled` 状态；保留本地 stream 和停止键，以便用户重试真实取消而非落入无入口的运行态。
  4. 在 `stream_start` 到达前没有可靠 `run_id`，发送按钮保持禁用。收到 `stream_start` 后，或重载取得可取消的 current run 后，才显示真实停止键。这避免提供明知无法兑现的停止操作。
  5. `store.cancelling` 是停止键可用性的组成部分，处理器也同步 guard；双击或连续键盘激活至多发出一次取消请求。
- 移除 `AgentChatHeader` 的 run/cancelling/cancel props 和 `@cancel` 绑定。

### 3.3 `AgentChatHeader.vue`

- 删除取消按钮、`Pause`/`AppButton` import、取消相关 props/computed/emits/CSS。
- 保留 Agent 名称、侧边栏切换和响应式布局。

### 3.4 `AgentFirstRun.vue`

- 删除 `.first-run__identity` 及其 emoji/name 子节点和 CSS。
- 保留欢迎语 heading，因它本身是新会话的任务导向提示。

### 3.5 `AgentMessageList.vue`

- 保留现有仅在用户打断自动滚动时显示的逻辑与 `aria-label="回到底部"`。
- 删除 `新内容` 文本；按钮固定为圆形，以 `ChevronDown` 图标表达动作。
- 保留 hover/focus 可见反馈；不改变滚动状态机。

## 4. 状态与错误处理

| 场景 | 处理 |
| --- | --- |
| 未运行/已结束 | 无停止键，不发取消请求。 |
| 正在建立 SSE、尚未收到 `stream_start` | 发送按钮禁用，停止键不出现，避免无 run_id 的假取消。 |
| `stream_start` 后、SSE 409 回退轮询中，或重载后的 pending/running run | 输入区停止键可用；取消 API 成功后 run 转 `cancelled`、流停止、输入恢复。 |
| 取消中 | 停止键禁用且处理器 guard，至多一条取消请求在飞行。 |
| 取消 API 失败 | 维持服务端真实 run 状态和 SSE，显示错误通知并保留停止键重试；不会显示“已取消”。 |
| 只读历史 | 不显示输入区与停止入口。 |
| 取消完成 | 发送按钮恢复；既有 `cancelCurrent()` 系统消息作为持久会话记录。 |

## 5. 测试设计

### RED-first 回归

在修改业务代码前，更新 `e2e/agent-streaming.spec.ts` 的中断场景，使其：

1. 使用实际 `.send-btn--stop[aria-label="终止"]`，而不是已不存在的 `.abort-bar`；替换同文件全部 `.abort-bar` 零匹配假绿断言。
2. 通过 `page.addInitScript` 将 `/stream` 响应替换为收到 `AbortSignal` 前不关闭的 `ReadableStream`，先发 `stream_start` 确立 run，再使 stop 按钮可见。
3. mock `POST /v1/agent-runs/2/cancel` 并捕获精确 URL 与请求方法。
4. 断言点击后取消请求只发生一次、输入恢复可编辑、且页面不出现发送失败；另断言 5xx 时没有 cancelled 系统消息、流与停止键保留可重试。

该测试应先失败，因为现有停止键不会发出取消请求。

### 成功回归

- `AgentInputArea` 单元测试以 fake timer 验证输入后不再 emit `estimate-request`；mock 模式不会产生真实网络请求，不能以网络计数证明该点。
- E2E 验证欢迎区没有 emoji+名称 pill，但欢迎语存在。
- `AgentChatHeader.spec.ts` 验证不再存在“取消任务”及 cancel emit。
- `AgentFirstRun.spec.ts` 与 `AgentMessageList.spec.ts` 验证 identity/text 消失、圆形箭头无文本节点且带 `aria-label="回到底部"`；消息列表测试 mock interrupted scroll state。视觉 QA 验证 32px 圆形与 `:focus-visible`。
- 运行 `npm run lint`、`npm run type-check` 和聚焦 Playwright。

## 6. 安全与兼容性

- 取消仍由既有后端鉴权、run 所有权和计费对账处理；前端不绕过这些边界。
- 不把 run ID 置入 URL、DOM 文本或新的持久化介质。
- 保留现有运行期 `currentSessionEpoch` 及 store guard，避免过期会话的取消回写到新会话。

## 7. 自检

- 无 TBD/TODO/未决占位。
- 唯一停止入口的真实语义、无 run_id 窗口、失败行为和测试证据已明确。
- 范围限于用户端运行界面与已有取消契约，不扩展到后端、计费或权限。
