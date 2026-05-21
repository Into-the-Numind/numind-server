# NDF S0 Requirement Card · `agent-mode-e2e-rollout`

**Track**：Standard
**Feature ID**：`agent-mode-e2e-rollout`（14-feature 分解 **#14/14** — 终局集成）
**起草日期**：2026-05-21
**起草人**：AI（autopilot）
**状态**：S0 草案
**仓库**：跨 3 仓库 — `numind-server` + `numind-admin-web` + `numind-web-v3`
**依赖**：#1-#13 全部 merged（develop 当前 HEAD `490c752a` 含 micro-fix `7d1737d4`）

| # | Feature | Merge Commit |
|---|---------|--------------|
| 1 | `agent-mode-phase0-verification` | `ebe4217f` |
| 2 | `agent-mode-runtime-skeleton` | `45770bb5` |
| 3 | `agent-mode-tool-registry` | `e0ae5da9` |
| 4 | `agent-mode-sandbox-integration` | `8c883533` |
| 5 | `agent-mode-skill-system` | `e05498b6` |
| 6 | `agent-mode-permission-pipeline` | `65e9d144` |
| 7 | `agent-mode-memory-system` | `49c8ab67` |
| 8 | `agent-mode-narration-layer` | `124e62b4` |
| 9 | `agent-mode-compact` | `2f90156c` |
| 10 | `agent-mode-configurator-ux` | `fdebd7b` (admin-web) |
| 11 | `agent-mode-student-ux` | `a02a442` (web-v3) |
| 12 | `agent-mode-billing-integration` | `bd988fd5` |
| 13 | `agent-mode-compliance-3layer` | `e8912f36` |
| micro | `fix-credit-stub-agent-test` | `7d1737d4` |

**阻塞**：无下游（#14 是终点）；**user 手动 prod 部署** 是 #14 完成后的下一步（不在本 feature 范围）

---

## 1. 起因（Why now）

13 个 feature 已落地 Agent 模式的所有横向能力：runtime skeleton（#2）、tool registry（#3）、Daytona sandbox（#4）、skill system + 9 API（#5）、permission pipeline（#6）、memory L1+L2（#7）、narration（#8）、compact（#9）、admin-web UI（#10）、student-web UI（#11）、billing 4-dim BudgetTracker + admin_test 池（#12）、L0+L1+L2 compliance + 注入检测（#13）。

**当前问题**：每个 feature 都明确把 "真实 LLM 调用"、"E2E 端到端验证"、"管理端最后 5% UI"、"dev 部署 + migration 顺序" 推迟到 **#14 终局集成**。具体未完成项分散在 13 份 S5 acceptance 文档的 §6 Known gaps / §6 Out of scope / §5 follow-up 节，**没有任何一个 feature 单独能让 Agent 模式真正跑起来**。

举例（按 13 个 S5 doc 整理出的 #14 待办）：

| 来源 | 待办项 |
|------|--------|
| #2 runtime-skeleton §"关键 follow-ups" #1 | `_ = einoAgent` 短路要替换为真实 Eino ReAct loop |
| #2 follow-ups #5 | Withhold MaxPTLRetries / Reactive Compact mock noop 在 #9 已落地，需在 runner.go 真实接通 |
| #6 permission §5.1 | 真实 LLM Classifier（异步 qwen-turbo）—— `#14 agent-mode-e2e-rollout` |
| #7 memory §10 | 真实 SyncTurn 接入；真实 `aiservice.Embed`（替换 mockEmbedder 1024 维零向量）；tools_section placeholder 填充 |
| #8 narration §7 N2 | 真实 LLM fallback（stub-only）—— `#14` |
| #9 compact 文件注释 | `biz.go MockCompactProvider // TODO(#14)` |
| #10 configurator §"已知 v1 限制" #2/#3/#4/#5 | 监控后台无真实数据源；试聊 toast 占位；Langfuse trace 跳转；强制取消 agent_run 后端 |
| #11 student-ux §2.5 | 8 个 e2e case 已写但本 session 未跑 dev server，留给 #14 dev 部署后跑通 |
| #12 billing §6 v1 limitations | PostToolCall tokens ctx 数据流；MaxTurnsPerRun 字段；migration 文件不自动跑 |
| #13 compliance §5 | mock LLM classifier 永远返回 false，真实 qwen-turbo 集成在 #14 |

