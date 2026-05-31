# S1 Proposal + PRD — agent-tool-schema-infra

## 1. 问题陈述

Eino 适配器 `adapter_full_to_eino.go` 的 `Info()` 方法（行 40-46）把每个工具的
function-calling parameters schema 写死成空对象：

```go
ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
```

LLM 因此只能从 `Description()` 自然语言猜测参数结构。强模型靠猜能蒙对；弱模型
（fallback chain 的 doubao-seed-2-0-lite / qwen-turbo）参数构造错误率显著升高，
且无法启用 OpenAI structured outputs strict validation（strict 要求非空 schema）。

**根因比工单更深**：`FullTool` 接口已声明 `InputSchema() json.RawMessage`
（`tool_full.go:18`），`BaseTool` 默认返回 `nil`（`base_tool.go:22`），且已有
8 个工具实现了它（run_python / use_skill / create_json / create_text /
create_html / create_csv / create_png_chart）。但 **adapter 从不读 `InputSchema()`**，
所以连这 8 个写好的 schema 都送不到 LLM。基础设施差“最后一公里”。

## 2. 目标 / 非目标

### 目标
- adapter `Info()` 消费 `ft.InputSchema()`：JSON Schema → `schema.NewParamsOneOfByJSONSchema`。
- `nil` 或解析失败 → 回退到当前空 params 行为（**零回归，防御性**）。
- 给剩余 ~14 个 LLM 工具补 `InputSchema()`，内容与各自 `Execute()` 反序列化结构体一致。
- 每个改动有单测保护。

### 非目标
- **不改 `InputSchema()` 接口签名**（保持 `json.RawMessage`）。改签名会触及
  `tool_full.go`/`base_tool.go`/全部 22 工具，并与并行 feature 强冲突（见 §5）。
- **不在 adapter 热路径加运行时参数校验**（不在 `InvokableRun` 里跑 jsonschema 校验
  拦截 bad args）。本期只让 LLM *看到* schema；运行时 strict 拦截是独立后续项
  （风险更高，改热路径）。S5 用单测证明 schema 形状正确即可。
- 不动 `get_current_date`（天然无参；`nil` → 空 params 是正确表达）。
- 不动 `runner.go` 工具注册、不动 `use_skill`/`read_skill` 合并（属并行 feature）。

## 3. 用户故事

- 作为 agent 运行时，当我用弱 fallback 模型时，我能从结构化 schema 知道
  `web_search` 需要 `{query, max_results(1-10), allowed_domains[]}`，而不是从英文
  描述里猜字段名/类型，从而减少 tool-call 参数构造失败。
- 作为平台维护者，我能在将来一行开关启用 structured outputs strict，因为每个工具
  都已暴露非空 parameters schema。

## 4. 验收标准（PRD）

| # | 标准 | 验证方式 |
|---|------|---------|
| A1 | adapter `Info()` 对有 `InputSchema()` 的工具返回非空 `ParamsOneOf`（含正确 properties/required） | 单测 `adapter_full_to_eino_test.go` |
| A2 | adapter `Info()` 对 `InputSchema()==nil`（BaseTool 默认）的工具回退空 params，且不 panic、不报错 | 单测（用 stub 工具 / get_current_date） |
| A3 | adapter `Info()` 对 `InputSchema()` 返回非法 JSON 的工具回退空 params，不 panic | 单测（malformed schema stub） |
| A4 | 14 个目标工具均返回与 Execute struct json tag 一致的 schema（字段名/类型/required） | 每工具 schema-shape 单测 |
| A5 | `factory_sandbox_hooks.go` 两处 `t.Info(ctx)` 调用不被破坏 | build + 既有测试通过 |
| A6 | `task lint` 通过；`go test ./internal/numind/biz/agent/...` 全绿 | CI/本地 |

## 5. 跨 feature 协同

并行 Standard feature `agent-mode-open-tools-skill-as-guidance`（另一 session）触及
`tool_full.go` / `base_tool.go` / `tool_use_skill.go` / `runner.go`。

**经 S0-D1 设计，本 feature 与其 ZERO 文件重叠**：保留 `InputSchema()` 签名后，本
feature 仅改 `adapter_full_to_eino.go` + 14 个待补 schema 的 `tool_*.go`（其中 *不含*
`use_skill`，它的 schema 已存在）+ 新增/扩展对应 `_test.go`。因此无需协调 land 顺序。
merge 前仍 `git fetch origin develop` 复查对方是否意外动了 adapter 或这 14 个工具文件。

## 6. 技术选型

- eino v0.8.13 提供 `schema.NewParamsOneOfByJSONSchema(*jsonschema.Schema)`。
- `github.com/eino-contrib/jsonschema@v1.0.3` 已是 indirect 依赖；其 `Schema` 类型
  实现 `UnmarshalJSON`，可直接 `json.Unmarshal(rawJSONSchema, &js)`，无需新增依赖。
- 转换封装为一个内部 helper（如 `paramsOneOfFromInputSchema(raw json.RawMessage) *schema.ParamsOneOf`），
  集中处理 nil / 空 / 解析失败的回退，便于单测与复用。
