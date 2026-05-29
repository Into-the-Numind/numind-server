# Spec: skill-progressive-loader

> S2 工件 · 2026-05-29 · 实现 proposal.md 中 B 方案（Codex-style progressive disclosure）的精确技术设计

## 0. 关键架构决定（最终）

**单一外层 agent LLM + skill catalog 注入 system prompt + 新增 `read_skill` 工具 (progressive disclosure) + 复用 `run_python` 工具执行 + 删除 invoke_skill Execute 与 inner LLM。**

```
┌─ 外层 agent LLM（唯一 LLM）────────────────────────────────────┐
│                                                                │
│ system_prompt §2 (Institution Section) 现含 skill catalog:    │
│   - pptx-author: 生成 PowerPoint 演示文档...                  │
│     (读取详细指南: read_skill({"skill_name": "pptx-author"})) │
│   - xlsx-author: 生成 Excel 表格...                          │
│   ...                                                          │
│                                                                │
│ ┌─ Turn 1: 用户「做 PPT」────────────────────────────────┐    │
│ │ LLM 调用 read_skill({skill_name: "pptx-author"})       │    │
│ │   → 工具同步从 /app/skills/pptx-author/SKILL.md 读     │    │
│ │   → 返回 {name, description, body_md (≤4KB), ...}      │    │
│ └────────────────────────────────────────────────────────┘    │
│ ┌─ Turn 2: LLM 看完 SKILL.md 写真 python-pptx 代码 ──────┐    │
│ │ LLM 调用 run_python({                                  │    │
│ │   code: "from pptx import Presentation\nprs=Presenta..",│    │
│ │   input_files: ["<用户上传 logo URL>"],                │    │
│ │ })                                                     │    │
│ │   → sandbox docker 跑 python3                          │    │
│ │   → /output/result.pptx → COS → /agent-outputs/<u>/...│    │
│ │   → 返回 {files: [{url, filename, ...}]}              │    │
│ └────────────────────────────────────────────────────────┘    │
│ ┌─ Turn 3: LLM 把 URL 嵌入最终回答 ─────────────────────┐    │
│ │ assistant message: 「PPT 已生成: <link>」              │    │
│ └────────────────────────────────────────────────────────┘    │
└────────────────────────────────────────────────────────────────┘
```

**对比删掉的旧路径**：

| 路径 | 旧（broken） | 新 |
|---|---|---|
| LLM 数量 | 2（外层 + 内层 GenerateCode） | **1**（仅外层） |
| Eino loop 数 | 1 + 隐藏 aiservice.Chat | **1** |
| 调用入口 | invoke_skill(skill_name, instructions, input_files) | read_skill(skill_name) → run_python(code, input_files) |
| Python 代码作者 | 内层 LLM 看 SKILL.md 写 | **外层 agent LLM 看 SKILL.md 写** |
| 错误能否被 LLM 看见 | 看不见（被 aiservice.Chat 吞） | **看得见**（read_skill / run_python 都返回 ToolResult） |

## 1. 文件级改动清单

> 注：所有文件路径相对 `numind-server/` 仓库根

### 1.1 新增文件

| 文件 | 用途 | 估算 LOC |
|---|---|---|
| `internal/numind/biz/agent/tool_read_skill.go` | `read_skill` FullTool 实现 | ~150 |
| `internal/numind/biz/agent/tool_read_skill_test.go` | unit tests | ~200 |
| `internal/numind/biz/agent/skill_catalog.go` | skill catalog 字符串渲染（给 BuildInstitutionSection） | ~80 |
| `internal/numind/biz/agent/skill_catalog_test.go` | 渲染测试 | ~100 |

### 1.2 修改文件

