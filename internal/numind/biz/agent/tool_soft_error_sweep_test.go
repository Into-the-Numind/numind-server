package agent

import (
	"context"
	"strings"
	"testing"

	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/retrieval/retrieve"
)

// ── tool-soft-error-sweep regression tests ──────────────────────────────────
//
// Reproduces dev runs 136/137 (2026-06-11): the model emitted malformed tool
// arguments (bool prompt to web_fetch, missing prompt to image_gen) and the
// tools returned non-nil Go errors. Eino v0.8.13 has no tool-error →
// tool-message hook, so a non-nil error becomes a NodeRunError that TERMINATES
// the whole agent run (state_reason=model_error). The contract under test:
// every model-input-derived error and every recoverable runtime error must
// come back as (ToolResult, nil) whose body carries an "ERROR" marker the LLM
// can read and self-correct — the same contract web_search/ask_user_question
// already honour (which is why runs 143/145 survived their malformed calls).
//
// These tests are permanent regression protection (NDF Rule 11): anyone who
// turns a soft error back into a hard error makes this file RED.

// failingKBRetriever stubs the retrieval base to simulate a transient
// retrieval outage (ES/vector store down). Such failures must not kill the run.
type failingKBRetriever struct{}

func (failingKBRetriever) Retrieve(_ context.Context, _ string, _ retrieve.Scope, _ retrieve.Options) (*retrieve.RetrievalResult, error) {
	return nil, context.DeadlineExceeded
}

func TestToolSoftErrorSweep_InputErrors(t *testing.T) {
	ctxNoUser := context.Background()

	cases := []struct {
		name  string
		tool  FullTool
		ctx   context.Context
		input string
	}{
		// dev run 137: missing prompt killed the run.
		{"image_gen missing prompt", &imageGenTool{}, ctxNoUser, `{}`},
		// bool where string expected — same class as run 136.
		{"image_gen bool prompt", &imageGenTool{}, ctxNoUser, `{"prompt": true}`},
		// dev run 136: bool prompt on web_fetch killed the run.
		{"web_fetch bool prompt", &webFetchTool{}, ctxNoUser, `{"url":"https://example.com","prompt":true}`},
		{"web_fetch invalid json", &webFetchTool{}, ctxNoUser, `{"url":`},
		{"create_csv invalid json", &createCSVTool{}, ctxNoUser, `{`},
		{"create_csv empty data", &createCSVTool{}, ctxNoUser, `{"filename":"a.csv","data":[]}`},
		{"create_json invalid json", &createJSONTool{}, ctxNoUser, `{`},
		{"create_html invalid json", &createHTMLTool{}, ctxNoUser, `{`},
		{"create_text invalid json", &createTextTool{}, ctxNoUser, `{`},
		{"kb_search invalid json", &kbSearchTool{}, ctxNoUser, `{`},
		{"memory_write invalid json", &memoryWriteTool{}, ctxNoUser, `{`},
		// valid shape but no user injected in ctx — a wiring gap must not kill the run.
		{"memory_write missing user", &memoryWriteTool{}, ctxNoUser, `{"kind":"fact","key":"k","value":"v"}`},
		{"memory_read invalid json", &memoryReadTool{}, ctxNoUser, `{`},
		{"memory_read missing user", &memoryReadTool{}, ctxNoUser, `{"key":"k"}`},
		{"document_generate missing prompt", &documentGenerateTool{}, ctxNoUser, `{}`},
		// stub tool: even a well-formed call must not kill the run.
		{"document_generate stub not registered", &documentGenerateTool{}, ctxNoUser, `{"prompt":"写一份文档"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.tool.Execute(tc.ctx, ToolInput(tc.input))
			if err != nil {
				t.Fatalf("Execute returned a hard error (kills the agent run via NodeRunError): %v", err)
			}
			if len(result) == 0 {
				t.Fatal("Execute returned an empty ToolResult; the LLM needs a readable error payload")
			}
			if !strings.Contains(string(result), "ERROR") {
				t.Fatalf("soft error payload must contain an ERROR marker for the LLM, got: %s", result)
			}
		})
	}
}

// TestToolSoftErrorSweep_RecoverableRuntimeErrors covers transient backend
// failures surfaced through tool deps: they must come back soft so the LLM can
// retry or route around them (spec §2 contract, ctx cancellation is handled at
// the Eino framework layer, never in the tool).
func TestToolSoftErrorSweep_RecoverableRuntimeErrors(t *testing.T) {
	ctx := middleware.NewContextWithUserID(context.Background(), 1)

	tool := &kbSearchTool{retriever: failingKBRetriever{}}
	result, err := tool.Execute(ctx, ToolInput(`{"query":"任意查询"}`))
	if err != nil {
		t.Fatalf("kb_search retrieval outage returned a hard error (kills the agent run): %v", err)
	}
	if !strings.Contains(string(result), "ERROR") {
		t.Fatalf("kb_search soft error payload must contain an ERROR marker, got: %s", result)
	}
}
