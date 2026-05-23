# Spec · agent-mode-v2-skill-invocation

**Status**: S2 Design
**Date**: 2026-05-24
**Author**: AI (NDF Standard track autopilot)
**Inputs**:
- [S0 Requirement card](../../../requirements/agent-mode-v2-skill-invocation.md)
- [S1 Proposal+PRD](../../../proposals/agent-mode-v2-skill-invocation-proposal.md)
- [Architecture v1 §4.3 Skill 系统 / §决策#11](../../agent-mode/architecture-v1.md) — 本 spec 颠覆决策#11 的 1:1 Agent-Skill 模型
- runner.go 当前实现 line 390-589（system prompt 6 段拼接）+ line 617-641（工具装配）

**Hard dependencies (must land develop before S4)**:
- v2 #1 `agent-mode-v2-skill-as-artifact` 提供以下接口（S3 plan 起草前 git fetch + grep 验证）：
  - DB 表：`skill`（id, parent_user_id, name, description, when_to_use, allowed_tools JSON, body_md, version, is_active）+ `agent_skill_binding`（id, agent_id, skill_id, sort_order, bound_at）
  - Go package `internal/numind/biz/skill/`：`Service.GetByID(ctx, id) (*Skill, error)` + `Service.GetByNameForUser(ctx, parentUserID, name) (*Skill, error)` + `Binding.ListByAgent(ctx, agentID) ([]*Binding, error)`
  - GORM model `internal/pkg/model/skill.go` + `agent_skill_binding.go`

如 #1 未实现上述任一接口，本 feature 在 worktree 内补（Tier 2 跨文件协作不冲突，因 #1 主代码在不同子文件）。

---

## §1 Goals & Non-Goals

### Goals

1. **Runtime use_skill 闭环**：让 Agent 的 LLM 能通过 `use_skill(name)` tool-call 动态调用装载的 Skill（v2 #1 的"装载关系"从 DB 装饰变为运行时真正生效）
2. **6 段 system prompt invariant 不破坏**（CLAUDE.md §6b I3）：Skill 目录扩展进段位 [3] body 而非新增段位
3. **5 slot hook chain 顺序不破坏**：turn-scope tool gate 落地为 permission pipeline 内新 validator 或独立 EinoToolWrapper（不新增 chain slot）
4. **dual-read 兜底**：Agent 无 binding 时走 v1 legacy 路径，与 v2 #2 上线前行为完全一致（agent-student.spec.ts 零回归）
5. **Langfuse trace 完整**：agent_run trace → use_skill span（含 skill metadata）→ 下次 LLM generation（input 含 body）
6. **跨 Skill 编排**：一个 turn 内允许 ≤3 次 use_skill；超 cap 返回 error result（不抛 fatal）
7. **narration 学员可见**：前端 chat 流显示 "📚 调用技能：{name}" 气泡

### Non-Goals

- DB schema 改动（#1 范围）
- Skill CRUD HTTP API（#1 范围）
- Marketplace 跨租户发布订阅（#3 范围）
- Skill scripts/ 子目录代码执行（v2.5 评估）
- 删 `agent_definition.generated_skill_body/custom_skill_body/tool_flags` deprecated 字段（v2 #4）
- prod 部署（按 autopilot 规则跳）
- admin-web 改动
- 意图分类器自动 routing（LLM 自主决策，无规则引擎）
- 跨 Skill 自动编排或固定调用顺序（LLM 自主决定调用顺序）

---

## §2 Data Model (read-only from #1)

本 feature **不创建新表 / 不改 schema**。读 #1 已建的 4 个对象：

| 资源 | 来源 | 本 feature 使用方式 |
|---|---|---|
| `skill` table | #1 | runtime 在 use_skill 工具内通过 `Service.GetByNameForUser(parentUserID, name)` 读 body_md + allowed_tools |
| `agent_skill_binding` table | #1 | runtime 启动时通过 `Binding.ListByAgent(agentID)` 读全部 binding，缓存到 Run scope |
| `biz/skill.Service` | #1 | 编排查询，包含 owner 校验 |
| `agent_definition.generated_skill_body / custom_skill_body / tool_flags` | v1 (deprecated by #1) | dual-read fallback 路径读 |

### §2.1 缺函数补丁（如 #1 未实现）

若 `git fetch origin develop && grep "ListByAgent" internal/numind/biz/skill/binding.go` 为空，本 feature 在 worktree 的 `internal/numind/biz/skill/binding.go` 补实现：

```go
// ListByAgent 列出 Agent 装载的全部 Skill binding，按 sort_order asc 排序。
// owner 校验由调用方保证（runner.Run 已注入 ctx 含 parentUserID）。
func (b *Binding) ListByAgent(ctx context.Context, agentID uint64) ([]*AgentSkillBinding, error) {
    var bindings []*AgentSkillBinding
    err := b.db.WithContext(ctx).
        Where("agent_id = ?", agentID).
        Order("sort_order ASC").
        Find(&bindings).Error
    if err != nil {
        return nil, fmt.Errorf("Binding.ListByAgent agent=%d: %w", agentID, err)
    }
    return bindings, nil
}
```

S4 task 1 第一步：`git fetch origin develop` + 验证 `ListByAgent` 是否已实现。如已实现直接 import；未实现按上述补。

---

## §3 Runtime Architecture Changes

### §3.1 use_skill platform tool

**文件**：`internal/numind/biz/agent/tool_use_skill.go`（**新建**）

实现 `AgentTool` interface（与 8 个内置工具同构，参考 `internal/numind/biz/agent/tool_kb_search.go` 等）：

```go
package agent

import (
    "context"
    "encoding/json"
    "fmt"

    "numind-server/internal/numind/biz/skill"
    "numind-server/internal/pkg/log"
    "numind-server/internal/pkg/middleware"
    "numind-server/internal/pkg/model"
)

const (
    UseSkillToolName       = "use_skill"
    UseSkillTurnCapDefault = 3
)

// CtxKeyUseSkillTurn — ctx key (exported so permission validator can read it).
// 类型为命名 struct{} 避免 string-key 冲突。
type ctxKeyUseSkillTurnT struct{}
var CtxKeyUseSkillTurn = ctxKeyUseSkillTurnT{}

// CtxKeySkillBindings — runner 缓存的 binding 列表 ctx key (bound check / catalog 复用)
type ctxKeySkillBindingsT struct{}
var CtxKeySkillBindings = ctxKeySkillBindingsT{}

// CtxKeyAgentBaseToolNames — Agent 基础工具白名单 ctx key (TurnScopeAllowedToolsValidator 读)
type ctxKeyAgentBaseToolNamesT struct{}
var CtxKeyAgentBaseToolNames = ctxKeyAgentBaseToolNamesT{}

// UseSkillTurnState — 每个 user turn 起一份，runner 在 user input 时 reset
type UseSkillTurnState struct {
    InvocationCount      int                 // 本 turn 已 use_skill 次数
    AllowedTools         map[string]struct{} // turn-scope 临时启用工具集合
    Cap                  int                 // 默认 3
    PendingBody          string              // 待注入下次 LLM 调用的 Skill body
    PendingSkillName     string              // 配套 PendingBody
    PendingSkillVersion  int                 // 配套 PendingBody
    SkillByID            map[uint]*model.Skill // runner 启动时 batchGet，复用给 use_skill bound check（解 P0-1 N+1）
}

type useSkillTool struct {
    skillService skill.IService
}

// NewUseSkillTool 由 biz.go wire（S4 task 5 集成）
// 注意：bindingStore 不再注入到 tool — 改为从 ctx.UseSkillTurnState.SkillByID 读 runner 缓存（避免 use_skill 每次重查 DB）
func NewUseSkillTool(skillSvc skill.IService) AgentTool {
    return &useSkillTool{skillService: skillSvc}
}

func (t *useSkillTool) Name() string { return UseSkillToolName }

func (t *useSkillTool) Description() string {
    return "调用一个已装载的技能。技能内容会被载入对话上下文，技能需要的额外工具会在本轮临时启用。\n参数 name: 技能名称（必填，必须是 system prompt '可用技能' 段中列出的技能名之一）"
}

func (t *useSkillTool) ParamSchema() *ToolParamSchema {
    return &ToolParamSchema{
        Type: "object",
        Properties: map[string]ToolParamProperty{
            "name": {Type: "string", Description: "要调用的技能名称（必须是已装载的技能之一）"},
        },
        Required: []string{"name"},
    }
}

func (t *useSkillTool) IsDestructive() bool { return false }

func (t *useSkillTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
    var p struct{ Name string `json:"name"` }
    if err := json.Unmarshal(args, &p); err != nil {
        return "", fmt.Errorf("use_skill: bind: %w", err)
    }
    if p.Name == "" {
        return jsonErr("name 参数不能为空"), nil
    }

    // 1. 取 ctx 注入的 turn state（runner 维护）
    turn, ok := ctx.Value(CtxKeyUseSkillTurn).(*UseSkillTurnState)
    if !ok || turn == nil {
        // runner 未注入 turn state — 说明本 Agent 没有 binding 走 legacy 路径，use_skill 不应注册到 toolMap
        // 防御性：返回 error tool result（不抛 fatal）
        return jsonErr("use_skill 未启用（本 Agent 无任何技能装载，请联系配置者）"), nil
    }

    // 2. 检查 turn cap
    if turn.InvocationCount >= turn.Cap {
        return jsonErr(fmt.Sprintf("已达本轮技能调用上限 (%d 次)，本轮无法再调用其他技能", turn.Cap)), nil
    }

    // 3. lookup Skill (owner 校验在 GetByNameForUser 内做)
    parentUserID := middleware.ParentUserIDFromCtx(ctx)
    sk, err := t.skillService.GetByNameForUser(ctx, parentUserID, p.Name)
    if err != nil {
        if isNotFoundErr(err) {
            return jsonErr(fmt.Sprintf("技能 '%s' 不存在或无权访问", p.Name)), nil
        }
        // 严重内部错误（DB down 等）— 此处也返回 error result 让 LLM 优雅恢复，不上抛 terminate run
        log.Errorw("use_skill: GetByNameForUser failed", "name", p.Name, "error", err)
        return jsonErr(fmt.Sprintf("技能 '%s' 查询失败：%s", p.Name, err.Error())), nil
    }

    // 4. 业务校验
    if !sk.IsActive {
        return jsonErr(fmt.Sprintf("技能 '%s' 已被禁用", p.Name)), nil
    }
    if sk.BodyMD == "" {
        // S1-D14: 空 body 视为 error
        emitNarrationError(ctx, p.Name, "技能内容为空")
        return jsonErr(fmt.Sprintf("技能 '%s' 内容为空，请联系配置者更新", p.Name)), nil
    }

    // 5. 验证该 Agent 真的装载了这个 Skill（防止跨 Agent 窃取）
    //    P0-1 拍板：使用 runner 缓存的 SkillByID（启动时 batchGet 一次），不再独立查 DB
    if _, bound := turn.SkillByID[sk.ID]; !bound {
        return jsonErr(fmt.Sprintf("技能 '%s' 未被本 Agent 装载", p.Name)), nil
    }

    // 6. 合并 allowed_tools 到 turn-scope（去重）
    var allowedTools []string
    if err := json.Unmarshal(sk.AllowedTools, &allowedTools); err != nil {
        log.Warnw("use_skill: AllowedTools unmarshal failed; 视为空", "skill_id", sk.ID, "error", err)
    }
    for _, toolName := range allowedTools {
        turn.AllowedTools[toolName] = struct{}{}
    }

    // 7. narration emit (Provider 由 runner 注入到 ctx)
    emitNarrationLoaded(ctx, p.Name)

    // 8. 准备 PendingBody — runner 在下次 LLM 调用前消费（§3.3）
    turn.PendingBody = sk.BodyMD
    turn.PendingSkillName = sk.Name
    turn.PendingSkillVersion = int(sk.Version)

    // 9. budget 由 hook chain 在 PostToolCall 自动记账（与其他工具同构）

    // 10. count++
    turn.InvocationCount++

    // 11. 返回 acknowledgement (body 通过 PendingBody 通道注入 §3.3，不在返回值中)
    return fmt.Sprintf(`{"status":"loaded","skill_name":%q,"skill_version":%d,"body_length":%d,"allowed_tools_added":%v,"message":"技能 '%s' 已载入对话上下文，请根据技能指引完成任务"}`,
        sk.Name, sk.Version, len(sk.BodyMD), allowedTools, sk.Name), nil
}

// 辅助函数
func jsonErr(msg string) string {
    b, _ := json.Marshal(map[string]string{"status": "error", "error": msg})
    return string(b)
}
```

**关键约束**：
- `Invoke` 永不返回非 nil error（包括 DB lookup 失败）——所有业务错误用 tool result 表达，让 LLM 自我恢复
- `ctx` 携带：parentUserID（中间件已注入）、agentDefinitionID（runner 注入）、CtxKeyUseSkillTurn（runner 注入）、narrationProvider（runner 注入）
- **Skill body 通过 turn.PendingBody 通道注入下次 LLM 调用的 messages**（§3.3），不在 Invoke 返回值中
- **bound check 数据源**（P0-1 拍板）：使用 runner 启动时缓存的 `turn.SkillByID`，不重复查 DB。trade-off：若 Skill 在本 Run 进行中被父账户卸载（unbind），当前 Run 仍能 use_skill（接受这个新鲜度）— 下次 Run 启动 ListByAgent 重查时生效。理由：N+1 性能优先；卸载场景罕见且无安全风险（cross-tenant 校验在 GetByNameForUser 内做了，bound check 仅做本 Agent 装载验证）

### §3.2 system prompt 段位 [3] body 扩展

**文件**：`internal/numind/biz/agent/runner.go`，改动位置 line 396-419（body 装载块）+ line 578-589（6 段拼接）

**保 invariant**：6 段顺序 [1] PlatformBase → [2] tenantHardRules → [3] body → [4] Memories → [5] toolsSection → [6] PlatformSafetyFooter 不变。

**段位 [3] body 双轨**：
- **v2 路径**（len(skillBindings) > 0）：body = "## 可用技能" catalog block（不读 ad.GeneratedSkillBody，binding 表已是技能定义新源）
- **legacy 路径**（无 binding）：body = ad.GeneratedSkillBody 或 ad.CustomSkillBody（v1 行为完全保留）

**runner.go 改动框架**（详细 batchGet 实现见 §3.6）：

```go
// 4. #5 skill-system + v2 #2: 装载 agent_definition + 组装 SystemPrompt
var skillVer int
var body string
var ad *model.AgentDefinition
var useSkillTurnState *UseSkillTurnState        // nil 走 legacy
var skillBindings []*model.AgentSkillBinding    // cache to Run scope
var skillByID map[uint]*model.Skill           // §3.6 batchGet 产出，复用给 catalog + tool wire + use_skill bound check

if req.AgentDefinitionID > 0 && r.skillStore != nil {
    ad, _ = r.skillStore.GetByIDIncludeInactive(ctx, req.AgentDefinitionID)
    // ... 现有 owner 校验等不变 ...

    // v2 #2: 查 binding (1 SQL)
    if r.skillBindingStore != nil {
        skillBindings, _ = r.skillBindingStore.ListByAgent(ctx, req.AgentDefinitionID)
    }

    // v2 #2: batchGet Skills (1 SQL IN — 详见 §3.6)
    // P1-2 修复：避免 N+1
    if len(skillBindings) > 0 && r.skillService != nil {
        skillByID = batchGetSkills(ctx, r.skillService, skillBindings)
        if skillByID == nil {
            // batchGet 失败 → 降级 legacy（fail-safe）
            skillBindings = nil
        }
    }

    if len(skillBindings) > 0 {
        // v2 路径
        // - 同名防御 (S1-D13)
        if err := checkDuplicateSkillNames(skillBindings, skillByID); err != nil {
            return nil, err // 拒绝启动 Run
        }
        // - body = catalog block
        body = buildSkillCatalogBlockFromMap(skillBindings, skillByID)
        // - 初始化 turn state (PendingBody/PendingSkillName/PendingSkillVersion 字段在 §3.1 struct 定义)
        useSkillTurnState = &UseSkillTurnState{
            InvocationCount:     0,
            AllowedTools:        make(map[string]struct{}),
            Cap:                 UseSkillTurnCapDefault,
            PendingBody:         "",
            PendingSkillName:    "",
            PendingSkillVersion: 0,
            SkillByID:           skillByID, // ★ runner cache 复用给 use_skill bound check
        }
        ctx = WithUseSkillTurn(ctx, useSkillTurnState)
    } else {
        // legacy (dual-read 兜底)
        body = ad.GeneratedSkillBody
        if ad.AdvancedMode {
            body = ad.CustomSkillBody
        }
    }
    skillVer = int(ad.Version)
    // ... rest of existing context injections (parent_user_id, agent_definition_id, etc) ...
}
```

**buildSkillCatalogBlockFromMap 详见 §3.6**。

**6 段拼接保持原样**（line 578-589 完全不动），body 已包含 v2 catalog 或 v1 legacy body。**断言测试**（§10 测试项 5）：S4 task 加 `TestRunner_SystemPrompt_6SegmentInvariant`，验证 string 包含顺序 `PlatformBasePrompt < tenantHardRules < body < Memories < toolsSection < PlatformSafetyFooter`。

### §3.3 Skill body 注入对话 messages 的 role 选择 (S0-D7 / S1-D7)

**S2 拍板**：使用 **system reminder 包装的 user message**，原因如下：

3 候选对比（基于 Eino schema.Message 行为 + Anthropic / OpenAI tool-calling 规范）：

| 候选 | 优 | 缺 | S2 评估 |
|---|---|---|---|
| **assistant 消息** | 与对话流自然 | LLM 会把 body 当作自己说过的话（导致 "我刚说过 X" 的元混乱）；Eino ReAct 状态机看到 assistant msg 会认为本轮回合已完成提前退出 | **❌ 否决** — Eino ReAct 状态机冲突 |
| **tool result** | 与 use_skill tool-call 配对清晰；Eino 原生支持 | body 长度可能超过 tool result 软上限（不同 provider 不一致，Anthropic 16KB / OpenAI 无明确）；tool result 设计意图是"工具执行结果"，载入文档语义不匹配 | ⚠ 备选 |
| **system reminder（包装 user msg）** | LLM 把它看作"系统提示"，不会与对话流混淆；不破坏 Eino 状态机；与现有 attachment fallback / memory disclaimer 模式一致 | 需要包装格式（`<system-reminder>...</system-reminder>` XML 包裹）+ 前端 Scrubber 已知会过滤这类标签（compactv2 Streaming Context Scrubber，已 land） | **✅ 选定** |

**实施细节**：

1. `use_skill` Invoke 返回 acknowledgement tool result（含 `body_length`、`status=loaded` 等元信息）
2. **runner 在下一次 LLM 调用前**，在 messages list 末尾追加一条特殊 user message：

```go
// runner 接收 Eino tool-result 后，在 Generate 前 inject body
if turn := getUseSkillTurnState(ctx); turn != nil && turn.PendingBody != "" {
    einoMessages = append(einoMessages, &schema.Message{
        Role:    schema.User,
        Content: fmt.Sprintf("<system-reminder>\n以下是你刚调用的技能 '%s' 的详细指引（v%d）。请按这些指引继续完成用户的任务：\n\n%s\n</system-reminder>",
            turn.PendingSkillName, turn.PendingSkillVersion, turn.PendingBody),
    })
    turn.PendingBody = ""    // consume
    turn.PendingSkillName = ""
}
```

3. `turn.PendingBody` 由 `useSkillTool.Invoke` 在第 9 步（count++ 之前）写入：
```go
turn.PendingBody = sk.BodyMD
turn.PendingSkillName = sk.Name
turn.PendingSkillVersion = sk.Version
```

4. **Scrubber 兼容**（P2-1 已验证）：`internal/numind/biz/compactv2/scrubber/scrubber.go` 是 `StreamScrubber`，仅处理 LLM stream **output** 的 SSE chunk（见 `scrubber.go:9` `scrubberState` 注释 + `scrubber.go:1` package doc）。Skill body 通过 `einoMessages` append 注入是 **input** 路径，不经过 scrubber。✅ 验证证据：`grep -rn "StreamScrubber\|Scrub" internal/numind/biz/agent/runner.go` 仅在 stream emit 点引用，input 构建（line 793 `buildEinoMessages`）无 scrubber 调用。

### §3.4 Turn-scope tool whitelist hook (S0-D4 / S1-D11)

**S2 拍板**：候选 (a) **permission pipeline 内新 validator**，理由：

- permission pipeline 已有 7 validator 同构架构（ToolFlag / TenantAdminRule / SandboxOverride / WorkingDir / UserSessionRule / PlatformHardRule / LLMClassifier，见 `biz/permission/validators/`）
- 不改 hook chain 5 slot 顺序（CLAUDE.md §6b）
- validator 输出 `permission.PermissionResult`，与现有架构兼容
- Eino 调用工具时 PermissionGate 已通过 `permission.WrapHooks` 装配（见 `biz/biz.go:312`），新 validator 自然生效

**文件**：`internal/numind/biz/permission/validators/use_skill_turnscope.go`（**新建**）

```go
package validators

import (
    "context"
    "fmt"

    "numind-server/internal/numind/biz/agent"
    "numind-server/internal/numind/biz/permission"
)

// UseSkillTurnScope — v2 #2 use_skill turn-scope 工具白名单 validator
// 位置：permission pipeline 第 8 个 validator（与现有 7 个并列）
// 行为：
//   - 工具名 == use_skill → Allow
//   - 工具名在 Agent 基础白名单 (ctx CtxKeyAgentBaseToolNames) → Passthrough（让后续 validator 判）
//   - 工具名在 turn-scope AllowedTools (ctx CtxKeyUseSkillTurn 内) → Passthrough
//   - 否则（绑定到 Skill 但 use_skill 未调用）→ Deny (DecisionReasonRule)
//
// Passthrough 而非 Allow：让后续 7 validator 继续判（ToolFlag / TenantAdminRule 等），
// 保持判定语义独立性。
type UseSkillTurnScope struct{}

func NewUseSkillTurnScope() permission.Validator { return &UseSkillTurnScope{} }

func (v *UseSkillTurnScope) ID() string { return "UseSkillTurnScope" }

func (v *UseSkillTurnScope) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
    if req.Tool == nil {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no tool")
    }
    toolName := req.Tool.Name()

    // use_skill 自身永远 allow
    if toolName == agent.UseSkillToolName {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "use_skill tool itself")
    }

    // base 白名单 — Passthrough
    if baseNames, ok := ctx.Value(agent.CtxKeyAgentBaseToolNames).([]string); ok {
        for _, n := range baseNames {
            if n == toolName {
                return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "in base whitelist")
            }
        }
    }

    // turn-scope check
    turn, ok := ctx.Value(agent.CtxKeyUseSkillTurn).(*agent.UseSkillTurnState)
    if !ok || turn == nil {
        // legacy 路径（无 binding），所有 RunRequest.ToolNames 已在 base whitelist
        // — 走到这里说明工具不在 base 白名单，让后续 validator 判（不应当走 deny）
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no turn state — legacy path")
    }
    if _, allowed := turn.AllowedTools[toolName]; allowed {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "in turn-scope allowed_tools")
    }

    // 工具属于某 Skill 的 allowed_tools，但当前 turn 还没 use_skill 调用 → deny
    return permission.Deny(v.ID()+":"+toolName, permission.DecisionReasonRule,
        fmt.Sprintf("工具 '%s' 当前未启用。该工具属于某个技能，请先用 use_skill 调用对应技能。", toolName))
}
```

**为什么用 `DecisionReasonRule` 而非新枚举值**：

CLAUDE.md §6b 列出的 5 invariants 不含 "DecisionReasonType 11 值不新增"，但 `permission/result.go:5` 注释明确写"11 种 canonical（蓝本 §4.4.5）"——视为软 invariant。"use_skill 未调用"在语义上属于"rule 违反"（runtime 规则：先 use_skill 再用该 Skill 的工具），用 `DecisionReasonRule` 合理。**新增 `DecisionReasonSkillNotInvoked` 12th 枚举值** 留 v3 评估，本 feature 不破坏 11 值。

**注册位置**：`biz/biz.go` 装配 PermissionPipeline 时 append 此 validator。S2 spec 不指定 wire order（pipeline 行为：所有 validator 都跑，结果合并；新 validator 加在末尾不影响判定）。S4 task 编码时引用 `biz/biz.go:279-312` 现有 wire 模式。

**ValidationRequest 字段不动**：现有 `PermissionRequest` 通过 `Tool` 字段已提供工具元数据；新增信息（AgentBaseToolNames / turn state）走 ctx，不改 struct。

### §3.5 turn-scope state reset on user message

每次新 user message 到达 runner（runner.Run 每次调用 = 一次 user input → 一次 RunResult）→ 启动时初始化新 turn state；同 Run 内不需要 reset（runner.Run 与 user input 1:1，不像 chat session 多 turn 共享 runner）。

**实施**：runner.go §3.2 binding 装载块内，turn state 在 `useSkillTurnState != nil` 分支创建后直接进入 Run，**生命周期 = 一次 runner.Run() 调用**。Eino 内层 ReAct 循环（multiple LLM iterations within one Run）共享同一个 turn state — InvocationCount 累加，AllowedTools 累积，符合 "一个 user turn 内多次 use_skill" 语义。

注：v1.5 compactv2 已把外层循环简化为 single-attempt（runner.go:809 maxAttempts=2 但只 1 attempt 真跑）。本 feature 不改这个行为；turn state lifetime = 单次 runner.Run() 调用，与 user input 自然 1:1。**学员对话页"新消息"** = 前端 SSE 重新建立 → 后端 runner.Run() 重新调用 → 新 turn state。

```go
// runner.go inside §3.2 binding 装载分支
if len(skillBindings) > 0 {
    // ... buildSkillCatalogBlock ...
    useSkillTurnState = &UseSkillTurnState{
        InvocationCount:     0,
        AllowedTools:        make(map[string]struct{}),
        Cap:                 UseSkillTurnCapDefault,
        PendingBody:         "",
        PendingSkillName:    "",
        PendingSkillVersion: 0,
        SkillByID:           skillByID, // §3.6 batchGet 产出
    }
    ctx = context.WithValue(ctx, CtxKeyUseSkillTurn, useSkillTurnState)
}
```

### §3.6 Runner 工具装配（line 617-641）+ Skill batchGet（解 P1-2 N+1）

**改动**：用一次 batchGet 拉取所有 binding 的 Skill 元数据，复用给 catalog block + 工具装配 + use_skill bound check。

```go
// 在 §3.2 binding 装载块开始处，紧跟 skillBindings 查询后

// P1-2: batchGet Skills (一次 SQL IN (...) 替代 N+1)
var skillIDs []uint64
for _, b := range skillBindings {
    skillIDs = append(skillIDs, b.SkillID)
}
skillByID := make(map[uint]*model.Skill, len(skillIDs))
if len(skillIDs) > 0 && r.skillService != nil {
    skills, err := r.skillService.GetByIDs(ctx, skillIDs)
    if err != nil {
        log.Warnw("AgentRunner.Run: skillService.GetByIDs failed; v2 路径降级 legacy",
            "agent_id", req.AgentDefinitionID, "skill_ids", skillIDs, "error", err)
        // 降级：当作没 binding，走 legacy
        skillBindings = nil
    } else {
        for _, sk := range skills {
            skillByID[sk.ID] = sk
        }
    }
}

// 同名 Skill 防御（S1-D13）— 使用 batchGet 结果
seen := make(map[string]uint64)
for _, b := range skillBindings {
    sk, ok := skillByID[b.SkillID]
    if !ok { continue }
    if existing, dup := seen[sk.Name]; dup {
        log.Errorw("AgentRunner.Run: duplicate Skill name in bindings",
            "agent_id", req.AgentDefinitionID, "skill_name", sk.Name,
            "skill_ids", []uint64{existing, sk.ID})
        return nil, fmt.Errorf("AgentRunner.Run: duplicate Skill name '%s' in bindings (rule S1-D13)", sk.Name)
    }
    seen[sk.Name] = sk.ID
}

// 然后再走 §3.2 buildSkillCatalogBlock (接收 skillByID 复用) + 设 useSkillTurnState.SkillByID = skillByID
body = buildSkillCatalogBlockFromMap(skillBindings, skillByID)
// ... useSkillTurnState 创建（详见 §3.5）SkillByID 字段填 skillByID ...
```

更新 `buildSkillCatalogBlockFromMap`（与 §3.2 原 buildSkillCatalogBlock 签名不同）：

```go
func buildSkillCatalogBlockFromMap(bindings []*model.AgentSkillBinding, byID map[uint]*model.Skill) string {
    if len(bindings) == 0 { return "" }
    var sb strings.Builder
    sb.WriteString("\n\n## 可用技能\n\n")
    sb.WriteString(fmt.Sprintf("你装载了以下技能。当对话需要某个技能时，使用 `use_skill(name=\"<技能名>\")` 工具调用它。工具会把技能详细指引载入对话上下文，并临时启用该技能需要的额外工具。每轮对话最多可调用 %d 次技能。\n\n", UseSkillTurnCapDefault))
    for _, b := range bindings {
        sk, ok := byID[b.SkillID]
        if !ok || sk == nil || !sk.IsActive { continue }
        sb.WriteString(fmt.Sprintf("- **%s**：%s\n", sk.Name, sk.Description))
        if sk.WhenToUse != "" {
            sb.WriteString(fmt.Sprintf("  - 何时使用：%s\n", sk.WhenToUse))
        }
    }
    return sb.String()
}
```

**注意**：`buildSkillCatalogBlock` 返回值前缀 `\n\n` — 这是因为 §3.9 描述 catalog 是段位 [3] body 的尾巴扩展。runner.go body 赋值改为：

```go
// §3.2 binding 装载分支末尾
if len(skillBindings) > 0 {
    // v2 路径：catalog 占据整段 [3] body
    // 原 agent_definition.generated_skill_body / custom_skill_body 不再读
    body = buildSkillCatalogBlockFromMap(skillBindings, skillByID)
    // 注：v2 路径下原 Skill body 已被 binding 表中的多个 Skill 接管；
    // 不读 generated_skill_body 是有意 — 父账户用 binding 模型时不应再编辑 ad.body
} else {
    // legacy 路径 (dual-read 兜底)
    body = ad.GeneratedSkillBody
    if ad.AdvancedMode { body = ad.CustomSkillBody }
}
```

**工具装配**（line 617-641 改造，复用 skillByID 解 P1-2）：

```go
// 5. 从 registry 装配 Eino 工具列表 (改造)
var einoTools []einotool.BaseTool
toolMap := make(map[string]FullTool)
basicToolNames := req.ToolNames // Agent 基础工具白名单

