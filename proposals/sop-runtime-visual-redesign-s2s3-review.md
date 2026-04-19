# S2/S3 Gate Review Report

**Reviewer**: Sonnet Subagent (独立 Gate Reviewer)
**Date**: 2026-04-11
**Verdict**: PASS_WITH_FIXES

---

## Summary

整体工件质量较高。S0 requirement 的 6 个硬约束在 spec 中基本得到覆盖，S1 proposal 的决策链条清晰完整，S3 plan 的依赖图和 task 粒度大体合理，S5 验证策略也包含了 Playwright + gstack + curl 三层组合，有诚实声明。

但存在 **3 个 P0 Blocker** 和若干 P1，必须修复后才能进入 S4：

1. **P0-1（Phantom Field）**：`spec §4.4 biz 写入 node.ModelName` 引用了不存在的字段。`model.SopNode` 确实有 `ModelName`（第 34 行），但 **`model.SopChatMsg` 中没有 `model_name` 字段**，而 `RunChatMessageItem` DTO（`pkg/api/numind/v1/sop.go:319-330`）也没有 `model_name` / `duration_ms`。spec 和 plan 的 B5 task 声称"RunChatMessageItem 已有 ModelName 则跳过"，但实际上**两个字段在 DTO 和 model 中都不存在**，B3/B5 的实现要点需要更新以明确这是新增而非"检查后跳过"。
2. **P0-2（Phantom field / SSE done 取 model_name）**：spec 附录 C 发现 SSE `done` 事件不含 `model_name`，前端 onDone 之后必须重新拉 `/status` 或 `/runs/:id/nodes/:nodeId` 才能拿到后端写入的 `model_name`。spec 将此列为"S3 需决定的拉取策略"（附录 C 最后段落），但 **S3 plan 中没有任何 task 处理这条"onDone 之后拉 model_name"逻辑**。它不属于 F1/F2/F6/F11 任何一个 task 的 scope 说明，会导致 MetaFooter 在 state E 永远显示空。
3. **P0-3（Token 字段隐藏，MetaFooter 无法显示 token 用量）**：`model.SopNodeRun` 的 `PromptTokens` / `CompletionTokens` / `TotalTokens` 全部标注 `json:"-"`（第 93-95 行），**前端永远收不到 token 数据**。spec §3.5 的 `SopNodeRun` 接口定义里没有 token 字段，mockup E 态 MetaFooter 展示"3.2K tokens"，但实际后端数据对前端不可见。spec 没有说明如何解决，也没有声明"token 用量不展示"。必须明确处置：要么在 B5 去掉 `json:"-"`（或新增 DTO 字段），要么修改 mockup/spec 不展示 token 用量。

---

## Findings by Severity

### P0 - Blocker (must fix before S4)

- [ ] **P0-1 | SopChatMsg 无 model_name，RunChatMessageItem DTO 无 model_name/duration_ms**
  - **Evidence**: `numind-server/internal/pkg/model/sop.go:156-174` — `SopChatMsg` struct 有 `model_name`（**检查后发现**：后端 audit §5 "消息表：字段 ... model_name" 正确；但 `pkg/api/numind/v1/sop.go:318-330` 的 `RunChatMessageItem` 确实没有 `model_name` 和 `duration_ms` 字段）。
  - **Correction after re-check**: `SopChatMsg` struct 在 `sop.go` 第 156-170 行实际上**没有** `model_name` 字段（只有 `Role, Content, Thinking, Seq, PromptTokens...`），与 backend audit §5 说"消息表字段含 model_name"矛盾。
  - **实际状态**：`SopChatMsg` 无 `model_name` 字段（需要 B3 task 新增），`RunChatMessageItem` DTO 无 `model_name` 和 `duration_ms`（B5 task 需新增）。spec B3/B5 task 描述中说"model_name 在 chat message 表原本就有，重点是 RunChatMessageItem 是否已包含；审查一遍" —— 这段描述**误导 implementer**，会导致 B3 漏掉 SopChatMsg.ModelName 的新增，进而 B5 也漏掉 DTO 映射。
  - **Fix**: B4/B5 task description 必须明确：`SopChatMsg` 需新增 `ModelName string` 字段（与 `DurationMs` 一起加）；`RunChatMessageItem` 需新增 `ModelName string` + `DurationMs int64` 两个字段。

