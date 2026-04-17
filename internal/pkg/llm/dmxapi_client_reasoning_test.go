package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// sseOKResponse returns a minimal valid SSE streaming response body.
func sseOKResponse() string {
	return "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
}

// Test_ReasoningEffort_InjectedWhenFormatIsReasoningEffort verifies that when
// thinkingFormat="reasoning_effort", the outbound request body contains
// "reasoning_effort":"high".
func Test_ReasoningEffort_InjectedWhenFormatIsReasoningEffort(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseOKResponse()))
	}))
	defer srv.Close()

	client := NewDMXAPIClientWithConfig(srv.URL+"/v1", "fake-key")
	msgs := []ChatMessage{{Role: "user", Content: "hello"}}
	_, _, err := client.StreamChatCompletion(context.Background(), "deepseek-v3.2", msgs, 0.7, 0, "reasoning_effort", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	val, ok := body["reasoning_effort"]
	if !ok {
		t.Fatal("expected reasoning_effort field in request body, but it was absent")
	}
	if val != "medium" {
		t.Fatalf("expected reasoning_effort='medium', got %v", val)
	}

	maxComp, ok := body["max_completion_tokens"]
	if !ok {
		t.Fatal("expected max_completion_tokens field for reasoning token cap")
	}
	if maxComp != float64(1000) {
		t.Fatalf("expected max_completion_tokens=1000, got %v", maxComp)
	}
}

// Test_ReasoningEffort_NotInjectedByDefault verifies that when thinkingFormat=""
// the outbound request body does NOT contain a reasoning_effort field.
func Test_ReasoningEffort_NotInjectedByDefault(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseOKResponse()))
	}))
	defer srv.Close()

	client := NewDMXAPIClientWithConfig(srv.URL+"/v1", "fake-key")
	msgs := []ChatMessage{{Role: "user", Content: "hello"}}
	_, _, err := client.StreamChatCompletion(context.Background(), "deepseek-v3.2", msgs, 0.7, 0, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	if _, ok := body["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort field should be absent when thinkingFormat is empty, but it was present")
	}
}

// Test_ThinkSuffix_TriggersTemperature1 verifies that a model with a "-think"
// suffix overrides the caller-provided temperature with 1, matching the
// behaviour already present for "-thinking" suffix.
func Test_ThinkSuffix_TriggersTemperature1(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseOKResponse()))
	}))
	defer srv.Close()

	client := NewDMXAPIClientWithConfig(srv.URL+"/v1", "fake-key")
	msgs := []ChatMessage{{Role: "user", Content: "hello"}}
	// Pass temperature=0.3 — the -think suffix should force it to 1
	_, _, err := client.StreamChatCompletion(context.Background(), "claude-sonnet-4-6-think", msgs, 0.3, 0, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	// JSON numbers are float64 when decoded into interface{}
	temp, ok := body["temperature"]
	if !ok {
		t.Fatal("temperature field missing from request body")
	}
	if temp != float64(1) {
		t.Fatalf("expected temperature=1 for -think suffix model, got %v", temp)
	}
}

// Test_400Fallback_OnReasoningEffortError verifies that when the provider
// returns 400 with a body containing "reasoning_effort", the client
// automatically retries without the reasoning_effort parameter and succeeds.
func Test_400Fallback_OnReasoningEffortError(t *testing.T) {
	var requestCount int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requestCount, 1)
		if n == 1 {
			// First request: return 400 with reasoning_effort error body
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"reasoning_effort: unknown_parameter"}`))
			return
		}
		// Second request: return 200 normal SSE
		capturedBody, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		if err := json.Unmarshal(capturedBody, &body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		// Verify second request does NOT contain reasoning_effort
		if _, ok := body["reasoning_effort"]; ok {
			http.Error(w, "reasoning_effort should be absent in retry", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseOKResponse()))
	}))
	defer srv.Close()

	client := NewDMXAPIClientWithConfig(srv.URL+"/v1", "fake-key")
	msgs := []ChatMessage{{Role: "user", Content: "hello"}}
	_, _, err := client.StreamChatCompletion(context.Background(), "deepseek-v3.2", msgs, 0.7, 0, "reasoning_effort", nil)
	if err != nil {
		t.Fatalf("expected no error after fallback retry, got: %v", err)
	}

	if got := atomic.LoadInt32(&requestCount); got != 2 {
		t.Fatalf("expected 2 requests (1 failed + 1 retry), got %d", got)
	}
}
