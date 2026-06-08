package agent

// terminal_metadata_consistency_test.go — S5 T16-BE Category 2
//
// Goal: Verify that the Hotfix 648d16d4 terminal_metadata persistence
// (MergeTerminalMetadata via finalizeRun) works correctly on the streaming
// path, mirroring the non-streaming path already covered by
// runner_model_error_meta_test.go.
//
// Two invariants:
//   1. LLM error on the streaming path → terminal_metadata persisted with
//      error_message + error_class fields.
//   2. Successful streaming completion → terminal_metadata is NOT set
//      (no corruption / spurious writes).
//
// Code-path note: MergeTerminalMetadata is called by finalizeRun(), which is
// invoked from the consumeEinoStream error branch in RunStream (line 524 of
// runner_runstream.go).  It is NOT called from the streamErr branch (lines
// 498-504), which handles the case where einoAgent.Stream() itself fails
// before returning a StreamReader.  Tests therefore exercise finalizeRun
// directly (same-package access) rather than going through the full RunStream
// stack, which accurately targets the code path that Hotfix 648d16d4 fixed.
//
// Mock pattern: makeRunner() + makeRun() + schema.Pipe (from runner_stream_test.go)
// + mockAgentRunStore.MergeTerminalMetadata (runner_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
)

// TestRunStream_TerminalMetadata_PersistsLLMError verifies that when the
// stream reader returns an error mid-stream (the path that calls finalizeRun),
// the agent_run row's terminal_metadata JSON contains:
//   - error_message: a user-facing friendly message (no raw engineer text)
//   - error_detail: the raw provider error string (for ops debugging)
//   - error_class: the terminal reason string
//
// This is the streaming-path regression test for Hotfix 648d16d4.
// finalizeRun's MergeTerminalMetadata must fire on the streaming error path
// (consumeEinoStream error branch), not just on the non-streaming Run() path.
func TestRunStream_TerminalMetadata_PersistsLLMError(t *testing.T) {
	ms := newMockStore()
	run := makeRun(200)
	// Seed the run into the mock store so finalizeRun.WriteTurn can find it.
	require.NoError(t, ms.Create(context.Background(), run))

	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
	}

	// Build a stream reader that sends one delta then errors — simulates a
	// mid-stream provider timeout (the path that hits consumeEinoStream error
	// branch → finalizeRun → MergeTerminalMetadata).
	injectedErr := errors.New("provider timeout: tcp dial: net/http: timeout awaiting response headers (dmxapi.cn)")
	sr, sw := schema.Pipe[*schema.Message](4)
	sw.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
	sw.Send(nil, injectedErr)
	sw.Close()

	st := &LoopState{}
	ch := make(chan stream.Event, 64)
	consumeResult, consumeErr := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)

	// consumeEinoStream must return the injected error.
	require.Error(t, consumeErr, "consumeEinoStream must propagate stream errors")
	assert.Equal(t, TerminalModelError, st.TerminalReason,
		"stream error must set TerminalModelError on the LoopState")

	// Simulate the RunStream consumeErr branch: call finalizeRun with the error.
	finalText := ""
	if consumeResult != nil {
		finalText = consumeResult.FinalOutput
	}
	if st.TerminalReason == "" {
		st.TerminalReason = TerminalModelError
	}
	finalResult, finalErr := r.finalizeRun(
		context.Background(), run, st, time.Now(), finalText,
		"", nil, false, 0, false, RunRequest{UserID: 1, Input: "trigger error"},
		make(chan *PermissionDenialDetail, 1), consumeErr, "sess-meta-test",
	)
	// finalizeRun may return the consumeErr or nil depending on WriteTurn success.
	_ = finalErr
	_ = finalResult

	// The critical assertion: terminal_metadata must be populated.
	got, dbErr := ms.Get(context.Background(), run.ID)
	require.NoError(t, dbErr)

	// Verify run was terminated.
	assert.Equal(t, "terminated", got.Status,
		"agent_run status must be 'terminated' after finalizeRun on error path")
	assert.NotEmpty(t, got.StateReason,
		"state_reason must be set after finalizeRun")

	// MergeTerminalMetadata must have been called with error_message + error_class.
	require.NotEmpty(t, got.TerminalMetadata,
		"terminal_metadata MUST be populated on streaming LLM error "+
			"(regression: Hotfix 648d16d4 — MergeTerminalMetadata must fire via finalizeRun)")

	var meta map[string]any
	require.NoError(t, json.Unmarshal(got.TerminalMetadata, &meta),
		"terminal_metadata must be valid JSON")

	errMsg, ok := meta["error_message"].(string)
	require.True(t, ok,
		"terminal_metadata.error_message must be a string, got %T", meta["error_message"])
	// error_message is now USER-FACING (friendly Chinese); it must NOT leak raw
	// engineer text. The raw provider error is preserved under error_detail for ops.
	assert.NotEmpty(t, errMsg, "error_message must carry a user-facing message")
	assert.NotContains(t, errMsg, "net/http",
		"error_message must not leak raw engineer text to users")
	assert.NotContains(t, errMsg, "dmxapi",
		"error_message must not leak the provider name to users")

	errDetail, _ := meta["error_detail"].(string)
	assert.Contains(t, errDetail, "timeout awaiting response headers",
		"raw provider error must be preserved in error_detail for ops debugging")

	errClass, _ := meta["error_class"].(string)
	assert.Equal(t, string(TerminalModelError), errClass,
		"error_class must equal the TerminalReason string for grep-ability")
}

// TestRunStream_TerminalMetadata_NoOverwriteOnSuccess verifies that a
// successful streaming completion does NOT write spurious terminal_metadata.
// terminal_metadata must remain nil (or empty) to avoid false positives in
// admin dashboards that treat non-empty terminal_metadata as an error signal.
func TestRunStream_TerminalMetadata_NoOverwriteOnSuccess(t *testing.T) {
	withMockChatStreamFn(t, successStreamFn("everything went fine"))

	ms := newMockStore()
	run := makeRunForStream(t, ms)
	runner, toolName := newReActRunnerForStream(ms)

	ch := make(chan stream.Event, 256)
	result, err := runner.RunStream(context.Background(), RunRequest{
		UserID:    1,
		Input:     "success run",
		ToolNames: []string{toolName},
	}, run.ID, ch)
	close(ch)

	require.NoError(t, err, "RunStream must not return error on successful LLM response")
	require.NotNil(t, result)
	assert.Equal(t, TerminalCompleted, result.TerminalReason,
		"successful stream must terminate with TerminalCompleted")

	// DB row.
	got, dbErr := ms.Get(context.Background(), run.ID)
	require.NoError(t, dbErr)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalCompleted), got.StateReason)

	// On success, finalizeRun skips the MergeTerminalMetadata call (runErr==nil).
	// terminal_metadata must remain nil (or empty JSON — not an error payload).
	if len(got.TerminalMetadata) > 0 && string(got.TerminalMetadata) != "null" {
		var meta map[string]any
		require.NoError(t, json.Unmarshal(got.TerminalMetadata, &meta))
		_, hasErrMsg := meta["error_message"]
		assert.False(t, hasErrMsg,
			"terminal_metadata must NOT contain error_message on successful stream "+
				"(would be a false-positive error signal in admin dashboard)")
		_, hasErrClass := meta["error_class"]
		assert.False(t, hasErrClass,
			"terminal_metadata must NOT contain error_class on successful stream")
	}
}
