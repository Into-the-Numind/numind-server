package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/pkg/log"
)

// LLMRouter 基于大模型的意图路由器 (V3 - CoT + HyDE)
// 使用 DMXAPI 平台的 qwen-turbo-latest（深度思考模式）进行深度意图理解和查询改写
type LLMRouter struct {
	dmxClient *DMXAPIClient
}

// NewLLMRouter 创建新的 LLM意图路由器
func NewLLMRouter() *LLMRouter {
	return &LLMRouter{
		dmxClient: NewDMXAPIClient(),
	}
}

// ========== 销售话术模式 Prompt (V3 - CoT + HyDE) ==========
// 输入被认为是纯客户消息
const salesModePrompt = `你是一个资深的销售意图分析师和检索专家。你的任务是深度理解客户消息的真实含义，并生成高质量的知识库检索方案。

## 你需要完成的任务

### 任务 1: 深度意图理解
请对客户消息进行全维度深度分析，确保你真正理解了客户"话里话外"的含义。你必须从以下维度逐一思考：

1. 表层语义 vs 深层意图：字面上在说什么？真正想要的是什么？（例如："太贵了"可能不是真的嫌贵，而是在试探底线或者需要更多购买理由）
2. 情绪信号解读：信任/好奇/犹豫/焦虑/不耐烦/抗拒/期待/测试？背后驱动因素是什么？
3. 决策阶段判断：初步了解/深入对比/临门一脚/售后跟进/流失挽回？
4. 信息缺口识别：客户缺少什么信息才能做出决定？什么信息能最有效地推动对话前进？
5. 对话历史的态度变化：态度是在升温还是降温？是否有前后矛盾的信号？
6. 隐性需求挖掘：没说出来但可能存在的需求/核心顾虑是什么？

### 任务 2: 生成 3 个检索改写 Query
基于你的深度分析，生成 3 个用于检索知识库的高质量改写搜索词。

**检索目标参考（知识库通常包含以下三类文档）**：
- 产品/公司介绍（功能、参数、价格、服务等）
- 成功案例库（客户案例、合作成果、数据表现等）
- 百问百答（常见问题解答、异议处理话术等）

**改写规则**：
1. 灵活适配：这 3 个搜索词不需要生硬地一一对应上述三类文档。请根据你的深度分析，判断哪种检索方向对当前问题最有效。
2. 多维覆盖：这 3 个词应代表 3 种不同的检索策略，以追求最大化召回率。例如，如果这是一个纯技术问题，你可能会生成两个不同维度的产品参数查询和一个可能的相关案例查询；如果这是一个纯异议处理，你可能会生成多个不同侧重点的 Q&A 改写。
3. 独立性：每个搜索词必须独立、完整、包含主语，脱离上下文亦可理解。
4. 保留细节：必须保留核心实体名、技术参数或具体型号，严禁过度泛化。
5. 你必须在思考过程中解释故为什么要选择这 3 个特定的检索方向。

### 任务 3: 生成 1 个 HyDE（假设性文档）
想象你是知识库的作者。针对客户的这个问题，知识库中最可能包含答案的那篇文档长什么样？

请写一段 50-150 字的假设性文档内容：
- 写成百问百答或产品介绍文档中的一个条目的样子
- 直接包含客户问题的答案或相关信息
- 使用与知识库文档一致的专业但通俗的语言风格，不要写成建议或分析报告

## 对话历史
%s

## 客户当前消息
%s

## 输出格式（严格 JSON）
{"search_queries": ["改写词1", "改写词2", "改写词3"], "hyde_query": "假设性文档内容..."}`

// ========== 自由讨论模式 Prompt (V3 - CoT + HyDE) ==========
// 输入可能包含销售人员的指令 + 客户消息混合
const freeModePrompt = `你是一个资深的销售意图分析师和检索专家。你的任务是深度理解销售人员转发的内容，并生成高质量的知识库检索方案。

## 背景
销售人员转发的内容可能是：纯客户消息、销售指令、或两者的混合。你需要从中提取关键信息。

## 你需要完成的任务

### 任务 1: 区分内容并深度分析
首先识别输入中哪部分是客户消息，哪部分是销售指令。

针对客户消息，你必须从以下维度逐一进行深度思考：
1. 表层语义 vs 深层意图：客户转发的消息字面上在说什么？潜台词是什么？
2. 情绪信号解读：客户的情绪状态是什么？（犹豫/抗拒/期待等）
3. 决策阶段判断：客户处于哪个博弈阶段？
4. 信息缺口识别：客户缺少什么核心支撑？销售人员的额外指令暗示了什么痛点或难点？
5. 对话历史的态度变化：结合历史，客户的态度轨迹如何？
6. 隐性需求挖掘：客户最担心的成本、风险或信任点在哪里？

### 任务 2: 生成 3 个检索改写 Query
基于上述深度分析，生成 3 个高质量的改写搜索词。

**检索目标参考（知识库通常包含以下三类文档）**：
- 产品/公司介绍（功能、参数、价格、服务等）
- 成功案例库（客户案例、合作成果、数据表现等）
- 百问百答（常见问题解答、异议处理话术等）

**改写规则**：
1. 灵活适配：这 3 个词不需要生硬地一一对应上述三类文档。根据你的判断，生成最能解决实际问题的 3 个检索方向。
2. 客户视角核心：搜索词必须以客户消息中的核心实体和核心疑虑为中心。
3. 严禁动作化导出：严禁根据销售指令生成纯动作类搜索词（如“怎么夸奖客户”、“幽默话术”），必须生成能从知识库中检索到业务知识、产品细节或策略支撑的搜索词。
4. 策略独立性：3 个搜索词应代表 3 种不同的切入点或召回侧重。
5. 保留细节：独立完整，且保留原始消息中的关键参数和型号。
6. 你必须在思考过程中解释这 3 个检索词的选取逻辑。

### 任务 3: 生成 1 个 HyDE（假设性文档）
想象你是知识库的作者。针对该场景，写一段 50-150 字的假设性知识库原文：
- 写成百问百答或介绍文档的一个条目。
- 直接包含解决客户问题所需的信息或策略。
- 风格专业且通俗，坚决不要写成建议或分析。

## 对话历史
%s

## 销售人员转发的内容
%s

## 输出格式（严格 JSON）
{"search_queries": ["改写词1", "改写词2", "改写词3"], "hyde_query": "假设性文档内容...", "sales_instruction": "识别出的销售指令", "customer_message": "识别出的客户消息"}`

