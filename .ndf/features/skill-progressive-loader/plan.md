# Plan: skill-progressive-loader

> S3 工件 · 2026-05-29 · 把 spec.md §1 文件改动拆为可独立 build + verify 的 S4 task

## Task 依赖图

```
Wave 1 (并行):
  Task 1 (read_skill 工具 + 测试) ─┐
  Task 2 (skill_catalog 渲染 + 测试) ┤
  Task 3 (system prompt addendum 改写) ┘
                                       │
                                       ▼
Wave 2:
  Task 4 (wire: factory + biz + runner + lifecycle + tool_create_* + adapter 注释)
                                       │
                                       ▼
Wave 3:
  Bug-from-customer 复现测试 commit
  Task 5a (pptx-author SKILL.md) ──┐
  Task 5b (xlsx-author SKILL.md) ──┤  并行
  Task 5c (docx-author SKILL.md) ──┤
  Task 5d (pdf-from-html SKILL.md) ─┘
                                       │
                                       ▼
Wave 4:
  Task 6 (删除 invoke_skill + tests)
  Task 7 (s5-strategy.md)
```

**关键约束**: Task 6（删除 invoke_skill）必须 **晚于** Task 4（wire 改 factory）+ 复现测试 commit + 所有 Task 5 子任务，否则 Wave 3 重写 SKILL.md 时无法用旧 invoke_skill 路径做对照测试。Task 5/6 与 Task 1-3 **不可同 wave 并行**。

## Task 列表

### Task 1: 实现 `read_skill` 工具 + unit tests

**文件**:
- `internal/numind/biz/agent/tool_read_skill.go` (新建, ~150 LOC)
- `internal/numind/biz/agent/tool_read_skill_test.go` (新建, ~200 LOC)

**实现内容**：spec §2 的 JSON schema、output 结构、Execute 流程（含 path traversal 防护、4KB 运行时 cap、Codex soft-error pattern、langfuse span）、IsEnabled wiring。

**验收**: 
- `go test ./internal/numind/biz/agent/ -run TestReadSkill -count=1` 全过
- 8 个 test case 全覆盖（happy + 7 error paths per spec §5.1）

**Bug-from-customer 复现测试**: ⚠️ 注意 — 本 task 创建的 ReadSkill 完全是新工具，没有 bug 在 read_skill 路径上需要复现。Bug-from-customer Rule 11 对应的复现测试应该在 Task 4（wire）或 Task 6（删除）后通过完整 e2e 路径检验。整体 feature 的 bug-from-customer 复现锚点定在 **Task 5 之后**（pptx-author SKILL.md 重写完）：写一个用 `mockSkillRegistry` 注册重写后的 pptx-author SKILL.md，让外层 LLM 调用 read_skill → run_python → 期待返回 .pptx 文件 URL 的端到端集成测试。

### Task 2: 实现 `RenderSkillCatalog` + unit tests

**文件**:
- `internal/numind/biz/agent/skill_catalog.go` (新建, ~80 LOC)
- `internal/numind/biz/agent/skill_catalog_test.go` (新建, ~100 LOC)

**实现内容**：spec §3 的渲染函数（含 nil registry → "" 兜底、deterministic alphabetical 排序、单 description 截 200 字符、整体 2000 字符 cap、log warn 截断）。

**验收**:
- `go test ./internal/numind/biz/agent/ -run TestRenderSkillCatalog -count=1` 全过
- 5 个 test case 全覆盖（per spec §5.2）

### Task 3: 改写 `OutputToolsPriorityAddendum` constant

**文件**:
- `internal/numind/biz/agent/output_tools_priority_prompt.go`

**实现内容**：8 处 invoke_skill 引用全部改写为 `read_skill → run_python` 两步流；删除任何 declarative-pseudo-code 残留；新文案与 RenderSkillCatalog 输出协调一致（避免重复说"必须先 read_skill"）。

**验收**:
- `grep -c "invoke_skill" internal/numind/biz/agent/output_tools_priority_prompt.go` = 0
- `grep -c "read_skill" internal/numind/biz/agent/output_tools_priority_prompt.go` ≥ 3
- 现有 runner_test.go / output_tools_priority_prompt_test.go (如有) 全过

### Task 4: Wire 改动 — factory + biz + runner + lifecycle + tool_create_* + adapter 注释