| 文件 | 改动概述 | 估算净 LOC |
|---|---|---|
| `internal/numind/biz/agent/tool_full.go` | `EnableSkills` 字段保留但 doc-comment 改为 "gate read_skill"；注释 invoke_skill 字样改成 read_skill | ~10 |
| `internal/numind/biz/agent/factory_platform.go` | `LoadTools` 删除 `NewInvokeSkillTool` 注册 + 添加 `NewReadSkillTool(f.skillRegistry)` 注册；**ToolMetadata 列表里把 invoke_skill 改 read_skill**；**run_python ToolMetadata 描述里 "（invoke_skill）" 改 "（read_skill → run_python）"**；可选移除 `skillPool` 参数（dead param）| +25 / -30 |
| `internal/numind/biz/agent/student_run_lifecycle.go` | line 742 `"enable_skills": {"invoke_skill"}` 改为 `"enable_skills": {"read_skill"}` | ~3 |
| `internal/numind/biz/agent/runner.go` | line 697-700 skillCatalog 填充改为调用新 `RenderSkillCatalog(f.skillRegistry)`，传给 BuildInstitutionSection；旧的 skill body 填充逻辑（若有）删 | +10 / -20 |
| `internal/numind/biz/biz.go` | line 282/289/291 log message `invoke_skill` → `read_skill`；**删除 `SkillPool` gate**（`if sp, ok := sandboxPool.(sandbox.SkillPool); ok`）—— read_skill 不依赖 SkillPool，只需 registry；移除 SkillPool 参数从 `NewPlatformToolFactoryWithSkills` call-site | +5 / -10 |
| `internal/numind/biz/agent/output_tools_priority_prompt.go` | **关键**：constant `OutputToolsPriorityAddendum` 含 8 处 invoke_skill 引用，会被注入 system prompt — 全部改写为「使用 read_skill 读取技能指南后用 run_python 执行」的两步流。否则 LLM 同时收到 catalog 教它用 read_skill + addendum 教它用 invoke_skill 的矛盾指令 | ~40 改写 |
| `internal/numind/biz/agent/tool_create_html.go` | description 字符串 line 44 含 "invoke_skill path"，改为 "read_skill → run_python path"（LLM 看 tool description 时不能看到已删除工具名）| ~3 |
| `internal/numind/biz/agent/tool_create_png_chart.go` | description 字符串 line 109 同上 | ~3 |
| `internal/numind/biz/agent/adapter_full_to_eino.go` | line 82 stale comment `// during a 30–60s invoke_skill it looks frozen` → 改为 `// during a 30–60s run_python it looks frozen` | ~1 |
| `internal/numind/biz/agent/tool_invoke_skill.go` | **整文件删除**（含 aiserviceSkillLLMCaller、GenerateCode、Execute、SkillLLMCaller interface — 已 grep 确认无外部 caller、所有 helper） | -550 |
| `internal/numind/biz/agent/tool_invoke_skill_test.go` | **整文件删除**（旧 11 个 test 不再适用） | -380 |
| `skills/pptx-author/SKILL.md` | 重写 ≤4KB 真实 python-pptx 教程 | -350 lines |
| `skills/xlsx-author/SKILL.md` | 重写 ≤4KB（如存在） | -varies |
| `skills/docx-author/SKILL.md` | 重写 ≤4KB（如存在） | -varies |
| `skills/pdf-from-html/SKILL.md` | 重写 ≤4KB（如存在） | -varies |

### 1.3 不动的文件

- `internal/numind/biz/agent/skills/registry.go`（Registry 接口保留 — read_skill 用 Get 拿 entry）
- `internal/numind/biz/agent/skills/manifest.go`（manifest 格式不变）
- `internal/numind/biz/agent/tool_run_python.go`（既有 run_python 工具完美适配）
- `internal/numind/biz/sandbox/pool_skill.go` & `pool.go`（**sandbox 仍可用作 run_python 的执行环境**；不删 SkillPool 接口的 AcquireForSkill — 留 forward compatibility，可能后续 skill 需要 pre-copied scripts/）
- `internal/numind/biz/agent/runner_prompt.go::BuildInstitutionSection`（skillCatalog 参数槽已存在）
- 前端（`AgentToolCallItem.vue` 的 `KNOWN_TOOL_NAMES` 保留 `'invoke_skill'` 为历史 run 渲染兼容）

## 2. `read_skill` 工具规格

### 2.1 JSON Schema

