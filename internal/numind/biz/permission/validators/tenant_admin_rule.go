package validators

import (
	"context"
	"regexp"
	"strings"

	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// TenantAdminRule reads agent_permission_config table for L2 tenant admin rules.
// v1 不缓存 regex（每次 Validate 重 compile）；优化项后续。
type TenantAdminRule struct {
	store store.IAgentPermissionStore
}

func NewTenantAdminRule(s store.IAgentPermissionStore) permission.Validator {
	return &TenantAdminRule{store: s}
}

func (v *TenantAdminRule) ID() string { return "TenantAdminRule" }

func (v *TenantAdminRule) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if v.store == nil || req.ParentUserID == 0 {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no store or parent")
	}
	rules, err := v.store.ListActiveByParent(ctx, req.ParentUserID)
	if err != nil {
		log.Warnw("TenantAdminRule.Validate: ListActiveByParent failed; fail-open",
			"parent_user_id", req.ParentUserID,
			"error", err)
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "store error fail-open")
	}
	for _, rule := range rules {
		if !ruleMatches(rule, req) {
			continue
		}
		validatorID := v.ID() + ":" + rule.RuleType
		action := rule.Action
		if action == "" {
			action = permission.BehaviorDeny
		}
		message := rule.Message
		if message == "" {
			message = "本规则不允许该操作"
		}
		switch action {
		case permission.BehaviorAsk:
			return permission.Ask(validatorID, permission.DecisionReasonRule, message)
		default:
			return permission.Deny(validatorID, permission.DecisionReasonRule, message)
		}
	}
	return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no rule matched")
}

func ruleMatches(rule model.AgentPermissionConfig, req permission.PermissionRequest) bool {
	if req.Tool == nil {
		return false
	}
	switch rule.RuleType {
	case "tool_blacklist":
		return rule.RuleKey == req.Tool.Name()
	case "tool_input_regex_deny":
		if rule.RuleKey != req.Tool.Name() {
			return false
		}
		re, err := regexp.Compile(rule.RuleValue)
		if err != nil {
			return false
		}
		return re.MatchString(req.InputJSON)
	case "topic_blacklist":
		return strings.Contains(req.InputJSON, rule.RuleKey)
	default:
		return false
	}
}