- [ ] **P0-2 | onDone 后 model_name 拉取策略未在 plan 任何 task 中承接**
  - **Evidence**: spec 附录 C 最后一段："SSE done 事件当前 payload 不含 model_name。前端 onDone 后需重新从 /status 或 /runs/:id/nodes/:nodeId 拉一次 node run，才能拿到后端新写入的 model_name。**S3 需决定此拉取策略是否算本 feature 范围**。"
  - S3 plan 中 F6 task scope 说"MetaFooter 缺字段整段不渲染"，F11 验收标准说"E 态 MetaFooter 有 model_name + latency"，但没有任何 task 明确"onDone 触发 GET /runs/:id/status 或等价接口以填充 model_name"。
  - **影响**：F11 验收标准 state E MetaFooter 无法通过，S5 P1 路径失败。
  - **Fix**: 在 F11（或 F6）task 的实现要点中明确：`onDone` 回调触发后，调用 `fetchRunStatusDetail(runId)` 或新建 API 函数拉取最新 node run（含 model_name），再调用 `store.setNodeRun`。或在 spec 中明确声明 model_name 在 onDone 时从后端 SSE 的 done payload 扩展（需要 Go 侧改 done 事件 payload）。两个方案都可以，但必须在 plan 里有明确 task 承接。

- [ ] **P0-3 | SopNodeRun token 字段全部 json:"-"，MetaFooter token 展示不可行**
  - **Evidence**: `numind-server/internal/pkg/model/sop.go:93-97` — `PromptTokens`, `CompletionTokens`, `TotalTokens`, `ReasoningTokens`, `EstimatedPromptTokens` 全部 `json:"-"`，前端收不到任何 token 数据。
  - spec §3.5 `SopNodeRun` TypeScript interface 没有 token 字段。mockup E 态 footer 显示"3.2K tokens"。spec §3.2 state E MetaFooter 描述"完成时间 · 模型 · 耗时 · token 用量"。
  - **影响**：如果不解决，token 用量展示在前端永远是空，MetaFooter 的"token"段不渲染，与 mockup 不符。
  - **Fix 选项 A（推荐）**: B5 task 增加：在 B 端字段层面，`/runs/:id/status` 的 `CompletedNodeInfo` DTO 或新 `/runs/:id/nodes/:nodeId` 接口将 `total_tokens` 透出（加到 DTO，不去掉 model 层的 `json:"-"`，避免影响其他接口）。前端 `SopNodeRun` type 加 `total_tokens` 字段，MetaFooter 读取。
  - **Fix 选项 B（最小改动）**: spec/mockup 声明"state E MetaFooter 不展示 token 用量（原始 token 数据受 json:- 保护）"，MetaFooter 仅展示"耗时 · 模型 · 完成时间"。需同步更新 MetaFooter props spec。
  - 无论选哪个方案，**必须在 spec/plan 中明确，不能留白**。

### P1 - Important (should fix)

- [ ] **P1-1 | spec §4.4 line 引用不准确**
  - **Evidence**: spec §4.4 "节点执行成功路径（~line 676-700）"。实际代码 `sop.go` 成功路径在约第 676-700 行附近（`updateData := map[string]interface{}{"status": SopStatusSucceeded, ...}` 在第 676 行），line 引用基本正确，但失败路径在第 656-675 行。spec 说"失败路径同样 copy 一份"，而失败路径的 updateData（第 656-662 行）确实没有 `model_name`。Plan B4 task 说"成功 + 失败两处"，这是正确的。但 spec 里写"~line 676-700"容易让 implementer 只看成功路径而漏掉失败路径（第 656-662 行）。**建议**: plan B4 task 明确列出失败路径的具体位置（约 line 656-662）。

