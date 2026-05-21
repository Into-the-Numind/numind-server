# Session Handoff — agent-mode-p0-tools

> 创建于：2026-05-22
> Feature: agent-mode-p0-tools (Standard track, S4 进行中)
> 目的：保证 context 受限时下一 session 能续作

## 当前状态快照

| 阶段 | 状态 | Artifact / Commit |
|------|------|------------------|
| S0 requirements | ✅ 完成 | `numind-server/requirements/agent-mode-p0-tools.md` (e02cdf95) |
| S1 proposal + PRD | ✅ 完成 | `numind-server/proposals/agent-mode-p0-tools-proposal.md` (5a1ac02b) |
| S2 spec | ✅ 完成 | `docs/superpowers/specs/2026-05-22-agent-mode-p0-tools-design.md` (ca9d9200) |
| S3 plan | ✅ 完成 | `docs/superpowers/plans/agent-mode-p0-tools-plan.md` (cd321e22) |
| Manifest stage | S3, total_tasks=9, completed_tasks=0 | `numind-server/.ndf/manifest.yaml` |
| S4 Wave 1 T3 | 🟡 in progress (background subagent) | feature/agent-mode-p0-tools |
| S4 Wave 1 T1/T2/T5 | ⏳ pending | — |
| S4 Wave 2 T4 | ⏳ blocked on T3 | — |
| S4 Wave 3 T6 | ⏳ blocked on T4 | — |
| S4 Wave 4 T7 | ⏳ blocked on T1+T2+T4+T5 | — |
| S4 Wave 5 T8 + T9 | ⏳ blocked on T7 | — |
| S5 验收 | ⏳ pending | — |
| S6 ndf-done | ⏳ pending | — |
| Dev 部署 | ⏳ pending | — |

## Worktree

- **Main numind-server worktree**: `/private/tmp/wt-agent-mode-p0-tools-numind-server`
  - Current branch: `feature/agent-mode-p0-tools`
  - Branched from develop (after S3 docs commits)
  - Both `feature/agent-mode-p0-tools` 和 `develop-p0-tools-workspace`（docs 推送用）共存
- **Main numind-web-v3 worktree**: `/private/tmp/wt-agent-mode-p0-tools-numind-web-v3`
  - Current branch: `feature/agent-mode-p0-tools`

## 继续推进指令（给下一 session 用）

### 0. Session 启动校验

```bash
# 1. 看活跃 NDF state
cat /private/tmp/wt-agent-mode-p0-tools-numind-server/.ndf-active 2>&1 || \
  echo "WARNING: .ndf-active 不存在，从 manifest 读 stage"

# 2. 看 manifest
git -C /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server show develop:.ndf/manifest.yaml | head -50

# 3. 看 feature 分支当前状态
git -C /private/tmp/wt-agent-mode-p0-tools-numind-server log --oneline feature/agent-mode-p0-tools -10
```

### 1. 必读 artifacts（顺序）

1. `numind-server/requirements/agent-mode-p0-tools.md` — S0
2. `numind-server/proposals/agent-mode-p0-tools-proposal.md` — S1
3. `docs/superpowers/specs/2026-05-22-agent-mode-p0-tools-design.md` — **S2 — 完整技术 spec，最重要**
4. `docs/superpowers/plans/agent-mode-p0-tools-plan.md` — S3 — 9 task plan

### 2. autopilot 规则（来自 [[feedback_agent_mode_autopilot]] 用户授权 2026-05-20）

- **不停顿推进** S0-S6
- **绝不碰 prod** (`/deploy-prod`, `git tag v*` 都不允许)
- **reviewer P0/P1 必修，但不算阻塞**——AI 自己修
- dev 部署后停一下，等用户确认 prod 时机

### 3. 剩余 task 清单（按依赖序）

**Wave 1（其余 3 个）** — 若 T3 已 merge 到 feature 分支：

#### T1 web_search backend
- Files: 
  - `internal/numind/biz/agent/tool_web_search.go` (new)
  - `internal/numind/biz/agent/tool_web_search_test.go` (new)
  - `internal/pkg/aiservice/web_search.go` (new) — 新加 `aiservice.WebSearch(ctx, req)` 顶层入口
  - `internal/pkg/aiservice/web_search_test.go` (new)
  - `config_local.yaml` / `config_dev.yaml` / `config_qa.yaml`: 加 `web_search:` 段
- 参考 spec §1.1 §7
- Provider: Tavily, https://api.tavily.com/search
- In-memory cache 5 分钟 TTL，max 1k 项
- Langfuse Span `tool.web_search.execute`