```json
{
  "name": "read_skill",
  "description": "Read the full guidance for a skill (returned from skill_catalog in system prompt). Use when you decide to generate a structured file (PPT/Excel/Word/PDF) — read the skill's SKILL.md to learn the exact Python code patterns, then call run_python to execute.",
  "input_schema": {
    "type": "object",
    "properties": {
      "skill_name": {
        "type": "string",
        "description": "Name of a skill listed in the skill catalog (e.g. pptx-author, xlsx-author)."
      }
    },
    "required": ["skill_name"]
  }
}
```

### 2.2 Output Schema

```go
type readSkillOutput struct {
    Name              string `json:"name"`
    Description       string `json:"description"`
    BodyMarkdown      string `json:"body_md"`       // SKILL.md 全文（≤4KB 硬上限）
    MaxRuntimeSeconds int    `json:"max_runtime_seconds"`
    Categories        []string `json:"categories,omitempty"`
}
```

### 2.3 Execute 流程

> **构造时 nil 容忍约定**：`NewReadSkillTool(nil)` 允许；工具注册不 panic；但每次 Execute 都返回 "skill registry not configured" soft error。这样测试可独立构造工具而不需要 mock registry。

```
1. JSON unmarshal input → soft error if invalid（Codex pattern）
2. skill_name == ""        → soft error "skill_name is required"
3. registry == nil          → soft error "skill registry not configured"
4. entry, err := registry.Get(skill_name)
   - err = ErrSkillNotFound → soft error "skill %q not found. available: <list>"
5. os.ReadFile(filepath.Join(entry.RootDir, "SKILL.md"))
   - err → soft error "SKILL.md unreadable: %v"
6. 文件 > 4KB → soft error "SKILL.md exceeds 4KB cap" (defensive — S4 test gate confirms ≤4KB but如运行时被改了大文件)
7. 返回 ToolResult JSON
8. Langfuse: CreateSpan "tool.read_skill.execute" with skill_name input, body length output
```

### 2.4 错误处理（Codex RespondToModel pattern）

- 所有路径返回 `(ToolResult, nil)`，无 Go error
- ToolResult content 含 `"ERROR: ..."` 前缀
- 与 file_read 修复后的 pattern 完全一致

### 2.5 IsEnabled / Metadata

```go
func (t *readSkillTool) IsEnabled(cfg ToolConfig) bool {
    return cfg.EnableSkills  // 复用现有 flag，已映射到 enable_skills tool category
}

UserFacingName() string  = "读取技能指南"
NarrationVerb() string   = "查阅技能"
IsReadOnly() bool        = true
IsSearchOrReadCommand() = true
AlwaysLoad() bool        = false   // 跟 enable_skills flag 走
```

## 3. Skill Catalog 渲染规格

### 3.1 渲染函数

```go
// RenderSkillCatalog 返回注入 system prompt §2 institution section 的 skill catalog 字符串。
// Catalog 仅含 name + description + 调用提示，不含 SKILL.md body（progressive disclosure）。
// 当 registry == nil 或没有可用 skill → 返回 ""（toolsHint 等其他段不受影响）。
func RenderSkillCatalog(reg skills.Registry) string
```

### 3.2 输出格式（用户实际看到的 system prompt 片段）

```
## 可用技能（Skills）

需要生成 PPT/Excel/Word/PDF 等结构化文件时，使用以下技能。
**重要**: 不要直接编写 Python 代码 — 必须先调用 read_skill 读取详细指南，按指南示例写代码，然后通过 run_python 执行。

可用技能：
- `pptx-author`: 生成 PowerPoint 演示文档（封面、列表、表格、图表 4 类布局；可注入品牌色/logo）。
- `xlsx-author`: 生成 Excel 工作簿（多 sheet、公式、条件格式、图表）。
- `docx-author`: 生成 Word 文档（标题层级、段落、表格、图片）。
- `pdf-from-html`: 把 HTML 渲染为 PDF（支持中文字体、分页、页眉页脚）。

工作流：
1. 读取技能指南: read_skill({"skill_name": "<选定技能>"}) → 看返回的 body_md
2. 按指南示例编写完整 Python 代码
3. 执行: run_python({"code": "<完整代码>", "input_files": [<用户上传 URL，可选>]})
4. run_python 返回 {files: [{url: "https://...agent-outputs/.../result.pptx"}]} — 把 url 嵌入最终回答
```

