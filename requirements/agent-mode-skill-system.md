# NDF S0 Requirement Card · `agent-mode-skill-system`

**Track**：Standard
**Feature ID**：`agent-mode-skill-system`（14-feature 分解 #5/14）
**起草日期**：2026-05-22
**起草人**：AI（autopilot）
**状态**：S0 草案
**依赖**：#2 `agent-mode-runtime-skeleton`（merged `45770bb5`）+ #3 `agent-mode-tool-registry`（merged `e0ae5da9`）+ #4 `agent-mode-sandbox-integration`（merged `8c883533`）
**阻塞**：#10 `agent-mode-configurator-ux` / #11 `agent-mode-student-ux` / #13 `agent-mode-compliance-3layer`

---

## 1. 起因（Why now）

Agent 模式底座 14-feature 分解的 **#5/14** —— Skill 系统是 Agent 模式的"灵活性核心"（蓝本 §4.3）。

**核心矛盾**：LLM prompt 工程是技术工种，但 Numind 的配置者是完全非技术的机构父账户。

**解决方案**：把 prompt 工程翻译成业务问卷。

- 95% 配置者走问卷路径（12 题 → 自动生成 SKILL.md → 注入 system prompt）
- 5% 头部机构用高级模式直接编辑 SKILL.md 全文
- 历史版本回滚作安全网（避免改坏后无路可走）

**前 4 个 feature 完成度**：
- #1 V5 ADR 沙箱选型 ✓
- #2 Runtime skeleton（AgentRunner + 状态机 + AbortController） ✓
- #3 Tool Registry（FullTool 38 字段 + ToolFactory + 6 platform tools） ✓
- #4 Sandbox 集成（Docker pool + RunHooks 接入 + bash_exec 真实化） ✓

但 **Agent 仍然没法"成为某个角色"**——它无法从 DB 加载 Skill 配置组装 system prompt。这是 #5 解决的问题。

**1:1 约束**：每个 Agent 绑定唯一 1 个 Skill（蓝本 §4.3.2 设计决策）；多能力组合走"配多个 Agent"路径，不是"一个 Agent 路由多 Skill"。

---

## 2. 业务范围

> **关键术语翻译（P0-1 修复）**：蓝本 DDL 用 `tenant_id BIGINT`，Numind 当前数据模型**不存在** `tenant_id` 字段（无独立 tenant 概念）。本 feature 一律将 tenant 等价物映射为 `user.parent_user_id`（B2B2C 模型中的父账户 ID）。Skill 隶属"机构" = `parent_user_id` 指向某父账户。`agent_definition.parent_user_id BIGINT UNSIGNED NOT NULL`，平台预置内容放独立 `skill_template` 表（见下文 P2-3 修复）。

> **问卷 Q 编号 canonical（P0-3 修复）**：蓝本 §4.3.3/§4.3.4 与 §5.3 的 Q 编号不一致；以 **§5.3 为 canonical source of truth**。本 feature 实装的 12 题映射严格按下表（reconciled）：

| §5.3 Q# | 题目 | 映射 | SKILL.md 落点 | DB 字段 | 注入 LLM？|
|---|---|---|---|---|---|
| Q1 | Agent 名字 | 角色 | `## 角色定义` 中 name | `agent_definition.name` | 是 |
| Q2 | 头像 | UI | — | `agent_definition.icon_url` | 否 |
| Q3 | 一句话描述 | UI | — | `agent_definition.description` | 否（仅卡片）|
| Q4 | 欢迎语 | UI | — | `agent_definition.welcome_message` | 否（首条消息）|
| Q5 | 快速开始按钮 | UI | — | `agent_definition.starters` JSON | 否（conversation starters）|
| Q6 | 任务类型（多选）| 角色 | `## 核心职责` | `questionnaire_answers.q6` | 是 |
| Q7 | 学员材料类型（多选）| 工具 | `## 输入材料类型` + 引导 tool_flags 默认值 | `questionnaire_answers.q7` | 是 |
| Q8 | 每次积分上限（滑块）| 成本控制 | — | `agent_definition.credit_cap_per_session` | 否（BudgetTracker 强制）|
| Q9 | 网络搜索允许（radio）| 工具 | — | `agent_definition.tool_flags.web_search` | 否（tool 启用 flag）|
| Q10 | 注意话题（多行可选）| 软约束 | `## 禁区（软规则）` | `questionnaire_answers.q10` | 是 |
| Q11 | 超出范围话术（可选）| 软约束 | `## 越界处理策略` | `questionnaire_answers.q11` | 是 |
| Q12 | 说话风格（radio）| 风格 | `## 语气风格` | `questionnaire_answers.q12` | 是 |

