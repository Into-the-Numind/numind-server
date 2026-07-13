package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

func TestExternalActionPayload_PersistentStripsTransientURL(t *testing.T) {
	expiresAt := time.Date(2026, 7, 13, 9, 30, 0, 0, time.UTC)
	live := ExternalActionPayload{
		Provider:    "feishu",
		OperationID: "op-123",
		SessionID:   "auth-456",
		ToolCallID:  "call-789",
		Phase:       "user_auth",
		URL:         "https://open.feishu.cn/authorize?state=short-lived",
		ExpiresAt:   expiresAt,
	}

	persistent := live.Persistent()
	assert.Empty(t, persistent.URL)
	assert.Equal(t, live.Provider, persistent.Provider)
	assert.Equal(t, live.OperationID, persistent.OperationID)
	assert.Equal(t, live.SessionID, persistent.SessionID)
	assert.Equal(t, live.ToolCallID, persistent.ToolCallID)
	assert.Equal(t, live.Phase, persistent.Phase)
	assert.Equal(t, live.ExpiresAt, persistent.ExpiresAt)

	raw, err := json.Marshal(persistent)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "url")
	assert.NotContains(t, string(raw), "short-lived")
}

func externalActionFixture() ExternalActionPayload {
	return ExternalActionPayload{
		Provider:    "feishu",
		OperationID: "op-123",
		SessionID:   "auth-456",
		ToolCallID:  "call-789",
		Phase:       "user_auth",
		URL:         "https://open.feishu.cn/authorize?state=short-lived",
		ExpiresAt:   time.Date(2026, 7, 13, 9, 30, 0, 0, time.UTC),
	}
}

type externalActionWriterStore struct {
	*mockAgentRunStore
	updateErr error
	persisted []byte
}

func (s *externalActionWriterStore) UpdatePendingExternalAction(_ context.Context, runID uint64, payloadJSON []byte) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return errors.New("not found")
	}
	s.persisted = append([]byte(nil), payloadJSON...)
	run.PendingExternalActionJSON = append([]byte(nil), payloadJSON...)
	now := time.Now()
	run.PendingExternalActionAt = &now
	run.PendingQuestionJSON = nil
	run.PendingQuestionAt = nil
	run.StateReason = string(TerminalWaitingForUserChoice)
	return nil
}

func captureYieldEvents(t *testing.T) (func(stream.EventType, any), *[]stream.Event) {
	t.Helper()
	events := make([]stream.Event, 0, 2)
	emit := func(eventType stream.EventType, payload any) {
		event, err := stream.Encode(eventType, payload, uint64(len(events)+1), 91, 0)
		require.NoError(t, err)
		events = append(events, event)
	}
	return emit, &events
}