// 装载 Agent 基础工具 (现有代码不变)
if r.registry != nil {
    for _, name := range basicToolNames {
        if ft, ok := r.registry.GetTool(name); ok {
            base := adaptFullToEinoTool(ft, effectiveHooks)
            if useCompactV2 {
                base = wrapToolWithV2ArtifactProcessing(base, ft.Name(), run.ID, r.artifactStore, r.artifactDir)
            }
            einoTools = append(einoTools, base)
            toolMap[name] = ft
        }
    }
}

// v2 #2: 装载 use_skill + 所有 binding 的 allowed_tools 并集
if useSkillTurnState != nil {
    // (1) use_skill 本身
    if ft, ok := r.registry.GetTool(UseSkillToolName); ok {
        einoTools = append(einoTools, adaptFullToEinoTool(ft, effectiveHooks))
        toolMap[UseSkillToolName] = ft
    } else {
        log.Errorw("AgentRunner.Run: use_skill tool not registered — Agent has bindings but tool missing",
            "agent_id", req.AgentDefinitionID)
    }
    // (2) binding allowed_tools 并集 (复用 skillByID 解 P1-2 N+1)
    extraTools := make(map[string]struct{})
    for _, b := range skillBindings {
        sk, ok := skillByID[b.SkillID]
        if !ok || sk == nil { continue }
        var allowed []string
        _ = json.Unmarshal(sk.AllowedTools, &allowed)
        for _, t := range allowed {
            if _, dup := extraTools[t]; dup { continue }
            if _, base := toolMap[t]; base { continue }
            extraTools[t] = struct{}{}
        }
    }
    for name := range extraTools {
        if ft, ok := r.registry.GetTool(name); ok {
            einoTools = append(einoTools, adaptFullToEinoTool(ft, effectiveHooks))
            toolMap[name] = ft
        }
    }
}
ctx = WithFullToolMap(ctx, toolMap)
ctx = context.WithValue(ctx, CtxKeyAgentBaseToolNames, basicToolNames) // 供 UseSkillTurnScope validator 读
```

**Eino 工具列表**：基础工具 ∪ {use_skill} ∪ Σ binding.allowed_tools 全部注册到 Eino。**默认拒绝 binding tools** 由 UseSkillTurnScope validator 在 permission pipeline 层做；Eino 看到全集但运行时 hook 拦截非白名单调用。HookAction 走现有 `HookActionPermissionDeny`（CLAUDE.md §6b I6 5 值不动）。

### §3.7 ctx helpers 一览

本 feature 新增 3 个 ctx key（全部在 `tool_use_skill.go` 包内定义为 typed struct{} key，避免 string-key 冲突）：

| ctx key | 注入方 | 读取方 | 值类型 |
|---|---|---|---|
| `CtxKeyUseSkillTurn` | runner.go §3.2 binding 分支 | useSkillTool.Invoke + UseSkillTurnScope validator | `*UseSkillTurnState` |
| `CtxKeySkillBindings` | runner.go §3.2 binding 分支（备用，主路径直接走 TurnState.SkillByID） | 暂未使用，预留扩展 | `[]*model.AgentSkillBinding` |
| `CtxKeyAgentBaseToolNames` | runner.go §3.6 工具装配后 | UseSkillTurnScope validator | `[]string` |

helper 函数（约定）：

```go
// tool_use_skill.go 内
func WithUseSkillTurn(ctx context.Context, s *UseSkillTurnState) context.Context {
    return context.WithValue(ctx, CtxKeyUseSkillTurn, s)
}
func UseSkillTurnFromCtx(ctx context.Context) (*UseSkillTurnState, bool) {
    s, ok := ctx.Value(CtxKeyUseSkillTurn).(*UseSkillTurnState)
    return s, ok && s != nil
}

