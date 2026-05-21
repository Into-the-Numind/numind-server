# #14 agent-mode-e2e-rollout — Session 1 Handoff

**Session 1 日期**：2026-05-21
**Session 1 范围**：S0-S3 完整规划 + Phase A Wave 1 前 3 task
**下个 Session 接入点**：S4 Phase A Wave 1 剩余 + Wave 2 起

---

## 1. 已完成

### NDF 规划阶段（S0 → S3）

| 阶段 | 工件 | 主 commit | reviewer commit | reviewer 反馈 |
|------|------|----------|----------------|--------------|
| S0 | `numind-server/requirements/agent-mode-e2e-rollout.md` | `c8aeeded` | `dd3203cd` | 2 P0 + 4 P1 + 5 P2 → 13 项 S0-Dx 决策 |
| S1 | `numind-server/proposals/agent-mode-e2e-rollout-proposal.md` | `5923bee4` | `a6e369be` | 2 P1 + 6 P2 → 7 项 S1-Dx 决策 |
| S2 | `numind-server/docs/superpowers/specs/2026-05-21-agent-mode-e2e-rollout-design.md` | `715d3897` | (inline in S3 §0) | 1 P1 + 4 P2 → 5 项 inline errata |
| S3 | `numind-server/docs/superpowers/plans/2026-05-21-agent-mode-e2e-rollout-plan.md` | `69d9485a` | `416f98a8` | 1 P1 + 6 P2 → 7 项 S3-Dx 决策 |

**累计 reviewer**：4 轮 Sonnet → 6 P1 + 20 P2 全修；0 P0 残留。

### Phase A 实施（Wave 1 进行中）

| Task | 仓库 | Commit | 状态 |
|------|------|--------|------|
| M-A0a | numind-server | `ca01ddd3` | ✅ DONE — profile/constants.go +7 const (21 total) |
| M-A0b | numind-server | `ca01ddd3` (same) | ✅ DONE — migrations/20260521_180000_agent_task_profiles_seed{,_rollback}.sql |
| M-A2 | numind-server | `a87e5469` | ✅ DONE — memory/embedder.go aiserviceEmbedder + RetrieverOption + biz.go wire + 4 tests PASS |

**生产 build 状态**：
- `go build ./...` exit 0（仅 sqlite-vec cgo 历史 warning，与 #14 无关）
- `go test -race ./internal/numind/biz/memory/` PASS（M-A2 4 tests + 38 既有 tests）

---

## 2. 待完成（继续 session 接力）

### Phase A 剩 9 个 task（按 Wave 2 顺序）

> **重要**：Wave 2 顺序见 S3 plan §2 — 严格按以下顺序串行：

1. **M-A3** — `memory/provider.go` SyncTurn 真实实装
   - 新建 `internal/pkg/middleware/agent_session_ctx.go`（CtxKeySessionID）
   - 新建 `internal/numind/biz/memory/sync_prompt.go`（SyncTurnSystemPrompt）
   - 改 `compositeProvider.SyncTurn` 用 `p.l1Store.Create(ctx, &model.AgentSessionMemory{...})`（**S3 P1-1 修正**：不用 `p.notepad.AppendL1`，方法不存在）
   - 5 tests in `provider_synturn_test.go`

