package validators

import (
	"context"
	"encoding/json"
	"strings"

	"numind-server/internal/numind/biz/agent/bashvalidator"
	"numind-server/internal/numind/biz/permission"
)

type PlatformHardRule struct{}

func NewPlatformHardRule() permission.Validator { return &PlatformHardRule{} }

func (v *PlatformHardRule) ID() string { return "PlatformHardRule" }

func (v *PlatformHardRule) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if req.Tool == nil || req.Tool.Name() != "bash_exec" {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "not bash_exec")
	}
	cmd := extractBashCommand(req.InputJSON)
	if cmd == "" {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no command field")
	}
	allow, reason := bashvalidator.Validate(cmd)
	if !allow {
		return permission.Deny(v.ID()+":"+firstColonField(reason), permission.DecisionReasonRule, reason)
	}
	return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "bash command allowed by hard rules")
}

func extractBashCommand(input string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		return ""
	}
	s, _ := m["command"].(string)
	return s
}

func firstColonField(s string) string {
	parts := strings.SplitN(s, ":", 2)
	return parts[0]
}
