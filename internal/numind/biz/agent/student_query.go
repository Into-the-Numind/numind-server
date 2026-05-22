package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// defaultCreditBudget is the fallback per-session credits budget when
// agent_definition.credit_cap_per_session is NULL or 0.
const defaultCreditBudget = 200

// RunSummary is a lightweight view of model.AgentRun returned by list endpoints.
// Full messages are omitted to keep list payloads small.
// JSON field names align with web-v3 src/types/agent.ts AgentRun / RecentSession contract.
type RunSummary struct {
	ID                uint64     `json:"id"`
	UserID            uint       `json:"user_id"`
	SessionID         string     `json:"session_id"`
	AgentDefinitionID uint64     `json:"agent_skill_id,omitempty"`
	Status            string     `json:"status"`
	StateReason       string     `json:"state_reason,omitempty"`
	CompactSummary    string     `json:"compact_summary,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`

	// Fix 2: enriched identity fields (from agent_definition).
	AgentName  string `json:"agent_name,omitempty"`
	AgentEmoji string `json:"agent_emoji,omitempty"`

	// Fix 2: timing / preview.
	// LastActiveAt is ended_at if set, else updated_at, else started_at (RFC3339).
	LastActiveAt string `json:"last_active_at,omitempty"`
	// PreviewText is the first user-role content from messages, truncated to ~60 chars.
	PreviewText string `json:"preview_text,omitempty"`

	// Fix 1: credits fields computed on read (no DB columns added).
	CreditsUsed           int    `json:"credits_used"`
	CreditsBudget         int    `json:"credits_budget"`
	CreditsThresholdState string `json:"credits_threshold_state"` // 'under_60' | 'warning_60' | 'blocked_100'
}

// frontendStatus maps backend agent_run.status + state_reason to the AgentRunStatus
// enum the web-v3 frontend expects: 'pending' | 'running' | 'completed' | 'timeout'
// | 'failed' | 'cancelled' | 'budget_exhausted'.
//
// Backend stores "running" while a run is active and "terminated" once it finishes;
// state_reason (TerminalReason) carries the granular outcome. The frontend never
// learned the "terminated" status, so we collapse it here at the response boundary.
func frontendStatus(status, stateReason string) string {
	switch status {
	case "running", "pending":
		return status
	case "terminated":
		switch stateReason {
		case "completed":
			return "completed"
		case "error_max_budget":
			return "budget_exhausted"
		case "max_turns":
			return "timeout"
		case "cancelled", "aborted_streaming", "aborted_tools":
			return "cancelled"
		default:
			return "failed"
		}
	}
	return status
}

// creditsThresholdState computes the threshold state string from used/budget ratio.
func creditsThresholdState(used, budget int) string {
	if budget <= 0 {
		return "under_60"
	}
	ratio := float64(used) / float64(budget)
	switch {
	case ratio >= 1.0:
		return "blocked_100"
	case ratio >= 0.6:
		return "warning_60"
	default:
		return "under_60"
	}
}

// runEnrichment holds the computed-on-read extra fields for a single run.
type runEnrichment struct {
	agentName     string
	agentEmoji    string
	creditsUsed   int
	creditsBudget int
}

// runToSummary converts a model.AgentRun to a RunSummary without enrichment.
// Call enrichSummary afterwards to populate credits / agent name / preview.
func runToSummary(r model.AgentRun) RunSummary {
	lastActive := r.StartedAt
	if r.EndedAt != nil {
		lastActive = *r.EndedAt
	} else if !r.UpdatedAt.IsZero() {
		lastActive = r.UpdatedAt
	}

	return RunSummary{
		ID:                    r.ID,
		UserID:                r.UserID,
		SessionID:             r.SessionID,
		AgentDefinitionID:     r.AgentDefinitionID,
		Status:                frontendStatus(r.Status, r.StateReason),
		StateReason:           r.StateReason,
		CompactSummary:        r.CompactSummary,
		StartedAt:             r.StartedAt,
		EndedAt:               r.EndedAt,
		CreatedAt:             r.CreatedAt,
		LastActiveAt:          lastActive.UTC().Format(time.RFC3339),
		PreviewText:           extractPreviewText(r.Messages),
		CreditsThresholdState: "under_60", // will be overwritten by enrichSummary
	}
}

// enrichSummary applies computed enrichment to an already-constructed RunSummary.
func enrichSummary(s *RunSummary, e runEnrichment) {
	s.AgentName = e.agentName
	s.AgentEmoji = e.agentEmoji
	s.CreditsUsed = e.creditsUsed
	s.CreditsBudget = e.creditsBudget
	s.CreditsThresholdState = creditsThresholdState(e.creditsUsed, e.creditsBudget)
}

