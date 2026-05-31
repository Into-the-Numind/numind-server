# NDF S0 Requirement Card · `agent-mode-open-tools-skill-as-guidance`

**Track**：Standard
**Feature ID**：`agent-mode-open-tools-skill-as-guidance`
**起草日期**：2026-05-31
**起草人**：AI（cold-start prompt，用户 2026-05-31 拍板决策）
**状态**：S0 草案
**软依赖**：`agent-tool-schema-infra`（并行 Standard，另一 session）—— 触碰同一批文件（`runner.go` 工具注册段 / `tool_full.go` / `base_tool.go` / `tool_use_skill.go`）。**对方是基础设施，建议先 land；本 feature S4 编码前 `git fetch` + 检查对方是否已动这些文件，必要时 rebase 到其上。** 截至起草时对方未进 manifest、未建分支（尚未 land 任何代码）。
**关联决策**：`.ndf/decisions/skill-progressive-loader/0001-doc-generation-three-system-comparison.md`（ADR 0001，单 loop 范式背书 + 发现 4「Codex skill 不 gate 工具」旁证）；memory `project-agent-mode-tool-open-skill-guidance`（拍板原文）

---

## 0. ⚠️ 起草前的前提校正（重要——影响 scope）

cold-start 简报把「改单 loop（废除外层 invoke_skill → 内层 LLM 双 LLM 切分）」列为本 feature 的待办。**实读源码校正：该工作已于 2026-05-29 落地 develop，本 feature 不含此项。**

| 简报假设 | 实际（已验证，develop HEAD 9706e21f） |
|---|---|
| 现存「外层 invoke_skill → 内层 LLM 读 SKILL.md 写代码」双 LLM 切分，需废除 | ❌ 已不存在。`invoke_skill` 工具 2026-05-29 删除，替换为 `read_skill` + `run_python`（Codex 式单 loop 渐进披露）。有永久回归测试 `skill_progressive_loader_regression_test.go` 钉死该形态 |
| `use_skill` 同 turn 多次调用 `PendingBody` 覆盖（latent bug，本重构自然修掉） | ❌ 已修。commit `94910336` 改为 `PendingSkills []PendingSkill` 累积。spawn 的修复 chip 可 dismiss |
| skill body 通过双 LLM 内层执行 | ❌ 单 loop：`use_skill` body 包 `<system-reminder>` 进 **tool result**（同一 agent 上下文必读）；`read_skill` 返回 SKILL.md 原文给同一 agent。两者都是单 loop |

**校正后本 feature 真实剩余范围**（更窄）：删权限层 + `use_skill`/`read_skill` 合并为 `load_skill` + `allowed_tools` 改语义。单 loop 不动（已就绪，只需确保合并不回归它）。

---

## 1. 起因（Why now）

### 1.1 权限层是冗余的产品约束，不是安全机制

`agent-mode-v2-skill-invocation`（2026-05-24）建了一套 **deny-by-default 工具权限模型**：

- runtime 启动只装入 agent 配置的「基础工具白名单」（`req.ToolNames`）+ `use_skill`
- 所有「skill 私有工具」（binding 的 `allowed_tools` 并集）预注册到 Eino，但被 `UseSkillTurnScope` validator（permission hook 第 8 个）默认 **deny**
- LLM 必须先 emit `use_skill("X")`，该 turn 内才把 X 的 `allowed_tools` 加进 `turn.AllowedTools` 放行
- 见该 feature 的 AC-3（turn-scope 临时扩展）+ AC-10（启动只装 base + use_skill）

用户 2026-05-31 拍板：**这套权限层与另三层安全机制冗余，删掉，走 Codex 全开模型。**

| 安全层 | 管什么 | hook chain 位置 | 本 feature |
|---|---|---|---|
| compliancegate | 内容合规（L0/L1，输入/输出/工具调用过滤） | compliance（最外） | **保留不动** |
| **permission（UseSkillTurnScope）** | **工具是否「启用」（人设边界）** | permission | **❌ 删除（本 feature）** |
| budgetgate | 成本（Reserve/Reconcile，turn 预算） | budget | **保留不动** |
| sandbox + bashvalidator | 代码执行能干啥（8 个 P0 Bash 检查器：rm -rf / curl\|bash 等） | sandbox | **保留不动** |