func externalActionEventTypes(events []stream.Event) []stream.EventType {
	types := make([]stream.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func TestPersistAndEmitYield_ExternalActionPersistsBeforeSeparateEvent(t *testing.T) {
	base := newMockStore()
	run := makeRun(91)
	base.runs[run.ID] = run
	writer := &externalActionWriterStore{mockAgentRunStore: base}
	runner := NewAgentRunner(writer, nil).(*agentRunner)
	emit, events := captureYieldEvents(t)
	persistBeforeExternalAction := false
	emitAfterPersistence := func(eventType stream.EventType, payload any) {
		if eventType == stream.EventExternalAction {
			persistBeforeExternalAction = len(writer.persisted) > 0
		}
		emit(eventType, payload)
	}
	state := &LoopState{}
	action := externalActionFixture()

	result, err := runner.persistAndEmitYield(context.Background(), run.ID, state, emitAfterPersistence, time.Now(), YieldPayload{
		ExternalAction: &action,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)
	assert.NotContains(t, string(writer.persisted), "url")
	assert.NotContains(t, string(writer.persisted), "short-lived")
	assert.True(t, persistBeforeExternalAction, "external_action must not be emitted before restart identity is durable")
	assert.Contains(t, externalActionEventTypes(*events), stream.EventExternalAction)
	assert.NotContains(t, externalActionEventTypes(*events), stream.EventQuestionPrompt)

	var live ExternalActionPayload
	for _, event := range *events {
		if event.Type == stream.EventExternalAction {
			require.NoError(t, json.Unmarshal(event.Data, &live))
		}
	}
	assert.Equal(t, action, live, "live SSE must retain the short-lived URL")
}

func TestPersistAndEmitYield_ExternalActionFailsClosedWithoutWriter(t *testing.T) {
	base := newMockStore()
	run := makeRun(92)
	base.runs[run.ID] = run
	runner := NewAgentRunner(base, nil).(*agentRunner)
	emit, events := captureYieldEvents(t)
	action := externalActionFixture()

	result, err := runner.persistAndEmitYield(context.Background(), run.ID, &LoopState{}, emit, time.Now(), YieldPayload{
		ExternalAction: &action,
	})

	require.NoError(t, err)
	assert.Equal(t, TerminalModelError, result.TerminalReason)
	assert.NotContains(t, externalActionEventTypes(*events), stream.EventExternalAction)
	assert.NotContains(t, externalActionEventTypes(*events), stream.EventQuestionPrompt)
}

func TestPersistAndEmitYield_ExternalActionFailsClosedOnPersistenceError(t *testing.T) {
	base := newMockStore()
	run := makeRun(93)
	base.runs[run.ID] = run
	writer := &externalActionWriterStore{mockAgentRunStore: base, updateErr: errors.New("database unavailable")}
	runner := NewAgentRunner(writer, nil).(*agentRunner)
	emit, events := captureYieldEvents(t)
	action := externalActionFixture()

	result, err := runner.persistAndEmitYield(context.Background(), run.ID, &LoopState{}, emit, time.Now(), YieldPayload{
		ExternalAction: &action,
	})

	require.NoError(t, err)
	assert.Equal(t, TerminalModelError, result.TerminalReason)
	assert.NotContains(t, externalActionEventTypes(*events), stream.EventExternalAction)
}

func TestPersistAndEmitYield_QuestionPromptRegression(t *testing.T) {
	base := newMockStore()
	run := makeRun(94)
	base.runs[run.ID] = run
	runner := NewAgentRunner(base, nil).(*agentRunner)
	emit, events := captureYieldEvents(t)

	result, err := runner.persistAndEmitYield(context.Background(), run.ID, &LoopState{}, emit, time.Now(), YieldPayload{
		Questions: []YieldQuestion{{Question: "公司全称？", Options: []YieldOption{}}},
	})

	require.NoError(t, err)
	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)
	assert.NotEmpty(t, run.PendingQuestionJSON)
	assert.Contains(t, externalActionEventTypes(*events), stream.EventQuestionPrompt)
	assert.NotContains(t, externalActionEventTypes(*events), stream.EventExternalAction)
}

type externalActionTool struct {
	BaseTool
	action ExternalActionPayload
}

func (t *externalActionTool) Name() string           { return "external_action_test" }
func (t *externalActionTool) Description() string    { return "external action test tool" }
func (t *externalActionTool) UserFacingName() string { return "外部授权" }
func (t *externalActionTool) NarrationVerb() string  { return "授权" }
func (t *externalActionTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	return nil, &yieldError{Payload: YieldPayload{ExternalAction: &t.action}}
}

func externalActionChatFn(toolName string) func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			ToolCalls: []aiservice.ToolCall{{
				ID:   "call-789",
				Type: "function",
				Function: aiservice.ToolCallFunction{
					Name:      toolName,
					Arguments: `{}`,
				},
			}},
			FinishReason: "tool_calls",
			Model:        "test-model",
			Provider:     "test",
		}, nil
	}
}

func TestRun_NonStreamingExternalActionPersistsWithoutPendingQuestion(t *testing.T) {
	action := externalActionFixture()
	tool := &externalActionTool{action: action}
	withMockChatFn(t, externalActionChatFn(tool.Name()))
	writer := &externalActionWriterStore{mockAgentRunStore: newMockStore()}
	runner := NewAgentRunner(writer, newStaticRegistry(tool))

	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1, SessionID: "session-external", Input: "发到飞书", ToolNames: []string{tool.Name()},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)
	assert.NotContains(t, string(writer.persisted), "url")
	run, getErr := writer.Get(context.Background(), result.AgentRunID)
	require.NoError(t, getErr)
	assert.Empty(t, run.PendingQuestionJSON)
	assert.NotEmpty(t, run.PendingExternalActionJSON)
}

