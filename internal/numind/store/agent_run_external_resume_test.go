package store

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/externalaction"
	"numind-server/internal/pkg/model"
)

const externalResumePayload = `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`

func seedExternalResumeRun(t *testing.T, s IAgentRunStore, mutate func(*model.AgentRun)) uint64 {
	t.Helper()
	endedAt := time.Now().Add(-time.Minute)
	run := &model.AgentRun{
		UserID:                    7,
		SessionID:                 "session-7",
		Status:                    "terminated",
		StateReason:               "waiting_for_user_choice",
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写入飞书"}]`),
		StartedAt:                 time.Now().Add(-time.Minute),
		EndedAt:                   &endedAt,
		PendingExternalActionJSON: datatypes.JSON(externalResumePayload),
	}
	if mutate != nil {
		mutate(run)
	}
	require.NoError(t, s.Create(context.Background(), run))
	return run.ID
}

func externalToolResumeLease(t *testing.T, s IAgentRunStore) IExternalToolResumeLease {
	t.Helper()
	r, ok := s.(IExternalToolResumeLease)
	require.True(t, ok, "production agentRunStore must expose the narrow external resume lease lifecycle")
	return r
}

func externalToolWaitFinalizer(t *testing.T, s IAgentRunStore) IExternalToolWaitFinalizer {
	t.Helper()
	finalizer, ok := s.(IExternalToolWaitFinalizer)
	require.True(t, ok, "production agentRunStore must expose the narrow external wait terminalizer")
	return finalizer
}

func TestExternalToolWaitFinalizerConsumesExactWaitOnceAndClearsRunningCard(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	runID := seedExternalResumeRun(t, s, nil)
	finalizer := externalToolWaitFinalizer(t, s)

	finalized, err := finalizer.FinalizeExternalToolWait(
		context.Background(), 7, runID, "op-1", "tc-9", externalaction.TerminalOutcomeCancelled,
	)
	require.NoError(t, err)
	require.True(t, finalized)

	got, err := s.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, "aborted_tools", got.StateReason)
	assert.Empty(t, got.PendingExternalActionJSON)
	assert.Nil(t, got.PendingExternalActionAt)
	assert.NotNil(t, got.CancellationRequestedAt)
	assert.NotNil(t, got.EndedAt)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(got.TerminalMetadata, &metadata))
	assert.Equal(t, map[string]any{"provider": "feishu", "outcome": string(externalaction.TerminalOutcomeCancelled)}, metadata["external_action"])

	msgs := decodeStoredSchemaMessages(t, got.Messages)
	require.Len(t, msgs, 2)
	assert.Equal(t, schema.Tool, msgs[1].Role)
	assert.Equal(t, "tc-9", msgs[1].ToolCallID)
	assert.JSONEq(t, `{"ok":false,"operation_state":"cancelled","error":{"code":"feishu_operation_cancelled","message":"飞书操作已取消，未执行。"}}`, msgs[1].Content)
	assert.Equal(t, "op-1", msgs[1].Extra[externalOperationIDExtraKey])

	finalized, err = finalizer.FinalizeExternalToolWait(
		context.Background(), 7, runID, "op-1", "tc-9", externalaction.TerminalOutcomeCancelled,
	)
	require.NoError(t, err)
	assert.False(t, finalized, "repeated lifecycle delivery must not append a second tool result")
	got, err = s.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 2)
	lease := externalToolResumeLease(t, s)
	candidates, err := lease.ListExternalToolResumeCandidates(context.Background(), time.Now().Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, candidates, "Task11's reclaimer must not reclaim a durably cancelled external wait")
}

