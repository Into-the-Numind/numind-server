package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// buildStrategySelectFragments constructs the ContextFragment slice for a
// strategy-selection LLM call (operation=salesrag_strategy_select).
//
// P2-2 fix (spec §9.2 system/user separation): the full prompt string encodes
// both a system-level instruction block (role definition + strategy list +
// output rules) and the user-supplied query. We split on the last occurrence of
// "## 客户当前消息\n" so the instruction skeleton becomes a RoleImmutable system
// fragment and the actual user query becomes a separate RoleRecent user fragment.
//
// Fallback: when the separator is not found (e.g. prompt template changed),
// the whole prompt is kept as a single RoleImmutable system fragment with a
// synthetic empty user fragment, preserving prior behaviour without panicking.
func buildStrategySelectFragments(prompt string) []cb.ContextFragment {
	const userQuerySeparator = "## 客户当前消息\n"
	sepIdx := strings.LastIndex(prompt, userQuerySeparator)
	if sepIdx < 0 {
		// Fallback: treat entire prompt as system instruction.
		return []cb.ContextFragment{
			{
				ID:              "strategy-sys",
				Role:            cb.RoleImmutable,
				Source:          cb.SourceSystem,
				ContentType:     cb.ContentText,
				Content:         prompt,
				Importance:      10,
				Order:           0,
				Compressibility: cb.CompressNone,
				Critical:        true,
			},
		}
	}
	sysContent := strings.TrimRight(prompt[:sepIdx], "\n ")
	// Skip the separator line itself and the "## 输出要求" block that follows
	// the user query to isolate just the user's current message.
	afterSep := prompt[sepIdx+len(userQuerySeparator):]
	// The user query ends at the next "## " heading (if any).
	if nextSection := strings.Index(afterSep, "\n## "); nextSection >= 0 {
		afterSep = afterSep[:nextSection]
	}
	userContent := strings.TrimSpace(afterSep)

	return []cb.ContextFragment{
		{
			ID:              "strategy-sys",
			Role:            cb.RoleImmutable,
			Source:          cb.SourceSystem,
			ContentType:     cb.ContentText,
			Content:         sysContent,
			Importance:      10,
			Order:           0,
			Compressibility: cb.CompressNone,
			Critical:        true,
		},
		{
			ID:              "strategy-user",
			Role:            cb.RoleRecent,
			Source:          cb.SourceUser,
			ContentType:     cb.ContentText,
			Content:         userContent,
			Importance:      9,
			Order:           1,
			Compressibility: cb.CompressNone,
			Critical:        true,
		},
	}
}

// StrategyRouter 基于LLM的策略路由器
// 使用 profile.SalesragIntent 进行策略选择（意图/策略分析，语义最接近）
type StrategyRouter struct{}

// NewStrategyRouter 创建新的策略路由器
func NewStrategyRouter() *StrategyRouter {
	return &StrategyRouter{}
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
		if len(history) > 4 {
			recentHistory = history[len(history)-4:]
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
**必须且只能选择 1 个最匹配的策略ID**。
请严格按照以下JSON格式输出，不要包含其他内容：
{"meta_id": "选中的策略ID", "reason": "选择理由"}`, options.String(), historyStr, query)

	// 注入计费上下文
	if uid, ok := middleware.UserIDFromCtx(ctx); ok && uid > 0 {
		ctx = billing.WithBilling(ctx, uid, "salesrag_strategy_select")
		ctx = aismw.WithUserID(ctx, uid)
	}
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	aiMessages := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Text: prompt},
		},
	}
	resp, err := aiservice.Chat(ctx, profile.SalesragIntent, aiservice.ChatRequest{
		Messages:         aiMessages,
		ContextFragments: buildStrategySelectFragments(prompt),
		Temperature:      0.1,
		MaxTokens:        200,
	})
	if err != nil {
		log.C(ctx).Warnw("Meta strategy selection LLM call failed", "error", err)
		// Fallback: 返回第一个策略
		return metas[0].ID, nil
	}

	// 解析JSON响应
	jsonStr := extractJSON(resp.Content)
	var result struct {
		MetaID string `json:"meta_id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		log.C(ctx).Warnw("Failed to parse meta strategy JSON", "response", resp.Content, "error", err)
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

// SelectBasicStrategy 根据综合策略的决策树逻辑，选择最匹配的基础策略
func (r *StrategyRouter) SelectBasicStrategy(ctx context.Context, query string, history []string, decisionTree string, basics []domain.BasicStrategy) (string, error) {
	if len(basics) == 0 {
		return "", fmt.Errorf("no basic strategies provided")
	}

	// 如果只有一个选项，直接返回
	if len(basics) == 1 {
		return basics[0].ID, nil
	}

	// 构建备选列表（仅用于给LLM做ID参考）
	var options strings.Builder
	for i, b := range basics {
		options.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, b.ID, b.Name))
	}

	// 构建历史上下文
	historyStr := "无"
	if len(history) > 0 {
		recentHistory := history
		if len(history) > 4 {
			recentHistory = history[len(history)-4:]
		}
		historyStr = strings.Join(recentHistory, "\n")
	}

	// 使用决策树逻辑专用 Prompt
	prompt := fmt.Sprintf(`你是一个严格执行逻辑的销售决策引擎。请阅读以下【决策树逻辑】，根据客户的最新消息，判断应路由到哪个基础策略卡片。

## 核心策略决策树 (逻辑准则)
%s

## 可选基础策略 ID 列表
%s

## 对话历史
%s

## 客户当前消息
%s

## 输出要求
请严格基于决策树逻辑进行判断。**必须且只能选择 1 个最匹配的策略ID**。
只输出 JSON：
{"basic_id": "选中的策略ID", "reason": "基于决策树的判断依据"}`, decisionTree, options.String(), historyStr, query)

	// 注入计费上下文
	if uid, ok := middleware.UserIDFromCtx(ctx); ok && uid > 0 {
		ctx = billing.WithBilling(ctx, uid, "salesrag_strategy_select")
		ctx = aismw.WithUserID(ctx, uid)
	}
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	aiMessages := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Text: prompt},
		},
	}
	resp, err := aiservice.Chat(ctx, profile.SalesragIntent, aiservice.ChatRequest{
		Messages:         aiMessages,
		ContextFragments: buildStrategySelectFragments(prompt),
		Temperature:      0.1,
		MaxTokens:        200,
	})
	if err != nil {
		log.C(ctx).Warnw("Basic strategy selection LLM call failed", "error", err)
		return basics[0].ID, nil
	}

	// 解析JSON响应
	jsonStr := extractJSON(resp.Content)
	var result struct {
		BasicID string `json:"basic_id"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		log.C(ctx).Warnw("Failed to parse basic strategy JSON", "response", resp.Content, "error", err)
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
