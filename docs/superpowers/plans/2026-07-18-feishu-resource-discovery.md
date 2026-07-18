# 飞书资源发现与标题读取 — 实施计划

## Task 1 — 客户失败复现（RED）

修改测试文件，不改生产代码：

- `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`
- `internal/numind/biz/feishu/skill_reader_test.go`
- `internal/numind/biz/feishu/command_catalog_test.go`
- `internal/numind/biz/feishu/operation_service_test.go`
- `internal/numind/store/feishu_workspace_test.go`（若现有测试文件名不同，使用对应 capability 测试文件）

证明：lark-drive schema/receipt、`drive +search`、`wiki +space-list`、drive scope/capability 在当前 develop 上失败。运行聚焦测试确认 RED 后，以 `test(qa): reproduce Feishu title discovery gap` 作为首个代码 commit。

## Task 2 — 技能与 Agent 指导

- `skill_reader.go` 加入 Drive domain/skill/exact receipt set。
- `tool_lark_skill_read.go` 同步 schema、description、hosted policy、拒绝文案。
- `tool_lark_execute.go` 同步受控 domain description。

运行 agent + skill_reader 聚焦测试，GREEN 后提交 `fix(agent): expose hosted Lark Drive discovery skill`。

## Task 3 — 只读命令目录与授权 scope

- `command_catalog.go` 增加严格的 `drive +search` 和 `wiki +space-list`。
- `device_auth_flow.go` 将 driveScopes 纳入 canonical allowlist。
- 为 query Unicode 长度、doc-types、分页与永久拒绝补边界测试。

运行 command catalog、device auth、operation 聚焦测试，GREEN 后提交 `fix(feishu): allow safe resource discovery commands`。

## Task 4 — Capability 持久化与状态投影

- `operation_service.go` 支持 drive capability outcome。
- `store/feishu_workspace.go` 保留和校验 drive 状态。
- `biz/feishu/service.go` 默认并投影 drive capability。
- 补旧 JSON 向后兼容与非法 domain fail-closed 测试。

运行 store/service/operation 聚焦测试，GREEN 后提交 `fix(feishu): persist Drive discovery capability state`。

## Task 5 — S4 双审与修复

- 规格审查：逐条核对不变量、命令/scope、用户隔离、非目标。
- 质量/安全审查：检查 allowlist、参数归一化、receipt 绑定、日志与错误泄漏。
- 所有 P0/P1/P2 必须修复并重跑聚焦测试。

## Task 6 — S5 自动验收

- `gofmt`（仅改动 Go 文件）
- `go test` 聚焦包
- `go test ./...`
- Feishu/agent/store 相关 race gate
- `task lint`
- secret/config/router/diff hygiene 检查
- 写 QA 报告并更新 NDF stage。

## Task 7 — S6/S7 合并、部署与验证

- 在 worktree 内运行 `ndf-done`，原子合并 develop、push、清理 worktree/branch。
- 从 develop 运行 `bash scripts/cicd/release.sh dev server`。
- 验证镜像 commit、容器 healthy、公共 `/healthz`、lark-cli=1.0.68、部署后 critical logs。
- 服务级验证固定目录 manifest 包含 `drive +search` 与 `wiki +space-list`。
- 产品验收提示词：新对话发送“请读取飞书文档「有数飞书二次连接测试」，告诉我标题和正文，不要修改任何内容。”首次缺 scope 应出现增量授权卡片；授权后原操作自动恢复。

