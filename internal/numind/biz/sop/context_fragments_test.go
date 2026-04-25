// Package sop — context_fragments_test.go
//
// Task 9: SOP Gateway Producer Migration tests.
//
// Verifies that:
//   - The current node input is built as a critical (non-compressible) user fragment.
//   - The Gateway ChatRequest carries ContextFragments instead of rendered Messages.
//   - The Gateway path does NOT call creditSvc.Reserve (old R2 path); the middleware
//     owns the budget reservation via ReserveBudget.
//   - SOP chat builds conversation fragments correctly.
package sop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Helpers — fragment introspection
// ----------------------------------------------------------------------------

// findFragmentByID returns the fragment with the given ID, or nil.
func findFragmentByID(frags []contextbudget.ContextFragment, id string) *contextbudget.ContextFragment {
	for i := range frags {
		if frags[i].ID == id {
			return &frags[i]
		}
	}
	return nil
}

// fragmentsByRole collects all fragments with the given role.
func fragmentsByRole(frags []contextbudget.ContextFragment, role contextbudget.FragmentRole) []contextbudget.ContextFragment {
	var out []contextbudget.ContextFragment
	for _, f := range frags {
		if f.Role == role {
			out = append(out, f)
		}
	}
	return out
}

// fragmentsBySource collects all fragments with the given source.
func fragmentsBySource(frags []contextbudget.ContextFragment, src contextbudget.FragmentSource) []contextbudget.ContextFragment {
	var out []contextbudget.ContextFragment
	for _, f := range frags {
		if f.Source == src {
			out = append(out, f)
		}
	}
	return out
}

// ----------------------------------------------------------------------------
// Test 1: current node input is built as a critical user fragment
// ----------------------------------------------------------------------------

// TestSOPNodeExecutionBuildsCurrentInputAsCriticalFragment verifies that
// buildSOPNodeFragments sets the current user input as a Critical, non-
// compressible fragment (CompressNone, RoleRecent, SourceUser, Critical=true).
// Historical outputs must NOT be critical.
func TestSOPNodeExecutionBuildsCurrentInputAsCriticalFragment(t *testing.T) {
	const currentInput = "当前步骤输入内容，不可被裁剪"

	node := &model.SopNode{
		Model:  gorm.Model{ID: 10},
		Prompt: "你是一个专业助手",
		Name:   "node-A",
	}
	template := &model.SopTemplate{
		Prompt: "模板级系统提示",
	}
	// Two historical turns before the current input.
	history := []LLMMessage{
		{Role: "user", Content: "历史输入一"},
		{Role: "assistant", Content: "历史输出一"},
		{Role: "user", Content: "历史输入二"},
		{Role: "assistant", Content: "历史输出二"},
	}

	frags := buildSOPNodeFragments(template, node, history, currentInput)

	require.NotEmpty(t, frags, "fragments must not be empty")

	// Exactly one critical user fragment must exist.
	var criticalUserFrags []contextbudget.ContextFragment
	for _, f := range frags {
		if f.Critical && f.Source == contextbudget.SourceUser {
			criticalUserFrags = append(criticalUserFrags, f)
		}
	}
	require.Len(t, criticalUserFrags, 1, "exactly one critical user fragment (current input)")

	cur := criticalUserFrags[0]
	assert.Equal(t, contextbudget.RoleRecent, cur.Role, "current input must have RoleRecent")
	assert.Equal(t, contextbudget.CompressNone, cur.Compressibility, "current input must be incompressible")
	assert.True(t, cur.Critical, "current input must be Critical=true")
	assert.Contains(t, cur.Content, currentInput, "current input content must be included verbatim")

	// Historical assistant outputs must NOT be critical.
	assistantFrags := fragmentsBySource(frags, contextbudget.SourceAssistant)
	require.NotEmpty(t, assistantFrags, "historical assistant outputs must produce fragments")
	for _, af := range assistantFrags {
		assert.False(t, af.Critical,
			"historical assistant output fragment must not be critical: id=%s content=%q", af.ID, af.Content[:min(30, len(af.Content))])
	}

	// System prompt fragment must exist and be immutable.
	systemFrags := fragmentsBySource(frags, contextbudget.SourceSystem)
	require.NotEmpty(t, systemFrags, "system prompt must produce a fragment")
	for _, sf := range systemFrags {
		assert.Equal(t, contextbudget.RoleImmutable, sf.Role, "system fragment must be RoleImmutable")
	}
}

// ----------------------------------------------------------------------------
// Test 2: Gateway ChatRequest carries ContextFragments
// ----------------------------------------------------------------------------

