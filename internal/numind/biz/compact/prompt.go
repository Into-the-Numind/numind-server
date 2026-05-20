package compact

// NoToolsPreamble is prepended to compact-request system prompts to suppress
// tool-calling in the single-turn summarization (blueprint §4.8.2). Empirical
// effect: tool_use rate drops from 2.79% to 0.01%.
const NoToolsPreamble = `【重要】你现在的任务是生成对话摘要。在本次任务中：
- 禁止调用任何工具
- 禁止使用 function_call / tool_use 格式
- 只需输出纯文本摘要
- 不需要向用户提问

请直接输出摘要内容，无需任何前缀或解释。`

// BaseCompactPrompt instructs the LLM to output a 9-section summary with two
// verbatim-quoting sections (6 and 9) that prevent intent / task drift
// (blueprint §4.8.3).
const BaseCompactPrompt = `请按以下 9 节结构输出对话摘要：

1. 主要请求和意图
   ────────────────
   用 1-3 句话描述学员最初想解决什么问题。精确，不加推断。

2. 关键技术概念
   ────────────────
   本次会话涉及的专业名词、工具、方法论。bullet list，每项一行。

3. 文件和代码片段
   ────────────────
   学员上传的文件名、处理结果、生成的产物。代码片段仅保留函数签名和关键注释。

4. 错误和修复
   ────────────────
   出现过的错误及其解决方案。格式：问题 → 原因 → 解决方案。

5. 问题解决过程
   ────────────────
   agent 尝试过哪些策略，哪些成功，哪些失败及原因。

6. 所有用户消息原文（防 intent drift）
   ────────────────
   verbatim 引用每条用户消息，不压缩、不改写。

7. 待办任务
   ────────────────
   明确承诺给学员但尚未完成的事项。

8. 当前进展
   ────────────────
   截至压缩点，已完成了什么，到达了哪个阶段。

9. 可选下一步（verbatim 引用，防 task drift）
   ────────────────
   如果 agent 已说"接下来我会..."，原文引用。若未说，此节留空。
`

// FullCompactSystemPrompt returns the concatenation of NoToolsPreamble and
// BaseCompactPrompt. Callers inject this as the system prompt of the compact
// LLM call (§4.8.2 + §4.8.3).
func FullCompactSystemPrompt() string {
	return NoToolsPreamble + "\n\n" + BaseCompactPrompt
}
