package httpclient

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLLMStreamConfig_DoesNotTruncateHealthyLongStream reproduces the prod
// incident (2026-06-16, SOP run 3294, claude-opus-4-6 thinking via dmxapi): a
// healthy streaming LLM response that keeps emitting data the whole time still
// got cut off because http.Client.Timeout is a HARD total-request cap that
// "remains running ... and will interrupt reading of the Response.Body" (Go
// stdlib doc). Claude extended thinking streamed for >10min, the 600s
// http.Client.Timeout fired mid-stream → "context deadline exceeded
// (Client.Timeout ...)" → 504 ProviderTimeout → user saw endless thinking and
// no answer.
//
// The stream below always has data in flight (no idle gap), so an idle watchdog
// would never trip — only a total-duration cap truncates it. The test asserts:
//   - a client WITH a short total Timeout truncates the healthy stream (the bug)
//   - the streaming LLM config (LLMStreamConfig, no total Timeout) reads it fully
//
// Before the fix LLMStreamConfig does not exist (compile failure = red); after
// the fix it returns a config with Timeout==0 and the stream completes.
func TestLLMStreamConfig_DoesNotTruncateHealthyLongStream(t *testing.T) {
	t.Parallel()

	const (
		lines    = 10
		interval = 30 * time.Millisecond // total stream ~300ms, data always flowing
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			return
		}
		for i := 0; i < lines; i++ {
			time.Sleep(interval)
			_, _ = fmt.Fprintf(w, "data: chunk %d\n", i)
			fl.Flush()
		}
	}))
	defer srv.Close()

	// readAll consumes the SSE body and returns how many "data: " lines arrived
	// before the stream ended or the client aborted the body read.
	readAll := func(c *Client) (int, error) {
		resp, err := c.Do(&Request{
			Method:      http.MethodGet,
			URL:         srv.URL,
			Context:     context.Background(),
			RetryPolicy: &RetryPolicy{MaxRetries: 0},
		})
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		n := 0
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				n++
			}
		}
		return n, scanner.Err()
	}

	// 1. Reproduce the bug: a total http.Client.Timeout shorter than the stream
	//    duration truncates a perfectly healthy stream mid-read. We assert it was
	//    a *body-read* truncation — some chunks arrived (n>0), then the read was
	//    cut (err!=nil) before completion (n<lines) — not a connect/header failure
	//    (which against a localhost httptest server is implausible anyway).
	short := NewClient(&Config{
		Timeout:               100 * time.Millisecond, // total cap < ~300ms stream
		ConnectTimeout:        time.Second,
		ResponseHeaderTimeout: time.Second, // headers arrive instantly; ensure it's the total cap that bites
		TLSHandshakeTimeout:   time.Second,
	})
	n, err := readAll(short)
	if err == nil || n == 0 || n >= lines {
		t.Fatalf("expected mid-body truncation by total timeout (0<n<%d, err!=nil), got n=%d err=%v", lines, n, err)
	}

	// 2. The fix: the streaming LLM config carries no total request timeout, so a
	//    healthy long stream is read to completion. Overall ceiling/liveness are
	//    enforced elsewhere (caller context deadline + idle watchdog), never by a
	//    blunt http.Client.Timeout.
	streamCfg := LLMStreamConfig()
	if streamCfg.Timeout != 0 {
		t.Fatalf("LLMStreamConfig must have no total request timeout, got %v", streamCfg.Timeout)
	}
	stream := NewClient(streamCfg)
	n, err = readAll(stream)
	if err != nil {
		t.Fatalf("streaming client errored on a healthy stream: %v", err)
	}
	if n != lines {
		t.Fatalf("expected to read all %d chunks, got %d", lines, n)
	}
}
