# QA Report — 飞书连续授权卡片与过期链接刷新

## 验证环境

- 后端：Standard feature worktree；Go 单元、集成、全仓 race 与 coverage 使用受控 fake `lark-cli`
- 前端：Standard feature worktree；Vue 单元测试、lint、typecheck
- 浏览器：Playwright mocked-browser；保留真实 Store、组件、轮询、刷新与生命周期 API 调用方式
- 安全边界：未新增 API、数据库 schema、自动授权、自动确认或业务命令重放；Prod 未改动

## 自动化检查结果

| 检查项 | 结果 | 备注 |
|--------|------|------|
| Go lint | PASS | `task lint` 退出码 0 |
| Go 全仓普通测试 | PASS | `TZ=UTC go test ./... -count=1` 退出码 0 |
| Go 全仓 race | PASS | `TZ=UTC go test -tags sqlite_fts5 -race ./...` 退出码 0 |
| Go 全仓 coverage | PASS | `TZ=UTC go test -tags sqlite_fts5 -coverprofile=coverage.out ./...` 与 HTML 生成均退出码 0；Feishu package coverage 81.1% |
| Feishu focused / concurrency | PASS | expired pending、凭据/lease 矩阵、continue no-replay、同 source 并发均通过 |
| Vue unit | PASS | 99 files；1120 passed、11 skipped、3 todo |
| Vue lint | PASS | 0 error；7 个仓库既有 warning |
| Vue typecheck | PASS | `vue-tsc --build --force` 退出码 0 |
| Feishu Playwright | PASS | 7/7；桌面与移动端、连续卡、replacement、continue、超时、expired、terminal、missing-link 全覆盖 |
| Visual trace | PASS | 页面 load 始终为 1；第一张完成卡保留，第二张授权卡无需 reload 出现；无孤立错误态 |
| 双独立审查 | PASS | 最终规格审查与代码质量/安全审查均为 P0/P1/P2 = 0 |
| Diff hygiene | PASS | 两仓 `git diff --check` 通过且 worktree 干净 |

## 客户 RED 与修复结果

- 后端首个 feature commit `51bdabdd` 先复现 protocol-v2 `user_auth` 仍为 pending、但服务器有效期已过时，refresh/continue 返回内部错误的问题。
- 后端 GREEN `1a614080` 只允许精确绑定、已过期、无活动 lease、凭据全空或完整的 source 做原子 replacement；partial credential、live/半截 lease、身份/摘要/scope 冲突均 fail closed。
- 旧 session、密文、key、expiry、lease、replacement 与 operation summary 在同一事务中更新；并发刷新只有一个权威 replacement。
- 过期后点击“我已完成，继续”返回 `authorization_expired` 与新 action，不进入 CLI completion，不触发或重放 Base 写操作。
- 前端首个 feature commit `b36b2ac` 先复现 resumed Agent 产生第二个 `external_action` 后，页面只有刷新才出现新卡的问题。
- 前端 GREEN `16ca715` 从 waiting snapshot 恢复第二张卡，按 run + operation 协调；不同 operation 使用独立本地 ID，同 operation replacement 原位换卡且撤销旧 URL。
- route epoch、run/session/waiting 状态、live revision 与 snapshot request sequence 共同阻止迟到 SSE、旧 snapshot 和重叠 snapshot 回滚新链接。
- snapshot 不恢复临时 URL，也不自动 refresh；用户仍明确点击“重新生成链接”。

## 基线隔离说明

本地上海时区凌晨运行两个既有 `xhsscript` daily analytics 测试时，Go 本地日期与 SQLite UTC `DATE()` 分桶不一致；同样在干净 `develop` 失败，`TZ=UTC` 下全仓通过。该问题自既有小红书统计实现存在，与本次飞书文件和调用链无重叠。

完整 mocked-browser 项目中，一个既有 `QuestionPrompt` 流式最终回复用例失败；在未改动的前端 `develop` 上同条件独立运行也完全相同。此次相关的 7 条飞书 Playwright 场景独立全部通过。

