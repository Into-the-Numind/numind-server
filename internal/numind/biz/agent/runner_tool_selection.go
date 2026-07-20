package agent

import "numind-server/internal/pkg/model"

// applyDefinitionToolPolicy normalizes legacy request fields to the platform-wide
// tool policy. AgentDefinition.ToolFlags remains persisted for API compatibility,
// but it is no longer an authorization boundary.
func applyDefinitionToolPolicy(req *RunRequest, ad *model.AgentDefinition) {
	if req == nil || ad == nil {
		return
	}
	req.EnforceToolAllowlist = false
}

// selectToolsForRun is the single tool-registration policy shared by Run and
// RunStream. The legacy allowlist arguments remain for rolling compatibility,
// but every Agent receives every registry tool enabled by the full platform
// config. Hard-disabled tools opt out through IsEnabled.
func selectToolsForRun(registry AgentToolRegistry, _ []string, _ bool) []FullTool {
	if registry == nil {
		return nil
	}
	fullConfig := FullyEnabledToolConfig()
	selected := make([]FullTool, 0)
	for _, tool := range registry.ListAllTools() {
		if !tool.IsEnabled(fullConfig) {
			continue
		}
		selected = append(selected, tool)
	}
	return selected
}

// enforceExplicitToolAllowlist is retained until all lifecycle callers drop the
// legacy field. No ToolFlags JSON shape can restrict the platform tool set.
func enforceExplicitToolAllowlist(_ []byte) bool {
	return false
}
