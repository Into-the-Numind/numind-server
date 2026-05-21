package compliance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

func TestSystemPromptAssembler_NilAgentDef_OnlyL0(t *testing.T) {
	a := NewSystemPromptAssembler(nil)
	block, err := a.Assemble(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, PlatformHardRulesFenced, block)
}

func TestSystemPromptAssembler_WithL1Rules(t *testing.T) {
	fs := &fakeTenantStore{rules: []*model.ComplianceRule{
		{ID: 1, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "Bank X", Priority: 100},
	}}
	tp := NewTenantRuleProvider(fs, NewTTLCache(10, time.Minute))
	a := NewSystemPromptAssembler(tp)
	ad := &model.AgentDefinition{ParentUserID: 42}
	block, err := a.Assemble(context.Background(), ad)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(block, PlatformHardRulesFenced), "L0 must come first")
	assert.Contains(t, block, "<tenant_hard_rules parent_id=\"42\">", "L1 tag must include parent_id")
	assert.Contains(t, block, "Bank X")
}

func TestSystemPromptAssembler_TenantStoreError_FailOpen(t *testing.T) {
	fs := &fakeTenantStore{err: errors.New("db down")}
	tp := NewTenantRuleProvider(fs, NewTTLCache(10, time.Minute))
	a := NewSystemPromptAssembler(tp)
	ad := &model.AgentDefinition{ParentUserID: 42}
	block, err := a.Assemble(context.Background(), ad)
	require.Error(t, err, "L1 fetch error should be returned for caller to log")
	assert.Equal(t, PlatformHardRulesFenced, block, "L0 still injected on L1 error (fail-open)")
}

func TestSystemPromptAssembler_NilTenantProvider_OnlyL0(t *testing.T) {
	a := NewSystemPromptAssembler(nil)
	ad := &model.AgentDefinition{ParentUserID: 42}
	block, err := a.Assemble(context.Background(), ad)
	require.NoError(t, err)
	assert.Equal(t, PlatformHardRulesFenced, block)
}
