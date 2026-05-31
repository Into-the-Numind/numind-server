# S5 Acceptance Record — agent-tool-schema-infra

## 验证方式
**纯后端 TDD（Go 单测）** — 见 S3 plan Task 5。本 feature 是纯后端 schema 元数据改动，
无前端可视面，故不走 Playwright / gstack `/qa`。全部验证为持久化 Go 单测，未来修改 adapter
或任一工具 schema 时自动回归（回归保护诚实声明：单测持久化，非一次性）。

## 验收标准结果（对应 S1 PRD §4）

| # | 标准 | 结果 | 证据 |
|---|------|------|------|
| A1 | adapter Info() 对有 InputSchema() 的工具返回非空 ParamsOneOf（properties/required 正确） | ✅ PASS | `TestAdapterInfo_PopulatesSchema` |
| A2 | InputSchema()==nil 回退空 params，不 panic / 不报错 | ✅ PASS | `TestAdapterInfo_FallbackNilSchema` + `TestAdapterInfo_FallbackEmptySchema` |
| A3 | InputSchema() 非法 JSON 回退空 params，不 panic | ✅ PASS | `TestAdapterInfo_FallbackMalformedSchema` + `TestParamsOneOfFromInputSchema_NeverNil`（含 json-null 边界） |
| A4 | 14 工具 schema 与各自 Execute struct json tag 一致 | ✅ PASS | `TestToolInputSchemas_MatchExecuteContract`（14 子用例全绿） |
| A5 | factory_sandbox_hooks.go 两处 t.Info(ctx) 不被破坏 | ✅ PASS | Info() 签名不变、err 恒 nil；full agent pkg 测试全绿 |
| A6 | task lint 通过；go test ./internal/numind/biz/agent/... 全绿 | ✅ PASS | 见下 |

## 关键验证路径（S3 Task 5 的 a/b/c）

- **a) 全包测试全绿**：`go test ./internal/numind/biz/agent/... -count=1` →
  agent + attachment + bashvalidator + budgetgate + callctx + compliancegate +
  memory/agentmd + search + skills + stream 全部 `ok`。
- **b) 弱模型路径专项（schema 真正下发的可测代理）**：`TestAdapterInfo_PopulatesSchema`
  通过 `ParamsOneOf.ToJSONSchema()`（eino 在构造 model 请求时调用的同一路径）断言关键工具
  schema 含预期 properties + required——证明 schema 离开 adapter 进入 model 请求构造路径，
  而非停留在 adapter 内部。任意模型（含弱 fallback）由此获得结构化参数契约。
- **c) `task lint` 通过**（go vet + golangci-lint，exit 0）。

## 额外验证
- 21 个内嵌 JSON Schema 字面量全部通过 `json.loads` 解析（脚本校验）。
- memory_write/read 的 `kind` enum 与 `memory.MemoryKind.Valid()` 的 5 个合法值
  （learning/decision/issue/fact/preference）完全一致。
- annotate_image `regions` 的 `maxItems:10` 与 Execute 的 `annotateImageMaxRegions=10`
  截断阈值一致。

## 双 reviewer 结论
- Spec-compliance reviewer：**PASS**（round 2）
- Code-quality reviewer：round 1 **FAIL（1 P1 + P2s）** → 全部修复 → round 2 **PASS**
- P1（web_search max_results schema-optional 但 Execute 拒 <1）已修：schema 标 required +
  Description 去除误导性 "default 5"，与既有锁定测试 `TestWebSearchTool_MaxResultsOutOfRange`
  一致，零行为变更。
- 所有 P2（godoc 错位、minimum:0、enum、maxItems、测试命名/缺口）已在 round-2 commit 修复。

## 跨 feature 复查（merge 前）
并行 feature `agent-mode-open-tools-skill-as-guidance` 截至本记录仅在其分支推进，未向 develop
合并触及 adapter 或本 feature 的 14 个工具文件。ndf-done 前会 `git fetch origin develop` 复查。