permission 层**唯一独占作用** = 「塑造 agent 人设边界」（让销售 FAQ agent 够不到 bash）。这是**产品约束，非安全约束**。用户明确不要这个约束——Codex 生产系统就是工具全开（ADR 0001 发现 4：Codex `mcp_skill_dependencies.rs` 的 `dependencies.tools` 只在缺失时自动装 MCP server，不 gate 不解锁）。

### 1.2 删权限后，两套 skill 系统失去区分理由，应合并

现存**两个** skill 工具：

| 工具 | 来源 | 入参 | 行为 | 独占功能 |
|---|---|---|---|---|
| `use_skill` | 业务 skill（DB `skill` 表，父账户 CRUD） | `{name}` | 从 `turn.SkillByName` 查（runner 预读，0 DB），body 包 `<system-reminder>` 进 tool result | **解锁 allowed_tools**（→ `turn.AllowedTools`） |
| `read_skill` | 平台 skill（磁盘 `<skills_root>/<name>/SKILL.md`） | `{skill_name}` | `os.ReadFile` 返回 SKILL.md 原文 | 无（纯读） |

删权限后，`use_skill` 唯一独占功能（解锁工具）消失 → 与 `read_skill` 完全同质（都是「把一段指引塞进同一 agent 上下文」）。**两个工具、两套目录（system prompt §2）、两个入参名是纯粹的重复**，合并为单一 `load_skill`：业务 skill(DB) + 平台 skill(磁盘) 统一从一个目录暴露、一个工具加载。

### 1.3 收益

- **消 ~80% 配置成本**：父账户配 agent 时不再需要为每个 skill 精确列 `allowed_tools` 白名单（配错就工具够不到）。
- **消冷启动 UX 痛点**：agent 配置的 base 工具集若没勾 `get_current_date`，学员问「今天几号」就答不上（工具被 deny）。全开后任何 agent 都够得到全部注册工具。
- **架构更简单**：删一个 validator + 一个 ctx 注入对 + 一套 turn-scope 集合 + 合并两个工具 → 更少的活动部件。

---

## 2. 业务范围

### 关键术语

| 术语 | 含义 |
|---|---|
| **全开模型（Codex 式）** | 每个 agent 默认可用**所有注册工具**，按任务自主选；skill 不 gate / 不解锁工具 |
| **skill 纯指引** | skill = 一段 markdown 指引文档，让 LLM 把**已有能力**用对用好；不再是「能力开关」 |
| **`load_skill`** | 合并 `use_skill` + `read_skill` 后的单一工具，统一加载 DB 业务 skill + 磁盘平台 skill |
| **`allowed_tools` 推荐语义** | 字段不删，从「白名单（强制 deny 之外的）」变「推荐工具（提示文字，拼进 skill body 末尾给 LLM 看）」。**零数据迁移** |
| **cap 计数** | turn 内 skill 加载次数上限（默认 3），防 LLM 抽风无限调 skill。**保留** |

### In scope（本 feature 必交付）

#### 2.1 删权限层
- 删 `internal/numind/biz/permission/validators/use_skill_turnscope.go` 的 **deny 逻辑**（该 validator 整体是否删除 vs 退化为 passthrough no-op，S2 定；倾向整体摘除并从 hook chain 注销，保持简单）。
- 删 `runner.go` 的 `WithAgentBaseToolNames` / `WithFullToolMap` ctx 注入（permission deny 的输入，删 validator 后无消费者）。
- 删 `UseSkillTurnState.AllowedTools` 字段 + `use_skill` 内填充它的 union 逻辑（`tool_use_skill.go` 约 304-317）。
- runner.go 工具注册段（约 787-861）：**不再**收集 binding 的 `allowed_tools` 并集做 deny 控制；改为**直接全量注册**所有注册表工具到 Eino（全开）。S2 确认「全开」= 全部注册表工具 vs agent 配置工具 ∪ 全部 skill 工具（倾向前者：真全开）。

#### 2.2 `allowed_tools` 改语义（不删字段，零迁移）
- DB `skill.allowed_tools`（JSON）继续存在，现有父账户配置继续有效。
- runtime 不再据它放行/拦截工具；改为：加载 skill 时把 `allowed_tools` 渲染成一行「💡 推荐配合工具：X, Y」拼进 skill body 末尾，作为对 LLM 的**提示**。

