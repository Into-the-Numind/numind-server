# Post-Context-Budget Known Issues & Repository State

> **Generated:** 2026-04-27 after S4 完成 of feature `context-budget-compression`
> **Purpose:** 集中记录所有 S4 期间发现但未解决的问题（含与本 feature 无关的 pre-existing
> bugs、其他 session 留下的工件、git stash 残留、副作用变更），以及推迟的 P2 项。
> 下次任何人接手 develop 分支前应阅读本文档，避免重复发现这些已知项目。
>
> **维护方式：** 每个条目修复后从本文档移除，并在最下方"已关闭项"区记录关闭时间 + commit。
> 当文档为空时可整体删除。

---

## §1 本 feature 主动推迟的 P2（按 Task 分组）

所有 P0/P1 已修复并落 develop。下列 P2 在 review 阶段判定"修复成本 > 当前价值"或"依赖外部条件未就绪"被推迟，已记录在 `build-manifest.yaml` 对应 wave 的 decisions 行。

### Task 4 — credit budget API
- **OpChatbotChat 的 flat estimate 值** 暂用 6（与 sop_chat 对齐）。S5 收集真实 chatbot token 数据后调整。

### Task 6 — ContextBudgetCredits middleware
- **Spec §5.1 step 1 empty fragments + migrated operation 行为**：spec 原文要求"已迁移操作 + 空 fragments → 写 `context_budget_event.status='skipped'`"，当前实现统一 passthrough（不区分 migrated vs non-migrated）。Task 9/10 完成后所有 migrated path 都会发非空 fragments，此分支不可达；保持现状。
- **Nit-3 `EstimatedCredits` 单位语义混乱**：Task 7 修复了上层（middleware 通过 `CheckAndEstimateBudget` 计算真实 credits），但 `EstimatedCompletionTokens = ReservedOutputTokens` 的字段冗余在 Task 12 加了 `TODO(S7)` 注释。S7 拿到真实 completion token 估算时再解耦。

### Task 7 — Prepare/Finalize biz
- **Token profile lookup 仅 3 级（exact + provider fallback + global fallback）**：spec §3.2 line 329 还要求"family-level fallback `(provider, model_family)`"，目前缺失。原因：admin 配置时未要求显式提供 `model_family` 字段，无法可靠派生。Task 11 admin API 接受 `model_family` 字段，但 UI（Task 13 ContextBudget.vue）的 token profile 表单可能未把这个字段暴露给运营。**待 admin 显式提供 `model_family` 后实现 4 级 lookup。**
- **`successCompressor` 测试**：已有 `TestPrepare_CompressorIsCalledWhenSummarizeAction` 使用，但还可补一个测 compressor 失败时 fragment 退化为 drop 的 case。低优先级。

### Task 9 — SOP producer migration
- **P2-2 reasoning fragments → RoleWorking 映射**：spec §9.1 列了"historical reasoning → working + assistant + reasoning + drop|summarize"，但当前 SOP 数据流不区分"最终输出"和"思考过程"——`conversationHistory` 只追加 `nodeRun.Output`。要做这个映射必须改 SOP 数据流（独立保存 reasoning content），超出本 feature scope。已在 `sop_fragments.go` package comment 加 TODO 指向 spec §9.1。

### Task 10 — chatbot/SalesRAG producer
- **N1 ChatStream 历史消息 double-fetch**：`buildChatMessages`（旧 prompt 路径）和 fragment builder 各调一次 `ListMessages`，每次请求多 ~50ms DB 查询。结果一致，仅性能 nit。建议在后续 SalesRAG/chatbot prompt 拼装重构 sprint 修复。
- **N3 `NewCriticalUserFragment` helper 未被外部 caller 使用**：在 `producers.go` 定义但 chatbot/salesrag 都用了私有内联构造（参数不完全匹配）。保留供未来 producer 迁移复用。

### Task 12 — Observability + Evaluation
- **Evaluation P50/P90/P99 阈值仍是 Phase 1 宽松值**：当前测试用 P50≤50% / P90≤80%，spec §4.3 字面要求 P50≤5% / P90≤10%。**S5 阶段必做**：跑真实 tokenizer 拿 ground truth → 调 `TokenProfile.classes.*.token_per_char` 系数 + `safety_multiplier` → 收紧测试阈值。在 `evaluation_test.go:177-181` 内联注释中标记。
- **`EstimatedCompletionTokens` 与 `ReservedOutputTokens` 字段冗余**：当前两者赋同一个值（`policy.ReservedOutputTokens`），spec §11.2 要求两个独立 metadata key。已在 `budgetMetadata` struct 加 `TODO(S7): decouple when actual completion estimate is available from biz layer` 注释。

