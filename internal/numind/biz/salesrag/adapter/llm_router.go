package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/pkg/log"
)

// LLMRouter 基于大模型的意图路由器 (V2)
// 使用 DMXAPI 平台的 qwen-turbo-latest 进行意图识别和查询生成
type LLMRouter struct {
	dmxClient *DMXAPIClient
}

// NewLLMRouter 创建新的 LLM 意图路由器
func NewLLMRouter() *LLMRouter {
	return &LLMRouter{
		dmxClient: NewDMXAPIClient(),
	}
}

// ========== 销售话术模式 Prompt ==========
// 输入被认为是纯客户消息
const salesModePrompt = `你是一个专业的销售意图分析师。分析以下客户发来的消息，完成两个任务：

## 任务 1: 意图分类
将客户消息归类为以下类别之一：
- CHIT_CHAT: 闲聊/寒暄/表情包/无需业务回复
- OBJECTION: 异议/抗拒（嫌贵、不需要、还要考虑、质疑价值）
- COMPARISON: 竞品对比/选型/提到其他厂商
- INQUIRY: 产品/业务咨询（问参数、功能、资质）
- BUYING_PROOF: 购买信号/案例需求（问合同、价格、成功案例）

## 任务 2: 搜索词生成
基于意图，生成 2-3 个用于检索知识库的搜索词。要求：
1. 每个搜索词必须独立、完整、包含主语（不能用"它"、"这个"）
2. 如果是模糊指代，结合对话历史补全主语
3. 针对不同维度生成（如：产品参数、销售话术、成功案例）
4. 如果是闲聊类型，search_queries 可以为空数组

## 对话历史
%s

## 客户当前消息
%s

## 输出格式（严格 JSON，不要包含其他内容）
{"intent": "OBJECTION", "search_queries": ["Numind 价格异议话术", "Numind 成功案例 ROI"], "reason": "客户表达了价格顾虑"}`

// ========== 自由讨论模式 Prompt ==========
// 输入可能包含销售人员的指令 + 客户消息混合
const freeModePrompt = `你是一个专业的销售意图分析师。分析以下销售人员转发的内容，完成三个任务：

## 背景
销售人员在微信和客户聊天，现在向你转发了一段内容。这段内容可能是：
1. 纯客户消息（如："客户说：最近太忙了"）
2. 销售人员的问题/指令（如："帮我想个促成话术"）
3. 混合内容（客户消息 + 销售人员的指令）

## 任务 1: 区分内容
识别输入中哪部分是客户消息，哪部分是销售指令。

## 任务 2: 意图分类（针对客户消息）
将客户消息归类为以下类别之一：
- CHIT_CHAT: 闲聊/寒暄/表情包/无需业务回复
- OBJECTION: 异议/抗拒（嫌贵、不需要、还要考虑、质疑价值）
- COMPARISON: 竞品对比/选型/提到其他厂商
- INQUIRY: 产品/业务咨询（问参数、功能、资质）
- BUYING_PROOF: 购买信号/案例需求（问合同、价格、成功案例）

如果没有客户消息，只有销售指令，意图设为 INQUIRY。

## 任务 3: 搜索词生成
基于客户意图和销售指令，生成 2-3 个用于检索知识库的搜索词。

## 对话历史
%s

## 销售人员转发的内容
%s

## 输出格式（严格 JSON，不要包含其他内容）
{"intent": "OBJECTION", "search_queries": ["价格异议话术", "产品价值卖点"], "reason": "客户嫌贵", "sales_instruction": "帮我想个回复", "customer_message": "太贵了，考虑考虑"}`

// AnalyzeIntentV2 分析用户意图并生成搜索策略
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

	// 调用 qwen-turbo-latest
	resp, err := r.dmxClient.ChatCompletion(ctx, "qwen-turbo-latest", messages, 0.1, 500)
	if err != nil {
		log.C(ctx).Errorw("LLM intent analysis failed", "error", err, "chatMode", chatMode)
		// Fallback: 返回默认询问意图
		return &port.IntentAnalysisResult{
			Intent:        port.IntentInquiry,
			SearchQueries: []string{query},
			Reason:        "LLM 分析失败，使用原始查询",
		}, nil
	}

	// 解析 JSON 响应
	jsonStr := extractJSON(resp)

	var result port.IntentAnalysisResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		log.C(ctx).Warnw("Failed to parse intent JSON", "response", resp, "error", err, "chatMode", chatMode)
		// Fallback
		return &port.IntentAnalysisResult{
			Intent:        port.IntentInquiry,
			SearchQueries: []string{query},
			Reason:        "JSON 解析失败，使用原始查询",
		}, nil
	}

	// 验证意图有效性
	result.Intent = normalizeIntent(result.Intent)

	// 确保至少有一个搜索词（除非是闲聊）
	if len(result.SearchQueries) == 0 && result.Intent != port.IntentChitChat {
		result.SearchQueries = []string{query}
	}

	log.C(ctx).Infow("Intent analysis completed",
		"query", query,
		"chatMode", chatMode,
		"intent", result.Intent,
		"queries", result.SearchQueries,
		"salesInstruction", result.SalesInstruction,
		"customerMessage", result.CustomerMessage,
		"reason", result.Reason)

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

// normalizeIntent 验证并规范化意图类型
func normalizeIntent(intent port.IntentType) port.IntentType {
	switch intent {
	case port.IntentChitChat, port.IntentObjection, port.IntentComparison, port.IntentInquiry, port.IntentBuyingProof:
		return intent
	default:
		return port.IntentInquiry // 默认为咨询
	}
}
