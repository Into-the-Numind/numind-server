# 三 Agent 飞书内容生产流水线实施计划

> Stage: S3 · Track: Standard · Date: 2026-07-20  
> Spec: `docs/superpowers/specs/2026-07-20-three-agent-feishu-pipeline-design.md`  
> Prompt SSOT: `docs/superpowers/specs/2026-07-20-three-agent-feishu-pipeline-authoritative-prompts.md`  
> Repositories: `numind-server`（实现）+ `numind-web-v3`（零代码回归证明）

## 1. 实施原则

1. 所有生产代码任务都执行 RED → GREEN → REFACTOR；RED 测试先提交或至少先在工作树中证明确实失败，再写实现。
2. 不新增公开 HTTP API、数据库 migration、客户表、飞书目标表、前端页面或模板自动分发。
3. `xhs_note_list` 只信任鉴权上下文中的当前用户，不接受 `user_id`。
4. 飞书 Base/Doc 的业务编排主要由三个最终 system prompt 约束；后端只补工具能力、文件完整读取和安全可观测性。
5. 当前组织的三个 AgentDefinition 通过现有 `/v1/agent/skills` API 创建或更新；源码不硬编码组织 ID、用户 ID、AgentDefinition ID 或飞书 token。
6. `numind-web-v3` 预计没有 source diff。若实现中发现必须改前端，先回退 S2 说明新接口/UX 原因，不能在 S4 顺手扩大。

## 2. Task 依赖图

```text
T1 XHS snapshot store
  └─> T2 xhs_note_list + factory

T3 file_read resumable parser/tool
  └─> T4 trusted bounded-atomic delivery

T2 + T4 ─> T6 safe Langfuse spans
T5 final prompts + AgentDefinition contract ─┘

T1..T6 ─> T7 full gates + review + S5 handoff
```

为降低同目录文件交叉和接口中途不可编译风险，S4 默认按 T1 → T2 → T3 → T4 → T5 → T6 → T7 串行。只读 reviewer 可以并行；不为这组线性依赖强行拆并行实现。

## 3. Task 1 — XHS 稳定快照读取存储契约

### 目标

在现有 XHS store 增加只服务 Agent 工具的 keyset snapshot 查询，不改变用户端 offset 分页 API。

### RED

先在 `internal/numind/store/xhs_topic_test.go` 增加失败测试：

- 243 条当前用户记录按 `id ASC`、limit 100 分三页，无重复无遗漏；
- 第一页后插入的新记录不进入已有 `snapshot_max_id`；
- 用户 A 的 ID/filter 永远看不到用户 B；
- `xhs_note_ids + keyword + [collected_from,collected_to)` 组合过滤；
- keyword 中 `%`、`_`、escape 字符按字面匹配；
- index/full projection 只选择各自需要列；
- 空结果、删除中途行和 `limit+1` 的 has-more 边界。

RED 证据：focused test 在新方法不存在或行为未实现时失败。

### GREEN / REFACTOR

修改：

- `internal/numind/store/xhs_topic.go`
- `internal/numind/store/xhs_topic_test.go`
- 所有实现 `IXhsTopicStore` 的 test fake（只补编译所需新方法；不改既有测试语义）

新增明确 DTO：snapshot filter、projection、cursor state、page result。SQL 强制 `user_id = ? AND id > ? AND id <= ? ORDER BY id ASC LIMIT limit+1`；首次按同一 filter 取得 max/count。现有 `ListNotes`、`GetByIDs`、富化队列行为保持不变。

### 验收

```bash
go test ./internal/numind/store -run 'TestXhs.*Snapshot' -count=1
go test ./internal/numind/biz/xhs -count=1
```

- focused tests PASS；
- `go test ./internal/numind/store ./internal/numind/biz/xhs` 可编译通过；
- 无 migration、无 HTTP 端点。

### Commit

`feat(agent): add user-scoped XHS snapshot reads`

## 4. Task 2 — `xhs_note_list` 工具、游标与平台注册

### 目标

把 Task 1 的 store 能力包装成当前用户只读 Agent tool，锁定 design §4 的 Schema、soft error 和安全 trace 元数据。

### RED

新增：

- `internal/numind/biz/agent/tool_xhs_note_list_test.go`
- 更新 `internal/numind/biz/agent/factory_platform_test.go`

