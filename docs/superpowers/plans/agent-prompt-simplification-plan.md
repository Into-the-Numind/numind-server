# NDF S3 Plan · `agent-prompt-simplification`

**Status**: S3 · 2026-05-28
**关联 spec**: `docs/superpowers/specs/2026-05-28-agent-prompt-simplification-design.md`

---

## §1 任务拆分（6 个原子单元）

每个 task 在 commit 后系统可编译可运行，可被 reviewer 独立审查。

### T1 — DB migration + GORM model field

**目标**：新增 `agent_definition.system_prompt MEDIUMTEXT NOT NULL DEFAULT ''` 字段。

**改动文件（独占）**：
- `numind-server/migrations/20260528_140000_agent_definition_add_system_prompt.sql`（新增）
- `numind-server/internal/pkg/model/agent_definition.go`（加 1 个字段）

**验收**：
- 在 dev DB 跑 migration（feature-level 不跑，留给 S6 dev 部署阶段）
- GORM AutoMigrate 不变（项目不用 AutoMigrate）
- 字段 GORM tag：`gorm:"type:mediumtext;not null;default:''" json:"system_prompt"`
- struct field 顺序：紧邻 `CustomSkillBody` 之后

**依赖**：无

---

### T2 — errno + DTO + biz 校验

**目标**：后端 API 接受 `system_prompt` 字段、长度校验、错误码。

**改动文件（独占）**：
- `numind-server/internal/pkg/errno/agent.go`（加 1 个 `ErrSystemPromptTooLong`）
- `numind-server/internal/numind/biz/agent/agent_definition_service.go` 或 `skill_service.go`（实施者 grep `Create.*Agent.*` / `UpdateAgent` 找具体函数；加 64KB 长度校验）
- 同 service 的 Request/Response DTO 加 `SystemPrompt string`
- controller 层无需改（DTO 字段通过 ShouldBindJSON 透传）

**验收**：
- `go test ./internal/numind/biz/agent/...` 全过
- 单测：`TestCreateAgent_SystemPromptTooLong`
- create/update 两条路径都校验

**依赖**：无（不依赖 T1，因为 GORM struct 默认零值时也能跑测试）

---

### T3 — 新拼装函数 + 单测（核心）

**目标**：新增 `runner_prompt.go` 提供 V2 拼装；提供独立单元测试。

**改动文件（独占新增）**：
- `numind-server/internal/numind/biz/agent/runner_prompt.go`（新增，spec §4 给出完整代码）
- `numind-server/internal/numind/biz/agent/runner_prompt_test.go`（新增）

**测试用例**（必须覆盖）：
- `TestPromptSegments_Append_FiltersEmpty`
- `TestPromptSegments_Render_NoExtraNewlines`
- `TestBuildSystemPromptV2_FourSegments`
- `TestBuildSystemPromptV2_AllEmpty`
- `TestBuildInstitutionSection_EmptyParts`
- `TestBuildInstitutionSection_SpecialChars`（CJK / `\n` / 双引号）
- `TestBuildUserContextSection_NoneSet`
- `TestBuildUserContextSection_SomeSet`
- `TestBuildUserContextSection_DisclaimerOnlyImpossible`（确认 disclaimer 与 memorySystem 同进同退的不变量）

**验收**：
- `go test ./internal/numind/biz/agent/ -run TestPromptSegments\|TestBuildSystemPromptV2\|TestBuildInstitutionSection\|TestBuildUserContextSection` 全 pass
- 测试不引入新外部依赖

**依赖**：无

---

### T4 — Legacy 函数提取 + Runner 主流程分叉

**目标**：把旧 inline 拼装抽到 `runner_legacy_prompt.go`，runner.go 主拼装替换为 if/else 分叉。

