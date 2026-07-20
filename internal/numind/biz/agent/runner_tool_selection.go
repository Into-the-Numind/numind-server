package agent

import "encoding/json"

// selectToolsForRun is the single tool-registration policy shared by Run and
// RunStream. Existing category-only Agent definitions keep the historical
// full-open behavior. Definitions that contain any direct tool flag opt into a
// strict server-side allowlist, so explicit false values cannot be bypassed by
// the model or by prompt injection.
func selectToolsForRun(registry AgentToolRegistry, allowedNames []string, enforceAllowlist bool) []FullTool {
	if registry == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = struct{}{}
	}
	fullConfig := FullyEnabledToolConfig()
	selected := make([]FullTool, 0)
	for _, tool := range registry.ListAllTools() {
		if !tool.IsEnabled(fullConfig) {
			continue
		}
		if enforceAllowlist {
			if _, ok := allowed[tool.Name()]; !ok {
				continue
			}
		}
		selected = append(selected, tool)
	}
	return selected
}

// enforceExplicitToolAllowlist distinguishes legacy category-only definitions
// from definitions that intentionally enumerate individual tool names. The
// latter are security policy, not UI hints, and must be enforced by the runner.
func enforceExplicitToolAllowlist(toolFlagsJSON []byte) bool {
	if len(toolFlagsJSON) == 0 {
		return false
	}
	var flags map[string]bool
	if err := json.Unmarshal(toolFlagsJSON, &flags); err != nil {
		return false
	}
	for key := range flags {
		if !isKnownToolCategoryFlag(key) {
			return true
		}
	}
	return false
}

func isKnownToolCategoryFlag(name string) bool {
	switch name {
	case "code_sandbox", "media", "dangerous", "enable_skills":
		return true
	default:
		return false
	}
}