func WithAgentBaseToolNames(ctx context.Context, names []string) context.Context {
    return context.WithValue(ctx, CtxKeyAgentBaseToolNames, names)
}
func AgentBaseToolNamesFromCtx(ctx context.Context) ([]string, bool) {
    n, ok := ctx.Value(CtxKeyAgentBaseToolNames).([]string)
    return n, ok
}
```

无需写到 `middleware/` 包（这些是 agent runner 内部状态，不跨 HTTP 层）。

---

## §4 API Contracts (internal only)

本 feature **不暴露任何 HTTP 端点**。新增 / 引用的 Go interface 契约：

| Interface / Type | 文件 | 用途 / Method set |
|---|---|---|
| `AgentTool` impl `useSkillTool` | `biz/agent/tool_use_skill.go`（新建）| 实现 `Name() / Description() / ParamSchema() / IsDestructive() / Invoke()` |
| `permission.Validator` impl `UseSkillTurnScope` | `biz/permission/validators/use_skill_turnscope.go`（新建）| 实现 `ID() string` + `Validate(ctx, req permission.PermissionRequest) permission.PermissionResult` |
| `skill.IService` 新增 `GetByIDs(ctx, []uint64) ([]*model.Skill, error)` | `biz/skill/service.go`（#1 land 后；若缺则本 feature 在 worktree 补）| batchGet（解 P1-2 N+1） |
| `skill.IService` 已有 `GetByID(ctx, uint64) (*model.Skill, error)` | #1 | use_skill bound check（已废弃改用 SkillByID 缓存）/ runner fallback |
| `skill.IService` 已有 `GetByNameForUser(ctx, parentUserID uint, name string) (*model.Skill, error)` | #1 | use_skill 主路径 lookup |
| `skill.IBindingStore.ListByAgent(ctx, agentID uint64) ([]*model.AgentSkillBinding, error)` | #1 (`biz/skill/binding.go`)；若缺本 feature 补 §2.1 | runner 启动时查 binding 列表 |
| `narration.Event` 新增 `kind="skill_use"` + `phase` 字段 | `biz/agent/narration/event.go`（追加） | SSE event 类型；前端按 phase 渲染 |
| `RunRequest.ToolNames` 语义不变 | `biz/agent/runner.go` | 仍是 Agent 基础工具白名单；binding tools 由 runner 内部从 skillBindings 派生（不影响调用方契约）|
| `RunResult.SkillVersion` 兼容 | `biz/agent/runner.go` | 走 binding 路径时取 ad.Version（同 legacy 行为）；后续 v2 #4 可拆 binding-specific version |
| `middleware.ParentUserIDFromCtx` (uint) | `internal/pkg/middleware/` (已有) | 复用 |
| `middleware.AgentDefinitionIDFromCtx` (uint64) | 已有 (`agent_def_ctx.go` 已注入) | 复用 |
| `agent.CtxKeyUseSkillTurn` / `CtxKeyAgentBaseToolNames` / `CtxKeySkillBindings` (typed struct{} keys) | `biz/agent/tool_use_skill.go`（新建）| §3.7 ctx helpers，runner ↔ tool ↔ validator 之间状态通道 |
| `agent.UseSkillTurnState` struct | `biz/agent/tool_use_skill.go` | §3.1 定义，含 InvocationCount / AllowedTools / Cap / PendingBody / PendingSkillName / PendingSkillVersion / SkillByID |

**`skill.IService` vs `skill.IBindingStore`（P1-5 区分）**：

| 接口 | Method | 作用域 |
|---|---|---|
| `IService` | `GetByID` / `GetByNameForUser` / `GetByIDs` / `Create` / `Update` / `Delete` / 等 | Skill 主资源 CRUD |
| `IBindingStore` | `ListByAgent` / `Bind` / `Unbind` / `Reorder` | Agent ↔ Skill 关联表 |

**runner.go 字段重命名**（清晰起见）：
- `r.skillStore` (v1 IAgentDefinitionStore) — 不动，与现有 ad lookup 关联
- `r.skillService` (v2 #1 skill.IService) — **新增** wire（如 #1 已 wire 则直接复用）
- `r.skillBindingStore` (v2 #1 skill.IBindingStore) — **新增** wire

---

## §5 Hook Chain Integration

**hook chain 5 slot 顺序固定不变**（CLAUDE.md §6b I3）：

```
compliance → permission → budget → sandbox → narration
```

本 feature 新增工作：

| slot | 新增内容 | 文件 |
|---|---|---|
| **permission** | UseSkillTurnScope (validator 8/8，与现有 7 个 ToolFlag/TenantAdminRule/SandboxOverride/WorkingDir/UserSessionRule/PlatformHardRule/LLMClassifier 同 slot 内并列) | `biz/permission/validators/use_skill_turnscope.go` |
| **budget** | 无新增；use_skill 走默认 PreToolCall/PostToolCall 路径，与其他工具同构 | — |
| **narration** | provider 加 `kind="skill_use"` event 类型 | `biz/agent/narration/event.go` + `provider.go` |
| compliance | 无新增 | — |
| sandbox | 无新增（use_skill 不是 sandbox tool） | — |

---

## §6 Eino Integration Test Contract

**Hard test requirement**：S4 task 必须包含一个 Eino integration test 验证：

1. **中文工具名参数**：use_skill("销售话术训练") 能被 Eino schema.ToolUseBlock 正确解析（参数 JSON 包含 UTF-8 中文）
2. **system-reminder 包装 message** 不破坏 Eino ReAct 状态机（注入后下次 Generate 返回正常 assistant msg，不直接 terminate）
3. **UseSkillTurnScope deny** 触发的 PermissionResult 能被 Eino 工具调用层正确捕获并转化为 `HookActionPermissionDeny`（现有 5 个 HookAction 值之一，不新增）

**测试文件**：`internal/numind/biz/agent/eino_skill_integration_test.go`（新建）

**测试 fixture 来源（P2-3 解答）**：复用现有 `runner_integration_test.go`（含 mockChatModel + mockToolRegistry pattern）和 `hooks_test.go`（含 HookActionRegistry assertion）。S3 task 1 起步必读这两个文件熟悉现有 mock 风格。具体复用：

- mock LLM：定义 `fakeChatModel` 实现 Eino `model.ToolCallingChatModel` 接口，按 fixture 返回固定 tool-call 或 assistant msg（参照 `runner_integration_test.go` 已有 fake）
- mock skillService / bindingStore：手写 struct 实现 `skill.IService` / `skill.IBindingStore` 接口（与 `tool_flag_test.go` 内 mock IAgentDefinitionStore 同模式）
- 真实 PermissionPipeline + UseSkillTurnScope validator（不 mock 验证逻辑）
- 真实 runner.Run() 调用，inspect RunResult + Langfuse trace assertions（trace 走 mock collector）

---

## §7 Langfuse Trace Topology

**Trace 起点**：复用现有 `agent_run` trace（runner.Run 创建，不新建 root）

**新增 spans**（per use_skill 调用）：

```
agent_run (trace)
  ├── react_iteration_N (span, 已有)
  │   ├── llm_generate (generation, 已有)
  │   ├── tool_kb_search (span, 已有)
  │   └── tool_use_skill (span, 新增)   ← v2 #2
  │        metadata:
  │           skill_id: 42
  │           skill_name: "销售话术训练"
  │           skill_version: 3
  │           body_token_count: 1850
  │           allowed_tools_added: ["web_search"]
  │           turn_invocation_count_after: 1
  │           result_status: loaded | error
  │           error: ""  // if error, 填错误消息
  └── llm_generate (generation, 含 system-reminder 包装的 Skill body 作 input) ← input 含 body 验证
