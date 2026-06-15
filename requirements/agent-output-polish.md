# Agent Mode 输出体验打磨（agent-output-polish）

## 来源
- 提出人：User（dev 实跑 agent-qa-card-ux 后的 4 条反馈）
- 提出日期：2026-06-15

## 需求描述
1. **去掉问题卡的「检查页」**：多问题卡目前答完所有题后会进一个 review 检查页再提交。希望最后一题的按钮直接变「提交」，点击即发给 AI。
2. **生成图片不显示 / 不进 docx**：AI 调用生成图片工具后，聊天页没看到图、下载的 docx 里也没有图。
3. **AI 回复里 emoji 太多不美观**：最终回答正文用了很多 emoji（📊🔑🥇📐📥）当装饰，希望不生成 emoji 或更美观。User 选定方案 = **禁用 emoji + 美化 markdown 渲染**。
4. **下载文档做成卡片式**：下载文档现在是普通文本链接，希望做成卡片让用户直观知道可点击下载。

## 业务目标
Agent mode 的「最终交付」体验直接影响用户对 AI 产出质量的信任。问题卡多余步骤、产物（图片/文档）不显眼或缺失、emoji 堆砌、下载入口不直观，都在削弱交付的专业感。

## 优先级
高（核心交付体验）

## 根因/落点摸底（AI 调查 + 运行时取证）
| # | 结论 | 落点 |
|---|------|------|
| 1 | `QuestionPrompt.vue` 多问题模式最后一题点「检查并提交」→ `reviewing=true` 进 review 步 | 前端：最后一题按钮直接 `submitAnswers()` |
| 2a | **取证推翻初判**：run 158 持久化最终回答里**确实嵌了** `![](url)`，图片 URL `curl` 200/image-png/1MB 有效；前端 `renderMarkdown` 正确产出 `<img>`。即聊天图**技术上正常渲染**，只是在正文末尾不显眼/易被忽略（User 截图正好停在图片上方一行） | 前端：把生成图片渲染成**显眼产物卡片**（与 #4 合并） |
| 2b | docx 由 AI 用 `run_python`+`docx-author` 技能现写 python-docx 生成；图没进 docx = AI 没把图 URL 传进 python 嵌入。AI 行为，非一处代码 bug | 后端：改 `docx-author` 技能/输出提示引导嵌图（尽力，不保证） |
| 3 | 平台基础提示（`skill/constants.go`）无任何「不用 emoji / 输出风格」指令，AI 自由发挥 | 后端：基础提示加「不用 emoji，用标题/加粗结构」；前端：美化 markdown 渲染（标题分级/间距/分隔） |
| 4 | 下载链接是 AI 在最终回答 markdown 里自己写的 `[下载完整报告](cos-url)`（非结构化 artifact；docx 工具是 stub 没用上） | 前端：`AgentFinalAnswer` 从最终回答 markdown 抽取图片 + 下载链接 → 渲染成产物卡片（复用 `AgentArtifactItem` 卡片样式），prose 部分去掉这些原始链接 |

**统一设计**：#2a + #4 合并 —— 最终回答里的「生成图片」和「可下载文档」都渲染成**产物卡片**，既解决下载卡片化、又让图片不被忽略，且因派生自持久化 markdown 故刷新后依然在。

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：否
  2. 新增 API 端点：否
  3. 新外部服务集成：否
  4. 影响文件数：>3（后端提示 + 前端多组件）
  5. 高风险业务逻辑（支付/权限）：否
- 人类决定：**确认 Standard，一个 feature 打包 4 个**（User 档位确认环节选定）

## 备注
- emoji 方案：User 选「禁用 + 美化渲染」。
- #2a 经运行时取证证明聊天图实际能渲染（不是 bug），改进点是「显眼度」——用产物卡片解决；S5 dev browse 复验真实渲染。
- #2b（docx 嵌图）诚实声明：AI 行为，只能靠技能指南引导，不保证 100%。
- 前序 feature：agent-qa-card-ux（刚部署 dev，含 issue4 SSE 流式续跑）。
- #1/#2a/#3 偏体验改进，非客户上报的功能性 bug；#2b/#4 同理。Rule 11 复现测试按可测性配（#1 可测 emit；产物卡片渲染可 vitest；emoji 提示无强回归测试，靠 prompt + 前端渲染测试）。
