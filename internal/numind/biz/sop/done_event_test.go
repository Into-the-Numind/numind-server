package sop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// sseStreamHandler returns an httptest handler that streams the given SSE data
// frames followed by [DONE].
func sseStreamServer(frames ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for _, fr := range frames {
			fmt.Fprintf(w, "data: %s\n\n", fr)
			if f != nil {
				f.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f != nil {
			f.Flush()
		}
	}))
}

// TestCallVolcDeepThinkingStream_NoDoneEvent verifies the fallback executor no
// longer emits a "done" event (problem 4): the single done frame is written by
// the controller after biz returns, so executor emitting one too caused a
// duplicate done on the fallback path.
func TestCallVolcDeepThinkingStream_NoDoneEvent(t *testing.T) {
	srv := sseStreamServer(
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[{"delta":{"content":" world"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	)
	defer srv.Close()

	e := &SopExecutor{}
	node := &model.SopNode{BaseURL: srv.URL, ModelName: "m", APIKey: "k"}

	var events []string
	var messages string
	_, err := e.callVolcDeepThinkingStream(context.Background(), node,
		[]LLMMessage{{Role: "user", Content: "hi"}}, 256, false, "",
		func(event, chunk string) error {
			events = append(events, event)
			if event == "message" {
				messages += chunk
			}
			return nil
		})
	require.NoError(t, err)
	assert.NotContains(t, events, "done", "executor must NOT emit a done event (controller emits the single done)")
	assert.Equal(t, "hello world", messages, "message content still streamed")
}

// TestCallAliDeepThinkingStream_NoDoneEvent — same assertion for the Ali fallback.
func TestCallAliDeepThinkingStream_NoDoneEvent(t *testing.T) {
	srv := sseStreamServer(
		`{"choices":[{"delta":{"content":"abc"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	)
	defer srv.Close()

	e := &SopExecutor{}
	node := &model.SopNode{BaseURL: srv.URL, ModelName: "qwen-test", APIKey: "k"}

	var sawDoneEvent bool
	_, err := e.callAliDeepThinkingStream(context.Background(), node,
		[]LLMMessage{{Role: "user", Content: "hi"}}, 256, false, "",
		func(event, _ string) error {
			if event == "done" {
				sawDoneEvent = true
			}
			return nil
		})
	require.NoError(t, err)
	assert.False(t, sawDoneEvent, "Ali fallback executor must NOT emit a done event")
}
