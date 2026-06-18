# Agent Mode 输出与附件呈现 UX 修复

## 来源
- 提出人：用户（运营/测试中发现）
- 提出日期：2026-06-18

## 需求描述
用户在使用 Agent mode 过程中发现 4 个问题（第 3 项原为疑问，已查代码回答，不进修复范围）：

### 问题一：技能名称显示缺失（前端 bug）
Agent 调用技能时显示"正在加载技能 / 已加载技能"，但**不显示技能名称**。
- 根因：后端 `tool_call_start` 事件的 `input_preview` 已带 `name`、narration 模板也写对（`正在加载技能：{{ .input.name }}`），但前端 `agentChat.ts` 的 `actionLabels` 字典漏了 `load_skill`，落入默认分支显示"正在调用工具 load_skill..."；`streamingToolUseLabel()` 也没有 `load_skill` 分支去取 `input_preview.name`。
- 修复侧：前端（numind-web-v3）。

### 问题二：系统提示泄露到用户气泡（后端 bug）
用户上传附件并发送后，聊天气泡里显示系统引导文案"【系统提示】用户上传了以下附件，请立即调用 file_read 工具读取它们的内容…"。该提示应只进 LLM 上下文，不应展示给用户。
- 根因：`buildAgentInput()`（`student_run_lifecycle.go:419`）拼接的字符串（用户原文+系统提示+附件URL）被**同时**用于喂 LLM（正确）和持久化进 `agent_run.Messages`（`runner.go:1324`，错误）；前端 `transformMessages`（`student_query.go:613`）原样渲染 `turn["content"]`，于是系统提示显示出来了。
- 修复侧：后端（numind-server）——分离"给 LLM 的 input"与"持久化/展示的用户原文"，保证引导指令仍进 LLM 上下文。

### 问题三（已回答，非修复项）：附件读取的底层机制
当前两条路径并存：
- Legacy（AttachmentURLs）：拼接 URL 列表 + 系统提示，**AI 主动调 `file_read`** 工具实时读取（`tool_file_read.go`）。
- 新（AttachmentIDs + 多模态路由 `multimodal.go`）：按模型能力分流——支持原生图片/PDF 则嵌 URL 块让 AI 可选读取；不支持则后台 fallback 预处理转文字注入（`waitForFallback` → `fallback_service.go`，这里才用到 MarkItDown 类提取）。
结论：**主路径是 AI 主动调 file_read，MarkItDown 仅在"模型不支持原生多模态"兜底分支用**。此项只回答疑问，不改代码。

### 问题四（原编号"问题3"）：工具调用进行中缺明显反馈（前端 UX）
AI 调用长耗时工具（docx/HTML 生成）时缺乏明显"进行中"反馈，用户不知是卡死还是在跑。
- 根因：后端已有 `tool_call_progress` 事件，前端也有 `Loader2` spinner（`AgentToolCallItem.vue:118-125`），但 label 计算（同文件 36-42 行）运行中永远显示**首个事件**文案，后续 progress 消息不更新；长工具中间一片静止。
- 期望：显示最新进度消息 + 更明显的"进行中"动效（用户接受闪烁点等非进度条形式）。
- 修复侧：前端为主（如 docx/html 工具后端未发 progress 事件，可顺带补发）。

### 问题五（原"最后一个问题"）：生成文件统一走卡片而非下载链接（后端为主）
生成文档类文件后只给下载链接（点击直接下载），不是卡片，无法在线预览/编辑。
- 根因：后端 `image_collector.go` 只收集**图片**并在 `finalizeRun`（`runner.go:559-571`）以 `![](url)` 嵌入最终回答 markdown；文档类（docx/html/pdf）未被收集、未嵌入。文档链接只能靠 AI 自己手写 `[文件名](url)`，若非独占一行则前端 `standaloneArtifactOf`（`agentArtifacts.ts:284`）不提取成卡片，渲染成普通超链接。前端卡片组件 `AgentArtifactItem.vue` 与 `DOC_EXTS` 其实已支持 docx/html/pdf 的卡片+预览。
- 用户诉求：(a) 所有生成文件都应以卡片呈现（卡片自带下载按钮，更清晰）；(b) 至少支持预览的应给卡片。
- 修复侧：后端为主——建对称的 artifact 收集器，把所有生成文件统一收集并在 finalizeRun 按类型嵌入（图片 `![]`、文档 `[]` 独占一行），前端自动识别成卡片无需改动。

## 业务目标
提升 Agent mode 的输出可读性与可信度：技能调用透明（显名）、不泄露系统内部提示、长任务有进度感、生成物以统一卡片承载（可下载/可预览/可编辑），减少"卡死了吗""文件去哪了"的困惑，是产品打磨的关键一环。

## 优先级
高（直接影响 Agent mode 日常使用体验与专业观感）

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：否
  2. 新增 API 端点：否
  3. 新外部服务集成：否
  4. 影响文件数：**>3**（跨 numind-server + numind-web-v3，约 6-8 文件）
  5. 高风险业务逻辑（支付/权限）：否
  - 单独看 4 个修复各满足 Hotfix 条件，但合并打包跨前后端、文件数 >3 → 升 Standard（用户已确认"一个 Standard feature 打包"）。
- 人类决定：升级为 Standard（用户 2026-06-18 已选）

## 备注
- 问题二、问题五是真正的逻辑/正确性 bug（用户测试报告 → Bug-from-Customer，Rule 11）：对应修复 task 第一个 commit 须为失败的复现测试（后端 Go test，断言持久化消息不含系统提示 / 生成文档被嵌入 final answer markdown）。
- 问题一、问题四偏前端显示/UX 打磨，按 CLAUDE.md "前端 UI bug 先用 Playwright/gstack 诊断"，S5 用 gstack `/qa` 浏览器验证。
- 问题二修复时务必保留"引导 AI 调 file_read"的指令进入 LLM 上下文（与问题三机制相关），仅从展示/持久化文本剥离。
- 涉及仓库：numind-server（问题二、五）+ numind-web-v3（问题一、四，及问题五前端自动受益）。
