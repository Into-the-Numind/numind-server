# SOP 上下文压缩串步修复 — 实施计划 (S3)

> Feature: `sop-context-ordering-fix` · Track: standard · 2026-06-05
> 关联：`docs/superpowers/specs/2026-06-05-sop-context-ordering-fix-design.md`

## Task DAG

T0 →(必须先 commit) T1 → review → S5

### T0 — 失败复现测试（NDF 规则 11，第一个 commit）
- commit 前缀：`test(qa): reproduce ...`
- 内容：
  1. `biz/contextbudget/biz_test.go` `TestPrepare_CompressionKeepsCurrentInputLast`：构造 system(CompressNone) + 2 个 durable 历史(CompressSummarize, 大内容) + 当前输入(CompressNone, 高 Order)；小 ContextWindow 强制压缩；wire `successCompressor` 返回带哨兵内容的摘要；断言 `result.Messages` 最后一条 == 当前输入内容、摘要出现在当前输入之前、且 compressor 确被调用。**pre-fix FAIL**（applyPlan 追加摘要到末尾 + 无排序）。
  2. `biz/sop/...test.go` `TestBuildSOPGatewayFragments_CurrentInputHasHighestOrder`：`buildSOPGatewayFragments` 后断言当前输入(critical)fragment 的 Order > 所有历史 fragment 的 Order。**pre-fix FAIL**（当前输入 Order=0）。
- 独立验收：两个测试编译通过且 FAIL（证明 bug 在）。

### T1 — 实施修复（Fix A + B + C，一个 commit）
- **Fix A** `producers.go`：`NewImmutableSystemFragment(id, content string, order int)`、`NewCriticalUserFragment(id, content string, order int)` 写入 `Order`；更新调用点 `chatbot/stream.go`、`sop_fragments.go`（5 + 3 处）。
- **Fix B** `biz.go applyPlan`：`summaryFrag.Order = min(summarizeCandidates.Order)`。
- **Fix C** `biz.go Prepare`/`prepareWithoutCompression`：渲染前 `sort.SliceStable(fragments, byOrder)`；加 `sort` import。
- 独立验收：编译通过；T0 两个测试转 PASS；既有 contextbudget/sop/chatbot/salesrag 测试不回归。

> 原子性说明：T0 单独 commit 后系统可编译（测试 FAIL 是预期）；T1 单独 commit 后系统可编译且全绿。符合规则 9。

## S5 验证策略（NDF 规则 10）

- **验证方式：后端 Go 单元测试（TDD 持久回归）** + `task lint`。
- **理由**：bug 100% 在后端 `contextbudget`/`sop` 纯逻辑层（fragment 排序 → 渲染消息顺序），无前端/UI 参与；Go 单测能确定性复现并永久守护回归（优于一次性 gstack /qa，因本 bug 触发条件=压缩，难在浏览器稳定构造）。计费相邻共享逻辑更应有持久回归保护。
- **关键路径**：
  1. `Prepare` 压缩路径渲染顺序（T0 测试 1）——覆盖 SOP + chatbot 共享缺陷。
  2. `buildSOPGatewayFragments` 当前输入 Order（T0 测试 2）——覆盖 SOP producer 缺陷。
  3. `go test ./...` 全仓库回归——确认 chatbot/salesrag/contextbudget 既有测试不破。
- **人工核对**：阅读渲染后消息序，确认 system → 历史/摘要 → 当前指令（最后）。

## Review（规则 6）
T1 完成后并行 dispatch 2 个 Sonnet reviewer（Spec Compliance + Code Quality）；P0/P1 修复后再过；reviewed_tasks == completed_tasks 才能 ndf-done。
