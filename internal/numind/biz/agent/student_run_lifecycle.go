package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/budget"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// StudentRunService handles learner-facing agent run lifecycle operations.
// Spec: #14 follow-up BETA — 6 run lifecycle endpoints.
type StudentRunService struct {
	runner          AgentRunner
	runStore        store.IAgentRunStore
	skillStore      store.IAgentDefinitionStore
	pricingCalc     pricing.ICalculator
	narrationProv   *narration.Provider
	narrationBuf    *NarrationBuffer
	attachmentStore store.IAgentAttachmentStore // task 1.3: capability-aware routing
	streamLock      *stream.SubscriptionLock    // T07: SSE single-subscriber guard
}

// NewStudentRunService constructs a StudentRunService.
//
// narrationProv + narrationBuf must be wired together: the service forwards
// every event emitted by the Provider (per agent_run.id) into the Buffer,
// which is what PollNarration reads. Either side nil disables the forwarder
// — narration_provider init can fail gracefully (yaml missing) and the
// service still functions, just without learner-visible tool-call narration.
func NewStudentRunService(
	runner AgentRunner,
	runStore store.IAgentRunStore,
	skillStore store.IAgentDefinitionStore,
	pricingCalc pricing.ICalculator,
	narrationProv *narration.Provider,
	narrationBuf *NarrationBuffer,
) *StudentRunService {
	return &StudentRunService{
		runner:        runner,
		runStore:      runStore,
		skillStore:    skillStore,
		pricingCalc:   pricingCalc,
		narrationProv: narrationProv,
		narrationBuf:  narrationBuf,
		streamLock:    stream.NewSubscriptionLock(),
	}
}

// WithAttachmentStore wires the IAgentAttachmentStore so that capability-aware
// routing (task 1.3) can load AgentAttachment entities for fallback polling.
// Call this at biz.go wiring time alongside WithNarrationProvider.
func (s *StudentRunService) WithAttachmentStore(attStore store.IAgentAttachmentStore) *StudentRunService {
	s.attachmentStore = attStore
	return s
}

// forwardNarration drains the narration provider's per-runID channel into the
// poll-friendly NarrationBuffer. Without this bridge, Provider.Emit puts
// events into an in-memory channel that nobody reads from, and PollNarration
// (which queries the Buffer) returns [] — exactly the symptom that the
// learner-facing UI surfaced as "no narration visible despite tools running".
//
// Lifecycle: spawned per Create call, exits when runner.Run finishes and
// runner.go's defer runs provider.CloseRun(runID), which closes the channel.
// Safe to call when narrationProv or narrationBuf is nil (graceful degrade).
func (s *StudentRunService) forwardNarration(runID uint64) {
	if s.narrationProv == nil || s.narrationBuf == nil {
		return
	}
	ch, cleanup := s.narrationProv.Subscribe(runID)
	defer cleanup()
	for ev := range ch {
		evCopy := ev // pin the loop var; AppendEvent stores by pointer
		s.narrationBuf.AppendEvent(runID, &evCopy)
	}
}

// ---------------------------------------------------------------------------
// Estimate
// ---------------------------------------------------------------------------

// EstimateRunRequest is the payload for POST /v1/agent-runs/estimate.
// Field names align with web-v3 src/types/agent.ts EstimateRequest contract.
type EstimateRunRequest struct {
	AgentDefinitionID uint64 `json:"agent_skill_id" binding:"required"`
	Message           string `json:"input_text" binding:"required"`
}