注入 LLM 的题：Q1、Q6、Q7、Q10、Q11、Q12（6 题）。Q2-Q5 / Q8 / Q9 是纯 UI / 配置字段。

### In scope

1. **DB 层**
   - `agent_definition` 表 — `parent_user_id BIGINT UNSIGNED NOT NULL`（不是蓝本 `tenant_id`）+ name + description + icon_url + welcome_message + starters JSON + questionnaire_answers JSON + generated_skill_body TEXT + advanced_mode TINYINT + custom_skill_body TEXT + tool_flags JSON + credit_cap_per_session + daily_credit_cap + version + is_active + source_template_id + created_by + created_at + updated_at
   - `agent_definition_history` 表 — append-only snapshot（agent_id + version + snapshot JSON）
   - `skill_template` 表 — 平台预置模板独立存储（避免与用户 agent 同表混淆；DB 层无 parent_user_id 字段，CRUD 端点不暴露删除）
   - GORM model + AutoMigrate（注册到 `internal/numind/helper.go`） + migration SQL（双文件 含 _rollback.sql）

2. **biz/skill 子包**
   - `skill_builder.go`：问卷答案 → SKILL.md 自动组装（按蓝本 §4.3.4 公式，Q 映射严格按上表）
   - `versioning.go`：历史版本写入（每次保存触发 +1）+ 一键回滚（创建新版本，不删旧）
   - `templates.go`：10 个内置模板 seed（蓝本 §4.3.6 模板库），写入 `skill_template` 表
   - `service.go`：业务编排（创建 / 列表 / 详情 / 更新 / 历史 / 回滚 / 切高级模式）

3. **API 端点**（用户端 `/v1/agent/skills/*` —— **首次为 Agent 模式引入 HTTP 端点**）
   - POST `/v1/agent/skills` 创建（接受 questionnaire_answers，调 skill_builder 组装，写历史 v1）
   - GET `/v1/agent/skills` 列表（**仅父账户可访问**：从 JWT 取 userID，过滤 `parent_user_id = userID`；子账户调用直接返回 403；P2-2 修复）
   - GET `/v1/agent/skills/:id` 详情
   - PUT `/v1/agent/skills/:id` 更新（version +1，写历史）
   - GET `/v1/agent/skills/:id/history` 历史版本列表（**包含已软删除 agent**，P1-3 修复）
   - POST `/v1/agent/skills/:id/restore/:version` 回滚（创建新版本 = old_version_count + 1）
   - POST `/v1/agent/skills/:id/advanced-toggle` 切高级模式（不可逆；DB 层 advanced_mode=1 后拒绝改回 0；保留 questionnaire_answers 做历史回滚救援）
   - GET `/v1/agent/skill-templates` 内置模板列表（读 `skill_template` 表）—— **也走 user_token 鉴权**（P2-5 修复 — 配置类接口未登录用户不应访问，与其他端点保持一致）
   - 鉴权走 user_token middleware（所有 8 个端点统一）；controller 在 `internal/numind/controller/v1/agent/`（新目录）

4. **Runner 集成**
   - `AgentRunner.Run` 接受 `AgentDefinitionID`（在 RunRequest 加字段；保持向后兼容 — 字段为 0 时 fall through 到 #2 mock 行为）
   - 装配 system prompt 时调 skill.effective_body 注入 adapter（按蓝本 §4.3.9 顺序，平台固定段 `PLATFORM_BASE_PROMPT` / `PLATFORM_SAFETY_FOOTER` 简化为常量字符串放 `biz/skill/constants.go`，#14 完善多版本管理）