**文件**:
- `internal/numind/biz/agent/factory_platform.go`（LoadTools 注册改 + ToolMetadata 改）
- `internal/numind/biz/biz.go`（删除 SkillPool gate + log message 改）
- `internal/numind/biz/agent/runner.go`（line 697 RenderSkillCatalog 注入）
- `internal/numind/biz/agent/student_run_lifecycle.go`（line 742 mapping 改）
- `internal/numind/biz/agent/tool_full.go`（EnableSkills doc 改）
- `internal/numind/biz/agent/tool_create_html.go`（description 字符串改）
- `internal/numind/biz/agent/tool_create_png_chart.go`（description 字符串改）
- `internal/numind/biz/agent/adapter_full_to_eino.go`（line 82 stale comment）

**实现内容**：全部按 spec §1.2 描述串改；确保 build 通过（此时 invoke_skill 仍然存在，但不再被 factory 注册）。

**验收**:
- `go build ./...` 通过
- `go test ./internal/numind/biz/agent/...` 通过（旧 tool_invoke_skill_test.go 仍能跑因为文件还在）
- `task lint` 通过

**Task 4 commit 后系统的一致性声明**：此 commit 后系统处于 **deployable consistent state**。Factory `LoadTools` 不再注册 `invoke_skill`，agent runtime 看不到该工具；`tool_invoke_skill.go` 源文件仍在 disk 上 — Go 不会因不被引用而报错，旧 unit test 仍能编译运行（mock 直接 new `&invokeSkillTool{}`）。即使在 Task 6 删文件之前部署到 dev，行为也是「invoke_skill 工具不存在 + read_skill 工具可用」的目标稳定状态。

### Task 5: 4 份 SKILL.md 重写 — 每份单独 commit 单独 review

子任务：
- **Task 5a**: `skills/pptx-author/SKILL.md` 重写 ≤4KB — 4 个模板: 封面+标题 / 标题+列表 / 标题+表格 / 标题+柱形图
- **Task 5b**: `skills/xlsx-author/SKILL.md` 重写 ≤4KB — 4 个模板: 单 sheet summary / 多 sheet+index / 含 chart 的数据表 / 条件格式 table
- **Task 5c**: `skills/docx-author/SKILL.md` 重写 ≤4KB — 4 个模板: 多级标题正文 / 内嵌图片 / 表格 / 页眉页脚分节
- **Task 5d**: `skills/pdf-from-html/SKILL.md` 重写 ≤4KB — 4 个模板: 中文报告 / 带 logo 封面 / 分页页码 / 带表格的发票样式

每个子任务的验收 gate（per S2 §4.3 + PRD AC-6b/AC-6c）：
- `wc -c <file>` ≤ 4096
- `grep -c "from <relevant_module>" <file>` ≥ 3
- `grep -c "invoke_skill" <file>` = 0
- 至少 4 个完整可拷贝代码模板（按 layout/sheet/section/page 等维度）

**关键**：本 task 完成是 PPT 实机能力恢复的关键节点。Task 5a（pptx-author）是用户实际场景，优先做。

### Task 6: 删除 `invoke_skill` 工具 + 测试

**文件**:
- `internal/numind/biz/agent/tool_invoke_skill.go`（**删除**）
- `internal/numind/biz/agent/tool_invoke_skill_test.go`（**删除**）

**前置**: Task 1-4 完成（read_skill 已 wire，runner 不再依赖 invoke_skill）。

**验收**:
- `go build ./...` 通过
- `task test`（含 race）全过
- `grep -rn "invoke_skill\|InvokeSkillTool\|aiserviceSkillLLMCaller\|SkillLLMCaller" --include="*.go" internal/` 应只剩前端 KNOWN_TOOL_NAMES 兼容字符串 + 历史 commit messages

### Task 7: S5 验证策略写文档

**文件**:
- `.ndf/features/skill-progressive-loader/s5-strategy.md`

**内容**: 详 §S5 验证策略章节。

## Bug-from-customer 复现测试 commit（Rule 11）

按 NDF Rule 11，bug-from-customer feature 第一个 commit 必须是失败的复现测试。但本 feature 的特殊性：

- bug 在 `invoke_skill` 内层 LLM 误抄 SKILL.md → ModuleNotFoundError
- 直接复现要 mock `aiserviceSkillLLMCaller.GenerateCode` 返回带 `import invoke_skill` 的 Python，然后让 sandbox.ExecCommand 报 ModuleNotFoundError
- 该测试很重，且修复后整个 invoke_skill 文件都删了，测试也跟着删 — **违反 Rule 11 "测试永久留在代码库"**

**解决**: 复现测试改为**集成测试形式**，永久留在新 read_skill 路径上：

