package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/externalaction"
	"numind-server/internal/pkg/model"
)

// IAgentRunStore 定义 agent_run 表的存取接口。
type IAgentRunStore interface {
	Create(ctx context.Context, run *model.AgentRun) error
	Get(ctx context.Context, id uint64) (*model.AgentRun, error)
	UpdateState(ctx context.Context, id uint64, status, stateReason string, endedAt *time.Time) error
	WriteTurn(ctx context.Context, id uint64, messages json.RawMessage) error // turn 级整体覆写
	ListBySession(ctx context.Context, sessionID string, offset, limit int) ([]model.AgentRun, int64, error)
	// UpdateTerminalMetadata 写入 terminal_metadata JSON 字段。
	// 用途：#12 BudgetGate.writeTerminalMetadata 写 budget_dimension 等元数据，
	// #13 compliance 后续追加 compliance_block_reason 等。
	// RowsAffected==0 时报错（认为 id 不存在）。
	UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error
	// SetCancellationRequested marks agent_run.cancellation_requested_at = NOW()
	// and writes terminal_metadata (merged with existing). Used by admin force-cancel (M-C3b).
	// RowsAffected==0 means the run was not found.
	SetCancellationRequested(ctx context.Context, id uint64, metadata datatypes.JSON) error
	// ListByParentUserIDAndStatus returns runs whose agent_definition.parent_user_id = parentUserID
	// and agent_run.status = status (M-C4a admin listing).
	// parentUserID=0 skips the parent filter (global cross-tenant view).
	ListByParentUserIDAndStatus(ctx context.Context, parentUserID uint, status string, offset, limit int) ([]model.AgentRun, int64, error)
	// ListByUser returns agent_run rows for a specific user, ordered by started_at DESC.
	// sinceTime is optional — zero value means no time filter (returns all rows up to limit).
	// Used by student-facing recent/history endpoints (#14 follow-up ALPHA).
	ListByUser(ctx context.Context, userID uint, sinceTime *time.Time, limit int) ([]model.AgentRun, error)
	// MergeTerminalMetadata merges a JSON patch into agent_run.terminal_metadata.
	// Reads current value, merges key-by-key (shallow), writes back.
	// RowsAffected==0 returns error.
	// Used by finalizeRun() to record error_message/error_class on the run's
	// terminal (error/extend) path.
	MergeTerminalMetadata(ctx context.Context, id uint64, patch map[string]interface{}) error
	// UpdatePendingQuestion writes the ask_user_question payload JSON to
	// agent_run.pending_question_json and sets pending_question_at = NOW() and
	// state_reason = "waiting_for_user_choice". Called by runner.go yield handler (T4).
	UpdatePendingQuestion(ctx context.Context, id uint64, payloadJSON []byte) error
	// ClearPendingQuestion nulls out pending_question_json / pending_question_at
	// and resets state_reason to "running". Called by Answer biz method (T4).
	ClearPendingQuestion(ctx context.Context, id uint64) error
	// AppendUserMessage appends a user-role message JSON object to agent_run.messages.
	// The message is appended as {"role":"user","content":<content>}.
	// RowsAffected==0 returns error. Used by Answer biz method (T4).
	AppendUserMessage(ctx context.Context, id uint64, content string) error
	// AnswerAndClear atomically appends a pre-built answer turn to
	// agent_run.messages AND clears pending_question_json / pending_question_at /
	// state_reason in a single DB transaction. The biz layer builds the full turn
	// (role=user + content + embedded question_answer for issue1 reconstruction),
	// so the store appends it verbatim. Replaces the separate AppendUserMessage +
	// ClearPendingQuestion pair used in earlier iterations.
	AnswerAndClear(ctx context.Context, id uint64, turn json.RawMessage) error
	UpdateSessionPinned(ctx context.Context, sessionID string, isPinned bool) error
	UpdateSessionName(ctx context.Context, sessionID string, name string) error
	// UpdateSessionNameIfEmpty atomically names all runs of a session ONLY while
	// their session_name is still empty (WHERE session_id=? AND session_name='').
	// Used by adaptive-session-titles auto-titling so a concurrent manual rename
	// during title generation is never clobbered. Returns updated=true when ≥1 row
	// changed; false (no error) when the name was already set (skip).
	UpdateSessionNameIfEmpty(ctx context.Context, sessionID string, name string) (updated bool, err error)
	UpdateSessionDeleted(ctx context.Context, sessionID string, isDeleted bool) error
}

