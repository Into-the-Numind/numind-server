# Proposal: skill-progressive-loader

> S1 工件 · 2026-05-29 · 基于 S0 需求卡 + Codex/Claude Code 双范式调研

## 1. 问题再陈述（一句话）

`invoke_skill` 当前的「内层 LLM 读 SKILL.md 写 Python」抽象把 declarative skill 文档误投喂给了 generative LLM，造成 deterministic 失败（`ModuleNotFoundError: No module named 'invoke_skill'`）；需要让 SKILL.md 由**唯一的外层 agent LLM**读取，并直接通过现有 `run_python` 工具执行真实 Python。

## 2. 三个候选方案

| | A. 修补 SKILL.md（已在 S0 否决） | **B. Codex 风格 progressive disclosure** ⭐ | C. Claude Code 风格 forked sub-agent |
|---|---|---|---|
| **改动量** | 小（仅文档+prompt） | 中（新增 `read_skill` 工具 + system prompt 注入 + 删 invoke_skill） | 大（要实现 sub-agent dispatch 框架） |
| **保留内层 LLM** | 是 | **否** | 否（但有 sub-agent loop） |
| **取消 invoke_skill** | 不取消 | **取消，新增 read_skill** | 取消，新增 use_skill (fork sub-agent) |
| **现有 run_python 复用** | 不变 | **是** | 是 |
| **架构错配是否消除** | 否（仍是双 LLM） | **是**（单 LLM loop） | 是 |
| **新增 skill 的成本** | 仍要 LLM 抄对一次 | **写 SKILL.md 即可**，外层 LLM 看即懂 | 同 B + 需考虑 sub-agent 隔离 |
| **token 预算** | 不变 | **catalog 极小（~800 chars），body 按需 read_skill** | sub-agent context 全独立 |
| **Eino v0.8.13 兼容** | 兼容 | **兼容**（单 react.Agent loop） | 需另起 agent loop |
| **代码新增/删除净行数估算** | +50 / -0 | **+200 / -300**（含 invoke_skill 删除） | +800 / -300 |
| **Langfuse trace 复杂度** | 同今 | **简化**（一次 ReAct 一条 trace） | sub-agent trace 需手工挂载 |
| **风险** | 短期治标，长期复发 | sandbox 的 `run_python` 既有路径继续承担 LLM 写代码执行 | sub-agent 框架本身可能耗 1-2 周 |

### 推荐：方案 B (Codex 风格 progressive disclosure)

**理由**：
1. **匹配现有架构**：我们已经有单 react.Agent loop（runner.go），`BuildInstitutionSection` 已预留 `skillCatalog` 槽位，`run_python` 已上线。Codex 模式就是「在 system prompt 列 skill catalog + 让模型用普通工具读 body + 用 shell/python 工具执行」——和我们已有材料 1:1 对应。
2. **删除「内层 LLM」这个错位的抽象**：B 方案直接消除 root cause（不只是治标），符合 S0 业务目标 #2「消除架构错配」。
3. **成本可控**：净改动 ~200 行 Go + 4 份 SKILL.md 重写。比方案 C 小一个数量级。
4. **可测性**：read_skill 工具是 deterministic（从磁盘读文件），不像 aiserviceSkillLLMCaller 是 nondeterministic LLM 输出，单元测试稳定。
5. **token 经济**：~4 个 skill × 200 字符 catalog = 800 字符常驻；SKILL.md body (~13KB) 只在需要时按需 `read_skill` 一次。
6. **未来扩展容易**：xlsx/docx/pdf-from-html SKILL.md 一并按 B 风格写，新增 skill 不再有架构债。

### 为什么不选 C
Claude Code 的 forked sub-agent 模式优雅，但它依赖 Claude Code 内部的 `runAgent()` + 共享 tool registry + token budget split 机制——我们 Eino 上没有这些原语，从 0 实现 sub-agent dispatch 框架的工作量远超本次需要的能力。Codex 范式做到了同样目标（progressive disclosure + 真实代码执行）但没有 sub-agent 复杂度。**如果未来需要 sub-agent 隔离能力**（如品牌一致性多 skill 编排），届时再单独考虑——本次不背这个 debt。

### 为什么不选 A
S0 阶段 user 已明确否决「短期治标」方向，理由是「架构错配的根因不解决，下一个 skill 仍会踩」。A 方案只是给 inner LLM 加 prompt 兜底（"看到 import invoke_skill 别当真"），下一次 LLM 因别的理由抄错 SKILL.md 又会失败。

## 3. 主要技术决定（S2 将详化）

- **新增工具 `read_skill`**：input `{skill_name: string}`；output `{name, description, body_md, max_runtime_seconds}`；从 `viper.GetString("sandbox.skills_root")` 下读 disk file（**不**走 sandbox COS URL 路径，不动 file_read）。
- **删除 `invoke_skill` 工具**：包括 `aiserviceSkillLLMCaller`、`GenerateCode` 方法、`tool_invoke_skill.go` 的 Execute 方法体。`skills.Registry` 保留（read_skill 需要列出可用 skill）。
- **System prompt 改造**：`runner_prompt.go::BuildSystemPromptV2` 已有 `skillCatalog` 参数，runner.go 调用时填充——格式：每行 `- {name}: {description}（读取详细指南：read_skill({skill_name: "<name>"})）`+ 一段使用指引说明「需要生成 PPT/Excel/PDF 等结构化文件时，先 `read_skill` 拿到文档，按文档示例写 Python，调用 `run_python` 执行；输出文件会自动以 `/agent-outputs/<userID>/` URL 形式回到对话历史」。
- **4 个 SKILL.md 全部重写**为面向「外层 agent LLM」的真实 python-pptx / openpyxl / python-docx / pdf-from-html 教学；删除 declarative `import invoke_skill` 伪代码示例。
- **保留**：sandbox pool 不动、`run_python` 不动、`file_read` 不动（A 修复已 cover `/agent-outputs/`）、Langfuse trace/generation 通过 aiservice 走（I5 invariant 保持）。

