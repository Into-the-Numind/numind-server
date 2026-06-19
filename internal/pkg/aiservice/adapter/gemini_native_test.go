package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// geminiRoute builds a minimal ResolvedRoute pointed at the gemini-native
// provider for request-build / parse tests.
func geminiRoute(policy string) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		ProviderModelID:   "gemini-2.5-pro",
		PromptCachePolicy: policy,
		Provider: registry.ProviderInfo{
			Name:    "gemini-native",
			BaseURL: "https://www.dmxapi.cn",
			APIKey:  "sk-fake-gemini-key",
		},
	}
}

// decodeGeminiBody marshals the gemini request body and decodes it back into a
// generic map for assertion.
func decodeGeminiBody(t *testing.T, body geminiRequest) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// URL build + key redaction (finding #4)
// ---------------------------------------------------------------------------

func TestGeminiURL_KeyInQueryNotHeader_NonStream(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")

	full := a.buildGeminiURL(route, "generateContent", false)
	if !strings.Contains(full, "/v1beta/models/gemini-2.5-pro:generateContent") {
		t.Errorf("URL missing model:method path: %q", full)
	}
	if !strings.Contains(full, "key=sk-fake-gemini-key") {
		t.Errorf("URL must carry the key as ?key= query param: %q", full)
	}
	if strings.Contains(full, "alt=sse") {
		t.Errorf("non-stream URL must NOT carry alt=sse: %q", full)
	}
}

func TestGeminiURL_StreamCarriesAltSSE(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")

	full := a.buildGeminiURL(route, "streamGenerateContent", true)
	if !strings.Contains(full, ":streamGenerateContent") {
		t.Errorf("stream URL must use streamGenerateContent: %q", full)
	}
	if !strings.Contains(full, "alt=sse") {
		t.Errorf("stream URL must carry alt=sse: %q", full)
	}
	if !strings.Contains(full, "key=sk-fake-gemini-key") {
		t.Errorf("stream URL must carry the key: %q", full)
	}
}

// The auth key must be in the URL only — NEVER in a header (proven: DMXAPI
// Gemini rejects Bearer).
func TestGeminiHeaders_NoAuthHeader(t *testing.T) {
	h := geminiHeaders(false)
	for k, v := range h {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "x-api-key" || lk == "x-goog-api-key" {
			t.Errorf("gemini headers must not carry an auth header, found %s=%q", k, v)
		}
		if strings.Contains(v, "sk-fake-gemini-key") {
			t.Errorf("gemini headers leaked a key in %s=%q", k, v)
		}
	}
	if h["Content-Type"] != "application/json" {
		t.Errorf("Content-Type=%q want application/json", h["Content-Type"])
	}
}

// Key-in-URL redaction: the redacted form NEVER contains the fake key and DOES
// contain key=REDACTED (finding #4).
func TestGeminiURL_RedactionHidesKey(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")

	full := a.buildGeminiURL(route, "generateContent", false)
	redacted := redactGeminiURL(full)

	if strings.Contains(redacted, "sk-fake-gemini-key") {
		t.Errorf("redacted URL leaked the key: %q", redacted)
	}
	if !strings.Contains(redacted, "key=REDACTED") {
		t.Errorf("redacted URL missing key=REDACTED: %q", redacted)
	}
	// The path must survive redaction.
	if !strings.Contains(redacted, "/v1beta/models/gemini-2.5-pro:generateContent") {
		t.Errorf("redaction mangled the path: %q", redacted)
	}
}

// sanitizeGeminiTransportErr must scrub the live key out of a transport error
// string regardless of how it was embedded (finding #4 — Go's url.Error folds the
// full ?key=<live key> URL into err.Error()).
func TestSanitizeGeminiTransportErr_ScrubsKey(t *testing.T) {
	// Simulate the worst case: Go's *url.Error style string carrying the full URL.
	raw := errors.New(`Post "https://www.dmxapi.cn/v1beta/models/gemini-2.5-pro:generateContent?key=sk-fake-gemini-key&alt=sse": dial tcp: connection refused`)
	got := sanitizeGeminiTransportErr("sk-fake-gemini-key", raw)
	if got == nil {
		t.Fatal("sanitized error must not be nil")
	}
	s := got.Error()
	if strings.Contains(s, "sk-fake-gemini-key") {
		t.Errorf("sanitized transport error leaked the key: %q", s)
	}
	if !strings.Contains(s, "REDACTED") {
		t.Errorf("sanitized transport error must contain REDACTED: %q", s)
	}
	// The non-secret cause must survive.
	if !strings.Contains(s, "connection refused") {
		t.Errorf("sanitized error dropped the cause: %q", s)
	}
}