### Task 13 — Admin Web UI
- **`as unknown as` 双重类型断言（约 8 处）**：与 numind-admin-web 项目其他 view 一致（`EstimationCoefficientView.vue` 等）。是项目级 tech debt（axios interceptor 类型与函数签名 impedance mismatch）。等项目统一改 axios wrapper 时一并解决。
- **硬编码 hex 颜色（5 处）**：`.service-type-badge { background: #dbeafe; color: #1e40af }` 等。等 admin 端设计 token 系统建立后统一替换。

### Task 14 — User Web Counter
- **Counter 显示策略不一致**：StepInput 始终显示 counter；ChatComposer / ChatbotChat / InputArea 仅当 `text.length > 0` 显示。两种策略各有合理 UX 理由，等产品有统一规范后对齐。
- **InputArea 移动端隐藏 hint 文字**：`@media (max-width: 768px)` 时 `.input-budget-hint { display: none }` 保留 label 但删 hint 文字。空间权衡，符合移动 UX 实践。

---

## §2 Pre-existing 测试失败（与本 feature 无关，但发现于 S4）

**根因**：`newCreditsUser` 测试 fixture 的 seed bug —— 创建 user 时设 `BillingMode=credits` + `UserTier=standard` + `TierExpires=nil`。`HasActiveMembership()` 看到 `UserTier=standard` 返回 `true` → `isEffectiveLegacy()` 返回 `true` → 路由到 `legacyTierImpl.Reserve`，但该 path 触发 design-by-contract panic（"credits 用户不应走 legacy path"）。

**已在 `develop@9f5f5ca` (Wave 5 完成时的 base) 验证存在**，多个 review 通过 `git stash` round-trip 交叉验证。

### 受影响测试（约 6 个）

| 测试名 | 包路径 | 失败模式 |
|--------|--------|----------|
| `TestSopCredits_CreditsMode_ReserveThenReconcile` | `internal/numind/biz/sop` | panic in `legacyTierImpl.Reserve` |
| `TestReserve_ExactlyExhaustedThenRetry_ReturnsInsufficientSentinel` | `internal/numind/biz/credit` | panic in `legacyTierImpl.Reserve` |
| `TestAcquireSalesragCredits_CreditsHappyPath` | `internal/numind/biz/salesrag` | panic in `legacyTierImpl.Reserve` |
| `TestAcquireSalesragCredits_InsufficientBalance` | `internal/numind/biz/salesrag` | 同上 |
| `TestAcquireSalesragCredits_IdempotentReplay` | `internal/numind/biz/salesrag` | 同上 |
| `TestFinalize_StreamErrorTriggersRefund` | `internal/numind/biz/salesrag` | 同上 |

Task 9 reviewer 还提到 `TestCreateRun_FreeUserReturnsTypedError`（biz/sop）也 fail，但根因略不同（FreeUser 路径），需独立确认。

### 建议修复方案
新建一个独立 hotfix branch `fix/credits-fixture-seed`：
```go
// 修 newCreditsUser fixture（位置：internal/numind/biz/credit/testhelpers_test.go 之类）
func newCreditsUser(id uint) *model.User {
    return &model.User{
        ID:           id,
        BillingMode:  "credits",
        UserTier:     "free",       // ← 改为 free（credits 用户没有旧 tier）
        TierExpires:  nil,
        // 或者保持 UserTier=standard 但设 TierExpires=time.Now().Add(24*time.Hour)
        //（这样 HasActiveMembership=true 但 isEffectiveLegacy 检查更细致）
    }
}
```
**预计工作量：30 分钟**，跑遍 6 个测试确认全部 PASS。

---

## §3 其他 session 留下的 untracked 文件（不属于本 feature）

整个 S4 期间小心绕开但保留在 working tree 的文件：

### `internal/numind/biz/sop/bookmark_tokens_test.go`
- **属于：** SOP token bookmark 相关功能（推测）
- **状态：** untracked，可能是另一并行 session 的 WIP
- **处理建议：** 联系作者确认是否要 commit；如确认无主可由本 session 后清理。

### `internal/numind/biz/sop/vicky_verify_test.go`
- **属于：** "Vicky" 相关验证测试（unclear）
- **状态：** untracked
- **处理建议：** 同上。

### `scripts/2026-04-24-legacy-tier-migration/`
- **属于：** 已完成的 `legacy-tier-migration` feature（manifest decisions 中可见 2026-04-24 完成）
- **状态：** untracked 整目录
- **处理建议：** 该 feature 已上线（commit `527f03d` 之前），按理应 commit 过；脚本目录可能是事后归档但未提交。需确认是否仍需保留 → 若需要则 commit 到 develop，若不需要则删除。

**整体建议：** S5 release engineer 应在部署前清理这些 untracked 文件（commit 或删除），让 working tree 干净。

---

## §4 Git Stash 列表（4 个其他 session 的 WIP）