func TestExternalToolWaitFinalizerRejectsCrossUserWrongIdentityAndContradictorySuccess(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	finalizer := externalToolWaitFinalizer(t, s)

	t.Run("cross user", func(t *testing.T) {
		runID := seedExternalResumeRun(t, s, nil)
		finalized, err := finalizer.FinalizeExternalToolWait(context.Background(), 8, runID, "op-1", "tc-9", externalaction.TerminalOutcomeCancelled)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		assert.False(t, finalized)
		got, getErr := s.Get(context.Background(), runID)
		require.NoError(t, getErr)
		assert.NotEmpty(t, got.PendingExternalActionJSON)
	})

	t.Run("wrong operation identity", func(t *testing.T) {
		runID := seedExternalResumeRun(t, s, nil)
		finalized, err := finalizer.FinalizeExternalToolWait(context.Background(), 7, runID, "op-other", "tc-9", externalaction.TerminalOutcomeCancelled)
		require.Error(t, err)
		assert.False(t, finalized)
	})

	t.Run("completed success cannot be overwritten", func(t *testing.T) {
		runID := seedExternalResumeRun(t, s, nil)
		lease := externalToolResumeLease(t, s)
		token, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
		require.NoError(t, err)
		require.True(t, claimed)
		require.NotEmpty(t, token)
		finalized, err := finalizer.FinalizeExternalToolWait(context.Background(), 7, runID, "op-1", "tc-9", externalaction.TerminalOutcomeUnknown)
		require.Error(t, err, "a terminal callback must not append a contradictory second tool result")
		assert.False(t, finalized)
		got, getErr := s.Get(context.Background(), runID)
		require.NoError(t, getErr)
		assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 2)
		assert.NotEqual(t, "aborted_tools", got.StateReason)
	})
}

func TestExternalToolResumeLease_DoesNotExposeUntokenizedResume(t *testing.T) {
	leaseType := reflect.TypeOf((*IExternalToolResumeLease)(nil)).Elem()
	_, exposed := leaseType.MethodByName("ResumeExternalTool")
	assert.False(t, exposed, "production lease capability must require the tokenized runner handshake")
}

func decodeStoredSchemaMessages(t *testing.T, raw []byte) []*schema.Message {
	t.Helper()
	var turns []json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &turns))
	out := make([]*schema.Message, 0, len(turns))
	for _, turn := range turns {
		var msg schema.Message
		require.NoError(t, json.Unmarshal(turn, &msg))
		out = append(out, &msg)
	}
	return out
}

func TestExternalToolResume_AppendsOriginalToolResultAndClaimsOnce(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	runID := seedExternalResumeRun(t, s, nil)
	result := json.RawMessage(`{"ok":true}`)

	lease := externalToolResumeLease(t, s)
	token, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEmpty(t, token)

	got, err := s.Get(context.Background(), runID)
	require.NoError(t, err)
	msgs := decodeStoredSchemaMessages(t, got.Messages)
	require.Len(t, msgs, 2)
	assert.Equal(t, schema.Tool, msgs[1].Role)
	assert.Equal(t, "tc-9", msgs[1].ToolCallID)
	assert.JSONEq(t, string(result), msgs[1].Content)
	assert.Equal(t, externalResumePayload, string(got.PendingExternalActionJSON), "identity remains durable until runner preflight acknowledges start")
	assert.NotNil(t, got.PendingExternalActionAt)
	assert.Equal(t, "running", got.Status)
	assert.Equal(t, externalResumeStartingPrefix+token, got.StateReason)
	assert.Nil(t, got.EndedAt)

	require.NoError(t, lease.CompleteExternalToolResume(context.Background(), runID, "op-1", "tc-9", token))
	got, err = s.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Empty(t, got.PendingExternalActionJSON)
	assert.Nil(t, got.PendingExternalActionAt)
	assert.Equal(t, "running", got.StateReason)

	_, claimed, err = lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	assert.False(t, claimed)
	got, err = s.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 2, "idempotent callback must not append twice")

	_, _, err = lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":false}`))
	require.Error(t, err, "a different result for the same already-resumed tool call is an integrity error")
	_, _, err = lease.ClaimExternalToolResume(context.Background(), runID, "op-other", "tc-9", result)
	require.Error(t, err, "consumed identity must include operation_id, not only tool_call_id and result")
}