func TestRun_NonStreamingExternalActionFailsClosedWithoutWriter(t *testing.T) {
	action := externalActionFixture()
	tool := &externalActionTool{action: action}
	withMockChatFn(t, externalActionChatFn(tool.Name()))
	base := newMockStore()
	runner := NewAgentRunner(base, newStaticRegistry(tool))

	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1, SessionID: "session-external", Input: "发到飞书", ToolNames: []string{tool.Name()},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TerminalModelError, result.TerminalReason)
	run, getErr := base.Get(context.Background(), result.AgentRunID)
	require.NoError(t, getErr)
	assert.Empty(t, run.PendingQuestionJSON)
	assert.Empty(t, run.PendingExternalActionJSON)
}

func TestGetSessionSnapshot_ExternalActionHasNoURLOrDuplicateQuestion(t *testing.T) {
	svc, db := newSQServiceFull(t)
	pendingAt := time.Date(2026, 7, 13, 8, 30, 0, 0, time.UTC)
	action := externalActionFixture().Persistent()
	raw, err := json.Marshal(action)
	require.NoError(t, err)
	run := &model.AgentRun{
		UserID:                    11,
		SessionID:                 "snapshot-external-action",
		Status:                    "terminated",
		StateReason:               string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[]`),
		PendingQuestionJSON:       datatypes.JSON(`{"questions":[{"question":"stale","options":[]}]}`),
		PendingExternalActionJSON: datatypes.JSON(raw),
		PendingExternalActionAt:   &pendingAt,
		StartedAt:                 pendingAt.Add(-time.Hour),
	}
	require.NoError(t, db.Create(run).Error)
	require.NoError(t, db.Model(&model.AgentRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"pending_external_action_json": datatypes.JSON(raw),
		"pending_external_action_at":   pendingAt,
	}).Error)

	snapshot, err := svc.GetSessionSnapshot(context.Background(), run.UserID, run.SessionID)
	require.NoError(t, err)
	messages := snapshot.Messages.([]agentMessage)
	require.Len(t, messages, 1, "external action takes precedence over a stale question payload")
	message := messages[0]
	assert.Equal(t, "external_action", message.Type)
	assert.Equal(t, action.Provider, message.Provider)
	assert.Equal(t, action.OperationID, message.OperationID)
	assert.Equal(t, action.SessionID, message.SessionID)
	assert.Equal(t, action.ToolCallID, message.ToolCallID)
	assert.Equal(t, action.Phase, message.Phase)
	assert.Empty(t, message.URL)
	assert.Equal(t, pendingAt.Format(time.RFC3339), message.Timestamp)
	assert.Empty(t, message.Questions)
	encoded, marshalErr := json.Marshal(message)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(encoded), "url")
}

func TestGetSessionSnapshot_ExternalActionRejectsUnknownOrSensitiveFields(t *testing.T) {
	for name, extra := range map[string]string{
		"url":          `,"url":"https://sensitive.example"`,
		"device_code":  `,"device_code":"ABC"`,
		"secret":       `,"secret":"shh"`,
		"future_field": `,"future_sensitive":"unknown"`,
	} {
		t.Run(name, func(t *testing.T) {
			svc, db := newSQServiceFull(t)
			raw := `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"` + extra + `}`
			run := &model.AgentRun{
				UserID:                    12,
				SessionID:                 "snapshot-external-action-" + name,
				Status:                    "terminated",
				StateReason:               string(TerminalWaitingForUserChoice),
				Messages:                  datatypes.JSON(`[]`),
				PendingExternalActionJSON: datatypes.JSON(raw),
				StartedAt:                 time.Now(),
			}
			require.NoError(t, db.Create(run).Error)
			require.NoError(t, db.Model(&model.AgentRun{}).Where("id = ?", run.ID).
				Update("pending_external_action_json", datatypes.JSON(raw)).Error)

			snapshot, err := svc.GetSessionSnapshot(context.Background(), run.UserID, run.SessionID)
			require.NoError(t, err)
			assert.Empty(t, snapshot.Messages.([]agentMessage), "unsafe persisted payload must fail closed")
		})
	}
}

