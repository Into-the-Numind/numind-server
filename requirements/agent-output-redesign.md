# Agent 产物卡片重设计 + 反馈删除 + docx 嵌图（agent-output-redesign）

## 来源
- 提出人：User（dev 实跑 agent-output-polish 后的反馈 + 设计选型）
- 提出日期：2026-06-15

## 需求描述
基于 playground（`docs/numind-card-playground.html`）User 选定的设计，落地一批 agent 输出体验：
1. **文件下载卡 → A1（极简行内卡）**：图标+文件名+大小+下载按钮。
2. **图片卡 → B1（缩略卡）**：圆角缩略图+说明，点击看大图。
3. **多问题卡 → C3（对话感软卡）**：头像+问题+chip 选项+圆角输入框。
4. **#4 bug 修复**：当前 extractArtifacts 把 AI 写在句子里的下载链接抽走、留下空的「文件下载：」、卡片挪到底部。改成**就地分段渲染**——卡片出现在链接原位置，「文件下载：」后面直接是卡片，不留空。
5. **#5 删反馈条**：底部「这个回答对你有帮助吗？」👍👎 经查是坏的（前端发 `{feedback,note}`，后端要 `{verdict}` 必填→每次点都 400 啥都没存）+ 没任何地方读它（admin 无看板）+ SOP/chatbot 都没做。User 决定**删掉**（以后要做反馈成体系做）。前端 UI + 后端坏端点一起清。
6. **#2 docx 嵌图（强提示）**：docx 由 AI 用 run_python+docx-author 现写；取证 run159（新代码）AI 生成了图但 run_python NO input_files→图没进 docx。软提示无效。改成**强指令+代码模板**（生成过图必须 input_files+add_picture，位置交 AI）。

## 业务目标
最终交付（卡片/文档/问答）的专业感与可信度。

## 优先级
高

## 根因/取证（已查清）
- #4：extractArtifacts strip→append-bottom 设计导致「文件下载：」空 + 卡片脱节（dev run159 截图证实）。
- #5：字段名 `feedback/note` vs `verdict/text` 不匹配→400；admin/前端无任何消费方（纯写入黑洞）。
- #2：run159 AI 调 image_gen + run_python(docx) 但 NO input_files→图没进沙箱→add_picture 无图。AI 行为，机制是通的。位置必须 AI 代码决定（自动喂解决不了定位），靠强提示+代码模板拉高服从度（0%→预期 80-95%，非硬保证）。
- #3（不在本 feature）：定位调研助手 system_prompt 自己写「报告配封面图」→每次生图；模型 Gemini 2.5 Flash Image；提示词 AI 自由写后端零加工。User 自行改 agent 提示词。

## Triage
- 推荐轨道：**Standard**
- 理由：①DB schema 否 ②新增 API 否（反而删一个端点）③新外部服务 否 ④影响文件 >3（跨仓库前端卡片/反馈+后端技能/端点）⑤高风险业务 否
- 人类决定：确认 Standard，一个 feature 打包（User 选型 A1/B1/C3 + 确认删反馈 + 确认 #2 强提示方案）

## 备注
- 设计选型 playground：`docs/numind-card-playground.html`（A1/B1/C3）。
- #4 核心技术改动：AgentFinalAnswer 从「strip+底部追加」改「分段就地渲染」（prose/artifact 按原顺序交替）。
- #2 诚实声明：强提示提升服从度但非 100%。
- #3 由 User 改 agent 提示词，本 feature 不含。
- 偏体验/设计改进 + 2 个真 bug（#4 卡片留空、#5 反馈坏）。