### 3.3 实现细节

- `RenderSkillCatalog` 用 `registry.List()` 取所有 skill entry
- 按 name 字母排序保证 deterministic 输出
- 每个 entry 取 manifest.json 的 `description` 字段（如缺则用 fallback）
- description 超过 200 字符截断 + ellipsis（防单个 skill description 异常长撑大 catalog）
- 总长度软上限 2000 字符（**包含 header boilerplate 约 300 字符**，即 skill entry 部分约 1700 字符。如 catalog 整体超过 → log warn + 截断尾部 skill）

## 4. SKILL.md 重写约束（4 份各自一个 S4 task）

### 4.1 硬约束（所有 4 份）

- **文件大小 ≤ 4096 bytes**（用 `wc -c` 验证）
- **不出现** `invoke_skill(`、`import invoke_skill`、伪代码示例
- **必须含**至少 4 个完整可运行 Python 代码模板（封面/列表/表格/图表类，针对 pptx；其他类型同步类比）
- **不引用**外部 helper modules（除 manifest.required_libs 中声明的）
- **写作对象明确**: 「你是 agent，需要为用户生成 .pptx 文件。下面是完整代码示例，复制粘贴并按用户需求修改字段即可」
- 头部 H1 = skill name；下面 4-6 个 H2 section（速查 / 速建 / 完整模板 / 排版约束 / 已知坑）

### 4.2 模板段落（pptx-author 为例）

```markdown
# pptx-author

## 用途
生成 .pptx PowerPoint 演示文档。布局: cover / title-bullets / title-table / title-chart / section / end.

## 最小示例（3 页 deck）
\`\`\`python
from pptx import Presentation
from pptx.util import Inches, Pt
prs = Presentation()
prs.slide_width = Inches(13.33); prs.slide_height = Inches(7.5)
# 封面
s = prs.slides.add_slide(prs.slide_layouts[6])  # blank
... (省略，但提供完整代码)
prs.save('/output/result.pptx')
\`\`\`

## 完整模板 - 含品牌色和图表
\`\`\`python
... (≤1.5KB 代码块)
\`\`\`

## 文件路径约定
- 输入: /workspace/input/<filename>（run_python input_files 自动下载到此）
- 输出: /output/result.pptx（run_python 自动收集 /output/ 下文件 → 上传 COS → 返回 agent-outputs URL）
- 字体: Noto Sans CJK SC 已预装

## 已知坑
- 图表 X 轴非数字 → 用整数字符串
- 中文乱码 → 强制 font_family="Noto Sans CJK SC"
```

### 4.3 验收 gate

每份 SKILL.md 重写完后 S4 task gate 包括：
- `wc -c <file>` ≤ 4096
- 关键词 grep: `grep -c "from pptx" <file>` ≥ 3（确保有真实 import）
- 关键词 grep: `grep -c "invoke_skill" <file>` = 0（防 LLM 写代码时回填）
- 与原 SKILL.md 保留的功能覆盖度（layouts 列表/brand_config/chart 类型）人工 sanity check

## 5. Test Plan

### 5.1 新增 test cases (`tool_read_skill_test.go`)

| Test | 路径 |
|---|---|
| `TestReadSkill_HappyPath_ReturnsBody` | skill 存在 → body_md 正确返回 |
| `TestReadSkill_SkillNotFound_ReturnsSoftError` | 不存在 → soft error 含 available list |
| `TestReadSkill_BadInputJSON_ReturnsSoftError` | invalid JSON → soft error |
| `TestReadSkill_EmptySkillName_ReturnsSoftError` | "" → soft error |
| `TestReadSkill_NilRegistry_ReturnsSoftError` | registry nil → soft error |
| `TestReadSkill_PathTraversalAttempt_ReturnsSoftError` | skill_name 含 "../" → soft error (registry.Get 应该已经拒绝，但加 test 保险) |
| `TestReadSkill_SKILLMDOver4KB_ReturnsSoftError` | 模拟 SKILL.md > 4KB → defensive soft error |
| `TestReadSkill_IsEnabled` | EnableSkills flag matrix |

