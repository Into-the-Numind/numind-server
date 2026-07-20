# S6 Dev 部署验收

日期：2026-07-20

- `ndf-done` 已将 `feature/global-agent-full-tools` 原子合并并推送到 `develop`。
- 合并提交：`e9990fef`。
- Dev 镜像：`ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-e9990fef`。
- 运行镜像 ID：`sha256:25887f1a6129d59a6c0d0c13fd2c74772e2b9b7ef5eb724b7d3bf5ee2b11f2fb`。
- 容器：`numind-server-dev`，状态 `running`，健康状态 `healthy`。
- 外部 `/healthz`：`code=0`、`status=ok`。
- 最近启动日志无 panic/fatal。
- 发布锁正常释放。

结论：第一项已部署并通过 Dev 技术验收。生产未触碰。
