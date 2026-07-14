# 飞书个人工作区 — 前端授权基线（Task 16）

日期：2026-07-14。本文是前端改动前的**运行时**记录；不含真实用户、令牌、飞书 URL、应用 ID、请求值、截图或 trace。

## 运行方式与限制

- 实际 `playwright.config.ts` 只有 `setup` 与 `e2e` 两个 project；没有计划中写的 `chromium` project。
- 首先执行既有链路：`VITE_AGENT_MOCK=true npx playwright test e2e/agent-ask-user-question.spec.ts --project=e2e --trace=on`。`setup` 阶段的 `POST /v1/web/login` 经本地 Vite proxy 连接被拒绝，页面保持在 `/login`；trace/失败截图仅留在临时输出，未读取或输出任何凭据。
- 因本地 API 不可用，随后运行 `VITE_AGENT_MOCK=false npx playwright test e2e/.tmp-feishu-task16-diagnosis.spec.ts --project=e2e --no-deps --trace=on`。临时 spec 使用既有 Playwright `page.route` 安全 mock、脱敏授权 URL 和诊断 helper `createDiagnostics`，在运行中的 Vue/Pinia 应用内观测 DOM、请求和截图；完成后已删除，未改生产代码。

## 授权暂停卡基线

注入 `pause_type=auth` 的 `question_prompt` 后，运行时卡根节点为 `.auth-prompt`，其结构为：

- `.auth-prompt__copy` 复制按钮；
- `.auth-prompt__continue`「我已完成，继续」按钮；
- `.auth-prompt__cta` 外链；
- 二维码图片或骨架存在。

浏览器没有公开的 `window.__PINIA__` 或其它包含 `pinia` 的 window key，因此未绕过应用封装读取私有 message store；以上 DOM 是可验证的消息渲染快照。

点击旧「我已完成，继续」后的实际请求顺序（run id 已泛化）：

1. `POST /v1/agent-runs/:run_id/answer-stream`；
2. 该流请求非 2xx 时回退为 `POST /v1/agent-runs/:run_id/answer`。

两种请求的脱敏 body 形状相同：顶层仅有 `answers`；其一个答案条目只有 `selected`（数组）和 `free_text`。未观察到 argv、飞书 URL、scope、令牌或其他连接配置进入该请求。

## 设置页飞书卡基线

`GET /v1/feishu/status` 的运行时四态均已观测：

| 状态 | DOM/可见结果 |
| --- | --- |
| loading | 首屏存在三个 `.fc-skeleton` 元素 |
| empty | 单张 `.fc-card`，状态 pill 为「未连接」，动作是「去 AI 助手连接」和「直接连接」 |
| success | 单张 `.fc-card`，状态 pill 为「已连接」，动作是「解绑」 |
| error | `.fc-card--error`，可见「重试」按钮 |

诊断请求在开发代理下表现为 `/api/v1/feishu/status`；本文和后续契约均以逻辑 API 路径 `/v1/feishu/status` 表述。

## 375px 视口

在 `375 × 812` 的运行时设置页，`documentElement.clientWidth`、`documentElement.scrollWidth` 和 `body.scrollWidth` 均为 `375`，`overflow=false`。本次基线下未观察到横向溢出。

## 对后续实现的约束

- 新的 external-action 卡必须保留「操作卡展示」与「继续请求」的边界；续跑请求不可携带飞书命令、scope 或密钥材料。
- 新设置页状态应继续覆盖 loading / empty / error / success，且在 375px 下不得新增横向滚动。
- 真实登录与真实 API 验收留给 S5/S6；本地诊断的 route mock 只证明前端结构与请求编排，不能证明真实飞书授权完成。