5. **Hook 信号传播改造**（P0-2 修复 — 范围精确化）

   **当前问题**：`adapter_full_to_eino.go:55` 把 HookAction 包装成 `fmt.Errorf("tool execution stopped by hook: action=%d", action)`，runner 收到的是普通 error，无法区分 hook stop vs 工具错误，所以 `TerminalHookStopped` / `TerminalStopHookPrevented` 即使在 state machine 中已实现也永远不会触发。

   **#5 改造范围**（**不要求**真实 LLM ReAct loop，那部分是 #14）：
   - (a) 在 `adapter_full_to_eino.go` 把 hook 返回的 `HookAction` 通过 ctx-bound 字段（`HookActionFromCtx(ctx) HookAction`）记录到本次 toolCall 的状态；可以是 `*atomic.Int32` 由 `RunRequest.Hooks` 持有的共享字段
   - (b) `runner.Run()` 当 `einoAgent.Generate()` 返回 error 时，查 last hook action：若是 Stop/BlockingStop → 调 `state.Transition(LoopEventHookActionStop/BlockStop)` → 写 `TerminalHookStopped` / `TerminalStopHookPrevented`；否则按现有 model_error 路径
   - (c) 单测：mock einoAgent 返回 hook error → runner 正确 dispatch state event → terminal_reason 正确（含 race-safe 验证）

   **不在 #5**：
   - 真实 LLM ReAct loop / Eino agent.Generate 全流程（#14）
   - 独立的 Stop Hook（query loop 完成时的 hook 类型，与 Pre/PostToolCall 不同；#14）
   - PreToolCall / PostToolCall 信号通道不动 #2 mock 测试（保留所有现有 测试通过；新增独立 race-safe 测试）

### Out of scope（明确划线）

- **管理端 UI**（Skill CRUD UI 在 #10 `agent-mode-configurator-ux` 落地；本 feature 仅 biz + 用户端 API）
- **学员端 UI**（试聊页面 / 历史版本回滚 UI / 模板画廊在 #10 + #11 落地）
- **权限 pipeline**（#6 — Skill 的 tool_flags 在 #5 仅做存储，权限检查在 #6 决定）
- **Memory 系统**（#7 — system prompt 的 memory.SystemBlock 段在 #7 注入）
- **Narration**（#8 — 工具显示 / 配置 narration 在 #8 落地）
- **Compact**（#9 — system prompt token 预算管理在 #9 落地）
- **试聊配额（5000 积分）grant 表 + 用户使用** — 完全推迟 #12；**本 feature 不引入新 source_type 枚举值**（P1-4 修复 — 占位不实装就是真的不动 `credit_transaction.source_type` CHECK constraint，避免破坏现有约束）
- **跨机构脱敏共享**（v2，蓝本 §4.3.10）— v1 不实装，但 `source_template_id` 字段已预留
- **prod 部署** — develop merge 后停（不打 git tag、不动 prod）
- **真实 LLM 跑通完整 ReAct loop** — runner 主循环改造仅含 hook 信号传播（见上 5.c），完整 ReAct loop 由 #14 落地

---

## 3. 验收条件（Definition of Done）

S6 ndf-done 准入门槛：

### 工件 + 测试

