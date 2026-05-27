# NDF S2 Spec · `agent-prompt-simplification`

**Status**: S2 v2 · 2026-05-28（吸收 S2 reviewer P0×2 修复 + 摸底现实校正）
**关联**: S0 `requirements/agent-prompt-simplification.md` / S1 `proposals/agent-prompt-simplification-proposal.md`

---

## §0 S2 阶段重要决策

### D6（保留）：cache_control 拆出本 feature

理由不变：aiservice 抽象层零 cache 支持、provider 行为未知。本 feature 只做结构重构，cache 串通走独立 feature `aiservice-prompt-cache-plumbing`。

### D7（保留）：保留旧拼装代码不重写

`runner.go:676-687` 旧拼装代码 1:1 搬到 `runner_legacy_prompt.go`，仅做"提取函数"重构，不改语义。

### D8（S2 reviewer P0 修复，**新决策**）：保留 `OutputToolsPriorityAddendum` 与所有 `toolsSection*` 内容

S2 reviewer 指出 `OutputToolsPriorityAddendum`（[`output_tools_priority_prompt.go:13-54`](#)）是中英双语共约 45 行的**工具路由策略引导**（Layer 1 Go 工具 → Layer 2 invoke_skill → Layer 3 run_python），删除会导致 LLM 退化到 `invoke_skill` 全量调度或 `run_python` 兜底全开，对成本与延迟都不利。同 runner 还有：

- `compactv2.ReadArtifactSystemPromptAddendum`（仅 V2 artifact 启用时注入）
- `attachmentReminderText`（仅有 fallback 附件时注入）

这三段都是**动态条件注入**，不是冗余的"工具说明"。本 feature **全部保留**。

**修正 S1 §3.2 中"删 toolsSectionPlaceholder 全段"的说法**——此为对 Claude Code 设计的误读，本 feature 不做此项。

### D9（S2 reviewer P0 修复，**新决策**）：`## Memories` header 在新 V2 路径中保留

`runner.go:649-657` 中 `memoriesSectionHeader = "\n\n## Memories\n"` 是条件注入（5 个 memory block 任一非空才注入）。新 V2 路径的 §3 端用户上下文继承此逻辑：在 runner 主流程中预先组装 `userContextWithHeader`，再传给 `BuildSystemPromptV2`。这样新老路径在 Memories 段行为完全一致。

### D11（S2 v2 reviewer P1 修复，**新决策**）：V2 路径 + 无 skills 时丢弃 body

新 V2 prompt 路径（`system_prompt != ""`）下：

- `len(skills) > 0` → `skillCatalog = body`（即 buildSkillCatalogBlock 输出）
- `len(skills) == 0` → `skillCatalog = ""`（丢弃 body；不把 v1 legacy `GeneratedSkillBody`/`CustomSkillBody` 注入 V2 prompt）

**理由**：机构方填了 `system_prompt`，即明确表态"我用大文本框定义这个 agent"，v1 legacy body 视为旧路径残留，新路径不再叠加。这样行为可预测且符合 user intent。

**边界覆盖矩阵**：

| SystemPrompt | skills | 走哪条路径 | 行为 |
|---|---|---|---|
| 空 | 空 | Legacy | 老 agent（无 v2 skill 无新 prompt）—— 跟重构前完全一致 |
| 空 | 非空 | Legacy | 老 v2 agent（有 binding skill 但还没填新 prompt）—— 跟重构前完全一致 |
| 非空 | 空 | V2 | 新 agent / 老 agent 升级时 system_prompt 唯一权威；v1 legacy body 丢弃 |
| 非空 | 非空 | V2 | 完整 v2 path：§2 = system_prompt + catalog + tools hint |

---

### D10（**新决策**）：成功指标修订

放弃 S1 提出的 "input_tokens 减少 ≥20%" 指标——D8 决定保留 tools section 后，token 数变化预期 < 5%，达不到 20% 阈值。

修订指标为：

- **结构清晰度（核心）**：机构方在 AgentBuilder UI 上一眼看到大文本框 + 简短引导，能在 5 分钟内配出可用 agent
- **不破现有 agent（必达）**：所有 `system_prompt == ""` 的 v2 agent，重构前后 `req.SystemPrompt` byte-for-byte 一致（S5 fixture diff）
- **新功能可用（必达）**：新 agent 填 `system_prompt`，跑一轮对话，Langfuse trace 验证 system prompt 含该字段内容

---

## §1 代码现状（摸底引用）

| 现象 | 文件:行 |
|---|---|
| Prompt inline 拼装 6+ 段 | `internal/numind/biz/agent/runner.go:676-687` |
| `memoriesSectionHeader` 条件注入 | `runner.go:649-657` |
| `toolsSectionPlaceholder` 三段累积 | `runner.go:539, 543, 559, 665` |
| `OutputToolsPriorityAddendum` 内容 | `internal/numind/biz/agent/output_tools_priority_prompt.go:13-54` |
| `compactv2.ReadArtifactSystemPromptAddendum` | `internal/numind/biz/agent/compactv2/` |
| `attachmentReminderText` | `internal/numind/biz/agent/runner_strip_retry.go` |
| body 段选择（v1 / v2 catalog） | `runner.go:449-501` |
| `buildSkillCatalogBlock` | `runner.go:1395-1413` |
| adapter system 注入 | `internal/numind/biz/agent/adapter.go:198-203` |
| ChatRequest 字段 | `internal/numind/biz/aiservice/types.go:150-184` |
| PlatformBasePrompt 常量 | `internal/numind/biz/agent/skill/constants.go:4-9` |
| PlatformSafetyFooter 常量 | `skill/constants.go:11-18` |
| AgentDefinition GORM model | `internal/pkg/model/agent_definition.go:12-33` |
| AgentBuilder Vue 表单 state | `numind-web-v3/src/views/config/agents/AgentBuilder.vue:106-116` |
| createAgent / patchAgent API | `numind-web-v3/src/api/agentBuilder.ts:30-51` |
| errno agent 错误码（约定参考） | `internal/pkg/errno/agent.go` |
| MEDIUMTEXT migration 样例 | `migrations/alter_sales_message_verdict_to_mediumtext.sql:3` |

---

## §2 DB Migration

**文件**：`numind-server/migrations/20260528_140000_agent_definition_add_system_prompt.sql`

```sql
-- agent_prompt_simplification S2: 新增 system_prompt 字段
-- 老 agent 默认空字符串，运行时 fallback 到 generated_skill_body / custom_skill_body
-- DB 列类型 MEDIUMTEXT（16MB 兜底），后端 biz 层校验 64KB 软上限

ALTER TABLE agent_definition
  ADD COLUMN system_prompt MEDIUMTEXT NOT NULL DEFAULT ''
  AFTER custom_skill_body;
```

执行：手工或随部署脚本一次性执行。回滚 `ALTER TABLE agent_definition DROP COLUMN system_prompt`。

---

## §3 GORM Model 变更

**文件**：`internal/pkg/model/agent_definition.go`（在 `CustomSkillBody` 字段下方）

```go
// SystemPrompt 是机构方在 AgentBuilder 写的"行为指引"大文本框内容。
// 非空时走新 4 段拼装（BuildSystemPromptV2）；空字符串时 fallback 到
// BuildSystemPromptLegacy（沿用现有 6+ 段拼装逻辑）。
// 上限：64KB（后端 biz 层校验），DB 列 MEDIUMTEXT 16MB 仅兜底。
SystemPrompt string `gorm:"type:mediumtext;not null;default:''" json:"system_prompt"`
```

无 GORM hook、不改 TableName、不动其它字段。

---

## §4 新拼装函数（V2 路径核心）

**文件**：`internal/numind/biz/agent/runner_prompt.go`（新增）

```go
package agent

import (
	"strings"

	"git.code.tencent.com/youshu/numind-server/internal/numind/biz/agent/skill"
)

// PromptSegment 一段 system prompt，附带语义标签。
// 未来切到 message-blocks + cache_control 时，可按 Name 决定 cache_control 注入位置。
type PromptSegment struct {
	Name string // "platform_head" | "institution" | "end_user_context" | "platform_safety_footer"
	Text string
}

// PromptSegments 多段容器。Render 拼成最终 system prompt 字符串。
type PromptSegments struct {
	Segments []PromptSegment
}

func (ps *PromptSegments) Append(name, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	ps.Segments = append(ps.Segments, PromptSegment{Name: name, Text: text})
}

// Render 用 "\n\n" 拼段。空段（已被 Append 过滤）不会出现，所以不产生多余空行。
func (ps *PromptSegments) Render() string {
	parts := make([]string, 0, len(ps.Segments))
	for _, s := range ps.Segments {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

// BuildSystemPromptV2 走新 4 段路径，仅在 ad.SystemPrompt 非空时调用。
//
// 段构成：
//   §1 platform_head            PlatformBasePrompt
//   §2 institution              ad.SystemPrompt + skillCatalog + toolsHint
//   §3 end_user_context         memoryHeader + agentMd + selector + dialectic + temporal + memoryDisclaimer + memorySystem
//   §4 platform_safety_footer   PlatformSafetyFooter
//
// 调用者（runner.go 主流程）负责把各 source 字段组装成 institutionSection / userContext，
// 此函数只做最终四段拼接 + segment 标签注入。
func BuildSystemPromptV2(institutionSection, userContext string) string {
	ps := &PromptSegments{}
	ps.Append("platform_head", skill.PlatformBasePrompt)
	ps.Append("institution", institutionSection)
	ps.Append("end_user_context", userContext)
	ps.Append("platform_safety_footer", skill.PlatformSafetyFooter)
	return ps.Render()
}

// BuildInstitutionSection 组装 §2 段内容：机构 system_prompt + skill catalog + tools hint。
// 使用 "\n\n" 拼内部子段，与 PromptSegments.Render 同分隔风格。空子段被过滤。
func BuildInstitutionSection(systemPrompt, skillCatalog, toolsHint string) string {
	parts := []string{}
	for _, s := range []string{systemPrompt, skillCatalog, toolsHint} {
		if t := strings.TrimSpace(s); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// BuildUserContextSection 组装 §3 段：memoriesHeader（条件）+ 5 个 memory block 拼接。
// 沿用旧路径行为：5 个 block 任一非空时挂 "## Memories" header；全空则整段为空。
func BuildUserContextSection(
	agentMd, selector, dialectic, temporal, memoryDisclaimer, memorySystem string,
) string {
	hasAny := agentMd != "" || selector != "" || dialectic != "" ||
		temporal != "" || memorySystem != ""
	if !hasAny {
		return ""
	}
	const memoriesHeader = "## Memories\n"
	return memoriesHeader +
		agentMd +
		selector +
		dialectic +
		temporal +
		memoryDisclaimer +
		memorySystem
}
```

**约束**：
- 不引入新外部依赖
- 不动 `aiservice.ChatRequest`
- 三个函数均纯（无 side effect），易单测

**§3 关键注意**：`memoriesHeader` 不带前导 `\n\n`（旧代码的 `"\n\n## Memories\n"` 中的 `\n\n` 是拼接时段间分隔符；新路径用 `PromptSegments.Render` 的 `\n\n` 完成段间分隔，所以 header 内只留 `"## Memories\n"`）。这保持外观一致性。

---

## §5 旧拼装代码搬家（Legacy 路径）

**文件**：`internal/numind/biz/agent/runner_legacy_prompt.go`（新增）

```go
package agent

// BuildSystemPromptLegacy 旧拼装路径，沿用 runner.go:676-687 的字面顺序。
// 当 ad.SystemPrompt == "" 时调用，老 agent 行为完全一致。
// 入参完整保留各 placeholder，调用者负责按现有逻辑预先组装。
func BuildSystemPromptLegacy(
	platformBase string,
	tenantHardRules string,
	body string,
	memoriesHeader string,
	agentMd string,
	selector string,
	dialectic string,
	temporal string,
	memoryDisclaimer string,
	memorySystem string,
	toolsSection string,
	platformFooter string,
) string {
	return platformBase +
		tenantHardRules +
		body +
		memoriesHeader +
		agentMd +
		selector +
		dialectic +
		temporal +
		memoryDisclaimer +
		memorySystem +
		toolsSection +
		platformFooter
}
```

**完全保持原字符串顺序与"无分隔符直接拼接"语义**，与 `runner.go:676-687` 字面一一对应。

`runner.go` 主拼装替换为（约 676-687 行）：

```go
if strings.TrimSpace(ad.SystemPrompt) != "" {
	// 新 V2 路径（system_prompt 非空 = 机构方已用大文本框定义 agent）
	//
	// body 的语义按 skills 是否有绑定来分支：
	//   - len(skills) > 0：body = buildSkillCatalogBlock 输出（v2 catalog）
	//   - len(skills) == 0：body = ad.GeneratedSkillBody / CustomSkillBody（v1 legacy）
	//
	// **决策（D11，见 §0）**：在新 V2 prompt 路径下，仅当 skills 非空时把 body 当作
	// skill catalog 拼到 §2 institution；skills 为空时丢弃 body（不把 v1 legacy 内容
	// 注入 V2 prompt）。理由：user 写了 system_prompt 即视为 agent 行为的唯一权威源，
	// 不再叠加 v1 legacy。
	var skillCatalog string
	if len(skills) > 0 {
		skillCatalog = body
	}
	institutionSection := BuildInstitutionSection(
		ad.SystemPrompt,
		skillCatalog,
		toolsSectionPlaceholder,    // 含 OutputToolsPriorityAddendum + (V2 artifact) + (attachment reminder)
	)
	userContext := BuildUserContextSection(
		agentMdBlock, selectorBlock, dialecticInsightBlock, temporalBlock,
		memoryDisclaimerBlock, memorySystemBlock,
	)
	req.SystemPrompt = BuildSystemPromptV2(institutionSection, userContext)
} else {
	// Legacy 路径，字面顺序与重构前一致；body 不论 v1/v2 都直接传入。
	req.SystemPrompt = BuildSystemPromptLegacy(
		skill.PlatformBasePrompt,
		tenantHardRulesPlaceholder,
		body,
		memoriesSectionHeader,
		agentMdBlock,
		selectorBlock,
		dialecticInsightBlock,
		temporalBlock,
		memoryDisclaimerBlock,
		memorySystemBlock,
		toolsSectionPlaceholder,
		skill.PlatformSafetyFooter,
	)
}
```

**注意**：V2 路径下 `body` 与 `toolsSectionPlaceholder` 仍来自现有 runner 主流程上方代码，不重新计算——只在最终拼装这一步分叉并按 D11 选择是否使用 `body`。

---

## §6 删除/收紧（保守版本）

S2 v1 计划的"删除冗余常量"被 D8 否决。本期**不删任何常量、不动 `OutputToolsPriorityAddendum` / `ReadArtifactSystemPromptAddendum` / `attachmentReminderText` 内容**。

唯一变化：新 V2 路径下，工具引导内容**位置变了**——从段 5（紧贴 footer 前）挪到 §2 institution 末尾（紧跟 skill catalog 后）。这个位置更符合"机构层的工具能力声明"语义。Legacy 路径不动。

`PlatformSafetyFooter` 本期**也不缩短**（5 行已经够短，缩短的边际收益小、语义敏感）。

---

## §7 后端 API 层

### §7.1 errno 新增

**文件**：`internal/pkg/errno/agent.go`（在最后一个错误码下方）

```go
// ErrSystemPromptTooLong is returned when agent.system_prompt exceeds the 64KB cap.
ErrSystemPromptTooLong = &Errno{HTTP: 400, Code: "InvalidParameter.SystemPromptTooLong", Message: "智能体行为指引文本过长（上限 64KB）"}
```

约定参考：`agent.go:18` 的 `ErrInvalidInput`：`{HTTP: 400, Code: "InvalidParameter.ToolInput", ...}`。

### §7.2 创建/更新校验

**文件**：找 agent 的 create/update biz 入口。

参考 `internal/numind/biz/agent/`：本 feature 实施者需 grep `agent_definition` 与 `Create.*Agent.*` 找到具体函数（多半在 `skill_service.go` 或 `agent_definition_service.go` 类似命名）。

校验逻辑：

```go
const SystemPromptMaxLen = 64 * 1024 // 64KB

if len(req.SystemPrompt) > SystemPromptMaxLen {
	return nil, errno.ErrSystemPromptTooLong
}
```

Create 与 Update 两条路径都加。

### §7.3 Request/Response DTO

`CreateAgentRequest` / `UpdateAgentRequest` / `AgentDTO` 都加 `SystemPrompt string \`json:"system_prompt"\`` 字段。空字符串为默认值。

---

## §8 前端 UI 变更

### §8.1 表单 state

**文件**：`numind-web-v3/src/views/config/agents/AgentBuilder.vue:106-116`

`initialFormState()` 返回值加 `system_prompt: ''`。

### §8.2 表单字段

**实施者必读**：S4 实施前先读 `AgentBuilder.vue` 现有 form-item 标记习惯（class 命名、layout、是否用公共组件如 `AppInput`），按其约定加新字段。**不要用 spec 这里凭空写的 class 名**。

字段位置：建议在"欢迎语"后、"挂载技能"前。逻辑组件结构：

```vue
<!-- 用现有项目的 form-item / label / textarea 类，本 spec 不规定具体 class -->
<form-item label="行为指引" hint="告诉它扮演什么角色、有什么职责、怎么说话">
  <textarea
    v-model="form.system_prompt"
    :maxlength="MAX_SYSTEM_PROMPT_LEN"
    rows="12"
    placeholder="例：你是【XX 公司】的销售助手。
职责：帮销售应对客户异议、提供推单话术。
规则：聊到价格永远不报具体数字、涉及投诉转人工、用专业但亲和的语气。"
  />
  <span>{{ form.system_prompt.length }} / {{ MAX_SYSTEM_PROMPT_LEN }}</span>
</form-item>
```

`MAX_SYSTEM_PROMPT_LEN = 16384`（16KB 前端软限，后端 64KB 留余量）。

### §8.3 API 函数 + TypeScript 类型

- `numind-web-v3/src/api/agentBuilder.ts`：`CreateAgentPayload` / `PatchAgentPayload` 类型加 `system_prompt?: string`。axios 调用本身不改。
- `numind-web-v3/src/types/agent.ts`（或 `agentBuilder.ts` 同文件）的 `AgentDefinition` interface 加 `system_prompt?: string`。

---

## §9 兼容性测试用例（S5 种子）

### §9.1 老 agent 不破（最关键）

| Test | 期望 |
|---|---|
| 真实老 agent（system_prompt 为空）跑 `runner.Run`，dump `req.SystemPrompt` | 与重构前同 agent 跑一次的 dump byte-for-byte 一致 |
| 真实老 agent 包含 5 个 memory block 任一非空 | "## Memories" header 仍按条件注入 |
| 真实老 agent 5 个 memory block 全空 | 无 Memories header |
| v1 chatbot 路径 | 完全不动 |

实现：S5 跑前先 dump fixture（重构前的 prompt 字符串存到 testdata/）；重构后跑同一 agent 比对。

### §9.2 新 agent 走新路径

| Test | 期望 |
|---|---|
| 新建 agent，system_prompt = "你是 XX..." | `req.SystemPrompt` 含 4 段 + tools hint 在 §2 末尾 |
| AgentBuilder 提交带 system_prompt | API 收到字段、DB 落库 |
| 提交 system_prompt = 70KB | 后端 400 ErrSystemPromptTooLong |
| 提交 system_prompt = "  \n\t  " | 走 legacy 路径（strings.TrimSpace == ""） |

### §9.3 单元测试

`runner_prompt_test.go` 新增：

- `TestBuildSystemPromptV2_HappyPath`
- `TestBuildSystemPromptV2_AllEmpty`
- `TestBuildInstitutionSection_EmptyParts`
- `TestBuildUserContextSection_NoneSet`
- `TestBuildUserContextSection_SomeSet`
- `TestBuildSystemPromptLegacy_MatchesOldInline`（fixture diff）
- `TestBuildInstitutionSection_SpecialChars`（systemPrompt 含 `\n` / CJK / 双引号）—— 防 fixture diff 因 escape 差异翻车
- `TestRunner_V2PathNoSkills_DropsLegacyBody`（D11 边界：SystemPrompt 非空 + skills 空时不注入 v1 legacy body）

---

## §10 测量字段（S5 用）

由于 D6 本期不串 cache，cache 字段不验证。只测：

| 指标 | 字段 | 期望 |
|---|---|---|
| input tokens 总量 | Langfuse `generation.usage.input_tokens` | 同 agent 重构前后变化 < 5% |

不要求"减少"，只验证"不大幅膨胀"。

---

## §11 风险更新（S2 v2）

| 风险 | 评估 | 缓解 |
|---|---|---|
| R1 fallback 字符序列差异 | 中 | D7 旧函数 1:1 搬家 + §9.1 fixture diff 验证 |
| R6 删 `OutputToolsPriorityAddendum` 退化 | **消除**：D8 不删 | — |
| R7 V2 路径 tools hint 位置改变影响 LLM 行为 | 低 | 内容相同，位置差异；LLM 对位置不敏感；S5 跑一轮对话验证主路径 |
| R8 64KB 上限超大 payload | 低 | §7.2 校验 |

---

## §12 S3 入场清单

S3 plan 必须明确：

1. **任务拆分**（建议 6 个）：
   - T1 DB migration + GORM model field（disjoint：migrations/ + model/agent_definition.go）
   - T2 errno 新增 + DTO 加字段 + biz 层校验（disjoint：errno/agent.go + biz/agent/ 相关 service）
   - T3 新拼装函数 + 单测（disjoint：新增 runner_prompt.go + runner_prompt_test.go）
   - T4 Legacy 函数提取 + Runner 主流程分叉（依赖 T3，单线程修 runner.go + 新增 runner_legacy_prompt.go）
   - T5 前端 AgentBuilder + API client + TS 类型（disjoint：仅 numind-web-v3）
   - T6 兼容性集成测试 + fixture diff（依赖 T1-T5）

2. **并行 Tier**：T1/T2/T3/T5 可 Tier 3 并行（跨 disjoint 文件集，跑 `ndf-check-disjoint`）；T4 串行依赖 T3；T6 最后串行。

3. **验证策略**（必填，参 NDF Rule 10）：
   - **Go 单测**：T3 函数纯，覆盖所有空段组合
   - **Go 集成测**：T6 跑真实 fixture + diff
   - **Playwright E2E**：机构方 UI 填 system_prompt → 保存 → 重新打开 agent → 字段保留
   - **不需要 gstack /qa**：本 feature 不改端用户交互链路

4. **manifest 条目**：写入 `numind-server/.ndf/manifest.yaml`，stage=S3 待 S4 切换

---

## §13 不变量（实施时必守）

- 老 v2 agent（system_prompt 空）行为 byte-for-byte 不变（D7 + §9.1 fixture diff）
- v1 chatbot 路径完全不动
- `aiservice.ChatRequest` 接口零变更
- 不引入新 dependencies
- 任一段为空时不在最终 prompt 留多余空行（`Append` 过滤空字符串）
- `OutputToolsPriorityAddendum` 内容仍出现在最终 prompt（D8）
- `## Memories` header 在两路径下都按条件注入（D9 + 新路径 BuildUserContextSection 内嵌条件判断）