// IExternalActionWriter is the narrow capability used by agent runners to
// persist restart-safe external waits without expanding every IAgentRunStore
// fake in the codebase.
type IExternalActionWriter interface {
	UpdatePendingExternalAction(ctx context.Context, runID uint64, payloadJSON []byte) error
}

// IExternalToolResumer is the narrow store capability used after an external
// operation completes. It atomically claims the pending wait and appends the
// original tool call's result. bool is true only for the callback that won the
// claim; duplicate callbacks return false without appending another turn.
type IExternalToolResumer interface {
	ResumeExternalTool(ctx context.Context, runID uint64, operationID, toolCallID string, resultTurn json.RawMessage) (bool, error)
}

// IExternalToolResumeLease extends the atomic result claim with a durable
// runner-start handshake. The pending identity is retained while synchronous
// runner preflight owns the lease; Complete acknowledges a usable runner and
// clears it, while Release makes the same result immediately reclaimable.
type IExternalToolResumeLease interface {
	IExternalToolResumer
	ClaimExternalToolResume(ctx context.Context, runID uint64, operationID, toolCallID string, resultTurn json.RawMessage) (leaseToken string, claimed bool, err error)
	CompleteExternalToolResume(ctx context.Context, runID uint64, operationID, toolCallID, leaseToken string) error
	ReleaseExternalToolResume(ctx context.Context, runID uint64, operationID, toolCallID, leaseToken string) error
}

type agentRunStore struct {
	db *gorm.DB
}

func newAgentRunStore(db *gorm.DB) IAgentRunStore {
	return &agentRunStore{db: db}
}

var _ IAgentRunStore = (*agentRunStore)(nil)
var _ IExternalActionWriter = (*agentRunStore)(nil)
var _ IExternalToolResumer = (*agentRunStore)(nil)
var _ IExternalToolResumeLease = (*agentRunStore)(nil)

func (s *agentRunStore) Create(ctx context.Context, run *model.AgentRun) error {
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return fmt.Errorf("agentRunStore.Create: %w", err)
	}
	return nil
}

func (s *agentRunStore) Get(ctx context.Context, id uint64) (*model.AgentRun, error) {
	var run model.AgentRun
	if err := s.db.WithContext(ctx).First(&run, id).Error; err != nil {
		return nil, fmt.Errorf("agentRunStore.Get(id=%d): %w", id, err)
	}
	return &run, nil
}

func (s *agentRunStore) UpdateState(ctx context.Context, id uint64, status, stateReason string, endedAt *time.Time) error {
	updates := map[string]interface{}{
		"status":       status,
		"state_reason": stateReason,
	}
	if endedAt != nil {
		updates["ended_at"] = *endedAt
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdateState(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.UpdateState: no row matched id=%d", id)
	}
	return nil
}

func (s *agentRunStore) WriteTurn(ctx context.Context, id uint64, messages json.RawMessage) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Update("messages", datatypes.JSON(messages))
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.WriteTurn(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.WriteTurn: no row matched id=%d", id)
	}
	return nil
}

// UpdateTerminalMetadata 写入 terminal_metadata JSON 字段。
// 用途：#12 BudgetGate.writeTerminalMetadata 写 budget_dimension 等元数据，
// #13 compliance 后续追加 compliance_block_reason 等。
// RowsAffected==0 时报错（认为 id 不存在）。
func (s *agentRunStore) UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Update("terminal_metadata", metadata)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdateTerminalMetadata(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.UpdateTerminalMetadata: no row matched id=%d", id)
	}
	return nil
}