失败测试覆盖：

- Input Schema 不存在 `user_id`，`limit` 最大 100；
- 未鉴权、limit 0/101、损坏 cursor、cursor 更换 filters/projection 返回 soft error；
- user A context 不泄露 user B；
- `projection=index` 不含正文，`projection=full` 含 Prompt 1 六类输入；
- title/content/transcript/url 缺失返回 null；count 标注 `stored_capture_value_presence_unknown`；
- comments JSON 只投影文本/一层回复，畸形 JSON 安全降级为空，不推断评论者；
- cursor canonical/base64url round-trip、filter SHA-256 校验、稳定翻页；
- factory 只有 ds 非 nil 时注册 `xhs_note_list`，metadata 为 safe/read-only；nil ds 不制造可执行空依赖工具。

### GREEN / REFACTOR

新增/修改：

- `internal/numind/biz/agent/tool_xhs_note_list.go`
- `internal/numind/biz/agent/tool_xhs_note_list_test.go`
- `internal/numind/biz/agent/factory_platform.go`
- `internal/numind/biz/agent/factory_platform_test.go`

工具从 `middleware.UserIDFromCtx` 取用户，输入 `projection/cursor/limit/xhs_note_ids/keyword/collected_from/collected_to`；输出严格使用 `xhs-note-list/v1`。验证失败走带 `error` 的 ToolResult + nil Go error，存储故障走真实 error。

### 验收

```bash
go test ./internal/numind/biz/agent -run 'TestXhsNoteList|TestPlatformToolFactory' -count=1
go test ./internal/numind/store ./internal/numind/biz/xhs ./internal/numind/biz/agent -count=1
```

- 当前用户隔离和 limit 100 都有直接测试；
- full-open runtime 中工具可发现，但 Agent 2/3 的最终 Prompt 明确禁止绕过 Agent 1 Base；
- factory 计数、顺序和 metadata assertions 全部更新。

### Commit

`feat(agent): expose current-user XHS notes to agents`

## 5. Task 3 — `file_read` UTF-8 安全续读

### 目标

让上传文件的完整解析文本按 offset 读取，不再在 parser 内永久截断到 200 KiB，并保持现有格式与所有权兼容。

### RED

先修改：

- `internal/numind/biz/agent/file_read_parsers_test.go`
- `internal/numind/biz/agent/tool_file_read_test.go`

失败测试覆盖：

- 300 KiB+ 中英文/emoji 文本多次读取后逐字节重组；
- 2/3/4-byte rune 边界回退，无乱码、重复或遗漏；
- `offset=content_bytes`、越界 offset、非 rune 边界；
- read token 首次产生、续页匹配、文件内容变化后拒绝拼接；
- `limit_bytes` 1/65536 有效，0/65537 soft error；
- 文档/text/OCR parser 返回完整规范化文本；text 20 MiB + 1 明确报错；
- 现有 attachment/output URL ownership、双签名 HEAD/GET、MIME 路由全部回归；
- 旧调用只传 `{file_url,prompt?}` 仍能得到第一页，`truncated == has_more`。

### GREEN / REFACTOR

修改：

- `internal/numind/biz/agent/tool_file_read.go`
- `internal/numind/biz/agent/file_read_parsers.go`
- `internal/numind/biz/agent/tool_file_read_test.go`
- `internal/numind/biz/agent/file_read_parsers_test.go`

parser 输出完整内容；tool 统一 `strings.ToValidUTF8`、SHA-256 read token、64 KiB max chunk 和 rune boundary slicing。输出增加 `content_byte_size/offset/returned_bytes/next_offset/has_more/read_token`，保留原字段兼容。

### 验收

```bash
go test ./internal/numind/biz/agent -run 'TestFileRead|TestDocumentParser|TestTextParser|TestImageParser' -count=1
go test ./internal/numind/biz/agent -run FileRead -race -count=1
```

- >200 KiB 内容能读到 `has_more=false`；
- 现有格式和跨用户拒绝行为不回归；
- 不新增缓存表或 migration。

### Commit

`feat(agent): make file reads resumable and UTF-8 safe`

## 6. Task 4 — 可信 `file_read` bounded-atomic 交付

