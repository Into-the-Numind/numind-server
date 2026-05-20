# Follow-up: agent-mode-skill-system advanced-mode body edit

**触发**: #10 `agent-mode-configurator-ux` S2 spec §13 / §20（commit `a41cded5`）
**优先级**: P1（影响高级模式 5% 用户体验）
**类型**: backend + frontend 联动 micro

---

## Problem

#5 backend `controller/v1/agent/skill.go` `PatchRequest`（line 50-62）**缺 `custom_skill_body *string` 字段**。
经核查 `biz/skill/service.go` 的 `Patch()` 函数，service 层只 apply 9 个字段（name/description/icon_url/welcome_message/starters/questionnaire_answers/tool_flags/credit_cap_per_session/daily_credit_cap），**没有处理 custom_skill_body**。

结果：切高级模式后 UI 无法保存自定义 SKILL.md 全文，只能修改工具开关（`tool_flags`）。

## #10 当前缓解（v1）

`numind-admin-web/src/views/agent/AgentAdvancedEdit.vue`：
- 顶部 NoticeBanner 明示 "✏️ 自定义 Prompt 编辑功能即将上线（v1 仅可查看 + 切换工具开关）"
- 渲染当前 body（`custom_skill_body || generated_skill_body`）为只读 disabled textarea
- 三个工具开关（code_sandbox / media / dangerous）可编辑 + PATCH 保存
- dangerous 切 true 时弹二次 ConfirmModal

## Proposed scope (后续 feature)

### Backend (micro 优先)

1. `numind-server/internal/numind/controller/v1/agent/skill.go`：
   - PatchRequest 加 `CustomSkillBody *string \`json:"custom_skill_body"\``
2. `numind-server/internal/numind/biz/skill/service.go` `Patch()`：
   - 加分支：`if req.CustomSkillBody != nil { ad.CustomSkillBody = *req.CustomSkillBody }`
   - **必须**：仅当 `ad.AdvancedMode == true` 时允许修改 custom_skill_body；否则返回 errno 400（"问卷模式不允许直接编辑 SKILL.md"）
   - **必须**：保存时检查 custom_skill_body 不能删除或修改平台固定段（PlatformBasePrompt / PlatformSafetyFooter 是 runtime 拼接，不在 body 内——但用户可能误把它们 paste 进 body；后端 strip 重复内容）
3. 单测覆盖：
   - advanced_mode=true + PATCH custom_skill_body → 持久化成功
   - advanced_mode=false + PATCH custom_skill_body → 400
   - PATCH 同时含 tool_flags 和 custom_skill_body → 都成功
   - history 表新版本含 custom_skill_body 完整快照
4. 部署：dev 跑 migration（无 schema 变更，仅代码改动）→ frontend follow-up 上线

### Frontend (micro，依赖 backend 先 merged)

升级 `numind-admin-web/src/views/agent/AgentAdvancedEdit.vue`：
- 移除 NoticeBanner（功能上线）
- textarea 改为可编辑（移除 disabled prop）
- v-model 双向绑定 local `body` ref
- 字符计数 + 8000 字符警示边界
- onBeforeRouteLeave 守卫纳入 body dirty 检查
- [保存] 按钮调 `store.update(id, { tool_flags, custom_skill_body: body.value })`

### Out of scope（此 follow-up 不包含）

- 切回问卷模式（不可逆约束保持，蓝本 §4.3.5）
- Markdown 语法高亮（v1 textarea 纯文本足够；后续独立 micro）
- 全文 system prompt 预览（含平台固定段渲染，独立 micro）
- LLM 协助 prompt 编辑（vN 远期）

## Acceptance

- 切高级 → 编辑 textarea → 保存 → DB `agent_definition.custom_skill_body` 更新 + `agent_definition_history` 新版本含完整快照
- 平台固定段 PLATFORM_BASE_PROMPT 不被用户内容污染（保存时后端 strip 或拒绝）
- advanced_mode=false 的 agent PATCH custom_skill_body → 400
- 已存在 advanced_mode=true 的 agent（如果有）保留行为不变

## 估算

- Backend micro: ~150 行 + 4 单测，~30-60 分钟
- Frontend micro: ~50 行修改 + 1 e2e spec 更新，~30 分钟
- 总 ~1-2 小时（两个 micro 串行）

## 关联

- #5 `agent-mode-skill-system`（merged `e05498b6`）— 落地的 backend 缺字段
- #10 `agent-mode-configurator-ux`（in progress）— v1 UI 已就位但只读
- #14 `agent-mode-e2e-rollout` — 真实 LLM 跑通后会触发高级模式编辑需求