// SetCancellationRequested sets cancellation_requested_at = NOW() and merges terminal_metadata.
func (s *agentRunStore) SetCancellationRequested(ctx context.Context, id uint64, metadata datatypes.JSON) error {
	updates := map[string]interface{}{
		"cancellation_requested_at": time.Now(),
	}
	if metadata != nil {
		updates["terminal_metadata"] = metadata
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.SetCancellationRequested(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.SetCancellationRequested: no row matched id=%d", id)
	}
	return nil
}

// ListByParentUserIDAndStatus joins agent_run ⋈ agent_definition on agent_definition_id
// to filter by parent_user_id, then filters agent_run.status.
// parentUserID=0 skips the parent filter (cross-tenant view for super-admin).
func (s *agentRunStore) ListByParentUserIDAndStatus(ctx context.Context, parentUserID uint, status string, offset, limit int) ([]model.AgentRun, int64, error) {
	base := s.db.WithContext(ctx).Model(&model.AgentRun{})
	if parentUserID != 0 {
		// LEFT JOIN: historical runs with agent_definition_id=0 have no join row.
		// Only return runs with a matching agent_definition row for the given parent.
		base = base.
			Joins("JOIN agent_definition ON agent_definition.id = agent_run.agent_definition_id").
			Where("agent_definition.parent_user_id = ?", parentUserID)
	}
	if status != "" {
		base = base.Where("agent_run.status = ?", status)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListByParentUserIDAndStatus.Count: %w", err)
	}

	var runs []model.AgentRun
	if limit <= 0 {
		limit = 20
	}
	if err := base.Offset(offset).Limit(limit).Order("agent_run.started_at DESC").Find(&runs).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListByParentUserIDAndStatus.Find: %w", err)
	}
	return runs, total, nil
}

// ListByUser returns the latest run for each non-deleted session for a specific user,
// ordered by is_pinned DESC and started_at DESC.
// If sinceTime is non-nil, only runs with started_at >= sinceTime are evaluated.
// limit <= 0 defaults to 20.
func (s *agentRunStore) ListByUser(ctx context.Context, userID uint, sinceTime *time.Time, limit int) ([]model.AgentRun, error) {
	if limit <= 0 {
		limit = 20
	}

	// 1. 子查询：分组取得该用户所有未逻辑删除会话的最大 started_at
	subQuery := s.db.Model(&model.AgentRun{}).
		Select("session_id, MAX(started_at) as max_started_at").
		Where("user_id = ? AND is_deleted = false", userID)

	if sinceTime != nil {
		subQuery = subQuery.Where("started_at >= ?", *sinceTime)
	}
	subQuery = subQuery.Group("session_id")

	var runs []model.AgentRun
	// 2. 主查询：JOIN 子查询，并执行 order 排序
	err := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Joins("JOIN (?) as latest ON agent_run.session_id = latest.session_id AND agent_run.started_at = latest.max_started_at", subQuery).
		Where("agent_run.user_id = ? AND agent_run.is_deleted = false", userID).
		Order("agent_run.is_pinned DESC, agent_run.started_at DESC").
		Limit(limit).
		Find(&runs).Error

	if err != nil {
		return nil, fmt.Errorf("agentRunStore.ListByUser(userID=%d): %w", userID, err)
	}
	return runs, nil
}

// MergeTerminalMetadata reads the current terminal_metadata, shallow-merges patch keys into
// it, and writes the result back. RowsAffected==0 returns an error.
func (s *agentRunStore) MergeTerminalMetadata(ctx context.Context, id uint64, patch map[string]interface{}) error {
	// Read current value.
	var run model.AgentRun
	if err := s.db.WithContext(ctx).Select("terminal_metadata").First(&run, id).Error; err != nil {
		return fmt.Errorf("agentRunStore.MergeTerminalMetadata get(id=%d): %w", id, err)
	}

	// Merge: start from existing JSON (or empty map), overlay patch keys.
	merged := make(map[string]interface{})
	if len(run.TerminalMetadata) > 0 && string(run.TerminalMetadata) != "null" {
		if err := json.Unmarshal(run.TerminalMetadata, &merged); err != nil {
			// If existing value is not a JSON object, replace entirely.
			merged = make(map[string]interface{})
		}
	}
	for k, v := range patch {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("agentRunStore.MergeTerminalMetadata marshal(id=%d): %w", id, err)
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Update("terminal_metadata", datatypes.JSON(b))
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.MergeTerminalMetadata update(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.MergeTerminalMetadata: no row matched id=%d", id)
	}
	return nil
}

// UpdatePendingQuestion stores the ask_user_question yield payload.
// Sets pending_question_json, pending_question_at, and state_reason.
func (s *agentRunStore) UpdatePendingQuestion(ctx context.Context, id uint64, payloadJSON []byte) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"pending_question_json":        datatypes.JSON(payloadJSON),
			"pending_question_at":          now,
			"pending_external_action_json": nil,
			"pending_external_action_at":   nil,
			"state_reason":                 "waiting_for_user_choice",
		})
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdatePendingQuestion(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.UpdatePendingQuestion: no row matched id=%d", id)
	}
	return nil
}