- [ ] **P1-2 | Cross-Repo Gate curl 命令 URL 硬编码且依赖"先有数据"**
  - **Evidence**: plan §3.3 step 4 的 curl 命令 `https://49.233.219.254:9091/v1/sop/runs/1/chat-messages`，run ID 硬编码为 1。如果 dev 环境没有 ID=1 的 run 或该 run 没有 chat messages，字段验证就是假过。
  - **Fix**: curl 命令改为先登录拿 token，再触发一次 SOP 执行（或使用已知有 chat messages 的 runId），或改为 `... | jq 'has("duration_ms")'` 而不是依赖具体数据值。

- [ ] **P1-3 | spec §5.4 状态 A/C 时 ⭐ 按钮隐藏的逻辑与 §3.2 状态机轻微矛盾**
  - **Evidence**: spec §5.4 "状态 A/C 尚未有 output 时 ⭐ button 隐藏"。但 spec §3.2 state A 描述"⭐ 收藏 隐藏（没 input 内容时不显示）"——隐藏条件是"没有 input 内容"，不是"没有 output"。State A 可以有 output（如果该节点之前已执行过，用户通过 setViewingStep 切回来）。实际上 state A（active 未执行非首次）根据 §3.2 表格 output 是"OutputEmpty 或上一步结果摘要"——没有本节点的 output，⭐ 应隐藏。但 spec §5.4 用"没有 output"为条件更准确，两处表述应对齐。
  - **Fix**: 统一以"本节点 nodeRun 不存在或为空"作为隐藏条件，在组件 props 里用 `hasOutput: boolean` 控制。

- [ ] **P1-4 | plan F4 task 用 stub/placeholder 会导致 F4 commit 后系统 "不可运行"**
  - **Evidence**: plan F4 task 说"先搭骨架并用 stub/占位组件让系统可编译，实际子组件在 F5-F10 填肉"。NDF Rule 9 要求"完成后系统可编译可运行（不处于半成品状态）"。F4 完成后 `StepCanvas.vue` 对子组件用 placeholder div，用户打开页面看到空白 — 这是"可编译"但不是真正"可运行"的状态。
  - **Fix**: F4 应包含最小可运行的"step header + 纯文字 placeholder（'内容加载中...'）"，确保页面结构可见。或重新说明 F4 的 Done 标准是"组件树可编译、路由器逻辑正确、渲染非空页面骨架"（而不是完整功能）。

- [ ] **P1-5 | F11 依赖列表说"几乎全部前置"但实际验收依赖 B5 部署，而 gate 条件描述不够严格**
  - **Evidence**: plan F11 task 说"跨仓库依赖：B5 已 merge develop + dev deploy（见 Cross-Repo Gate）"，但 plan §3.2 同时说"F0/F1/F2/F3/F4/F5/F7/F8/F9/F12 可在 gate 之前并行开始"。F11 的验收标准"E 态 MetaFooter 有 model_name + latency"明确依赖 dev API。如果 implementer 在 gate 未通过时就 commit F11，验收会假过（MetaFooter 不渲染但系统看起来正常）。
  - **Fix**: F11 task 验收标准中明确标注 `[需要 cross-repo gate 已通过]`，并要求 implementer 先 curl dev 验证字段再提交最终 commit。

### P2 - Minor (nice to fix)

- [ ] **P2-1 | spec 附录 C checklist 中 `StatusCompletedNodeInfo.model_name` 问题未决**
  - spec 附录 C："评估 `StatusCompletedNodeInfo` 是否也需要 `model_name`"。`StatusCompletedNodeInfo`（`src/api/sop.ts:330-341`）当前没有 `model_name` 和 `latency_ms` 字段。state B（查看历史步骤）的 MetaFooter 依赖 `nodeRuns[nodeId]` 中的字段。如果用户打开 URL with runId（不是 SSE onDone 触发），则 node run 的 `model_name` 需要通过 `/status` 或 `/runs/:id/nodes/:nodeId` 拿到。spec 把这个问题标为"评估"但没有给出结论，会导致 implementer 自行决策。
  - **建议**: 在 spec 或 plan F2 task 中明确 `StatusCompletedNodeInfo` 是否补字段，避免 F11 实施时临时决策。