func externalActionStreamFn(toolName string) func(context.Context, string, aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		ch := make(chan aiservice.ChatChunk, 1)
		ch <- aiservice.ChatChunk{
			IsFinal:      true,
			FinishReason: "tool_calls",
			ToolCalls: []aiservice.ToolCall{{
				ID:   "call-789",
				Type: "function",
				Function: aiservice.ToolCallFunction{
					Name:      toolName,
					Arguments: `{}`,
				},
			}},
		}
		close(ch)
		return ch, nil
	}
}

func TestRunStream_ExternalActionPersistsActualTerminalReason(t *testing.T) {
	for _, tt := range []struct {
		name       string
		store      func(*mockAgentRunStore) store.IAgentRunStore
		wantReason TerminalReason
		wantEvent  bool
	}{
		{
			name: "missing writer fails closed",
			store: func(base *mockAgentRunStore) store.IAgentRunStore {
				return base
			},
			wantReason: TerminalModelError,
		},
		{
			name: "writer failure fails closed",
			store: func(base *mockAgentRunStore) store.IAgentRunStore {
				return &externalActionWriterStore{mockAgentRunStore: base, updateErr: errors.New("database unavailable")}
			},
			wantReason: TerminalModelError,
		},
		{
			name: "successful persistence waits",
			store: func(base *mockAgentRunStore) store.IAgentRunStore {
				return &externalActionWriterStore{mockAgentRunStore: base}
			},
			wantReason: TerminalWaitingForUserChoice,
			wantEvent:  true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			action := externalActionFixture()
			tool := &externalActionTool{action: action}
			withMockChatStreamFn(t, externalActionStreamFn(tool.Name()))
			base := newMockStore()
			run := makeRunForStream(t, base)
			runner := NewAgentRunner(tt.store(base), newStaticRegistry(tool))
			ch := make(chan stream.Event, 256)

			result, err := runner.RunStream(context.Background(), RunRequest{
				UserID: 1, Input: "发到飞书", ToolNames: []string{tool.Name()},
			}, run.ID, ch)
			close(ch)

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantReason, result.TerminalReason)
			stored, getErr := base.Get(context.Background(), run.ID)
			require.NoError(t, getErr)
			assert.Equal(t, string(tt.wantReason), stored.StateReason, "DB terminal must match returned terminal")

			var terminal stream.TerminalPayload
			sawExternalAction := false
			for event := range ch {
				if event.Type == stream.EventExternalAction {
					sawExternalAction = true
				}
				if event.Type == stream.EventTerminal {
					require.NoError(t, json.Unmarshal(event.Data, &terminal))
				}
			}
			assert.Equal(t, string(tt.wantReason), terminal.Reason, "SSE terminal must match returned terminal")
			assert.Equal(t, tt.wantEvent, sawExternalAction)
		})
	}
}

func TestAnswer_ExternalActionIsNotAQuestion(t *testing.T) {
	runStore := newAnswerRunStore()
	action := externalActionFixture().Persistent()
	raw, err := json.Marshal(action)
	require.NoError(t, err)
	run := &model.AgentRun{
		UserID:                    21,
		SessionID:                 "answer-external-action",
		Status:                    "terminated",
		StateReason:               string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[]`),
		PendingExternalActionJSON: datatypes.JSON(raw),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	svc := newAnswerService(runStore)

	response, err := svc.Answer(context.Background(), run.UserID, run.ID, AnswerRequest{
		Answers: map[string]AnswerItem{"飞书授权": {FreeText: "已完成"}},
	})

	require.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "no pending question json")
	assert.Empty(t, runStore.answerAndClearCalls)
}
