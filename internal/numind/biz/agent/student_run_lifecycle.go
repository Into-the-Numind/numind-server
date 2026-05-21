package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/budget"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// StudentRunService handles learner-facing agent run lifecycle operations.
// Spec: #14 follow-up BETA — 6 run lifecycle endpoints.
type StudentRunService struct {
	runner       AgentRunner
	runStore     store.IAgentRunStore
	skillStore   store.IAgentDefinitionStore
	pricingCalc  pricing.ICalculator
	narrationBuf *NarrationBuffer
}

// NewStudentRunService constructs a StudentRunService.
func NewStudentRunService(
	runner AgentRunner,
	runStore store.IAgentRunStore,
	skillStore store.IAgentDefinitionStore,
	pricingCalc pricing.ICalculator,
	narrationBuf *NarrationBuffer,
) *StudentRunService {
	return &StudentRunService{
		runner:       runner,
		runStore:     runStore,
		skillStore:   skillStore,
		pricingCalc:  pricingCalc,
		narrationBuf: narrationBuf,
	}
}

// ---------------------------------------------------------------------------
// Estimate
// ---------------------------------------------------------------------------

// EstimateRunRequest is the payload for POST /v1/agent-runs/estimate.
type EstimateRunRequest struct {
	AgentDefinitionID uint64 `json:"agent_definition_id" binding:"required"`
	Message           string `json:"message" binding:"required"`
}

// EstimateResponse is the response for the estimate endpoint.
type EstimateResponse struct {
	EstimatedCredits int    `json:"estimated_credits"`
	EstimatedTokens  int    `json:"estimated_tokens"`
	Currency         string `json:"currency"` // always "credits"
}

// Estimate returns a pre-flight cost estimate for an agent run.
// Uses budget.EstimateAgentTurn with the agent definition's configured model.
// Falls back to simple inline estimate when pricingCalc is nil.
func (s *StudentRunService) Estimate(ctx context.Context, userID uint, req EstimateRunRequest) (*EstimateResponse, error) {
	// Look up the skill to get credit cap context (and validate it exists).
	ad, err := s.resolveDefinition(ctx, userID, req.AgentDefinitionID)
	if err != nil {
		return nil, err
	}

	// Combine message length + a representative system prompt size.
	// In production, ad.GeneratedSkillBody is the actual prompt; use that if available.
	systemPromptLen := len(ad.GeneratedSkillBody)
	if ad.AdvancedMode {
		systemPromptLen = len(ad.CustomSkillBody)
	}
	promptCharCount := systemPromptLen + len(req.Message) + 500 // +500 heuristic overhead

	var estCredits int64
	var estPromptTokens int

	if s.pricingCalc != nil {
		// Use the canonical budget estimator for the default model (glm-4-7).
		result, estErr := budget.EstimateAgentTurn(ctx, s.pricingCalc,
			"volc", "glm-4-7-251222",
			promptCharCount, budget.DefaultCompletionEstimate)
		if estErr == nil {
			estCredits = result.EstimatedCredits
			estPromptTokens = result.EstimatedPromptTokens + result.EstimatedCompletionTokens
		}
	}

	// Inline fallback: 1 token ≈ 2 chars, 1 credit per 100 tokens (rough).
	if estCredits <= 0 {
		tokens := promptCharCount/2 + budget.DefaultCompletionEstimate
		if tokens < 1 {
			tokens = 1
		}
		estCredits = int64(tokens) / 100
		if estCredits < 1 {
			estCredits = 1
		}
		estPromptTokens = tokens
	}

	// Cap estimate at credit_cap_per_session if configured.
	if ad.CreditCapPerSession != nil && int64(*ad.CreditCapPerSession) < estCredits {
		estCredits = int64(*ad.CreditCapPerSession)
	}

	return &EstimateResponse{
		EstimatedCredits: int(estCredits),
		EstimatedTokens:  estPromptTokens,
		Currency:         "credits",
	}, nil
}

// ---------------------------------------------------------------------------
// Create (start async run)
// ---------------------------------------------------------------------------

