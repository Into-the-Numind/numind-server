# agent-multi-question 实现蓝图

> R1 走查拍板（2026-06-10）：ask_user_question 从"单问题+2-4选项"重构为 **Claude Code 式 questions 数组**。
> 参考源码：`/Users/zhiyuchen/Downloads/ClaudeCode/src/tools/AskUserQuestionTool/`
> 用户拍板：照搬 Claude Code 问题数组模型（不拆两个工具）；现在就干完整做完，跨仓库。
> 记忆：[[project-agent-multi-question-model]]

## 为什么（根因）

截图暴露：agent 想问 4 个维度（陪跑/创始人/客群/业绩）→ 塞进一个问题的 4 个 checkbox 选项 → 选项是"维度名"非"答案"，用户勾了传达不了信息。根因=一个工具硬扛"开放收集"+"决策选择"两种冲突交互。Claude Code 的解法：questions 数组（每个 question 独立、各有候选答案 + 自动"其他"），逐个问可翻阅修改。

## 目标数据结构（照搬 Claude Code）

### 工具 input（ask_user_question 新 schema）
```
{
  "questions": [            // 1-4 个独立问题
    {
      "question": "你们的陪跑周期多长？",   // 文本，? 结尾，multiSelect 时复数
      "header": "陪跑模式",                // ≤12 字标签
      "options": [                         // 0 或 2-4 个（0=纯开放）
        {"key":"a","label":"90天","description":"..."},   // label 1-5 字简洁
        {"key":"b","label":"180天","description":"..."}
      ],
      "multi_select": false
    }
  ]
}
```
- 1-4 个 questions；每 question 0 或 2-4 options（沿用 ask-question-options-tolerant：>4 截断、0 开放、1 拒绝）
- **唯一性**：question text 唯一 + 每 question 内 label 唯一
- **"其他"自动**：前端给每个 question 自动加"其他/自由填写"，agent 不定义（沿用现有底部 free_text，但 per-question）

### 答案协议（回传）— T2 实现形态（结构化，非扁平字符串）
```
POST /v1/agent-runs/:id/answer
answers: { "<question text>": { "selected": ["<label>", ...], "free_text": "<text>" } }
```
- key = 问题文本；`selected` = 选中选项的 **label**（key 不下发前端，前端按 label 识别）；多选可多个 label，单选最多 1 个；纯开放问题只填 `free_text`。
- 校验（后端）：每个 key 必须对应已问的问题；每条至少有 selected 或 free_text；单选 ≤1、多选 ≤4；至少回答一个（跳过=省略该 key）。
- 服务端 `resolveAnswer` 把 selected（顿号「、」连接）+ free_text（「；」连接）拼成单条答案字符串。前端**不要**预先拼接。
回传 LLM：`用户已回答你的问题：\n- 「陪跑周期多长？」→ 90天\n- 「主要客群是谁？」→ 宝妈、职场人；一二线城市\n请据此继续，不要重复已回答的问题。`

## Task 分解（S4，每 task TDD + 双 review）

### 后端（numind-server）
- **T1 工具+协议数据结构**：YieldPayload/YieldOption → 改为 `Questions []YieldQuestion`（每个 YieldQuestion{Question,Header,Options,MultiSelect}）；ask_user_question InputSchema/Execute 重构（1-4 questions，每 question 沿用 0/2-4 options 容错+唯一性校验）；stream QuestionPromptPayload → questions 数组。**向后兼容**：旧单问题 pending_question_json 读取兼容（迁移期）。
- **T2 answer 端点协议**：AnswerRequest 从 `{selected, free_text}` → `{answers: map[string]AnswerItem}`（每 question 一个答案）；buildAnswerMessage 多问题拼装；跨字段校验（每 question 至少答一个或允许部分跳过——按 Claude Code 可跳过）。controller 同步。
- **T3 快照恢复**：synthesizeQuestionPrompt（student_query.go）→ 多问题合成；agentMessage question_prompt 字段改 questions 数组。

### 前端（numind-web-v3）
- **T4 类型+协议**：QuestionPromptMessage → questions 数组；SessionSnapshot/stream event 类型同步；api answer 提交协议改 answers map。
- **T5 多问题导航组件**（核心，全新）：QuestionPrompt 重构——标签栏(Q1☑ Q2☐…进度+←→翻阅+点击回改) + 当前问题视图(选项+底部自由填写) + Review 页(全 Q&A+提交)。状态：currentQuestionIndex + per-question {selected, freeText}。参考 Claude Code use-multiple-choice-state + QuestionNavigationBar + QuestionView + SubmitQuestionsView。
- **T6 store+联调**：agentChat 收集多问题答案 → answers map 提交；AgentMessageItem 传 questions。

### S5 验证
- 后端：go test（协议+Execute+answer+快照）
- 前端：vitest（多问题导航组件交互：翻阅/修改/Review/提交）
- dev gstack /qa：真实多问题 agent 提问 → 逐个答 → 翻阅改 → 提交 → resume

## 顺序与依赖
T1（协议基础）→ T2/T3（后端依赖 T1）→ T4（前端类型依赖 T1 协议）→ T5（组件）→ T6（联调）。前后端协议必须一致才能部署（不可只部署一端）。

## 兼容性
- 进行中的 waiting run（旧单问题格式）：T1/T3 读取兼容旧 pending_question_json（单问题视为 questions=[单个]）。
- 部署：前后端必须同时上 dev（协议变更）。

## 风险
- 大改动跨仓库 + 全新前端组件，单 session context 可能不够 → 每 task 完成即可作为 handoff 断点（T1-T3 后端先行，T4-T6 前端，蓝图在此可续作）。
