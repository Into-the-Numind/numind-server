# Agent 附件单次解析实施计划

> Track: 快速 Standard；后端 + 前端；完成至 S6 Dev，Prod 不动。

## Task 1：锁定数据库与存储契约（RED → GREEN）

文件：
- `internal/pkg/model/agent_attachment.go`
- `internal/numind/store/agent_attachment_store.go`
- 对应 store/fallback 测试
- `migrations/20260721_000000_agent_attachment_parsed_content.sql`
- `migrations/20260721_000000_agent_attachment_parsed_content_rollback.sql`

先增加测试证明规范正文、token、字节数和 URL+用户查询缺失并确认 RED；再新增字段、migration、回填和 store 方法。提交 RED 与 GREEN 分离。

## Task 2：让上传 worker 成为唯一解析生产者（RED → GREEN）

文件：
- `internal/numind/biz/agent/attachment/fallback_service.go`
- `internal/numind/biz/agent/attachment/templates.go`
- `internal/numind/biz/attachment/upload.go`
- 对应测试

覆盖 document/text 成功解析和其他 modality 规范内容落库；token 必须由标准化 UTF-8 正文生成，成功字段原子写入。将 text/markdown 从 unknown 改为 text 并纳入恢复队列。

## Task 3：把 file_read 改成缓存分页接口（RED → GREEN）

文件：
- `internal/numind/biz/agent/tool_file_read.go`
- `internal/numind/biz/agent/factory_platform.go`
- `internal/numind/biz/agent/tool_file_read_test.go`
- factory/schema 测试

先写附件 ID 缓存命中、URL 缓存命中、parser/HEAD 零调用、pending/failed/越权和多页重组用例；再注入 attachment store、增加 schema，并将现有 URL 网络解析保留为无 DB 记录时的兼容分支。

## Task 4：模型只接收引用，不接收全文（RED → GREEN）

文件：
- `internal/numind/biz/agent/multimodal.go`
- `internal/numind/biz/agent/student_run_lifecycle.go`
- 对应 lifecycle/multimodal 测试

断言 ready `text_fallback` 不出现在 input，固定引用含 attachment_id 与 `file_read` 指令；IDs 和 ID-less URLs 同时合并，显示附件不回归。更新系统附件 reminder 语义。

## Task 5：前端 ID 优先请求（RED → GREEN）

文件：
- `src/types/agent.ts`
- `src/stores/agentChat.ts`
- `src/views/agent/AgentChatView.vue`
- `src/stores/__tests__/agentChat.spec.ts`
- `src/views/agent/__tests__/AgentChatView.spec.ts`

先把测试改为上传 ID 正常发送、零 ID 才发送 URL并确认 RED；再更新流式与非流式请求。无视觉变化，不改附件 chip。

## Task 6：S4 本地双轮审查

由于本次未获授权创建审查子 Agent，执行两轮相互独立的本地审查：

1. 数据/安全轮：租户归属、SSRF、migration/rollback、状态原子性、正文泄露和 rolling compatibility。
2. 行为/质量轮：stream/non-stream 一致性、分页完整性、旧附件/agent-output 兼容、前端真实请求和测试有效性。

发现 P0/P1 必须修复并重跑相关测试；记录于 `.ndf/decisions/attachment-single-parse-file-read/`。

## Task 7：S5 完整验证

后端：

```bash
go test ./internal/numind/store ./internal/numind/biz/attachment ./internal/numind/biz/agent/attachment ./internal/numind/biz/agent
go test -race ./internal/numind/biz/agent/attachment ./internal/numind/biz/agent
go test ./...
PATH="$(go env GOPATH)/bin:$PATH" task lint
```

前端：

```bash
npm run lint
npm run type-check
npm run test:unit -- --run
```

两仓库运行 `git diff --check`，确认未改 `config_prod.yaml`。

## Task 8：S6 合并、推送与 Dev

先在两个 worktree 更新 NDF 记录，运行 `ndf-done` 原子合并并推送。按后端后前端顺序部署 Dev：

1. 后端 exact develop image，确认 migration、容器/public health、无 panic/fatal。
2. 前端 exact develop image，确认 web health 与 `/api/healthz` proxy。
3. 用只读浏览器/网络断言确认上传响应 ID 进入 Agent run 请求；不触发客户文件或飞书写入。
4. 记录镜像 digest 与验收结果。Prod 不部署。
