# SOP 多步执行串步修复（AI Gateway 上下文压缩时 fragment 排序错位）

## 来源
- 提出人：客户上报（线上 bug）
- 提出日期：2026-06-05
- 严重度：高（影响付费用户核心功能 SOP 多步执行的正确性）

## 现象（客户描述）

运行 AI SOP（多步工作流）时，**明明在执行第 3 步，AI 却生成了第 2 步应该产出的内容**（错步骤 / 串步）。前两步正常，偏偏到第 3 步才出错。

## 根因（已逐行核实）

这是 **AI Gateway（ContextBudgetCredits 中间件）上下文压缩时的 fragment 排序错位**，仅在两条件同时满足时触发：

1. 走 Gateway 路径（`modelKey != ""`，生产环境几乎总是如此）；
2. 上下文累积到**超出预算、触发压缩**（解释了"为何偏偏第 3 步"——前两步上下文短、不压缩，顺序正常）。

坏在三处叠加：

1. `internal/numind/biz/contextbudget/biz.go` `applyPlan`：压缩生成的"历史摘要"fragment 用 `result = append(result, *summaryFrag)` **追加到列表末尾**，排在"当前这一步用户输入"fragment 之后。
2. `internal/pkg/aiservice/context_renderer.go` `RenderContextFragments`：**不按 `Order` 排序**，直接照 slice 顺序渲染（函数注释明确"调用方负责预排序"），而调用方 `Prepare` / `prepareWithoutCompression` 在调用前**没有按 Order 排序**。
3. `internal/numind/biz/contextbudget/producers.go` `NewImmutableSystemFragment` / `NewCriticalUserFragment` **不设 `Order` 字段**（默认 0），而 SOP 的 `buildSOPGatewayFragments` 给历史项设 `Order=i`、给当前输入调 `NewCriticalUserFragment` 时没传 Order → 当前输入 Order=0，比历史还小，靠排序也兜不住。

后果：压缩后发给 LLM 的消息顺序变成「系统提示 + 当前第3步指令 + 第1&2步摘要」——摘要成了最后一条消息，LLM 据此续写 → 产出第 2 步的内容。

## 影响面

`contextbudget` 中间件是**共享基础设施**：SOP、chatbot、salesrag 都走它。

- **SOP**：受影响（producer Order=0 缺陷 + applyPlan 追加缺陷 双重命中）。
- **chatbot**（`BuildChatContextFragments`）：受影响——其 current input Order 设值正确，但历史 durable fragment 可压缩，applyPlan 追加摘要到末尾 + 无排序 → 摘要排到 current input 之后。
- **salesrag**（`buildSalesRAG*Fragment`）：已正确设 Order（system=0 / evidence=100+ / user=1000），且 fragment 全 CompressNone/CompressReference 无 summarize 候选 → 当前未命中；修复后仍正确（不回归）。

## 业务目标

1. 压缩触发后，发给 LLM 的消息顺序必须保持逻辑顺序：**系统提示 → 历史（含摘要，落在被替换历史的原位）→ 当前步骤指令（永远最后）**。
2. 修复在共享 `contextbudget` 层，SOP / chatbot 同时受益；salesrag 不回归。
3. 客户报告的 bug：第一个 commit 必须是失败的复现测试（NDF 规则 11），修复后转 PASS，永久留库做回归保护。

## 验收标准

- 复现测试 FAIL → PASS。
- `go test ./...` 全绿，`task lint` 通过。
- 构造一个触发压缩的多步场景，渲染消息顺序正确（当前指令在最后、摘要在中间）。
- 不破坏 chatbot / salesrag（同中间件）。

## 档位判定（Triage）

- Micro 边界：否（改的是核心业务排序逻辑 + 跨子系统）。
- Hotfix vs Standard 5 条：① 不涉及 DB schema ✓ ② 不涉及新 API 端点 ✓ ③ 不涉及新外部服务 ✓ ④ 影响文件数 ≤3 **✗（producers.go / biz.go / context_renderer 注释 / sop_fragments.go / chatbot/stream.go + 测试 ≥4 代码文件）** ⑤ 不涉及高风险业务逻辑 **✗（计费相邻的共享 context 中间件，跨 SOP/chatbot/salesrag）** → **Standard**。
