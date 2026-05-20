# NDF S1 Proposal + PRD · `agent-mode-skill-system`

**Track**：Standard
**Feature ID**：`agent-mode-skill-system`（14-feature 分解 #5/14）
**起草日期**：2026-05-22
**状态**：S1 草案
**前置 stage**：S0 通过（commit `dab4fecb`）

---

## 1. 目标与背景

### 1.1 商业价值

Numind 的 Agent 模式是"陪跑机构父账户帮子账户构建专属 AI 助手"的核心产品形态。配置者（机构父账户）的特点：

- **完全非技术**——不懂 prompt 工程、不懂 LLM 参数调优
- **领域专家**——能凭直觉判断 Agent 回答好坏
- **需要快速试错**——不愿等待技术团队介入

**Skill 系统的产品定位**：把 prompt 工程翻译成业务问卷，让配置者用业务语言（"我希望助手帮学员做什么"）配置 AI 行为，而不是写 prompt。

### 1.2 业务目标

- **95% 配置者走问卷路径**：12 题问卷 5 分钟填完，立即可用
- **5% 头部机构有自由度**：高级模式直接编辑 SKILL.md
- **零损失迭代**：每次保存 → 自动版本快照 → 失败可一键回滚
- **平台模板加速冷启动**：10 个开箱模板，覆盖常见场景（爆款分析师 / 复盘助手等）

### 1.3 技术目标（属于本 feature）

- Agent 能从 DB 加载 Skill 配置组装 system prompt 注入 LLM
- 真实落地 hook 信号传播路径（hook_stopped / stop_hook_prevented terminal_reason 真实生效）
- biz/skill 子包覆盖率 ≥ 80%；biz/agent 不降级
- 0 prod 影响

---

## 2. 用户故事（User Stories）

### US-1：父账户创建第一个 Agent（问卷路径，95% 用户）

```
作为：机构父账户「智学教育的张老师」
当：我登录 Numind 看到"Agent Builder 入口"
我想：从模板「学员爆款分析师」派生一个属于我机构的 Agent
以便：让我的学员可以分析他们的小红书笔记

完成路径（v1 仅支持后端 API，前端 UI 由 #10 落地）：
1. GET /v1/agent/skill-templates → 看到 10 个内置模板
2. POST /v1/agent/skills body={source_template_id: 1, name: "智学爆款分析师"}
3. 后端：
   - 拷贝模板 questionnaire_answers → 新 agent
   - 设置 parent_user_id = 张老师 user_id
   - 调 skill_builder 组装 generated_skill_body
   - 写 agent_definition_history v1
   - 返回 {id, version=1}
4. 学员立即可见此 Agent（在 #11 学员端 UI 选 Agent）
```

### US-2：父账户调整问卷某一题（修改场景）

```
作为：张老师
当：我发现 Agent 回答太严厉了，想改成"鼓励陪伴"风格
我想：只改 Q12（说话风格），其他不变
以便：保留之前的所有配置只改语气

完成路径：
1. GET /v1/agent/skills/123 → 看到完整 questionnaire_answers
2. PATCH /v1/agent/skills/123 body={questionnaire_answers: {...所有原值, q12: "鼓励陪伴"}}
3. 后端：
   - 计算 diff（仅 q12 变化）
   - 重新组装 generated_skill_body
   - version: 1 → 2
   - 写 history v2 完整快照
   - 返回 {id, version=2}
4. 下一次学员发消息 → 系统读 v2 generated_skill_body 注入 system prompt
```

### US-3：改坏了 → 回滚到旧版本

```
作为：张老师
当：我把 Agent 改成 v3 后发现回答质量变差
我想：回到 v2

完成路径：
1. GET /v1/agent/skills/123/history → 看到 v1/v2/v3 列表
2. POST /v1/agent/skills/123/restore/2 → 触发回滚
3. 后端：
   - 读 history v2 snapshot JSON
   - 创建新版本 v4，内容 = v2 的快照（含 questionnaire_answers + generated_skill_body）
   - 写 history v4（标注 "从 v2 恢复"）
   - 不删 v3 历史
   - 返回 {id, version=4}
4. 学员立即看到 v4 效果（v2 的内容）
```

### US-4：高级模式切换（5% 头部机构）