// A forced HTTP error through the real doPost path must surface ONLY the redacted
// form — the live key must NOT appear anywhere in the returned error, regardless
// of how Go's net/http formats the underlying *url.Error (spec §5C finding #4).
func TestGeminiDoPost_ForcedHTTPError_RedactedOnly(t *testing.T) {
	// Spin up then immediately close a server so the address yields a deterministic
	// transport-level (connection-refused) error instead of a real HTTP response.
	srv := httptest.NewServer(nil)
	addr := srv.URL
	srv.Close()

	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	route.Provider.BaseURL = addr // unreachable → forces a transport error

	_, err := a.doPost(context.Background(), route, []byte(`{}`))
	if err == nil {
		t.Fatal("doPost against a closed server must return a transport error")
	}
	s := err.Error()
	if strings.Contains(s, "sk-fake-gemini-key") {
		t.Errorf("forced HTTP error leaked the live key: %q", s)
	}
	if !strings.Contains(s, "REDACTED") {
		t.Errorf("forced HTTP error must surface the redacted form (REDACTED): %q", s)
	}
}

// ---------------------------------------------------------------------------
// Request body translation
// ---------------------------------------------------------------------------

func TestBuildGeminiRequest_SystemInstructionAndContents(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	req := aiservice.ChatRequest{
		MaxTokens:   1024,
		Temperature: 0.7,
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: "你是助理"}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "你好"}},
			{Role: aiservice.MessageRoleAssistant, Content: aiservice.MessageContent{Text: "您好"}},
		},
	}

	body := a.buildGeminiRequest(route, req)
	m := decodeGeminiBody(t, body)

	// systemInstruction
	si, ok := m["systemInstruction"].(map[string]interface{})
	if !ok {
		t.Fatalf("systemInstruction missing or wrong type: %#v", m["systemInstruction"])
	}
	parts, ok := si["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		t.Fatalf("systemInstruction.parts missing: %#v", si)
	}
	if txt := parts[0].(map[string]interface{})["text"]; txt != "你是助理" {
		t.Errorf("systemInstruction text=%v want 你是助理", txt)
	}

	// contents: user→user, assistant→model
	contents, ok := m["contents"].([]interface{})
	if !ok || len(contents) != 2 {
		t.Fatalf("contents len=%d want 2: %#v", len(contents), m["contents"])
	}
	c0 := contents[0].(map[string]interface{})
	if c0["role"] != "user" {
		t.Errorf("contents[0].role=%v want user", c0["role"])
	}
	c1 := contents[1].(map[string]interface{})
	if c1["role"] != "model" {
		t.Errorf("contents[1].role=%v want model (assistant→model)", c1["role"])
	}

	// generationConfig
	gc, ok := m["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("generationConfig missing: %#v", m["generationConfig"])
	}
	if gc["maxOutputTokens"].(float64) != 1024 {
		t.Errorf("maxOutputTokens=%v want 1024", gc["maxOutputTokens"])
	}
	if gc["temperature"].(float64) != 0.7 {
		t.Errorf("temperature=%v want 0.7", gc["temperature"])
	}

	// NEVER send cachedContent (DMXAPI 404 on explicit cache).
	if _, has := m["cachedContent"]; has {
		t.Error("body must NOT carry cachedContent (DMXAPI 404 on explicit cache)")
	}
}

func TestBuildGeminiRequest_TemperatureOmittedWhenZero(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	req := aiservice.ChatRequest{
		MaxTokens: 512,
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
		},
	}
	m := decodeGeminiBody(t, a.buildGeminiRequest(route, req))
	gc := m["generationConfig"].(map[string]interface{})
	if _, has := gc["temperature"]; has {
		t.Errorf("temperature must be omitted when 0: %#v", gc)
	}
}

