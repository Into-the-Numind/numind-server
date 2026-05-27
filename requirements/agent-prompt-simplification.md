# NDF S0 Requirement Card · `agent-prompt-simplification`

**Track**：Standard
**Feature ID**：`agent-prompt-simplification`
**起草日期**：2026-05-28
**起草人**：AI（autopilot · 用户对话需求）
**状态**：S0 草案
**依赖**：
- 跟 `agent-mode-v2-skill-as-artifact`（merged）共享 `skill_artifact` 表与 `skillBindingService`
- 跟 `agent-mode-v2-skill-marketplace`（merged）共享 catalog 注入入口
- 不依赖 `agent-mode-permission-pipeline`（独立改造，未来可叠加）

**阻塞**：暂无

---

## 1. 起因（Why now）

用户在 2026-05-28 对话中明确表达：**当前 agent 的 system prompt 拼装结构太碎，机构方（B2B2C 第二层）无法低成本配置 agent**。

### 1.1 当前结构的实证问题

调研 `internal/numind/biz/agent/runner.go:427-682` 的现状：拼装 6+ 段，命名混乱（`PlatformBasePrompt` + `tenantHardRules` + `skillCatalogBlock` + `agentMdBlock` + `selectorBlock` + `dialecticInsightBlock` + `temporalBlock` + `memoryDisclaimerBlock` + `memorySystemBlock` + `toolsSectionPlaceholder` + `PlatformSafetyFooter`）。机构方在 UI 里看到的只有 name / description / welcome_message + 挂技能——**没有任何一个字段能让机构方真正写入"这个 agent 是谁、怎么说话、能聊什么、不能聊什么"**。

后果：
- 机构方要塞"人格"只能曲线救国——自己写一个 skill 把人格描述塞进去
- `agent.description` 字段没进 prompt（只是 UI 摆设）
- "段 5 工具说明"和 OpenAI function-calling 的 `tools[]` 字段重复
- "PlatformSafetyFooter" 想做平台兜底，但安全防线应该在 `checkPermissions` 里而不是 prompt 文本里

### 1.2 与 Claude Code 设计的对照

