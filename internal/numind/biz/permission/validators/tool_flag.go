package validators

import (
	"context"
	"encoding/json"

	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
)

type ToolFlag struct {
	skillStore store.IAgentDefinitionStore
}

func NewToolFlag(s store.IAgentDefinitionStore) permission.Validator {
	return &ToolFlag{skillStore: s}
}

func (v *ToolFlag) ID() string { return "ToolFlag" }

func (v *ToolFlag) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if v.skillStore == nil || req.AgentDefinitionID == 0 {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no skillStore or definition")
	}
	ad, err := v.skillStore.GetByIDIncludeInactive(ctx, req.AgentDefinitionID)
	if err != nil {
		log.Warnw("ToolFlag.Validate: skill lookup failed; fail-open",
			"agent_definition_id", req.AgentDefinitionID, "error", err)
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "skill lookup error fail-open")
	}
	if len(ad.ToolFlags) == 0 {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no tool_flags configured")
	}
	var flags map[string]bool
	if err := json.Unmarshal(ad.ToolFlags, &flags); err != nil {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool_flags unmarshal error")
	}
	if req.Tool == nil {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no tool")
	}
	toolName := req.Tool.Name()
	enabled, present := flags[toolName]
	if !present {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool not in flags")
	}
	if !enabled {
		return permission.Deny(v.ID()+":"+toolName, permission.DecisionReasonRule,
			"该 Agent 暂未启用 "+toolName+" 功能")
	}
	return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool enabled")
}