// EstimateResponse is the response for the estimate endpoint.
// Field names + range/flag shape align with web-v3 EstimateResponse contract:
// {min, max, is_large_task}. Min/Max are derived from the central single-value
// estimate (±20% band); is_large_task = true when central estimate > 100 credits.
type EstimateResponse struct {
	Min         int  `json:"min"`
	Max         int  `json:"max"`
	IsLargeTask bool `json:"is_large_task"`
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

	// Derive {min, max, is_large_task} band from central estimate (web-v3 contract).
	// ±20% band accounts for completion-token variance in ReAct loops.
	_ = estPromptTokens // retained for future telemetry; not exposed in response
	min := int(float64(estCredits) * 0.8)
	max := int(float64(estCredits) * 1.2)
	if min < 1 {
		min = 1
	}
	if max < min {
		max = min
	}
	return &EstimateResponse{
		Min:         min,
		Max:         max,
		IsLargeTask: estCredits > 100,
	}, nil
}

// ---------------------------------------------------------------------------
// Create (start async run)
// ---------------------------------------------------------------------------

// CreateRunRequest is the payload for POST /v1/agent-runs.
// Field names align with web-v3 src/types/agent.ts CreateRunRequest contract.
type CreateRunRequest struct {
	AgentDefinitionID uint64   `json:"agent_skill_id" binding:"required"`
	SessionID         string   `json:"session_id,omitempty"` // empty → new session
	Message           string   `json:"input_text" binding:"required"`
	AttachmentURLs    []string `json:"attachment_urls,omitempty"`
	// AttachmentIDs is the task-1.3 field: the frontend sends DB row IDs for
	// uploaded attachments so that buildAgentInputForModel can load the full
	// AgentAttachment entity (incl. Modality, TextFallback, FallbackReady) and
	// apply capability-aware routing. Takes precedence over AttachmentURLs when
	// both are present.
	AttachmentIDs []uint64 `json:"attachment_ids,omitempty"`
	// ModelKey is the user-selected model identifier (from the model picker in
	// the UI). When non-empty, buildAgentInputForModel uses it to determine
	// capability routing. When empty, conservative defaults apply (full fallback).
	ModelKey string `json:"model_key,omitempty"`
}

