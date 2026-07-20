# 飞书显式连接入口需求

## 问题

未连接用户明确说“连接飞书”时，Agent 只调用只读 `lark_inspect` 并解释连接方式，不创建授权动作；设置页“连接飞书”也只跳转首页。用户无法从任何显式入口开始连接。

## 用户结果

- 用户明确要求连接、重新连接或授权飞书时，Agent 必须在本轮立即发起服务端连接流程。
- 未连接时在原会话展示唯一、可恢复的授权卡片；已连接时立即返回成功，不制造多余授权。
- 设置页点击“连接飞书/继续连接/重新授权”必须直接调用服务端连接流程并在原页面展示下一步，不要求用户重新描述业务任务。
- 重复点击、刷新、进程重启和旧卡片不得创建并行连接或把新状态回退。
- 连接入口只申请建立个人工作区所需的基础授权；Docs/Base/Wiki 业务 scope 仍按真实业务操作增量申请。

## 验收

1. Agent 工具目录存在独立 `lark_connect`，其说明明确覆盖显式 connect/reconnect/authorize 意图。
2. `lark_connect` 使用当前用户、Agent run 和 tool call 身份创建幂等的 connection-only operation；未连接时 yield 当前外部动作，连接后返回成功。
3. `lark_inspect` 保持只读；普通业务请求仍先 `lark_execute`，不被连接入口劫持。
4. 设置页点击入口发出 `POST /v1/feishu/connect`，显示服务器返回的官方飞书链接和“我已完成，继续”动作，并可重试/刷新状态。
5. “我已完成，继续”确认当前 `session_id`，不得再次启动连接；刷新后缺少临时 URL 时先恢复同一授权步骤。
6. Agent 显式连接、设置页连接和真实飞书业务触发的授权在同一账号 generation 内最多只有一个 bootstrap worker；进程在 session 创建前退出时，超时占位可安全接管。
7. 客户复现测试由 RED 变 GREEN；Go/Vue 全量静态检查和关键 Playwright 路径通过。

## 非目标

- 不新增数据库 schema、公开 API 端点或消息发送权限。
- 不要求用户提供 App ID/App Secret，不恢复旧版 `feishu_connect` 实现。
