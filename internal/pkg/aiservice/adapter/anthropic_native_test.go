package adapter

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// bigSystemText returns a system-prompt string long enough to clear the D8
// ~1024-token (~3000 char) economic guard, so cache_control is allowed to
// attach when the toggle is ON.
func bigSystemText() string {
	return strings.Repeat("你是一个专业的销售助理，请严格遵守以下规范。", 200) // ~ 4000+ chars
}

// claudeRoute builds a minimal ResolvedRoute pointed at the claude-native
// provider for request-build / parse tests.
func claudeRoute(policy string) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		ProviderModelID:   "claude-opus-4-6",
		PromptCachePolicy: policy,
		Provider: registry.ProviderInfo{
			Name:    "claude-native",
			BaseURL: "https://www.dmxapi.cn",
			APIKey:  "sk-fake-key",
		},
	}
}

// decodeBody marshals the anthropic request body and decodes it back into a
// generic map for assertion.
func decodeBody(t *testing.T, body anthropicRequest) map[string]interface{} {
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

// lastSystemBlockHasCacheControl reports whether the LAST element of the
// "system" array carries a non-nil cache_control object.
func lastSystemBlockHasCacheControl(t *testing.T, m map[string]interface{}) bool {
	t.Helper()
	sysRaw, ok := m["system"]
	if !ok {
		return false
	}
	arr, ok := sysRaw.([]interface{})
	if !ok || len(arr) == 0 {
		return false
	}
	last, ok := arr[len(arr)-1].(map[string]interface{})
	if !ok {
		return false
	}
	_, has := last["cache_control"]
	return has
}

// anySystemBlockHasCacheControl reports whether ANY system block carries
// cache_control — used to assert toggle-OFF emits none at all.
func anySystemBlockHasCacheControl(t *testing.T, m map[string]interface{}) bool {
	t.Helper()
	sysRaw, ok := m["system"]
	if !ok {
		return false
	}
	arr, ok := sysRaw.([]interface{})
	if !ok {
		return false
	}
	for _, e := range arr {
		blk, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if _, has := blk["cache_control"]; has {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// buildAnthropicRequest — body golden / cache_control gating
// ---------------------------------------------------------------------------

func TestBuildAnthropicRequest_CacheControlGating(t *testing.T) {
	a := NewClaudeNativeAdapter()
	bigSys := bigSystemText()

	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: bigSys}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "你好"}},
		},
		MaxTokens: 1024,
	}

	t.Run("cacheOn_adds_cache_control_to_last_system_block", func(t *testing.T) {
		body := a.buildAnthropicRequest(claudeRoute("claude_ephemeral"), req, true)
		m := decodeBody(t, body)
		if !lastSystemBlockHasCacheControl(t, m) {
			t.Fatalf("cacheOn: expected cache_control on last system block, body=%v", m["system"])
		}
		// model + max_tokens must be present.
		if m["model"] != "claude-opus-4-6" {
			t.Errorf("model=%v want claude-opus-4-6", m["model"])
		}
		if mt, _ := m["max_tokens"].(float64); int(mt) != 1024 {
			t.Errorf("max_tokens=%v want 1024", m["max_tokens"])
		}
	})

	t.Run("cacheOff_omits_cache_control_entirely", func(t *testing.T) {
		body := a.buildAnthropicRequest(claudeRoute("claude_ephemeral"), req, false)
		m := decodeBody(t, body)
		if anySystemBlockHasCacheControl(t, m) {
			t.Fatalf("cacheOff: expected NO cache_control anywhere, body=%v", m["system"])
		}
	})

	t.Run("cacheOn_but_short_prefix_omits_cache_control_D8", func(t *testing.T) {
		shortReq := aiservice.ChatRequest{
			Messages: []aiservice.ChatMessage{
				{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: "短系统提示"}},
				{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
			},
			MaxTokens: 256,
		}
		body := a.buildAnthropicRequest(claudeRoute("claude_ephemeral"), shortReq, true)
		m := decodeBody(t, body)
		if anySystemBlockHasCacheControl(t, m) {
			t.Fatalf("D8 guard: short prefix must NOT get cache_control, body=%v", m["system"])
		}
	})
}

