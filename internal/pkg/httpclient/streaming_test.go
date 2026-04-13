package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSSEProcessor_FlushesLastEventAndErrorsWithoutDone(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"a\":1}\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	client := NewClient(&Config{Timeout: 5 * time.Second})
	processor := NewSSEProcessor(client)

	var got []json.RawMessage
	err := processor.ProcessSSE(&Request{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Context: context.Background(),
	}, func(ev *SSEEvent) error {
		got = append(got, ev.Data)
		return nil
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF, got %v", err)
	}
	if len(got) != 1 || string(got[0]) != "{\"a\":1}" {
		t.Fatalf("unexpected events: %v", got)
	}
}

func TestSSEProcessor_AllowsDoneWithoutTrailingNewline(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"a\":1}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	client := NewClient(&Config{Timeout: 5 * time.Second})
	processor := NewSSEProcessor(client)

	var got []json.RawMessage
	err := processor.ProcessSSE(&Request{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Context: context.Background(),
	}, func(ev *SSEEvent) error {
		got = append(got, ev.Data)
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(got) != 1 || string(got[0]) != "{\"a\":1}" {
		t.Fatalf("unexpected events: %v", got)
	}
}
