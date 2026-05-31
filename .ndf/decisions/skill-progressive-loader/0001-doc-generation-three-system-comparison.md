---
feature: skill-progressive-loader
adr: 0001
title: "文档生成（xlsx/pptx/docx/pdf）防错策略 — Claude Code / Codex / Anthropic 官方 skill 三家源码对比"
status: accepted
date: 2026-05-31
deciders: [zhiyuchen, ai]
supersedes: null
superseded_by: null
tags: [skill, doc-generation, pptx, xlsx, progressive-disclosure, single-loop, validation-loop]
---

# ADR 0001: 文档生成防错策略 — 三家源码对比

## 背景（Context）

`skill-progressive-loader` feature 的根因 bug：现有 invoke_skill 架构是**双 LLM**（外层 agent 调 invoke_skill tool → 内层 LLM 读 SKILL.md 写 Python）。`pptx-author` 的 SKILL.md 教外层 agent 用声明式伪代码（`import invoke_skill; invoke_skill("pptx-author", {...})`），内层 LLM 把伪代码当真代码跑 → `ModuleNotFoundError`（dev agent_run_id=83 PPT 失败）。

S2 设计阶段需要在两个范式间选择：Codex 风格单 loop 渐进披露 vs Claude Code 风格 fork sub-agent。本 ADR 通过**实读三家源码**给出范式选择 + 防错策略的事实依据。

调研日期 2026-05-31，读取的源码：
- Claude Code CLI 源码：`/Users/zhiyuchen/Downloads/ClaudeCode/src/`
- Codex 源码：github.com/openai/codex（codex-rs）
- Codex vendored skill：`~/.codex/vendor_imports/skills/skills/.curated/`
- Anthropic 官方 doc skill：`~/Library/Application Support/Claude/local-agent-mode-sessions/skills-plugin/.../skills/{xlsx,pptx,docx,pdf}/`

## 关键发现（Findings）

### 发现 1：没有任何一家把文档生成做成确定性原生工具

- **Claude Code CLI**：`src/tools.ts` ~40 个工具，零 doc 生成工具，只有 Bash/Write/Read。SkillTool fork 一个 sub-agent，sub-agent 拿 Bash 跑 Python/Node。真正能力来自 Anthropic 官方 skill。
- **Codex**：源码零 doc 库依赖（grep reportlab/openpyxl/python-pptx 无结果）。只有一个 `.curated/pdf` skill，纯 prompt 教 LLM 写 reportlab，skill 目录零 .py helper。
- **Anthropic 官方 skill**：连 pptx 都没封成 `create_pptx` 工具，仍由 LLM 写代码（openpyxl / Node `docx` / `pptxgenjs` / 裸 OOXML）。

**结论**：输出形态太多变，固化成原生工具不可行。仅最简单结构化输出（图表、CSV）适合做成工具——莫小派 `create_png_chart`/`create_csv` 已正确这么做。

### 发现 2：好的 skill ≠ 一份 markdown 配方，而是"配方 + 确定性校验/修复 + 渲染回看循环"

Anthropic 官方 skill 是金标准，每种格式都在"LLM 写代码"外包了后三层：

| 格式 | LLM 写什么 | 防错关键 |
|---|---|---|
| xlsx | openpyxl（公式写成字符串） | ship `scripts/recalc.py` 调 LibreOffice 真算公式，扫全表 7 种错误（#REF!/#DIV/0!…）返回 JSON → LLM 改 → 重跑直到 status:success |
| pptx | unpack→改 XML→pack，或从零 `pptxgenjs`(Node) | pack 时 schema 校验 + 自动修复(durableId/xml:space)；**渲染成图 → subagent 看图**找重叠/溢出/对比度 → 改 → 重验 |
| docx | Node `docx` 库，或 unpack→改 XML→pack | schema 校验 + 自动修复 + validate.py；改已有文档直接操作 OOXML 不重新生成 |
| pdf | reportlab | 渲染回 PNG 看图检查（Codex pdf 同此） |

Anthropic ship 的 helper（unpack/pack/recalc/validate/soffice/add_slide/clean）**全是确定性的"校验/拆装/修复"工具，不是生成工具**。

莫小派现状只有第一层（配方），缺后三层——这是 fragile 的根因，比双 LLM bug 更本质。

### 发现 3：Claude Code 和 Codex 都是单 loop，双 LLM 是莫小派的异类

- Codex `core-skills/src/injection.rs`：skill 是 push 注入，SKILL.md 原文塞进**同一个** agent 的 prompt，同上下文写代码。
- Claude Code SkillTool：fork sub-agent，但 sub-agent 自己读 skill + 自己写代码，**单一上下文内闭环**，无"外层调用/内层执行"切分。
- 两家都没有"外层 LLM 发指令、内层 LLM 解释执行"的双 LLM 切分——这正是莫小派伪代码混淆 bug 的来源。

### 发现 4（旁证）：Codex skill 不 gate 工具

Codex `mcp_skill_dependencies.rs`：skill 的 `dependencies.tools` 只在缺失时**自动安装 MCP server**，不解锁/不限制工具。印证 Codex 工具全开、skill 不管权限——与本仓库正在推进的"全开 + skill 纯指引"方向一致（见 agent-mode 工具权限重构讨论）。

## 决策（Decision）

S2 范式选择 + 防错策略定为：

1. **单 loop 渐进披露**（Codex/Claude Code 一致范式），废除内外双 LLM 切分。同一 agent 读 skill + 写代码在同一上下文。
2. **不**为 pptx/xlsx/docx 做确定性原生工具（金标准都没做）。仅图表/CSV 这类已封装的保持工具形态。
3. skill 内容补齐后三层：**确定性校验脚本 + 自动修复 + 渲染回看循环**。

## 落地优先级（Consequences / Action Items）

按 ROI 排序，供 S3 plan 拆 task：

1. **改 SKILL.md 内容（零架构改动，立即做）**：删除/改写"长得像伪代码"的片段为明确的"这是要执行的真实代码"。单此一条消掉大半 bug。
2. **加渲染回看循环（最高 ROI）**：pptx/pdf 生成后沙箱内 `soffice --convert-to pdf` + `pdftoppm` 渲染成图，复用现有 `analyze_image` 工具让 LLM 看图检查、发现问题重做。工具链已具备。
3. **xlsx 加 recalc 校验**：沙箱装 LibreOffice，抄 Anthropic `recalc.py` 思路，生成后真算扫公式错误。
4. **改单 loop**：废内外双 LLM，skill body 直接进当前 agent 上下文（与"全开 + skill 纯指引"重构合流，use_skill/read_skill 合并）。

## S5 验证策略影响

渲染回看循环本身就是天然的验证手段。S5 应专门测：xlsx 公式错误能被 recalc 捕获并修复；pptx 视觉缺陷能被 render-back + analyze_image 发现。这些应落成可回归的测试（dev agent_run_id=83 的 PPT 复现 + 修复后通过）。

## 参考

- 三家源码对比原始调研：本 session（2026-05-31）
- 相关 memory：`reference_doc_generation_three_system_comparison`（reference 类）
- 关联 feature：`stream-emit-toolcall-events`（known_issues 误判已在本调研推翻）