### 5.2 新增 test cases (`skill_catalog_test.go`)

| Test | 路径 |
|---|---|
| `TestRenderSkillCatalog_HappyPath` | 4 skill → 渲染含 name+description+读取提示 |
| `TestRenderSkillCatalog_EmptyRegistry` | 0 skill → "" |
| `TestRenderSkillCatalog_NilRegistry` | nil → "" |
| `TestRenderSkillCatalog_LongDescriptionTruncated` | description > 200 chars → 截断 |
| `TestRenderSkillCatalog_DeterministicOrder` | 排序确定 |

### 5.3 修改 test cases

| 现 test | 改动 |
|---|---|
| `runner_test.go` 涉及 invoke_skill 的 | 删除或改 read_skill |
| `factory_platform_test.go` 涉及 invoke_skill 的 metadata | 改 read_skill |
| `student_run_lifecycle_test.go` 涉及 enable_skills→invoke_skill 映射的 | 改 → read_skill |

### 5.4 删除 test cases

- `tool_invoke_skill_test.go` 整文件删除（11 个 test）

## 6. SSE / Langfuse 事件影响

| 工具 | tool_call_start/result/error | langfuse span |
|---|---|---|
| 旧 invoke_skill | 通过 adapter_full_to_eino emit（hotfix stream-emit-toolcall-events 已修） | tool.invoke_skill.execute span，含 inner aiservice.Chat generation |
| **新 read_skill** | 同 emit（adapter 不感知工具种类，自动支持） | **新 span** tool.read_skill.execute（无 generation，read 是 deterministic） |
| **既有 run_python** | 已 emit | 既有 span 不变 |

**净效果**：每次 PPT 任务的 SSE 事件流多 1 个 tool_call_start/result 对（read_skill），少 1 个对（invoke_skill）— 总数不变。Langfuse 中每个 task 的 trace 少 1 个 inner-LLM generation（aiservice.Chat by aiserviceSkillLLMCaller），多 1 个 deterministic span（read_skill），trace 总长度减少。

## 7. Sandbox 影响

- `read_skill` **不**使用 sandbox（从 server 容器本地磁盘 `/app/skills/<name>/SKILL.md` 读）。
- `run_python` 继续使用 sandbox（既有路径不变）。
- 旧 `invoke_skill.AcquireForSkill` 把 SKILL.md / scripts/ 复制进 sandbox 容器的逻辑 — **保留代码**但不再被调用。`SkillPool.AcquireForSkill` 接口留作 forward compatibility（后续如果某 skill 需要预投放大 helper 脚本，可通过此路径）。
- `sandbox.skills_root` config 仍读取，给 read_skill 用作磁盘路径根。

## 8. 兼容性矩阵

| 场景 | 行为 |
|---|---|
| 历史 agent run（含 invoke_skill 调用记录） | 前端 AgentToolCallItem 渲染正常（KNOWN_TOOL_NAMES 保留 invoke_skill 字符串） |
| 历史 agent_definition row（tool_flags.enable_skills=true） | 自动启用 read_skill（同一 flag 现在 gate read_skill） |
| 现役 dev 用户跑「做 PPT」任务 | 进入新路径，end-to-end 在 S5 用 gstack /qa 验收 |
| Sandbox image v1.5.1（已部署 dev） | 不需更新（read_skill 不进 sandbox；run_python 用 python:3.11-slim + python-pptx 已预装） |

## 9. 风险增量 vs Proposal

无新增风险。S2 详化只是把 Proposal 中的方向落地为可执行规格。

## 10. 下一步

S3 plan 阶段把 §1.1-1.2 的文件改动拆为有序、原子、可独立验证的 S4 task；并指定 S5 验证策略（go test + gstack /qa）。
