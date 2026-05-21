package compliance

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// TenantRuleProvider — L1 父账户规则提供者（store + cache 组合）
type TenantRuleProvider struct {
	store store.IComplianceStore
	cache *TTLCache
}

func NewTenantRuleProvider(s store.IComplianceStore, c *TTLCache) *TenantRuleProvider {
	return &TenantRuleProvider{store: s, cache: c}
}

// GetActiveRules 返回 parent 当前生效的规则（优先级排序后）
func (p *TenantRuleProvider) GetActiveRules(ctx context.Context, parentUserID uint) ([]*model.ComplianceRule, error) {
	if cached, ok := p.cache.Get(parentUserID); ok {
		return cached, nil
	}
	rules, err := p.store.ListRulesByParent(ctx, parentUserID, true)
	if err != nil {
		return nil, fmt.Errorf("TenantRuleProvider.GetActiveRules: %w", err)
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].CreatedAt.After(rules[j].CreatedAt)
	})
	p.cache.Set(parentUserID, rules)
	return rules, nil
}

// RenderFenced 把规则列表渲染为 fence-tag 段（注入 system prompt 用）
func (p *TenantRuleProvider) RenderFenced(parentUserID uint, rules []*model.ComplianceRule) string {
	if len(rules) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<tenant_hard_rules parent_id=\"%d\">\n", parentUserID))
	for _, r := range rules {
		switch r.RuleType {
		case model.ComplianceRuleTypeForbidTopic:
			sb.WriteString(fmt.Sprintf("- 禁讨论话题：%s\n", r.RuleText))
		case model.ComplianceRuleTypeForbidBrand:
			sb.WriteString(fmt.Sprintf("- 禁讨论品牌：%s\n", r.RuleText))
		case model.ComplianceRuleTypeForbidPhrase:
			sb.WriteString(fmt.Sprintf("- 禁出现短语：%s\n", r.RuleText))
		case model.ComplianceRuleTypeCustom:
			sb.WriteString(fmt.Sprintf("- 自定义规则：%s\n", r.RuleText))
		}
	}
	sb.WriteString("</tenant_hard_rules>\n")
	return sb.String()
}

// MatchOutput 检查 LLM 输出是否命中任一启用规则（仅 forbid_brand / forbid_phrase 精确匹配）
// forbid_topic / custom 走 LLM 分类器 v2 兜底，v1 只关键词
func (p *TenantRuleProvider) MatchOutput(rules []*model.ComplianceRule, output string) (*model.ComplianceRule, string) {
	lower := strings.ToLower(output)
	for _, r := range rules {
		switch r.RuleType {
		case model.ComplianceRuleTypeForbidBrand, model.ComplianceRuleTypeForbidPhrase:
			needle := strings.ToLower(r.RuleText)
			if strings.Contains(lower, needle) {
				return r, r.RuleText
			}
		}
	}
	return nil, ""
}