func TestBuildAnthropicRequest_HeadersAndToolTranslation(t *testing.T) {
	a := NewClaudeNativeAdapter()
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: "sys"}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "search the web"}},
			{
				Role:    aiservice.MessageRoleAssistant,
				Content: aiservice.MessageContent{Text: ""},
				ToolCalls: []aiservice.ToolCall{{
					ID:   "toolu_1",
					Type: "function",
					Function: aiservice.ToolCallFunction{
						Name:      "web_search",
						Arguments: `{"q":"golang"}`,
					},
				}},
			},
			{
				Role:       aiservice.MessageRoleTool,
				ToolCallID: "toolu_1",
				Content:    aiservice.MessageContent{Text: "result text"},
			},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "thanks"}},
		},
		MaxTokens: 512,
		Tools: []aiservice.Tool{{
			Type: "function",
			Function: aiservice.ToolFunction{
				Name:        "web_search",
				Description: "search the web",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		}},
	}

	body := a.buildAnthropicRequest(claudeRoute("off"), req, false)
	m := decodeBody(t, body)

	// tools use input_schema, NOT function.parameters.
	toolsRaw, ok := m["tools"].([]interface{})
	if !ok || len(toolsRaw) != 1 {
		t.Fatalf("tools missing/len: %v", m["tools"])
	}
	tool0 := toolsRaw[0].(map[string]interface{})
	if tool0["name"] != "web_search" {
		t.Errorf("tool name=%v", tool0["name"])
	}
	if _, has := tool0["input_schema"]; !has {
		t.Errorf("tool missing input_schema: %v", tool0)
	}

	// messages: assistant tool_use block, then a user turn carrying tool_result.
	msgs, ok := m["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages missing: %v", m["messages"])
	}
	// Find the assistant message with a tool_use block.
	foundToolUse := false
	foundToolResult := false
	for _, mm := range msgs {
		msg := mm.(map[string]interface{})
		role := msg["role"]
		content, _ := msg["content"].([]interface{})
		for _, c := range content {
			blk, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			switch blk["type"] {
			case "tool_use":
				if role != "assistant" {
					t.Errorf("tool_use on non-assistant role %v", role)
				}
				if blk["id"] != "toolu_1" || blk["name"] != "web_search" {
					t.Errorf("tool_use id/name wrong: %v", blk)
				}
				// input must be an OBJECT (not the original string).
				if _, ok := blk["input"].(map[string]interface{}); !ok {
					t.Errorf("tool_use input must be object, got %T: %v", blk["input"], blk["input"])
				}
				foundToolUse = true
			case "tool_result":
				if role != "user" {
					t.Errorf("tool_result must be folded into a user turn, got role %v", role)
				}
				if blk["tool_use_id"] != "toolu_1" {
					t.Errorf("tool_result tool_use_id=%v want toolu_1", blk["tool_use_id"])
				}
				foundToolResult = true
			}
		}
	}
	if !foundToolUse {
		t.Error("no tool_use block found in messages")
	}
	if !foundToolResult {
		t.Error("no tool_result block found in messages")
	}
}

// ---------------------------------------------------------------------------
// Image block wire shape — url nested inside source (NOT block top level)
// ---------------------------------------------------------------------------

// firstUserImageSource finds the first user message and returns its first image
// block's "source" object as a generic map (or nil if none found).
func firstUserImageSource(t *testing.T, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	msgs, ok := m["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages missing: %v", m["messages"])
	}
	for _, mm := range msgs {
		msg, ok := mm.(map[string]interface{})
		if !ok || msg["role"] != "user" {
			continue
		}
		content, _ := msg["content"].([]interface{})
		for _, c := range content {
			blk, ok := c.(map[string]interface{})
			if !ok || blk["type"] != "image" {
				continue
			}
			// The top-level block MUST NOT carry a bare "url" key — it belongs inside
			// source (Anthropic Messages API wire shape).
			if _, leaked := blk["url"]; leaked {
				t.Fatalf("image block leaked top-level url key: %v", blk)
			}
			src, ok := blk["source"].(map[string]interface{})
			if !ok {
				t.Fatalf("image block missing source object: %v", blk)
			}
			return src
		}
	}
	return nil
}

// buildImageReq builds a minimal request whose single user turn carries one
// image_url part.
func buildImageReq(imgURL string) aiservice.ChatRequest {
	return aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{
				Parts: []aiservice.MessagePart{
					{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: imgURL}},
				},
			}},
		},
		MaxTokens: 256,
	}
}