- [ ] `agent_definition` + `agent_definition_history` + `skill_template` 表 migration（含 _rollback.sql；3 张表）
- [ ] GORM model `AgentDefinition` + `AgentDefinitionHistory` + `SkillTemplate` 已定义（**含 GORM `default:true` bool Create 测试 — P1-2**：单测覆盖 Create 时 `is_active=false` 正确持久化，参照 `.claude/rules/database.md §6` UpdateColumn 两步法）
- [ ] AutoMigrate 在 `internal/numind/helper.go` 已注册（3 张表）
- [ ] `internal/numind/store/` 加 `IAgentDefinitionStore`（含历史快照读写 + 软删除后仍可查 history）
- [ ] `internal/numind/biz/skill/` 子包：skill_builder + versioning + templates + service + constants（PLATFORM_BASE_PROMPT 等）
- [ ] 8 个 HTTP 端点已实现 + 在 `router.go` 注册（用户端 user_token middleware）
- [ ] Controller 在 `internal/numind/controller/v1/agent/` 新目录
- [ ] `AgentRunner.Run` 接入 Skill body 注入（RunRequest 加 AgentDefinitionID 字段，0 时 fall through #2 mock）
- [ ] Hook 信号传播改造（adapter ctx-bound action + runner 终止 reason 正确派发）
- [ ] **单元测试覆盖：skill_builder 12 个映射场景**（蓝本 §5.3 canonical 12 题，每题至少一个 happy path；其中 6 注入题 + 6 非注入题边界）
- [ ] **单元测试覆盖：versioning** — (a) 软删除 agent → GET /skills/:id/history 仍返回所有历史版本；(b) 回滚到 v2 → 新版本号 = current_max+1，旧 v2 仍在历史列表；(c) 历史快照 JSON 含完整 questionnaire_answers + generated_skill_body（P1-3 修复）
- [ ] **单元测试覆盖：runner hook → terminal_reason 真实路径**（P1-1 修复）—
  - (a) PreToolCall 返回 Stop → `terminal_reason = "hook_stopped"`
  - (b) PreToolCall 返回 BlockingStop → `terminal_reason = "stop_hook_prevented"`
  - (c) PostToolCall 返回 Stop → 同 (a)
  - (d) 两路径在 `go test -race` 下无 data race（用 atomic 或 channel 验证）
  - (e) 测试边界：mock einoAgent，不调真实 LLM
- [ ] 集成测试：8 个 API 端点 happy path + 401/404/422 错误路径
- [ ] **集成测试：advanced_mode=1 后调用 PUT 试图改回 0 → DB 层拒绝**（验证不可逆）
- [ ] biz/skill 包覆盖率 ≥80%
- [ ] biz/agent 包覆盖率不下降（保持 80%+）
- [ ] `go test -race ./...` PASS
- [ ] `go vet ./...` exit 0
- [ ] `task lint` PASS

### 安全 + 合规

- [ ] 所有 LLM 调用（如 skill_builder 中如有用 LLM 协助生成）走 `aiservice` 统一入口（v1 skill_builder 是纯模板拼接，不调 LLM；如未来扩展自动总结再加 LLM）
- [ ] 所有数据库变更走 GORM query builder（不裸 raw SQL）
- [ ] 控制器层零业务逻辑（验证 → biz → 响应）
- [ ] API 端点全部 user_token 鉴权
- [ ] 验证：列表 API 限定 `parent_user_id = JWT.userID`；子账户 403（P2-2 修复）
- [ ] 验证：高级模式切换不可逆约束（DB 拒绝 advanced_mode=1→0 切换；只能创建新 Agent）
- [ ] 验证：历史版本永久保留（即使 agent 软删除）；history 表无软删除字段
- [ ] 验证：is_active=false Create 正确持久化（P1-2 修复 — UpdateColumn fixup 模式）
- [ ] 验证：`credit_transaction.source_type` CHECK constraint 零修改（P1-4 修复）

### 0 prod 影响

- [ ] `config_prod.yaml` zero diff
- [ ] 不打 git tag
- [ ] 不调 `/deploy-prod`
- [ ] feature 分支不推 GitHub（pre-push hook 拦）

---

## 4. 风险

1. **Skill body 注入对 token 预算的影响** — 风险：长 SKILL.md 吃掉学员 context 配额
   - 缓解：在 `skill_builder.go` 加 token 估算 + 长度警告字段；R2 估算纳入 system prompt token（#9 完善）

