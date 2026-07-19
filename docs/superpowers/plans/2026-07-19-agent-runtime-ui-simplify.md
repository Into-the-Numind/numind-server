# Agent 运行界面精简与真实停止 — 实施计划

**Feature:** `agent-runtime-ui-simplify`  
**Spec:** `docs/superpowers/specs/2026-07-19-agent-runtime-ui-simplify-design.md`  
**Repository:** `numind-web-v3`  
**Execution:** 串行。所有任务依赖同一个运行页与 E2E 测试文件，不可并行。

## Task 1 — RED：证明输入停止键没有取消后端 run

**目标：** 将过期的 abort-bar E2E 改为真实输入停止键，并加入后端取消请求断言，保留失败状态作为客户可见回归保护。

**文件：**

- `numind-web-v3/e2e/agent-streaming.spec.ts`

**步骤：**

1. 使用 `page.addInitScript` mock 一个先发 `stream_start`、在 `AbortSignal` 前永不关闭的浏览器 `ReadableStream`，确保 stop 按钮稳定可见而非因 `route.fulfill` EOF 消失。
2. 让 Scenario 2 mock `POST /v1/agent-runs/2/cancel`，记录请求次数、方法与精确 URL，并返回成功取消响应。
3. 改用 `.send-btn--stop[aria-label="终止"]` 定位真正运行时按钮，替换同文件全部 `.abort-bar` 断言，消除零匹配 `toBeHidden()` 假绿。
4. 在点击后断言 cancel 请求发生一次、输入重新可编辑、无“发送失败”。
5. 在现状代码运行该测试，确认 stop 可见但失败于缺少取消请求。
5. 提交：`test(qa): reproduce agent input stop not cancelling run`。

**验收：** 测试失败的唯一原因是输入 stop 不调用取消接口，不是选择器或 mock 失效。

## Task 2 — GREEN：让输入停止真正取消 Agent run

**目标：** 把输入停止事件接到一个真实的、可重试的取消处理器。

**文件：**

- `numind-web-v3/src/components/agent/AgentInputArea.vue`
- `numind-web-v3/src/views/agent/AgentChatView.vue`
- `numind-web-v3/e2e/agent-streaming.spec.ts`

**步骤：**

1. 为输入停止键增加由父层传入的 `canStop` 状态：`currentRun` 为 pending/running 且 `!store.cancelling` 时显示，即使 SSE 已回退轮询或重载恢复；尚无 run ID 时只显示禁用发送按钮。
2. 在运行页新增 `handleStop()`：同步 guard `store.cancelling`，已有 current run 时先 await `runCtrl.cancel()`；成功才停止本地 SSE、narration 与 polling，并展示取消反馈。
3. 对取消错误显示真实错误，不改变当前 run 为 cancelled，也不关闭流，保留重试入口。
4. 将两个输入区的 `@stop` 绑定至该处理器，令 Task 1 的成功、取消失败和连续点击测试变绿。

**验收：** 点击输入停止键恰好请求一次 `/cancel`，run 进入 `cancelled`，输入恢复；SSE 回退轮询的 active run 仍有同一停止键；取消失败不产生假成功、SSE/重试入口保留；连续点击只发一次请求。

## Task 3 — 删除重复停止与积分预估

**目标：** 删除顶部重复取消按钮与输入前预估积分展示/请求，保留其它头部和输入能力。

**文件：**

- `numind-web-v3/src/components/agent/AgentChatHeader.vue`
- `numind-web-v3/src/components/agent/AgentInputArea.vue`
- `numind-web-v3/src/views/agent/AgentChatView.vue`
- `numind-web-v3/src/components/agent/__tests__/AgentChatHeader.spec.ts`
- `numind-web-v3/e2e/agent-streaming.spec.ts`
- `numind-web-v3/src/components/agent/__tests__/AgentInputArea.spec.ts`

**步骤：**

1. 删除 Header 取消相关 props、emit、imports、computed、样式与模板；保留名称和侧边栏。
2. 删除 InputArea estimate prop、emit、debounce、预估栏和无用 CSS。
3. 删除运行页 estimate state binding/request handler；新增 `AgentInputArea` fake-timer 单测，断言输入后不 emit `estimate-request`，并在页面断言预估文案不出现。
4. 将 Header 单元测试更新为“从不渲染取消任务”。

**验收：** 页面任意状态不显示“取消任务”或积分预估；Agent 输入、上传、字数提示和发送仍可用。

## Task 4 — 精简欢迎身份与新内容控件

**目标：** 删除首屏 emoji + Agent 名称 pill，并将“新内容”控件变为纯圆形向下箭头。

**文件：**

- `numind-web-v3/src/components/agent/AgentFirstRun.vue`
- `numind-web-v3/src/components/agent/AgentMessageList.vue`
- `numind-web-v3/src/components/agent/__tests__/AgentFirstRun.spec.ts`
- `numind-web-v3/src/components/agent/__tests__/AgentMessageList.spec.ts`

**步骤：**

1. 删除 welcome identity DOM 和对应 CSS，只保留欢迎语。
2. 删除“新内容”文本，调整回到底部按钮为固定圆形；保留 `aria-label="回到底部"`、图标、hover 与 focus。
3. 新增 FirstRun 单测，断言 identity 节点、emoji/name 标签不存在而欢迎语保留；新增 MessageList 单测，mock interrupted scroll state，断言按钮无文字、带 `aria-label="回到底部"`。
4. 样式采用 32px 宽高与 `border-radius: 50%`，添加 `:focus-visible` ring；S5 浏览器 QA 验证桌面/移动端。

**验收：** 欢迎区不含 emoji/name pill；被打断滚动时仅出现 32px、可访问、无文字的圆形箭头，点击继续回到最新消息。

## Task 5 — 质量关卡与自动验收

**目标：** 在本地真实 Vite/Playwright 环境验证完整行为并消除回归。

**文件：**

- 仅当验收发现问题时修改 Task 1–4 已列文件；不扩展范围。

**步骤：**

1. 运行聚焦 `agent-streaming` Playwright，确认取消请求和 UI 状态。
2. 运行 `npm run lint && npm run type-check`。
3. 运行全量或与改动相邻的 Agent 浏览器测试，并用 Playwright 截图检查桌面/移动端：无身份 pill、无顶部取消、无预估栏、圆形箭头不遮挡输入区。
4. 记录 S5 QA 结果和准确命令输出。

**验收：** 所有命令通过，E2E 明确证明真实取消；无 P0/P1 视觉或交互回归。

## 依赖与提交纪律

- T1 → T2 → T3 → T4 → T5 为严格线性依赖。
- T1 是此次发现的用户可见停止缺陷的永久 RED 回归提交，必须先于实现提交。
- 每个完成任务后提交一个聚焦 Conventional Commit，不混入不相关改动。
- S4 完成后进行独立规范合规与代码质量双审查；S5 通过后才允许 `ndf-done` 合并并部署 Dev。