- [ ] **P2-2 | spec §5.5 "重新生成 ConfirmModal" 与 proposal D6 "不弹确认" 的矛盾有注释但 spec 已决策**
  - spec §5.5 注释："proposal §3.2 D6 说不弹确认对话框，但 ui-ux.md 硬规则 4 明确销毁性操作必须弹。此处 spec **遵循硬规则**，S3 gate reviewer 若认为不需要可调整，但默认弹。"
  - reviewer 确认：遵循 ui-ux.md 硬规则 4（销毁性操作必须弹 ConfirmModal）是正确决策。Q3 已经标注为"已决定"。此条仅提醒 plan §5.2 的 "Open Question Q3" 应该在 plan 文件中明确标为"已关闭：弹 ConfirmModal"，避免 implementer 看到 Q3 未关闭而困惑。

- [ ] **P2-3 | spec §5.1 token 清单与 DESIGN.md 对齐工作未安排在 plan 任何 task 中**
  - spec §5.1 结尾："S4 实施时需要确认这些值在根目录 DESIGN.md 中是否有等价 token；若有则注释引用，若无则 scope 内硬编码并在 S3 plan 中排一个'DESIGN.md 同步'task 推荐但不强制。"
  - plan 中没有这个 task。F0 task 说"提取 spec §5.1 清单（~25 个 token）"但没有要求验证 DESIGN.md 对齐。
  - **建议**: 在 F0 task 验收标准中加一条"对照 DESIGN.md 注释每个 token 是否有全局等价变量（不强制替换，记录即可）"。

- [ ] **P2-4 | F12 task 与 F3/F6 task 的测试覆盖重复且职责边界模糊**
  - F3 task 已包含"StepNav.spec.ts 新建"，F6 task 已包含"OutputCard.spec.ts + MetaFooter.spec.ts 新建"。F12 task 又列了"若 F3/F6 task 未完成则在此补充"。这种模糊表达会导致 implementer 不知道"这个 task 到底要做什么"，可能 F12 变成空 task 或重复工作。
  - **建议**: F12 scope 明确为"snapshot 更新（StepInput / StepOutput）+ sopRun.spec.ts 补 viewing state 用例（如果 F1 未覆盖完）+ 删除 StepperPanel.spec.ts"，移除"若 F3/F6 未完成"的兜底描述。

---

## Dimension Reviews

### Dim 1: spec ↔ requirement/proposal/mockup 一致性
**结论：PASS_WITH_FIXES**

6 个硬约束（不展示 prompt / 执行后不可改 / 重新生成语义 / 长内容承载 / 不 localStorage / 设计语言简洁）在 spec 中均有对应覆盖：
- 不展示 prompt：spec §1.2 out of scope 明确，SopNodePublicDTO 隐藏敏感字段（backend audit §1 确认）
- 执行后不可改：spec §3.2 state D/E 明确 InputCard hidden；§5.5 HistoryViewStrip 约束
- 重新生成语义：spec §5.5 明确"抹除旧 output + 同 input 重跑 + ConfirmModal"
- 长内容承载：spec §7 R1 有 max-height overflow-y: auto 方案；mockup 本身也是全高设计
- 不 localStorage：spec §3.3 store 改造章节无 localStorage 引用；proposal D9 明确"后端持久化"
- 设计语言简洁：spec §5.1 token 全白、去衬线、去 hero 全部对应

6 个状态 A-F 在 spec §3.2 状态机表格中均有覆盖，与 mockup HTML 中的 CSS class / state 块对应关系清晰。

