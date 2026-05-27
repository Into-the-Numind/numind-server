# NDF S1 Proposal · `agent-prompt-simplification`

**Status**: S1 草案 · 2026-05-28
**关联 S0**: `requirements/agent-prompt-simplification.md`

---

## 1. 产品定位

### 1.1 核心命题

**"机构方应该只看到一个大文本框 + 几个可挂的能力。"**

类比：ChatGPT 的 GPTs 编辑器 / Claude Projects 的 instructions / Anthropic Workbench——业界已收敛到这套设计模式。本 feature 把莫小派 v2 agent 的拼装结构改造到同一收敛点。

### 1.2 三层架构在 prompt 层的体现

| 层 | 谁能写 | 在 prompt 哪里 | 是否缓存 |
|---|---|---|---|
| 平台 | 平台方（zhiyuchen）—— 通过常量/代码改 | §1 平台头（极简）/ §4 安全脚注 | ✅ ephemeral |
| 机构 | 机构方（父账户）—— 通过 AgentBuilder UI 写 | §2 agent.system_prompt | ✅ ephemeral |
| 端用户 | 系统自动收集（学员对话/记忆/时间） | §3 端用户上下文 | ❌ 每轮变 |

**关键设计**：cache breakpoint 落在 §3 之前。§1+§2+§4 三段是 session 内"稳定底座"，自动复用 cache；§3 每轮变化，承担"动态注入"。

---

## 2. 用户故事

### 2.1 机构方（B2B2C 父账户）的体验路径

```
进 AgentBuilder
  ↓
看到表单：[名称] [描述] [欢迎语] [行为指引 textarea] [挂载技能] [挂载知识库]
  ↓
在"行为指引"里写：
  「你是【XX 公司】的销售助手。
   职责：帮销售应对客户异议、提供推单话术。
   规则：不主动报价、涉及投诉转人工、用专业但亲和的语气。」
  ↓
点保存
  ↓
完成（30 秒搞定一个 agent 的基础人格）
```

不需要知道："PlatformBasePrompt / PlatformSafetyFooter / skillCatalogBlock 是什么" —— 平台层隐藏。

### 2.2 端用户（B2B2C 学员）的体验路径

完全不变。学员跟 agent 对话的体验 / 接口 / 流式响应 / 工具调用都保持原样。本 feature 是**机构方侧的内部改造**，不影响端用户层 UX。

### 2.3 平台方（zhiyuchen）的体验路径

- 调整平台头/脚注：改代码常量（与现在相同）
- 加新平台级规则：在 `§4 PlatformSafetyFooter` 字符串里加一行（与现在相同）
- 观察 agent 实际拼装结果：仍然通过 Langfuse trace 查看（与现在相同）

---

## 3. 与现状的差异（结构对比）

### 3.1 现状（6+ 段）

```
PlatformBasePrompt              (§1)
+ tenantHardRules               (§2，多数空)
+ skillCatalogBlock             (§3)
+ memoriesSectionHeader         (静态标题)
+ agentMdBlock                  (§3a)
+ selectorBlock                 (§3b)
+ dialecticInsightBlock         (§3c)
+ temporalBlock                 (§3d)
+ memoryDisclaimerBlock         (§4a 免责)
+ memorySystemBlock             (§4b 对话历史)
+ toolsSectionPlaceholder       (§5 工具说明 ← 删)
+ PlatformSafetyFooter          (§6)
```

### 3.2 目标（4 段）

```
§1 平台头 (PlatformBasePrompt, 1-2 行)
                          ⬇ [cache breakpoint A]
§2 机构层 (agent.system_prompt + skillCatalogBlock + agentMdBlock 合并)
                          ⬇ [cache breakpoint B]
§3 端用户上下文 (selector + dialectic + temporal + memory + disclaimer 合并)
                          ⬇ no cache (每轮变)
§4 平台安全脚注 (PlatformSafetyFooter, 3-5 行精简)
                          ⬇ [cache breakpoint C，可选]
```

被合并/删除：
- `memoriesSectionHeader` 标题：保留作为 §3 内部标题，对外不再是独立段
- `toolsSectionPlaceholder` / `AvailableToolsAddendum` / `OutputToolsPriorityAddendum`：**全删**，工具靠 API `tools[]` 描述
- `tenantHardRules`：合并进 §2 末尾（如未来需要 L0 hook，可在 §2 注入）

### 3.3 兼容性

新拼装函数签名：

```go
func BuildSystemPrompt(ad *AgentDefinition, skills []Skill, userCtx UserContext) string
```

- `ad.SystemPrompt != ""` → 走新 4 段路径
- `ad.SystemPrompt == ""` → 走旧拼装路径（不重写，保持现有 6+ 段逻辑）

老 agent 不动 = 不破。新 agent 默认走新路径。机构方什么时候首次在 UI 给 `system_prompt` 填值，agent 自然切到新路径。

---

## 4. 关键决策（消化 S0 P1）

### D1（已定）：老 agent 默认 `system_prompt = ''`，fallback 老路径

- DB migration 不 backfill
- 不主动迁移
- 老 agent 行为完全一致（fallback 走旧函数，旧函数代码保留）

### D2（已定，附前提确认）：保留 `use_skill` 元工具 + system prompt 文本列名