func TestBuildAnthropicRequest_ImageURLNestedInSource(t *testing.T) {
	a := NewClaudeNativeAdapter()

	t.Run("plain_https_url_nests_inside_source", func(t *testing.T) {
		const u = "https://example.com/cat.png"
		m := decodeBody(t, a.buildAnthropicRequest(claudeRoute("off"), buildImageReq(u), false))
		src := firstUserImageSource(t, m)
		if src == nil {
			t.Fatal("no image source block found")
		}
		if src["type"] != "url" {
			t.Errorf("source.type=%v want url", src["type"])
		}
		if src["url"] != u {
			t.Errorf("source.url=%v want %q", src["url"], u)
		}
		// base64-only keys must be omitted on the URL path.
		if _, has := src["data"]; has {
			t.Errorf("source must not carry data on the url path: %v", src)
		}
		if _, has := src["media_type"]; has {
			t.Errorf("source must not carry media_type on the url path: %v", src)
		}
	})

	t.Run("malformed_data_uri_falls_back_to_url_source", func(t *testing.T) {
		// A "data:" URI whose payload is not valid base64 fails splitDataURI and falls
		// back to the url source carrying the original string — still nested in source.
		const u = "data:image/png;base64,!!!not-base64!!!"
		m := decodeBody(t, a.buildAnthropicRequest(claudeRoute("off"), buildImageReq(u), false))
		src := firstUserImageSource(t, m)
		if src == nil {
			t.Fatal("no image source block found")
		}
		if src["type"] != "url" {
			t.Errorf("fallback source.type=%v want url", src["type"])
		}
		if src["url"] != u {
			t.Errorf("fallback source.url=%v want %q", src["url"], u)
		}
	})

	t.Run("valid_data_uri_uses_base64_source", func(t *testing.T) {
		// Regression guard for the base64 path: must remain {type:base64,media_type,data}.
		const u = "data:image/png;base64,aGVsbG8=" // "hello"
		m := decodeBody(t, a.buildAnthropicRequest(claudeRoute("off"), buildImageReq(u), false))
		src := firstUserImageSource(t, m)
		if src == nil {
			t.Fatal("no image source block found")
		}
		if src["type"] != "base64" {
			t.Errorf("source.type=%v want base64", src["type"])
		}
		if src["media_type"] != "image/png" {
			t.Errorf("source.media_type=%v want image/png", src["media_type"])
		}
		if src["data"] != "aGVsbG8=" {
			t.Errorf("source.data=%v want aGVsbG8=", src["data"])
		}
		if _, has := src["url"]; has {
			t.Errorf("base64 source must not carry url key: %v", src)
		}
	})
}

// ---------------------------------------------------------------------------
// Non-stream usage parse (D4)
// ---------------------------------------------------------------------------