```
stash@{0}: On feature/child-run-permission: parallel-session-wip
stash@{1}: On develop: task4-impl-pre-switch
stash@{2}: WIP on develop: 17a9421 docs(manifest): child-run-permission S4 Task 2 done + reviewed
stash@{3}: On develop: parallel-session-wip: 4 files from feature/child-run-permission
```

**全部属于 `feature/child-run-permission`** —— 一个跟本 feature 完全独立的并行 feature 的 working state。

### 重要提示
- 本 feature S4 期间曾因误操作 `git stash pop` 触发过 stash@{1}（task4-impl-pre-switch）的合并冲突，影响 `build-manifest.yaml` + `internal/numind/store/chatbot_config.go`。已通过 `git checkout HEAD --` 恢复，stash 重新保留。
- **绝对不要主动 `git stash pop`** —— 让 child-run-permission feature 的 owner 自行处理。

### 处理建议
等 `feature/child-run-permission` 工作 owner 回到 develop 时，由其决定每个 stash 的去留。

---

## §5 Side-effect Changes（非 Task plan 范围，但已 merge 到 develop）

### 5.1 Task 12 副作用：`internal/numind/store/chatbot_config.go` 11 行注释扩展
- **变更：** 函数 `ListByIDsOwnedBy` 和 `CountByIDsOwnedBy` 的 doc comment 从 1 行扩展为 3 行（更详细的语义说明）。
- **来自：** Wave 8 Task 12 implementer 在修复 bug 时顺手把这个文件的注释改了（疑似从某个 stash 误 pop 的内容中"取了更详细版本"）。
- **影响：** 零 — 纯注释扩展，无逻辑变化。
- **commit：** `813eab1` (within Task 12) → merged via `fffe861`
- **是否回滚：** 否，注释更详细对维护反而有利。仅在 Task 12 merge commit message 中标注。

### 5.2 Task 15 顺手修：`internal/numind/controller/v1/credit/credit_test.go` 加 6 行 stub
- **变更：** Task 4 给 `ICreditService` interface 新增了 `CheckAndEstimateBudget` + `ReserveBudget` 方法，但 controller 测试中的 `stubCreditSvc` 没更新，导致 `controller/v1/credit` 包测试 build fail。
- **修复：** Task 15 implementer 加了 2 个 no-op panic stub 让 build 通过。
- **commit：** `bf5aee9`
- **是否真正修了 bug：** 让测试 build 通过，但 stub 是 panic 的 —— 任何调用该 mock 的 controller 测试都会在 runtime 触发 panic。当前 controller 测试似乎没人调这两个新方法（所以 build 修了就够），但**如果未来 controller 层加这两方法的测试，需要给 stub 加合理实现**。
- **建议：** 跟 §2 的 newCreditsUser fixture seed bug 一起在同一个 hotfix branch 解决。

---

## §6 Tech Debt 推荐处理顺序

按 ROI（修复成本 vs 风险/价值）排序：

### High priority（建议下一个 sprint 解决）
1. **§2 newCreditsUser fixture seed bug** —— 影响 CI 信号，让 6 个测试持续 fail 会麻木开发者。30 分钟修复。
2. **§5.2 controller/v1/credit stub 真实实现** —— 与 §2 一起修，避免运行时 panic 风险。15 分钟。
3. **§3 untracked files 清理** —— 部署前必做，否则 dev/qa/prod 可能误带这些文件。15 分钟。

### Medium priority（S5 阶段必做）
4. **§1 Task 12 evaluation 阈值收紧** —— 收集真实 token ground truth 后调系数到 spec §4.3 字面阈值。S5 阶段 1-2 周。
5. **§1 Task 7 token profile family-level lookup**：等 admin UI（Task 13）暴露 `model_family` 配置后，实现 4 级 lookup。1-2 天。

### Low priority（看产品节奏）
6. **§1 Task 9 SOP reasoning fragments 映射** —— 需要 SOP 数据流重构。等 SOP 重构 sprint 一起做。
7. **§1 Task 10 chatbot.stream double-fetch** —— 与 SalesRAG/chatbot prompt 拼装重构一起。
8. **§4 Git stashes** —— 等 child-run-permission feature owner 处理。
9. **§1 Task 6/12 EstimatedCompletionTokens vs ReservedOutputTokens 解耦** —— 等 biz 层提供独立 completion token 估算。

### Permanent / Deferred indefinitely
10. **§1 Task 13 Admin UI 硬编码颜色 + as unknown as 断言** —— 跟项目级重构（admin 设计系统、axios 类型重写）一起。

---

## §7 已关闭项（archive）

> 修复后从上面移到这里，记录关闭时间 + commit SHA。

（暂无）

---

## 维护

- 本文档放在 `docs/superpowers/known-issues/`，下次创建类似汇总文档可沿用此目录。
- 开发者 onboarding 路径：`CLAUDE.md` → `build-manifest.yaml` → `docs/superpowers/known-issues/` → 具体 spec/plan。