## 4. 风险与缓解

| 风险 | 严重度 | 缓解 |
|---|---|---|
| 外层 LLM 写的 python-pptx 代码质量比内层 LLM 差 | 中 | SKILL.md 重写时给出**完整可拷贝代码模板**（封面/标题/图表/表格四类示例完整），LLM 抄模板成功率应≥当前内层 LLM；S5 用 gstack /qa 在 dev 跑真任务验收 |
| 删除 invoke_skill 后，已存在的 agent run 历史 messages 含 `invoke_skill` 工具调用引用，重放/导出可能 break | 低 | 历史 messages 不变（DB 已存的 tool_call_name=invoke_skill 仍能渲染显示，前端 `AgentToolCallItem.vue:115` `KNOWN_TOOL_NAMES` 已含 `'invoke_skill'`，保留不动）；不写 migration |
| **agent_definition.tool_flags.enable_skills 已 gate invoke_skill 在 runtime**（证据：tool_invoke_skill.go:210 `cfg.EnableSandbox && cfg.EnableSkills`；student_run_lifecycle.go:742 `"enable_skills": {"invoke_skill"}`）| **中**（强相关）| **复用同一 flag**：把 `enable_skills` → `invoke_skill` 的映射改为 `enable_skills` → `read_skill`（新工具）。所有现存 agent_definition row 的 `tool_flags.enable_skills=true` 自动获得 `read_skill`。**零 migration、零 DB 改动、零前端配置 UI 改动**。`ToolConfig.EnableSkills` Go 字段名保留（语义改为 gate read_skill），向后兼容字段名 |
| `read_skill` 工具读 disk 文件，从 server 容器（不是 sandbox）读，需注意 path traversal | 中 | input 只接 skill_name 字符串，与 registry 中已知 name 做白名单匹配（`registry.Get(name)`），不接受任意 path |
| Token 增量：4 skill × 200 字符 catalog = ~800 字符进 system prompt | 低 | 当前 system prompt ~6KB，800 字符增量 = ~13% — 可接受 |
| **多 SKILL.md 累积进 context**（最坏情况：一次任务里 read_skill PPT + xlsx + docx + pdf-from-html = 4 × 13KB = 52KB 进 history） | **中** | **硬约束**：每份 SKILL.md 重写时压到 **≤4KB**（去掉冗余文字+折叠示例）。4 × 4KB = 16KB 最坏，相对 system prompt 6KB 是 ~3× 而非 8×。**S4 任务验收门：SKILL.md 文件大小≤4096 bytes**（pptx-author 当前 13KB → 必须削掉 70%）。Codex 在 SkillMetadata 只存路径不存 body 也是同思路（progressive disclosure 的核心约束）。LRU 驱逐不在本次 scope（如未来 skill 数量 > 10 再做） |
| 外层 LLM 偷懒不 read_skill 直接写 Python（无 SKILL.md 指导可能写错） | 中 | system prompt 明确「必须先 read_skill 再写代码」；若 LLM 不调 read_skill 直接错误地 `run_python`，sandbox 报错 LLM 会自纠正（H A 已让校验 soft error） |
| **部署期间 in-flight agent run 撞 invoke_skill 被删** | 低 | dev/prod 都用 docker 滚动替换，sandbox `30s grace` 已覆盖；P0 用户全程持续在线的运行极少；deploy 时段建议在低峰（已是惯例） |
| 4 份 SKILL.md 重写工作量集中 — pptx-author 当前 13KB/359 行，削到 4KB 是非平凡工作 | **中** | S3 plan 把 SKILL.md 重写拆为独立的 4 个 S4 子任务，每份独立审查通过；总工时上修为 **S4 8-12h**（替换原 6-10h），全 feature **12-17h**（替换原 10-15h） |

## 5. 验收范围

- **功能**：dev 上用 gstack /qa 跑「找最近一周 AI 创业项目并出 PPT」任务，agent 全程不报 `ModuleNotFoundError`，输出有效 `.pptx` 文件可下载
- **回归**：所有现有 `tool_invoke_skill_test.go` / `tool_run_python_test.go` / `runner_*_test.go` 测试在重构后通过（或被有意删除/替换的 test 在 S3 plan 中明确）
- **观测**：Langfuse 中能看到 trace 包含 `read_skill` span 和 `run_python` generation，不再有 `tool.invoke_skill.execute` span

## 6. 估算

| 阶段 | 工时 |
|---|---|
| S2 spec | 1-2h |
| S3 plan | 1h |
| S4 实现（5+ 个原子 task：read_skill 新工具 + skill catalog 注入 + invoke_skill 移除 + 4 份 SKILL.md 独立重写（每份必须 ≤4KB）+ 测试更新） | **8-12h**（修上调，反映 SKILL.md 4 份 × 2-3h 真实工作量） |
| S5 验证 | 1-2h |
| **总** | **12-17h** |

## 7. 下一步

S2 阶段细化：
- read_skill 工具的精确 JSON schema + 错误路径
- skillCatalog 渲染的具体字符串模板
- pptx-author SKILL.md 新结构大纲
- 测试用例增删清单