**遗漏点**：requirement §7.1 骨架图中"chat (F)"条目在左 nav 下方独立一组，spec §3.1 StepNav.vue props 有 `trailingChatEnabled` 但组件级描述中追问分组的分隔线样式未明确（仅说"两个分组"）。这不是 P0，但 mockup 01 的 `.step--chat` 样式应当在 F3 task 中体现。

### Dim 2: spec ↔ 后端 audit 一致性
**结论：FAIL（因 P0-1、P0-3）**

**P0-1（已描述）**：`SopChatMsg` 无 `model_name`，`RunChatMessageItem` DTO 无 `model_name` 和 `duration_ms`。Backend audit §5 说"消息表字段含 model_name"——这与代码不符，audit 可能是基于期望字段描述的，不是对现有代码的准确描述。

**P0-3（已描述）**：`SopNodeRun` token 字段 `json:"-"` 使前端无法读取 token 数据，而 MetaFooter 需要展示。

**正确点**：
- `SopNode.ModelName` 存在（sop.go:34），B4 task 读取 `node.ModelName` 是有效的
- `POST /v1/sop/bookmarks` 端点存在（router.go:158）
- `DELETE /v1/sop/bookmarks/:id` 端点存在（router.go:160）
- `auto_apply_bookmarks` 由 `CreateRunWithBookmarks` 支持（sop.go:82/1652）；前端 `createRun` 传参后需要路由到 `CreateRunWithBookmarks` 而非 `CreateRun` —— 经过检查 biz 接口有两个方法，controller 实现细节需要确认（轻微不确定性，不升 P0）
- `SopNodeRun.LatencyMs` 存在（sop.go:87）

### Dim 3: plan ↔ spec 覆盖度
**结论：PASS_WITH_FIXES（P0-2）**

spec 中几乎所有组件和改动都在 plan 中有对应 task。唯一缺口：

**P0-2（已描述）**：spec 附录 C 标注的"onDone 拉 model_name 策略"没有任何 task 承接。

其余覆盖情况：
- spec §4.1-4.5 后端改动 → B1/B2/B3/B4/B5 对应
- spec §3.1 15个组件 → F3/F4/F5/F6/F7/F10/F11 均有对应
- spec §3.3 store 改造 → F1 对应
- spec §3.4 API 改造 → F2 对应
- spec §6.2 E2E → F13 对应
- spec §6.3 视觉回归 → F14（但 plan 的 task 编号到 V1，没有独立的 F14 task——视觉回归在 V1 的 S5 策略里，这是合理的合并）

### Dim 4: plan task 原子性
**结论：PASS_WITH_FIXES（P1-4）**

逐个 task 评估：
- B1/B2：各 1 SQL 文件，完全原子 ✅
- B3：2 个 struct 各加 1 行，原子 ✅
- B4：~15 行 Go，2 个写入点，原子（两个文件改动可一个 commit）✅
- B5：2 个文件，DTO 映射，原子 ✅
- F0：纯 CSS 变量定义，无 UI 结构，完全原子 ✅
- F1：store 改造 + 测试，改动集中在一个文件，原子 ✅
- F2：2 个文件的 API/type 改动，原子 ✅
- F3：3 个新组件 + 1 个测试文件，约 600 行，稍大但可原子完成（3 个组件高度内聚）✅
- F4：2 个新组件（StepCanvas + SopStepView），stub 骨架，原子；**P1-4** 的"半成品"问题已提
- F5：InputCard + StepInput 改造，1 新 + 1 改，原子 ✅
- F6：3 个新组件 + 1 改 + 2 测试，约 700 行；组件内聚，可接受 ✅
- F7：2 个新组件，轻量，原子 ✅
- F8：2 个改动（OutputCard + SopStepView 接线），原子 ✅
- F9：2 个改动，停止生成逻辑，轻量原子 ✅
- F10：3 个新 + 1 改，~400 行，原子 ✅
- F11：大改（主容器重写），是集成 task，spec 说"focus 只做接线"。独立验收标准清晰（6 态可达），原子性可接受 ✅
- F12：**P2-4** 的模糊问题；核心逻辑原子
- F13：selector 迁移 + 3 条 E2E，可单独验收 ✅
- V1：文档 task，完全原子 ✅