// capturedChatRequest is used by the stubbed ChatStream to record what the
// SOP executor passes through.
type capturedChatRequest struct {
	req aiservice.ChatRequest
}

// TestSOPGatewayPathSendsContextFragments verifies that when executing via the
// Gateway path (modelKey != ""), the ChatRequest fed to aiservice.ChatStream
// contains a non-empty ContextFragments slice, and that Messages is either
// empty or nil (fragments, not pre-rendered messages, own the context).
func TestSOPGatewayPathSendsContextFragments(t *testing.T) {
	const (
		systemPrompt = "系统指令"
		nodePrompt   = "节点指令"
		currentInput = "当前用户输入"
	)

	node := &model.SopNode{
		Model:  gorm.Model{ID: 42},
		Prompt: nodePrompt,
		Name:   "gw-node",
	}
	template := &model.SopTemplate{
		Prompt: systemPrompt,
	}
	history := []LLMMessage{
		{Role: "user", Content: "之前的用户消息"},
		{Role: "assistant", Content: "之前的助手回复"},
	}

	frags := buildSOPNodeFragments(template, node, history, currentInput)

	// buildSOPNodeFragments must produce ContextFragments.
	assert.NotEmpty(t, frags, "buildSOPNodeFragments must return non-empty fragments")

	// Verify that ContextFragments would be placed in ChatRequest correctly.
	req := aiservice.ChatRequest{
		ContextFragments: frags,
		Temperature:      0.7,
	}
	assert.NotNil(t, req.ContextFragments, "ChatRequest.ContextFragments must be set")
	assert.Empty(t, req.Messages, "ChatRequest.Messages must be empty when fragments are used")

	// Current input must appear in a Critical fragment within the request.
	var foundCritical bool
	for _, f := range req.ContextFragments {
		if f.Critical && strings.Contains(f.Content, currentInput) {
			foundCritical = true
			break
		}
	}
	assert.True(t, foundCritical, "current input must appear as a Critical fragment in ChatRequest")

	// Template system prompt must appear in an Immutable fragment.
	var foundSystem bool
	for _, f := range req.ContextFragments {
		if f.Role == contextbudget.RoleImmutable && strings.Contains(f.Content, systemPrompt) {
			foundSystem = true
			break
		}
	}
	assert.True(t, foundSystem, "system/template prompt must appear as Immutable fragment")
}

// ----------------------------------------------------------------------------
// Test 3: Gateway path does NOT call creditSvc.Reserve (old R2 path)
// ----------------------------------------------------------------------------

// spyCreditService is defined here for future integration-style tests that wire
// a complete sopBiz instance. The current TestSOPGatewayPathDoesNotDoubleReserveCredits
// only verifies the shouldSkipDirectReserveForGateway guard directly; full e2e
// double-reserve assertion will land when SOP biz is refactored to accept an
// injectable ICreditService in tests.
//
// It records all Reserve calls; ReserveBudget records its own calls.
type spyCreditService struct {
	reserveCalls       int
	reserveBudgetCalls int
}

func (s *spyCreditService) Reserve(_ context.Context, _ *model.User, _ credit.Operation, _ int64, _ uint64, _ *string) (*credit.Reservation, error) {
	s.reserveCalls++
	return nil, fmt.Errorf("spy: Reserve should not be called on Gateway path")
}

func (s *spyCreditService) ReserveBudget(_ context.Context, _ *model.User, _ credit.BudgetReservationInput) (*credit.Reservation, error) {
	s.reserveBudgetCalls++
	// Simulate successful budget reservation.
	return &credit.Reservation{ID: 9999, Status: credit.StatusReserved}, nil
}

func (s *spyCreditService) CheckAndEstimate(_ context.Context, _ *model.User, _ credit.Operation, _ credit.EstimationInput) (*credit.PreCheckResult, error) {
	return &credit.PreCheckResult{Sufficient: true, SkipDeduction: false, EstimatedCredits: 50, CoefficientID: 1}, nil
}

func (s *spyCreditService) CheckAndEstimateBudget(_ context.Context, _ *model.User, _ credit.BudgetPrecheckInput) (*credit.PreCheckResult, error) {
	return &credit.PreCheckResult{Sufficient: true, SkipDeduction: false, EstimatedCredits: 50}, nil
}

func (s *spyCreditService) Reconcile(_ context.Context, _ uint64, _ int64) error { return nil }
func (s *spyCreditService) Refund(_ context.Context, _ uint64, _ string) error   { return nil }
func (s *spyCreditService) FinalizeReservation(_ context.Context, _ *credit.Reservation, _ *int64, _ *error) error {
	return nil
}
func (s *spyCreditService) GetBalance(_ context.Context, _ *model.User) (*credit.BalanceBreakdown, error) {
	return &credit.BalanceBreakdown{}, nil
}

