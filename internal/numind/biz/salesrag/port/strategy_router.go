package port

import (
	"context"

	"numind-server/internal/numind/biz/salesrag/domain"
)

// StrategyRouter 策略路由器接口
// 负责双层策略选择：综合策略 -> 基础策略
type StrategyRouter interface {
	// SelectMetaStrategy 从综合策略列表中选择最匹配的一个
	// 返回选中的综合策略ID
	SelectMetaStrategy(ctx context.Context, query string, history []string, metas []domain.MetaStrategy) (string, error)

	// SelectBasicStrategy 根据综合策略的决策树逻辑，选择最匹配的基础策略
	// input: decisionTree (决策树逻辑文本)
	// 返回选中的基础策略ID
	SelectBasicStrategy(ctx context.Context, query string, history []string, decisionTree string, basics []domain.BasicStrategy) (string, error)
}

// StrategyRouterResult 策略路由结果
type StrategyRouterResult struct {
	MetaID  string `json:"meta_id"`  // 选中的综合策略ID
	BasicID string `json:"basic_id"` // 选中的基础策略ID
	Reason  string `json:"reason"`   // 选择理由
}
