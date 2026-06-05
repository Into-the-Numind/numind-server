# SOP 上下文压缩串步修复 — 设计 (S1+S2)

> Feature: `sop-context-ordering-fix` · Track: standard · 2026-06-05
> 关联：`requirements/sop-context-ordering-fix.md`

## 1. 问题定位（已逐行核实，行号 2026-06-05 复核）

调用链：`sop.ExecuteNodeStream` → `executor.executeViaGateway` → `buildSOPGatewayFragments`（`sop_fragments.go:133`）→ `aiservice.ChatStream` → `ContextBudgetCredits` 中间件 → `contextbudget.Biz.Prepare` → 压缩 → `aiservice.RenderContextFragments`。

三处缺陷叠加：

| # | 文件:函数 | 缺陷 |
|---|-----------|------|
| 1 | `biz/contextbudget/biz.go` `applyPlan` (~945) | `result = append(result, *summaryFrag)` 把摘要追加到末尾，排在当前输入之后 |
| 2 | `pkg/aiservice/context_renderer.go` `RenderContextFragments` (~22) + `biz.go` `Prepare`(~270)/`prepareWithoutCompression`(~367) | 渲染前不按 `Order` 排序，照 slice 顺序渲染 |
| 3 | `biz/contextbudget/producers.go` `NewImmutableSystemFragment`(~21)/`NewCriticalUserFragment`(~39) | 不设 `Order`（默认 0）；SOP 当前输入因此 Order=0，比历史还靠前 |

## 2. 各 producer 现状（决定修复面）

| Producer / 调用方 | system Order | 历史 Order | 当前输入 Order | 是否已正确 |
|---|---|---|---|---|
| `NewDurable*Fragment` | — | `order`（已设）| — | ✓ |
| `salesrag` 自建 fragment | 0 | evidence 100+ | 1000 | ✓（不回归）|
| `chatbot` `BuildChatContextFragments` | 0（producer 默认）| `order` | `order`（内联设, =max）| Order 值正确，但仍受 #1+#2 命中 |
| `sop` `buildSOPGatewayFragments`（**生产路径**）| 0（producer 默认）| `i` | **0（缺陷）** | ✗ |
| `sop` `buildSOPNodeFragments`/`buildSOPChatFragments`（test-only scaffold）| 0 | `order` | **0（缺陷）** | ✗ |

血缘范围确认（grep 全仓库）：
- `NewImmutableSystemFragment` 调用方：`chatbot/stream.go`、`sop_fragments.go`（5 处）。
- `NewCriticalUserFragment` 调用方：`sop_fragments.go`（3 处）。
- **无任何 `_test.go` 调用这两个 producer** → 改签名只需更新 2 个生产文件。
- `RenderContextFragments` 生产调用方仅 2 处（`Prepare`、`prepareWithoutCompression`）。
- `PrepareResult.Fragments` 唯一消费者 `middleware/context_budget.go:538` 只对 fragment 计数（order-independent）→ 原地排序安全。

## 3. 修复设计（三层，belt-and-suspenders）

### Fix A — producer 强制设 Order（结构性根治）
把两个 producer 改为接受 `order int` 并写入 `Order` 字段，使其与 `NewDurable*Fragment` 对称——Order 成为每个调用点必须显式给出的决策，永久消除"忘了设 Order"这类 bug。

调用点更新：
- `chatbot/stream.go`：`NewImmutableSystemFragment("sys-0", systemPrompt, 0)`。
- `buildSOPGatewayFragments`：system 与当前输入都传 `i`（消息索引）→ 全 fragment `Order=i`，与原消息顺序一致；当前输入 = `lastUserIdx` = 最大 index → 排最后。
- `buildSOPNodeFragments`/`buildSOPChatFragments`：用递增 `order` 计数，当前输入传末值（max）。

### Fix B — applyPlan 摘要落原位
摘要 fragment 的 `Order` 取**被它替换掉的 summarize 候选的最小 Order**（`min(candidate.Order)`），使其落在历史原位，而非末尾。

### Fix C — 渲染前稳定排序（最终保险）
`Prepare` 与 `prepareWithoutCompression` 在调 `RenderContextFragments` 前对 fragments 做 `sort.SliceStable(byOrder)`。稳定排序保证：① 同 Order 保持输入相对序（向后兼容所有未设 Order=全 0 的调用方，排序退化为恒等）；② 与 Fix A/B 配合，当前输入（最大 Order）必在最后、摘要（min 历史 Order）落历史中间。

> `RenderContextFragments` 自身**不改**——保持其"调用方负责预排序"的文档契约，排序责任放在 contextbudget biz 层（符合现有注释）。

## 4. 为什么三层都要

- 只做 C：SOP 当前输入 Order=0，排序后反而和 system 并列最前 → 更糟。**A 必需**。
- 只做 A：applyPlan 仍把摘要追加末尾、渲染不排序 → chatbot/SOP 摘要仍在当前输入之后。**B+C 必需**。
- 只做 A+B：渲染照 slice 序，applyPlan 追加的摘要（即便 Order 对）仍物理在末尾 → 仍错。**C 必需**。

## 5. 备选方案（否决）

- **在 `RenderContextFragments` 内部排序**：最省（单点全覆盖），但破坏其文档契约、让 `pkg/aiservice` 渲染器隐式依赖 `Order` 语义；且仍需 Fix A 给 SOP 当前输入正确 Order。否决——排序责任留在 biz 层更内聚。
- **producer 不改签名、调用点构造后补设 `.Order`**：可行但易漏，bug 会复发。否决——改签名强制每个调用点表态。

## 6. 不变量 / 安全网

- 默认无 Order（全 0）的任意现有/未来调用方：稳定排序 = 恒等，零行为变更。
- salesrag：Order 已 0/100+/1000，排序恒等，不回归。
- `result.Fragments` 消费者 order-independent，原地排序安全。

## 决策记录
- **S2-D1**：选 Fix A（改 producer 签名）而非调用点补设——结构性根治，强制表态，且无 test 调用方故 blast radius 小。
- **S2-D2**：排序放 contextbudget biz 层（Prepare/prepareWithoutCompression）而非 RenderContextFragments 内部——保 renderer 文档契约。
- **S2-D3**：摘要 Order = min(被替换候选 Order)，落历史原位。
- **S2-D4**：用 `sort.SliceStable`（非 `sort.Slice`）——同 Order 保持输入序，向后兼容 + attachment 跟随父 user fragment。