// UpdatePendingExternalAction stores only the allowlisted restart identity for
// an external wait. The strict decoder rejects URLs, secrets, device codes, and
// any future field until it is deliberately reviewed and allowlisted.
func (s *agentRunStore) UpdatePendingExternalAction(ctx context.Context, id uint64, payloadJSON []byte) error {
	canonicalJSON, err := externalaction.CanonicalJSON(payloadJSON)
	if err != nil {
		return fmt.Errorf("agentRunStore.UpdatePendingExternalAction: invalid payload: %w", err)
	}
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"pending_external_action_json": datatypes.JSON(canonicalJSON),
			"pending_external_action_at":   now,
			"pending_question_json":        nil,
			"pending_question_at":          nil,
			"state_reason":                 "waiting_for_user_choice",
		})
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdatePendingExternalAction(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.UpdatePendingExternalAction: no row matched id=%d", id)
	}
	return nil
}

const (
	maxExternalToolResultBytes   = 1 << 20
	externalResumeStartingPrefix = "ext_resume:"
	externalResumeStateReady     = "external_resume_ready"
	externalResumeLeaseDuration  = 30 * time.Second
	externalOperationIDExtraKey  = "external_operation_id"
)

var errExternalResumeLostClaim = errors.New("external tool resume claim was already consumed")

// ResumeExternalTool atomically replaces a durable external wait with the
// original tool call's result. The transaction locks the run on databases that
// support SELECT FOR UPDATE; the conditional UPDATE is the cross-database
// compare-and-swap backstop used by SQLite tests and concurrent callbacks.
func (s *agentRunStore) ResumeExternalTool(
	ctx context.Context,
	runID uint64,
	operationID string,
	toolCallID string,
	resultTurn json.RawMessage,
) (bool, error) {
	leaseToken, claimed, err := s.ClaimExternalToolResume(ctx, runID, operationID, toolCallID, resultTurn)
	if err != nil || !claimed {
		return claimed, err
	}
	if err := s.CompleteExternalToolResume(ctx, runID, operationID, toolCallID, leaseToken); err != nil {
		return false, err
	}
	return true, nil
}

// ClaimExternalToolResume returns a fencing token only to the callback that
// owns the current runner-start lease. Compatibility callers may continue to
// use ResumeExternalTool's bool, but production continuation must complete or
// release through this tokenized lifecycle.
func (s *agentRunStore) ClaimExternalToolResume(
	ctx context.Context,
	runID uint64,
	operationID string,
	toolCallID string,
	resultTurn json.RawMessage,
) (string, bool, error) {
	operationID = strings.TrimSpace(operationID)
	toolCallID = strings.TrimSpace(toolCallID)
	if runID == 0 || operationID == "" || toolCallID == "" {
		return "", false, fmt.Errorf("agentRunStore.ResumeExternalTool: run, operation, and tool call identity are required")
	}
	canonicalResult, err := canonicalExternalToolResult(resultTurn)
	if err != nil {
		return "", false, fmt.Errorf("agentRunStore.ResumeExternalTool: invalid result: %w", err)
	}

	// SQLite can return SQLITE_BUSY when several deferred transactions read the
	// same row before upgrading to a writer. Production MySQL serializes on FOR
	// UPDATE; bounded retry preserves the same observable single-winner contract
	// in tests without weakening the transactional claim.
	for attempt := 0; attempt < 20; attempt++ {
		token, claimed, txErr := s.resumeExternalToolOnce(ctx, runID, operationID, toolCallID, canonicalResult)
		if txErr == nil {
			return token, claimed, nil
		}
		if errors.Is(txErr, errExternalResumeLostClaim) {
			claimed, verifyErr := s.externalResumeAlreadyApplied(ctx, runID, operationID, toolCallID, canonicalResult)
			return "", claimed, verifyErr
		}
		if !isSQLiteBusy(txErr) || ctx.Err() != nil {
			return "", false, txErr
		}
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	return "", false, fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): database remained busy", runID)
}