**核心矛盾**：13 个 feature 是"分头并行落地"的产物，每个 feature 在自己范围内单测齐全（30+ 包 race PASS、累计 covera ge 80-99%），但**没有任何路径把所有 hook + LLM 调用串起来端到端跑过**。从配置者点 [创建 Agent] 到学员收到 final answer，期间穿过 narration / permission / budget / compliance / sandbox 5 层 hook + 3 个 LLM 调用点（chat / embed / compact），目前所有这些都是 mock。

**解决方案**：

- **Phase A**：把 6 个 mock 切到真实 `aiservice.Chat` / `aiservice.Embed` 入口；不裸 HTTP，不绕过 Langfuse trace + billing reconcile + 路由降级
- **Phase B**：写 8 个 Playwright e2e 串起 3 仓库（admin-web 创建 → web-v3 对话 → 真实 LLM ReAct + tool call + narration + memory + compact + permission + budget + compliance 全链路）
- **Phase C**：补齐 admin-web 5 个 v1-limitations（compliance CRUD UI / Langfuse 跳转 / agent-run 强制取消 / 监控真实数据 / 移除 v1 不联机 banner）
- **Phase D**：dev 部署（含手工 SSH 跑 13 个 migration）+ 端到端 smoke test
- **Phase E**：prod 部署准备文档（user 手动决定时机，#14 不真部署）

**为什么 #14 必须 Standard 而非 Hotfix**：5 条 Hotfix 标准至少违反 4 条（详见 §6 Triage）。

---

## 2. 业务范围

### In scope（5 大 Phase）

#### Phase A：Mock → 真实 LLM/Embedder/Classifier 切换（numind-server 后端）

> **关键约束**：所有 LLM 调用**必须**走 `aiservice.Chat(ctx, taskID, req)` / `aiservice.Embed(ctx, req)` 统一入口（CLAUDE.md `.claude/rules/ai-service.md §0` 硬规则）。**禁止**裸 HTTP / 直接 import provider 包 / 绕过 Langfuse + billing + 路由。

| # | 替换点 | 当前 stub | 替换为 | 文件 |
|---|--------|---------|---------|------|
| A1 | Adapter Generate（runner.go Eino ReAct）| `_ = einoAgent` 短路（#2）| 真实 ReAct loop（Eino `Generate`）调 aiservice via adapter `model.ToolCallingChatModel` 接口 | `internal/numind/biz/agent/runner.go` + `adapter_full_to_eino.go` |
| A2 | Memory embedder | `mockEmbedder` 返回 1024 维零向量（#7）| `aiservice.Embed(ctx, req)` 调真实 `text-embedding-v4` / `doubao-embedding` | `internal/numind/biz/memory/retrieval.go`（embedder 接口）+ wire 在 `biz.go` |
| A3 | Memory SyncTurn | `provider.SyncTurn` stub return nil（#7）| 真实写入：调 aiservice.Chat 做"对话回合摘要" + 写 L1 `agent_session_memory` 表 | `internal/numind/biz/memory/provider.go` + 集成在 runner.go Run 结尾 |
| A4 | Compact provider | `MockCompactProvider.PlaceholderSummary`（#9）| `aiservice.Chat` 调用 `BASE_COMPACT_PROMPT` 真实压缩（蓝本 §4.8.2）| `internal/numind/biz/compact/provider.go` |
| A5 | Narration LLM fallback | stub（#8 N2，YAML miss 时返回硬编码 fallback）| `aiservice.Chat` qwen-turbo 动态生成中文 narration | `internal/numind/biz/narration/translator.go` 的 `LLMFallback` 实现 |
| A6 | Injection classifier | mock 永远返回 false（#13）| `aiservice.Chat` qwen-turbo classifier（异步、超时 fallback 到关键词命中即拒）| `internal/numind/biz/compliance/injection_detector.go` |
| A7 | Permission L3 auto-mode classifier | mock（#6）| `aiservice.Chat` qwen-turbo classifier（异步、超时 fallback warn-allow）| `internal/numind/biz/permission/validators/`（L3 auto-mode validator）|
| A8 | PostToolCall tokens 数据流 | aiservice adapter 未写 ctx token（#12 §6 #1）| adapter 在 PostToolCall 注入 ctx token usage → BudgetTracker.RecordUsage 拿到真实数 | `internal/numind/biz/agent/adapter_full_to_eino.go` + `biz/budget/tracker.go` |