// CreateRunResponse is returned from POST /v1/agent-runs.
// Field names align with web-v3 src/types/agent.ts CreateRunResponse contract.
// run_id is the real DB row id (Create pre-allocates the row synchronously so
// the frontend can immediately poll GET /agent-runs/:id).
type CreateRunResponse struct {
	RunID               uint64 `json:"run_id"`
	SessionID           string `json:"session_id"`
	EstimatedCreditsMin int    `json:"estimated_credits_min"`
	EstimatedCreditsMax int    `json:"estimated_credits_max"`
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

	// Build the input: combine message + attachments using capability-aware routing
	// (task 1.3). When AttachmentIDs is populated the system loads full AgentAttachment
	// entities and routes inline vs fallback per the active model's capability matrix.
	//
	// Legacy path: AttachmentURLs (no DB entity) → buildAgentInput (plain text hint).
	// New path:    AttachmentIDs → buildAgentInputForModel → MessagesToInputString.
	//
	// Until runner.go (task 1.5) accepts InputMessages natively, the result is
	// serialised to string via MessagesToInputString for RunRequest.Input.
	var input string
	var hasFallbackAttachments bool
	if len(req.AttachmentIDs) > 0 && s.attachmentStore != nil {
		atts := loadAttachmentsByIDs(ctx, s.attachmentStore, req.AttachmentIDs, userID)
		msgs, buildErr := buildAgentInputForModel(ctx, req.Message, atts, req.ModelKey, s.attachmentStore)
		if buildErr != nil {
			log.Warnw("StudentRunService.Create: buildAgentInputForModel failed, falling back",
				"user_id", userID, "error", buildErr)
			input = buildAgentInput(req.Message, req.AttachmentURLs)
		} else {
			// Task 1.5 (task 1.3 deferral): detect whether any attachment used the
			// text-fallback path so that runner.Run can inject the attachment reminder
			// into system prompt segment 5.
			hasFallbackAttachments = HasFallbackAttachments(msgs)
			input = MessagesToInputString(msgs)
		}
	} else {
		// Legacy path: use plain URL list (no capability routing).
		// buildAgentInput emits an explicit Chinese instruction telling the LLM to
		// use the file_read tool. Without that instruction the LLM tends to ignore
		// a bare URL list and reply "you didn't upload anything" — see bug-from-
		// customer 2026-05-22 (#14-followup agent-attachment-flow).
		if len(req.AttachmentIDs) > 0 && s.attachmentStore == nil {
			log.Warnw("StudentRunService.Create: attachmentStore not configured, AttachmentIDs ignored",
				"user_id", userID, "attachment_ids", req.AttachmentIDs)
		}
		input = buildAgentInput(req.Message, req.AttachmentURLs)
	}

	// Resolve tool names from ToolFlags JSON.
	toolNames := toolNamesFromFlags(ad.ToolFlags)

	// 继承旧会话的 is_pinned 和 session_name
	var isPinned bool
	var sessionName string
	if sessionID != "" {
		runs, _, err := s.runStore.ListBySession(ctx, sessionID, 0, 1)
		if err == nil && len(runs) > 0 {
			isPinned = runs[0].IsPinned
			sessionName = runs[0].SessionName
		}
	}

	// Pre-create the agent_run row synchronously so the HTTP response can
	// return a real run_id to the frontend (which polls GET /agent-runs/:id).
	startedAt := time.Now()
	preRun := &model.AgentRun{
		UserID:            userID,
		SessionID:         sessionID,
		AgentDefinitionID: req.AgentDefinitionID,
		Status:            "running",
		Messages:          datatypes.JSON([]byte("[]")),
		StartedAt:         startedAt,
		// V1.5 compact-v1-removal — V1 包已删，所有新 run 默认走 V2 (maybeCompactV2)。
		UseCompactV2: true,
		IsPinned:     isPinned,
		SessionName:  sessionName,
	}
	if err := s.runStore.Create(ctx, preRun); err != nil {
		return nil, fmt.Errorf("StudentRunService.Create pre-create row: %w", err)
	}

	runReq := RunRequest{
		UserID:                userID,
		SessionID:             sessionID,
		Input:                 input,
		ToolNames:             toolNames,
		AgentDefinitionID:     req.AgentDefinitionID,
		EnableMemory:          true,
		ExistingRunID:         preRun.ID,
		AttachmentHasFallback: hasFallbackAttachments,
	}

	// Bridge narration events: Provider.Emit pushes events to an in-memory
	// channel keyed by runID; PollNarration reads from the queryable Buffer.
	// Without this forwarder the two halves never connect and the learner UI
	// gets [] forever despite the tools actually running. Spawn BEFORE the
	// runner goroutine so the Subscribe registration is in place when the
	// first PreToolCall fires its StateUse emit; memStreamer.Subscribe is
	// lazy-create-safe, so even a slight delay would not lose events, but
	// ordering is still cheaper to reason about this way.
	go s.forwardNarration(preRun.ID)

	// Async: detached context so HTTP cancel doesn't abort the run.
	go func() {
		detachedCtx := context.Background()
		_, _ = s.runner.Run(detachedCtx, runReq)
		// Result is persisted to DB by runner.Run; frontend polls via narration + GET run.
	}()

	// Return the pre-allocated run_id immediately so the frontend can poll
	// GET /agent-runs/:id without an extra session→run lookup.
	// EstimatedCredits{Min,Max} are populated by callers via Estimate first.
	return &CreateRunResponse{
		RunID:     preRun.ID,
		SessionID: sessionID,
	}, nil
}

