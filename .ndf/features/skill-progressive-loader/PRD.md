# PRD: skill-progressive-loader

> 用户视角功能描述与验收标准 · 2026-05-29

## 用户能感知到的变化

### 变化 1：PPT 生成任务从「时灵时不灵」变成「每次都能出文件」

**当前**：用户在 agent 对话中说「帮我做一份 PPT」，约 1/3 概率第一次成功，2/3 概率 agent 重试 1-2 次后才成功（dev log 显示 agent_run_id=78、83 都出现重试，83 最终失败）。即使成功也耗时 30-90 秒。

**变化后**：用户提需求后，agent 内部一次性生成正确代码并执行，首次成功率应≥90%（在 dev 用 gstack /qa 跑 5 次真实任务验收）。

### 变化 2：错误信息变得可读

**当前**：失败时 agent 对用户说「PPT 技能环境有问题」「文件系统受限」等无意义提示（这是 LLM 看到 ModuleNotFoundError 后的转述，无 actionable 信息）。

**变化后**：如果 `run_python` 执行失败，agent 看到的是真实 Python traceback（如 `KeyError: 'subtitle'`），有能力自行修正代码重试；对用户呈现的描述也更具体（如「图表数据格式有问题，正在调整」）。

### 变化 3：用户上传图片/数据可作为 PPT 素材

**当前**：invoke_skill 的 `input_files` 字段虽然支持，但用户在 agent 对话上传的 attachment URL 是否能被正确传入并非显而易见，且失败时无 fallback。

**变化后**：用户上传的图片（如品牌 logo）/ CSV（如数据表）经过 `/agent-attachments/<userID>/` URL 自动可被 agent 用 `file_read` 读取后嵌入 PPT，整个流程在同一 ReAct 循环内可见可追溯。

## 验收标准

| # | 标准 | 验证方式 | 通过门槛 |
|---|---|---|---|
| AC-1 | dev 上用 gstack /qa 跑「做一份 5 页关于 X 的 PPT 设计感强」任务，agent 输出有效 pptx 文件 | gstack /qa | 5 次跑 ≥ 4 次成功 |
| AC-2 | 失败时 agent 不再说「PPT 技能环境有问题」类无意义话术 | 实机跑 + 文本检查 | 0 次出现该文案 |
| AC-3 | Langfuse 中 trace 含 `read_skill` span + `run_python` span，不含 `invoke_skill` span | Langfuse UI 检查 | 100% |
| AC-4 | 现有非 invoke_skill 测试全部通过（go test ./internal/numind/biz/agent/...） | go test | 0 失败 |
| AC-5 | 新增 `read_skill` 工具的单元测试覆盖：skill 存在/不存在/path traversal 尝试/SKILL.md 不可读 | go test | ≥ 4 个 test |
| AC-6 | pptx-author SKILL.md 提供至少 4 个完整可运行 python-pptx 代码模板（封面/列表/表格/图表） | 人工检查 | 4 个示例都能 copy 直接 run |
| AC-7 | 部署后 dev 容器健康检查 200 OK，旧 `invoke_skill` agent run 历史的展示页面不报错（兼容） | curl + 前端检查 | 健康 + 0 控制台错误 |
| AC-8 | task lint 通过 | task lint | exit 0 |

## 不在本次范围（明确告知用户）

- ❌ 视频生成（不在 skill 体系）
- ❌ 在 PPT 内对图表做交互式编辑（仍是嵌入 PNG，pptx-chart-XML 留 V2 改进）
- ❌ 微信公众号 / 知乎导出（不在 skill 体系，依然走 agent 写文本+前端导出）
- ❌ 本 feature 不动 prod（per `feedback_agent_mode_autopilot.md`：只 dev 部署 + 验收）

## 用户故事样例

> **场景**：销售经理在莫小派 agent 对话中说"帮我做一份本周战报，10 页，封面要有公司 logo（已上传），数据表用蓝色主题"
>
> **期望流程**：
> 1. agent 看到「PPT」+「数据表」→ 决定调用 read_skill("pptx-author")
> 2. agent 读到完整 python-pptx 教程，包含 brand_config + slides 完整代码模板
> 3. agent 写出 Python 调用 `run_python({code: "...", input_files: ["<上传的 logo URL>"]})`
> 4. sandbox 跑 python-pptx 输出 .pptx 到 `/output/`
> 5. `/output/` 自动收集上传 COS，生成 `/agent-outputs/<userID>/...pptx` URL
> 6. agent 把 URL 嵌入回答消息，用户可下载
>
> **当前失败模式**：步骤 3 的 Python 代码里 `import invoke_skill` 失败，重试又失败，agent 放弃报错

## 相邻 feature 联动

- 本 feature 完成后**回写**`stream-emit-toolcall-events.known_issues` 字段（"判断为 LLM 偶尔出错"的旧判断是错的）；提供准确的事后报告。
- 本 feature 不影响 `file-read-accept-outputs-soft-errors` (Hotfix A) — Hotfix A 已让 file_read 接受 `/agent-outputs/`，本 feature 产生的 PPT 输出 URL 用 file_read 读回（如果后续 agent 想"读自己生成的 PPT 修改"）也是 OK 的。