func (s *agentRunStore) resumeExternalToolOnce(
	ctx context.Context,
	runID uint64,
	operationID string,
	toolCallID string,
	canonicalResult json.RawMessage,
) (string, bool, error) {
	leaseToken, err := newExternalResumeLeaseToken()
	if err != nil {
		return "", false, err
	}
	claimed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.AgentRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, runID).Error; err != nil {
			return fmt.Errorf("agentRunStore.ResumeExternalTool get(id=%d): %w", runID, err)
		}
		if run.CancellationRequestedAt != nil || run.IsDeleted {
			return nil
		}
		if !hasJSONValue(run.PendingExternalActionJSON) {
			already, checkErr := transcriptHasExactToolResult(run.Messages, operationID, toolCallID, canonicalResult)
			if checkErr != nil {
				return fmt.Errorf("agentRunStore.ResumeExternalTool existing result(id=%d): %w", runID, checkErr)
			}
			if already {
				return nil
			}
			return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): no pending external wait or matching prior result", runID)
		}
		pending, parseErr := externalaction.Parse(run.PendingExternalActionJSON)
		if parseErr != nil {
			return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): corrupt pending external action: %w", runID, parseErr)
		}
		if pending.OperationID != operationID || pending.ToolCallID != toolCallID {
			return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): pending operation/tool identity mismatch", runID)
		}

		var turns []json.RawMessage
		if err := json.Unmarshal(run.Messages, &turns); err != nil {
			return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): corrupt transcript: %w", runID, err)
		}
		if turns == nil && !bytes.Equal(bytes.TrimSpace(run.Messages), []byte("[]")) {
			return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): transcript must be a JSON array", runID)
		}
		if err := validateExternalResumeTranscript(turns); err != nil {
			return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): corrupt transcript: %w", runID, err)
		}
		now := time.Now()
		startingReason := externalResumeStartingPrefix + leaseToken
		updates := map[string]interface{}{
			"status":                     "running",
			"state_reason":               startingReason,
			"pending_external_action_at": now,
			"pending_question_json":      nil,
			"pending_question_at":        nil,
			"ended_at":                   nil,
		}

		switch {
		case run.StateReason == "waiting_for_user_choice":
			if run.Status != "terminated" {
				return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): waiting run has status %q", runID, run.Status)
			}
			already, checkErr := transcriptHasExactToolResult(run.Messages, operationID, toolCallID, canonicalResult)
			if checkErr != nil {
				return fmt.Errorf("agentRunStore.ResumeExternalTool existing result(id=%d): %w", runID, checkErr)
			}
			if already {
				return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): waiting run already contains a consumed result", runID)
			}
			toolMessage := schema.ToolMessage(string(canonicalResult), toolCallID)
			toolMessage.Extra = map[string]any{externalOperationIDExtraKey: operationID}
			toolTurn, marshalErr := json.Marshal(toolMessage)
			if marshalErr != nil {
				return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): marshal tool result: %w", runID, marshalErr)
			}
			turns = append(turns, json.RawMessage(toolTurn))
			newMessages, marshalErr := json.Marshal(turns)
			if marshalErr != nil {
				return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): marshal transcript: %w", runID, marshalErr)
			}
			updates["messages"] = datatypes.JSON(newMessages)
		case run.StateReason == externalResumeStateReady:
			if run.Status != "terminated" {
				return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): ready result has status %q", runID, run.Status)
			}
			already, checkErr := transcriptHasExactToolResult(run.Messages, operationID, toolCallID, canonicalResult)
			if checkErr != nil || !already {
				if checkErr == nil {
					checkErr = fmt.Errorf("durable result is missing")
				}
				return fmt.Errorf("agentRunStore.ResumeExternalTool ready result(id=%d): %w", runID, checkErr)
			}
		case strings.HasPrefix(run.StateReason, externalResumeStartingPrefix):
			if run.Status != "running" {
				return fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): starting lease has status %q", runID, run.Status)
			}
			already, checkErr := transcriptHasExactToolResult(run.Messages, operationID, toolCallID, canonicalResult)
			if checkErr != nil || !already {
				if checkErr == nil {
					checkErr = fmt.Errorf("durable result is missing")
				}
				return fmt.Errorf("agentRunStore.ResumeExternalTool starting result(id=%d): %w", runID, checkErr)
			}
			if run.PendingExternalActionAt != nil && now.Sub(*run.PendingExternalActionAt) < externalResumeLeaseDuration {
				return nil
			}
		default:
			return fmt.Errorf(
				"agentRunStore.ResumeExternalTool(id=%d): unexpected run state status=%q reason=%q",
				runID, run.Status, run.StateReason,
			)
		}

		result := tx.Model(&model.AgentRun{}).
			Where(
				"id = ? AND status = ? AND state_reason = ? AND cancellation_requested_at IS NULL AND is_deleted = ? AND pending_external_action_json = ?",
				runID, run.Status, run.StateReason, false, string(run.PendingExternalActionJSON),
			).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("agentRunStore.ResumeExternalTool update(id=%d): %w", runID, result.Error)
		}
		if result.RowsAffected != 1 {
			return errExternalResumeLostClaim
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return "", claimed, err
	}
	return leaseToken, true, nil
}