**改动文件（部分独占）**：
- `numind-server/internal/numind/biz/agent/runner_legacy_prompt.go`（新增，spec §5 给出完整代码）
- `numind-server/internal/numind/biz/agent/runner.go`（修改 676-687 行附近）
- `numind-server/internal/numind/biz/agent/runner_prompt_test.go`（加 D11 边界测试 `TestRunner_V2PathNoSkills_DropsLegacyBody`，与 T3 同文件）

**实施步骤**：
1. 把 `runner.go:676-687` 那段 `req.SystemPrompt = platformBase + ... + footer` 替换为 spec §5 的 if/else 分叉
2. `BuildSystemPromptLegacy` 12 个参数完全对应原 12 段，字面拼接顺序不变
3. 跑 `go vet ./internal/numind/biz/agent/...` 确认编译

**验收**：
- `go test ./internal/numind/biz/agent/ -run TestRunner_` 全 pass
- 跑现有 `runner_test.go` / `runner_memory_test.go` 全 pass（这两文件已存在，回归保护）
- 跑 `runner_runstream.go` 相关测试也要过（其 SSE 路径也走同一拼装代码）

**依赖**：T1 + T3（需要 `ad.SystemPrompt` GORM 字段已存在，以及 BuildSystemPromptV2 / BuildInstitutionSection / BuildUserContextSection 已存在；缺 T1 时 runner.go 引用 `ad.SystemPrompt` 编译失败）

---

### T5 — 前端 AgentBuilder UI + API client + TS 类型

**目标**：AgentBuilder 表单加"行为指引"textarea，API/类型同步。

**改动文件（独占，全在 numind-web-v3 仓库）**：
- `numind-web-v3/src/views/config/agents/AgentBuilder.vue`（加 form field + 加 `system_prompt: ''` 到 initialFormState）
- `numind-web-v3/src/api/agentBuilder.ts`（CreateAgentPayload / PatchAgentPayload 加 system_prompt 字段）
- `numind-web-v3/src/types/agent.ts` 或同名（AgentDefinition interface 加 system_prompt?: string；实施者 grep `interface AgentDefinition` 确认文件）

**UI 设计要求**：
- 实施者必须先读 AgentBuilder.vue 现有 form-item 样式约定，对齐相同 class
- 字段位置：在"欢迎语"后、"挂载技能"前
- maxlength = 16384（16KB 前端软限）
- 字符计数器显示
- placeholder：spec §8.2 给出例文

**验收**：
- `npm run lint && npm run type-check` 全过
- 手动验证：进 AgentBuilder 看到新 textarea，输入文字、保存、reload 看到回填

**依赖**：无（前后端跨仓库 Tier 2 disjoint，可与 T1/T2/T3 完全并行）

---

### T6 — 集成测试 + fixture diff（最关键的验收）

**目标**：跑真实老 agent 与新 agent 路径，对比 byte-for-byte。

**改动文件（部分新增，部分依赖已有）**：
- `numind-server/internal/numind/biz/agent/runner_prompt_integration_test.go`（新增）

**测试用例**：

1. **`TestPrompt_LegacyAgent_NoSystemPrompt_FixtureDiff`**
   - mock 一个真实 v2 老 agent（SystemPrompt="", 5 个 skills bound）
   - 跑 runner.Run 同样的 input
   - assert `req.SystemPrompt == golden_fixture`（fixture 在 testdata/ 里手工准备）

2. **`TestPrompt_V2Path_WithSkillsAndSystemPrompt`**
   - mock 新 agent（SystemPrompt="你是XX", 2 个 skills bound）
   - dump req.SystemPrompt
   - assert 含 4 段、含 "你是XX"、含 skills catalog、含 OutputToolsPriorityAddendum

3. **`TestPrompt_V2Path_SystemPromptOnly_NoSkills`**（D11 边界）
   - mock 新 agent（SystemPrompt="你是XX", 0 skills, GeneratedSkillBody="legacy junk"）
   - 验证 req.SystemPrompt **不**含 "legacy junk"（v1 legacy 被丢弃）

