package port

import (
	"context"
)

// IntentType 意图类型（销售动作导向分类）
type IntentType string

const (
	// IntentObjection 异议/抗拒（嫌贵、不需要、犹豫、质疑价值）
	IntentObjection IntentType = "OBJECTION"
	// IntentComparison 竞品对比/选型（提到其他厂商或对比）
	IntentComparison IntentType = "COMPARISON"
	// IntentInquiry 产品/业务咨询（问参数、功能、资质）
	IntentInquiry IntentType = "INQUIRY"
	// IntentBuyingProof 购买信号/案例需求（问合同、价格、成功案例）
	IntentBuyingProof IntentType = "BUYING_PROOF"

	// Deprecated: 以下类型保留向后兼容，实际使用时会映射到新类型
	IntentAmbiguous IntentType = "AMBIGUOUS" // -> IntentInquiry
	IntentComplex   IntentType = "COMPLEX"   // -> IntentComparison
	IntentDirect    IntentType = "DIRECT"    // -> IntentInquiry
)

// IntentAnalysisResult 意图分析结果（包含多路搜索 Query + HyDE）
type IntentAnalysisResult struct {
	Intent           IntentType `json:"intent"`            // 主要意图
	SearchQueries    []string   `json:"search_queries"`    // 生成的多路搜索词（已改写消歧，3 个）
	HyDEQuery        string     `json:"hyde_query"`        // HyDE 假设性文档片段（用于语义匹配检索）
	Reason           string     `json:"reason"`            // 判断依据
	SalesInstruction string     `json:"sales_instruction"` // [free模式专用] 销售指令（如果有）
	CustomerMessage  string     `json:"customer_message"`  // [free模式专用] 客户原始消息
}

// IntentRouter 意图路由器接口
type IntentRouter interface {
	// AnalyzeIntentV2 分析用户意图并生成搜索策略（V2）
	// chatMode: "sales"（销售话术模式-输入为纯客户消息）或 "free"（自由讨论模式-输入可能包含销售指令+客户消息）
	// history: 最近的聊天记录，用于指代消歧
	// 返回: 意图分析结果（包含多路搜索词）
	AnalyzeIntentV2(ctx context.Context, query string, history []string, chatMode string) (*IntentAnalysisResult, error)

	// AnalyzeIntent 旧版接口，保持向后兼容
	// Deprecated: 请使用 AnalyzeIntentV2
	AnalyzeIntent(ctx context.Context, query string, history []string) (IntentType, string, error)
}