读 `/Users/zhiyuchen/Downloads/ClaudeCode/src` 源码后确认（与本会话 2026-05-28 调研一致）：
- `use_skill` 元工具设计是**对的**，Claude Code 的 SkillTool 用同一套（[`tools/SkillTool/SkillTool.ts:291`](file:///Users/zhiyuchen/Downloads/ClaudeCode/src/tools/SkillTool/SkillTool.ts)）
- 但工具描述**完全不进 system prompt**，全靠 API `tools[]` 字段（[`services/api/claude.ts:1358`](file:///Users/zhiyuchen/Downloads/ClaudeCode/src/services/api/claude.ts)）
- 安全防线在每个工具的 `checkPermissions()`，而不是 prompt footer 文本

---

## 2. 业务范围

### In scope

#### 2.1 三层架构落地到 prompt 结构

平台 / 机构 / 端用户三层互相隔离，对应 prompt 4 段：

```
§1 平台头（写死，1-2 句，启用 prompt cache）
§2 机构方写的 system_prompt（机构方在 AgentBuilder 写的大文本框，启用 prompt cache）
§3 端用户上下文（记忆+对话历史，每轮变化，不缓存）
§4 平台安全脚注（写死，3-5 行，启用 prompt cache）
```

技能 catalog 仍在 system prompt 内（与 §2 同段或紧贴 §2），但用 `cache_control: ephemeral` 标记，让 session 内多轮共享缓存。

#### 2.2 DB schema 变更

- `agent_definition` 新增 `system_prompt MEDIUMTEXT NOT NULL DEFAULT ''`
- 不动 `generated_skill_body` / `custom_skill_body`（保留向后兼容，老 agent 不破）

#### 2.3 prompt 拼装重写

- `internal/numind/biz/agent/runner.go` 拼装函数从 6+ 段重构为 4 段
- 删除"工具说明"段（依赖 API `tools[]`）
- 缩短 `PlatformSafetyFooter` 至 3-5 行核心规则
- 把 `selectorBlock` / `dialecticInsightBlock` / `temporalBlock` / `memorySystemBlock` 合并为 §3「端用户上下文」一段（内部多源仍可，对外是一段）
- skill catalog 块挂在 §2 末尾，与 §2 一起 prompt-cache

#### 2.4 向后兼容

- 老 v1 chatbot 路径（`biz/chatbot/`）不动
- v2 agent 路径：`system_prompt != ''` 时用新结构；`system_prompt == ''` 时 fallback 到老路径（拼接 `generated_skill_body` / `custom_skill_body`），保证现有 agent 不破
- 老 agent 在前端首次编辑时**不强制迁移**到新字段

#### 2.5 前端 AgentBuilder UI

- 在表单关键位置加 `system_prompt` textarea（标签："智能体行为指引"或类似友好措辞）
- placeholder 引导写"角色 / 职责 / 边界 / 语气"
- 字符上限 16KB（前端软限）
- 旧 agent 在 UI 上正常显示空 textarea，机构方可填可不填

### Out of scope（明确排除）

- **"试运行"调试视图**：单独 feature 做，本期不上
- **AgentBuilder 分步向导改造**：保留当前一页式表单，只加字段
- **"AI 帮我写 prompt" 按钮**：单独 feature
- **机构方模板库**：单独 feature
- **`checkPermissions()` 加固**：单独 feature（继承自 `agent-mode-permission-pipeline` 进度）
- **跨 agent 记忆隔离配置**：单独 feature
- **chatbot（v1）路径改造**：v1 是老 chatbot，不动
- **skill catalog 重构成多 tool**：上面调研已确认 meta-tool 设计是对的，保留

---

## 3. 用户行为路径（"机构方建 agent 的 happy path"）

1. 机构方进 AgentBuilder
2. 填名称 / 描述 / 欢迎语
3. **在新增的"行为指引"大文本框里写**："你是 XX 公司销售助手，职责...规则...语气..."
4. 挂载技能（已有 UI）
5. 保存
6. 端用户用这个 agent 时，LLM 收到的 system prompt 是：平台头 + 机构方写的指引 + 挂的 skill catalog + 端用户记忆 + 平台脚注
7. 机构方无需了解 6 段拼装、无需了解记忆系统内部、无需了解 cache_control

---

## 4. 三个关键决策（待 S1/S2 细化）

- **D1：老 agent 默认 system_prompt 空**——前端展示空 textarea，后端 fallback 到老拼装路径。**不主动迁移**。
- **D2：skill catalog 留在 system prompt**——不下放到 `tools[]` 元数据，仍是 `use_skill(name)` 元工具 + system prompt 文本列名字。理由：与 Claude Code 实践一致；session 内多轮共享 prompt cache 划算。
- **D3：删除 toolsSectionPlaceholder 文本**——`AvailableToolsAddendum` / `OutputToolsPriorityAddendum` / V2 artifact 说明这些段全删，工具元数据靠 API `tools[]` 描述。

---

## 5. 成功指标

- 机构方建一个新 agent，**只需要填一个大文本框**就能定制人格/职责/边界/语气
- LLM 实际收到的 system prompt 比当前缩短 **30%+** token（删除工具说明 + 缩短 footer）
- 不破现有 agent：所有 `system_prompt == ''` 的 v2 agent 运行结果与重构前一致
- prompt cache 命中率提升（§1+§2+§4 三段稳定缓存，§3 每轮变）

---

## 6. 风险

- **R1**：老 agent fallback 路径与新路径的语义可能因合并段落而发生细微差异 → S2 设计要严格保证 `system_prompt == ''` 时 fallback 路径完全沿用现有拼装代码（不重写）
- **R2**：cache_control 配错位置导致 cache miss 反而变贵 → S2 明确 cache breakpoint 位置 + S5 验证用 Langfuse 看 cache hit ratio
- **R3**：机构方写的 prompt 过激（"忽略平台规则"）→ §4 安全脚注 + 现有 L0 合规 hook 兜底，但本 feature 不引入新防御
