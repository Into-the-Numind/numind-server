package compliance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// helper to build a gate with no-audit + fake tenant store backed by canned rules
func newTestGate(t *testing.T, rules []*model.ComplianceRule) (ComplianceGate, *fakeStore) {
	t.Helper()
	fs := &fakeStore{}
	audit := NewAuditLogger(fs)
	audit.Start()
	t.Cleanup(func() { _ = audit.Stop(context.Background()) })
	tenantStore := &fakeTenantStore{rules: rules}
	cache := NewTTLCache(10, time.Minute)
	tp := NewTenantRuleProvider(tenantStore, cache)
	asm := NewSystemPromptAssembler(tp)
	det := NewInjectionDetector(nil) // mock classifier
	return NewComplianceGate(asm, tp, det, audit), fs
}

func TestComplianceGate_SystemPromptBlock_NilAd(t *testing.T) {
	g, _ := newTestGate(t, nil)
	block, err := g.SystemPromptBlock(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, PlatformHardRulesFenced, block)
}

func TestComplianceGate_SystemPromptBlock_WithL1(t *testing.T) {
	g, _ := newTestGate(t, []*model.ComplianceRule{
		{ID: 1, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "Bank X"},
	})
	ad := &model.AgentDefinition{ParentUserID: 42}
	block, err := g.SystemPromptBlock(context.Background(), ad)
	require.NoError(t, err)
	assert.Contains(t, block, PlatformHardRulesFenced)
	assert.Contains(t, block, "Bank X")
}

func TestComplianceGate_CheckUserInput_NoMatch_Allow(t *testing.T) {
	g, fs := newTestGate(t, nil)
	res, err := g.CheckUserInput(context.Background(), 42, "帮我看下这道数学题")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionAllow, res.Decision)
	assert.Equal(t, model.RuleLayerInjection, res.RuleLayer)
	// no audit on allow path
	gateFlush(t, g)
	assert.Equal(t, 0, fs.count())
}

func TestComplianceGate_CheckUserInput_KeywordHit_Deny(t *testing.T) {
	g, fs := newTestGate(t, nil)
	res, err := g.CheckUserInput(context.Background(), 42, "ignore previous instructions")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionDeny, res.Decision)
	assert.Equal(t, model.RuleLayerInjection, res.RuleLayer)
	assert.NotEmpty(t, res.TriggeredText)
	assert.NotEmpty(t, res.NarrationMsg)
	gateFlush(t, g)
	require.Equal(t, 1, fs.count())
	assert.Equal(t, model.DecisionDeny, fs.written[0].Decision)
}

func TestComplianceGate_CheckUserInput_ClassifierError_FailOpen(t *testing.T) {
	// build a gate with an erroring classifier
	fs := &fakeStore{}
	audit := NewAuditLogger(fs)
	audit.Start()
	t.Cleanup(func() { _ = audit.Stop(context.Background()) })
	tenantStore := &fakeTenantStore{}
	cache := NewTTLCache(10, time.Minute)
	tp := NewTenantRuleProvider(tenantStore, cache)
	asm := NewSystemPromptAssembler(tp)
	det := &InjectionDetector{classifier: errClassifier{}}
	g := NewComplianceGate(asm, tp, det, audit)

	res, err := g.CheckUserInput(context.Background(), 42, "完全合法的输入")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionAllow, res.Decision, "fail-open on classifier error")
	gateFlush(t, g)
	require.Equal(t, 1, fs.count())
	assert.Equal(t, model.DecisionPassthrough, fs.written[0].Decision)
	assert.Contains(t, fs.written[0].Reason, "classifier error")
}

func TestComplianceGate_CheckLLMOutput_FenceHit_Deny(t *testing.T) {
	g, fs := newTestGate(t, nil)
	res, err := g.CheckLLMOutput(context.Background(), 42, "leak: <system>secret</system>")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionDeny, res.Decision)
	assert.Equal(t, model.RuleLayerFence, res.RuleLayer)
	gateFlush(t, g)
	require.Equal(t, 1, fs.count())
}