#### T2 web_fetch backend
- Files:
  - `internal/numind/biz/agent/tool_web_fetch.go` (new)
  - `internal/numind/biz/agent/tool_web_fetch_test.go` (new)
- 参考 spec §1.2 §8
- SSRF: net.IP.IsLoopback/IsPrivate/IsLinkLocalUnicast + 169.254.169.254 blocklist + .local TLD
- html-to-markdown: 用 `github.com/JohannesKaufmann/html-to-markdown` (添 go.mod)
- 30s timeout, 100KB 截断

#### T5 file_read backend
- Files:
  - `internal/numind/biz/agent/tool_file_read.go` (new)
  - `internal/numind/biz/agent/tool_file_read_test.go` (new)
  - `internal/numind/biz/agent/file_read_parsers.go` (new) — PDF/image/text parser 函数
  - `internal/numind/biz/agent/file_read_parsers_test.go` (new)
- 参考 spec §1.4 §9
- PDF: 走 aiservice.Chat with qwen-long (用 profile.SopText 或类似 taskID)
- Image: 走 aiservice.OCR
- text/markdown: httpclient 直读
- user_id check: parse URL path `agent-attachments/<user_id>/...` 比对 ctx user
- 200KB 截断

**Wave 2** — depends on T3 merged:

#### T4 ask_user_question tool + answer endpoint
- Files:
  - `internal/numind/biz/agent/tool_ask_user_question.go` (new)
  - `internal/numind/biz/agent/tool_ask_user_question_test.go` (new)
  - `internal/numind/biz/agent/answer.go` (new) — biz.AnswerPendingQuestion
  - `internal/numind/biz/agent/answer_test.go` (new)
  - `internal/numind/controller/v1/agent/student_run.go` (改：加 Answer handler + 路由 `/agent-runs/:id/answer`)
  - `internal/numind/controller/v1/agent/student_run_test.go` (改)
  - `internal/numind/store/agent_run.go` (改：加 UpdatePendingQuestion / ClearPendingQuestion / UpdateStateReason)
  - `internal/numind/store/agent_run_test.go` (改)
  - `internal/pkg/model/agent.go` (改：AgentRun struct 加 PendingQuestionJSON + PendingQuestionAt)
- 参考 spec §1.3 §2.3 §3.1 §4.1
- ask_user_question.Execute 返 `&yieldError{Payload: ...}` (sentinel from T3)
- answer endpoint: `POST /v1/agent-runs/:id/answer` body `{selected, free_text?}`
- biz.AnswerPendingQuestion: 校验 + inject message + clear pending + restart runner.Run with ExistingRunID

**Wave 3** — depends on T4:

#### T6 前端 QuestionPrompt + AgentChatView 集成
- Files (numind-web-v3):
  - `src/components/agent/QuestionPrompt.vue` (new) — 完整 SFC 见 spec §13.1
  - `src/components/agent/__tests__/QuestionPrompt.spec.ts` (new vitest)
  - `src/views/agent/AgentChatView.vue` (改) — SSE handler + render
  - `src/api/agent.ts` (改) — `postAgentAnswer(runId, payload)`
  - `e2e/agent-ask-user-question.spec.ts` (new Playwright)
- 参考 spec §1.3 §5 §13

**Wave 4** — depends on T1+T2+T4+T5:

#### T7 Tool registry + biz.go wiring + migrations
- Files:
  - `internal/numind/biz/agent/factory_platform.go` (改) — 列表追加 4 个工具 + 加依赖 fields
  - `internal/numind/biz/biz.go` (改) — Init 装配传新依赖
  - `internal/numind/biz/biz_test.go` (改)
  - `internal/numind/biz/agent/factory_platform_test.go` (改)
  - `migrations/20260522_153000_add_agent_run_pending_question.sql` (new)
  - `migrations/20260522_154500_seed_p0_tool_definitions.sql` (new, idempotent fallback)
  - `internal/pkg/model/agent.go` (改：如未在 T4 加，加 2 个字段)
- 参考 spec §11 §12 §4
- AutoMigrate 自动加列；migration SQL 作 fallback

**Wave 5** — final:

#### T8 architecture-v1.md 工具清单 + runbook
- Files:
  - `docs/agent-mode/architecture-v1.md` (改) — 工具清单 8→12（**当前未入 git，工作目录 untracked，T8 决定不入 git 保持本地草稿**）
  - `docs/agent-mode/runbook.md` (改) — 4 新工具运维