### 目标

防止 64 KiB file_read 页再次被通用 16 KiB artifact preview 截断，同时不为同名 mock/外部工具开放绕过。

### RED

修改 `internal/numind/biz/agent/runner_v2_artifact_test.go`，新增失败测试：

- 可信内置 `*fileReadTool` 的完整 envelope ≤384 KiB 时原子返回，不写 artifact；
- 同名 `file_read` mock 仍走普通 artifact wrapper；
- 超过 384 KiB 返回可恢复 soft error，不把超大 payload 直接注入 context；
- 其他工具和现有 `lark_skill_read` bounded path 字节行为不变。

### GREEN / REFACTOR

修改：

- `internal/numind/biz/agent/runner_v2_artifact.go`
- `internal/numind/biz/agent/runner_v2_artifact_test.go`

只对 concrete trusted adapter 中的 `*fileReadTool` 使用 `boundedAtomicFileReadTool`。边界判断覆盖完整 JSON envelope，而不是只看 content。

### 验收

```bash
go test ./internal/numind/biz/agent -run 'Test.*Artifact.*FileRead|TestBoundedAtomic' -count=1
go test ./internal/numind/biz/compactv2 ./internal/numind/biz/agent -count=1
```

### Commit

`feat(agent): deliver bounded file pages atomically`

## 7. Task 5 — 三份最终 system prompt 与 AgentDefinition 契约

### 目标

把 S2 的“运行契约前言 + 完整业务 Prompt + 两处补丁”落实成三个可直接写入现有 Agent Builder 的版本化产物，并证明均能被当前组织的 AgentDefinition 接受。

### RED

新增 `internal/numind/biz/skill/three_agent_definition_contract_test.go`，测试在最终 prompt 文件不存在时失败。锁定：

- 三份 prompt 非空且每份 ≤ `SystemPromptMaxLen` 64 KiB；
- Prompt SSOT SHA-256 为 design 锁定值；测试按 SSOT 一级标题边界提取 Prompt 1/2/3 的完整字节，不能用最终 prompt 自己的 hash 反向证明自己；
- 每份最终 prompt 必须逐字节等于 `<对应 runtime-contract 文件> + 固定分隔符 + <对应 SSOT 完整原文经授权 patch 后的字节>`。Agent 2 的业务段与 SSOT 零 diff；Agent 1 只允许新增两行“可借鉴部分/不可照搬部分”；Agent 3 只允许新增一行“选择原因”；patch 匹配次数不是恰好 1 时测试失败；
- Agent 1 包含自动扫描、无手动勾选、默认不重分析、Base 字段/检查点、`可借鉴部分/不可照搬部分`；
- Agent 2 包含上传/飞书双输入、读完全部分页、客户级完整最新版 Doc、七个模块；
- Agent 3 包含 Agent 1/2 双上游、九字段含 `选择原因`、蓝 V/0-1、round marker append/replace；
- 三者都包含目标缺失询问、0/1/>1 精确搜索、官方授权恢复、unknown write 不盲重放和不依赖长期记忆；
- prompt 中不含真实组织 ID、用户 ID、Agent ID、飞书 token/base token/doc token；
- manifest 为三个 Agent 声明完整且精确的 `tool_flags`。Agent 1 仅启用 `ask_user_question/get_current_date/xhs_note_list/lark_skill_read/lark_inspect/lark_execute`；Agent 2/3 仅启用 `ask_user_question/get_current_date/file_read/lark_skill_read/lark_inspect/lark_execute`；当前其他平台工具及 `code_sandbox/media/dangerous` 类别显式为 false；
- 使用现有 `skill.Service.Create/Patch` 在隔离 SQLite 中创建/更新三个 definition；回读断言 name/system_prompt/tool_flags 全部持久化，Patch 后版本历史保留变更前后的 prompt hash 和 tool flags，其他 parent 无法 list/get/patch 这些 definition。

manifest 的精确 flags 集合如下，不能用“缺省即禁用”代替，因为当前 validator 对缺省键是 full-open passthrough：