4. **`TestPrompt_V2Path_EmptyAfterTrim_FallsBackToLegacy`**（trim 边界）
   - SystemPrompt = "  \n\t  " 走 legacy

**fixture 准备方式**：
- 实施前先 commit T1+T2+T3+T4 改动
- 跑一遍 testbed agent，dump 拼装结果到 `testdata/legacy_agent_prompt.golden.txt`
- 把 fixture 也 commit 进去

**依赖**：T1 + T2 + T3 + T4（全部前置任务完成）

---

## §2 文件归属表（Tier 3 disjoint 校验）

| Task | 文件集 | 仓库 |
|---|---|---|
| T1 | `numind-server/migrations/20260528_140000_agent_definition_add_system_prompt.sql` `numind-server/internal/pkg/model/agent_definition.go` | numind-server |
| T2 | `numind-server/internal/pkg/errno/agent.go` `numind-server/internal/numind/biz/agent/agent_definition_service.go` （or similar） | numind-server |
| T3 | `numind-server/internal/numind/biz/agent/runner_prompt.go` `numind-server/internal/numind/biz/agent/runner_prompt_test.go` | numind-server |
| T4 | `numind-server/internal/numind/biz/agent/runner_legacy_prompt.go` `numind-server/internal/numind/biz/agent/runner.go` `numind-server/internal/numind/biz/agent/runner_prompt_test.go`（**追加 D11 边界测试**，串行依赖 T3，不参与并行 disjoint 校验）| numind-server |
| T5 | `numind-web-v3/src/views/config/agents/AgentBuilder.vue` `numind-web-v3/src/api/agentBuilder.ts` `numind-web-v3/src/types/agent.ts`（或同名） | numind-web-v3 |
| T6 | `numind-server/internal/numind/biz/agent/runner_prompt_integration_test.go` `numind-server/internal/numind/biz/agent/testdata/legacy_agent_prompt.golden.txt` | numind-server |

**并行策略**：

- **Wave 1**（Tier 3 同仓库 disjoint + Tier 2 跨仓库）：T1 / T2 / T3 / T5 同时并行
  - T1 / T2 / T3 在 numind-server 仓库，disjoint
  - T5 在 numind-web-v3 仓库，跨仓库天然 Tier 2
- **Wave 2**（依赖 T1 + T3）：T4 串行
  - T1 提供 `ad.SystemPrompt` 字段，T3 提供 BuildSystemPromptV2 等函数；二者缺一 T4 编译失败
  - T4 追加测试到 `runner_prompt_test.go`（T3 已创建），串行追加非并行写
- **Wave 3**（依赖前 5 个）：T6 串行

实施前跑 `numind-server/scripts/ndf/ndf-check-disjoint.sh` 验证 **Wave 1 内 T1/T2/T3 三套文件集**（T5 跨仓库不入校验）。**T4 不参与并行校验**（它对 `runner_prompt_test.go` 的追加是 Wave 2 串行步骤，与 Wave 1 T3 的初建版本不冲突）。

---

## §3 S5 验证策略（按 NDF Rule 10 必填）

### §3.1 验证方式

**主验证**：Go 集成测试 + Playwright E2E（轻量）

- Go 集成测试（T6 实现）：byte-for-byte fixture diff，最严验证
- Playwright E2E：1 个用例覆盖前端 UI 提交 → 后端 DB 持久化 → 重打开 AgentBuilder 回填

**不采用**：gstack `/qa`（本 feature 不改端用户交互，不需要浏览器视觉验证）

### §3.2 关键用户路径（具体步骤）

#### 路径 A：机构方填写新字段
1. 登录父账户（E2E_USERNAME / E2E_PASSWORD）
2. 进入 `/config/agents`，点击"新建助手"或编辑已有 agent
3. 进入 AgentBuilder，向下滚动到"行为指引"区
4. 在 textarea 输入 "你是 XX 公司销售助手。规则：用专业但亲和的语气。"
5. 保存
6. 重新 reload AgentBuilder 同 agent，确认 textarea 回填一致

