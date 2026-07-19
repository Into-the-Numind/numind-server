# 飞书 Base 授权后终态实施计划

## Task 1 — 客户故障 RED

- 修改 `internal/numind/biz/feishu_resume_dispatcher_test.go`。
- 复现 unknown operation 被错误送进 `Resume`，并锁定期望：调用 terminal finalizer、dispatcher 不返回 500 上游错误。
- 单独提交 `test(qa): reproduce feishu base terminal resume 500`，测试在现有实现上必须 FAIL。

## Task 2 — Dispatcher 终态分流

- 修改 `internal/numind/biz/feishu_resume_dispatcher.go` 及测试 fake。
- succeeded 继续 Resume；failed/unknown/cancelled 映射 durable finalizer。
- 覆盖成功、幂等 no-op、暂时错误、并发和跨实例语义。

## Task 3 — 安全 operation diagnostics

- 修改 `internal/numind/biz/feishu/operation_service.go`、`internal/numind/biz/feishu_adapter.go` 及对应测试。
- 在 invoke 分类和 Agent handoff 边界发出严格 allowlist observation。
- 不改变 write unknown/no-replay 决策，不持久化原始 CLI 输出。

## Task 4 — 集成验收与交付

- 增加授权完成后 operation unknown 的 lifecycle 回归。
- 运行 focused tests、`go test ./...`、Feishu/Agent race、`task lint`。
- 独立规格与质量评审，无 P0/P1 后进入 S5。
- `ndf-done` 合并推送 develop，部署 Dev，检查 healthz、容器健康和安全日志字段；Prod 不在范围内。

## 依赖与原子性

Task 1 → Task 2 → Task 3 → Task 4 串行。Dispatcher interface 与 composition/fakes 有交集，不做并行写；只读 reviewer 可并行。
