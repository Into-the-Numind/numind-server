package validators

import (
	"context"

	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/numind/store"
)

type ToolFlag struct {
	skillStore store.IAgentDefinitionStore
}

func NewToolFlag(s store.IAgentDefinitionStore) permission.Validator {
	return &ToolFlag{skillStore: s}
}

func (v *ToolFlag) ID() string { return "ToolFlag" }

// Validate intentionally ignores AgentDefinition.ToolFlags. The field remains
// for storage/API compatibility, while every usable registered tool is available
// to every Agent. Other validators and tool-local checks still enforce tenant
// isolation, external authorization, destructive confirmations, and sandbox rules.
func (v *ToolFlag) Validate(_ context.Context, _ permission.PermissionRequest) permission.PermissionResult {
	return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "agent tool flags are compatibility metadata")
}
