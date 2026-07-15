# S5/S6 真实飞书租户 Dev Gate

- 状态：等待 `develop` 合并边界澄清；尚未产生真实飞书副作用。
- 日期：2026-07-15
- 用户授权：先原子合并到 `develop`，仅部署 dev，再用真实飞书测试空间验收；禁止生产部署。
- S4 基线：Task 1–23 已完成；后端全包测试、lint，前端 1,051 条单测、lint/type-check 与 mocked Playwright 均通过。

## Dev 环境预检

| 项目 | 结果 | 说明 |
| --- | --- | --- |
| 当前 dev 服务 | PASS | 现有服务健康，但仍是旧镜像。 |
| 当前 dev `lark-cli` | BLOCKED（待部署解决） | 旧镜像为 1.0.56；本次后端镜像固定安装 1.0.68 到 `/usr/local/bin/lark-cli`。 |
| dev keyring | PASS | 已生成独立 AES-256 key，只写入 dev 权限 0600 的 secrets 文件；格式和 32 字节材料长度均已在服务器内校验，未读取或输出材料。 |
| 运行时挂载 | PASS | `/opt/numind/dev` 已挂载到容器；`feishu-runtime` 将由运行时按需创建。 |
| 授权页、真实资源 | NOT RUN | 仍未打开官方授权 URL，未创建任何真实 Docs/Base/Wiki 资源。 |

## 验收顺序

1. 已安全写入 dev keyring；等待隔离主前端目录中未推送的小红书扩展提交后，才合并后端和前端至 `develop`、分别部署 dev，并确认健康、CLI 版本和 feature composition。
2. 在用户的飞书测试空间中，由 Agent 发起首次连接；仅当即将创建应用或提交权限授权时，请用户在飞书官方页面确认。
3. 记录 Docs/Base/Wiki 创建、读取、更新；分阶段 scope、重启、撤销后重新授权、unknown 写、解绑、双用户隔离和重复 resume 的脱敏证据。
4. 若全部通过，记录 S6 dev 验收；生产仍保持未部署，等待独立授权。

本文件不记录账号、完整 URL、device code、token、app secret、完整 app id 或 keyring 内容。

## 当前合并阻塞（2026-07-15）

主前端 `develop` 比 `origin/develop` 多 6 个未推送的小红书扩展提交，且存在同目录未提交改动。个人飞书分支以该本地 `develop` 为祖先；直接运行 `ndf-done` 会把这 6 个无关提交一同推到远端并带入 dev 部署。为避免超出本次授权范围，未执行合并、部署或真实飞书操作，等待用户选择如何处置这组无关前端变更。
