# S5 全量验证

## 结论

ALL PASS。允许进入 S6 原子合并与 Dev 部署，Prod 不在本次范围内。

## 后端门禁

- `go test ./...` 通过。
- Agent stream、Agent biz、Agent controller 聚焦测试通过。
- 上述三个关键包的 `go test -race` 通过。
- `task lint`（`go vet` + `golangci-lint`）通过。
- Redis transport 回归覆盖原子 TTL、重放、同 run 多订阅 fan-out、取消清理、故障断订阅、final terminal 重试、pause baseline、租户所有权与 synthetic terminal。

## 前端门禁

- `npm run lint` 通过（仅保留 7 个既有 warning，无 error）。
- `npm run type-check` 通过。
- Vitest 全量 100 个测试文件通过：1154 passed、11 skipped、3 todo。
- `agent-streaming.spec.ts` mocked Chromium 全量通过：9 passed、1 known-flaky skipped。
- 客户回归用例用真实时间间隔断言：卡片出现后，reasoning、正式文字和工具活动均在 final terminal 前进入 DOM，无需刷新。

## 兼容性与安全检查

- 旧前端不调用新 endpoint；新前端在 endpoint/Redis 不可用时耗尽短重试后降级状态轮询。
- endpoint 先验证当前用户的 run 所有权；跨租户与未知 run 返回相同 404 外观。
- 每个浏览器订阅持有独立 cursor，不使用 Consumer Group，不会被其他账号或标签页抢走事件。
- Redis Streams 为有界恢复层（每 run 最多 4096 条、TTL 24h），数据库仍是最终状态源。

