package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// RunDTO is a flattened view of model.AgentRun for admin responses.
type RunDTO struct {
	ID                      uint64          `json:"id"`
	UserID                  uint            `json:"user_id"`
	SessionID               string          `json:"session_id"`
	AgentDefinitionID       uint64          `json:"agent_definition_id,omitempty"`
	Status                  string          `json:"status"`
	StateReason             string          `json:"state_reason,omitempty"`
	TerminalMetadata        json.RawMessage `json:"terminal_metadata,omitempty"`
	ReservationID           *uint64         `json:"reservation_id,omitempty"`
	StartedAt               time.Time       `json:"started_at"`
	EndedAt                 *time.Time      `json:"ended_at,omitempty"`
	CancellationRequestedAt *time.Time      `json:"cancellation_requested_at,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
}

func runToDTO(r model.AgentRun) RunDTO {
	dto := RunDTO{
		ID:                      r.ID,
		UserID:                  r.UserID,
		SessionID:               r.SessionID,
		AgentDefinitionID:       r.AgentDefinitionID,
		Status:                  r.Status,
		StateReason:             r.StateReason,
		ReservationID:           r.ReservationID,
		StartedAt:               r.StartedAt,
		EndedAt:                 r.EndedAt,
		CancellationRequestedAt: r.CancellationRequestedAt,
		CreatedAt:               r.CreatedAt,
	}
	if r.TerminalMetadata != nil {
		dto.TerminalMetadata = json.RawMessage(r.TerminalMetadata)
	}
	return dto
}

// IAgentAdminService defines the admin-facing agent_run operations (M-C3b + M-C4a).
type IAgentAdminService interface {
	// CancelByAdmin sets cancellation_requested_at and writes admin metadata.
	// Triggers in-memory Cancel if the run is still registered in the AgentRunner.
	// Returns errno.ErrAgentRunNotFound (404) if id doesn't exist.
	// Returns errno.ErrAgentRunNotCancellable (409) if the run is already terminal.
	CancelByAdmin(ctx context.Context, runID uint64, adminUserID uint) error

	// ListByStatus returns runs filtered by status and optionally by parent_user_id,
	// with pagination (M-C4a).
	ListByStatus(ctx context.Context, parentUserID uint, status string, page, pageSize int) ([]RunDTO, int64, error)
}

// AgentAdminService implements IAgentAdminService.
type AgentAdminService struct {
	runStore store.IAgentRunStore
	runner   AgentRunner // may be nil in tests
}

// NewAgentAdminService constructs an AgentAdminService.
// runner is used to call Cancel on in-memory run; may be nil (cancel won't fire
// but DB is still updated — run will detect cancellation_requested_at on next
// store poll if we add that in future; for now the context cancel is best-effort).
func NewAgentAdminService(runStore store.IAgentRunStore, runner AgentRunner) *AgentAdminService {
	return &AgentAdminService{runStore: runStore, runner: runner}
}

// CancelByAdmin implements M-C3b force-cancel.
func (s *AgentAdminService) CancelByAdmin(ctx context.Context, runID uint64, adminUserID uint) error {
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrAgentRunNotFound
		}
		// Check for agentRunStore wrapped error containing "no row matched"
		if isNotFoundErr(err) {
			return errno.ErrAgentRunNotFound
		}
		return fmt.Errorf("AgentAdminService.CancelByAdmin get: %w", err)
	}

	// Only allow cancel if the run is in a non-terminal state.
	if isTerminalStatus(run.Status) {
		return errno.ErrAgentRunNotCancellable
	}

	// Build terminal_metadata for admin cancel. We intentionally do NOT set
	// agent_run.status to a new TerminalReason here — we use the existing
	// TerminalAbortedStreaming path triggered by AbortController context cancel.
	// terminal_metadata records the admin attribution.
	type adminCancelMeta struct {
		CancelledBy string `json:"cancelled_by"`
		AdminUserID uint   `json:"admin_user_id"`
		CancelledAt string `json:"cancelled_at"`
	}
	meta := adminCancelMeta{
		CancelledBy: "admin",
		AdminUserID: adminUserID,
		CancelledAt: time.Now().UTC().Format(time.RFC3339),
	}
	metaJSON, _ := json.Marshal(meta)

	if err := s.runStore.SetCancellationRequested(ctx, runID, datatypes.JSON(metaJSON)); err != nil {
		return fmt.Errorf("AgentAdminService.CancelByAdmin set: %w", err)
	}

	// Trigger in-memory context cancel (best-effort — run may have finished already).
	if s.runner != nil {
		s.runner.Cancel(runID)
	}
	return nil
}

// ListByStatus implements M-C4a agent_run admin listing.
func (s *AgentAdminService) ListByStatus(ctx context.Context, parentUserID uint, status string, page, pageSize int) ([]RunDTO, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	runs, total, err := s.runStore.ListByParentUserIDAndStatus(ctx, parentUserID, status, offset, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("AgentAdminService.ListByStatus: %w", err)
	}
	dtos := make([]RunDTO, len(runs))
	for i, r := range runs {
		dtos[i] = runToDTO(r)
	}
	return dtos, total, nil
}

// isTerminalStatus returns true when the run status represents a completed/failed run.
func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "error":
		return true
	}
	return false
}

// isNotFoundErr returns true for "no row matched" errors from agentRunStore.Get.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sub := range []string{"no row matched", "record not found"} {
		if len(msg) >= len(sub) {
			for i := 0; i <= len(msg)-len(sub); i++ {
				if msg[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