| Agent | true | false |
|---|---|---|
| Agent 1 | `ask_user_question`, `get_current_date`, `xhs_note_list`, `lark_skill_read`, `lark_inspect`, `lark_execute` | 下方公共 false 集合 + `file_read` |
| Agent 2 | `ask_user_question`, `get_current_date`, `file_read`, `lark_skill_read`, `lark_inspect`, `lark_execute` | 下方公共 false 集合 + `xhs_note_list` |
| Agent 3 | `ask_user_question`, `get_current_date`, `file_read`, `lark_skill_read`, `lark_inspect`, `lark_execute` | 下方公共 false 集合 + `xhs_note_list` |

公共 false 集合：`kb_search`, `document_generate`, `image_gen`, `bash_exec`, `web_search`, `web_fetch`, `analyze_image`, `annotate_image`, `load_skill`, `create_csv`, `create_html`, `create_json`, `create_text`, `create_docx`, `create_png_chart`, `run_python`, `memory_write`, `memory_read`, `code_sandbox`, `media`, `dangerous`。契约测试同时读取 `platformToolFactory` 当前 metadata，若后来新增工具而 manifest 未明确 true/false，测试失败，避免新工具因缺省 passthrough 悄然进入这三个 Agent。

### GREEN / REFACTOR

新增：

- `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-runtime-contract.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/agent-2-runtime-contract.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/agent-3-runtime-contract.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-system-prompt.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/agent-2-system-prompt.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/agent-3-system-prompt.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/manifest.json`
- `internal/numind/biz/skill/three_agent_definition_contract_test.go`

manifest 记录 name/description/welcome/starters/runtime-contract file/prompt file/prompt SHA-256/SSOT SHA-256/完整 `tool_flags`，不记录组织或环境 ID。最终 prompt 由测试中的唯一组合器确定性重建；runtime contract 还要求在最终用户可见报告末尾输出受控 HTML comment `numind-pipeline-report/v1`，其 JSON 只允许 Agent 1 的 `processed/skipped/remaining/failed` 或 Agent 2/3 的 `source_count/output_mode`，供 Task 6 记录安全 final-run metadata。

### 验收

```bash
go test ./internal/numind/biz/skill -run TestThreeAgentDefinitionContract -count=1
go test ./internal/numind/controller/v1/agent -run 'Test(Create|Patch|ListHistory)_' -count=1
```

- 三份文件及精确 tool flags 可通过现有 POST/PATCH 直接配置，GET/List 和 history 回读值与 manifest 完全一致；
- 任意 SSOT 缺段、额外业务改写、第三处 patch、runtime-contract 漂移或 manifest/hash 漂移都会使测试失败；
- 不修改 `skill_template`，不自动创建到其他组织；
- prompt hash 可在 Dev/Prod 配置后核对。

### Commit

`feat(agent): define three Feishu pipeline agents`

## 8. Task 6 — Langfuse 安全可观测性专项

### 目标

实现 design §12 的专项 trace topology，确保新工具可诊断且不把 XHS/客户正文或飞书敏感信息写进 span。

### RED

新增/修改测试：

- `internal/numind/biz/agent/tool_xhs_note_list_test.go`
- `internal/numind/biz/agent/tool_file_read_test.go`
- `internal/numind/biz/agent/file_read_parsers_test.go`
- `internal/numind/biz/agent/pipeline_run_metrics_test.go`
- `internal/numind/biz/agent/runner_e2e_loop_test.go`
- `internal/numind/biz/agent/runner_runstream_test.go`

使用 Langfuse test client 捕获事件并先失败，断言：

- `tool.xhs_note_list.execute` input/output 只含 projection、filter kinds、limit、returned_count、has_more、duration/error class；不含 title/content/comment/note_url/xhs_note_id；
- `tool.file_read.execute` 只含 MIME、offset、limit、returned_bytes、has_more、duration/error class；不含 file URL、prompt、content、read token；
- document/OCR/direct 子 span 不再记录 presigned URL；
- 无 trace context 或 Langfuse disabled 时功能语义不变；
- error span 使用固定分类，不写原始敏感错误 body。
- final answer 中合法 `numind-pipeline-report/v1` marker 被解析为白名单字段：Agent 1 只有四个非负整数，Agent 2/3 只有非负 `source_count` 和枚举 `output_mode`；未知字段、负数、错误 agent/schema、重复或畸形 marker 全部记为 `unavailable`，绝不把原 marker/body 写日志或 trace；
- non-stream 和 stream runner 在成功结束时都调用同一 final metrics recorder；Agent 1 写 `processed/skipped/remaining/failed`，Agent 2/3 写 `source_count/output_mode`；
- 有 trace 时通过 `UpdateTraceMetadata` 写同一白名单；Langfuse disabled/无 trace 时仍写结构化应用日志且运行结果不变。缺 marker 只记录 `status=unavailable`，不把用户任务判失败。

