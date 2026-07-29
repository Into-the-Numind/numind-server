// Package attachment provides the async fallback generation service for agent
// attachments. It generates textual descriptions (VLM) and ASR transcripts for
// uploaded files so that single-modal LLMs can reason about them.
package attachment

// VLMSystemPrompt is the system prompt for the attachment.vision_describe task.
// It instructs the VLM to produce a structured Chinese description tailored for
// the sales-assistant and general knowledge-worker contexts supported by Numind.
const VLMSystemPrompt = `你是AI工作台的视觉分析员，负责将图片内容转换为精准的文字描述，供文本模型使用。

请按以下场景之一识别图片内容后进行结构化描述（200-400字）：

- 客户聊天截图：完整还原对话顺序，标注发言者和关键诉求
- 产品/UI截图：列出界面元素、品牌标识、关键功能点
- 数据图表：图表类型、坐标轴标签、关键数值、趋势方向
- 自然实物图：物体、品牌、型号、可见参数
- 设计稿/合同/单据：版式、关键字段、金额/日期/落款
- 其他：按实际内容如实描述

输出要求：
- 仅描述事实，禁止编造、评价或给出建议
- 用中文输出，简洁准确
- 若图片模糊或无法识别，如实说明`

// VLMUserPromptTemplate is a fmt-style template for the user turn of the VLM
// request. The caller substitutes %s with the image URL.
const VLMUserPromptTemplate = "请描述这张图片的内容。"

// PDFExtractPrompt is the user-turn prompt for attachment.pdf_extract (qwen-long).
// It instructs the model to extract all text from a PDF URL without summarising.
// The caller appends the PDF URL to this string before sending.
const PDFExtractPrompt = "请提取以下PDF文档的全部文字内容，只输出提取的文字，不做摘要或分析：\n"
