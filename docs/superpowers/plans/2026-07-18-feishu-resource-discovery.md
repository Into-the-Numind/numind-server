# 飞书资源发现与标题读取 — 实施计划

## Task 1 — 客户失败复现（RED）

修改测试文件，不改生产代码：

- `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`
- `internal/numind/biz/feishu/skill_reader_test.go`
- `internal/numind/biz/feishu/command_catalog_test.go`
- `internal/numind/biz/feishu/operation_service_test.go`
- `internal/numind/store/feishu_workspace_test.go`（若现有测试文件名不同，使用对应 capability 测试文件）

证明：lark-drive schema/receipt、`drive +search`、`wiki +space-list`、drive scope/capability 在当前 develop 上失败。运行聚焦测试确认 RED 后，以 `test(qa): reproduce Feishu title discovery gap` 作为首个代码 commit。

## Task 2–4 — 原子实现技能、命令、授权与 capability

- `skill_reader.go` 加入 Drive domain/skill/exact receipt set。
- `tool_lark_skill_read.go` 同步 schema、description、hosted policy、拒绝文案，并固化 docx/base/wiki 搜索后 receipt 切换。
- `tool_lark_execute.go`、`factory_platform.go`、`tool_bash_exec.go` 同步受控 domain description。
- 回归运行 Run/RunStream full-open、模型输入无 Agent 飞书凭据、同 Agent 两用户隔离、同用户多 Agent 均取当前 request user_id。

- `command_catalog.go` 增加严格的 `drive +search` 和 `wiki +space-list`；Drive 搜索强制 `--only-title` 与 `--doc-types docx,wiki,bitable`（或其受支持子集）。
- `device_auth_flow.go` 将 driveScopes 纳入 canonical allowlist。
- `operation_service.go` 支持 drive capability outcome。
- `store/feishu_workspace.go` 保留和校验 drive 状态。
- `biz/feishu/service.go` 默认并投影 drive capability。
- 为 query 的 30/31 中文和 emoji、invalid UTF-8、所有控制字符、doc-types、opaque page token、分页与永久拒绝补边界测试。
- 补旧 JSON 向后兼容、非法 domain fail-closed，以及两个用户通过同一平台 Agent 入口产生各自 operation/capability 的隔离测试。

以上生产改动必须作为一个 deployable 原子 commit：任一中间状态不得单独提交或部署。运行 agent/skill/catalog/device-auth/store/service/operation 聚焦测试全部 GREEN 后提交 `fix(feishu): add user-scoped resource discovery`。

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

## Task 7 — S6 合并、Dev 部署与验证

- 在 worktree 内运行 `ndf-done`，原子合并 develop、push、清理 worktree/branch。
- 从 develop 运行 `bash scripts/cicd/release.sh dev server`。
- 验证镜像 commit、容器 healthy、公共 `/healthz`、lark-cli=1.0.68、部署后 critical logs。
- 服务级验证固定目录 manifest 包含 `drive +search` 与 `wiki +space-list`。
- 产品验收提示词：新对话发送“请读取飞书文档「有数飞书二次连接测试」，告诉我标题和正文，不要修改任何内容。”首次缺 scope 应出现增量授权卡片；授权后原操作自动恢复。
- S7 生产发布不在本任务授权范围内，保持未进入；必须等待独立生产授权和版本 tag。