#### 2.3 合并 `use_skill` + `read_skill` → `load_skill`
- 统一工具：单一 `load_skill` 工具，入参统一（`{name}` vs `{skill_name}` 取舍 S2 定，倾向 `{name}`）。
- 统一目录：system prompt §2 把 DB skill 目录 + 磁盘 SKILL.md catalog 合并为一个 skill 目录段。
- 统一加载路径：先查 DB 业务 skill（`turn.SkillByName`），未命中查磁盘平台 skill registry；命名冲突/优先级 S2 定。
- 保留 cap 计数 + 已加载 skill 列表（turn state 留 `InvocationCount` / `Cap` / `PendingSkills` / `SkillByID` / `SkillByName`，删 `AllowedTools`）。
- **向后兼容**：`agent_definition.tool_flags` 的 `enable_skills` flag 当前映射 `categoryToTools["enable_skills"] = {read_skill, run_python}`（回归测试钉死）。合并后该 flag 应映射到 `load_skill`（+ run_python），**且不能让回归测试 `TestRegression_NoInvokeSkillInProgressiveLoaderSurfaces` 失败**——S2 设计兼容方案（更新回归测试断言以反映新工具名，属合法演进，需在 spec 注明理由）。

#### 2.4 单 loop（不做，仅验证不回归）
- 单 loop 已就绪（2026-05-29）。本 feature 只需确保 `load_skill` 合并后 skill body 仍走 tool result 通道进同一 agent 上下文，回归测试仍绿。

### Out of scope（明确不做）

1. ❌ **doc-gen 防错**（ADR 0001 落地项 #1-3：SKILL.md 删伪代码 / 渲染回看循环 / xlsx recalc 校验）—— 用户 2026-05-31 选「保持独立」，归 skill-progressive-loader 剩余 track。两者层次不同（本 feature 改工具表面/权限；doc-gen 改平台 skill 内容 + 沙箱校验脚本），各自独立可测。
2. ❌ **bash_exec / run_python 的 agent 级高危开关**：建议先不做，等真有 C 端开放知识库需求再加。先彻底全开保持简单。
3. ❌ **DB schema 变更**：无（`allowed_tools` 字段保留，零迁移）。
4. ❌ **新增 API 端点**：无（`load_skill` 是内部 agent tool，不暴露 REST）。
5. ❌ **admin-web / 配置 UI 改动**：父账户配 skill 的 `allowed_tools` 输入框继续工作（运行时含义变了）。UI 文案是否需从「工具白名单」改措辞为「推荐工具」→ S1 评估，**倾向另开 micro，不进本 feature**。
6. ❌ **numind-web-v3 前端改动**：narration 已渲染工具调用气泡；`load_skill` 沿用现有 `use_skill`/`read_skill` 的 narration 通道（tool-display.yaml 加 `load_skill` entry 属本 feature 后端配置，不算前端代码改动）。S2 确认。
7. ❌ **prod 部署**：高风险改动，dev 收尾，prod 等用户验收。

---

## 3. 业务目标 / 验收标准

### 业务目标

把 agent-mode 工具权限模型从「deny-by-default + skill 解锁」改为 **Codex 全开模型**：每个 agent 默认可用所有工具、按任务自主选；skill 降级为纯指引文档。消除权限配置成本与冷启动 UX 痛点，同时保持 compliance/budget/sandbox 三层安全不变。

### 关键验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| AC-1 | **全开**：一个**未加载任何 skill** 的 agent 能直接调用之前被 gate 的工具（如 `get_current_date`、`run_python`）——不再返回「工具未启用，请先 use_skill」 | Go unit test（validator 删除后无 deny）+ Playwright E2E |
| AC-2 | **行为一致性**：一个典型业务 agent（如销售 FAQ）改动前后对**同一输入**的核心行为一致（不因全开而行为漂移到答非所问） | Playwright E2E 前后对比 |
| AC-3 | **skill 指引生效**：`load_skill("X")` 后，X 的 body（含 `allowed_tools` 渲染出的「推荐工具」行）出现在下一次 LLM generation 输入中 | Go unit test on runner + Langfuse trace |
| AC-4 | **现有 B2B skill 配置不破**：DB 中带 `allowed_tools` JSON 的现有 skill 行继续被 `load_skill` 加载（now as 推荐），不因 schema/语义变更报错 | Go unit test（用现有 skill 行 fixture） |
| AC-5 | **两工具合一**：`use_skill` 与 `read_skill` 不再各自注册；`load_skill` 同时能加载 DB 业务 skill 与磁盘平台 skill | Go unit test（两类 skill 各加载一次） |
| AC-6 | **cap 仍生效**：同 turn `load_skill` 超上限（默认 3）返回「已达本轮技能调用上限」，不抽风 | Go unit test |
| AC-7 | **单 loop 不回归**：`TestRegression_NoInvokeSkillInProgressiveLoaderSurfaces`（更新工具名后）仍 PASS；无 `invoke_skill` 复活；skill body 仍走单 agent 上下文 | 现有回归测试（断言更新） |
| AC-8 | **删字段干净**：`turn.AllowedTools`、`UseSkillTurnScope` deny、`WithAgentBaseToolNames`/`WithFullToolMap` 的所有引用清除，编译无悬挂引用 | `task lint` + grep 验证 |

