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

// toolGatingCategories maps a restricted tool to the risk categories that gate it.
// Mirrors agent.categoryToTools for the categories the AgentAdvancedEdit UI exposes
// (code_sandbox / media / dangerous). A tool is denied when any of its gating
// categories is explicitly set false — so the parent's UI toggle actually takes
// effect. Baseline tools (kb_search, web_search, file_read, …) have no entry and are
// never category-gated. run_python/load_skill are intentionally NOT gated (they are
// the always-on skill executor path promised by OutputToolsPriorityAddendum).
var toolGatingCategories = map[string][]string{
	// image_gen removed (2026-06-17): 文生图是常用功能、不再当开关，永远可调用；
	// 改用每用户并发上限（agent.imageGenMaxConcurrentPerUser=6）控制，不在权限层门控。
	"bash_exec": {"code_sandbox", "dangerous"},
}

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

	// 1. Category gate — an explicitly-disabled risk category denies the tools it
	//    gates. This is checked FIRST so a category toggle wins over a stale per-tool
	//    key from legacy data (e.g. {"bash_exec": true, "code_sandbox": false}); the
	//    UI toggle always takes effect regardless of whether PATCH merged or replaced.
	for _, cat := range toolGatingCategories[toolName] {
		if catEnabled, present := flags[cat]; present && !catEnabled {
			return permission.Deny(v.ID()+":"+toolName, permission.DecisionReasonRule,
				"该 Agent 暂未启用 "+toolName+" 功能")
		}
	}

	// 2. Per-tool override — honors a direct tool-name key (legacy raw-name configs
	//    and future per-tool toggles). Absent → not gated → passthrough (full-open).
	enabled, present := flags[toolName]
	if !present {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool not gated")
	}
	if !enabled {
		return permission.Deny(v.ID()+":"+toolName, permission.DecisionReasonRule,
			"该 Agent 暂未启用 "+toolName+" 功能")
	}
	return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool enabled")
}
