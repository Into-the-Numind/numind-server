# 飞书授权确认稳定性实施计划

## Task 1 — 客户故障 RED

- 前端 API 测试锁定 resume 必须传 60 秒 scoped timeout，现状因缺失配置失败。
- 后端测试锁定 10 分钟 session、30 秒 CLI completion、10 分钟 device link 上限和损坏 Agent link 的拒绝，现状失败。
- 两仓库分别以 `test(qa)` 提交 RED，满足客户 bug 回归规则。

## Task 2 — 时限对齐

- 后端默认 session 改为 10 分钟、device provider expiry 上限改为 10 分钟；CLI completion 保持 30 秒，避免与前后置存储边界叠加后超过浏览器 60 秒。
- 前端仅 `resumeFeishuOperation` 传 `{ timeout: 60_000 }`。
- 从确认入口创建 detached 50 秒主动处理 deadline，替代 device-auth 成功路径原先可扩展到 12 分钟的 dispatch context；为最慢 5 秒持久化收尾及 HTTP/网络返回预留余量。

## Task 3 — 严格绑定与立即恢复

- 在 completion claim 前验证 operation/session/user/generation/state/phase/summary 与 Agent run/tool link 完整性；operation-bound 记录不允许 zero/zero legacy 例外。
- 保留 AAD、account app/generation re-read、scope hash、candidate HOME、atomic finalize 和 dispatcher exactly-once。
- 成功 finalize 后直接 dispatch，不增加 sleep、timer 或 worker；30 秒 CLI、前置校验、reconciliation、mutation 和 dispatch 全部共享 50 秒主动处理 deadline，由 60 秒浏览器窗口覆盖最慢 5 秒持久化收尾和 HTTP/网络返回。

## Task 4 — 可诊断日志

- 将 CLI pending timeout、network、read、parse、slow-down 分类为固定安全 outcome。
- 在 cli completion、binding、reconciliation 阶段发出 allowlisted observation。
- 扩展生产日志 sink 白名单并增加泄密反例测试。

## Task 5 — 验收和交付

- Go focused/full/race、`task lint`。
- Vue focused/full、`npm run lint`、`npm run type-check`。
- Playwright 飞书卡片浏览器契约。
- 独立规格与质量评审，S5 验收，`ndf-done` 两仓库，先后端后前端部署 Dev，健康检查与关键日志检查。

## 串行依赖

Task 1 → Task 2 → Task 3 → Task 4 → Task 5。两仓库文件不交叉，但最终浏览器契约依赖后端时序设计，因此不拆成互相依赖的并行写任务。