### Dim 5: 跨仓库 gate 现实性
**结论：PASS**

plan §3 明确写出了 gate 步骤，包括：1）本地 merge，2）push CI，3）SSH 执行 migration，4）curl 验证，5）gate 通过条件。**P1-2** 的 curl 命令 hardcoded run ID 问题建议修复。

gate 的"宽松"策略（F0/F1/F2/F3/F4/F5/F7 可在 gate 前并行）与"F6/F10/F11 验收必须等 gate"的划分是合理的。**P1-5** 的 F11 验收不够明确，但总体 gate 设计是正确的。

### Dim 6: S5 验证策略合理性
**结论：PASS**

- V1 task 独立存在（plan §2 最后一个 task）✅
- 三层验证（Playwright + gstack /qa + curl）组合合理 ✅
- 关键用户路径 P1-P7 覆盖 6 个状态（P6）+ bookmark（P4）+ 重新生成（P3）+ 停止生成（P5）+ MetaFooter 字段（P7）✅
- 回归保护诚实声明存在（plan §4.3）✅：明确声明 gstack /qa 是一次性验证，Playwright E2E 是持久化测试
- 高风险业务逻辑（bookmark 持久化、重新生成覆盖写入）使用 Playwright ✅

**唯一问题**：P2（视觉重设计）的 Playwright 路径也覆盖了 bookmark/重新生成等业务行为，视觉部分用 gstack 是合理的分工。没有偷懒用 gstack 跳过关键业务 E2E 的情况。

### Dim 7: 依赖图正确性
**结论：PASS**

依赖图无循环依赖，关键路径清晰：B1+B2 → B3 → B4 → B5 → [gate] → F11。前端 F0/F1/F2 可并行，F5/F6/F7 可并行，F3 依赖 F0+F1（正确，需要 store viewingStepStatus）。F11 依赖几乎全部 F task（正确，是集成 task）。

### Dim 8: 风险与缓解
**结论：PASS**

spec §7 的 10 条风险全部在 plan §6 中有对应缓解。plan 还新增了 R11（cross-repo gate 前端先开工）和 R12（F11 大改集成 bug），覆盖完整。**P0-3**（token 字段 json:-）是 spec/plan 未列入 risk 的新发现。

### Dim 9: 与 CLAUDE.md / NDF rules 合规
**结论：PASS**

- `ConfirmModal` 应用：spec §5.4 删书签用 ConfirmModal ✅；spec §5.5 重新生成用 ConfirmModal ✅（已决策按硬规则 4 弹）
- lint / type-check：每个 frontend task 验收标准都包含 `npm run lint && npm run type-check` ✅
- Go task 包含 `task lint` ✅
- Conventional Commits：plan §0 元信息中 branches 命名规范，但 commit message 格式没有在 plan 中明确引用（CLAUDE.md §3 要求）。plan §5.3 说"NDF Rule 8 commit 验证"但没有重申格式要求。轻微 P2，不升级。
- 外部 UI 框架：spec §附录 D 硬性检查项 12 条中有检查 ✅

### Dim 10: 可执行性
**结论：PASS（修复 P1-4 后）**

大多数 task 描述清晰，新 implementer 可 1 hour 内完成：
- B1/B2：10 分钟 ✅
- F0：30 分钟 ✅
- F1：90 分钟（含单测）✅
- F7：45 分钟 ✅
- F11：最复杂，但 spec 附录 A 提供了组件交互矩阵，可按矩阵接线。预计 3-4 小时，不超界 ✅

唯一可能超时的是 F13（E2E），因为 Playwright 调试耗时不可预测，但这是测试 task 的正常成本。

---

## Verdict Reasoning

