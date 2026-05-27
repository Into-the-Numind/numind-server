package agent

// BuildSystemPromptLegacy 旧拼装路径，沿用 runner.go:676-687 的字面顺序。
// 当 ad.SystemPrompt == "" 时调用，老 agent 行为完全一致。
// 入参完整保留各 placeholder，调用者负责按现有逻辑预先组装。
func BuildSystemPromptLegacy(
	platformBase string,
	tenantHardRules string,
	body string,
	memoriesHeader string,
	agentMd string,
	selector string,
	dialectic string,
	temporal string,
	memoryDisclaimer string,
	memorySystem string,
	toolsSection string,
	platformFooter string,
) string {
	return platformBase +
		tenantHardRules +
		body +
		memoriesHeader +
		agentMd +
		selector +
		dialectic +
		temporal +
		memoryDisclaimer +
		memorySystem +
		toolsSection +
		platformFooter
}