func (s *agentRunStore) externalResumeAlreadyApplied(
	ctx context.Context,
	runID uint64,
	operationID string,
	toolCallID string,
	canonicalResult json.RawMessage,
) (bool, error) {
	run, err := s.Get(ctx, runID)
	if err != nil {
		return false, err
	}
	if run.CancellationRequestedAt != nil || run.IsDeleted {
		return false, nil
	}
	already, err := transcriptHasExactToolResult(run.Messages, operationID, toolCallID, canonicalResult)
	if err != nil {
		return false, fmt.Errorf("agentRunStore.ResumeExternalTool verify concurrent result(id=%d): %w", runID, err)
	}
	if !already {
		return false, fmt.Errorf("agentRunStore.ResumeExternalTool(id=%d): resume claim changed without the expected result", runID)
	}
	return false, nil
}

// CompleteExternalToolResume acknowledges that runner synchronous preflight
// succeeded. Only then is the durable pending identity cleared.
func (s *agentRunStore) CompleteExternalToolResume(ctx context.Context, runID uint64, operationID, toolCallID, leaseToken string) error {
	return s.transitionExternalToolResumeLease(ctx, runID, operationID, toolCallID, leaseToken, true)
}

// ReleaseExternalToolResume returns a failed synchronous preflight to a durable
// ready state so the next dispatcher callback can immediately reacquire it.
func (s *agentRunStore) ReleaseExternalToolResume(ctx context.Context, runID uint64, operationID, toolCallID, leaseToken string) error {
	return s.transitionExternalToolResumeLease(ctx, runID, operationID, toolCallID, leaseToken, false)
}

func (s *agentRunStore) transitionExternalToolResumeLease(
	ctx context.Context,
	runID uint64,
	operationID string,
	toolCallID string,
	leaseToken string,
	complete bool,
) error {
	leaseToken = strings.TrimSpace(leaseToken)
	if leaseToken == "" {
		return fmt.Errorf("agentRunStore transition external resume: lease token is required")
	}
	expectedState := externalResumeStartingPrefix + leaseToken
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.AgentRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, runID).Error; err != nil {
			return fmt.Errorf("agentRunStore transition external resume get(id=%d): %w", runID, err)
		}
		if run.CancellationRequestedAt != nil || run.IsDeleted {
			if complete {
				return fmt.Errorf("agentRunStore transition external resume(id=%d): run was cancelled or deleted", runID)
			}
			return nil
		}
		if run.StateReason != expectedState || run.Status != "running" {
			return fmt.Errorf("agentRunStore transition external resume(id=%d): no active starting lease", runID)
		}
		pending, err := externalaction.Parse(run.PendingExternalActionJSON)
		if err != nil {
			return fmt.Errorf("agentRunStore transition external resume(id=%d): corrupt identity: %w", runID, err)
		}
		if pending.OperationID != operationID || pending.ToolCallID != toolCallID {
			return fmt.Errorf("agentRunStore transition external resume(id=%d): identity mismatch", runID)
		}
		updates := map[string]interface{}{}
		if complete {
			updates["pending_external_action_json"] = nil
			updates["pending_external_action_at"] = nil
			updates["status"] = "running"
			updates["state_reason"] = "running"
		} else {
			endedAt := time.Now()
			updates["status"] = "terminated"
			updates["state_reason"] = externalResumeStateReady
			updates["ended_at"] = endedAt
		}
		result := tx.Model(&model.AgentRun{}).
			Where(
				"id = ? AND status = ? AND state_reason = ? AND cancellation_requested_at IS NULL AND is_deleted = ? AND pending_external_action_json = ?",
				runID, "running", expectedState, false, string(run.PendingExternalActionJSON),
			).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("agentRunStore transition external resume update(id=%d): %w", runID, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("agentRunStore transition external resume(id=%d): lease changed concurrently", runID)
		}
		return nil
	})
}

func canonicalExternalToolResult(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxExternalToolResultBytes {
		return nil, fmt.Errorf("result must be a non-empty JSON object within %d bytes", maxExternalToolResultBytes)
	}
	if err := validateStrictJSONObject(trimmed); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("result must be a JSON object")
	}
	if trailing, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing JSON token %v", trailing)
		}
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(canonical), nil
}

