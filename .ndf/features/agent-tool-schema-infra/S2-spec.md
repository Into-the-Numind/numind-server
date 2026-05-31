# S2 Spec — agent-tool-schema-infra

## 1. 架构概览

```
LLM  ◄── function-calling tools[].parameters ──  Eino ChatModel
                                                      ▲
                                          react.AgentConfig.ToolsConfig.Tools
                                                      ▲
                          einotool.InvokableTool.Info(ctx) → *schema.ToolInfo
                                                      ▲
                              fullToolEinoAdapter.Info()  ◄── 本期改这里
                                                      ▲
                                   FullTool.InputSchema() json.RawMessage
                                       (BaseTool 默认 nil / 8 已实现 + 14 待补)
```

`Info()` 是 LLM 看到的工具契约的唯一来源。当前它丢弃 `InputSchema()` → params 永远空。

## 2. 核心改动：adapter Info() + 转换 helper

### 2.1 helper（新增，同文件 `adapter_full_to_eino.go`）

```go
// paramsOneOfFromInputSchema converts a tool's JSON-Schema (json.RawMessage,
// as returned by FullTool.InputSchema()) into an Eino *schema.ParamsOneOf.
//
// Defensive fallback (ZERO regression): on nil / empty / unparseable input it
// returns the historical empty-params value, so a tool that has no schema (or a
// malformed one) behaves exactly as before this change — Info() never errors and
// never panics on account of the schema.
func paramsOneOfFromInputSchema(raw json.RawMessage) *schema.ParamsOneOf {
    if len(bytes.TrimSpace(raw)) == 0 {
        return schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})
    }
    var js jsonschema.Schema
    if err := json.Unmarshal(raw, &js); err != nil {
        // Malformed schema authored by a tool — log + fall back, do not break the run.
        log.Warnw("paramsOneOfFromInputSchema: invalid InputSchema JSON, falling back to empty params",
            "err", err)
        return schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})
    }
    return schema.NewParamsOneOfByJSONSchema(&js)
}
```

import 追加：`bytes`、`github.com/eino-contrib/jsonschema`。

### 2.2 Info() 改动

```go
func (a *fullToolEinoAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
    return &schema.ToolInfo{
        Name:        a.ft.Name(),
        Desc:        a.ft.Description(),
        ParamsOneOf: paramsOneOfFromInputSchema(a.ft.InputSchema()),
    }, nil
}
```

**不变量保持**：返回 `(*schema.ToolInfo, error)` 签名不变；error 永远 nil（与现状一致），
因此 `factory_sandbox_hooks.go:87,150` 的 `info, err := t.Info(ctx)` 调用语义不变（A5）。

## 3. 14 个工具的 InputSchema() 契约

每个 schema 严格对应该工具 `Execute()` 里 `json.Unmarshal` 的目标结构体的 json tag。
`required` 取 Execute 中“缺失即报错/无意义”的字段。

| 工具 | properties（type） | required |
|------|--------------------|----------|
| bash_exec | command(string) | [command] |
| memory_write | kind(string), key(string), value(string) | [kind, key, value] |
| memory_read | key(string), kind(string), limit(integer 1-50) | [] |
| ask_user_question | question(string), options(array<{key,label}>, 2-4), header(string ≤12), multi_select(boolean) | [question, options] |
| web_search | query(string), max_results(integer 1-10, default 5), allowed_domains(array<string>) | [query] |
| web_fetch | url(string, format uri), prompt(string) | [url] |
| kb_search | query(string), doc_ids(array<integer>) | [query] |
| file_read | file_url(string, format uri), prompt(string) | [file_url] |
| analyze_image | attachment_url(string, format uri), question(string) | [attachment_url] |
| annotate_image | attachment_url(string, format uri), regions(array<{x:int,y:int,width:int,height:int,label:string}>) | [attachment_url, regions] |
| image_gen | prompt(string) | [prompt] |
| learner_data_query | user_id(integer), field(string) | [user_id] |
| document_generate | prompt(string), format(string) | [prompt] |
| read_skill | skill_name(string) | [skill_name] |

> get_current_date：无参，保持 BaseTool 默认 `nil` → 回退空 params。✔ 正确表达。

每个 schema 顶层为 `{"type":"object","properties":{...},"required":[...]}`，字段带
`description`（帮助弱模型），与既有 8 个 schema 的写法风格一致。

## 4. 边界与回退

- nil schema（BaseTool 默认） → 空 params（历史行为）。
- 空字符串 / 全空白 → 空 params。
- 非法 JSON → log.Warn + 空 params（不 panic、不中断 run）。
- 合法 JSON Schema → `NewParamsOneOfByJSONSchema`。
- eino 在 `ToJSONSchema()` 阶段消费 `jsonschema` 分支；本期不调用 `ToJSONSchema()`，
  由 eino 框架在构造 model 请求时调用。helper 只负责构造 `*schema.ParamsOneOf`。

## 5. 测试策略（详见 S3 的 S5 task）

- adapter 层：populate（有 schema）/ fallback-nil / fallback-malformed 三态。
- 工具层：每工具断言 InputSchema() 非空且 properties/required 与 Execute struct 对齐
  （可用一个表驱动测试 + 反射对比 json tag，或逐工具显式断言）。
- 弱模型路径：用 mock ToolCallingChatModel 断言传入的 tools[].ParamsOneOf 非空（证明
  schema 真的进入了 model 请求构造路径），无需真实调用弱模型。

## 6. 不变量校验清单（reviewer 用）

- [ ] `Info()` 签名不变，error 永远 nil。
- [ ] helper 对 nil/空/非法 JSON 三种输入都回退空 params 且不 panic。
- [ ] 未新增第三方依赖（jsonschema 已是 indirect；go.mod 可能从 indirect 提升为 direct）。
- [ ] 不改 tool_full.go / base_tool.go / runner.go / tool_use_skill.go。
- [ ] 14 工具 schema 与各自 Execute struct json tag 严格一致。
