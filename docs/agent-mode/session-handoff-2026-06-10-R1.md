# Session Handoff — Agent Mode 上线冲刺 R1（2026-06-10）

> 给下一个 session 的完整交接。读完即可无缝续作。

## 一句话状态

R1 走查实战进行中。今天闭环了 **6 个 P0 + 1 个交互 feature**，全部上 dev。**唯一未完成**：`agent-multi-question`（Claude Code 式多问题数组重构，最大改动）——蓝图就绪、worktree 已建、等 fresh session 做 S4 实现。

## 下一个 session 第一件事

**继续 `agent-multi-question` feature**：
- 蓝图（必读）：`numind-server/docs/agent-mode/multi-question-blueprint.md`（= worktree 内 `.ndf/features/agent-multi-question/requirement.md`）
- 参考源码：`/Users/zhiyuchen/Downloads/ClaudeCode/src/tools/AskUserQuestionTool/`（标准答案）
- 记忆：`project_agent_multi_question_model.md`
- worktree 已建：`/private/tmp/wt-agent-multi-question-numind-server` + `/private/tmp/wt-agent-multi-question-numind-web-v3`（branch `feature/agent-multi-question`）
- 进度：S3 蓝图完成，requirement 已 commit 到 feature 分支。**从 S4 T1（后端协议数据结构）开始**。
- 顺序：T1 协议 → T2 answer 端点 → T3 快照 → T4 前端类型 → T5 多问题导航组件（核心，全新）→ T6 store 联调。前后端协议必须一致才能部署。
- 每 task TDD + 双 Sonnet reviewer（规则 6）。

## 今天已闭环（全部 develop + dev，未上 prod）

| # | 名称 | 是什么 |
|---|------|--------|
| BUG-1 | budget-tracker-token-units | 预算护栏把 token 当积分，run 秒死 |
| BUG-2 | permission-allow-sandboxed-exec | 权限拦死沙箱工具，文档生成全灭 |
| BUG-3 | stream-yield-errpath | 流式 yield 杀 run + 空転录 |
| BUG-4 | yield-session-reload | waiting 会话刷新空白 |
| BUG-5 | yield-resume-context | resume 后 agent 失忆（HW-33） |
| BUG-6 | ask-question-options-tolerant | >4 选项崩 run（我引入的回归） |
| feat | ask-question-freetext | 提问加自由填写框 + agent 行为引导 |
| from-scratch-q6q7 | （早合并） | 创建表单 422 死路 |

> BUG-1~5 中三个直接证伪了旧 prod-readiness-test-plan 标记的"已关闭"红线。

## R1 走查产出的设计裁决（design-baseline 已记）

- **D-1**：agent 入口改"工作区"卡片区（与 SOP/chatbot 并列）— 待做
- **D-2~D-8**（配置端走查，从 R1 建壳暴露）：一句话描述/欢迎语=自动生成可改；行为指引→改名"提示词"；任务类型/材料类型=删除（必填且无意义）；头像=删除（无处显示，待核实）；会话积分上限=删表单+默认 800→**10000**；高级模式=删；Agent 模板库=删（只留 skill marketplace 官方店）
- **D-9**：ask_user_question 交互重设计 → 已 closed（ask-question-freetext + 即将做的 multi-question）
- 配置模式 v1=陪跑式（创始人访谈专家挖方法论，专家 5 分钟试聊）；试聊按钮跳聊天页（P1 必修，待做）
- 锚定 agent = 定位调研助手（agent 100008，dev）；方法论=公司中心三步定位（吃透公司→竞对→差异化）

## 关键 backlog（`numind-server/docs/agent-mode/launch-backlog.md`）

未做的高优先级：
- **D-2~D-8 配置端裁决**（删字段/改文案/积分默认改 10000）— 一批前端 micro/hotfix
- **D-1 工作区卡片入口** — 前端改造
- **试聊跳转**（配置端 P1 必修）
- **HW-35**：prompt 引导 agent 用工具问是软约束，需 dev 验证；不行则 runner 层强制（agent-multi-question 可能顺带改善）
- **HW-17**：presigned URL 24h 过期图裂（P1，试点隔日必现）
- **HW-1/HW-2**：compliance 输出检查未接线 / sandbox 网络无出口限制（P1）
- 一批 HW-4~HW-34 硬基建 verify 项（红线残留 + 计费/安全纵深）

## 流程提醒

- 走查铁律：**用户旅程动作用户亲手执行**，AI 只取证/修复，探针用 E2E 测试账号+先征同意（memory `feedback_walkthrough_user_executes`）
- 全程止步 dev，prod 等用户明示
- dev 锚定 agent 100008（定位调研助手）；E2E 账号 user_id=1
- 产品名"有数"，客户名"莫小派"（memory `project_naming_youshu_vs_moxiaopai`）

## 流程 SOT
`docs/superpowers/specs/2026-06-10-agent-mode-launch-process-design.md`（方案 C 走查主轴）