`task test` 将普通、race、coverage 三轮全仓测试连续运行时，两次尝试分别在不同阶段出现非确定性失败；三个构成命令随后在同一 worktree、同一 UTC 环境分别运行均为退出码 0，Feishu 包在所有轮次始终通过。因此记录为既有全仓组合压力基线，不把无关修复混入本 feature。

## Dev 失败回归补充（09:27）

首轮 Dev 验收又暴露了两条独立路径，均已使用对应生产请求与浏览器行为定位，而非根据通用 `Internal server error` 猜测：

1. 飞书授权、账号匹配和凭据原子提交均成功，但授权后的 Base/Agent continuation 错误继承了仅供数据库 mutation 使用的 5 秒上下文；约 4.9 秒的业务调用越过该 deadline 后被安全标记为 unknown，并向浏览器返回 500。
2. 成功路径中服务端约 10 秒后完成，浏览器也轮询到 completed；但授权暂停时的阶段性 assistant bubble 已被前端误转为 `final_answer`，真正终答随后被错误去重，只有 reload snapshot 才能显示。

永久回归提交顺序：

- Server RED `9936ec73` → GREEN `df6eeaee`：5 秒只覆盖 `FinalizeDeviceAuthSuccess`，提交后的 durable reread、飞书操作与原 Agent 恢复使用原 50 秒确认预算；用户、Agent、operation、app、generation、session、lease、幂等与 unknown-write 禁止重放边界均未放宽。
- Web RED `45622a6` → GREEN `5fae1c2`：首个 RED 同时包含 Vitest 与有限 SSE Playwright；waiting/queued 状态不再生成最终回复，首次真实 active→terminal 状态可纠正旧临时答案，迟到的 status/reconcile 响应由统一单调序号丢弃。

补充验收结果：

| 检查项 | 结果 | 备注 |
|--------|------|------|
| Go 全仓普通测试 | PASS | `TZ=UTC go test ./... -count=1` |
| Feishu race | PASS | 单独运行 `TZ=UTC go test -race ./internal/numind/biz/feishu -count=1`；并发抢占机器资源时固定 5 秒 CLI 测试曾超时，隔离重跑 77.769 秒通过 |
| Go lint / Feishu biz | PASS | `task lint` 与完整 Feishu biz 92.596 秒通过 |
| Vue unit | PASS | 99 files；1124 passed、11 skipped、3 todo |
| Vue lint / typecheck / production build | PASS | lint 0 error（7 个既有 warning）；typecheck 与 Vite production build 通过 |
| Feishu Playwright | PASS | 8/8；新增有限 SSE 关闭、仅 GET status 续跑并无需 reload 显示终答 |
| 竞态回归 | PASS | duplicate terminal、empty final、overlapping status、status/reconcile cross-path 全通过 |
| 独立复审 | PASS | P0/P1/P2 = 0；提交链符合 customer RED before GREEN |

本补充修复未新增 API、schema、自动确认、业务命令重放或凭据暴露；Prod 仍未改动。

## Dev 验收提示词

在新对话使用唯一名称，避免与此前已部分执行的写操作混淆：

`请创建一个飞书多维表格，名称为「有数 Base 连续授权回归-0720」。创建一个名为「任务列表」的数据表，包含字段「任务名称」（文本）和「状态」（单选：待处理、已完成）。新增一条记录：任务名称为「验证连续授权与链接刷新」，状态为「待处理」。完成后重新读取这条记录，并告诉我多维表格链接。不要创建飞书文档或知识库。`

预期：如连续需要两次权限，第二张卡无需刷新页面自动出现；如果卡片链接已过期，点击“重新生成链接”会得到新链接，不出现 `Internal server error`；完成授权并点击继续后恢复同一个 Agent 原任务。

## 结论

本次飞书功能范围 `ALL_PASS`，两个最终独立审查均通过。记录并隔离上述可在干净 `develop` 复现的非飞书基线项后，允许进入 S6 原子合并、推送与 backend-first Dev 部署；Prod 不在本次范围内。
