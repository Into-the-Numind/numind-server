package adapter

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/errno"
)

func TestStreamIdleWatcher_TripsAfterIdle(t *testing.T) {
	closed := make(chan struct{})
	w, stop := startStreamIdleWatcher(closerFunc(func() error { close(closed); return nil }), 40*time.Millisecond)
	defer stop()
	// No mark() → the watcher trips and closes the body after the idle window.
	select {
	case <-closed:
		assert.True(t, w.tripped.Load())
	case <-time.After(time.Second):
		t.Fatal("idle watcher did not close the body after the idle window")
	}
}

func TestStreamIdleWatcher_MarkPreventsTrip(t *testing.T) {
	closed := make(chan struct{})
	w, stop := startStreamIdleWatcher(closerFunc(func() error { close(closed); return nil }), 80*time.Millisecond)
	defer stop()
	// Marking faster than the idle window → never trips.
	for i := 0; i < 5; i++ {
		time.Sleep(25 * time.Millisecond)
		w.mark()
	}
	assert.False(t, w.tripped.Load(), "regular mark() must keep the watcher from tripping")
	select {
	case <-closed:
		t.Fatal("watcher tripped despite regular mark()")
	default:
	}
}

// test(qa): reproduce dev run 138 — a provider that sends headers then stalls would
// block runOAIStream's scanner.Scan() forever and hang the whole agent run (~6.5 min).
// With the idle watchdog, the stalled read is killed after the idle window and a clear
// idle_timeout terminal chunk is emitted. Without the fix this test hangs (the select
// timeout fires).
func TestRunOAIStream_IdleTimeout_StalledStream(t *testing.T) {
	viper.Set("aiservice.stream_idle_timeout", "100ms")
	defer viper.Set("aiservice.stream_idle_timeout", "") // disable for other tests

	r := newStallingReader("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n")
	ch := make(chan aiservice.ChatChunk, 16)
	go runOAIStream(r, ch, "test", "test-model", nil)

	var gotIdle, gotContent bool
	timeout := time.After(3 * time.Second)
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				require.True(t, gotContent, "expected the first content chunk before the stall")
				require.True(t, gotIdle, "expected an idle_timeout terminal chunk after the stream stalled")
				return
			}
			if chunk.Delta == "hi" {
				gotContent = true
			}
			if strings.Contains(chunk.FinishReason, "idle_timeout") {
				gotIdle = true
				assert.ErrorIs(t, chunk.Err, errno.ErrAIProviderTimeout, "idle timeout must classify as a retryable provider timeout")
			}
		case <-timeout:
			t.Fatal("runOAIStream hung — no idle_timeout chunk emitted (the run-138 bug)")
		}
	}
}

// closerFunc adapts a func to io.Closer for the watcher tests.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// stallingReader returns one chunk then blocks on Read until Close is called —
// simulating a provider that sends headers then stalls mid-stream.
type stallingReader struct {
	first  string
	sent   bool
	closed chan struct{}
	once   sync.Once
}

func newStallingReader(first string) *stallingReader {
	return &stallingReader{first: first, closed: make(chan struct{})}
}

func (s *stallingReader) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		return copy(p, s.first), nil
	}
	<-s.closed // block until the idle watchdog closes us
	return 0, io.EOF
}

func (s *stallingReader) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}