// extractPreviewText pulls the first user-role content from the messages JSON array,
// truncated to 60 Unicode codepoints (avoids mid-rune splits with CJK text).
func extractPreviewText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var turns []map[string]any
	if err := json.Unmarshal(raw, &turns); err != nil {
		return ""
	}
	for _, turn := range turns {
		if role, _ := turn["role"].(string); role == "user" {
			if content, ok := turn["content"].(string); ok && content != "" {
				return truncateRunes(content, 60)
			}
		}
	}
	return ""
}

// truncateRunes truncates s to at most n Unicode codepoints.
func truncateRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	_ = utf8.RuneCountInString(s) // silence staticcheck
	return s
}

// SessionSnapshot is returned by GetSessionSnapshot for resume flows.
// Messages is the transformed frontend-shaped array (see transformMessages).
// CompactSummary is the latest compact summary (may be empty).
type SessionSnapshot struct {
	Run            RunSummary  `json:"run"`
	Messages       interface{} `json:"messages"`        // frontend-shaped AgentMessage array
	CompactSummary string      `json:"compact_summary"` // from agent_run.compact_summary
}

// FeedbackRequest carries a 👍/👎 verdict and optional text from the learner.
type FeedbackRequest struct {
	Verdict string `json:"verdict"` // "up" | "down"
	Text    string `json:"text,omitempty"`
}

// StudentQueryService handles the student-facing read + feedback endpoints.
// (#14 follow-up ALPHA — 7 GET + 1 POST).
// TODO(v2): promote feedback to a dedicated agent_run_feedback table for analytics.
type StudentQueryService struct {
	runStore    store.IAgentRunStore
	userStore   store.UserStore
	skillStore  store.IAgentDefinitionStore // for agent_name/emoji + credit_cap_per_session
	creditStore store.CreditStore           // for credits_used computation (SumByReservationIDs)
}

// NewStudentQueryService constructs a StudentQueryService.
// skillStore and creditStore may be nil; the service degrades gracefully
// (credits_used=0, agent_name=”, etc.) rather than panicking.
func NewStudentQueryService(
	runStore store.IAgentRunStore,
	userStore store.UserStore,
	opts ...StudentQueryOption,
) *StudentQueryService {
	svc := &StudentQueryService{runStore: runStore, userStore: userStore}
	for _, o := range opts {
		o(svc)
	}
	return svc
}

// StudentQueryOption is a functional option for NewStudentQueryService.
type StudentQueryOption func(*StudentQueryService)

// WithQuerySkillStore injects an IAgentDefinitionStore so RunSummary can be
// enriched with agent_name, agent_emoji, and credit_cap_per_session.
func WithQuerySkillStore(s store.IAgentDefinitionStore) StudentQueryOption {
	return func(svc *StudentQueryService) { svc.skillStore = s }
}

// WithQueryCreditStore injects a CreditStore so credits_used can be computed.
func WithQueryCreditStore(s store.CreditStore) StudentQueryOption {
	return func(svc *StudentQueryService) { svc.creditStore = s }
}

// ListRecentSessions returns the last N sessions for the learner, ordered by
// started_at DESC. limit ≤ 0 defaults to 5.
func (s *StudentQueryService) ListRecentSessions(ctx context.Context, userID uint, limit int) ([]*RunSummary, error) {
	if limit <= 0 {
		limit = 5
	}
	runs, err := s.runStore.ListByUser(ctx, userID, nil, limit)
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.ListRecentSessions: %w", err)
	}
	return s.toEnrichedSummaries(ctx, runs)
}

// ListAllHistorySessions returns all sessions for the learner in the last 30 days.
func (s *StudentQueryService) ListAllHistorySessions(ctx context.Context, userID uint) ([]*RunSummary, error) {
	since := time.Now().AddDate(0, 0, -30)
	runs, err := s.runStore.ListByUser(ctx, userID, &since, 500)
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.ListAllHistorySessions: %w", err)
	}
	return s.toEnrichedSummaries(ctx, runs)
}