func TestExternalToolResume_ReleasedPreludeLeaseCanBeReclaimed(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	lease := externalToolResumeLease(t, s)
	runID := seedExternalResumeRun(t, s, nil)
	result := json.RawMessage(`{"ok":true}`)

	token, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	require.True(t, claimed)

	// Concurrent duplicate while preflight owns a live lease must not launch.
	_, claimed, err = lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	assert.False(t, claimed)

	// A synchronous runner preflight failure releases the same durable result;
	// the next dispatcher callback can immediately reacquire without appending.
	require.NoError(t, lease.ReleaseExternalToolResume(context.Background(), runID, "op-1", "tc-9", token))
	_, claimed, err = lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	assert.True(t, claimed)
	got, getErr := s.Get(context.Background(), runID)
	require.NoError(t, getErr)
	assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 2)
}

func TestExternalToolResume_ExpiredOwnerIsFencedAfterReclaim(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	lease := externalToolResumeLease(t, s)
	runID := seedExternalResumeRun(t, s, nil)
	result := json.RawMessage(`{"ok":true}`)

	tokenA, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	require.True(t, claimed)
	concrete := s.(*agentRunStore)
	require.NoError(t, concrete.db.Model(&model.AgentRun{}).Where("id = ?", runID).
		Update("pending_external_action_at", time.Now().Add(-externalResumeLeaseDuration-time.Second)).Error)

	tokenB, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEqual(t, tokenA, tokenB)

	require.Error(t, lease.CompleteExternalToolResume(context.Background(), runID, "op-1", "tc-9", tokenA))
	require.Error(t, lease.ReleaseExternalToolResume(context.Background(), runID, "op-1", "tc-9", tokenA))
	stillB, getErr := s.Get(context.Background(), runID)
	require.NoError(t, getErr)
	assert.Contains(t, stillB.StateReason, tokenB)
	assert.Equal(t, externalResumePayload, string(stillB.PendingExternalActionJSON))

	require.NoError(t, lease.CompleteExternalToolResume(context.Background(), runID, "op-1", "tc-9", tokenB))
}