// buildAgentInput composes the LLM-facing user-message text from the human's
// message plus any uploaded attachment COS URLs.
//
// Deprecated: use buildAgentInputForModel. Will be removed when task 1.5
// completes multimodal wiring (runner.go accepts InputMessages natively).
//
// When attachments are present, an unconditional Chinese imperative is appended
// telling the agent to invoke the file_read tool with each URL. The previous
// implementation emitted "[attachments: <JSON>]" which the LLM frequently
// ignored — it would reply "you didn't upload anything" despite the URLs
// being in the prompt.
//
// Phrasing notes (locked by tests, do NOT soften):
//   - "请立即调用" (imperative, no opt-out) — earlier "如需查看" gave thinking
//     models like deepseek-v4-pro an out and they would skip the tool call.
//   - The hint MUST come AFTER the user message, not before. Hoisting it to
//     the top changes the ack-then-act priming; tests assert the position.
//   - "然后再回答用户" makes the tool call a prerequisite, not optional.
//
// Returns the bare message unchanged if attachmentURLs is empty.
func buildAgentInput(message string, attachmentURLs []string) string {
	if len(attachmentURLs) == 0 {
		return message
	}
	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\n【系统提示】用户上传了以下附件，请立即调用 file_read 工具读取它们的内容（传入对应的 file_url 参数），然后再回答用户的问题：\n")
	for _, u := range attachmentURLs {
		b.WriteString("- ")
		b.WriteString(u)
		b.WriteString("\n")
	}
	return b.String()
}

// loadAttachmentsByIDs fetches AgentAttachment entities for the given IDs,
// enforcing that each row belongs to userID. Rows that fail the ownership
// check or cannot be fetched are skipped (logged as warnings).
//
// Silent skip is intentional: a single attachment fetch failure should not
// abort the entire run. The run continues with whichever attachments loaded
// successfully. Callers that need strict all-or-nothing semantics should not
// use this function.
//
// This is the biz-layer bridge for task 1.3: the HTTP handler binds
// CreateRunRequest.AttachmentIDs from the frontend; this function resolves
// them to full entities for buildAgentInputForModel.
func loadAttachmentsByIDs(
	ctx context.Context,
	attStore store.IAgentAttachmentStore,
	ids []uint64,
	userID uint,
) []*model.AgentAttachment {
	var results []*model.AgentAttachment
	for _, id := range ids {
		att, err := attStore.GetByIDAndUser(ctx, id, userID)
		if err != nil {
			log.Warnw("loadAttachmentsByIDs: skipping attachment",
				"att_id", id, "user_id", userID, "error", err)
			continue
		}
		results = append(results, att)
	}
	return results
}

// ---------------------------------------------------------------------------
// T07 — SSE streaming: AcquireStreamLock / ReleaseStreamLock / RunStream
// ---------------------------------------------------------------------------

// AcquireStreamLock creates the agent_run row (reusing the same pre-create
// logic as Create) and then tries to acquire a single-subscriber SSE lock on
// it.  Only one SSE connection per run is allowed; a second caller gets
// acquired=false with the existing runID so it can surface a 409 with the ID.
//
// If acquired=false the agent_run row is NOT rolled back — it has been written
// to DB and the caller must NOT try to clean it up (the row may already have
// been picked up by a background runner in a concurrent CreateStream request).
func (s *StudentRunService) AcquireStreamLock(ctx context.Context, userID uint, req CreateRunRequest) (runID uint64, acquired bool, err error) {
	if req.Message == "" {
		return 0, false, errno.ErrBind.SetMessage("message is required")
	}

	// Validate agent definition.
	ad, err := s.resolveDefinition(ctx, userID, req.AgentDefinitionID)
	if err != nil {
		return 0, false, err
	}

	// Generate session ID if not provided.
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Inherit is_pinned / session_name from prior session runs.
	var isPinned bool
	var sessionName string
	if sessionID != "" {
		runs, _, listErr := s.runStore.ListBySession(ctx, sessionID, 0, 1)
		if listErr == nil && len(runs) > 0 {
			isPinned = runs[0].IsPinned
			sessionName = runs[0].SessionName
		}
	}

	// Pre-create the agent_run row synchronously (same pattern as Create).
	startedAt := time.Now()
	preRun := &model.AgentRun{
		UserID:            userID,
		SessionID:         sessionID,
		AgentDefinitionID: req.AgentDefinitionID,
		Status:            "running",
		Messages:          datatypes.JSON([]byte("[]")),
		StartedAt:         startedAt,
		UseCompactV2:      true,
		IsPinned:          isPinned,
		SessionName:       sessionName,
	}
	if err := s.runStore.Create(ctx, preRun); err != nil {
		return 0, false, fmt.Errorf("StudentRunService.AcquireStreamLock pre-create row: %w", err)
	}

	// Suppress unused variable warning from resolveDefinition result.
	_ = ad

	// Attempt to acquire the SSE lock for the new run.
	if !s.streamLock.Acquire(preRun.ID) {
		// Another subscriber already holds this run's lock (extremely unlikely for
		// a brand-new run, but the interface contract must be upheld).
		return preRun.ID, false, nil
	}
	return preRun.ID, true, nil
}

