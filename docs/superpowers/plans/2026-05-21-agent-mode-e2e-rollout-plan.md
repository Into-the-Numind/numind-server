# NDF S3 Plan · `agent-mode-e2e-rollout`

**Track**：Standard
**Feature ID**：`agent-mode-e2e-rollout` (#14/14)
**Author**: AI (autopilot)
**Date**: 2026-05-21
**前置**：S0 `dd3203cd` + S1 `a6e369be` + S2 `715d3897`，S2 reviewer 1 P1 + 4 P2 (本 plan §0 inline errata 修)

---

## §0 S2 errata 修正（inline applied）

| 来源 | 处置 |
|------|------|
| S2 reviewer P1-1 | A3 task 改用 `p.l1Store.Create(ctx, &model.AgentSessionMemory{...})` 而非 `p.notepad.AppendL1(...)` |
| S2 reviewer P2-1 | 新增 M-A8a task：定义 `callctx.WithCallID` / `callctx.CallIDFromCtx` + `adapter.LookupUsage(callID) (budgetctx.Usage, bool)` |
| S2 reviewer P2-2 | A1 task 改用 `agent.HandleLLMError(state, err)` → `r.handlePTLError` / `r.handleMaxOutputError`（已存在 line 524/567）|
| S2 reviewer P2-3 | B fixture task 显式列出 commit 到 **两个 repo** 的 path |
| S2 reviewer P2-4 | C3 task 加 GORM model field `CancellationRequestedAt *time.Time`；C4 task 加 store join 策略：用 `agent_run` 已有的 `AgentDefinitionID`（实际存在 — runner.go RunRequest.AgentDefID 写入 store），join 到 `agent_definition` 拿 `parent_user_id` 过滤 |

---

## §1 M-Task 全清单（35 个 task）

### Phase A — Mock → 真实 LLM (12 tasks)

| Task | 仓库 | 文件 | LOC 估 | 测试 |
|------|------|------|-------|------|
| M-A0a | numind-server | `internal/pkg/aiservice/profile/constants.go` | +30 | 1 test (count==21) |
| M-A0b | numind-server | `migrations/20260521_180000_agent_task_profiles_seed.sql` + rollback | +50 | DB seed verify |
| M-A1 | numind-server | `runner.go:389-490` 重写 ReAct loop + 4 helper | +200 | 7 tests in `runner_e2e_loop_test.go` |
| M-A2 | numind-server | `memory/embedder.go` 加 aiserviceEmbedder + `memory/retrieval.go` 加 RetrieverOption | +80 | 2 tests |
| M-A3 | numind-server | `memory/provider.go` SyncTurn 真实实装 + `memory/sync_prompt.go` + `pkg/middleware/agent_session_ctx.go` | +180 | 5 tests |
| M-A4 | numind-server | `compact/aiservice_provider.go` + `biz.go` wire | +100 | 3 tests |
| M-A5 | numind-server | `narration/aiservice_fallback.go` + `biz.go` wire | +130 | 4 tests |
| M-A6 | numind-server | `compliance/injection_detector.go` 加 aiserviceLLMClassifier + `biz.go` wire | +90 | 4 tests |
| M-A7 | numind-server | `permission/validators/llm_classifier.go`（NEW）+ wire | +90 | 4 tests |
| M-A8a | numind-server | `biz/agent/callctx/callctx.go` (NEW pkg) + `adapter.go` usageStore + `adapter_lookup.go` | +60 | 3 tests |
| M-A8b | numind-server | `biz/agent/budgetctx/usage_ctx.go` (NEW pkg) + budgetgate gate.go 改 PostToolCall | +90 | 3 tests |
| M-A9 | numind-server | `audit_logger.go` + `runner.go` final log + `compliance/gate.go` deny log | +40 | 3 tests |

### Phase B — Playwright E2E (10 tasks)

| Task | 仓库 | 文件 | 测试 |
|------|------|------|------|
| M-B0a | numind-server | `migrations/20260521_190000_seed_e2e_test_agent.sql` + rollback | dev only |
| M-B0b | numind-admin-web + numind-web-v3 | `e2e/fixtures/test-agent-id.json`（**两个 repo 各一份**）| — |
| M-B1 | numind-admin-web | `e2e/admin-create-agent.spec.ts` | 1 spec |
| M-B2 | numind-web-v3 | `e2e/student-dialog-happy.spec.ts` | 1 spec |
| M-B3 | numind-web-v3 | `e2e/student-permission-deny.spec.ts` | 1 spec |
| M-B4 | numind-web-v3 | `e2e/student-budget-exceed.spec.ts` | 1 spec |
| M-B5 | numind-web-v3 | `e2e/student-compliance-block.spec.ts` | 1 spec |
| M-B6 | numind-web-v3 | `e2e/student-compact-trigger.spec.ts` | 1 spec |
| M-B7 | numind-web-v3 | `e2e/student-session-resume.spec.ts` | 1 spec |
| M-B8 | numind-admin-web | `e2e/admin-history-rollback.spec.ts` | 1 spec |

### Phase C — Admin-web UI 补全 (7 tasks)

| Task | 仓库 | 文件 | LOC 估 | 测试 |
|------|------|------|-------|------|
| M-C1a | numind-server | `controller/v1/admin/compliance_rule.go` + `biz/compliance/admin_service.go` + admin_router.go register | +250 | 5 endpoint tests |
| M-C1b | numind-admin-web | `api/complianceRule.ts` + `stores/complianceRule.ts` + 3 views | +400 | unit + e2e |
| M-C2 | numind-admin-web | `views/agent/AgentMonitoring.vue` Langfuse trace link + `.env.development` | +40 | manual |
| M-C3a | numind-server | `migrations/20260521_200000_agent_run_admin_cancel.sql` + rollback + model field `CancellationRequestedAt *time.Time` + GORM tag | +50 | model test |
| M-C3b | numind-server | `controller/v1/admin/agent_run.go` cancel endpoint + `biz/agent/admin_cancel.go` + admin_router register | +180 | 3 endpoint tests |
| M-C3c | numind-admin-web | `AgentMonitoring.vue` action 加 [强制取消] + ConfirmModal | +60 | unit |
| M-C4a | numind-server | store `ListByParentUserIDAndStatus` (join `agent_run` ⋈ `agent_definition` ON agent_definition_id) + biz `ListRunsByStatus` + admin endpoint | +150 | 3 endpoint tests |
| M-C4b | numind-admin-web | `AgentMonitoring.vue` 替换假数据 fetcher + 30s 轮询 (useIntervalFn) | +80 | unit |
| M-C5 | numind-admin-web | `AgentMonitoring.vue` 删 NoticeBanner + import + 测试 snapshot 更新 | +5/-15 | unit |

### Phase D — Dev 部署 (4 tasks)

| Task | 操作 |
|------|------|
| M-D1 | SSH dev MySQL 跑 13 + 4 个新 migration（按 §4 表）+ verify SQL |
| M-D2 | `/deploy-dev server` + `/deploy-dev admin`（端口 9091 / 9099）|
| M-D3 | `/deploy-dev` admin-web + web-v3（端口 9100 / 9200）|
| M-D4 | dev 8 e2e smoke test + Langfuse trace 验证 |

### Phase E — Prod 准备文档 (8 tasks)

| Task | 文件 |
|------|------|
| M-E1 | `docs/agent-mode/deploy-checklist-feature-14.md` |
| M-E2 | `docs/agent-mode/config-prod-diff.md` |
| M-E3 | `docs/agent-mode/runbook.md` |
| M-E4 | `docs/agent-mode/architecture-v1.md` 加 §16 v1.0 Landing Record |
| M-E5 | 根目录 `CLAUDE.md` 加 Agent 模式 § |
| M-E6 | `numind-server/CLAUDE.md` 加 biz/agent/* 子包说明 |
| M-E7 | `docs/agent-mode/go-live-checklist.md` |
| M-E8 | `CHANGELOG.md` (or v2.2.0 节) |

---

## §2 Wave 并行编排（Phase A）

> **Tier 3 规则**：同 worktree disjoint file 并行需 `ndf-check-disjoint` 验证

### Wave 1（Phase A 基础设施 — 4 个并行 implementer）

| Implementer | Task | 文件归属 |
|-------------|------|---------|
| Agent-1 | M-A0a + M-A0b | `internal/pkg/aiservice/profile/constants.go` + `migrations/20260521_180000_*` |
| Agent-2 | M-A2 | `internal/numind/biz/memory/embedder.go` + `retrieval.go` |
| Agent-3 | M-A4 | `internal/numind/biz/compact/aiservice_provider.go`（新文件）|
| Agent-4 | M-A5 | `internal/numind/biz/narration/aiservice_fallback.go`（新文件）|

**Disjoint check**：
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "internal/pkg/aiservice/profile/constants.go,migrations/20260521_180000_agent_task_profiles_seed.sql,migrations/20260521_180000_agent_task_profiles_seed_rollback.sql" \
  "internal/numind/biz/memory/embedder.go,internal/numind/biz/memory/retrieval.go,internal/numind/biz/memory/embedder_test.go" \
  "internal/numind/biz/compact/aiservice_provider.go,internal/numind/biz/compact/aiservice_provider_test.go" \
  "internal/numind/biz/narration/aiservice_fallback.go,internal/numind/biz/narration/aiservice_fallback_test.go"
```
Expected: exit 0

### Wave 2（Phase A 串行 — 单 implementer）

| Task | 理由 |
|------|------|
| M-A6 | biz.go wire 与 Wave 1 wire 冲突，串行 |
| M-A7 | 需 grep #6 permission validators 找 placeholder |
| M-A8a | 新 callctx pkg + 改 adapter.go usageStore |
| M-A8b | budgetgate gate.go 改 PostToolCall — 依赖 M-A8a |
| M-A3 | provider.go SyncTurn — 依赖 A1 ctx 注入（runner.go）|
| M-A1 | runner.go 大改 — 最后做（依赖前面所有 wire） |
| M-A9 | 3 处 log 调用，串行 |

**理由**：A1 是 runner.go 主大改，其他 wire 完后做。A8a/A8b 串行依赖。A3 依赖 A1 的 sessionID ctx 注入。

### Wave 3（Phase B 并行 — 同仓库分组）

admin-web 内 spec 互不冲突 → Tier 2 并行：
- M-B0b admin-web fixture（独立）
- M-B1 admin-create-agent.spec.ts
- M-B8 admin-history-rollback.spec.ts

web-v3 内 spec 互不冲突 → Tier 2 并行：
- M-B0b web-v3 fixture（独立）
- M-B2 / M-B3 / M-B4 / M-B5 / M-B6 / M-B7（6 spec 并行 OK，每个独立 file）

### Wave 4（Phase C 串行 + 跨仓库并行）

server 后端：M-C1a → M-C3a → M-C3b → M-C4a（串行 — 都在 controller/biz/store 改）
admin-web 前端：M-C1b → M-C2 → M-C3c → M-C4b → M-C5（串行 — 都在 AgentMonitoring.vue 或同 namespace）

server 与 admin-web 跨仓库 → Tier 2 并行

### Wave 5（Phase D 严格串行）

D1 → D2 → D3 → D4，每步都依赖前一步

### Wave 6（Phase E 并行）

8 个文档独立，全部 Tier 2 并行 OK

---

## §3 NDF Rule 10 S5 验证策略

| 范围 | 验证方式 |
|------|---------|
| Phase A 9 切换点 | Go TDD + race detector + mock aiservice (interface injection) |
| Phase A 整合 | `runner_e2e_loop_test.go` + mock aiservice 5-step ReAct + verify state machine 派发 |
| Phase B 8 e2e | Playwright 真实 dev 环境（D4 跑） |
| Phase C 后端 7 endpoints | Go controller test + integration test |
| Phase C 前端 | vitest + at least 2 Playwright spec (e2e/compliance-rule.spec.ts + e2e/agent-run-cancel.spec.ts) |
| Phase D | Manual SSH + smoke test |
| Phase E | Reviewer subagent on each doc |
| 端到端 | dev 8 e2e 全 PASS + Langfuse trace 后台手动验 |

**回归保护**：
- Phase A 单测 → **永久回归**
- Phase B 8 e2e → **永久回归**（dev 部署后可重跑）
- Phase C 单测 + 2 e2e → **永久回归**
- Phase D smoke → **一次性**（每次 dev 部署后手动重跑）
- Phase E doc → **静态**（reviewer 把关）

**NDF Rule 10 高风险**：A7 permission classifier + A8 budget tokens 数据流触及"权限/计费"，强制 Phase B B3/B4 e2e 覆盖（已设计）

---

## §4 D1 Migration 顺序（最终）

> 严格按时间戳 + 字母序

| 序号 | 文件 | Feature | 说明 |
|------|------|---------|------|
| 1 | `2026XXXX_*_agent_run_table.sql` | #2 | agent_run 主表 |
| 2 | `2026XXXX_*_agent_factory_registry.sql` | #3 | tool registry |
| 3 | `2026XXXX_*_agent_sandbox_session.sql` | #4 | sandbox |
| 4 | `2026XXXX_*_agent_mode_skill_system.sql` (× 3) | #5 | agent_definition + history + skill_template |
| 5 | `20260521_120000_agent_mode_compliance_3layer.sql` | #13 | compliance_rule + compliance_audit_log |
| 6 | `20260521_120000_agent_permission_pipeline.sql` | #6 | agent_permission_config + agent_permission_decision_log |
| 7 | `20260521_120000_create_agent_session_memory.sql` | #7 | memory L1 |
| 8 | `2026XXXX_*_user_global_memory.sql` | #7 | memory L2 |
| 9 | `2026XXXX_*_agent_run_compact.sql` (ALTER) | #9 | agent_run.compact_state/summary |
| 10 | `20260521_140000_agent_billing_source_type_admin_test.sql` | #12 | source_type CHECK enum |
| 11 | `20260521_140100_agent_run_terminal_metadata.sql` (ALTER) | #12 | terminal_metadata |
| 12 | `20260521_140200_create_credit_admin_test_grant.sql` | #12 | admin_test grant |
| 13 | `20260521_180000_agent_task_profiles_seed.sql` | #14 (M-A0b) | 7 task profile |
| 14 | `20260521_190000_seed_e2e_test_agent.sql` | #14 (M-B0a) | E2E test agent (dev only) |
| 15 | `20260521_200000_agent_run_admin_cancel.sql` (ALTER) | #14 (M-C3a) | cancellation_requested_at |

每个 migration 跑前用 `SELECT 1 FROM information_schema.tables WHERE table_name='<x>'` 验证状态（如已存在则 skip + log）

---

## §5 Done Criteria（feature 整体）

- [ ] 9 mock 切换点全接通：runner.go 无 `_ = einoAgent`；biz/memory 无 mockEmbedder import；biz.go 无 MockCompactProvider；narration 默认 fallback 用 aiserviceLLMFallback；compliance.NewInjectionDetector 用 NewAIServiceLLMClassifier；permission L3 用 NewAIServiceLLMClassifier；adapter.go usageStore 工作；audit_logger 有 drop log；runner final log
- [ ] `go test -race -count=1 ./...` 30+ 包 PASS
- [ ] `go vet ./...` exit 0
- [ ] biz/agent 覆盖率 ≥ 80%
- [ ] biz/memory 覆盖率不下降
- [ ] biz/compact 覆盖率不下降
- [ ] biz/narration 覆盖率不下降
- [ ] biz/compliance 覆盖率不下降
- [ ] biz/permission 覆盖率不下降
- [ ] 7 个新 admin endpoints 单测 PASS
- [ ] 8 个 Playwright e2e 在 dev 全 PASS（D4 后）
- [ ] dev 8 e2e Langfuse trace 完整（每 trace 含 ≥ 1 generation w/ model=qwen-turbo + ≥ 1 span）
- [ ] 0 prod 影响（git diff config_prod.yaml 空 / 不打 tag / 不调 /deploy-prod）
- [ ] 累计 P0 = 0；P1 全修
- [ ] 3 仓库 S6 manual merge 完成

---

## §6 ndf-done 前置门槛检查

- [ ] manifest `progress.completed_tasks == 35`
- [ ] manifest `progress.reviewed_tasks == 35`
- [ ] manifest `stage == S5`（即将切 S6）
- [ ] 全部 M-task commit 存在（git log on feature/agent-mode-e2e-rollout 3 仓库）
- [ ] dev 部署 + smoke 验证通过
- [ ] S5 acceptance doc 写好

---

## §7 风险（S3 viable）

| 风险 | 缓解 |
|------|------|
| A1 runner.go 大改引入 race | race detector + 100 goroutine concurrent test |
| A8a/A8b ctx 注入断链 | callctx test 验证 runner → adapter → budgetgate 透传 |
| A3 SyncTurn LLM 调用阻塞主对话 | `go func()` async + 不依赖结果 |
| Phase B B6 compact 在 dev 难触发 | mock 长 user 消息（4000 字 × 30 轮）或临时降阈值 |
| Phase D migration 跑挂 | backup dev DB 前；逐个 `SELECT ... information_schema` 验证 |
| 3 仓库 merge 冲突 | S6 顺序 server → admin-web → web-v3；提前 git fetch develop |

---

## §8 状态

**S3 完结。等 reviewer。**