func TestExternalToolResume_CompleteRejectsCancellationOrDeletionAfterClaim(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, uint64) error
	}{
		{
			name: "cancel requested",
			mutate: func(db *gorm.DB, runID uint64) error {
				return db.Model(&model.AgentRun{}).Where("id = ?", runID).
					Update("cancellation_requested_at", time.Now()).Error
			},
		},
		{
			name: "soft deleted",
			mutate: func(db *gorm.DB, runID uint64) error {
				return db.Model(&model.AgentRun{}).Where("id = ?", runID).
					Update("is_deleted", true).Error
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newExternalActionAgentRunStore(t)
			lease := externalToolResumeLease(t, s)
			runID := seedExternalResumeRun(t, s, nil)
			token, claimed, err := lease.ClaimExternalToolResume(
				context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`),
			)
			require.NoError(t, err)
			require.True(t, claimed)

			concrete := s.(*agentRunStore)
			require.NoError(t, tc.mutate(concrete.db, runID))
			err = lease.CompleteExternalToolResume(context.Background(), runID, "op-1", "tc-9", token)
			require.Error(t, err, "cancelled/deleted work must never be accepted for LLM execution")

			got, getErr := s.Get(context.Background(), runID)
			require.NoError(t, getErr)
			assert.Equal(t, externalResumeStartingPrefix+token, got.StateReason)
			assert.Equal(t, externalResumePayload, string(got.PendingExternalActionJSON))
			require.NoError(t, lease.ReleaseExternalToolResume(context.Background(), runID, "op-1", "tc-9", token),
				"release is a safe no-op once cancellation/deletion owns the run")
		})
	}
}

func TestExternalToolResume_TouchRenewsExactLeaseAndRejectsDelete(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	lease := externalToolResumeLease(t, s)
	runID := seedExternalResumeRun(t, s, nil)
	token, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
	require.NoError(t, err)
	require.True(t, claimed)
	old := time.Now().Add(-time.Minute)
	db := s.(*agentRunStore).db
	require.NoError(t, db.Model(&model.AgentRun{}).Where("id = ?", runID).Update("pending_external_action_at", old).Error)
	require.NoError(t, lease.TouchExternalToolResume(context.Background(), runID, "op-1", "tc-9", token))
	got, err := s.Get(context.Background(), runID)
	require.NoError(t, err)
	require.NotNil(t, got.PendingExternalActionAt)
	assert.True(t, got.PendingExternalActionAt.After(old))

	require.NoError(t, db.Model(&model.AgentRun{}).Where("id = ?", runID).Update("is_deleted", true).Error)
	require.Error(t, lease.TouchExternalToolResume(context.Background(), runID, "op-1", "tc-9", token))
}

func TestExternalToolResume_CandidateScanOnlyReturnsReadyAndExpired(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	lease := externalToolResumeLease(t, s)
	makeClaim := func() (uint64, string) {
		id := seedExternalResumeRun(t, s, nil)
		token, claimed, err := lease.ClaimExternalToolResume(context.Background(), id, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
		require.NoError(t, err)
		require.True(t, claimed)
		return id, token
	}
	readyID, readyToken := makeClaim()
	require.NoError(t, lease.ReleaseExternalToolResume(context.Background(), readyID, "op-1", "tc-9", readyToken))
	expiredID, _ := makeClaim()
	activeID, _ := makeClaim()
	cancelledID, cancelledToken := makeClaim()
	deletedID, _ := makeClaim()
	db := s.(*agentRunStore).db
	require.NoError(t, db.Model(&model.AgentRun{}).Where("id = ?", expiredID).Update("pending_external_action_at", time.Now().Add(-time.Minute)).Error)
	require.NoError(t, db.Model(&model.AgentRun{}).Where("id = ?", cancelledID).Updates(map[string]any{
		"pending_external_action_at": time.Now().Add(-time.Minute), "cancellation_requested_at": time.Now(),
	}).Error)
	require.NoError(t, db.Model(&model.AgentRun{}).Where("id = ?", deletedID).Updates(map[string]any{
		"pending_external_action_at": time.Now().Add(-time.Minute), "is_deleted": true,
	}).Error)
	_ = cancelledToken

	candidates, err := lease.ListExternalToolResumeCandidates(context.Background(), time.Now().Add(-30*time.Second), 100)
	require.NoError(t, err)
	var ids []uint64
	for i := range candidates {
		ids = append(ids, candidates[i].ID)
	}
	assert.ElementsMatch(t, []uint64{readyID, expiredID}, ids)
	assert.NotContains(t, ids, activeID)
	assert.NotContains(t, ids, cancelledID)
	assert.NotContains(t, ids, deletedID)
}

func TestExternalToolResume_ConcurrentCallbacksHaveOneWinner(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	lease := externalToolResumeLease(t, s)
	runID := seedExternalResumeRun(t, s, nil)
	const workers = 12

	start := make(chan struct{})
	var wg sync.WaitGroup
	claimed := make(chan bool, workers)
	tokens := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token, ok, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
			claimed <- ok
			tokens <- token
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(tokens)
	close(errs)

	winners := 0
	for ok := range claimed {
		if ok {
			winners++
		}
	}
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, winners)
	var winnerToken string
	for token := range tokens {
		if token != "" {
			winnerToken = token
		}
	}
	require.NotEmpty(t, winnerToken)
	require.NoError(t, lease.CompleteExternalToolResume(context.Background(), runID, "op-1", "tc-9", winnerToken))
	got, err := s.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 2)
	assert.Empty(t, got.PendingExternalActionJSON, "tokenized handshake must clear the wait only after runner acknowledgement")
	assert.Equal(t, "running", got.StateReason)
}

func TestExternalToolResume_FailsClosedWithoutClearingWait(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		toolCall  string
		result    json.RawMessage
		mutate    func(*model.AgentRun)
	}{
		{name: "wrong operation", operation: "op-other", toolCall: "tc-9", result: json.RawMessage(`{"ok":true}`)},
		{name: "wrong tool call", operation: "op-1", toolCall: "tc-other", result: json.RawMessage(`{"ok":true}`)},
		{name: "corrupt pending", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true}`), mutate: func(r *model.AgentRun) { r.PendingExternalActionJSON = datatypes.JSON(`{"operation_id":`) }},
		{name: "corrupt transcript", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true}`), mutate: func(r *model.AgentRun) { r.Messages = datatypes.JSON(`[{`) }},
		{name: "invalid result", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":`)},
		{name: "non object result", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`true`)},
		{name: "trailing result", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true} {}`)},
		{name: "duplicate result key", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true,"ok":false}`)},
		{name: "nested duplicate result key", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true,"data":{"id":1,"id":2}}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newExternalActionAgentRunStore(t)
			lease := externalToolResumeLease(t, s)
			runID := seedExternalResumeRun(t, s, tc.mutate)
			before, err := s.Get(context.Background(), runID)
			require.NoError(t, err)
			beforeWait := append([]byte(nil), before.PendingExternalActionJSON...)
			beforeMessages := append([]byte(nil), before.Messages...)

			_, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, tc.operation, tc.toolCall, tc.result)
			require.Error(t, err)
			assert.False(t, claimed)
			after, getErr := s.Get(context.Background(), runID)
			require.NoError(t, getErr)
			assert.Equal(t, string(beforeWait), string(after.PendingExternalActionJSON), "wait identity must remain for diagnosis/retry")
			assert.Equal(t, string(beforeMessages), string(after.Messages), "corrupt transcript must never be reset")
			assert.Equal(t, "terminated", after.Status)
			assert.Equal(t, "waiting_for_user_choice", after.StateReason)
		})
	}
}

