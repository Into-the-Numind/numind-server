package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// RunSummary is a lightweight view of model.AgentRun returned by list endpoints.
// Full messages are omitted to keep list payloads small.
type RunSummary struct {
	ID                uint64     `json:"id"`
	UserID            uint       `json:"user_id"`
	SessionID         string     `json:"session_id"`
	AgentDefinitionID uint64     `json:"agent_definition_id,omitempty"`
	Status            string     `json:"status"`
	StateReason       string     `json:"state_reason,omitempty"`
	CompactSummary    string     `json:"compact_summary,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

func runToSummary(r model.AgentRun) RunSummary {
	return RunSummary{
		ID:                r.ID,
		UserID:            r.UserID,
		SessionID:         r.SessionID,
		AgentDefinitionID: r.AgentDefinitionID,
		Status:            r.Status,
		StateReason:       r.StateReason,
		CompactSummary:    r.CompactSummary,
		StartedAt:         r.StartedAt,
		EndedAt:           r.EndedAt,
		CreatedAt:         r.CreatedAt,
	}
}

// SessionSnapshot is returned by GetSessionSnapshot for resume flows.
// Messages is the raw turn-level JSON stored in agent_run.messages.
// CompactSummary is the latest compact summary (may be empty).
type SessionSnapshot struct {
	Run            RunSummary  `json:"run"`
	Messages       interface{} `json:"messages"`        // raw JSON array from agent_run
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
	runStore  store.IAgentRunStore
	userStore store.UserStore
}

// NewStudentQueryService constructs a StudentQueryService.
func NewStudentQueryService(runStore store.IAgentRunStore, userStore store.UserStore) *StudentQueryService {
	return &StudentQueryService{runStore: runStore, userStore: userStore}
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
	return toSummaries(runs), nil
}

// ListAllHistorySessions returns all sessions for the learner in the last 30 days.
func (s *StudentQueryService) ListAllHistorySessions(ctx context.Context, userID uint) ([]*RunSummary, error) {
	since := time.Now().AddDate(0, 0, -30)
	runs, err := s.runStore.ListByUser(ctx, userID, &since, 500)
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.ListAllHistorySessions: %w", err)
	}
	return toSummaries(runs), nil
}

// GetSessionSnapshot returns the full run (messages + compact_summary) for resume.
// Returns errno.ErrForbidden if the run belongs to a different user.
func (s *StudentQueryService) GetSessionSnapshot(ctx context.Context, userID uint, runID uint64) (*SessionSnapshot, error) {
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || isNotFoundErr(err) {
			return nil, errno.ErrAgentRunNotFound
		}
		return nil, fmt.Errorf("StudentQueryService.GetSessionSnapshot get: %w", err)
	}
	if run.UserID != userID {
		return nil, errno.ErrForbidden.SetMessage("access to another user's session is not allowed")
	}
	snap := &SessionSnapshot{
		Run:            runToSummary(*run),
		CompactSummary: run.CompactSummary,
	}
	// Expose raw messages JSON as interface{} so it round-trips through gin without
	// double-encoding.
	if len(run.Messages) > 0 {
		var msgs interface{}
		if err := run.Messages.UnmarshalJSON(run.Messages); err == nil {
			snap.Messages = msgs
		} else {
			// Fallback: pass through as raw bytes.
			snap.Messages = run.Messages
		}
	}
	return snap, nil
}

// GetRun returns a single run, ownership-checked.
// Returns errno.ErrForbidden if the run belongs to a different user.
func (s *StudentQueryService) GetRun(ctx context.Context, userID uint, runID uint64) (*model.AgentRun, error) {
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
	return run, nil
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
// helpers
// ---------------------------------------------------------------------------

func toSummaries(runs []model.AgentRun) []*RunSummary {
	out := make([]*RunSummary, len(runs))
	for i, r := range runs {
		s := runToSummary(r)
		out[i] = &s
	}
	return out
}