// GetSessionSnapshot returns the latest run in the session (messages +
// compact_summary) for the learner-facing resume flow.
//
// Lookup is by agent_run.session_id (UUID string), NOT agent_run.id. The
// URL contract is /v1/sessions/:id/snapshot and the frontend passes the
// UUID surfaced via RunSummary.session_id. Picks the most recent run via
// ListBySession (ordered by created_at DESC) — today every session is 1:1
// with a run, but a multi-run resume in the future will naturally return
// the freshest state.
//
// Returns errno.ErrAgentRunNotFound if no runs match the session_id, and
// errno.ErrForbidden if the session belongs to a different user.
func (s *StudentQueryService) GetSessionSnapshot(ctx context.Context, userID uint, sessionID string) (*SessionSnapshot, error) {
	runs, _, err := s.runStore.ListBySession(ctx, sessionID, 0, 1)
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.GetSessionSnapshot list: %w", err)
	}
	if len(runs) == 0 {
		return nil, errno.ErrAgentRunNotFound
	}
	run := &runs[0]
	if run.UserID != userID {
		return nil, errno.ErrForbidden.SetMessage("access to another user's session is not allowed")
	}

	summaries, err := s.toEnrichedSummaries(ctx, []model.AgentRun{*run})
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.GetSessionSnapshot enrich: %w", err)
	}
	runSummary := *summaries[0]

	snap := &SessionSnapshot{
		Run:            runSummary,
		CompactSummary: run.CompactSummary,
	}
	// Fix 3: transform raw messages JSON into frontend-shaped AgentMessage array.
	snap.Messages = transformMessages(run.Messages, run.ID, run.StartedAt, run.EndedAt, run.Status, run.StateReason)
	return snap, nil
}

// RunDetail is the GET /v1/agent-runs/:id response. RunSummary fields are
// included so the frontend can read the same shape as list endpoints, plus
// `final_output` (extracted assistant text from the last turn) for the chat UI
// to render once the run completes.
type RunDetail struct {
	RunSummary
	FinalOutput string `json:"final_output,omitempty"`
}

// GetRun returns a single run, ownership-checked.
// Returns errno.ErrForbidden if the run belongs to a different user.
func (s *StudentQueryService) GetRun(ctx context.Context, userID uint, runID uint64) (*RunDetail, error) {
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || isNotFoundErr(err) {
			return nil, errno.ErrAgentRunNotFound
		}
		return nil, fmt.Errorf("StudentQueryService.GetRun get: %w", err)
	}
	if run.UserID != userID {
		return nil, errno.ErrForbidden.SetMessage("access to another user's run is not allowed")
	}

	summaries, err := s.toEnrichedSummaries(ctx, []model.AgentRun{*run})
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.GetRun enrich: %w", err)
	}
	return &RunDetail{
		RunSummary:  *summaries[0],
		FinalOutput: extractFinalAssistantText(run.Messages),
	}, nil
}

// extractFinalAssistantText pulls the last assistant message content out of
// agent_run.messages (a JSON array of {role,content} turns). Returns "" if
// nothing useful is present (still-running run, error before WriteTurn, etc.).
func extractFinalAssistantText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var turns []map[string]any
	if err := json.Unmarshal(raw, &turns); err != nil {
		return ""
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if role, _ := turns[i]["role"].(string); role == "assistant" {
			if content, ok := turns[i]["content"].(string); ok {
				return content
			}
		}
	}
	return ""
}

