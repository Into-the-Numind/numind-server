# Agent 输出细节精修（agent-output-refine）

## 来源
- 提出人：User（dev 实跑 agent-output-redesign 后 7 条反馈）
- 提出日期：2026-06-16

## 需求 + 落点（已查清）
| # | 需求 | 落点 |
|---|------|------|
| 1 | 思考块展开/折叠箭头方向：折叠→右▶、展开→下▼ | 前端 `ThinkingBlock` 组件 chevron |
| 2 | 文件/图片命名丑（docx 全下划线 `..py-____.docx`、图 `gemini-image-{unix}.png`）| 前端 splitIntoSegments 用 markdown **链接文字/alt** 当显示名（现错用 URL path sanitized 名）；后端 image_gen 起干净默认名 |
| 3 | 图片别用大外框；单图 S2(无框+轻阴影+点击大图)、多图 M1(自适应网格) | 前端 AgentArtifactItem(图片样式)+splitIntoSegments(连续图分组)+AgentFinalAnswer |
| 4 | 标题分级字号 + 段间距（微妙不夸张）| 前端 AgentFinalAnswer markdown CSS |
| 5 | 表格/引用等带色格式 → 翠绿柔和（不刺眼）| 前端 AgentFinalAnswer markdown CSS |
| 6 | 「等你回答一个问题」绿底行还在转圈 | 前端从工具时间线**过滤 ask_user_question**（根因: tool-display.yaml 给它配 narration, yield 工具永拿不到 result→永久 in-flight 转圈+和问题卡重复）|
| 7 | 问题卡「已回答态」回看卡样式不统一（旧白卡）| 前端 QuestionPrompt answered-state recap 重设计为 C3 |

## 业务目标
最终交付细节的专业感与一致性。

## 优先级
高（体验细节，User 连续迭代中）

## 取证（关键）
- #6：`configs/tool-display.yaml:148-150` `ask_user_question: verb:"等你回答" detail:"一个问题"`，yield 工具 emit StateUse 后暂停永无 result。
- #2：run_python 后端保留原始 filename（`runPythonFileResult.Filename=name` 中文）但前端从 URL path 反推 sanitized 名（agentArtifacts.ts `filenameOf(url)`）；image_gen `gemini-image-{unix}.png` 纯时间戳（tool_image_gen.go:103）。markdown 链接文字(group 2)其实有可读名但没被用。
- #1：ThinkingBlock chevron；#7：QuestionPrompt 只改了 !answered 态 C3，answered recap 仍旧样式。

## Triage
- 推荐轨道：**Standard**
- 理由：跨仓库(后端 image 命名+前端多组件) / 影响文件 >3。无 DB schema/新 API/支付权限。
- 人类决定：确认 Standard 一个 feature 打包 7 项（User playground 选 #3=S2/M1）。

## 备注
- playground：`docs/numind-image-playground.html`(单图 S2/多图 M1)。
- #6 用前端过滤(安全, 不动后端 narration emit)。
- #2 image_gen 默认名给干净的(日期式中文显示名, COS key 仍 sanitize)。
- 多为前端 CSS/样式 + 1 后端命名 + #6 过滤。