func TestParseAnthropicResponse_UsageD4(t *testing.T) {
	a := NewClaudeNativeAdapter()
	raw := []byte(`{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"claude-opus-4-6",
		"stop_reason":"end_turn",
		"content":[{"type":"text","text":"hello world"}],
		"usage":{
			"input_tokens":100,
			"cache_creation_input_tokens":1500,
			"cache_read_input_tokens":2000,
			"output_tokens":42
		}
	}`)
	resp, err := a.parseAnthropicResponse(raw, claudeRoute("claude_ephemeral"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Content != "hello world" {
		t.Errorf("content=%q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason=%q want stop (end_turn→stop)", resp.FinishReason)
	}
	// D4: PromptTokens = input + read + creation.
	if want := 100 + 2000 + 1500; resp.Usage.PromptTokens != want {
		t.Errorf("PromptTokens=%d want %d (input+read+creation)", resp.Usage.PromptTokens, want)
	}
	if resp.Usage.CachedPromptTokens != 2000 {
		t.Errorf("CachedPromptTokens=%d want 2000 (read)", resp.Usage.CachedPromptTokens)
	}
	if resp.Usage.CacheCreationTokens != 1500 {
		t.Errorf("CacheCreationTokens=%d want 1500 (creation)", resp.Usage.CacheCreationTokens)
	}
	if resp.Usage.CompletionTokens != 42 {
		t.Errorf("CompletionTokens=%d want 42", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != resp.Usage.PromptTokens+42 {
		t.Errorf("TotalTokens=%d want %d", resp.Usage.TotalTokens, resp.Usage.PromptTokens+42)
	}
	// PromptTokens invariant: cw + cr + normal == PromptTokens.
	cw := resp.Usage.CacheCreationTokens
	cr := resp.Usage.CachedPromptTokens
	normal := resp.Usage.PromptTokens - cw - cr
	if normal != 100 {
		t.Errorf("normal(non-cached)=%d want 100", normal)
	}
}

func TestParseAnthropicResponse_FinishReasonMap(t *testing.T) {
	a := NewClaudeNativeAdapter()
	cases := map[string]string{
		"end_turn":      "stop",
		"max_tokens":    "length",
		"tool_use":      "tool_calls",
		"stop_sequence": "stop",
	}
	for stop, want := range cases {
		raw := []byte(`{"content":[{"type":"text","text":"x"}],"stop_reason":"` + stop + `","usage":{"input_tokens":1,"output_tokens":1}}`)
		resp, err := a.parseAnthropicResponse(raw, claudeRoute("off"))
		if err != nil {
			t.Fatalf("parse %s: %v", stop, err)
		}
		if resp.FinishReason != want {
			t.Errorf("stop_reason %q → %q want %q", stop, resp.FinishReason, want)
		}
	}
}

func TestParseAnthropicResponse_ToolUse(t *testing.T) {
	a := NewClaudeNativeAdapter()
	raw := []byte(`{
		"content":[
			{"type":"text","text":"let me search"},
			{"type":"tool_use","id":"toolu_9","name":"web_search","input":{"q":"go"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":5}
	}`)
	resp, err := a.parseAnthropicResponse(raw, claudeRoute("off"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool_calls len=%d want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_9" || tc.Function.Name != "web_search" {
		t.Errorf("tool call id/name: %+v", tc)
	}
	// Arguments must be a re-serialised JSON STRING (OAI shape).
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("Arguments not valid JSON string: %q err=%v", tc.Function.Arguments, err)
	}
	if args["q"] != "go" {
		t.Errorf("args=%v", args)
	}
}

// ---------------------------------------------------------------------------
// Streaming — finding #3 capture semantics
// ---------------------------------------------------------------------------

// collectChunks drains a chunk channel into a slice.
func collectChunks(ch <-chan aiservice.ChatChunk) []aiservice.ChatChunk {
	var out []aiservice.ChatChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

// runAnthStream is a small wrapper that pipes an SSE transcript through the
// adapter's streaming reader.
func runAnthStream(transcript string, model string) []aiservice.ChatChunk {
	ch := make(chan aiservice.ChatChunk, 64)
	r := io.NopCloser(strings.NewReader(transcript))
	go runAnthropicStream(r, ch, "claude-native", model, nil)
	return collectChunks(ch)
}

func terminalChunk(t *testing.T, chunks []aiservice.ChatChunk) aiservice.ChatChunk {
	t.Helper()
	finals := 0
	var term aiservice.ChatChunk
	for _, c := range chunks {
		if c.IsFinal {
			finals++
			term = c
		}
	}
	if finals != 1 {
		t.Fatalf("expected exactly 1 IsFinal chunk, got %d", finals)
	}
	return term
}

// Case (i): max()-prompt-side capture survives a duplicated/corrected usage chunk.
func TestRunAnthropicStream_MaxPromptCapture(t *testing.T) {
	// message_start carries the prompt-side buckets; a LATER message_delta echoes a
	// corrected (larger) input_tokens AND a stray 0 cache_read — the 0 must not
	// clobber the earlier non-zero capture, and the larger value must win.
	transcript := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":80,"cache_creation_input_tokens":1000,"cache_read_input_tokens":2000,"output_tokens":1}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":100,"cache_creation_input_tokens":1000,"cache_read_input_tokens":0,"output_tokens":50}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	chunks := runAnthStream(transcript, "claude-opus-4-6")
	term := terminalChunk(t, chunks)
	if term.Usage == nil {
		t.Fatal("terminal usage nil")
	}
	// input max(80,100)=100; creation 1000; read max(2000,0)=2000 (stray 0 ignored).
	if want := 100 + 1000 + 2000; term.Usage.PromptTokens != want {
		t.Errorf("PromptTokens=%d want %d", term.Usage.PromptTokens, want)
	}
	if term.Usage.CachedPromptTokens != 2000 {
		t.Errorf("CachedPromptTokens=%d want 2000 (stray 0 must not clobber)", term.Usage.CachedPromptTokens)
	}
	if term.Usage.CacheCreationTokens != 1000 {
		t.Errorf("CacheCreationTokens=%d want 1000", term.Usage.CacheCreationTokens)
	}
	if term.Usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens=%d want 50", term.Usage.CompletionTokens)
	}
	if term.FinishReason != "stop" {
		t.Errorf("finish=%q want stop", term.FinishReason)
	}
}

// Case (ii): output_tokens last-largest, not summed.
func TestRunAnthropicStream_OutputLastLargest(t *testing.T) {
	transcript := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`,
		``,
		// First message_delta with a partial output count.
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":30}}`,
		``,
		// Second (final) message_delta with the larger total — must WIN, not sum to 60.
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":55}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	chunks := runAnthStream(transcript, "claude-opus-4-6")
	term := terminalChunk(t, chunks)
	if term.Usage == nil {
		t.Fatal("terminal usage nil")
	}
	if term.Usage.CompletionTokens != 55 {
		t.Errorf("CompletionTokens=%d want 55 (last-largest, NOT summed to 85)", term.Usage.CompletionTokens)
	}
	if term.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens=%d want 10", term.Usage.PromptTokens)
	}
}

// Case (iii): a NO-usage transcript ⇒ post-stream PromptTokens=0 explicitly.
func TestRunAnthropicStream_NoUsageGuard(t *testing.T) {
	transcript := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	chunks := runAnthStream(transcript, "claude-opus-4-6")
	term := terminalChunk(t, chunks)
	if term.Usage == nil {
		t.Fatal("terminal usage should be non-nil with explicit 0 prompt tokens")
	}
	if term.Usage.PromptTokens != 0 {
		t.Errorf("PromptTokens=%d want 0 (no-usage guard, never bill phantom tokens)", term.Usage.PromptTokens)
	}
	// Content should still have been streamed.
	var got strings.Builder
	for _, c := range chunks {
		got.WriteString(c.Delta)
	}
	if got.String() != "hello" {
		t.Errorf("delta content=%q want hello", got.String())
	}
}

// Tool round-trip over the stream: input_json_delta fragments accumulate into the
// terminal ToolCall Arguments without re-parsing.
func TestRunAnthropicStream_ToolRoundTrip(t *testing.T) {
	transcript := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_42","name":"run_python","input":{}}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"print(1)\"}"}}`,
		``,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	chunks := runAnthStream(transcript, "claude-opus-4-6")
	term := terminalChunk(t, chunks)
	if len(term.ToolCalls) != 1 {
		t.Fatalf("terminal tool_calls len=%d want 1", len(term.ToolCalls))
	}
	tc := term.ToolCalls[0]
	if tc.ID != "toolu_42" || tc.Function.Name != "run_python" {
		t.Errorf("tool id/name: %+v", tc)
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("accumulated Arguments not valid JSON: %q err=%v", tc.Function.Arguments, err)
	}
	if args["code"] != "print(1)" {
		t.Errorf("args=%v want code=print(1)", args)
	}
	if term.FinishReason != "tool_calls" {
		t.Errorf("finish=%q want tool_calls", term.FinishReason)
	}
}

// Error event → single terminal chunk with Err set.
func TestRunAnthropicStream_ErrorEvent(t *testing.T) {
	transcript := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		``,
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"server overloaded"}}`,
		``,
	}, "\n")

	chunks := runAnthStream(transcript, "claude-opus-4-6")
	term := terminalChunk(t, chunks)
	if term.Err == nil {
		t.Fatal("expected terminal Err on error event")
	}
	if !strings.Contains(strings.ToLower(term.Err.Error()), "overloaded") {
		t.Errorf("err=%v want mention of overloaded", term.Err)
	}
}

// Ensure the global-flag helper is honoured by the gating used in build (smoke).
func TestBuildAnthropicRequest_GlobalFlagIndependentOfBuilder(t *testing.T) {
	// buildAnthropicRequest takes an explicit cacheOn argument; the 3-layer AND
	// (global flag + policy + per-call) is computed by the Chat/ChatStream caller.
	// This smoke test only locks that the builder respects its own bool argument.
	viper.Set("features.provider_prompt_cache.enabled", true)
	defer viper.Set("features.provider_prompt_cache.enabled", false)

	a := NewClaudeNativeAdapter()
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: bigSystemText()}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
		},
		MaxTokens: 256,
	}
	on := decodeBody(t, a.buildAnthropicRequest(claudeRoute("claude_ephemeral"), req, true))
	off := decodeBody(t, a.buildAnthropicRequest(claudeRoute("claude_ephemeral"), req, false))
	if !lastSystemBlockHasCacheControl(t, on) {
		t.Error("cacheOn=true should attach cache_control")
	}
	if anySystemBlockHasCacheControl(t, off) {
		t.Error("cacheOn=false should attach nothing")
	}
}