2. **历史版本表无限增长** — 风险：高频改 Agent 的机构表会爆
   - 缓解：定义保留策略上限（如每 Agent 最多 50 个 history rows，超过则归档）；v1 不实装归档，仅在 model 加 INDEX 避免慢查询

3. **高级模式不可逆+丢失 questionnaire_answers** — 风险：配置者切高级后无法回问卷
   - 缓解：DB 切高级时保留 questionnaire_answers 字段不清空（向前兼容），UI 不暴露入口（#10）；强制 dialog 警示在前端 #10 完成

4. **adapter / runner hook 信号传播改造引入回归** — 风险：现有 PreToolCall/PostToolCall 通过 `fmt.Errorf` 包装 error 的 `factory_sandbox_hooks_test.go` 等测试假设 error type
   - 缓解：(a) 引入 `HookActionFromCtx` 助手 + ctx-bound atomic.Int32 共享字段，**新增机制不删旧路径**（adapter 仍返回 error，但 ctx 中存储 action）；(b) 保留所有 #4 现有 sandbox hooks 测试不变；(c) 新增 runner-level 端到端 hook 信号测试

5. **tool_flags ↔ Tool Registry 协议不明确** — 风险：tool_flags JSON 结构与 #3 ToolDefinition.Source 字段口径不一致
   - 缓解：在 S2 spec 明确 tool_flags JSON shape；约束为 `map[string]bool` 即工具名 → 启用 flag；不存 source/version 等元信息（保存即生效原则）

6. **questionnaire_answers JSON schema 演进**（P2-4 修复） — 风险：v2 加题或改 Q 编号时，旧历史快照 unmarshal 失败
   - 缓解：(a) S2 定义 `QuestionnaireAnswers` Go struct 含所有字段 `omitempty` 标签；(b) skill_builder.go 对缺失字段返回 graceful default（如 Q10 缺失 → 跳过禁区段）；(c) 禁止 strict mode `DisallowUnknownFields`；(d) 历史快照永远是当时 schema 的完整冗余存储（不依赖最新 schema 解析）

7. **平台预置模板与用户 agent 数据隔离**（P2-3 修复） — 风险：若混入 `agent_definition` 表（蓝本暗示），CRUD 端点可能误删模板
   - 缓解：独立 `skill_template` 表（无 parent_user_id；不暴露删除端点；不出现在 `/v1/agent/skills` 列表，仅出现在 `/v1/agent/skill-templates`）

8. **`source_template_id` 跨表 FK 缺失** — 风险：`agent_definition.source_template_id` 指向 `skill_template.id`，但 v1 不加 FK constraint（避免 #14 删除 template 影响历史 agent 引用）
   - 缓解：在 S2 spec 明确：`source_template_id` 是软引用（无 FK），CRUD 端点不做 join 校验，仅在历史展示时尝试 lookup name，lookup 失败显示"[已删除模板]"

---

## 5. 简单时间线（参考）

S0（本卡） → S1 proposal/PRD → S2 spec → S3 plan → S4 编码（M1-M~10）→ S5 验收 → S6 ndf-done

每阶段独立 Sonnet reviewer，遵循 `feedback_review_each_stage`。

---

## 6. 相关文档

- 蓝本 §4.3 Skill 系统：`docs/agent-mode/architecture-v1.md`
- 蓝本 §5.3 12 题问卷 canonical：`docs/agent-mode/architecture-v1.md`
- 蓝本 §5.7 高级模式 UI：`docs/agent-mode/architecture-v1.md`
- 蓝本 §4.1.5 hook 终止 reason：`docs/agent-mode/architecture-v1.md`
- #2 验收：`numind-server/docs/superpowers/qa/2026-05-2X-agent-mode-runtime-skeleton-s5-acceptance.md`
- #3 验收：`numind-server/docs/superpowers/qa/2026-05-2X-agent-mode-tool-registry-s5-acceptance.md`
- #4 验收：`numind-server/docs/superpowers/qa/2026-05-2X-agent-mode-sandbox-integration-s5-acceptance.md`

---

**S0 完结。S1 写 proposal + PRD。**