```

**实施**：use_skill `Invoke()` 内启用 langfuse span（参照 ai-service.md §3 Span 模式）：

```go
if tc := langfuse.FromContext(ctx); tc != nil {
    spanID := langfuse.SpanID()
    langfuse.CreateSpan(tc.TraceID, spanID,
        langfuse.WithSpanParent(tc.ParentObservationID),
        langfuse.WithSpanName("tool_use_skill"),
        langfuse.WithSpanMetadata(map[string]interface{}{
            "skill_id": sk.ID,
            "skill_name": sk.Name,
            "skill_version": sk.Version,
            "body_token_count": estimateTokens(sk.BodyMD),
            "allowed_tools_added": allowedTools,
            "turn_invocation_count_after": turn.InvocationCount + 1,
            "result_status": "loaded",
        }),
    )
    defer langfuse.EndSpan(spanID)
}
```

错误路径：error result 同样写 span（带 `result_status=error` + `error` 字段）

**S5 验证**：手工 trigger use_skill 一次，截图 Langfuse trace 包含 tool_use_skill span + 下一个 generation 的 input 含 system-reminder 包装的 body

---

## §8 Frontend Changes (`numind-web-v3`)

**唯一文件**：S2 阶段 git grep 确认 ToolBubble 实际位置（S0/S1 占位"或对应文件"）

S2 探查（在 develop 分支跑）：

```bash
cd numind-web-v3
grep -rn "ToolBubble\|tool-bubble" src/components/ | head -5
grep -rn "narration" src/api/ src/stores/ | head -5
```

**预期改动**：

1. `narration.ts` event 类型 enum 加 `'skill_use'`
2. `ToolBubble.vue` 模板加 `v-else-if="event.kind === 'skill_use'"` 分支：
   ```vue
   <div v-else-if="event.kind === 'skill_use'" class="tool-bubble skill-use">
     <span class="icon">📚</span>
     <span class="text" v-if="event.phase === 'loading'">正在加载技能：{{ event.skill_name }}…</span>
     <span class="text" v-else-if="event.phase === 'loaded'">已调用技能：{{ event.skill_name }}</span>
     <span class="text error" v-else-if="event.phase === 'error'">⚠ 技能加载失败：{{ event.error_message }}</span>
   </div>
   ```
3. 样式与现有 tool-bubble 一致（无新增 SCSS 文件）

---

## §9 Dual-Read Fallback Protocol

**决策协议**（runner.go § 4 inside）：

```
1. AgentDefinitionID > 0 且 skillStore 非 nil → 查 agent_definition
2. skillBindingStore 非 nil → ListByAgent(agent_id)
3. if len(bindings) > 0:
     走 v2 新路径
     body = buildSkillCatalogBlock(bindings)
     useSkillTurnState = &{}; ctx 注入
   else:
     走 v1 legacy 路径
     body = ad.GeneratedSkillBody / ad.CustomSkillBody (按 AdvancedMode)
     useSkillTurnState = nil; use_skill 工具不注册
