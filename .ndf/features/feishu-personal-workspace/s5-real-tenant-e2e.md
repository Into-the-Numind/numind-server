# S5/S6 真实飞书租户 Dev Gate

- 状态：准备在云端 dev 运行时执行；尚未产生真实飞书副作用。
- 日期：2026-07-15
- 用户授权：先原子合并到 `develop`，仅部署 dev，再用真实飞书测试空间验收；禁止生产部署。
- S4 基线：Task 1–23 已完成；后端全包测试、lint，前端 1,051 条单测、lint/type-check 与 mocked Playwright 均通过。

## Dev 环境预检

| 项目 | 结果 | 说明 |
| --- | --- | --- |
| 当前 dev 服务 | PASS | 现有服务健康，但仍是旧镜像。 |
| 当前 dev `lark-cli` | BLOCKED（待部署解决） | 旧镜像为 1.0.56；本次后端镜像固定安装 1.0.68 到 `/usr/local/bin/lark-cli`。 |
| dev keyring | BLOCKED（待安全注入） | 现有 `secrets.env` 无 `NUMIND_FEISHU_KEYRING`；将生成独立 AES-256 key 并只写入权限 0600 的 dev secrets 文件。 |
| 运行时挂载 | PASS | `/opt/numind/dev` 已挂载到容器；`feishu-runtime` 将由运行时按需创建。 |
| 授权页、真实资源 | NOT RUN | 仍未打开官方授权 URL，未创建任何真实 Docs/Base/Wiki 资源。 |

## 验收顺序

1. 安全写入 dev keyring，合并后端和前端至 `develop`，分别部署 dev，并确认健康、CLI 版本和 feature composition。
2. 在用户的飞书测试空间中，由 Agent 发起首次连接；仅当即将创建应用或提交权限授权时，请用户在飞书官方页面确认。
3. 记录 Docs/Base/Wiki 创建、读取、更新；分阶段 scope、重启、撤销后重新授权、unknown 写、解绑、双用户隔离和重复 resume 的脱敏证据。
4. 若全部通过，记录 S6 dev 验收；生产仍保持未部署，等待独立授权。

本文件不记录账号、完整 URL、device code、token、app secret、完整 app id 或 keyring 内容。