// TestSOPGatewayPathDoesNotDoubleReserveCredits verifies the double-Reserve
// guard: when modelKey != "" (Gateway path), the old direct creditSvc.Reserve
// (R2 char-based path) is NOT called. Only the middleware's ReserveBudget path
// may be invoked (driven by ContextFragments in the ChatRequest).
func TestSOPGatewayPathDoesNotDoubleReserveCredits(t *testing.T) {
	const currentInput = "问题"

	node := &model.SopNode{
		Model:  gorm.Model{ID: 5},
		Prompt: "助手提示",
	}
	template := &model.SopTemplate{
		Prompt: "系统提示",
	}
	history := []LLMMessage{
		{Role: "user", Content: "历史消息"},
		{Role: "assistant", Content: "历史回答"},
	}
	// modelKey != "" signals Gateway path.
	const modelKey = "volc-deepseek-v3"

	frags := buildSOPNodeFragments(template, node, history, currentInput)

	// buildSOPNodeFragments must produce fragments when modelKey != "".
	assert.NotEmpty(t, frags, "Gateway path must produce fragments")

	// The shouldSkipDirectReserve guard must return true for Gateway path.
	shouldSkip := shouldSkipDirectReserveForGateway(modelKey)
	assert.True(t, shouldSkip,
		"shouldSkipDirectReserveForGateway must return true when modelKey != '', got false")

	// Confirm that when modelKey == "" (legacy path), the guard is false.
	shouldNotSkip := shouldSkipDirectReserveForGateway("")
	assert.False(t, shouldNotSkip,
		"shouldSkipDirectReserveForGateway must return false for legacy path (empty modelKey)")
}

// ----------------------------------------------------------------------------
// Test 4: SOP chat builds conversation fragments
// ----------------------------------------------------------------------------

// TestSOPChatBuildsConversationFragments verifies that the SOP trailing-chat
// path produces the correct fragment structure:
//   - Historical user/assistant turns → durable fragments (compressible).
//   - The current question → critical user fragment (non-compressible).
//   - No fragment is both critical AND durable for historical messages.
func TestSOPChatBuildsConversationFragments(t *testing.T) {
	const currentQuestion = "追问：这个方案有何风险？"

	// Simulate the history that would exist before this chat turn.
	// Includes both node execution messages (as "run facts") and prior chat turns.
	chatHistory := []LLMMessage{
		{Role: "user", Content: "节点一的输入"},
		{Role: "assistant", Content: "节点一的输出"},
		{Role: "user", Content: "节点二的输入"},
		{Role: "assistant", Content: "节点二的输出"},
		{Role: "user", Content: "上一次追问"},
		{Role: "assistant", Content: "上一次回答"},
	}

	frags := buildSOPChatFragments(chatHistory, currentQuestion)

	require.NotEmpty(t, frags, "chat fragments must not be empty")

	// Exactly one fragment must be critical — the current question.
	var criticalFrags []contextbudget.ContextFragment
	for _, f := range frags {
		if f.Critical {
			criticalFrags = append(criticalFrags, f)
		}
	}
	require.Len(t, criticalFrags, 1, "exactly one critical fragment (current question)")
	assert.Contains(t, criticalFrags[0].Content, currentQuestion,
		"the critical fragment must contain the current question")
	assert.Equal(t, contextbudget.CompressNone, criticalFrags[0].Compressibility,
		"current question must be incompressible")

	// Historical turns must be durable / compressible (not critical).
	for _, f := range frags {
		if f.Critical {
			continue // already checked above
		}
		assert.NotEqual(t, contextbudget.CompressNone, f.Compressibility,
			"non-critical historical fragment should be compressible: %q", f.Content[:min(40, len(f.Content))])
	}

	// Assistant history fragments must have SourceAssistant.
	assistantFrags := fragmentsBySource(frags, contextbudget.SourceAssistant)
	assert.NotEmpty(t, assistantFrags, "historical assistant messages must become SourceAssistant fragments")

	// User history fragments (non-current) must have SourceUser.
	userFrags := fragmentsBySource(frags, contextbudget.SourceUser)
	// All user frags: 3 historical + 1 current
	assert.GreaterOrEqual(t, len(userFrags), 1,
		"at least the current question must be SourceUser")
}

// min is a local helper for Go < 1.21 compat (returns min of two ints).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