- 参考 spec §15

#### T9 S5 validation strategy
- File: `docs/agent-mode/agent-mode-p0-tools-validation-strategy.md` (new)
- 参考 plan §5 T9 章节内已有完整内容，直接抄

### 4. 每 task 完成后必做（per NDF rule 6）

1. `task lint` (numind-server) / `npm run lint && npm run type-check` (web-v3) PASS
2. `go test ./changed-package/...` / `npm run test:unit` PASS
3. **并行** dispatch 2 个 sonnet reviewer subagent:
   - Spec Compliance Review (templates/ndf/review-spec-compliance.md)
   - Code Quality Review (templates/ndf/review-code-quality.md)
4. P0/P1 inline 修复（P2 能现修则现修）
5. 更新 `numind-server/.ndf/manifest.yaml` 的 `progress.completed_tasks` 和 `reviewed_tasks` 各 +1
6. Commit message 用 Conventional Commits 风格

### 5. S5 验收

- 启本地 server `cd numind-server && task dev`
- 启本地 web `cd numind-web-v3 && E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run dev`
- 跑 Playwright `npm run test:e2e`（至少 ask_user_question.spec.ts）
- 跑 gstack /qa 浏览器 smoke 5 路径 P1-P5（见 plan §5 T9）
- 启本地 Langfuse `docker compose -f docker-compose.langfuse.yml up -d`，验证 trace

### 6. S6 ndf-done 等效（手动）

```bash
# 在 feature worktree 内
cd /private/tmp/wt-agent-mode-p0-tools-numind-server

# 1. 最后 fetch + rebase on develop
git fetch origin develop
git rebase origin/develop

# 2. checkout develop 分支并 merge feature
git -C /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server fetch origin develop:develop  # 主 repo 同步
# 用 develop-p0-tools-workspace worktree 操作：
cd /private/tmp/wt-agent-mode-p0-tools-numind-server
git checkout develop-p0-tools-workspace
git pull --rebase origin develop
git merge --no-ff feature/agent-mode-p0-tools -m "Merge feature/agent-mode-p0-tools (agent-mode-p0-tools完成)"
git push origin develop-p0-tools-workspace:develop

# 3. 删 feature 分支
git branch -D feature/agent-mode-p0-tools

# 4. 删 worktree (numind-server)
git -C /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server worktree remove /private/tmp/wt-agent-mode-p0-tools-numind-server --force

# 同步 numind-web-v3
cd /private/tmp/wt-agent-mode-p0-tools-numind-web-v3
git checkout develop-p0-tools-workspace-web
git pull --rebase origin develop
git merge --no-ff feature/agent-mode-p0-tools -m "Merge feature/agent-mode-p0-tools"
git push origin develop-p0-tools-workspace-web:develop
git branch -D feature/agent-mode-p0-tools
git -C /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-web-v3 worktree remove /private/tmp/wt-agent-mode-p0-tools-numind-web-v3 --force

# 5. 更新 manifest
# stage: S6 → completed
# completed_at: <now>
# merged_commit: <merge SHA>
# commit to develop with `docs(ndf): agent-mode-p0-tools completed`
```

### 7. Dev 部署

```bash
# numind-server
/deploy-dev server

# numind-web-v3
/deploy-dev
```

部署 OK 后**停一下**等用户确认 prod 上线时机。

## 关键决策记录（S1-D1..D8 + S2-D1..D7）

见 `numind-server/.ndf/manifest.yaml` 的 `decisions` 列表，15 个决策项已 lock。

## 已知风险

1. **architecture-v1.md 未入 git**（本地工作树 317KB 草稿）— T8 不修改它
2. **跟 `agent-mode-configurator-relocate` 并发**（S0，他们改 web-v3 `/views/config/`，我们改 `/components/agent/` + `/views/agent/`，无交集）
3. **Tavily quota 1k/月免费**——dev 测试要节制；产线建议付费 plan
4. **ask_user_question 无 timeout**——长期 stuck run admin 手工 cancel
5. **runner.go yield handler 集成**：T3 加的代码块在 ReAct loop 内 tool exec 失败处理前。T7 wire 时需校验 r.runStore.UpdatePendingQuestion 方法已加（T4 提供）

## context-handoff 触发条件

如下一 session 看到此文档时：
1. 跑 §0 验证 NDF state
2. 跑 §1 必读 4 个 artifact
3. 按 §3 顺序继续未完成 task
4. **不要重做已完成 task**（看 manifest progress + git log）
5. 完成所有后写中文验收总结（用户原始指令）