func TestBuildGeminiRequest_Tools(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	req := aiservice.ChatRequest{
		MaxTokens: 256,
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "search"}},
		},
		Tools: []aiservice.Tool{
			{Type: "function", Function: aiservice.ToolFunction{
				Name:        "web_search",
				Description: "search the web",
				Parameters:  map[string]interface{}{"type": "object"},
			}},
		},
	}
	m := decodeGeminiBody(t, a.buildGeminiRequest(route, req))
	tools, ok := m["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools len wrong: %#v", m["tools"])
	}
	fd, ok := tools[0].(map[string]interface{})["functionDeclarations"].([]interface{})
	if !ok || len(fd) != 1 {
		t.Fatalf("functionDeclarations missing: %#v", tools[0])
	}
	decl := fd[0].(map[string]interface{})
	if decl["name"] != "web_search" {
		t.Errorf("functionDeclaration name=%v want web_search", decl["name"])
	}
	if _, has := decl["parameters"]; !has {
		t.Errorf("functionDeclaration must carry parameters: %#v", decl)
	}
}

func TestBuildGeminiRequest_NoToolsOmitsField(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	req := aiservice.ChatRequest{
		MaxTokens: 256,
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
		},
	}
	m := decodeGeminiBody(t, a.buildGeminiRequest(route, req))
	if _, has := m["tools"]; has {
		t.Errorf("tools must be omitted when there are none: %#v", m["tools"])
	}
}

// Assistant tool_call → functionCall on a model content; the OAI Arguments
// STRING is unmarshalled into an args OBJECT.
func TestBuildGeminiRequest_FunctionCall(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	req := aiservice.ChatRequest{
		MaxTokens: 256,
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "weather?"}},
			{Role: aiservice.MessageRoleAssistant, ToolCalls: []aiservice.ToolCall{
				{ID: "call-1", Type: "function", Function: aiservice.ToolCallFunction{
					Name: "get_weather", Arguments: `{"city":"沪"}`,
				}},
			}},
		},
	}
	m := decodeGeminiBody(t, a.buildGeminiRequest(route, req))
	contents := m["contents"].([]interface{})
	if len(contents) != 2 {
		t.Fatalf("contents len=%d want 2", len(contents))
	}
	model := contents[1].(map[string]interface{})
	if model["role"] != "model" {
		t.Errorf("functionCall must be on a model content, role=%v", model["role"])
	}
	parts := model["parts"].([]interface{})
	fc, ok := parts[0].(map[string]interface{})["functionCall"].(map[string]interface{})
	if !ok {
		t.Fatalf("functionCall missing: %#v", parts[0])
	}
	if fc["name"] != "get_weather" {
		t.Errorf("functionCall.name=%v want get_weather", fc["name"])
	}
	args, ok := fc["args"].(map[string]interface{})
	if !ok {
		t.Fatalf("functionCall.args must be an OBJECT: %#v", fc["args"])
	}
	if args["city"] != "沪" {
		t.Errorf("functionCall.args.city=%v want 沪", args["city"])
	}
}

// ---------------------------------------------------------------------------
// Stateless functionResponse name recovery (finding #6)
// ---------------------------------------------------------------------------

// Two parallel tool calls in one assistant turn, resolved OUT OF ORDER in the
// following tool messages. Each functionResponse.name MUST match the original
// functionCall.name resolved via a backward scan by ToolCallID — NOT an
// ephemeral per-request map.
func TestBuildGeminiRequest_ParallelToolResponseNameMatch_OutOfOrder(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	req := aiservice.ChatRequest{
		MaxTokens: 256,
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "do both"}},
			// Assistant turn with TWO parallel tool calls.
			{Role: aiservice.MessageRoleAssistant, ToolCalls: []aiservice.ToolCall{
				{ID: "id-weather", Type: "function", Function: aiservice.ToolCallFunction{Name: "get_weather", Arguments: `{}`}},
				{ID: "id-stock", Type: "function", Function: aiservice.ToolCallFunction{Name: "get_stock", Arguments: `{}`}},
			}},
			// Tool results returned OUT OF ORDER: stock first, weather second.
			{Role: aiservice.MessageRoleTool, ToolCallID: "id-stock", Content: aiservice.MessageContent{Text: "100"}},
			{Role: aiservice.MessageRoleTool, ToolCallID: "id-weather", Content: aiservice.MessageContent{Text: "sunny"}},
		},
	}
	m := decodeGeminiBody(t, a.buildGeminiRequest(route, req))
	contents := m["contents"].([]interface{})

	// Collect every functionResponse from all user contents.
	got := map[string]string{} // name -> response.result
	for _, ci := range contents {
		c := ci.(map[string]interface{})
		partsRaw, ok := c["parts"].([]interface{})
		if !ok {
			continue
		}
		for _, pi := range partsRaw {
			p := pi.(map[string]interface{})
			fr, ok := p["functionResponse"].(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := fr["name"].(string)
			resp, _ := fr["response"].(map[string]interface{})
			result, _ := resp["result"].(string)
			got[name] = result
		}
	}

	if got["get_stock"] != "100" {
		t.Errorf("get_stock functionResponse=%q want 100 (name matched by ToolCallID across out-of-order results)", got["get_stock"])
	}
	if got["get_weather"] != "sunny" {
		t.Errorf("get_weather functionResponse=%q want sunny", got["get_weather"])
	}
}

