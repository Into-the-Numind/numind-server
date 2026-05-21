package compliance

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/pkg/model"
)

// SystemPromptAssembler — runner.go step [2] tenantHardRulesPlaceholder 装配器
type SystemPromptAssembler struct {
	tenantProvider *TenantRuleProvider
}

// NewSystemPromptAssembler constructs an assembler with the given tenant provider.
// tenantProvider may be nil — in that case, only L0 is injected (L1 skipped).
func NewSystemPromptAssembler(tp *TenantRuleProvider) *SystemPromptAssembler {
	return &SystemPromptAssembler{tenantProvider: tp}
}

// Assemble 拼装 L0 + L1 段位（L2 不在此处注入；Q10/Q11 已在 skill body 中）
// ad nil → 仅注入 L0 platform rules
// tenantProvider nil 或 GetActiveRules error → fail-open，L0 仍注入
func (a *SystemPromptAssembler) Assemble(ctx context.Context, ad *model.AgentDefinition) (string, error) {
	var sb strings.Builder
	sb.WriteString(PlatformHardRulesFenced) // L0 always injected
	if ad == nil || a.tenantProvider == nil {
		return sb.String(), nil
	}
	rules, err := a.tenantProvider.GetActiveRules(ctx, ad.ParentUserID)
	if err != nil {
		return sb.String(), fmt.Errorf("SystemPromptAssembler.Assemble L1 fetch: %w", err)
	}
	sb.WriteString(a.tenantProvider.RenderFenced(ad.ParentUserID, rules)) // L1
	return sb.String(), nil
}
