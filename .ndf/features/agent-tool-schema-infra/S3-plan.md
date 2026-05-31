# S3 Plan — agent-tool-schema-infra

TDD 优先（superpowers:test-driven-development）：每个 task 先写/扩展失败测试，再实现到绿。
每个 task 完成后 commit，并按 NDF Rule 6 跑 **两个并行 Sonnet reviewer**（spec-compliance +
code-quality），双 PASS 后 `reviewed_tasks += 1` 才进入下一个 task。

所有代码改动在 worktree `/private/tmp/wt-agent-tool-schema-infra-numind-server` 内进行。

## Task 1（keystone / 基础设施）— adapter Info() 消费 InputSchema()

**文件**：`internal/numind/biz/agent/adapter_full_to_eino.go`,
`internal/numind/biz/agent/adapter_full_to_eino_test.go`

- 新增 helper `paramsOneOfFromInputSchema(raw json.RawMessage) *schema.ParamsOneOf`
  （nil/空/非法 JSON → 空 params；合法 → `NewParamsOneOfByJSONSchema`）。
- `Info()` 改为调用该 helper。
- 测试（先写，红→绿）：
  - populate：stub 工具返回标准 JSON Schema → `Info()` 的 ParamsOneOf `ToJSONSchema()`
    含预期 properties/required。
  - fallback-nil：stub 返回 nil → 不 panic，params 为空 object schema。
  - fallback-malformed：stub 返回 `{"type":` 非法 JSON → 不 panic，回退空。
  - 回归：已有 8 schema 之一（如真实 run_python 适配）→ ParamsOneOf 非空。

**验收**：`go test -run Adapter ./internal/numind/biz/agent/...` 绿；这一步即让 8 个既有
schema 立刻生效（最大价值、最小改动）。**这是 keystone，必须最先做且单独 review。**

## Task 2 — 沙箱/副作用类工具 schema（最高风险优先）

**文件**：`tool_bash_exec.go`, `tool_memory_write.go`, `tool_ask_user_question.go`
+ 对应 `_test.go`。

- 各加 `InputSchema()`，内容见 S2 §3。
- 各加/扩展 schema-shape 单测：断言非空 + properties/required 与 Execute struct json tag 一致。
- run_python / use_skill 已有 schema，本 task 不动（仅在 Task 1 验证其经 adapter 生效）。

## Task 3 — 读取类工具 schema（出错只是空结果，风险低）

**文件**：`tool_web_search.go`, `tool_web_fetch.go`, `tool_kb_search.go`,
`tool_file_read.go`, `tool_memory_read.go` + 对应 `_test.go`。

- 各加 `InputSchema()`（见 S2 §3）+ schema-shape 单测。

## Task 4 — 图像/数据/生成类工具 schema

**文件**：`tool_analyze_image.go`, `tool_annotate_image.go`, `tool_image_gen.go`,
`tool_learner_data_query.go`, `tool_document_generate.go`, `tool_read_skill.go`
+ 对应 `_test.go`。

- 各加 `InputSchema()`（见 S2 §3）+ schema-shape 单测。
- get_current_date 不动（无参）。

## Task 5 — S5 验证策略（NDF Rule 10，必须独立列出）

**验证方式**：**纯后端 TDD（Go 单测）**。

**理由**：
1. 本 feature 是纯后端 schema 改动，无任何前端可视面 → 排除 Playwright / gstack `/qa`。
2. 改动的“正确性”= schema 形状与 Execute 契约一致 + adapter 三态回退 + schema 真正进入
   model 请求构造路径。这些都能被确定性 Go 单测覆盖，且**留在代码库做永久回归保护**
   （契合 Rule 10 对“回归保护诚实声明”的要求——单测是持久化的，不像 gstack 一次性）。
3. 非支付/权限/会员高风险逻辑，无需 E2E。

**关键验证路径（S5 必须跑）**：
- a) `go test ./internal/numind/biz/agent/...` 全绿（adapter 三态 + 14 工具 schema-shape）。
- b) **弱模型路径专项**：用 mock `ToolCallingChatModel`（实现 eino model 接口）捕获
   `BindTools`/请求构造时传入的 `[]*schema.ToolInfo`，断言关键工具（web_search、bash_exec）
   的 `ParamsOneOf` 非空且 `ToJSONSchema()` 含预期字段——证明 schema 真的会被下发给（任意，
   含弱）模型，而非停留在 adapter 内部。这是对工单“弱模型参数构造改善”的可测代理。
- c) `task lint` 通过。

**回归保护声明**：本 feature 全部验证为持久化 Go 单测，未来修改 adapter 或工具 schema 时
自动回归，无需手动重跑。

## 文件归属（与并行 feature 的 disjoint 确认）

本 feature 触及文件集合：
`adapter_full_to_eino.go`(+test), `tool_bash_exec.go`(+test), `tool_memory_write.go`(+test),
`tool_ask_user_question.go`(+test), `tool_web_search.go`(+test), `tool_web_fetch.go`(+test),
`tool_kb_search.go`(+test), `tool_file_read.go`(+test), `tool_memory_read.go`(+test),
`tool_analyze_image.go`(+test), `tool_annotate_image.go`(+test), `tool_image_gen.go`(+test),
`tool_learner_data_query.go`(+test), `tool_document_generate.go`(+test), `tool_read_skill.go`(+test).

并行 feature 触及：`tool_full.go`, `base_tool.go`, `tool_use_skill.go`, `runner.go`（+ 权限层文件）。
**交集为空。** merge 前 `git fetch` 复查。

## Task 顺序与 review gate

Task1 → (dual review) → Task2 → (dual review) → Task3 → (dual review) → Task4 → (dual review)。
Task5 是策略声明（已随本 plan 由 S3 gate reviewer 审），不产代码。
全部 reviewed 后：worktree 内 `task lint` + `go test ./...`（agent 包）→ S5 跑 a/b/c → `ndf-done` → `/deploy-dev server`。