// AnalyzeIntentV2 深度理解用户意图并生成检索方案（V3 - CoT + HyDE）
// chatMode: "sales"（销售话术模式）或 "free"（自由讨论模式）
func (r *LLMRouter) AnalyzeIntentV2(ctx context.Context, query string, history []string, chatMode string) (*port.IntentAnalysisResult, error) {
	// 构建历史上下文
	historyStr := "无"
	if len(history) > 0 {
		// 只取最近 5 轮历史
		recentHistory := history
		if len(history) > 5 {
			recentHistory = history[len(history)-5:]
		}
		historyStr = strings.Join(recentHistory, "\n")
	}

	// 根据模式选择不同的 Prompt
	var prompt string
	if chatMode == "free" {
		prompt = fmt.Sprintf(freeModePrompt, historyStr, query)
	} else {
		// 默认使用销售话术模式
		prompt = fmt.Sprintf(salesModePrompt, historyStr, query)
	}

	messages := []ChatMessage{
		{Role: "user", Content: prompt},
	}

	// 调用 qwen-turbo-latest（深度思考模式）
	resp, err := r.dmxClient.ChatCompletionWithThinking(ctx, "qwen-turbo-latest", messages, 0.1, 2000)
	if err != nil {
		log.C(ctx).Errorw("LLM intent analysis failed", "error", err, "chatMode", chatMode)
		// Fallback: 返回原始查询
		return &port.IntentAnalysisResult{
			SearchQueries: []string{query},
		}, nil
	}

	// 解析 JSON 响应
	jsonStr := extractJSON(resp)

	var result port.IntentAnalysisResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		log.C(ctx).Warnw("Failed to parse intent JSON", "response", resp, "error", err, "chatMode", chatMode)
		// Fallback
		return &port.IntentAnalysisResult{
			SearchQueries: []string{query},
		}, nil
	}

	// 确保生成了 3 个搜索词
	if len(result.SearchQueries) == 0 {
		result.SearchQueries = []string{query}
	}
	// 截断到最多 3 个改写 Query
	if len(result.SearchQueries) > 3 {
		result.SearchQueries = result.SearchQueries[:3]
	}

	// 确保原始 Query 始终在搜索列表中（底层安全保障）
	originalQueryExists := false
	for _, q := range result.SearchQueries {
		if q == query {
			originalQueryExists = true
			break
		}
	}
	if !originalQueryExists {
		// 将原始 Query 添加到列表开头
		result.SearchQueries = append([]string{query}, result.SearchQueries...)
	}

	log.C(ctx).Infow("Intent analysis completed (V3 CoT+HyDE)",
		"query", query,
		"chatMode", chatMode,
		"queries", result.SearchQueries,
		"hyde_query_len", len(result.HyDEQuery),
		"salesInstruction", result.SalesInstruction,
		"customerMessage", result.CustomerMessage)

	return &result, nil
}

// AnalyzeIntent 旧版接口，保持向后兼容
func (r *LLMRouter) AnalyzeIntent(ctx context.Context, query string, history []string) (port.IntentType, string, error) {
	// 默认使用 sales 模式
	result, err := r.AnalyzeIntentV2(ctx, query, history, "sales")
	if err != nil {
		return port.IntentInquiry, query, err
	}
	// 返回第一个搜索词作为改写结果
	rewrite := query
	if len(result.SearchQueries) > 0 {
		rewrite = result.SearchQueries[0]
	}
	return result.Intent, rewrite, nil
}

// extractJSON 从响应中提取 JSON 字符串
func extractJSON(resp string) string {
	// 尝试找到 JSON 对象
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start != -1 && end != -1 && end > start {
		return resp[start : end+1]
	}
	return resp
}