#### 路径 B：超长 prompt 拒绝
1. 在 textarea 粘 70KB 文本
2. 保存
3. 前端先 maxlength 截断（16KB 后无法继续输入），不会触发后端
4. 用 curl 绕过前端，POST 70KB → 400 ErrSystemPromptTooLong

#### 路径 C：现有 agent 不破（最关键）
1. 选一个 dev 上已存在的 v2 agent（SystemPrompt="", 有 skills bound）
2. 跑一轮对话
3. 查 Langfuse trace，看 system prompt 文本结构（应该是 legacy 6+ 段）
4. 跟重构前的 trace 对比（如能找到历史 trace）

### §3.3 回归保护诚实声明

- T6 集成测试是**持久化回归保护**——未来 prompt 拼装代码改动 fixture 必须更新或测试 fail
- Playwright E2E 是**持久化回归保护**——AgentBuilder UI 后续重构必须保留 system_prompt 字段
- 不依赖 gstack /qa（无浏览器一次性验证需求）

---

## §4 manifest 条目

写入 `numind-server/.ndf/manifest.yaml`（T1-T6 实施期间 stage=S3；进 S4 后改 stage=S4；以此类推）：

```yaml
- id: agent-prompt-simplification
  ndf_version: '3.0'
  description: '机构方建 agent 时把所有 AI 设定塞到一个大文本框（system_prompt 字段）。重构 6+ 段 prompt 为 4 段（platform_head / institution / end_user_context / platform_safety_footer）。Legacy 路径 1:1 保留兼容老 agent。本期不串通 cache_control（拆出独立 feature）；不删 OutputToolsPriorityAddendum（保留工具路由引导）。'
  track: standard
  stage: S3
  last_updated_by: ai
  last_updated_at: '2026-05-28T...'
  repos:
  - numind-server
  - numind-web-v3
  branches:
    numind-server: feature/agent-prompt-simplification
    numind-web-v3: feature/agent-prompt-simplification
  worktrees:
    numind-server: /private/tmp/wt-agent-prompt-simplification-numind-server
    numind-web-v3: /private/tmp/wt-agent-prompt-simplification-numind-web-v3
  artifacts:
    requirement_card: requirements/agent-prompt-simplification.md
    proposal: proposals/agent-prompt-simplification-proposal.md
    spec: docs/superpowers/specs/2026-05-28-agent-prompt-simplification-design.md
    plan: docs/superpowers/plans/agent-prompt-simplification-plan.md
  progress:
    total_tasks: 6
    completed_tasks: 0
    reviewed_tasks: 0
  decisions:
  - 'D6: cache_control 拆出本 feature，独立 feature aiservice-prompt-cache-plumbing 后做'
  - 'D7: 旧拼装 1:1 搬到 BuildSystemPromptLegacy，不改语义'
  - 'D8: OutputToolsPriorityAddendum 与所有 toolsSection* 全保留，纠正 S1 误读'
  - 'D9: ## Memories header 在新旧两路径都按条件注入'
  - 'D10: 放弃 20% token 减少指标，改为 byte-for-byte 一致 + 新功能可用'
  - 'D11: V2 路径 + 无 skills 时丢弃 body（不混入 v1 legacy）'
```

---

## §5 进 S4 前 checklist

- [ ] manifest 写入 + stage=S3 标记
- [ ] 跑 `ndf-check-disjoint.sh` 验证 T1/T2/T3/T5/T6 文件归属无交集
- [ ] dispatch 6 个 implementer agent 时附 spec 路径 + 自己 task 段
- [ ] 每个 implementer commit 后主控验证 commit message + git diff
- [ ] 每个 task 完成后并行 dispatch 2 个 sonnet reviewer（spec-compliance + code-quality）
- [ ] reviewed_tasks 计数与 completed_tasks 一致才能进 S5