// ReleaseStreamLock releases the SSE single-subscriber lock for runID.
// It is idempotent — safe to call via defer even if AcquireStreamLock was
// never called or returned acquired=false.
func (s *StudentRunService) ReleaseStreamLock(runID uint64) {
	s.streamLock.Release(runID)
}

// RunStream executes the agent in streaming mode, emitting stream.Event values
// onto ch. The caller must have already called AcquireStreamLock (which
// pre-creates the agent_run row and acquires the SSE lock).
//
// RunStream does NOT close ch; the controller goroutine that spawns RunStream
// closes ch after RunStream returns so that the SSE pump can drain all
// remaining events.
//
// The req.SessionID / req.AgentDefinitionID fields are used to build the
// runner's RunRequest; the session ID is re-derived from the existing row to
// ensure consistency.
func (s *StudentRunService) RunStream(ctx context.Context, userID uint, req CreateRunRequest, runID uint64, ch chan<- stream.Event) (*RunResult, error) {
	// Load the pre-created run to get the canonical sessionID from DB.
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("StudentRunService.RunStream: load run: %w", err)
	}

	// Build capability-aware input (same logic as Create).
	var input string
	var hasFallbackAttachments bool
	if len(req.AttachmentIDs) > 0 && s.attachmentStore != nil {
		atts := loadAttachmentsByIDs(ctx, s.attachmentStore, req.AttachmentIDs, userID)
		msgs, buildErr := buildAgentInputForModel(ctx, req.Message, atts, req.ModelKey, s.attachmentStore)
		if buildErr != nil {
			log.Warnw("StudentRunService.RunStream: buildAgentInputForModel failed, falling back",
				"user_id", userID, "error", buildErr)
			input = buildAgentInput(req.Message, req.AttachmentURLs)
		} else {
			hasFallbackAttachments = HasFallbackAttachments(msgs)
			input = MessagesToInputString(msgs)
		}
	} else {
		if len(req.AttachmentIDs) > 0 && s.attachmentStore == nil {
			log.Warnw("StudentRunService.RunStream: attachmentStore not configured, AttachmentIDs ignored",
				"user_id", userID, "attachment_ids", req.AttachmentIDs)
		}
		input = buildAgentInput(req.Message, req.AttachmentURLs)
	}

	toolNames := toolNamesFromFlags(nil) // tool flags resolved from the loaded run's definition below
	if s.skillStore != nil {
		ad, adErr := s.skillStore.GetByIDIncludeInactive(ctx, run.AgentDefinitionID)
		if adErr == nil {
			toolNames = toolNamesFromFlags(ad.ToolFlags)
		}
	}

	runReq := RunRequest{
		UserID:                userID,
		SessionID:             run.SessionID,
		Input:                 input,
		ToolNames:             toolNames,
		AgentDefinitionID:     run.AgentDefinitionID,
		EnableMemory:          true,
		ExistingRunID:         runID,
		AttachmentHasFallback: hasFallbackAttachments,
	}

	// Bridge narration events (same as Create).
	go s.forwardNarration(runID)

	return s.runner.RunStream(ctx, runReq, runID, ch)
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

