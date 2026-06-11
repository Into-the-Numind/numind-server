package adapter

import (
	"io"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
)

// defaultStreamIdleTimeout is the fallback idle window for streaming LLM reads when
// aiservice.stream_idle_timeout is unset. 120s is generous enough that a healthy
// thinking model (deepseek-v4-pro streams reasoning continuously) never trips it,
// yet short enough that a stalled provider stream is killed in 2 minutes instead of
// hanging the whole agent run for minutes (dev run 138 hung ~6.5 min).
const defaultStreamIdleTimeout = 120 * time.Second

// streamIdleTimeout returns the configured idle timeout for streaming LLM reads.
// Config key aiservice.stream_idle_timeout (e.g. "2m", "90s"); unset → 120s default.
// An explicit "0s" disables the watchdog entirely (safety valve).
func streamIdleTimeout() time.Duration {
	if viper.IsSet("aiservice.stream_idle_timeout") {
		return viper.GetDuration("aiservice.stream_idle_timeout") // may be 0 → disabled
	}
	return defaultStreamIdleTimeout
}

// streamIdleWatcher closes a streaming response body when no chunk is read within
// the idle window. It guards the blocking bufio read in runOAIStream: a provider
// that sends headers then stalls would otherwise block scanner.Scan() forever and
// hang the agent run (dev run 138). Mirrors sop.idleWatcher; the read loop MUST call
// mark() after every successful read to reset the idle clock, and check tripped()
// after the loop to surface a clear "provider stalled" error.
type streamIdleWatcher struct {
	timer   *time.Timer
	idle    time.Duration
	tripped atomic.Bool
}

// startStreamIdleWatcher arms an idle timer that closes body after idle of
// inactivity. The returned stop func (call via defer) tears it down.
func startStreamIdleWatcher(body io.Closer, idle time.Duration) (*streamIdleWatcher, func()) {
	w := &streamIdleWatcher{idle: idle}
	w.timer = time.AfterFunc(idle, func() {
		w.tripped.Store(true)
		_ = body.Close() // unblocks the in-flight bufio read
	})
	return w, func() { w.timer.Stop() }
}

// mark resets the idle clock; call after every successful read. Once tripped it is a
// no-op (avoids a redundant Reset racing the fired AfterFunc callback).
func (w *streamIdleWatcher) mark() {
	if w.tripped.Load() {
		return
	}
	w.timer.Reset(w.idle)
}