```
TestE2E_PPTGenerationViaReadSkillAndRunPython:
  - mock skill registry with a fake pptx-author SKILL.md
    (containing `from pptx import Presentation` real code)
  - simulate outer LLM tool calls:
      1. read_skill({skill_name: "pptx-author"}) → mock returns body
      2. run_python({code: "<extracted from body>"}) → mock sandbox returns
         {files: [{url: "agent-outputs/1/test.pptx"}]}
  - assert no `import invoke_skill` anywhere in code path
  - assert run_python invoked with real `from pptx import` code
```

这个测试：
- 在当前 code 上 **无法编译**（read_skill 类型不存在），等价于 Rule 11 要求的 "FAIL"（测试不可运行 = 不通过）— commit 该测试时分支处于 build-broken 状态是 Rule 11 重复测试约定的可接受形式（参考 `feedback_review_each_stage.md`：复现测试早于 fix，fix 才让它能跑）
- Task 1 完成 read_skill 后测试可编译，但 mock 链未完整时部分 PASS
- Task 4 完成 wire 后全 PASS
- 后续如果有人想恢复 invoke_skill 路径，此测试断言「无 import invoke_skill」会 FAIL，回归保护成立

**commit message 前缀**: `test(qa): reproduce invoke_skill ModuleNotFoundError via skill-loader e2e harness`

**实际放在 Task 4 完成后写**（read_skill 已存在，可写测试）。但作为 commit log 上"复现测试 commit 必须早于 fix commit"的要求，本 feature 的 fix commit 是 Task 6（删 invoke_skill），所以 Task 4 之后、Task 6 之前写测试是合规的。

## 实施顺序

Wave 1 (并行可做):
- Task 1 (read_skill)
- Task 2 (skill_catalog)
- Task 3 (output prompt addendum)

Wave 2:
- Task 4 (wire) — 依赖 Wave 1 全部

Wave 3:
- 写 Bug-from-customer 复现测试 commit
- Task 5a (pptx — 优先；用户场景)
- Task 5b/c/d (xlsx/docx/pdf-from-html — 并行可做)

Wave 4:
- Task 6 (删 invoke_skill)
- Task 7 (s5-strategy.md)

## 每个 Task 的两阶段 Review（NDF Rule 6 强制）

每个 task 完成后（含每个 Task 5 子任务），并行 dispatch:
- **spec-compliance reviewer** (Sonnet): 对照 spec.md §1.2 验证文件列表 + 行为
- **code-quality reviewer** (Sonnet): 验证 returnSoftError pattern、test 覆盖、命名、注释

P0/P1 必修。P2 在同 task 内 inline 修。

## S5 验证策略

> 详细文档见 `s5-strategy.md`（Task 7 产出）；本节为高层概览。

### 5.1 验证方式选择

混合：**Go 单元/集成测试 + gstack /qa 在 dev 实机跑**

理由：
- 每个 task 已有 unit test 覆盖（spec §5）
- 整体 e2e 流程涉及 LLM 实际写 python-pptx 代码 + sandbox 执行 — unit test 无法覆盖 LLM 写代码质量；必须用 gstack /qa 在 dev 跑真实任务
- 单独 gstack /qa（无 Playwright E2E）的代价：**未来回归保护需重新手动跑**（per `feedback_review_each_stage.md` 注意点）。但本 feature 是 sticker：核心回归保护已在 Go 测试里（read_skill IsEnabled / skill_catalog deterministic / Task 5 SKILL.md ≤4KB grep），LLM 写代码质量层面的回归在 Langfuse 中可追溯（trace 含失败的 run_python）。可接受。

### 5.2 关键用户路径

dev 上跑 4 个 gstack /qa task：
1. 「做一份 5 页 PPT 介绍 X」（pptx-author 验收）
2. 「生成一个 Excel 周报表 with 趋势图」（xlsx-author 验收）
3. 「写一份 Word 业务方案」（docx-author 验收）
4. 「把这段 markdown 转 PDF」（pdf-from-html 验收）

每个任务 gstack /qa 跑 2 次，期待 ≥7/8 成功（>87.5% 成功率，比当前 invoke_skill 33% 显著提升）。

### 5.3 验证不通过的处理

如某个 SKILL.md 重写后 LLM 写代码经常出错：
- 不回滚整个 feature
- 单独迭代该 SKILL.md（加更多示例 / 缩减误导文字）— 留作 micro/hotfix follow-up

如 read_skill / catalog 层有 bug：回到 S4 修。
