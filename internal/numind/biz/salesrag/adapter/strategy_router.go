package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/pkg/log"
)

// StrategyRouter 基于LLM的策略路由器
// 使用 qwen-turbo-latest 进行策略选择
type StrategyRouter struct {
	dmxClient *DMXAPIClient
}

// NewStrategyRouter 创建新的策略路由器
func NewStrategyRouter() *StrategyRouter {
	return &StrategyRouter{
		dmxClient: NewDMXAPIClient(),
	}
}

// SelectMetaStrategy 从综合策略列表中选择最匹配的一个
func (r *StrategyRouter) SelectMetaStrategy(ctx context.Context, query string, history []string, metas []domain.MetaStrategy) (string, error) {
	if len(metas) == 0 {
		return "", fmt.Errorf("no meta strategies provided")
	}

	// 构建策略选项描述
	var options strings.Builder
	for i, m := range metas {
		options.WriteString(fmt.Sprintf("%d. [%s] %s: %s\n", i+1, m.ID, m.Name, m.Description))
	}

	// 构建历史上下文
	historyStr := "无"
	if len(history) > 0 {
		recentHistory := history
		if len(history) > 3 {
			recentHistory = history[len(history)-3:]
		}
		historyStr = strings.Join(recentHistory, "\n")
	}

	prompt := fmt.Sprintf(`你是一个销售策略分析师。根据客户的消息，从以下综合策略系统中选择最匹配的一个。

## 可选策略系统
%s

## 对话历史
%s

## 客户当前消息
%s

## 输出要求
请严格按照以下JSON格式输出，不要包含其他内容：
{"meta_id": "选中的策略ID", "reason": "选择理由"}`, options.String(), historyStr, query)

	messages := []ChatMessage{
		{Role: "user", Content: prompt},
	}

	resp, err := r.dmxClient.ChatCompletion(ctx, "qwen-turbo-latest", messages, 0.1, 200)
	if err != nil {
		log.C(ctx).Warnw("Meta strategy selection LLM call failed", "error", err)
		// Fallback: 返回第一个策略
		return metas[0].ID, nil
	}

	// 解析JSON响应
	jsonStr := extractJSON(resp)
	var result struct {
		MetaID string `json:"meta_id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		log.C(ctx).Warnw("Failed to parse meta strategy JSON", "response", resp, "error", err)
		return metas[0].ID, nil
	}

	// 验证返回的ID是否有效
	for _, m := range metas {
		if m.ID == result.MetaID {
			log.C(ctx).Infow("Meta strategy selected", "meta_id", result.MetaID, "reason", result.Reason)
			return result.MetaID, nil
		}
	}

	// 如果ID无效，返回第一个
	log.C(ctx).Warnw("Invalid meta_id returned, using first", "returned", result.MetaID)
	return metas[0].ID, nil
}

// SelectBasicStrategy 从基础策略列表中选择最匹配的一个
func (r *StrategyRouter) SelectBasicStrategy(ctx context.Context, query string, history []string, basics []domain.BasicStrategy) (string, error) {
	if len(basics) == 0 {
		return "", fmt.Errorf("no basic strategies provided")
	}

	// 如果只有一个选项，直接返回
	if len(basics) == 1 {
		return basics[0].ID, nil
	}

	// 构建策略选项描述
	var options strings.Builder
	for i, b := range basics {
		options.WriteString(fmt.Sprintf("%d. [%s] %s: %s\n", i+1, b.ID, b.Name, b.Description))
	}

	// 构建历史上下文
	historyStr := "无"
	if len(history) > 0 {
		recentHistory := history
		if len(history) > 3 {
			recentHistory = history[len(history)-3:]
		}
		historyStr = strings.Join(recentHistory, "\n")
	}

	prompt := fmt.Sprintf(`你是一个销售策略分析师。根据客户的消息，从以下基础策略卡片中选择最匹配的一个。

## 可选策略卡片
%s

## 对话历史
%s

## 客户当前消息
%s

## 输出要求
请严格按照以下JSON格式输出，不要包含其他内容：
{"basic_id": "选中的策略ID", "reason": "选择理由"}`, options.String(), historyStr, query)

	messages := []ChatMessage{
		{Role: "user", Content: prompt},
	}

	resp, err := r.dmxClient.ChatCompletion(ctx, "qwen-turbo-latest", messages, 0.1, 200)
	if err != nil {
		log.C(ctx).Warnw("Basic strategy selection LLM call failed", "error", err)
		return basics[0].ID, nil
	}

	// 解析JSON响应
	jsonStr := extractJSON(resp)
	var result struct {
		BasicID string `json:"basic_id"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		log.C(ctx).Warnw("Failed to parse basic strategy JSON", "response", resp, "error", err)
		return basics[0].ID, nil
	}

	// 验证返回的ID是否有效
	for _, b := range basics {
		if b.ID == result.BasicID {
			log.C(ctx).Infow("Basic strategy selected", "basic_id", result.BasicID, "reason", result.Reason)
			return result.BasicID, nil
		}
	}

	// 如果ID无效，返回第一个
	log.C(ctx).Warnw("Invalid basic_id returned, using first", "returned", result.BasicID)
	return basics[0].ID, nil
}
