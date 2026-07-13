package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

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

type agentRunStore struct {
	db *gorm.DB
}

func newAgentRunStore(db *gorm.DB) IAgentRunStore {
	return &agentRunStore{db: db}
}

var _ IAgentRunStore = (*agentRunStore)(nil)
var _ IExternalActionWriter = (*agentRunStore)(nil)

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