```

**保证**：
- 任何 v1 Agent 没有 binding → 路径 1+2 走完 + len=0 → legacy；行为完全等同上线前
- 任何 v2 Agent 有 binding → 走新路径
- skillBindingStore 为 nil（runner wire 错误防御）→ 走 legacy（safer fallback）

**S5 验证**：`e2e/agent-student.spec.ts` 全套 pass（用 v1 fixture 不创建 binding）

---

## §10 S5 Verification Strategy (S1-D9 final)

继承 S1 候选，S2 拍板：

| 验证项 | 工具 | 范围 |
|---|---|---|
| 1. v2 新路径 happy path | Playwright E2E | 装 2 Skill → 对话触发 use_skill → 气泡显示 → 回复贴合指引 |
| 2. v1 legacy 零回归 | 现有 `e2e/agent-student.spec.ts` 全套 | 0 binding Agent 对话正常 |
| 3. use_skill 错误路径 | Go unit test on tool_use_skill.go | name 不存在 / Skill 已禁用 / 超 cap / 跨 Agent 未装载 |
| 4. turn-scope tool gate | Go unit test on permission validator | base 工具 allow / binding 工具 use_skill 前 deny use_skill 后 allow |
| 5. system prompt 6 段 invariant | Go unit test on runner.go | assert PlatformBase 在最前 / PlatformSafetyFooter 在最后 / Memories 在 body 与 toolsSection 之间 / segment count == 6 |
| 6. Eino 中文工具参数 + system-reminder 包装 | Go integration test (Eino) | 见 §6 |
| 7. Langfuse trace 完整性 | 手工 + Langfuse 截图 | 见 §7 末段 |
| 8. AC-11 调用率 | 手工 10 场景统计 | use_skill emit ≥3/10 |
| 9. **AC-6 BudgetTracker 集成** | Go unit test on tool_use_skill.go + budgetgate.WrapHooks | mock BudgetTracker 记录调用次数，assert use_skill 触发 PreToolCall + PostToolCall 各 1 次（与其他工具同构）；assert 超 cap 后 use_skill 仍 record budget（避免漏计） |

**禁止跳过项**：1 / 2 / 7 是父账户硬验收必看。

---

## §11 Open Decisions for S3 plan

S2 已拍板 / 引用上文的：
- ✅ Skill 目录位置（§3.2 段位 [3] body 双轨：v2 路径 = catalog；legacy = ad.GeneratedSkillBody）
- ✅ body 注入 role（§3.3 system-reminder 包装 user msg）— scrubber input 路径不过滤验证已加证据脚注
- ✅ turn-scope hook 实现（§3.4 permission validator，包路径 `biz/permission/validators/`）
- ✅ cap=3、排序=sort_order、同名 defensive check（继承 S1）
- ✅ DecisionReason 用 `DecisionReasonRule`（不新增 11 个 canonical 值）
- ✅ HookAction 用现有 `HookActionPermissionDeny`（5 值不动 CLAUDE.md §6b I6）
- ✅ bound check 数据源 = runner 缓存 `turn.SkillByID`（避免 use_skill 每次重查 DB）— 接受 unbind 当前 Run 不感知的新鲜度
- ✅ N+1 解 = `skillService.GetByIDs(ctx, skillIDs)` 一次 batchGet（§3.6），复用给 catalog + tool wire + bound check
- ✅ ctx helpers = `CtxKeyUseSkillTurn` / `CtxKeyAgentBaseToolNames` / `CtxKeySkillBindings`（typed struct{} keys）
- ✅ AC-6 BudgetTracker 集成测试（§10 项 9 新增）

**留给 S3 plan 编排（不是"未决"，是"task 切分"）**：

| # | 项 | S3 plan task |
|---|---|---|
| 1 | 7 task 拆分顺序（依赖图） | S3 plan §1 task DAG |
| 2 | Tier 1/2/3 并行可能性 | S3 plan §2 dispatch 矩阵 |
| 3 | 每 task 的验收准则 + LOC 边界 | S3 plan §3 per-task spec |
| 4 | S5 验证 task 在 plan 中的位置（NDF Rule 10）| S3 plan 末尾独立 task "S5 验证策略" |
| 5 | mock fixture 复用（#1 接口稳定后产物） | S3 plan §4 测试基础设施 |

---

## §12 Compatibility & Migration

### v1 → v2 兼容

| 资源 | 处理 |
|---|---|
| v1 Agent（无 binding） | 走 dual-read fallback，行为不变 |
| `agent_definition.generated_skill_body` | 保留读取（deprecated），删除留 v2 #4 |
| `agent_definition.custom_skill_body` | 同上 |
| `agent_definition.tool_flags` | runtime 仍读取（与 binding 的 allowed_tools 并集），删除留 v2 #4 |
| 现有工具白名单 hook | TurnScopeAllowedToolsValidator 是新 validator，不改现有 |
| 现有 e2e 测试 | 0 回归（dual-read 保证） |

### #1 → #2 接力

- #1 land develop 是 #2 进 S4 的硬阻塞（cold-start prompt 已约定）
- ScheduleWakeup 1800s 轮询；最多 7 天（336 次）
- #1 D5 仍未 land → 父账户介入 chip

### #2 → #3 接口稳定性

- `use_skill` 工具 schema 稳定（#3 marketplace 订阅的 Skill 走同一 tool）
- `narration.skill_use` event 稳定（#3 不扩展）
- `agent_skill_binding` 表 #1 拥有；#2 只读；#3 也只读（订阅来的 Skill 通过 `biz/skill.Service.Create` 创建本地副本 + 装载到 Agent，走 #1 已有接口）

---

## §13 Risk Reconfirmation

S1 §3 风险表 12 条 + invariant 兼容性 → 全部 S2 阶段有具体设计应对，无新增高风险：

| S1 风险 | S2 设计应对位置 |
|---|---|
| 1 #1 接口未定 | §2.1 缺函数补丁 + §11 S3 plan task 1 起步 git fetch 验证 |
| 2 Eino 不支持动态扩展 tool list | §3.6 启动时预注册并集 + §3.4 permission validator 默认拒绝 |
| 3 dual-read 写错破坏 v1 Agent | §9 协议 + S5 验证项 2 |
| 4 LLM 不主动调 use_skill | §3.2 catalog 中文 + 示例 + AC-11 ≥30% |
| 5 use_skill 无限递归 | §3.1 cap=3 + §3.5 reset on user msg |
| 6 Skill body 注入混乱 | §3.3 system-reminder 包装 |
| 7 hook chain 冲突 | §3.4 permission validator 不改 chain slot |
| 8 narration event 前端没处理 | §8 default fallback + S5 浏览器 QA |
| 9 #1 数据迁移失败 | §9 dual-read fallback 防 nil binding |
| 10 配置者改 Skill allowed_tools | §3.1 每次 use_skill 重新 lookup |
| 新11 DB 压力 | §3.6 启动时一次 ListByAgent 缓存到 Run scope |
| 新12 compactv2 阈值 | §3.3 body 走 system-reminder 走 input 路径，与 compactv2 prune 协议天然兼容（compactv2 prune 优先 tool result，user msg 后剪） |
| invariant 兼容 | §3.2 + §3.4 + 6 段 assertion test §10 项 5 |

---

## §14 Acceptance from PRD

复用 S1 §4 PRD 11 条 AC，**S2 不重写**。每条 AC 在 §10 验证策略中映射到具体测试项。

---

## §15 References

- v1 Spec：[architecture-v1.md §4.3](../../agent-mode/architecture-v1.md) — Skill 系统 v1 设计（决策#11 单 Skill 模型，被本 spec 颠覆）
- v1.5 Spec：[compactv2 / memory layer A](../../agent-mode/) — 复用 system-reminder 包装模式 / langfuse trace 模式
- #1 Spec（如已 land）：`docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-as-artifact-design.md`（S3 plan 阶段读它确认接口）
- #3 Spec：`docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md`（无运行时影响，仅契约对齐 §12）
- CLAUDE.md §6b agent/* 子包说明 + invariants
- ai-service.md §3 Span 与 Error 模式