**为什么这是对的**（S0 P1 补充）：
- 本系统当前**没有 per-skill API tool 注册机制**——`internal/numind/biz/agent/tools/registry.go` 注册的是固定一组系统工具（use_skill / kb_search / memory_write / read_artifact 等）。把每个技能注册成独立 OpenAI function 需要重构整个 registry，与本 feature 范围不相称。
- 即使重构后改成多 tool，Claude Code 的实证表明 meta-tool 在技能数量增长时反而更友好（cache 一段 catalog 文本，比 cache N 个 tool schemas 更省 token）。
- 因此 meta-tool 设计是**架构约束 + 主动选择**双重确认，本 feature 维持不动。

### D3（已定）：删 `toolsSectionPlaceholder` 段

- `AvailableToolsAddendum`：删
- `OutputToolsPriorityAddendum`：删（如确实需要"工具优先级提示"，移到 §2 文本里由机构方写，或者放进单个工具的 `description`）
- `compactv2.ReadArtifactSystemPromptAddendum`：作为该工具的 `description` 字段，**不再注入 system prompt**

### D4（新决策）：cache breakpoint 策略

- `breakpoint A`（§1 后）：标记 §1 为 cacheable
- `breakpoint B`（§2 后）：标记 §1+§2（含 skill catalog）为 cacheable
- `breakpoint C`（§3+§4 后）：可选——理论上 §4 也是稳定的，但 §3 跟 §4 之间不能放 cache（§3 每轮变）。所以 §4 不缓存（每轮重发，但只 3-5 行可接受）。

Anthropic API 的 prompt caching 最多 4 个 breakpoint，本 feature 只用 2 个（A 和 B）。

### D5（新决策）：后端字段长度校验（S0 P1 补充）

- DB：`system_prompt MEDIUMTEXT`（16MB 上限，过大）
- 前端软限：16KB（约 4000 字，足够写完整 prompt）
- 后端校验：`biz/agent` create/update 时如 `len(system_prompt) > 65536` 直接 reject，返回 `ErrSystemPromptTooLong`（错误码新增）
- 64KB 上限给 future-proof（远高于前端软限），但拦住恶意超大 payload

---

## 5. 成功指标（可观测）

### 5.1 必达

| 指标 | 测量方法 | 目标 |
|---|---|---|
| 现有 agent 不破 | S5 跑现有 agent 的 E2E 套件全过 | 100% pass |
| 新 agent 走新路径 | Langfuse trace 检查 system prompt 实际内容 | 4 段结构 |
| AgentBuilder 新字段 UI 可用 | Playwright 跑机构方"填字段→保存→读取"路径 | green |

### 5.2 可量化

| 指标 | 基线 | 目标 |
|---|---|---|
| system prompt token 数 | 当前 6 段约 X tokens（**S5 建立 baseline**） | 减少 ≥30% |
| prompt cache hit ratio | 当前 unknown（**S5 建立 baseline**） | session 内 ≥80% |

⚠️ **S0 P1 修正**：基线数字 unknown，S5 必须先建 baseline 再对比验收，否则"提升 30%"无法判定。

### 5.3 软性目标（不强制验收）

- 机构方上手时间 < 5 分钟（无法量化，但产品观察）
- 机构方反馈："agent 配置变简单了"（无法量化，长期观察）

---

## 6. 风险与缓解

| 风险 | 严重度 | 缓解 |
|---|---|---|
| R1 fallback 与新路径语义差异 | 高 | S2 设计强制旧函数代码 1:1 保留，新函数独立文件；S5 跑对比测试（空 system_prompt 的 agent 在新代码下应得到完全相同输出） |
| R2 cache_control 配错 | 中 | S2 明确 breakpoint 位置；S5 用 Langfuse `cache_creation_input_tokens` / `cache_read_input_tokens` 验证 |
| R3 机构方写越权 prompt | 低 | §4 安全脚注 + 现有 L0 合规 hook 兜底；本期不引入新防御；Langfuse 监控发现明显问题再 patch |
| R4 后端字段被超大 payload 攻击 | 低 | D5 后端校验 64KB 上限 |
| R5 前端 textarea 体验差 | 低 | 用普通 textarea，加 placeholder 引导；试运行视图留给后续 feature |

---

## 7. 范围边界（再次澄清）

- ✅ 改 prompt 拼装结构 + 加 `system_prompt` 字段
- ✅ AgentBuilder 加 textarea
- ✅ 后端校验长度
- ✅ Migration（加字段、空默认）
- ❌ 试运行视图（独立 feature）
- ❌ AI 帮写 prompt 按钮
- ❌ 机构方模板库
- ❌ permission pipeline 重构（已在 `agent-mode-permission-pipeline` feature 推进）
- ❌ v1 chatbot 路径

---

## 8. S2 / S3 入场清单

S2 spec 需要明确：
1. `BuildSystemPrompt` 函数签名与实现路径
2. `cache_control` 在 aiservice adapter 哪一层注入
3. Migration SQL（idempotent）
4. 新增 errno 错误码（`ErrSystemPromptTooLong`）
5. AgentBuilder UI 表单字段位置 + placeholder 文案
6. 兼容性测试用例清单

S3 plan 需要明确：
1. 任务拆分（推荐：Migration / 后端拼装重写 / 后端 API 校验 / 前端 UI / 端到端测试 5 个任务）
2. 文件归属表（disjoint 验证）
3. S5 验证策略（**必须包含 baseline 建立步骤**）