**Phase A 验收**：
- 所有 8 个切换点单测 + 集成测 PASS（用 mock aiservice for test）
- biz/agent 整包覆盖率不下降（≥ 80%）
- biz/compact / biz/memory / biz/narration / biz/compliance / biz/permission 各包覆盖率不下降
- `go test -race ./...` 全 PASS
- A1-A8 全部在 dev 环境用真实 dev qwen-turbo + dev billing 跑通（不真扣用户积分；single dev test user）

#### Phase B：Playwright E2E 端到端（admin-web + web-v3）

> **关键约束**：分布式 e2e — admin-web spec 在 admin-web 跑（端口 5174），web-v3 spec 在 web-v3 跑（端口 5173）。共用 `e2e/helpers/auth.setup.ts` 复用登录态。凭据走环境变量 `$E2E_USERNAME` / `$E2E_PASSWORD`（管理员=父账户）+ `$E2E_STUDENT_USERNAME` / `$E2E_STUDENT_PASSWORD`（学员=子账户）。子账户凭据可能未在 `.claude/settings.local.json` 配置 → 启动 Phase B 前向用户确认。

8 个核心流程：

| # | 流程 | 涉及 hook 链 | 验证不变量 |
|---|------|------------|-----------|
| B1 | 管理员创建 Agent（admin-web）| skill_system | 模板派生 → 12 题问卷 → 保存 → AgentDefinition.v1 写入 → SKILL.md 自动组装 |
| B2 | 学员对话流程（web-v3）| compliance → permission → budget → sandbox → narration → memory → compact | 选 agent → 发消息 → narration SSE 进度 → tool call → final answer → cost transparency |
| B3 | Permission deny 路径 | permission (HookActionDenied) | 触发 IsDestructive 工具（如 bash rm）→ `terminal_reason=permission_denied` → narration 显示 "操作被安全策略拦截" |
| B4 | Budget 超限路径 | budget (HookActionBudgetExceeded) | 累计 token 超 MaxCredits → BudgetTracker.RecordUsage 触发 → `terminal_reason=error_max_budget` + terminal_metadata JSON 写入 |
| B5 | Compliance hard rule 拦截 | compliance (L1 forbid_phrase) | 触发 PII / 竞品话题 → CheckUserInput deny → compliance_audit_log 写入 + 学员看到友好拒绝话术 |
| B6 | Compact 触发 | compact (PTL chain) | 长对话堆到 95% context → PTL 链触发 → CompactSummary 写入 agent_run.compact_summary + 后续 turn 用压缩态 |
| B7 | 会话恢复 | compact restore | 学员换 tab 重连 → cleanseMessages + 读 CompactSummary + restore → 继续对话不丢上下文 |
| B8 | 历史版本回滚 | skill_system | admin-web 改 Agent v3 → v4 → 回滚到 v1 → v5 出现 → web-v3 学员看到回滚后版本 |

**Phase B 验收**：
- 8 个 e2e spec 全 PASS（dev 环境）
- 截图 / video artifact 保留
- 禁词扫描（学员可见字符串无 stack trace / provider 名 / HTTP 状态码）

#### Phase C：Admin-web UI 补全（numind-admin-web）