// Defensive fallback: a tool message whose ToolCallID matches no prior call uses
// msg.Name when present, else "unknown_tool" — and the request still serialises.
func TestBuildGeminiRequest_FunctionResponseFallback(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	req := aiservice.ChatRequest{
		MaxTokens: 256,
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "x"}},
			// No matching assistant tool_use, and no Name → unknown_tool.
			{Role: aiservice.MessageRoleTool, ToolCallID: "orphan", Content: aiservice.MessageContent{Text: "r"}},
		},
	}
	m := decodeGeminiBody(t, a.buildGeminiRequest(route, req))
	contents := m["contents"].([]interface{})
	found := ""
	for _, ci := range contents {
		c := ci.(map[string]interface{})
		partsRaw, _ := c["parts"].([]interface{})
		for _, pi := range partsRaw {
			if fr, ok := pi.(map[string]interface{})["functionResponse"].(map[string]interface{}); ok {
				found, _ = fr["name"].(string)
			}
		}
	}
	if found != "unknown_tool" {
		t.Errorf("orphan tool response name=%q want unknown_tool (defensive fallback)", found)
	}
}

// ---------------------------------------------------------------------------
// Non-stream response parse (D5 usage)
// ---------------------------------------------------------------------------

func TestParseGeminiResponse_UsageNoDoubleCount(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	// D5: promptTokenCount ALREADY includes cachedContentTokenCount. So
	// PromptTokens = promptTokenCount (NOT promptTokenCount + cached).
	raw := `{
		"candidates":[{"content":{"role":"model","parts":[{"text":"hello "},{"text":"world"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":6308,"candidatesTokenCount":12,"totalTokenCount":6320,"cachedContentTokenCount":6122,"thoughtsTokenCount":3}
	}`
	resp, err := a.parseGeminiResponse([]byte(raw), route)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Content != "hello world" {
		t.Errorf("Content=%q want 'hello world'", resp.Content)
	}
	if resp.Usage.PromptTokens != 6308 {
		t.Errorf("PromptTokens=%d want 6308 (no double-count)", resp.Usage.PromptTokens)
	}
	if resp.Usage.CachedPromptTokens != 6122 {
		t.Errorf("CachedPromptTokens=%d want 6122", resp.Usage.CachedPromptTokens)
	}
	if resp.Usage.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens=%d want 0 (Gemini implicit, no creation bucket)", resp.Usage.CacheCreationTokens)
	}
	if resp.Usage.CompletionTokens != 12 {
		t.Errorf("CompletionTokens=%d want 12", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 6320 {
		t.Errorf("TotalTokens=%d want 6320", resp.Usage.TotalTokens)
	}
	if resp.Usage.ReasoningTokens != 3 {
		t.Errorf("ReasoningTokens=%d want 3", resp.Usage.ReasoningTokens)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason=%q want stop", resp.FinishReason)
	}
	if resp.Provider != "gemini-native" {
		t.Errorf("Provider=%q want gemini-native", resp.Provider)
	}
}

func TestParseGeminiResponse_ToolCalls(t *testing.T) {
	a := NewGeminiNativeAdapter()
	route := geminiRoute("gemini_implicit")
	raw := `{
		"candidates":[{"content":{"role":"model","parts":[
			{"functionCall":{"name":"get_weather","args":{"city":"SH"}}}
		]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}
	}`
	resp, err := a.parseGeminiResponse([]byte(raw), route)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len=%d want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool name=%q want get_weather", tc.Function.Name)
	}
	if tc.ID == "" {
		t.Errorf("tool call must have a synthesised stable ID")
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (%q)", err, tc.Function.Arguments)
	}
	if args["city"] != "SH" {
		t.Errorf("args.city=%v want SH", args["city"])
	}
	// A functionCall present ⇒ finish reason maps to tool_calls.
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason=%q want tool_calls when a functionCall is present", resp.FinishReason)
	}
}