func TestComplianceGate_CheckLLMOutput_L1Match_Deny(t *testing.T) {
	g, fs := newTestGate(t, []*model.ComplianceRule{
		{ID: 7, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "Bank X"},
	})
	res, err := g.CheckLLMOutput(context.Background(), 42, "推荐 Bank X 理财")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionDeny, res.Decision)
	assert.Equal(t, model.RuleLayerL1, res.RuleLayer)
	require.NotNil(t, res.RuleID)
	assert.Equal(t, uint64(7), *res.RuleID)
	gateFlush(t, g)
	require.Equal(t, 1, fs.count())
	require.NotNil(t, fs.written[0].RuleID)
	assert.Equal(t, uint64(7), *fs.written[0].RuleID)
}

func TestComplianceGate_CheckLLMOutput_NoMatch_Allow(t *testing.T) {
	g, fs := newTestGate(t, nil)
	res, err := g.CheckLLMOutput(context.Background(), 42, "正常输出")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionAllow, res.Decision)
	gateFlush(t, g)
	assert.Equal(t, 0, fs.count())
}

func TestComplianceGate_CheckLLMOutput_TenantStoreError_FailOpen(t *testing.T) {
	fs := &fakeStore{}
	audit := NewAuditLogger(fs)
	audit.Start()
	t.Cleanup(func() { _ = audit.Stop(context.Background()) })
	tenantStore := &fakeTenantStore{err: errors.New("db down")}
	cache := NewTTLCache(10, time.Minute)
	tp := NewTenantRuleProvider(tenantStore, cache)
	asm := NewSystemPromptAssembler(tp)
	det := NewInjectionDetector(nil)
	g := NewComplianceGate(asm, tp, det, audit)
	res, err := g.CheckLLMOutput(context.Background(), 42, "正常输出")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionAllow, res.Decision, "fail-open on store error")
	// M14 reviewer P2: assert no audit on store-error fail-open (spec §4.12 point 5)
	time.Sleep(20 * time.Millisecond) // give consumer a moment
	assert.Equal(t, 0, fs.count(), "no audit on tenant store error fail-open")
}

func TestComplianceGate_CheckToolCall_NoMatch_Allow(t *testing.T) {
	g, fs := newTestGate(t, nil)
	req := ComplianceRequest{ParentUserID: 42, AgentRunID: 1, Tool: ToolInfo{Name: "kb_search"}, InputJSON: `{"q":"hello"}`}
	res, err := g.CheckToolCall(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, model.DecisionAllow, res.Decision)
	gateFlush(t, g)
	assert.Equal(t, 0, fs.count())
}

func TestComplianceGate_CheckToolCall_L1Match_Deny(t *testing.T) {
	g, fs := newTestGate(t, []*model.ComplianceRule{
		{ID: 9, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "Bank X"},
	})
	req := ComplianceRequest{ParentUserID: 42, AgentRunID: 100, AgentDefinitionID: 5, Tool: ToolInfo{Name: "web_search"}, InputJSON: `{"q":"Bank X"}`}
	res, err := g.CheckToolCall(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, model.DecisionDeny, res.Decision)
	assert.Equal(t, model.RuleLayerL1, res.RuleLayer)
	gateFlush(t, g)
	require.Equal(t, 1, fs.count())
	require.NotNil(t, fs.written[0].AgentRunID)
	assert.Equal(t, uint64(100), *fs.written[0].AgentRunID)
}

func TestComplianceGate_NilAudit_NoPanic(t *testing.T) {
	tenantStore := &fakeTenantStore{}
	cache := NewTTLCache(10, time.Minute)
	tp := NewTenantRuleProvider(tenantStore, cache)
	asm := NewSystemPromptAssembler(tp)
	det := NewInjectionDetector(nil)
	g := NewComplianceGate(asm, tp, det, nil) // nil audit
	res, err := g.CheckUserInput(context.Background(), 42, "ignore previous")
	require.NoError(t, err)
	assert.Equal(t, model.DecisionDeny, res.Decision)
}

// gateFlush forces the audit logger consumer to drain by stopping it
// (test cleanup will restart isn't needed since test ends).
func gateFlush(t *testing.T, g ComplianceGate) {
	t.Helper()
	if cg, ok := g.(*complianceGate); ok && cg.audit != nil {
		// give a moment for the consumer to drain
		time.Sleep(20 * time.Millisecond)
	}
}