### GREEN / REFACTOR

修改：

- `internal/numind/biz/agent/tool_xhs_note_list.go`
- `internal/numind/biz/agent/tool_file_read.go`
- `internal/numind/biz/agent/file_read_parsers.go`
- `internal/numind/biz/agent/pipeline_run_metrics.go`
- `internal/numind/biz/agent/pipeline_run_metrics_test.go`
- `internal/numind/biz/agent/runner.go`
- `internal/numind/biz/agent/runner_runstream.go`
- 对应 runner 测试
- 对应测试文件

沿用现有 `langfuse.CreateSpan/EndSpan`，不新增 LLM generation。主 Agent 的每轮 generation 继续由 runtime 产生；新 span 作为同一 `agent_run` trace 子节点。final recorder 只接受 runner 已知的 AgentDefinition 名称/ID 与 marker schema，并将输出归一为固定键；结构化日志和 `UpdateTraceMetadata` 共用同一 safe map，禁止记录 raw final answer、marker JSON、客户名或目标文件名。

### 验收

```bash
go test ./internal/numind/biz/agent -run 'TestXhsNoteList.*Langfuse|TestFileRead.*Langfuse|Test.*Parser.*Langfuse' -count=1
go test ./internal/numind/biz/agent -run 'TestPipelineRunMetrics|Test.*PipelineMetrics.*Runner' -count=1
go test ./internal/numind/biz/agent -race -run 'TestXhsNoteList|TestFileRead' -count=1
```

- 满足 `.claude/rules/ai-service.md`：没有绕过统一 Agent generation、无重复 generation、trace 关闭时降级；
- 敏感正文和 URL 正反测试通过；final-run metadata 在 trace enabled/disabled 两条路径都有直接测试。

### Commit

`feat(agent): trace pipeline tools without sensitive content`

## 9. Task 7 — 集成门、双评审与 S5 交接

### 目标

证明所有任务组合后仍满足 spec、现有产品回归和当前组织可配置性；不在 S4 偷跑 Dev 外部写入。

### 验证矩阵

先新增 `internal/numind/biz/agent/three_agent_pipeline_workflow_contract_test.go`，使用现有 runner mock-chat harness、fake `xhs_note_list/file_read/lark_execute/ask_user_question` 和固定 tool result，不调用真实 LLM/飞书。它是 S2 §13.3/13.4 的可执行编排契约，而不是人工 transcript 目测：

- Agent 1：Base scan → XHS index 全分页 → full 小批 → Base write 顺序；>100、第二次全 skip、新增一条、未完成 row upsert、显式范围逐条 upsert、重复业务键、部分成功、unknown-write 先读后判和 remaining 报告；断言不同分析结果不调用 `record-batch-update`；
- Agent 2：上传/飞书/混合来源必须全部读到末页；0/1/>1 搜索和授权恢复；create、managed overwrite、unmanaged collision、marker 损坏；未读完不能写完整画像；
- Agent 3：只能读 Agent 1 Base + Agent 2 Doc/上传件，不调用 `xhs_note_list`；新 round append、指定 round 精确 replace、unknown-write marker 对账、无标记不接管；达标/蓝 V/0-1 和九字段输出 fixture；
- 三组场景都断言最终 safe metrics marker 与实际 fake operation 计数/模式一致。

Focused command：

```bash
go test ./internal/numind/biz/agent -run 'TestThreeAgentPipelineWorkflow' -count=1
```

该文件只新增测试与 fixture；若测试暴露 T1-T6 缺陷，修复回归原所属 Task 并保留用例。它不新增生产编排引擎，也不声称 scripted model 能证明开放式 LLM 的所有未来输出。

随后运行全量门：

