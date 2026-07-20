# 全 Agent 默认开放平台工具实施计划

> Track: Standard；目标到 S6 Dev，不进入生产。

## Task 1：锁定统一选择策略（RED → GREEN）

文件：
- `internal/numind/biz/agent/runner_fullopen_test.go`
- `internal/numind/biz/agent/tool_flags_resolver_test.go`
- `internal/numind/biz/agent/runner_tool_selection.go`
- `internal/numind/biz/agent/student_run_lifecycle.go`

步骤：先把旧“严格名单排除工具”的断言改为“旧 false 不能缩减平台工具”，运行定向测试确认 RED；再让选择器忽略 AgentDefinition allowlist，并把兼容 resolver 改成全开提示集合。

提交：`feat(agent): make platform tools globally available`

## Task 2：移除 ToolFlag 二次拒绝（RED → GREEN）

文件：
- `internal/numind/biz/permission/validators/tool_flag_test.go`
- `internal/numind/biz/permission/validators/tool_flag.go`

步骤：先将直接 false、分类 false 用例改为 passthrough 并确认 RED；再将验证器变为兼容性透传。运行 permission 定向测试。

提交：并入 Task 1 的单一 feature commit；不拆成 bugfix。

## Task 3：同步三个官方 Agent 清单（RED → GREEN）

文件：
- `internal/numind/biz/agent/three_agent_manifest_tools_test.go`
- `docs/agent-definitions/three-agent-feishu-pipeline/manifest.json`

步骤：测试要求全部已注册外部工具均为 true、唯一例外为 `document_generate=false`；确认 RED 后批量同步三份声明并通过测试。

提交：`feat(agent): align official agents with global tools`

## Task 4：锁定四个默认平台技能

文件：
- `internal/numind/biz/agent/skills/registry_test.go`
- 必要时技能目录装配测试

步骤：增加生产 `skills/` 根目录恰好包含四个指定技能的契约测试，同时保留 DB-bound-only 单元测试，防止未绑定租户技能全局化。

提交：并入 Task 3。

## Task 5：S4 本地审查与修复

逐项检查 Run/RunStream/Create/Answer/External Resume 的策略一致性、`document_generate` 硬禁用、安全验证器不变量和未来新增工具行为。因当前会话禁止创建未获授权的子 Agent，本次执行两轮独立本地审查并记录结果。

## Task 6：S5 验证

依次运行：

```bash
go test ./internal/numind/biz/agent/... ./internal/numind/biz/permission/...
go test -race ./internal/numind/biz/agent/... ./internal/numind/biz/permission/...
go test ./...
task lint
```

## Task 7：S6 合并与 Dev

更新 NDF manifest/决策记录，运行 `ndf-done` 原子合并并推送 develop；运行后端 Dev 发布脚本；验证外部和容器健康、启动日志、已合并版本与 Dev 镜像一致。生产不动。