给出 PASS_WITH_FIXES 而非 FAIL 的理由：
1. P0 问题有明确的修复路径（文字修正，不需要重设计）
2. spec 整体质量高，6 个约束、6 个状态、组件树、依赖图全部清晰
3. S5 验证策略诚实完整
4. P0 问题不影响架构，只影响具体实现的几处代码

给出 PASS_WITH_FIXES 而非 PASS 的理由：
1. P0-2 是真正的遗漏：如果 implementer 按现有 plan 实施，state E 的 MetaFooter 会永远是空，S5 P1 路径无法通过
2. P0-3 需要明确决策（展示还是不展示 token），不能让 implementer 自行猜测
3. P0-1 会导致 chat message 端的 model_name 丢失（B3 task 漏加字段）

---

## Recommended Actions

按优先级排列，implementer 应在进入 S4 之前完成所有 P0 修复：

**Step 1（修复 P0-1）**：
- 更新 spec §4.3 GORM Model 改动部分：明确 `SopChatMsg` 需要新增 `ModelName string` 字段（不仅是 `DurationMs`）
- 更新 plan B3 task 实现要点：加上 `ModelName string \`gorm:"size:100;default:''" json:"model_name"\`` 到 `SopChatMsg` struct
- 更新 plan B5 task 实现要点：将"model_name 在 chat message 表原本就有，重点是 RunChatMessageItem 是否已包含；审查一遍"改为"RunChatMessageItem 需新增 `ModelName string \`json:"model_name"\`` 和 `DurationMs int64 \`json:"duration_ms"\`` 两个字段"

**Step 2（修复 P0-2）**：
- 在 spec 附录 C 中明确决策："onDone 触发后前端调用 `fetchRunStatusDetail(runId)`，从返回的 `completed_nodes` 中找到当前节点的完整 info（含 model_name），更新 `store.nodeRuns[nodeId]`"（或选择方案 2：扩展 SSE done payload 含 model_name）
- 在 plan F11 task（或新建独立的 F2.5 task）中明确"onDone 回调的 model_name 刷新逻辑"

**Step 3（修复 P0-3）**：
- 明确决策：是否展示 token 用量。
  - 如展示：在 plan B5 task 中新增"在 `StatusCompletedNodeInfo` DTO 或新接口中透出 `total_tokens` 字段（不修改 model.SopNodeRun 的 json tag）"；更新 spec §3.5 `SopNodeRun` TypeScript interface 加 `total_tokens?: number`
  - 如不展示：在 spec §3.2 state E/B 的 MetaFooter 描述中删除"token 用量"，更新 MetaFooter props spec，并在 mockup 中标注"token 字段暂不展示"

**Step 4（修复 P1-1）**：
- 在 plan B4 task 中明确标注失败路径改动位置（~line 656-662，在 `if err != nil` 分支的 updateData 里补 `"model_name": node.ModelName`）

**Step 5（修复 P1-2）**：
- 更新 plan §3.3 step 4 的 curl 命令：先触发一次 SOP 节点执行产生新 node_run，再 curl 验证；或将 curl 改为验证字段是否存在于 schema 而非依赖具体数据

**Step 6（可选，P1-3/P1-4/P2-x）**：
- 统一 spec §5.4 和 §3.2 中 ⭐ 隐藏条件的表述（用"本节点 nodeRun 不存在"）
- 更新 plan F4 task 验收标准，明确 stub 的可见性要求
- 关闭 plan §8 中 Q3（重新生成弹 ConfirmModal 已决策），标为 CLOSED
- 明确 F12 task scope，去除模糊的兜底描述

---

*本报告由独立 Sonnet Reviewer 于 2026-04-11 产出，作为 S3 Gate 的 NDF Rule 10 要求的独立审查文件。审查中反查了 `sop.go`、`model/sop.go`、`router.go`、`sop.ts`、`sopRun.ts`、`pkg/api/numind/v1/sop.go` 等关键代码文件。*
