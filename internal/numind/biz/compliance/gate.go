package compliance

import (
	"context"

	"numind-server/internal/pkg/model"
)

// complianceGate — ComplianceGate 默认实现，三层组合
type complianceGate struct {
	assembler *SystemPromptAssembler
	tenant    *TenantRuleProvider
	injection *InjectionDetector
	audit     *AuditLogger
}

// NewComplianceGate constructs the default 3-layer compliance gate.
// audit may be nil (no audit), but assembler/tenant/injection are required.
func NewComplianceGate(a *SystemPromptAssembler, t *TenantRuleProvider, i *InjectionDetector, audit *AuditLogger) ComplianceGate {
	return &complianceGate{
		assembler: a, tenant: t, injection: i, audit: audit,
	}
}

func (g *complianceGate) SystemPromptBlock(ctx context.Context, ad *model.AgentDefinition) (string, error) {
	return g.assembler.Assemble(ctx, ad)
}

func (g *complianceGate) CheckUserInput(ctx context.Context, parentUserID uint, input string) (ComplianceResult, error) {
	hit, kw, err := g.injection.Detect(ctx, input)
	if err != nil {
		// fail-open + audit + 不阻断
		g.writeAudit(&model.ComplianceAuditLog{
			ParentUserID: parentUserID,
			RuleLayer:    model.RuleLayerInjection,
			Decision:     model.DecisionPassthrough,
			Reason:       "classifier error: " + err.Error(),
		})
		return ComplianceResult{Decision: model.DecisionAllow, RuleLayer: model.RuleLayerInjection}, nil
	}
	if !hit {
		return ComplianceResult{Decision: model.DecisionAllow, RuleLayer: model.RuleLayerInjection}, nil
	}
	truncated := truncate(input, 500)
	g.writeAudit(&model.ComplianceAuditLog{
		ParentUserID:  parentUserID,
		RuleLayer:     model.RuleLayerInjection,
		Decision:      model.DecisionDeny,
		TriggeredText: truncated,
		Reason:        "keyword: " + kw,
	})
	return ComplianceResult{
		Decision:      model.DecisionDeny,
		RuleLayer:     model.RuleLayerInjection,
		Reason:        "keyword: " + kw,
		TriggeredText: truncated,
		NarrationMsg:  "检测到不安全的输入内容，无法处理。请重新上传或描述你的问题。",
	}, nil
}

func (g *complianceGate) CheckLLMOutput(ctx context.Context, parentUserID uint, output string) (ComplianceResult, error) {
	// 1. fence tag 检测
	if hit, fence := ValidateOutput(output); hit {
		truncated := truncate(output, 500)
		g.writeAudit(&model.ComplianceAuditLog{
			ParentUserID:  parentUserID,
			RuleLayer:     model.RuleLayerFence,
			Decision:      model.DecisionDeny,
			TriggeredText: truncated,
			Reason:        "forbidden fence: " + fence,
		})
		return ComplianceResult{
			Decision:      model.DecisionDeny,
			RuleLayer:     model.RuleLayerFence,
			Reason:        "forbidden fence: " + fence,
			TriggeredText: truncated,
			NarrationMsg:  "系统内部错误，请重试",
		}, nil
	}
	// 2. L1 rules 输出匹配（forbid_brand / forbid_phrase）
	rules, err := g.tenant.GetActiveRules(ctx, parentUserID)
	if err != nil {
		// fail-open：仍允许输出
		return ComplianceResult{Decision: model.DecisionAllow, RuleLayer: model.RuleLayerL1}, nil
	}
	if rule, matched := g.tenant.MatchOutput(rules, output); rule != nil {
		truncated := truncate(matched, 500)
		ruleID := rule.ID
		g.writeAudit(&model.ComplianceAuditLog{
			ParentUserID:  parentUserID,
			RuleLayer:     model.RuleLayerL1,
			RuleID:        &ruleID,
			Decision:      model.DecisionDeny,
			TriggeredText: truncated,
			Reason:        "L1 " + rule.RuleType + ": " + rule.RuleText,
		})
		return ComplianceResult{
			Decision:      model.DecisionDeny,
			RuleLayer:     model.RuleLayerL1,
			RuleID:        &ruleID,
			Reason:        "L1 " + rule.RuleType,
			TriggeredText: truncated,
			NarrationMsg:  DefaultOutOfScopeNarration,
		}, nil
	}
	// 3. v1 mock LLM classifier (qwen-turbo)：永远 PASS
	return ComplianceResult{Decision: model.DecisionAllow, RuleLayer: model.RuleLayerL0}, nil
}

func (g *complianceGate) CheckToolCall(ctx context.Context, req ComplianceRequest) (ComplianceResult, error) {
	// v1：仅检查工具参数中的 L1 forbid_brand / forbid_phrase
	rules, err := g.tenant.GetActiveRules(ctx, req.ParentUserID)
	if err != nil {
		return ComplianceResult{Decision: model.DecisionAllow, RuleLayer: model.RuleLayerL1}, nil
	}
	if rule, matched := g.tenant.MatchOutput(rules, req.InputJSON); rule != nil {
		truncated := truncate(matched, 500)
		ruleID := rule.ID
		runID := req.AgentRunID
		defID := req.AgentDefinitionID
		g.writeAudit(&model.ComplianceAuditLog{
			AgentRunID:        &runID,
			ParentUserID:      req.ParentUserID,
			AgentDefinitionID: &defID,
			RuleLayer:         model.RuleLayerL1,
			RuleID:            &ruleID,
			Decision:          model.DecisionDeny,
			TriggeredText:     truncated,
			Reason:            "L1 " + rule.RuleType + " in tool args: " + rule.RuleText,
		})
		return ComplianceResult{
			Decision:      model.DecisionDeny,
			RuleLayer:     model.RuleLayerL1,
			RuleID:        &ruleID,
			Reason:        "L1 in tool args: " + rule.RuleType,
			TriggeredText: truncated,
			NarrationMsg:  DefaultOutOfScopeNarration,
		}, nil
	}
	return ComplianceResult{Decision: model.DecisionAllow, RuleLayer: model.RuleLayerL1}, nil
}

// writeAudit is a small helper that no-ops on nil audit logger.
func (g *complianceGate) writeAudit(entry *model.ComplianceAuditLog) {
	if g.audit == nil {
		return
	}
	g.audit.Write(entry)
}