// safeToolBaseline 是所有 Agent 默认启用的无害工具集，配置者无需显式勾选。
// configurator 通过 tool_flags 仅控制 3 个风险类别（code_sandbox/media/dangerous）
// 是否启用，不影响这个 baseline。
//
// 等未来 UX 增加 "individual tool 单工具开关" 时，可以让 tool_flags 显式传
// {"web_search": false} 等覆盖 baseline；当前 frontend (AgentAdvancedEdit.vue)
// 只有 3 个 category 开关，所以 baseline 永远启用。
var safeToolBaseline = []string{
	"kb_search",          // RAG 检索
	"learner_data_query", // 学员档案（read-only）
	"memory_read",        // 长期记忆读
	"memory_write",       // 长期记忆写
	"get_current_date",   // 当前时间
	"ask_user_question",  // 反问学员
	"web_search",         // 网络搜索（Tavily）
	"web_fetch",          // URL → Markdown
	"file_read",          // PDF/图/文本
}

// categoryToTools 把 frontend AgentAdvancedEdit.vue 的 3 个 risk-category 开关
// 展开为受限工具（这些工具默认 OFF，必须通过 category 显式启用）。
//
//	code_sandbox  → bash_exec      (RequiresSandbox=true)
//	media         → image_gen      (Category="多媒体")
//	dangerous     → bash_exec      (RiskLevel="dangerous" 别名)
//	enable_skills → invoke_skill   (V1.5 Track 4 task 4.4; requires code_sandbox for IsEnabled to pass)
var categoryToTools = map[string][]string{
	"code_sandbox":  {"bash_exec"},
	"media":         {"image_gen"},
	"dangerous":     {"bash_exec"},    // alias of code_sandbox for now
	"enable_skills": {"invoke_skill"}, // V1.5 Track 4: invoke_skill skill framework
}

// toolNamesFromFlags resolves agent_definition.ToolFlags JSON to []string of
// enabled tool names that AgentRunner can look up in the registry.
//
// Frontend stores `tool_flags` as `{category_name: bool}` over 3 risk gates
// (code_sandbox, media, dangerous — see AgentAdvancedEdit.vue), NOT as raw
// tool names. This function:
//  1. Always includes safeToolBaseline (kb_search, web_search, memory_*, ...).
//  2. Expands enabled categories into their tool sets via categoryToTools.
//  3. Honors any direct tool-name keys not in categoryToTools (future-proofs
//     for when frontend gains per-tool toggles); explicit false disables.
//
// Returns nil only if json unmarshal fails. Empty/missing ToolFlags returns
// just the safe baseline so Agents are never useless ReAct short-circuits.
func toolNamesFromFlags(toolFlagsJSON []byte) []string {
	// Start with safe baseline always-on.
	enabled := make(map[string]bool, len(safeToolBaseline)+3)
	for _, name := range safeToolBaseline {
		enabled[name] = true
	}

	if len(toolFlagsJSON) == 0 {
		return mapKeysWhereTrue(enabled)
	}

	var flags map[string]bool
	if err := json.Unmarshal(toolFlagsJSON, &flags); err != nil {
		// Malformed JSON: fall back to safe baseline. Logging happens at the
		// caller (runner.go) once the result reaches the registry resolver.
		return mapKeysWhereTrue(enabled)
	}

	for key, on := range flags {
		if tools, isCategory := categoryToTools[key]; isCategory {
			// Category toggle: expand to tool set.
			for _, t := range tools {
				enabled[t] = on
			}
			continue
		}
		// Direct tool name (future: per-tool toggles in UI); explicit false
		// disables a baseline tool.
		enabled[key] = on
	}

	return mapKeysWhereTrue(enabled)
}

func mapKeysWhereTrue(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for name, on := range m {
		if on {
			out = append(out, name)
		}
	}
	return out
}