```bash
go test ./internal/numind/store ./internal/numind/biz/xhs ./internal/numind/biz/skill ./internal/numind/biz/agent -count=1
go test ./internal/numind/biz/agent -race -run 'TestXhsNoteList|TestFileRead|TestBoundedAtomic' -count=1
go test ./...
task lint
git diff --check
git diff --name-only develop...HEAD -- config_prod.yaml
```

在 web worktree 证明零接口回归：

```bash
npm run lint
npm run type-check
git status --short
```

### Prompt / workflow 验收清单

- 逐项运行 S2 AC-1..AC-24 的自动化对应项；
- 上述 fake-tool workflow contract tests 必须 PASS；人工 transcript review 仅作为 S5 真实模型 smoke 的补充，不计入 S4 自动化覆盖；
- 验证三份最终 prompt 的 hash、大小和历史版本契约；
- 核对 `numind-web-v3` 无 source change。

### Review

按 NDF S4 同时运行两个只读 reviewer：

1. spec-compliance：逐项对照 S2 spec/AC、检查 RED→GREEN commit 链；
2. code-quality/security：重点检查多用户隔离、cursor tamper、UTF-8/内存上限、PII trace、full-open tool 风险和外部写入契约。

任何 P0/P1 必须修复并重跑相关 gate；P2 只在有明确理由和后续项时允许保留。

### S5 / S6 handoff（本 Task 不执行外部写）

- S5 本地：启动服务和 Langfuse，跑本地 Agent/tool smoke、完整测试和 trace 检查，生成 QA 报告。
- S6 merge/deploy 后：使用当前组织父账户的现有认证调用 `/v1/agent/skills`。按 manifest 精确名称查找：0 个 create、1 个 patch、>1 个停止并要求人工消歧；写后再次 GET/List + history 回读，逐项比对 system prompt hash 和完整 tool flags；记录三个 AgentDefinition ID、version、prompt hash。
- Dev 真实验收：Agent 1 新建/复用测试 Base 并续跑；Agent 2 新/老客户画像 Doc；Agent 3 append 新轮和 replace 指定轮。Prod 保持未授权，直到独立 S7 确认。

### Commit

若 Task 7 仅验证不改代码则不制造空 commit；如修复 review 发现，按 `fix(agent): ...` 独立提交并重新 review。

## 10. S3 原子性与依赖自检

| Task | 完成后可编译 | 可独立验证 | 依赖 | 文件重叠处理 |
|---|---|---|---|---|
| T1 | 是 | store + XHS package | 无 | 接口 fake 同 task 补齐 |
| T2 | 是 | Agent tool/factory focused tests | T1 | T1 后串行 |
| T3 | 是 | file parser/tool focused tests | 无 | 与 T2 串行，避免 agent 目录交叉 |
| T4 | 是 | wrapper + compactv2 tests | T3 | 只改 wrapper 文件 |
| T5 | 是 | skill service + prompt contract | S2 | 文档与 skill test 独立 |
| T6 | 是 | Langfuse capture tests | T2,T4 | 明确串行回访 tool 文件 |
| T7 | 是 | full gates + reviewer | T1-T6 | 只验证或独立修复 |

依赖图无环。每个任务完成时仓库可编译；没有把后端和不存在的前端实现塞进同一个 task。

## 11. S3 Gate

- [x] 每个 Task 有编号、目标、文件、RED、GREEN、验收命令和 commit 边界；
- [x] 覆盖 S2 的 XHS、file_read、三 Prompt、Base/Docs 编排、trace 和当前组织配置；
- [x] 无新 HTTP API 时已明确多仓库兼容边界；
- [x] Langfuse 有独立 Task 6；
- [x] 依赖无环且后端先于任何潜在前端；
- [x] 独立原子性 reviewer PASS；
- [x] manifest stage/artifacts/progress 更新并提交后进入 S4。

### 独立原子性复核结果

2026-07-20 由独立只读 reviewer 对修订后的计划复核：`PASS`，P0=0、P1=0、P2=0。Reviewer 确认 T1-T7 均能独立编译和验证、依赖图无环，并完整覆盖 limit=100、当前用户隔离、`file_read` 全分页、三份确定性 Prompt、精确 tool flags、当前组织配置回读、安全 final metrics、unknown-write 与三个 Agent 的 fake-tool workflow contract。