func newExternalResumeLeaseToken() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate external resume lease token: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func validateStrictJSONObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if first != json.Delim('{') {
		return fmt.Errorf("result must be a JSON object")
	}
	if err := consumeStrictJSONValue(decoder, first); err != nil {
		return err
	}
	if trailing, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON token %v", trailing)
		}
		return err
	}
	return nil
}

func consumeStrictJSONValue(decoder *json.Decoder, token json.Token) error {
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key must be a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeStrictJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeStrictJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("JSON array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func transcriptHasExactToolResult(messages []byte, operationID, toolCallID string, canonicalResult json.RawMessage) (bool, error) {
	var turns []json.RawMessage
	if err := json.Unmarshal(messages, &turns); err != nil {
		return false, fmt.Errorf("corrupt transcript: %w", err)
	}
	if err := validateExternalResumeTranscript(turns); err != nil {
		return false, err
	}
	found := false
	for _, turn := range turns {
		var msg schema.Message
		if err := json.Unmarshal(turn, &msg); err != nil {
			return false, fmt.Errorf("corrupt transcript turn: %w", err)
		}
		if msg.Role != schema.Tool || msg.ToolCallID != toolCallID {
			continue
		}
		if found {
			return false, fmt.Errorf("duplicate tool result for tool_call_id %q", toolCallID)
		}
		storedOperationID, _ := msg.Extra[externalOperationIDExtraKey].(string)
		if storedOperationID == "" {
			return false, fmt.Errorf("tool result for tool_call_id %q has no consumed operation identity", toolCallID)
		}
		if storedOperationID != operationID {
			return false, fmt.Errorf("conflicting operation identity for tool_call_id %q", toolCallID)
		}
		storedResult, err := canonicalExternalToolResult(json.RawMessage(msg.Content))
		if err != nil {
			return false, fmt.Errorf("invalid stored tool result: %w", err)
		}
		if !bytes.Equal(storedResult, canonicalResult) {
			return false, fmt.Errorf("conflicting tool result for tool_call_id %q", toolCallID)
		}
		found = true
	}
	return found, nil
}

func validateExternalResumeTranscript(turns []json.RawMessage) error {
	for index, raw := range turns {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return fmt.Errorf("turn %d must be a JSON object", index)
		}
		var envelope struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return fmt.Errorf("decode turn %d: %w", index, err)
		}
		switch envelope.Role {
		case "user", "assistant", "tool", "tool_group":
		default:
			return fmt.Errorf("turn %d has unsupported role %q", index, envelope.Role)
		}
	}
	return nil
}

func hasJSONValue(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked")
}

// ClearPendingQuestion nulls out the pending_question fields and resets state_reason to "running".
func (s *agentRunStore) ClearPendingQuestion(ctx context.Context, id uint64) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"pending_question_json": nil,
			"pending_question_at":   nil,
			"state_reason":          "running",
		})
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.ClearPendingQuestion(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.ClearPendingQuestion: no row matched id=%d", id)
	}
	return nil
}

