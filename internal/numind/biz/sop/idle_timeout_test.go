package sop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

type recordingCloser struct{ closed atomic.Bool }

func (r *recordingCloser) Close() error { r.closed.Store(true); return nil }

func TestIdleWatcher_TripsAndClosesOnIdle(t *testing.T) {
	rc := &recordingCloser{}
	w, stop := startIdleWatcher(context.Background(), rc, 40*time.Millisecond)
	defer stop()

	assert.Eventually(t, func() bool {
		return w.tripped.Load() && rc.closed.Load()
	}, time.Second, 5*time.Millisecond, "idle watcher should trip and close body after idle window")
}

func TestIdleWatcher_MarkPreventsTrip(t *testing.T) {
	rc := &recordingCloser{}
	w, stop := startIdleWatcher(context.Background(), rc, 60*time.Millisecond)
	defer stop()

	// Keep marking faster than the idle window for ~200ms → never trips.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.mark(60 * time.Millisecond)
		time.Sleep(15 * time.Millisecond)
	}
	assert.False(t, w.tripped.Load(), "regular activity must keep the watcher from tripping")
	assert.False(t, rc.closed.Load(), "body must not be closed while active")
}

func TestIdleWatcher_CtxCancelClosesButDoesNotTrip(t *testing.T) {
	rc := &recordingCloser{}
	ctx, cancel := context.WithCancel(context.Background())
	w, stop := startIdleWatcher(ctx, rc, 10*time.Second) // long idle; ctx wins
	defer stop()

	cancel()
	assert.Eventually(t, func() bool { return rc.closed.Load() }, time.Second, 5*time.Millisecond,
		"ctx cancel should close the body")
	assert.False(t, w.tripped.Load(), "ctx cancel must NOT set tripped (it's not an idle timeout)")
}

// TestCallVolcDeepThinkingStream_IdleTimeout points the fallback Volc reader at a
// server that returns headers then stalls (sends no data). The idle watcher must
// abort with an error wrapping context.DeadlineExceeded.
func TestCallVolcDeepThinkingStream_IdleTimeout(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("sop.stream_idle_timeout", "80ms") // trip fast for the test

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // release response headers so client.Do returns
		}
		time.Sleep(800 * time.Millisecond) // stall: never send a data chunk (> idle window)
	}))
	defer srv.Close()

	e := &SopExecutor{}
	node := &model.SopNode{BaseURL: srv.URL, ModelName: "test-model", APIKey: "k"}

	done := make(chan error, 1)
	go func() {
		_, err := e.callVolcDeepThinkingStream(context.Background(), node,
			[]LLMMessage{{Role: "user", Content: "hi"}}, 256, false, "",
			func(_ string, _ string) error { return nil })
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "stalled provider must produce an error")
		assert.True(t, errors.Is(err, context.DeadlineExceeded),
			"idle timeout error must wrap context.DeadlineExceeded (got %v)", err)
	case <-time.After(3 * time.Second):
		t.Fatal("callVolcDeepThinkingStream did not return — idle timeout not enforced")
	}
}
