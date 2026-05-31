# Agent 工具 function-calling schema 基础设施

## 来源
- 提出人：技术债梳理（内部）
- 提出日期：2026-05-31

## 需求描述
`internal/numind/biz/agent/adapter_full_to_eino.go` 的 Eino 适配器 `Info()` 方法把工具 schema 写死成空对象：

```go
ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}), // ← 空！
```

后果：**LLM 收到的 function-calling schema 里 parameters 字段是空 `{}`**，模型只能从 `Description()` 自然语言反推该传什么参数。

现象学根因（本 session 实测确认，比工单描述更深一层）：
1. `FullTool` 接口**已经**声明了 `InputSchema() json.RawMessage`（`tool_full.go:18`），`BaseTool` 默认返回 `nil`（`base_tool.go:22`）。
2. **已有 8 个工具实现了 `InputSchema()`**：`run_python` / `use_skill` / `create_json` / `create_text` / `create_html` / `create_csv` / `create_png_chart`（+ BaseTool 默认）。
3. **但适配器 `Info()` 完全无视 `ft.InputSchema()`**，永远返回空 params——所以连这 8 个已写好的 schema 都送不到 LLM。

即：基础设施一半已就位（接口 + 默认值 + 部分实现），唯独“最后一公里”——适配器消费 `InputSchema()`——缺失，导致全部努力归零。

## 业务目标
- 让 LLM 拿到结构化、机器可读的 function-calling 参数 schema，而非靠 `Description()` 自然语言猜参。
- 当前强模型（deepseek-v3-2 / glm-4-7）能猜对是侥幸；fallback chain 的弱模型（doubao-seed-2-0-lite / qwen-turbo）参数构造错误率会陡升。修复后弱模型路径鲁棒性显著提升。
- 为将来启用 OpenAI structured outputs strict validation 打基础（strict 模式要求非空 parameters schema）。
- 补全 `FullTool` 接口的“半成品约定”：让 `InputSchema()` 真正生效。

## 优先级
中（技术债，无线上事故，但影响弱模型可靠性 + 阻塞 strict outputs）

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：否
  2. 新增 API 端点：否
  3. 新外部服务集成：否（仅用已有 indirect dep `eino-contrib/jsonschema`）
  4. 影响文件数：**>3**（adapter + ~14 个待补 schema 的 tool_*.go + 对应 _test.go）
  5. 高风险业务逻辑（支付/权限）：否，但**触及 agent 执行热路径**（每次工具调用都过 adapter `Info()`），回归面需谨慎
- 人类决定：**已确认 Standard**（用户在派单时预判，理由同上）

## 验收口径
- adapter `Info()` 把 `ft.InputSchema()`（JSON Schema）转成 `schema.NewParamsOneOfByJSONSchema`；`nil`/解析失败时回退到当前空 params 行为（**零回归**）。
- 全部 ~22 个 LLM 工具（除天然无参的 `get_current_date`）都有非空、与 `Execute()` 反序列化结构体一致的 `InputSchema()`。
- 每个工具有 schema-shape 单测：断言 schema 的 properties/类型/required 与 Execute struct 的 json tag 一致；adapter 有 populate + fallback 测试。
- `task lint` + `go test ./internal/numind/biz/agent/...` 全绿。

## 跨 feature 依赖（重要）
并行 Standard feature `agent-mode-open-tools-skill-as-guidance`（另一 session）同样会碰：
`tool_full.go` / `base_tool.go` / `tool_use_skill.go` / `runner.go`（工具注册段 ~793-867）。

- 本 feature 是**基础设施**，约定**先 land**；对方 rebase 到本 feature 之上。
- 实测：截至 2026-05-31，对方仅向 develop 提交了 S0 requirement card（commit 34ede324），**尚未动任何代码**——本 feature 安全先行。
- merge 前必须 `git fetch` + 复查对方是否已动上述文件；若对方先 land，则本 feature rebase。

## 备注
- eino 版本 v0.8.13，提供 `schema.NewParamsOneOfByJSONSchema(*jsonschema.Schema)`；`github.com/eino-contrib/jsonschema@v1.0.3` 已是 indirect 依赖，其 `Schema` 类型有 `UnmarshalJSON`，可直接 `json.Unmarshal(rawJSONSchema, &js)`。
- `factory_sandbox_hooks.go:87,150` 也调用 `t.Info(ctx)`——改 `Info()` 行为时必须确认不破坏这两处（S2 设计阶段核查）。