func TestExternalToolResume_DoesNotReviveCancelledOrDeletedRuns(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		mutate func(*model.AgentRun)
	}{
		{name: "cancel requested", mutate: func(r *model.AgentRun) { r.CancellationRequestedAt = &now }},
		{name: "soft deleted", mutate: func(r *model.AgentRun) { r.IsDeleted = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newExternalActionAgentRunStore(t)
			lease := externalToolResumeLease(t, s)
			runID := seedExternalResumeRun(t, s, tc.mutate)

			_, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
			require.NoError(t, err)
			assert.False(t, claimed)
			got, getErr := s.Get(context.Background(), runID)
			require.NoError(t, getErr)
			assert.Equal(t, externalResumePayload, string(got.PendingExternalActionJSON))
			assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 1)
		})
	}
}

func TestExternalToolResume_UnexpectedRunStateOrMissingWaitFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.AgentRun)
	}{
		{name: "wrong state reason", mutate: func(r *model.AgentRun) { r.StateReason = "completed" }},
		{name: "active run", mutate: func(r *model.AgentRun) { r.Status = "running" }},
		{name: "missing wait without prior result", mutate: func(r *model.AgentRun) { r.PendingExternalActionJSON = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newExternalActionAgentRunStore(t)
			lease := externalToolResumeLease(t, s)
			runID := seedExternalResumeRun(t, s, tc.mutate)

			_, claimed, err := lease.ClaimExternalToolResume(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
			require.Error(t, err)
			assert.False(t, claimed)
			got, getErr := s.Get(context.Background(), runID)
			require.NoError(t, getErr)
			assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 1)
		})
	}
}