// AppendUserMessage appends a {"role":"user","content":<content>} entry to agent_run.messages.
// The existing messages array is read, the new entry appended, and the result written back.
func (s *agentRunStore) AppendUserMessage(ctx context.Context, id uint64, content string) error {
	var run model.AgentRun
	if err := s.db.WithContext(ctx).Select("messages").First(&run, id).Error; err != nil {
		return fmt.Errorf("agentRunStore.AppendUserMessage get(id=%d): %w", id, err)
	}

	// Unmarshal current messages.
	var msgs []json.RawMessage
	if len(run.Messages) > 0 && string(run.Messages) != "null" {
		if err := json.Unmarshal(run.Messages, &msgs); err != nil {
			msgs = nil // start fresh on parse error
		}
	}

	// Build new user message entry.
	newMsg, err := json.Marshal(map[string]string{"role": "user", "content": content})
	if err != nil {
		return fmt.Errorf("agentRunStore.AppendUserMessage marshal(id=%d): %w", id, err)
	}
	msgs = append(msgs, json.RawMessage(newMsg))

	b, err := json.Marshal(msgs)
	if err != nil {
		return fmt.Errorf("agentRunStore.AppendUserMessage marshal array(id=%d): %w", id, err)
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Update("messages", datatypes.JSON(b))
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.AppendUserMessage update(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.AppendUserMessage: no row matched id=%d", id)
	}
	return nil
}

// AnswerAndClear atomically appends a user message and clears pending question state
// in a single DB transaction. This avoids a TOCTOU window between AppendUserMessage
// and ClearPendingQuestion that could leave the run in an inconsistent state on error.
func (s *agentRunStore) AnswerAndClear(ctx context.Context, id uint64, turn json.RawMessage) error {
	// Guard the interface contract: a nil/empty turn would append the literal
	// "null" (or nothing) into the transcript. The biz layer always builds a
	// valid turn, so this is defensive against future callers.
	if len(turn) == 0 || string(turn) == "null" {
		return fmt.Errorf("agentRunStore.AnswerAndClear(id=%d): empty answer turn", id)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Read current messages inside the transaction.
		var run model.AgentRun
		if err := tx.Select("id", "messages").First(&run, id).Error; err != nil {
			return fmt.Errorf("agentRunStore.AnswerAndClear get(id=%d): %w", id, err)
		}

		// Unmarshal existing messages array.
		var msgs []json.RawMessage
		if len(run.Messages) > 0 && string(run.Messages) != "null" {
			if err := json.Unmarshal(run.Messages, &msgs); err != nil {
				msgs = nil // start fresh on parse error
			}
		}

		// Append the pre-built answer turn verbatim (biz layer owns its shape:
		// role=user + content + embedded question_answer for issue1).
		msgs = append(msgs, turn)

		newMsgs, err := json.Marshal(msgs)
		if err != nil {
			return fmt.Errorf("agentRunStore.AnswerAndClear marshal array(id=%d): %w", id, err)
		}

		// Single UPDATE: messages + clear pending fields + return the row to a
		// truthful running state. The yield terminal wrote status='terminated' +
		// ended_at; leaving them in place made every poller declare the run
		// finished the moment the user answered, while the detached resume kept
		// working (dev run 148 — the user saw a stale "final answer" and the
		// real report arrived unseen 8.5 minutes later).
		result := tx.Model(&model.AgentRun{}).Where("id = ?", id).Updates(map[string]interface{}{
			"messages":              datatypes.JSON(newMsgs),
			"pending_question_json": nil,
			"pending_question_at":   nil,
			"state_reason":          "running",
			"status":                "running",
			"ended_at":              nil,
		})
		if result.Error != nil {
			return fmt.Errorf("agentRunStore.AnswerAndClear update(id=%d): %w", id, result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("agentRunStore.AnswerAndClear: no row matched id=%d", id)
		}
		return nil
	})
}

func (s *agentRunStore) ListBySession(ctx context.Context, sessionID string, offset, limit int) ([]model.AgentRun, int64, error) {
	var (
		runs  []model.AgentRun
		total int64
	)
	base := s.db.WithContext(ctx).Model(&model.AgentRun{}).Where("session_id = ?", sessionID)
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListBySession.Count: %w", err)
	}
	if err := base.Offset(offset).Limit(limit).Order("started_at DESC").Find(&runs).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListBySession.Find: %w", err)
	}
	return runs, total, nil
}

func (s *agentRunStore) UpdateSessionPinned(ctx context.Context, sessionID string, isPinned bool) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("session_id = ?", sessionID).
		Update("is_pinned", isPinned)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdateSessionPinned(sessionID=%s): %w", sessionID, result.Error)
	}
	return nil
}

func (s *agentRunStore) UpdateSessionName(ctx context.Context, sessionID string, name string) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("session_id = ?", sessionID).
		Update("session_name", name)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdateSessionName(sessionID=%s): %w", sessionID, result.Error)
	}
	return nil
}

// UpdateSessionNameIfEmpty sets session_name=name for the session's runs only
// while session_name is still empty (compare-and-set), so a concurrent manual
// rename during auto-title generation is never overwritten. Returns updated=false
// (no error) when nothing matched — name already set, or session absent.
func (s *agentRunStore) UpdateSessionNameIfEmpty(ctx context.Context, sessionID string, name string) (bool, error) {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("session_id = ? AND session_name = ?", sessionID, "").
		Update("session_name", name)
	if result.Error != nil {
		return false, fmt.Errorf("agentRunStore.UpdateSessionNameIfEmpty(sessionID=%s): %w", sessionID, result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (s *agentRunStore) UpdateSessionDeleted(ctx context.Context, sessionID string, isDeleted bool) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("session_id = ?", sessionID).
		Update("is_deleted", isDeleted)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdateSessionDeleted(sessionID=%s): %w", sessionID, result.Error)
	}
	return nil
}