#10 configurator-ux S5 §"已知 v1 限制" 5 条全清：

| # | 项 | 实施 |
|---|----|-----|
| C1 | compliance_rule CRUD UI | 新页面 `/admin/compliance-rules`：DataTable + 4 rule_type（forbid_brand / forbid_phrase / scope_filter / topic_classification）+ CRUD + 启用/禁用；后端复用 #13 既有 store 增 admin endpoints |
| C2 | Langfuse trace 跳转 | AgentMonitoring 列表 / agent-run 详情页加 "查看 Trace" 链接：基于 `agent_run.trace_id` 拼 Langfuse 后台 URL（从 `config_dev.yaml/langfuse.base_url` 读取）|
| C3 | Agent_run 强制取消 | UI：监控页 action 列加 [强制取消] 按钮 + ConfirmModal；后端：`POST /v1/admin/agent-runs/:id/cancel` 写 `cancellation_requested_at` + AbortController 三层 cancel |
| C4 | 监控真实数据源 | 后端新增 `GET /v1/admin/agent-runs?status=running&page=&page_size=` + 前端 30s 轮询；替换 #10 v1 空数据骨架 |
| C5 | NoticeBanner "v1 不联机" 移除 | `AgentMonitoring.vue` 删 NoticeBanner（联机已可用）|

**Phase C 验收**：
- 5 项功能 admin-web UI 端到端验证（手动 + 至少 2 个 Playwright spec：C1 CRUD + C3 cancel）
- `npm run lint` + `npm run type-check` exit 0
- 后端新增 2 个 admin endpoints（C3 cancel + C4 list-running）单测 + 集成测覆盖

#### Phase D：Dev 部署 + 验证

| # | 操作 | 说明 |
|---|------|-----|
| D1 | Dev MySQL 跑 13 个 migration | SSH 到 `$DEV_SSH_HOST` 用 `sshpass -p "$DEV_SSH_PASS"`；migrations 按 timestamp 顺序：#1 phase0 (无) / #2 / #3 / #4 / #5 / #6 / #7 / #8 (无) / #9 / #10 (无 schema) / #11 (无 schema) / #12 / #13 / #14 (本 feature 新 migrations) |
| D2 | 后端部署 | `/deploy-dev server`（端口 9091）+ `/deploy-dev admin`（端口 9099）|
| D3 | 前端部署 | numind-admin-web `/deploy-dev`（端口 9100）+ numind-web-v3 `/deploy-dev`（端口 9200）|
| D4 | Smoke test | dev 环境跑 Phase B 8 个 e2e（`$DEV_SITE_URL` + 真实 dev LLM）|

**Phase D 验收**：
- `/healthz` 200（dev 后端 + admin）
- `docker logs` 无 panic
- 8 e2e in dev 全 PASS
- Langfuse trace 后台可见至少 1 个完整 ReAct 链

#### Phase E：Prod 部署准备文档（不真部署）