```
作为：高级机构的内训团队负责人
当：我有自己设计好的 SKILL.md 想直接用
我想：切到高级模式编辑 prompt 全文

完成路径：
1. POST /v1/agent/skills/123/advanced-toggle → 不可逆切换
2. 后端：
   - 设置 advanced_mode=1
   - custom_skill_body = generated_skill_body（初始拷贝；用户后续可编辑）
   - 保留 questionnaire_answers 不清空（供历史回滚用）
   - version +1，写 history
3. 后续 PATCH /v1/agent/skills/123 body={custom_skill_body: "..."} → 改 prompt 全文
4. Runner 注入：advanced_mode=1 时用 custom_skill_body，否则 generated_skill_body
5. **不可逆约束**：若再次 PUT body={advanced_mode: 0} → DB 层拒绝（CHECK constraint 或 biz 层 ErrAdvancedModeIrreversible）
```

### US-5：父账户运行 Agent（注入路径，跨 feature）

```
作为：张老师的学员小明
当：我在学员工作区选「智学爆款分析师」并发消息"帮我分析这篇笔记"
我想：Agent 按张老师配置的风格回答

完成路径（v1 部分由 #10/#11 完成 UI；本 feature 完成 biz）：
1. 学员前端 → POST /v1/agent/runs body={agent_definition_id: 123, input: "..."}
2. 后端 AgentRunner.Run：
   - 读 agent_definition 123（含 parent_user_id 校验：学员的 parent_user_id 必须 = agent 的 parent_user_id）
   - 选 effective_body：advanced_mode=1 ? custom_skill_body : generated_skill_body
   - 装配 system prompt（按蓝本 §4.3.9 完整顺序，P1-3 修复）：
     ```
     [1] PLATFORM_BASE_PROMPT         (本 feature 常量)
     [2] tenant_hard_rules            (#13 提供；本 feature placeholder = 空字符串)
     [3] effective_body               (本 feature 注入)
     [4] memory.SystemBlock           (#7 提供；本 feature placeholder = 空字符串)
     [5] tools_section                (#3 Tool Registry 提供；本 feature 调 registry.RenderToolsSection(tool_flags) 生成；
                                        若 #14 前没有真实 render API，则 placeholder = 空字符串，留 TODO)
     [6] PLATFORM_SAFETY_FOOTER       (本 feature 常量)
     ```
   - 传入 adapter 调 LLM（#14 完整落地真实 LLM；本 feature 让 prompt 能被读到注入）
3. 返回 streaming response
```

### US-6：hook 终止学员会话（hook 真实落地）

```
作为：合规规则（#13 落地）
当：学员的输入触发某个 PreToolCall hook 检查（如试图调用未授权工具）
我想：会话立即终止，terminal_reason=hook_stopped

完成路径（本 feature 仅做 hook 信号传播；hook 真实合规规则在 #13）：
1. AgentRunner.Run → einoAgent.Generate → adapter.InvokableRun
2. adapter.InvokableRun → PreToolCall(ctx, t, args) → HookActionStop
3. adapter 把 HookAction 写入 `hooks.Registry.Record(action)`（**P0-2 已定方案：hooks-bound**，见 §5.2）
4. adapter 仍返回 fmt.Errorf 让 Eino 终止
5. einoAgent.Generate 返回 error → runner.Run catch
6. runner.Run 查 `hooks.Registry.LastAction()` → HookActionStop
7. 调 state.Transition(LoopEventHookActionStop) → terminal_reason = "hook_stopped"
8. UpdateState(agent_run) → status=terminated, terminal_reason=hook_stopped
9. 学员前端收到 terminal_reason → UI 显示"会话被规则终止"

**对 PostToolCall 同样覆盖**（P0-3 修复）：adapter 改造时调 `_postAction, postErr := hooks.PostToolCall(...)`，若 postAction != HookActionContinue 则 `hooks.Registry.Record(postAction)`。当前 `adapter_full_to_eino.go:69` 的 `_, postErr :=` 模式必须改为捕获 action 并写 Registry。
```

---

## 3. PRD 详细行为规格

### 3.1 创建 Agent（POST /v1/agent/skills）

**Request body**：

```json
{
  "name": "智学爆款分析师",                     // Q1, 必填, 2-20 字
  "description": "帮你分析小红书笔记找出爆款规律",   // Q3, 必填, 10-100 字
  "icon_url": "/icons/robot-01.png",          // Q2, 可选, 上限 2MB
  "welcome_message": "你好！...",              // Q4, 必填, 20-500 字
  "starters": ["帮我分析这周笔记", "..."],       // Q5, 可选, 最多 4 条
  "questionnaire_answers": {
    "q6": ["analyze_data", "answer_questions"],  // 多选 checkbox
    "q7": ["text", "image"],                     // 多选 checkbox
    "q8": 800,                                    // 滑块, 200-2000
    "q9": "no_web_search",                        // radio
    "q10": "不讨论竞品 X / 不讨论退款",            // 多行可选
    "q11": "这个问题超出我能力范围...",            // 多行可选
    "q12": "encouraging"                          // radio: friendly / professional / encouraging
  },
  "tool_flags": {                                 // 由 Q6/Q7/Q9 反推默认值
    "code_sandbox": false,
    "media_processing": true,
    "web_search": false
  },
  "credit_cap_per_session": 800,
  "daily_credit_cap": 5000,
  "source_template_id": 1                         // 可选，模板派生
}
```

**Server processing**：

```
1. 验参（Gin binding 仅校验顶层字段如 name 长度，questionnaire_answers 是 JSON 字段无法靠 binding 校验其内部 Q6/Q7/Q12 必填）
   P2-3 修复：questionnaire_answers.q6/q7/q12 必填项在步骤 4.b biz 层 skill_builder 校验，触发 ErrSkillBuilderFailed (422)
2. JWT 提取 userID → 查 user 表得 user.ParentUserID（*uint，nil = 父账户；非 nil = 子账户）→ 子账户调用直接 403
3. 调 biz/skill/service.Create(ctx, userID, req)
4. biz 内部：
   a. 拼装 model.AgentDefinition{parent_user_id=userID, name=..., ...}
   b. 调 skill_builder.Build(questionnaire_answers) → generated_skill_body
      （内部校验 Q6/Q7/Q12 必填；缺失 → 返回 ErrSkillBuilderFailed）
   c. db.Create(&agent) — UpdateColumn fixup 处理 is_active
   d. db.Create(&history{agent_id, version=1, snapshot=full_row_json})
5. 返回 200 {id, version: 1, generated_skill_body}
```

**Error responses**：

- 400：参数验证失败（如 Q1 长度不符）
- 401：未带 user_token
- 403：子账户调用（parent_user_id IS NOT NULL）
- 422：业务规则违反（如 questionnaire_answers JSON 缺必填字段）
- 500：DB 故障

### 3.2 列表（GET /v1/agent/skills）

**Query params**：
- `page` (default 1)
- `page_size` (default 20, max 100)
- `include_inactive` (default false) — 是否包含 is_active=0 软删除

**Server processing**：
```
1. JWT 提取 userID
2. 子账户调用 → 403
3. 查询 WHERE parent_user_id = userID AND (include_inactive ? true : is_active=1)
4. 分页 返回 {list: [...], total: N}
```

返回列表元素结构（精简版）：
```json
{
  "id": 123,
  "name": "智学爆款分析师",
  "description": "...",
  "version": 4,
  "is_active": true,
  "advanced_mode": false,
  "source_template_id": 1,
  "created_at": "2026-05-22T10:00:00Z",
  "updated_at": "2026-05-22T15:00:00Z"
}
```

**不返回**：`generated_skill_body` / `custom_skill_body` / `questionnaire_answers` JSON 体（列表瘦身，详情接口才返）

### 3.3 详情（GET /v1/agent/skills/:id）

返回单个 agent 完整结构（含所有 questionnaire_answers + skill body）。

**软删除处理**（P1-2 修复）：**详情接口永远不过滤软删除**（is_active=0 仍返回）。客户端按 response.is_active 字段判断状态。这与 §3.5 历史接口保持一致语义（回滚流程需要先 GET 软删除 agent 详情）。

404 仅当：(a) id 不存在；或 (b) agent 不属于当前 userID（即 `parent_user_id != JWT.userID`）。

### 3.4 更新（PATCH /v1/agent/skills/:id）（P1-1 修复：partial update 走 PATCH 符合 api-design.md §1）

**Request body**：与创建几乎一样，但：
- 所有字段都可选（partial update）
- 缺失字段视为不变
- `advanced_mode` 不能在 PATCH 中直接改（必须走 /advanced-toggle 端点）
- `parent_user_id` 不可改（永远 = JWT.userID）
- `is_active` 不能通过 PATCH 改（软删除走独立 DELETE 端点，见 §3.9）

**Server processing**：
```
1. 取 db.First(&agent, id) → 校验 parent_user_id 一致
2. 应用 patch
3. 如果 questionnaire_answers 改了 → 重算 generated_skill_body
4. version +1
5. db.Save(&agent) — 注意 db.Save 对 default:true bool zero-value 安全（见 database.md §6b）
6. 写 history snapshot
7. 返回 200 {id, version, updated_at}
```

### 3.5 历史版本列表（GET /v1/agent/skills/:id/history）

**关键：包含已软删除 agent**（P1-3 修复）

```sql
SELECT * FROM agent_definition_history WHERE agent_id = ? ORDER BY version DESC;
-- 不 JOIN agent_definition 避免软删除过滤
```

返回 [{version, created_by, created_at, changes_summary}]

**`changes_summary` 字段格式约定**（P2-1 修复）：
- 类型：简单字符串（不是 JSON diff，不是字段名列表）
- 内容样例："修改了 Q12（说话风格）" / "首次发布" / "从 v2 恢复" / "切换到高级模式" / "软删除"
- 生成算法：biz 层 versioning.computeChangesSummary(prevSnapshot, newSnapshot)
  - 首次发布 → "首次发布"
  - 切高级模式 → "切换到高级模式"
  - restore 路径 → "从 v{N} 恢复"
  - 软删除 → "软删除"
  - 一般修改 → 列出 Q 编号变化（如 "修改了 Q12（说话风格）, Q6（任务类型）"）
- 字段长度上限 200 字符

### 3.6 回滚（POST /v1/agent/skills/:id/restore/:version）

```
1. 校验 parent_user_id
2. 读 history WHERE agent_id=:id AND version=:version → 拿 snapshot
3. 解析 snapshot JSON → AgentDefinition struct
4. 取 max(version) FROM history WHERE agent_id=:id → new_version = max+1
5. db.Save(&agent) — id 不变，内容 = snapshot
6. 写 history snapshot.version = new_version, snapshot.notes = "从 v{version} 恢复"
7. 返回 200 {id, version: new_version}
```

### 3.7 切换高级模式（POST /v1/agent/skills/:id/advanced-toggle）

**不可逆**。

```
1. 校验 parent_user_id
2. db.First(&agent, id)
3. 如果 advanced_mode==1 → 返回 422 ErrAlreadyInAdvancedMode（已经在高级模式）
4. agent.advanced_mode = 1
5. agent.custom_skill_body = agent.generated_skill_body  // 拷贝初始值
6. version +1
7. db.Save
8. 写 history snapshot（标注 "切换到高级模式"）
9. 返回 200 {id, version}
```

### 3.9 软删除 Agent（DELETE /v1/agent/skills/:id）（P1-4 修复 — 加 DELETE 端点）

**幂等**：已是 is_active=0 仍返回 200（不报错）。

```
1. 校验 parent_user_id
2. db.Model(&agent).Where("id=?", id).UpdateColumn("is_active", false)
   （直接用 UpdateColumn 避免 default:true 踩坑 + 不触发 updated_at 更新）
3. version +1，写 history snapshot
4. 返回 200 {id, is_active: false}
```

**注意**：DELETE 是软删除（不删 DB 行）。所有历史版本永久保留，历史接口照常可查。

**ErrSkillNotFound 触发**：id 不存在 / parent_user_id 不匹配 → 404。
**已 is_active=0 不触发 Err**：幂等。

### 3.8 内置模板列表（GET /v1/agent/skill-templates）

```
SELECT * FROM skill_template WHERE is_active=1 ORDER BY display_order ASC;
```

返回 [{id, name, description, icon_url, category_tags, preview_questionnaire_answers, default_tool_flags}]
（preview 是部分字段，方便 #10 前端展示）

**P2-2 修复 — `skill_template` 表字段必须包含**（S2 spec 落表时验证）：
- `id` BIGINT UNSIGNED PRIMARY KEY
- `name` VARCHAR(50) NOT NULL
- `description` VARCHAR(300)
- `icon_url` VARCHAR(512)
- `category_tags` JSON (`["小红书运营", "数据分析"]`)
- `questionnaire_answers` JSON (完整 12 题预填)
- `default_tool_flags` JSON
- `display_order` INT (展示顺序)
- `is_active` TINYINT(1) DEFAULT 1
- `created_at` / `updated_at`

response 中 `preview_questionnaire_answers` = questionnaire_answers 的精简版（仅 Q1/Q3/Q6 三题做预览）。

**鉴权**：user_token 必填（P2-5 一致性）

---

## 4. 失败模式与错误码

| 错误码 | HTTP | 触发层 | 触发场景 |
|---|---|---|---|
| ErrSkillNameInvalid | 400 | binding | Q1 长度不符 |
| ErrUnauthorized | 401 | middleware | 缺 token |
| ErrChildAccountForbidden | 403 | biz | 子账户调用 |
| ErrSkillNotFound | 404 | biz | id 不存在 / parent_user_id 不匹配 |
| ErrSkillBuilderFailed | 422 | biz/skill/service.go | questionnaire_answers 缺必填字段 / Q6 / Q7 / Q12 必填项为空（P2-3 修复） |
| ErrAdvancedModeIrreversible | 422 | biz | 试图把 advanced_mode 改回 0 |
| ErrSkillVersionNotFound | 404 | biz | restore 指定不存在的 version |
| ErrTemplateNotFound | 404 | biz | source_template_id 不存在 |
| ErrDBOperationFailed | 500 | store | DB 故障 |

错误码常量定义在 `internal/pkg/errno/skill.go`（新文件）。

---

## 5. 跨 feature 接口

### 5.1 AgentRunner.Run 接口扩展（新增）

```go
type RunRequest struct {
    UserID            uint
    SessionID         string
    Input             string
    ToolNames         []string
    Hooks             *RunHooks
    AgentDefinitionID uint64        // <-- 新增；0 时 fall through #2 mock 行为
}

type RunResult struct {
    // ... 现有字段
    SkillVersion       int     // <-- 新增；本次注入的 skill version
                               // P1-5 修复：AgentDefinitionID=0 时 SkillVersion=0；
                               // 表示"未注入 Skill（fall through 路径）"
                               // 现有 #2 测试不修改；新增测试验证 AgentDefinitionID>0 → SkillVersion>0
}
```

### 5.2 Hook 信号传播（**hooks-bound** 方案，P0-2 已定）

**Old**: `adapter` 把 HookAction → fmt.Errorf → runner 只能看到普通 error，丢失 HookAction 类型信息。

**New**（hooks-bound — Registry 嵌入 RunHooks）：

```go
// internal/numind/biz/agent/hooks.go (扩展)
type HookActionRegistry struct {
    last atomic.Int32  // 单一 last action；race-safe
}

func NewHookActionRegistry() *HookActionRegistry { return &HookActionRegistry{} }
func (r *HookActionRegistry) Record(action HookAction) { r.last.Store(int32(action)) }
func (r *HookActionRegistry) LastAction() HookAction { return HookAction(r.last.Load()) }

type RunHooks struct {
    PreToolCall  func(ctx context.Context, t tool.BaseTool, input string) (HookAction, error)
    PostToolCall func(ctx context.Context, t tool.BaseTool, output string, err error) (HookAction, error)
    Registry     *HookActionRegistry  // <-- 新增字段；NewAgentRunner 自动 init 若 hooks 不为 nil
}

// adapter_full_to_eino.go (修改)
// InvokableRun:
//   action, err := hooks.PreToolCall(...)
//   if hooks.Registry != nil { hooks.Registry.Record(action) }  // <-- 新增
//   if action != HookActionContinue { return "", fmt.Errorf("tool execution stopped by hook: action=%d", action) }
//
//   ... Execute ...
//
//   postAction, postErr := hooks.PostToolCall(...)
//   if hooks.Registry != nil && postAction != HookActionContinue { hooks.Registry.Record(postAction) }  // <-- 新增 (P0-3 修复)

// runner.go (修改)
// Run() 在 einoAgent.Generate() 返回 error 时:
//   if hooks != nil && hooks.Registry != nil {
//       lastAction := hooks.Registry.LastAction()
//       if event := HookActionToLoopEvent(lastAction); event != LoopEventInvalid {
//           state.Transition(event) → 派发正确 terminal_reason
//       }
//   }
```

**为什么选 hooks-bound 而非 ctx-bound**：
1. adapter 不需要 ctx-key 查找 / 注入
2. registry 由 NewAgentRunner 工厂初始化，自动绑到 hooks
3. 测试时可以独立 mock registry 验证 Record 调用

**race-safe 保证**：atomic.Int32 提供 lock-free read/write；testRun -race 必过。
**多工具并发调 Record 的覆盖语义**：Record 是"最后赢家"语义，符合 hook 终止流程（任何 hook Stop 都触发 terminal，先后顺序不重要）。

### 5.3 Skill body 注入（**选项 A 已定**：RunRequest 加 SystemPrompt 字段）

**P2-4 修复 — S1 拍板**：

**选项 A**（采用）：
```go
type RunRequest struct {
    // ... 现有字段
    SystemPrompt string  // <-- 新增；runner.Run 内部装配后传入；adapter 是无状态 stateless
}
```

**理由**：
1. adapter 不需暴露可变 state（不变性原则）
2. adapter setter 调用顺序敏感（必须在 Generate 前）
3. runner.Run 是唯一调用点，加字段更易测
4. 与 RunRequest 现有的 Input / ToolNames 等"输入参数"风格一致

**选项 B 拒绝**：adapter SetSystemPrompt 方法需要严格调用顺序（先 set 再 Generate），易写错；现已无业务场景需要 runtime 动态改 system prompt。

---

## 6. UX 流程图（描述性）

> v1 仅后端 API；UI 流由 #10 完整实装。本节只描述 API 调用顺序，便于前端实施时参考。

### 6.1 模板派生流程

```
父账户登录
  ↓
GET /v1/agent/skill-templates
  ↓
选模板（前端高亮）
  ↓
POST /v1/agent/skills body={source_template_id: K, name: "自定义名"}
  ↓
返回 {id: 123, version: 1, generated_skill_body: "..."}
  ↓
前端跳转到 Agent 详情页（GET /v1/agent/skills/123）
```

### 6.2 修改 + 回滚流程

```
GET /v1/agent/skills/123 → 显示当前 v4 完整配置
  ↓
父账户改 Q12 → PATCH /v1/agent/skills/123 → v5
  ↓
父账户发现 v5 不好 → GET /v1/agent/skills/123/history → 显示 v1-v5 列表
  ↓
父账户点 "恢复 v4" → POST /v1/agent/skills/123/restore/4 → v6 内容=v4
```

### 6.3 高级模式流程

```
GET /v1/agent/skills/123 → advanced_mode=false
  ↓
父账户点 "切到高级模式" → POST /v1/agent/skills/123/advanced-toggle → v5
  ↓
返回 advanced_mode=true, custom_skill_body=<copy of generated_skill_body>
  ↓
父账户编辑 custom_skill_body → PATCH /v1/agent/skills/123 → v6
  ↓
父账户后悔 → GET /v1/agent/skills/123/history → POST /restore/4
  ↓
新 v7 内容 = v4（advanced_mode=false 状态）→ 实质等于"撤回高级模式切换"
```

---

## 7. 测试策略

详见 S0 验收条件（§3 工件 + 测试）。S3 plan 会按 task 粒度展开。

**关键测试覆盖矩阵**：

| 模块 | 单测 | 集成测 |
|---|---|---|
| skill_builder.Build | 12 Q 题映射 + edge case（缺字段 / 空多选）| — |
| versioning.WriteHistory | append-only / 软删除后仍可查 | — |
| versioning.Restore | 新版本号 = max+1 / 旧版本保留 | — |
| store IAgentDefinitionStore | 含 Update 后字段持久化（含 `default:true` bool） | — |
| Hook signal propagation | adapter → hooks.Registry.Record(atomic) → runner.LastAction() → state event | race-safe |
| API 8 端点 | — | happy + 401/403/404/422 |
| advanced_mode 不可逆 | biz 层 | 集成 PUT advanced_mode=0 → 422 |
| skill body 注入 | runner 装配 system prompt | — |

---

## 8. 不在 S1 范围

留给 S2 spec / S3 plan 决定的事：

- 具体 GORM model 字段类型 / 索引（S2 schema 设计）
- 具体的 task 数量与依赖关系（S3 plan）
- 平台 PLATFORM_BASE_PROMPT 文案（S2 常量值）
- skill_builder 组装算法的精确字符串模板（S2）
- 10 个模板的具体问卷答案预填（S2 seed SQL）

---

## 9. PRD 与需求卡的差异

S1 在 S0 基础上**精化**：
- 加 6 个用户故事（US-1 ~ US-6）
- 加 8 个 API 端点的 PRD 详细 schema
- 加错误码表
- 加跨 feature 接口设计
- 加 UX 流程图

S0 中的"业务范围 / 验收条件 / 风险"未删未改，S1 与 S0 兼容。

---

**S1 完结。S2 写技术 spec（DB schema 详细 + biz 子包 API 详细 + 测试矩阵详细）。**