func TestMapGeminiFinishReason(t *testing.T) {
	cases := map[string]string{
		"STOP":       "stop",
		"MAX_TOKENS": "length",
		"SAFETY":     "stop",
		"RECITATION": "stop",
		"":           "",
	}
	for in, want := range cases {
		if got := mapGeminiFinishReason(in, false); got != want {
			t.Errorf("mapGeminiFinishReason(%q,false)=%q want %q", in, got, want)
		}
	}
	// A functionCall present overrides STOP → tool_calls.
	if got := mapGeminiFinishReason("STOP", true); got != "tool_calls" {
		t.Errorf("mapGeminiFinishReason(STOP,true)=%q want tool_calls", got)
	}
}

// ---------------------------------------------------------------------------
// Streaming (runGeminiStream)
// ---------------------------------------------------------------------------

func runGemStream(transcript string, model string) []aiservice.ChatChunk {
	ch := make(chan aiservice.ChatChunk, 64)
	r := io.NopCloser(strings.NewReader(transcript))
	go runGeminiStream(r, ch, "gemini-native", model, nil)
	return collectChunks(ch)
}

func TestRunGeminiStream_TextAndUsage(t *testing.T) {
	transcript := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你"}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"好"}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":2,"totalTokenCount":102,"cachedContentTokenCount":40}}`,
		``,
	}, "\n")

	chunks := runGemStream(transcript, "gemini-2.5-pro")
	term := terminalChunk(t, chunks)

	var content strings.Builder
	for _, c := range chunks {
		content.WriteString(c.Delta)
	}
	if content.String() != "你好" {
		t.Errorf("streamed content=%q want 你好", content.String())
	}
	if term.Usage == nil {
		t.Fatal("terminal usage nil")
	}
	if term.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens=%d want 100 (no double-count)", term.Usage.PromptTokens)
	}
	if term.Usage.CachedPromptTokens != 40 {
		t.Errorf("CachedPromptTokens=%d want 40", term.Usage.CachedPromptTokens)
	}
	if term.Usage.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens=%d want 0", term.Usage.CacheCreationTokens)
	}
	if term.Usage.CompletionTokens != 2 {
		t.Errorf("CompletionTokens=%d want 2", term.Usage.CompletionTokens)
	}
	if term.FinishReason != "stop" {
		t.Errorf("FinishReason=%q want stop", term.FinishReason)
	}
}

func TestRunGeminiStream_ToolCall(t *testing.T) {
	transcript := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"do_x","args":{"k":"v"}}}]}}]}`,
		``,
		`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}`,
		``,
	}, "\n")

	chunks := runGemStream(transcript, "gemini-2.5-pro")
	term := terminalChunk(t, chunks)
	if len(term.ToolCalls) != 1 {
		t.Fatalf("terminal ToolCalls len=%d want 1", len(term.ToolCalls))
	}
	tc := term.ToolCalls[0]
	if tc.Function.Name != "do_x" {
		t.Errorf("tool name=%q want do_x", tc.Function.Name)
	}
	if tc.ID == "" {
		t.Errorf("streamed tool call must carry a synthesised ID")
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if args["k"] != "v" {
		t.Errorf("args.k=%v want v", args["k"])
	}
	if term.FinishReason != "tool_calls" {
		t.Errorf("FinishReason=%q want tool_calls (functionCall present)", term.FinishReason)
	}
}

// Exactly one terminal chunk even when the stream ends with a clean EOF and no
// usage at all.
func TestRunGeminiStream_SingleTerminalNoUsage(t *testing.T) {
	transcript := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}]}`,
		``,
	}, "\n")
	chunks := runGemStream(transcript, "gemini-2.5-pro")
	term := terminalChunk(t, chunks) // asserts exactly 1 IsFinal
	if term.Usage == nil {
		t.Fatal("terminal usage should be non-nil (explicit 0) even with no usage metadata")
	}
	if term.Usage.PromptTokens != 0 {
		t.Errorf("PromptTokens=%d want 0 (no usage → never bill phantom tokens)", term.Usage.PromptTokens)
	}
}
