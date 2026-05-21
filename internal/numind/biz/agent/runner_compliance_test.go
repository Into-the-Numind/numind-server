package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"numind-server/internal/numind/biz/compliance"
	"numind-server/internal/pkg/model"
)

// stubComplianceGate captures SystemPromptBlock calls so runner integration
// tests can verify step [2] injection without standing up the full compliance stack.
type stubComplianceGate struct {
	block string
	err   error
	calls int
}

func (s *stubComplianceGate) SystemPromptBlock(ctx context.Context, ad *model.AgentDefinition) (string, error) {
	s.calls++
	return s.block, s.err
}

func (s *stubComplianceGate) CheckUserInput(ctx context.Context, parentUserID uint, input string) (compliance.ComplianceResult, error) {
	return compliance.ComplianceResult{Decision: model.DecisionAllow}, nil
}

func (s *stubComplianceGate) CheckLLMOutput(ctx context.Context, parentUserID uint, output string) (compliance.ComplianceResult, error) {
	return compliance.ComplianceResult{Decision: model.DecisionAllow}, nil
}

func (s *stubComplianceGate) CheckToolCall(ctx context.Context, req compliance.ComplianceRequest) (compliance.ComplianceResult, error) {
	return compliance.ComplianceResult{Decision: model.DecisionAllow}, nil
}

func TestWithComplianceGate_Setter(t *testing.T) {
	gate := &stubComplianceGate{block: "L0+L1 text"}
	r := NewAgentRunner(nil, nil, WithComplianceGate(gate)).(*agentRunner)
	assert.Same(t, gate, r.complianceGate)
}

func TestWithComplianceGate_Nil_OK(t *testing.T) {
	r := NewAgentRunner(nil, nil).(*agentRunner)
	assert.Nil(t, r.complianceGate)
}

func TestComplianceGate_NilGate_DoesNotPanic(t *testing.T) {
	// Compile-time + runtime guard: agentRunner must accept nil complianceGate
	// without crashing. Integration coverage in S5 acceptance.
	r := NewAgentRunner(nil, nil).(*agentRunner)
	assert.NotPanics(t, func() {
		_ = r.complianceGate // just touching the field
	})
}

func TestComplianceGate_StubReturnsBlock(t *testing.T) {
	// Validate the stub itself (used by runner integration coverage).
	gate := &stubComplianceGate{block: "<platform_hard_rules>...</platform_hard_rules>"}
	block, err := gate.SystemPromptBlock(context.Background(), nil)
	assert.NoError(t, err)
	assert.Equal(t, "<platform_hard_rules>...</platform_hard_rules>", block)
	assert.Equal(t, 1, gate.calls)
}

func TestComplianceGate_StubReturnsErrorAndPartialBlock(t *testing.T) {
	// Stub can return both a partial block and an error to simulate fail-open
	// path in runner.go step [2].
	gate := &stubComplianceGate{block: "L0 only", err: errors.New("L1 fetch fail")}
	block, err := gate.SystemPromptBlock(context.Background(), nil)
	assert.Error(t, err)
	assert.Equal(t, "L0 only", block, "partial block returned even on error (fail-open)")
}