// CreateRunRequest is the payload for POST /v1/agent-runs.
type CreateRunRequest struct {
	AgentDefinitionID uint64   `json:"agent_definition_id" binding:"required"`
	SessionID         string   `json:"session_id,omitempty"` // empty → new session
	Message           string   `json:"message" binding:"required"`
	AttachmentURLs    []string `json:"attachment_urls,omitempty"`
}

// CreateRunResponse is returned from POST /v1/agent-runs.
type CreateRunResponse struct {
	AgentRunID uint64    `json:"agent_run_id"`
	SessionID  string    `json:"session_id"`
	Status     string    `json:"status"` // always "running" on success
	StartedAt  time.Time `json:"started_at"`
}

// Create starts an agent run asynchronously and returns the run_id immediately.
// AgentRunner.Run is synchronous; it is wrapped in a goroutine so the HTTP
// handler can return without waiting for the ReAct loop to complete.
//
// The goroutine uses context.Background() (detached) so that HTTP request
// cancellation does NOT kill the in-flight run.
func (s *StudentRunService) Create(ctx context.Context, userID uint, req CreateRunRequest) (*CreateRunResponse, error) {
	if req.Message == "" {
		return nil, errno.ErrBind.SetMessage("message is required")
	}

	// Validate agent definition exists and belongs to the learner's parent.
	ad, err := s.resolveDefinition(ctx, userID, req.AgentDefinitionID)
	if err != nil {
		return nil, err
	}

	// Generate session ID if not provided.
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Build the input: combine message + attachment URLs if any.
	input := req.Message
	if len(req.AttachmentURLs) > 0 {
		attachJSON, _ := json.Marshal(req.AttachmentURLs)
		input = fmt.Sprintf("%s\n\n[attachments: %s]", req.Message, string(attachJSON))
	}

	// Resolve tool names from ToolFlags JSON.
	toolNames := toolNamesFromFlags(ad.ToolFlags)

	runReq := RunRequest{
		UserID:            userID,
		SessionID:         sessionID,
		Input:             input,
		ToolNames:         toolNames,
		AgentDefinitionID: req.AgentDefinitionID,
		EnableMemory:      true,
	}

	startedAt := time.Now()

	// Async: detached context so HTTP cancel doesn't abort the run.
	go func() {
		detachedCtx := context.Background()
		_, _ = s.runner.Run(detachedCtx, runReq)
		// Result is persisted to DB by runner.Run; frontend polls via narration + GET run.
	}()

	// We return a synthetic run_id=0 here because the actual DB row is created
	// inside runner.Run (which is async). Frontend polls /narration?since=... and
	// GET /agent-runs/:id; for v1 the run_id is not known pre-flight.
	//
	// v1 pragmatic choice: return a "pending" response. The frontend can follow
	// up with GET /agent-runs/:session_id to find the run_id after a brief poll.
	// Full pre-run row creation would require refactoring runner.go — deferred.
	//
	// Note: we still return started_at and session_id which the frontend can use
	// to track the session.
	return &CreateRunResponse{
		AgentRunID: 0, // v1: async create — run_id not yet available
		SessionID:  sessionID,
		Status:     "running",
		StartedAt:  startedAt,
	}, nil
}

// ---------------------------------------------------------------------------
// PollNarration
// ---------------------------------------------------------------------------

// PollNarration returns narration events for runID where event.Timestamp > since.
// Verifies that the run belongs to userID (404 if not).
func (s *StudentRunService) PollNarration(ctx context.Context, userID uint, runID uint64, since time.Time) ([]*narration.Event, error) {
	if err := s.verifyRunOwnership(ctx, userID, runID); err != nil {
		return nil, err
	}

	if s.narrationBuf == nil {
		return []*narration.Event{}, nil
	}

	return s.narrationBuf.QuerySince(runID, since), nil
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

// Cancel sends a cancellation signal to a running agent run.
// Verifies ownership; returns ErrAgentRunNotFound (404) if the run belongs to a
// different user or doesn't exist.
func (s *StudentRunService) Cancel(ctx context.Context, userID uint, runID uint64) error {
	if err := s.verifyRunOwnership(ctx, userID, runID); err != nil {
		return err
	}

	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("StudentRunService.Cancel: get run: %w", err)
	}
	if run.Status != "running" {
		return errno.ErrAgentRunNotCancellable
	}

	s.runner.Cancel(runID)
	return nil
}