### 非功能性
- 现有 `e2e/` agent 测试零回归。
- runtime 启动延迟不增加（删 union 收集，反而略减）。
- `load_skill` 平均延迟 ≤ 50ms（纯 DB/磁盘读 + memory append）。

### S5 验证策略雏形（NDF Rule 10：S0/S1 留候选给 S3 定）
- **验证方式：Playwright E2E 强制**（本改动影响**所有 agent** 且涉及权限/skill，属高风险，不能只 gstack 一次性 QA——需持久化回归保护）。
- **关键 user path**（S5 必须覆盖）：
  1. 典型业务 agent 改动前后行为一致（AC-2）
  2. 全开后 agent 能调之前被 gate 的工具（AC-1）
  3. skill 加载后指引生效（AC-3）
  4. 现有 B2B skill 配置不破（AC-4）
- 凭据：`E2E_USERNAME` / `E2E_PASSWORD` 环境变量。
- Go unit test 覆盖：validator 删除、`load_skill` 合并路径、cap、`allowed_tools` 推荐渲染、回归测试更新。
- **S3 独立 reviewer 确认最终策略**。

### NDF 规则 11 适用性
本 feature 起因是**用户拍板的架构决策**（cold-start prompt），非客户 bug 上报。规则 11（Bug-from-Customer 强制复现测试）**不适用**。冷启动「问今天几号答不上」是被本改动顺带消除的症状，由 AC-1 的 E2E 兜成回归保护，不需独立 `test(repro)` commit。

---

## 4. Triage

- **推荐轨道**：**Standard**（用户已拍板，无需再 triage）
- **分类理由**：
  1. DB schema 变更：**否**（`allowed_tools` 保留，零迁移）
  2. 新增 API 端点：**否**（内部 agent tool）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（`runner.go` + `use_skill_turnscope.go` + `tool_use_skill.go` + `tool_read_skill.go` + `skill_catalog.go` + state + 回归测试 + tool-display.yaml + 新 `load_skill`）
  5. 高风险业务逻辑：**是**（动 `runner.go` 影响**所有 agent**；删工具权限层 = 安全/权限相关；写错 = 全平台 agent 行为异常或工具失控）

条件 4+5 触发 Standard。

---

## 5. 风险

