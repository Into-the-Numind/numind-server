package port

import (
	"context"
)

// IntentType 意图类型
type IntentType string

const (
	IntentChitChat  IntentType = "CHIT_CHAT" // 闲聊 (Hello, Bye)
	IntentAmbiguous IntentType = "AMBIGUOUS" // 指代模糊 (它多少钱?)
	IntentComplex   IntentType = "COMPLEX"   // 复合意图 (对比A和B)
	IntentDirect    IntentType = "DIRECT"    // 直接查询 (产品A价格)
)

// IntentRouter 意图路由器接口
type IntentRouter interface {
	// AnalyzeIntent 分析用户意图
	// history: 最近的聊天记录，用于指代消歧
	AnalyzeIntent(ctx context.Context, query string, history []string) (IntentType, string, error)
}