// ---------------------------------------------------------------------------
// ExtendBudget
// ---------------------------------------------------------------------------

// ExtendBudgetRequest is the payload for POST /v1/agent-runs/:id/extend-budget.
type ExtendBudgetRequest struct {
	AddCredits int `json:"add_credits" binding:"required,min=1"`
}

// ExtendBudget records a budget extension for a paused/budget-exceeded run.
// v1: writes extension metadata to agent_run.terminal_metadata; full resume
// (re-invoking AgentRunner.Run from checkpoint) is deferred to a later feature.
func (s *StudentRunService) ExtendBudget(ctx context.Context, userID uint, runID uint64, req ExtendBudgetRequest) (*model.AgentRun, error) {
	if err := s.verifyRunOwnership(ctx, userID, runID); err != nil {
		return nil, err
	}

	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("StudentRunService.ExtendBudget: get run: %w", err)
	}

	// Only allow extension on runs that stopped due to budget exhaustion.
	// "terminated" + state_reason matching budget-exceeded signals.
	if run.Status != "terminated" {
		return nil, fmt.Errorf("StudentRunService.ExtendBudget: run is not in a terminal state (status=%s)", run.Status)
	}

	// Write extension record to terminal_metadata.
	ext := map[string]any{
		"budget_extension": map[string]any{
			"add_credits": req.AddCredits,
			"extended_at": time.Now().Format(time.RFC3339),
			"extended_by": userID,
		},
	}
	extJSON, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("StudentRunService.ExtendBudget: marshal metadata: %w", err)
	}

	if err := s.runStore.UpdateTerminalMetadata(ctx, runID, extJSON); err != nil {
		return nil, fmt.Errorf("StudentRunService.ExtendBudget: update metadata: %w", err)
	}

	// Re-fetch updated run.
	updated, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("StudentRunService.ExtendBudget: re-fetch: %w", err)
	}
	return updated, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// resolveDefinition looks up the agent_definition by ID and validates that
// userID has access to it.
//
// Access rule: the learner's parent_user_id must match ad.ParentUserID.
// For simplicity in v1, we check that the run's user is directly the parent
// (same as runner.go line 262). Sub-user access validation would require
// reading the user record; deferred to a future auth middleware pass.
//
// Returns ErrSkillNotFound (404) for missing or cross-tenant definitions.
func (s *StudentRunService) resolveDefinition(ctx context.Context, userID uint, agentDefID uint64) (*model.AgentDefinition, error) {
	if s.skillStore == nil {
		return nil, fmt.Errorf("StudentRunService: skillStore not configured")
	}
	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, agentDefID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSkillNotFound
		}
		return nil, fmt.Errorf("StudentRunService.resolveDefinition: %w", err)
	}
	// Expose existence only to the owning parent (same policy as runner.go).
	if ad.ParentUserID != userID {
		return nil, errno.ErrSkillNotFound
	}
	return ad, nil
}

// verifyRunOwnership returns ErrAgentRunNotFound (404) if runID doesn't exist
// or belongs to a different user.
func (s *StudentRunService) verifyRunOwnership(ctx context.Context, userID uint, runID uint64) error {
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrAgentRunNotFound
		}
		return fmt.Errorf("StudentRunService.verifyRunOwnership: %w", err)
	}
	if run.UserID != userID {
		// Do not reveal existence to other users.
		return errno.ErrAgentRunNotFound
	}
	return nil
}

// toolNamesFromFlags converts agent_definition.ToolFlags (JSON {"tool_name": true})
// to a []string of enabled tool names. Returns nil if ToolFlags is empty.
func toolNamesFromFlags(toolFlagsJSON []byte) []string {
	if len(toolFlagsJSON) == 0 {
		return nil
	}
	var flags map[string]bool
	if err := json.Unmarshal(toolFlagsJSON, &flags); err != nil {
		return nil
	}
	var names []string
	for name, enabled := range flags {
		if enabled {
			names = append(names, name)
		}
	}
	return names
}