| # | 风险 | 概率 | 影响 | 缓解 |
|---|------|------|------|------|
| 1 | **【唯一真实取舍】prompt injection 爆炸半径变大**：今天「销售 FAQ」agent 够不到 bash → 物理免疫「注入驱动代码执行」；全开后够得到 | 确定发生 | 中 | sandbox + 8 P0 bashvalidator 仍管住**代码能干啥**；compliancegate 仍过滤内容；budgetgate 仍封顶成本。注入最多让 agent 多调工具，干不了沙箱外的事。**B2B SaaS 可接受，用户 2026-05-31 明确知情并接受**。这是 S0 必须写进卡的取舍 |
| 2 | **跨 feature 冲突 `agent-tool-schema-infra`**：触碰同一批文件（runner 注册段 / tool_full / base_tool / tool_use_skill） | 高 | 中 | 对方是基础设施，**建议先 land**；本 feature S4 前 `git fetch` + 检查对方动了哪些文件，rebase 到其上；S2 spec 标注交叠文件清单 |
| 3 | **删 validator 破坏 hook chain 注册/顺序**（其余 7 个 validator 依赖位置） | 中 | 高 | S2 实读 hook chain 注册代码；只摘 `UseSkillTurnScope`，其余 7 个 validator 顺序不动；S4 集成测试验证 chain 仍工作 |
| 4 | **`load_skill` 合并：DB skill 与磁盘 skill 命名冲突 / 优先级歧义** | 中 | 中 | S2 设计命名空间/优先级（倾向 DB 业务 skill 优先，磁盘平台 skill 兜底；冲突时 warn log）；统一目录渲染去重 |
| 5 | **向后兼容：`tool_flags.enable_skills` → 旧工具名映射 + 回归测试断言** | 中 | 中 | S2 设计 `enable_skills → {load_skill, run_python}` 映射；回归测试断言更新（合法演进，spec 注明）；保留 `categoryToTools` 兼容入口 |
| 6 | **全开后某些 agent「靠工具受限塑造人设」的预期被打破**（配置者本意是「这个 agent 只该用 X」） | 中 | 低 | 这是**本 feature 的预期改动**，非 bug；文档说明；AC-2 在代表性 agent 上验证行为不漂移；人设约束应由 system prompt 表达，非工具 deny |
| 7 | **配置 UI 仍把 `allowed_tools` 叫「白名单」→ 配置者困惑** | 低 | 低 | S1 评估是否改 UI 文案（倾向另开 micro）；运行时不依赖 UI 文案，功能不破 |
| 8 | **「全开」范围误判**（全部注册表工具 vs agent 配置 ∪ skill 工具） | 中 | 中 | S2 明确定义；倾向「全部注册表工具」（真全开，与 Codex 一致）；E2E AC-1 验证 |

---

## 6. 仓库与估时

- **仓库**：`numind-server`（仅后端；admin-web / web-v3 不动代码）
- **估时**：~1–1.5 周（比 `agent-mode-v2-skill-invocation` 短——单 loop 已就绪、无 DB/API/UI、纯删 + 合并）
- **worktree**：`/private/tmp/wt-agent-mode-open-tools-skill-as-guidance-numind-server`（branch `feature/agent-mode-open-tools-skill-as-guidance`，已建）
- **S4 启动前置**：`git fetch` 检查 `agent-tool-schema-infra` 是否 land；若 land 则 rebase

---

## 7. S0 待解决项（留给 S1/S2）

1. **`UseSkillTurnScope` 整删 vs 退化 no-op**：倾向整删 + 从 hook chain 注销（保持简单）。S2 实读注册代码确认无其他依赖。
2. **「全开」精确定义**：全部注册表工具 vs agent 配置工具 ∪ 全部 skill 工具。倾向全部注册表工具。
3. **`load_skill` 入参统一**：`{name}`（DB 式）vs `{skill_name}`（磁盘式）。倾向 `{name}`。
4. **DB skill 与磁盘 skill 命名空间/优先级**：冲突处理、统一目录去重。
5. **system prompt §2 目录合并**：DB skill 目录 + 磁盘 SKILL.md catalog 合一段。
6. **`enable_skills` flag 向后兼容映射** + 回归测试断言更新方案。
7. **`allowed_tools` → 推荐工具的渲染格式与位置**（body 末尾一行 vs 独立段）。
8. **turn state 删 `AllowedTools` 后的最终 struct** + 所有读写点清理。
9. **配置 UI 文案**是否进本 feature（倾向不进，另开 micro）。

---

## 8. 备注

- 本 feature **反转** `agent-mode-v2-skill-invocation` 的 AC-3（turn-scope `allowed_tools` 临时扩展）+ AC-10（启动只装 base + use_skill，私有工具默认 deny）。这两条 AC 随本 feature 作废——属架构演进，非回退 bug。
- `PendingBody` 覆盖 bug 已由 commit `94910336` 修复（→ `PendingSkills` 累积）；spawn 的修复 chip 可 dismiss。
- `skill-progressive-loader` 的单 loop 改造已 land（2026-05-29），本 feature **不触碰**；其剩余 doc-gen 防错工作是独立 track（用户 2026-05-31 选「保持独立」）。其 manifest 条目（stage S1）已过时，本 feature 起草时一并校正为反映「单 loop 已 done、剩余 = doc-gen 防错」。
- agent-mode autopilot 规则：**不部署 prod**；每个 stage gate 走 AI 自评 + Sonnet reviewer 双确认；prod 等用户验收。
