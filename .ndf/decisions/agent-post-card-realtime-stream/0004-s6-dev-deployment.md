# S6 Dev 部署验收

## 结论

PASS。后端与用户端前端均已从干净的精确 develop commit 部署到 Dev；Prod 未打 tag、未部署。

## 精确版本

- Server commit/tag：`28b785e4` / `develop-28b785e4`
- Server registry digest：`sha256:00a41b5e2714c9ef606c71ed87b44ba7e965341f42bee4e78e96e8f977c93352`
- Server runtime image：`sha256:dcf3545f4adf05c64997820deb3188a92400d5353d502d325cd5ac45ce3d558b`
- Web commit/tag：`202d678` / `develop-202d678`
- Web registry digest：`sha256:8f4eb40879fb472d4f1b31061b7b07f66cc29b9b2f7fafcfade843b76a3e0d46`
- Web runtime image：`sha256:e3013532b00de815343c588faf1cbf95bc19298443a0567ea47d1326a1d4e74c`

## 运行验收

- `http://49.233.219.254:9091/healthz` 返回 `status=ok`。
- `http://49.233.219.254:9200/health` 返回 `healthy`。
- `http://49.233.219.254:9200/api/healthz` 经前端反向代理返回 `status=ok`。
- 未认证访问 `GET /v1/agent-runs/1/events?after=pause` 返回 401，证明新路由已注册且仍受用户认证保护。
- 两个容器均为 `healthy`，最近启动日志未发现 panic/fatal/emergency。

## 部署说明

第一次新后端容器启动叠加了 5 个遗留 sandbox 容器清理，超过 360 秒健康门禁。先恢复上一健康镜像并完成遗留清理，再对同一精确新镜像执行第二次部署；新镜像约 4 分钟完成语义模型与 Agent 初始化并通过健康检查。该对照排除了 Redis broker 启动死锁，最终运行的仍是未经修改的已验证构建。