2. **M-A4** — `compact/aiservice_provider.go` 新文件
   - `aiserviceCompactProvider` 实装 CompactProvider interface
   - 调 `aiservice.Chat(ctx, profile.AgentCompact, ...)` with `BuildCompactSystemPrompt` (#9 已存在)
   - biz.go 替换 `&compact.MockCompactProvider{...}` 为 `compact.NewAIServiceCompactProvider(compact.DefaultConfig())`
   - 删除 TODO(#14) 注释
   - 3 tests

3. **M-A5** — `narration/aiservice_fallback.go` 新文件
   - `aiserviceLLMFallback` 实装 LLMFallback interface
   - 签名 `Render(ctx, toolName, state, payload EmitPayload) (verb, detail string)`（**S1 P1-2 修正**：不用 Generate）
   - `sync.Map` 缓存（**S1 P2-6 修正**：不用 LRUCache 自己 mutex）
   - 200ms timeout，超时 fail-allow（UX 方向，与 A6 fail-deny 刻意相反 — S0-D12）
   - biz.go wire `narration.NewTranslator(renderer, NewAIServiceLLMFallback())`
   - 4 tests

4. **M-A6** — `compliance/injection_detector.go` 加 `aiserviceLLMClassifier`
   - 复用现有 `LLMClassifier` interface（**S1-D5**：不引入新 InjectionDecision struct）
   - 签名 `Classify(ctx, input string) (bool, error)`
   - 300ms timeout，超时 **fail-deny**（注入检测安全方向）
   - biz.go wire `compliance.NewInjectionDetector(compliance.NewAIServiceLLMClassifier())`
   - 4 tests

5. **M-A7** — `permission/validators/llm_classifier.go` 新文件
   - 先 grep `internal/numind/biz/permission/validators/` 找 L3 auto-mode placeholder
   - 新接口 `LLMClassifier` + 实装
   - 250ms timeout，超时 **fail-allow**（UX 方向）
   - 4 tests

6. **M-A8a** — `biz/agent/callctx/callctx.go` 新 pkg + adapter usageStore
   - 新 pkg `callctx`：`WithCallID(ctx, id) context.Context` + `CallIDFromCtx(ctx) string`
   - `adapter.go` 加 `usageStore sync.Map` 字段
   - `adapter.Generate` 内调 aiservice.Chat 后用 callID key 存 Usage
   - 加 `(a *aiserviceAdapter) LookupUsage(callID string) (Usage, bool)` exported method
   - 3 tests

7. **M-A8b** — `biz/agent/budgetctx/usage_ctx.go` 新 pkg + budgetgate 改造
   - 新 pkg `budgetctx`：`WithUsage(ctx, Usage)` + `UsageFromCtx`
   - `budgetgate/gate.go` PostToolCall 内：取 adapter 引用 → LookupUsage(callID) → tracker.RecordUsage
   - **注意**：避免 import cycle —— budgetgate 已知 agent 包；agent 包不能 import budgetgate；考虑通过 interface 反向注入或 callctx pkg 中介
   - 3 tests

8. **M-A1** — `runner.go:389-490` 大改 ReAct loop（**最复杂**）
   - 删 `_ = einoAgent` 短路（line 389）
   - 接入真实 `einoAgent.Generate(queryCtx, einoMessages)` 循环
   - 用 `agent.HandleLLMError(state, err)` → `r.handlePTLError`（line 524）/ `r.handleMaxOutputError`（line 567）现有 helpers（**S2 P2-2 修正**：不用 `compact.IsPTLError`）
   - 调 `provider.SyncTurn` 末尾（goroutine async）
   - `run := &model.AgentRun{..., AgentDefinitionID: req.AgentDefID, ...}` 写入新字段（C 依赖 — 见 M-C3a）
   - 7 tests in `runner_e2e_loop_test.go`

9. **M-A9** — log observability（3 处）
   - `audit_logger.go`: drop count threshold zap.Warn
   - `runner.go`: agent_run_completed structured log
   - `compliance/gate.go`: compliance_hit log
   - 3 tests

### Phase B 10 个 task

跨 admin-web + web-v3 2 仓库的 Playwright e2e：
- M-B0a: seed E2E test agent SQL (dev only)
- M-B0b: fixture json (两个 repo 各一份)
- M-B0c: seed compliance_rule for B5
- M-B1: admin-create-agent.spec.ts (admin-web)
- M-B2 ~ M-B7: 6 student spec (web-v3)
- M-B8: admin-history-rollback.spec.ts (admin-web)

### Phase C 7 个 task（跨 numind-server + numind-admin-web）

- M-C1a/C1b: compliance_rule CRUD UI 后端 + 前端
- M-C2 / M-C2-prod: Langfuse trace 跳转 + 文档
- M-C3a/C3b/C3c: agent_run 强制取消（migration 加 `agent_definition_id` + `cancellation_requested_at` 两列，model field，endpoint，UI button）
- M-C4a/C4b: 监控真实数据源（store join + 后端 endpoint + 前端轮询）
- M-C5: NoticeBanner 移除

### Phase D 4 个 task

- M-D1: SSH dev MySQL 跑 15 个 migration（含 #14 新增 3 个）
- M-D2: `/deploy-dev server` + admin
- M-D3: `/deploy-dev` 前端 ×2
- M-D4: 8 e2e in dev + Langfuse trace verify

### Phase E 8 文档

- M-E1 ~ M-E8: deploy-checklist / config-prod-diff / runbook / arch §16 / CLAUDE.md / go-live-checklist / CHANGELOG

### S5 acceptance + S6 merge

- S5 acceptance doc
- S6 manual merge 3 repos: server (API) → admin-web → web-v3

---

## 3. 关键约束 / 不变量（reminder）

### 0 prod 影响（红线）
- ❌ 不调 `/deploy-prod`
- ❌ 不打 `git tag v*` / `admin-v*`
- ❌ 不真改 `config_prod.yaml`（写 diff 文档 OK）
- ❌ 不动 prod SSH（`PROD_SSH_*`）
- ❌ feature 分支不推 GitHub（pre-push hook 拦）

### NDF 不变量（10 项 — S0 §13 / S2 §8）
- I1: `credit_transaction.source_type` enum 不新增
- I2: `chk_ar_state_reason` 19 reason 不新增（C3 用现有 `cancelled` + terminal_metadata）
- I3: system prompt 6 段顺序不破坏
- I4: Hook chain 顺序（compliance → permission → budget → sandbox）
- I5: aiservice 唯一入口（**强制**：所有新 LLM 调用走 aiservice.Chat/Embed）
- I6: HookAction enum 5 个值不新增（Stop Hook → v2）
- I7: LoopEvent enum 19 个值不新增
- I8: controller 零业务逻辑
- I9: GORM `default:true` bool Create gotcha（C1 用 `*bool`）
- I10: feature 分支不推 GitHub

### Pre-push hook 拦截 + state.json
- 编辑 worktree 内代码前确认 `numind-server/.ndf/state.json` 显示 `active_feature: agent-mode-e2e-rollout` + `stage: S4`
- 如 state.json 被并行 session 清空 → 用本文件附录的 JSON 模板恢复

---

## 4. State.json 恢复模板（如被清空）

```bash
cat > /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server/.ndf/state.json <<'EOF'
{"version":"ndf-v2","active_feature":"agent-mode-e2e-rollout","active":{"id":"agent-mode-e2e-rollout","track":"standard","stage":"S4","created_at":"2026-05-21T18:00:00+0800","repos":["numind-server","numind-admin-web","numind-web-v3"],"worktrees":{"numind-server":"/private/tmp/wt-agent-mode-e2e-rollout-numind-server","numind-admin-web":"/private/tmp/wt-agent-mode-e2e-rollout-numind-admin-web","numind-web-v3":"/private/tmp/wt-agent-mode-e2e-rollout-numind-web-v3"},"branches":{"numind-server":"feature/agent-mode-e2e-rollout","numind-admin-web":"feature/agent-mode-e2e-rollout","numind-web-v3":"feature/agent-mode-e2e-rollout"},"review_policy":"double","blockers":[]}}
EOF
```

---

## 5. 启动 Session 2 的 sample 第一句话

> 继续 agent-mode-e2e-rollout #14/14 终局集成。先 `cat numind-server/.ndf/state.json` 确认 active；读 `numind-server/.ndf/feature-14-session-1-handoff.md`（S0-S3 完成 + Phase A 3/12）。下一步 Wave 2 M-A3 起：先创 `internal/pkg/middleware/agent_session_ctx.go`（SyncTurn 前置）。**绝对不影响 prod**。

---

## 6. 关键文件路径速查

- worktree numind-server: `/private/tmp/wt-agent-mode-e2e-rollout-numind-server/`
- worktree admin-web: `/private/tmp/wt-agent-mode-e2e-rollout-numind-admin-web/`
- worktree web-v3: `/private/tmp/wt-agent-mode-e2e-rollout-numind-web-v3/`
- branch: `feature/agent-mode-e2e-rollout` (3 repos 同名)
- S0: `numind-server/requirements/agent-mode-e2e-rollout.md`
- S1: `numind-server/proposals/agent-mode-e2e-rollout-proposal.md`
- S2: `numind-server/docs/superpowers/specs/2026-05-21-agent-mode-e2e-rollout-design.md`
- S3: `numind-server/docs/superpowers/plans/2026-05-21-agent-mode-e2e-rollout-plan.md`
- manifest 条目: `numind-server/.ndf/manifest.yaml` line 3-65（schema_version 后第一个 feature）

---

**Session 1 完结。Session 2 接力 Phase A Wave 2 起。**
