package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// PreparedStreamRun is the pre-created running row and request payload for a
// stream execution that will be started by a separate executor.
type PreparedStreamRun struct {
	RunID     uint64
	SessionID string
	UserID    uint
	Request   CreateRunRequest
}

// PrepareStreamRun validates a stream request and pre-creates its running row.
func (s *StudentRunService) PrepareStreamRun(ctx context.Context, userID uint, req CreateRunRequest) (*PreparedStreamRun, error) {
	if req.hasNoSendable() {
		return nil, errno.ErrBind.SetMessage("message or attachment is required")
	}
	if err := s.ensureAttachmentsReady(ctx, userID, req.AttachmentIDs); err != nil {
		return nil, err
	}

	// Validate agent definition. The returned ad is intentionally unused - its
	// fields (ToolFlags / ParentUserID) are not stored on agent_run; ToolFlags
	// are re-resolved from skillStore inside RunStream, and ParentUserID acts as
	// an access guard inside resolveDefinition itself. Calling for the side
	// effect (validation + error propagation) is sufficient.
	if _, err := s.resolveDefinition(ctx, userID, req.AgentDefinitionID); err != nil {
		return nil, err
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	var isPinned bool
	var sessionName string
	if sessionID != "" {
		runs, _, listErr := s.runStore.ListBySession(ctx, sessionID, 0, 1)
		if listErr == nil && len(runs) > 0 {
			isPinned = runs[0].IsPinned
			sessionName = runs[0].SessionName
		}
	}

	_, _, displayAtts := s.composeAttachmentInput(ctx, userID, req)

	startedAt := time.Now()
	preRun := &model.AgentRun{
		UserID:            userID,
		SessionID:         sessionID,
		AgentDefinitionID: req.AgentDefinitionID,
		Status:            "running",
		Messages:          initialDisplayMessagesJSON(req.Message, displayAtts),
		StartedAt:         startedAt,
		UseCompactV2:      true,
		IsTest:            req.IsTest,
		IsPinned:          isPinned,
		SessionName:       sessionName,
	}
	if err := s.runStore.Create(ctx, preRun); err != nil {
		return nil, fmt.Errorf("StudentRunService.PrepareStreamRun pre-create row: %w", err)
	}

	req.SessionID = sessionID

	return &PreparedStreamRun{
		RunID:     preRun.ID,
		SessionID: preRun.SessionID,
		UserID:    userID,
		Request:   req,
	}, nil
}