// WriteFeedback appends a learner's 👍/👎 + optional text to
// agent_run.terminal_metadata["feedback"] (v1 — simple and zero-schema).
// Verdict must be "up" or "down"; empty verdict is accepted (no-op validation here —
// controller must validate binding).
func (s *StudentQueryService) WriteFeedback(ctx context.Context, userID uint, runID uint64, req FeedbackRequest) error {
	// Ownership check first.
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || isNotFoundErr(err) {
			return errno.ErrAgentRunNotFound
		}
		return fmt.Errorf("StudentQueryService.WriteFeedback get: %w", err)
	}
	if run.UserID != userID {
		return errno.ErrForbidden.SetMessage("feedback can only be submitted for your own runs")
	}

	patch := map[string]interface{}{
		"feedback": map[string]interface{}{
			"verdict":      req.Verdict,
			"text":         req.Text,
			"submitted_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := s.runStore.MergeTerminalMetadata(ctx, runID, patch); err != nil {
		return fmt.Errorf("StudentQueryService.WriteFeedback merge: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Enrichment helpers (Fix 1 + Fix 2)
// ---------------------------------------------------------------------------

// toEnrichedSummaries converts a slice of AgentRun rows to enriched RunSummary
// using batch queries for agent_definitions and credit_transactions.
func (s *StudentQueryService) toEnrichedSummaries(ctx context.Context, runs []model.AgentRun) ([]*RunSummary, error) {
	out := make([]*RunSummary, len(runs))
	for i, r := range runs {
		rs := runToSummary(r)
		out[i] = &rs
	}
	if len(runs) == 0 {
		return out, nil
	}

	// --- Fix 2: batch-load agent_definitions for name/emoji/credit_cap ---
	defMap := make(map[uint64]*model.AgentDefinition)
	if s.skillStore != nil {
		defIDs := make([]uint64, 0, len(runs))
		seen := make(map[uint64]bool)
		for _, r := range runs {
			if r.AgentDefinitionID != 0 && !seen[r.AgentDefinitionID] {
				defIDs = append(defIDs, r.AgentDefinitionID)
				seen[r.AgentDefinitionID] = true
			}
		}
		for _, id := range defIDs {
			def, err := s.skillStore.GetByIDIncludeInactive(ctx, id)
			if err == nil && def != nil {
				defMap[id] = def
			}
			// silently skip missing / soft-deleted defs
		}
	}

	// --- Fix 1: batch-load credit sums by reservation_id ---
	creditsByReservation := make(map[uint64]int64)
	if s.creditStore != nil {
		rsvIDs := make([]uint64, 0, len(runs))
		for _, r := range runs {
			if r.ReservationID != nil && *r.ReservationID != 0 {
				rsvIDs = append(rsvIDs, *r.ReservationID)
			}
		}
		if len(rsvIDs) > 0 {
			sums, err := s.creditStore.SumByReservationIDs(ctx, rsvIDs)
			if err == nil {
				creditsByReservation = sums
			}
			// graceful degrade: if error, leave creditsByReservation empty → credits_used=0
		}
	}

	// Apply enrichment to each summary.
	for i, r := range runs {
		var creditsUsed int64
		if r.ReservationID != nil {
			creditsUsed = creditsByReservation[*r.ReservationID]
		}

		budget := defaultCreditBudget
		agentName := ""
		agentEmoji := ""
		if def, ok := defMap[r.AgentDefinitionID]; ok {
			if def.CreditCapPerSession != nil && *def.CreditCapPerSession > 0 {
				budget = int(*def.CreditCapPerSession)
			}
			agentName = def.Name
			// AgentDefinition doesn't have an emoji field in the model; use "" for now.
			// TODO: add emoji/icon field to agent_definition when the model gets it.
			_ = agentEmoji
		}

		enrichSummary(out[i], runEnrichment{
			agentName:     agentName,
			agentEmoji:    agentEmoji,
			creditsUsed:   int(creditsUsed),
			creditsBudget: budget,
		})
	}

	return out, nil
}

// ---------------------------------------------------------------------------
// Fix 3: SessionSnapshot.Messages transform
// ---------------------------------------------------------------------------

// agentMessage is the frontend-shaped message type.
// Discriminated by Type: 'user' | 'assistant' | 'final_answer'.
type agentMessage struct {
	ID        string `json:"id"`
	Type      string `json:"type"`               // 'user' | 'assistant' | 'final_answer'
	Text      string `json:"text,omitempty"`     // for type='user'
	Markdown  string `json:"markdown,omitempty"` // for type='assistant' | 'final_answer'
	RunID     uint64 `json:"run_id,omitempty"`   // for type='final_answer'
	Timestamp string `json:"timestamp"`          // RFC3339
}

// transformMessages converts the raw [{role,content}] turn array stored in
// agent_run.messages into the frontend AgentMessage discriminated union:
//   - role='user'      → {type:'user', text:content}
//   - role='assistant' (non-last or non-terminal) → {type:'assistant', markdown:content}
//   - role='assistant' last turn AND run is terminal-with-output → {type:'final_answer', markdown:content, run_id}
//
// Timestamps are all set to startedAt (pragmatic v1; frontend doesn't need precision here).
// Returns an empty slice (not nil) when there are no turns.
func transformMessages(raw []byte, runID uint64, startedAt time.Time, endedAt *time.Time, status, stateReason string) []agentMessage {
	if len(raw) == 0 {
		return []agentMessage{}
	}
	var turns []map[string]any
	if err := json.Unmarshal(raw, &turns); err != nil {
		return []agentMessage{}
	}
	if len(turns) == 0 {
		return []agentMessage{}
	}

	// Determine if the run is terminal-with-output (for final_answer promotion).
	isTerminalSuccess := status == "terminated" && stateReason == "completed"

	// Find the index of the last assistant turn.
	lastAssistantIdx := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if role, _ := turns[i]["role"].(string); role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	ts := startedAt.UTC().Format(time.RFC3339)

	msgs := make([]agentMessage, 0, len(turns))
	for i, turn := range turns {
		role, _ := turn["role"].(string)
		content, _ := turn["content"].(string)

		msg := agentMessage{
			ID:        uuid.New().String(),
			Timestamp: ts,
		}

		switch role {
		case "user":
			msg.Type = "user"
			msg.Text = content
		case "assistant":
			if i == lastAssistantIdx && isTerminalSuccess && content != "" {
				msg.Type = "final_answer"
				msg.Markdown = content
				msg.RunID = runID
			} else {
				msg.Type = "assistant"
				msg.Markdown = content
			}
		default:
			// Skip system / tool turns — frontend doesn't render them.
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs
}