| # | 工件 | 内容 |
|---|-----|-----|
| E1 | `docs/agent-mode/deploy-checklist-feature-14.md` | 13 + 1 个 feature migration 顺序 + rollback 顺序 + 数据校验 SQL（含每个表 row count assert）|
| E2 | `docs/agent-mode/config-prod-diff.md` | config_prod.yaml 应增加的字段（aiservice route 配置 / sandbox backend / compliance defaults / langfuse prod keys）—— **写文档不真改 config_prod.yaml** |
| E3 | `docs/agent-mode/runbook.md` | oncall 操作手册：如何强制取消 agent / 如何查 compliance audit / 如何升降 budget 阈值 / Langfuse trace 查询 |
| E4 | `docs/agent-mode/architecture-v1.md` 更新 | §11 实施路线图 标注 14-feature 全部 v1 落地日期；版本号 → v1.0-final |
| E5 | `CLAUDE.md` 加 Agent 模式 § | 与 SOP / Chatbot 并列第 3 模态 |
| E6 | `numind-server/CLAUDE.md` 加 biz/agent/* 子包说明 | adapter / runner / hooks / 5 个 gate 子包（permission/budget/sandbox/narration/compliance）|
| E7 | Go-live checklist | 用户手动 prod 部署 step-by-step（含 git tag + /deploy-prod + smoke test 节点）|
| E8 | CHANGELOG v2.2.0 | "Agent 模式 v1 完整落地" + 14 features 列表 |

**Phase E 验收**：
- 8 个文档全部 commit
- 文档 review 后无 P0/P1（reviewer subagent）
- `git diff develop -- config_prod.yaml` exit code 1 / 0 lines（**0 实改**）

---

### Out of scope（明确推迟到 v2 / 用户手动 / 后续 feature）

| Out | 原因 |
|-----|-----|
| 真 prod 部署 | autopilot 规则：永远不调 `/deploy-prod`；用户手动决定时机 |
| 打 `git tag v*` / `admin-v*` | autopilot 规则：永远不打；prod 部署的前置由用户手动 |
| 真改 `config_prod.yaml` | autopilot 规则：写 diff doc 让用户审；user 手动落到 prod |
| 跨账户 memory 共享（脱敏后跨机构）| v2 蓝本 §4.5.6 |
| Skill 模板市场（社区飞轮）| v2 蓝本 §11 Phase 3 M6 |
| Daytona OSS → CubeSandbox 升级 | v2 蓝本 §11 Phase 3 M5 |
| 真实 SQL-AST 静态分析（scope_validator）| 当前 GORM Before-Query hook + 7 表白名单 fail-open 满足 v1；AST 解析推到 v2 |
| 23 个 Bash validator 全扩展 | 当前 8 P0 满足 v1；扩展到 23 个推后 backlog |
| MMR / LLM rerank（memory retrieval）| v2 蓝本 §4.5.2 |
| 跨实例 Daily aggregate Redis sync（#12 §6 #2）| v1 单实例 in-memory 满足；多实例 prod 推 v2 |
| `AdminTestExpireDaemon` cron 调度（#12 §6 #3）| v1 lazy-create 满足；cron 推 v2 |
| `MaxTurnsPerRun` 字段引入 agent_definition（#12 §6 #4）| v1 走 DefaultLimits.MaxTurns=50；字段化推 v2 |
| `task lint` 历史债清理（3 个 pre-existing issue）| 由独立 micro 处理（在 commit `0f75ecfe` 等多个 S5 doc 说明）|

---

## 3. 用户故事

### 配置者视角（父账户）

1. 我点击 admin-web "AI 助手" → "新建 Agent" → 选 "销售助手" 模板 → 填 12 题问卷 → 保存 → 系统弹窗 "试聊一下？" → 我点 "试聊" → 输入 "帮我分析这周成交客户" → **看到** narration 进度 ("正在查询知识库..." → "正在生成报告...") → 收到 final answer → 我点 👍
2. 我在 "AI 助手 → Agent 监控" 看到 3 个学员正在用我配置的 Agent → 其中 1 个 stuck 60s → 我点 [强制取消] → 弹 ConfirmModal → 确认 → 该会话 status 变 cancelled
3. 我打开 "AI 助手 → 合规规则" → 新增 1 条 forbid_phrase "竞品X" → 启用 → 5 分钟后学员问 "竞品X 怎么样" → 收到友好拒绝话术（来自我配的 Q11 越界话术）
4. 我点 "Agent 监控 → 某 run → 查看 Trace" → 浏览器跳到 Langfuse 后台 → 看到完整 ReAct 链 + token usage 明细

### 学员视角（子账户）

1. 我打开 web-v3 "AI 助手" → 选 "销售助手" → 看到 Q4 欢迎语 + 4 个 starters → 点 "帮我分析这周成交" → AI 开始工作，narration 显示进度 → 拿到结果 → 我点 👍
2. 我问了一个 "如何破解XX密码" → AI 拒答："这个问题有点超出我的能力范围"（compliance 拦截）→ 我换问题继续
3. 我跑了一个大任务积分快用完 → 60% 警告 → 80% 警告 → 100% Modal 弹出 → 我点 [续费加量包]
4. 我和 AI 聊了 50 轮长对话 → 系统在后台 compact → 我换 tab 再回来 → 上下文保留 → 继续对话不需要从头开始
5. 我在最近会话点 v3 历史回滚后的 Agent → 看到新的 v5 配置（管理员刚回滚）

### 管理员（运维）视角

1. 我看 Langfuse 后台 prod traces → 任一 trace 全链完整（trace → generation × N → span × M）→ 没有 orphan span
2. 我看 Grafana → `compliance_audit_log` 异步 drop count = 0 → audit pipeline 健康
3. 我看 dev/prod migration 文档 → 13 个 + 1 个 migration 顺序清晰 → 我能自己手工跑

---

## 4. 成功指标

### 工程指标

| 指标 | 目标 | 验证 |
|------|------|------|
| 8 个 mock 切换点真实接通 | A1-A8 全完成 | runner.go 无 `_ = einoAgent` short-circuit；biz/memory 无 mockEmbedder；biz.go wire 无 MockCompactProvider |
| `go test -race ./...` 全 PASS | 30+ 包 | 无 FAIL / 无 data race |
| biz/agent 覆盖率 | ≥ 80%（不下降）| `go test -cover ./internal/numind/biz/agent/` |
| 8 个 Playwright e2e | 全 PASS in dev | spec 文件 commit + dev 验收截图 |
| 0 prod 影响 | config_prod.yaml 零 diff / 不打 tag / 不调 `/deploy-prod` | git diff verification + commit log 检查 |

### 业务指标（v1 最终态）

| 指标 | 目标 |
|------|------|
| 端到端可演示 | 父账户登录 admin-web → 创建 Agent → 学员登录 web-v3 → 跑通 ReAct → 收到答案 |
| Langfuse trace 完整 | 每个 ReAct loop 一个 trace，含 N 个 generation（LLM 调用）+ M 个 span（tool call）|
| 5 层 hook 全活 | compliance / permission / budget / sandbox / narration 全部链路验证 |
| 父账户监控可用 | 看到运行中学员会话、强制取消、Langfuse 跳转、合规规则 CRUD |

---

## 5. 风险与缓解

| # | 风险 | 概率 | 影响 | 缓解 |
|---|------|------|------|------|
| R1 | 8 个 mock 切换点级联失败：A1 不通 → A2-A8 都跑不了 | 中 | 极高 | 先 A1（adapter Generate ReAct loop），单独单测 mock aiservice 验证；A2-A8 逐个并行 |
| R2 | 真实 LLM 调用产生 dev 环境意外成本 | 低 | 低 | 用 dev qwen-turbo（廉价）+ dev 单一 test user；不真扣用户积分（admin_test pool）|
| R3 | 13 个 migration 在 dev 重跑顺序冲突 | 中 | 中 | E1 写顺序文档；按 timestamp 顺序逐个 SSH 跑；每个 migration 跑前 SELECT 验证表/列存在状态 |
| R4 | 3 仓库 worktree merge 冲突 | 中 | 中 | 顺序：先 server（API 协议）→ admin-web + web-v3（消费 API）；不在主仓库目录编辑；3 仓库 develop 各自 pull rebase |
| R5 | aiservice 调用未走统一入口（裸 HTTP）| 低 | 极高 | 强制 import check：`grep -rE "http.Post|resty" internal/numind/biz/{agent,memory,compact,narration,compliance,permission}` 必须 0 命中；reviewer 强制查 |
| R6 | Langfuse trace 不完整（child span 失联）| 中 | 中 | 在 A1 adapter Generate 用 `langfuse.WithTrace(ctx, traceID)` 注入 + 每个 generation `WithGenParent`；写专门集成测验证 trace 树形态 |
| R7 | Compact 真实压缩后恢复出错（cleanseMessages 边界）| 中 | 中 | A4 + B7 联动测；e2e B7 必须验证 50+ 轮对话 compact 后正确恢复 |
| R8 | 子账户 e2e 凭据未配置 | 高 | 低 | Phase B 启动前 confirm 用户在 `.claude/settings.local.json` 配 `E2E_STUDENT_USERNAME` / `E2E_STUDENT_PASSWORD`；如未配置 → BLOCKED_NEEDS_USER |
| R9 | dev DB 中存在 stale Agent 数据导致 e2e 不稳定 | 中 | 低 | Playwright spec 用 setup hook 创建独立 test Agent，teardown 标记 inactive |

---

## 6. Standard Triage justification

NDF v2 §4.2 Hotfix vs Standard 5 条标准全审：

| # | 标准 | 是否满足 Hotfix 条件 |
|---|------|---------------------|
| 1 | 不涉及 DB schema 变更 | ❌ **违反** — Phase C C3/C4 admin endpoint 可能需新增 `agent_run.cancellation_requested_at` ALTER；Phase A A3 需在 `agent_session_memory` 加 sync_turn_ts 字段（待 S2 定）|
| 2 | 不涉及新增 API 端点 | ❌ **违反** — Phase C 至少 2 个新 admin endpoints（C3 cancel + C4 list-running）+ Phase C1 compliance_rule CRUD（至少 5 个 admin endpoints）|
| 3 | 不涉及新外部服务集成 | ⚠️ **部分** — aiservice 是既有，但 Langfuse trace 子链补全可能视作"集成深化" |
| 4 | 影响文件数 ≤ 3 | ❌ **大幅违反** — 跨 3 仓库 Phase A 8 文件 + Phase B 8 spec + Phase C 5 UI + Phase D-E 8 docs，总数 30+ |
| 5 | 不涉及支付/权限/会员等高风险业务逻辑 | ❌ **违反** — Phase A A7 是 permission L3 真实 classifier；A8 是 billing PostToolCall token 数据流（影响真实计费准确性）|

**5 条标准至少 4 条违反 → Standard 必走**。

---

## 7. 阶段产出与验收门槛

| Stage | 工件 | 验收 |
|-------|------|------|
| S0 | 本文档 + Sonnet reviewer PASS | 0 P0/P1 残留 |
| S1 | `proposals/agent-mode-e2e-rollout-proposal.md`（PRD + 技术方案 + API 合约草案）+ reviewer PASS | 5 大 Phase 技术方案落地 |
| S2 | `docs/superpowers/specs/2026-05-21-agent-mode-e2e-rollout-design.md`（文件级 diff 计划 + API 合约 + 测试策略）+ reviewer PASS | 每个 Phase 有 file-level 计划 |
| S3 | `docs/superpowers/plans/2026-05-21-agent-mode-e2e-rollout.md`（M-task 拆分 + Wave 分组 + disjoint files + S5 验证策略）+ reviewer PASS | M-task 全可独立验证；S5 策略已定 |
| S4 | M-task 全 commit + 每 task reviewer + race PASS + coverage 不下降 | 全部 M-task 完成 |
| S5 | acceptance doc + dev 部署 + 8 e2e PASS + 0 prod verification | 14 features 终局集成完整 |
| S6 | 3 仓库 manual merge develop + worktree 清理 + state.json 清空 | 14 features 100% merged |

---

## 8. NDF Rule 10 S5 验证策略（S0 预声明，S3 final）

- **方式**：组合 — Phase A 用 Go TDD + mock aiservice unit test；Phase B 用 Playwright e2e（跨 admin-web + web-v3）；Phase C 用 vitest + Playwright + Go controller test；Phase D 用 dev 部署 + smoke test；Phase E 用 reviewer subagent
- **理由**：跨后端 + 前端 + dev infra；Go 单测不够（需端到端）；只 Playwright 不够（compliance/permission/budget 边界需 Go 单测）；只 dev smoke 不够（无持久化回归保护）
- **回归保护诚实声明**：
  - Phase A 单测 → 永久回归
  - Phase B Playwright e2e → 永久回归（dev 重跑）
  - Phase C vitest + Playwright → 永久回归
  - Phase D dev smoke → **一次性**（每次 dev 部署后手动跑）
  - Phase E doc → 静态，由 reviewer 把关
- **NDF Rule 10 高风险判定**：触及 permission + billing（Phase A A7/A8），强制 Playwright e2e B3/B4 覆盖 → 满足规则

---

## 9. NDF Rule 11 Bug-from-Customer 强制复现测试

**不适用** — #14 是计划内的终局集成，不是客户报告的线上 bug。无 `test(qa):` / `test(repro):` commit 要求。

但有 1 个例外：#13 S5 §3 已知的 `internal/numind/controller/v1/credit` pre-existing build failure（stub 缺 `ReconcileAgentTest`）已在 micro `7d1737d4` 修复，#14 不再涉及。

---

## 10. 跨阶段假设清单

- [x] aiservice 已落地且支持 `aiservice.Chat(ctx, taskID, req)` / `aiservice.Embed(ctx, req)` — Phase A 前提
- [x] Langfuse SDK 已集成（`langfuse.CreateTrace` / `CreateGeneration` / `CreateSpan`）— Phase A A1 / A4 / A5 前提
- [x] DB 已有 13 个 features 的所有 schema migration 文件 — Phase D D1 前提
- [x] dev 环境 SSH 凭据 + dev MySQL 凭据已配置（环境变量）— Phase D 前提
- [x] dev qwen-turbo / 真实 LLM provider 已配 dev key（不会因 quota 报错）— Phase A 真实测试前提
- [ ] `$E2E_STUDENT_USERNAME` / `$E2E_STUDENT_PASSWORD` 子账户 e2e 凭据已配 — **Phase B 启动前 confirm**
- [ ] dev 环境 Langfuse 后台 URL 可访问（给 Phase C C2 跳转用）— **Phase D D1 时 verify**

---

## 11. 估算

- 总 task：M-task 数预估 **25-35 个**（Phase A 8 + Phase B 8 + Phase C 5 + Phase D 4 + Phase E 8 + S5 doc + S6 = ~35）
- 总 commit：预估 **40-60 个**（含 reviewer fix commits）
- 跨阶段 reviewer 累计预估：**18-25 轮**

---

## 12. 不变量

#14 必须保持的不变量（前 13 feature 累计建立）：

| # | 不变量 | 由谁 owner | #14 必须不破坏 |
|---|--------|-----------|--------------|
| I1 | `credit_transaction.source_type` CHECK constraint 枚举值 | #12 锁定 | 不新增 source_type 值 |
| I2 | `chk_ar_state_reason` CHECK 19 reason | #2 + #9 锁定 | 不新增 terminal_reason |
| I3 | system prompt 6 段装配顺序（PlatformBase / tenant_hard_rules / body / disclaimer / memory / tools_placeholder / SafetyFooter）| #5 + #6 + #7 + #13 锁定 | 仅可填 `toolsSectionPlaceholder`（#7 P2-3 决议） |
| I4 | Hook chain 顺序（外→内）：compliance → permission → budget → sandbox | #13 + #12 + #6 + #4 锁定 | 不重排序 |
| I5 | aiservice 唯一入口 | #2 + .claude/rules/ai-service.md | 所有新 LLM 调用走 aiservice |
| I6 | HookAction enum 5 个值 | #2 + #6 + #5 + #12 锁定 | 不新增 enum 值 |
| I7 | LoopEvent enum 19 个值 | #2 + #6 + #9 + #12 锁定 | 不新增 |
| I8 | controller 零业务逻辑 | CLAUDE.md 硬规则 | Phase C 新 controllers 仅参数绑定 |
| I9 | GORM `default:true` bool Create gotcha | `.claude/rules/database.md` §6 | Phase A/C 新 model 用 wantActive pattern |
| I10 | feature 分支不推 GitHub | pre-push hook | #14 feature 也不推 |

---

**S0 完结。等 reviewer。**
