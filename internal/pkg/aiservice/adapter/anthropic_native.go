package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/aierr"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
)

// anthropic_native.go is the Claude-native adapter: it issues calls in
// Anthropic's native Messages format (POST {BaseURL}/v1/messages) so cache_control
// can be attached to a stable system-block prefix and the response cache token
// buckets (cache_creation_input_tokens / cache_read_input_tokens) become visible
// for billing — neither of which the OpenAI-compatible /chat/completions path can
// surface.
//
// T5 implements the full request build (incl. the D6 3-layer cache_control gating
// and the D8 ~1024-token economic guard), non-stream parse (D4 token
// normalisation), SSE streaming (5B grammar with the finding-#3 DEFENSIVE usage
// capture), and OAI↔Anthropic tool translation (tool_use / tool_result).
//
// Nothing routes to this adapter unless an admin activates a claude-native
// llm_provider row (is_active=1) and repoints a route at it (T8 two-step
// activation); until then this code is dormant.

const (
	// anthropicVersion is the required anthropic-version header value (proven
	// against DMXAPI's /v1/messages this session). Anthropic versions its wire
	// format by date; 2023-06-01 is the stable Messages-API version.
	anthropicVersion = "2023-06-01"

	// anthropicMessagesPath is the native Messages endpoint suffix appended to
	// route.Provider.BaseURL (e.g. https://www.dmxapi.cn → .../v1/messages).
	anthropicMessagesPath = "/v1/messages"

	// anthropicPostMaxRetries mirrors dmxapiPostMaxRetries: dmxapi.cn is a
	// third-party aggregator with occasional transient header-timeout blips, and a
	// non-streaming Messages body can be safely replayed.
	anthropicPostMaxRetries = 3

	// cacheControlMinChars is the D8 economic guard. Anthropic silently no-ops the
	// ephemeral cache below ~1024 tokens but the FIRST call still pays the creation
	// premium → pure loss. We attach cache_control only when the stable prefix
	// passes this cheap char-based heuristic. ~3000 chars ≈ 1024 tokens CJK-aware
	// (Chinese averages ~0.6 tok/char, English ~0.25 tok/char; 3000 chars clears
	// 1024 tokens in either regime).
	cacheControlMinChars = 3000
)

// Compile-time interface guards. These prove the struct SATISFIES the interfaces
// (D1 belt-and-suspenders against a missing-registration silent degrade); the
// runtime startup assertion (assertNativeAdaptersRegistered) proves it was
// actually registered into the running gateway.
var _ aiservice.ChatProvider = (*ClaudeNativeAdapter)(nil)
var _ ChatAdapter = (*ClaudeNativeAdapter)(nil)

// ClaudeNativeAdapter speaks the Anthropic Messages wire format.
type ClaudeNativeAdapter struct {
	// client serves non-streaming calls (one-shot body — the 600s LLMConfig total
	// timeout is a safe cap). Mirrors DMXAPIAdapter.client.
	client *httpclient.Client
	// streamClient serves streaming calls with LLMStreamConfig (no total request
	// timeout) so a long Claude thinking stream is not cut at 600s (prod incident
	// 2026-06-16). Caution B of D7. Mirrors DMXAPIAdapter.streamClient.
	streamClient *httpclient.Client
}

// NewClaudeNativeAdapter builds the adapter with the same two-client http split
// the dmxapi adapter uses (copied from dmxapi.go:105-114).
func NewClaudeNativeAdapter() *ClaudeNativeAdapter {
	return &ClaudeNativeAdapter{
		client:       httpclient.NewClient(httpclient.LLMConfig()),
		streamClient: httpclient.NewClient(httpclient.LLMStreamConfig()),
	}
}

// Name returns the adapter identifier. MUST equal the llm_provider.name of the
// native Claude provider row and the literal in KnownNativeProviderNames() — the
// gateway keys the per-route adapter on Provider.Name. "claude-native" is chosen
// so "dmxapi" is NOT a prefix of it (D1 naming-hazard mitigation).
func (a *ClaudeNativeAdapter) Name() string { return "claude-native" }

// ProviderType returns the provider category.
func (a *ClaudeNativeAdapter) ProviderType() string { return "anthropic" }

// Capabilities lists the capabilities this adapter supports.
func (a *ClaudeNativeAdapter) Capabilities() []string { return []string{"chat"} }

// ----------------------------------------------------------------------------
// Anthropic wire types
// ----------------------------------------------------------------------------

// cacheControl is the {"type":"ephemeral"} marker attached to a stable prefix
// (system block) to request a 5-min ephemeral cache.
type cacheControl struct {
	Type string `json:"type"`
}

// anthropicSystemBlock is one element of the `system` ARRAY. cache_control is a
// pointer so it is OMITTED entirely when caching is off → wire-identical to a
// non-cached Claude call.
type anthropicSystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// anthropicContentBlock is one block inside a message's content array. The fields
// are a superset over text / image / tool_use / tool_result; omitempty keeps each
// emitted block to only the keys its type needs.
type anthropicContentBlock struct {
	Type string `json:"type"`
	// text block
	Text string `json:"text,omitempty"`
	// image block (the url, for a plain http(s) image, lives INSIDE source — the
	// Anthropic Messages API nests it as source.url, NOT at the block top level).
	Source *anthropicImageSource `json:"source,omitempty"`
	// tool_use block
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result block
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// anthropicImageSource carries an image reference. For a base64 data URI it splits
// into media_type+data (Type="base64"); for a plain http(s) URL it carries the URL
// inline (Type="url", URL=u) — Anthropic nests the url INSIDE source.
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicMessage is one element of the `messages` array.
type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

// anthropicTool is one element of the `tools` array. Anthropic uses input_schema,
// NOT function.parameters.
type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}

// anthropicRequest is the native Messages request body.
type anthropicRequest struct {
	Model       string                 `json:"model"`
	MaxTokens   int                    `json:"max_tokens"`
	System      []anthropicSystemBlock `json:"system,omitempty"`
	Messages    []anthropicMessage     `json:"messages"`
	Tools       []anthropicTool        `json:"tools,omitempty"`
	Temperature *float64               `json:"temperature,omitempty"`
	Stream      bool                   `json:"stream,omitempty"`
}

// anthropicUsage mirrors the response usage object (and the streaming usage object).
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// anthropicResponse is the non-streaming response body.
type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Content    []anthropicContentBlock `json:"content"`
	Usage      *anthropicUsage         `json:"usage"`
	Error      *anthropicError         `json:"error"`
}

// anthropicError is the structured error object (200-with-error and non-2xx alike).
type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ----------------------------------------------------------------------------
// Request build
// ----------------------------------------------------------------------------

// buildAnthropicRequest assembles the native Messages request body. cacheOn is the
// already-AND-ed 3-layer toggle decision (D6) computed by the caller
// (claudeCacheOn). When cacheOn AND the concatenated system text clears the D8
// ~1024-token guard, cache_control:{"type":"ephemeral"} is attached to the LAST
// system block; otherwise NO cache_control appears anywhere (wire-identical to a
// non-cached call).
func (a *ClaudeNativeAdapter) buildAnthropicRequest(
	route *registry.ResolvedRoute,
	req aiservice.ChatRequest,
	cacheOn bool,
) anthropicRequest {
	thinkingOn := req.Thinking && route.SupportsThinking

	body := anthropicRequest{
		Model:     route.ProviderModelID,
		MaxTokens: req.MaxTokens,
		System:    buildAnthropicSystem(req.Messages, cacheOn),
		Messages:  buildAnthropicMessages(req.Messages),
		Tools:     buildAnthropicTools(req.Tools),
	}

	// Temperature: omit when 0 (provider default). When thinking is enabled Claude
	// requires temperature=1 (mirror the dmxapi Claude+thinking rule), so force it.
	switch {
	case thinkingOn:
		t := 1.0
		body.Temperature = &t
	case req.Temperature > 0:
		t := req.Temperature
		body.Temperature = &t
	}

	return body
}

// buildAnthropicSystem concatenates all role=system messages into the `system`
// ARRAY of text blocks. When cacheOn AND the concatenated text clears the D8
// char guard, cache_control is attached to the LAST block. Returns nil when there
// is no system text (omitempty drops the field).
func buildAnthropicSystem(msgs []aiservice.ChatMessage, cacheOn bool) []anthropicSystemBlock {
	var texts []string
	for _, m := range msgs {
		if m.Role != aiservice.MessageRoleSystem {
			continue
		}
		if t := systemMessageText(m); t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) == 0 {
		return nil
	}

	blocks := make([]anthropicSystemBlock, 0, len(texts))
	for _, t := range texts {
		blocks = append(blocks, anthropicSystemBlock{Type: "text", Text: t})
	}

	// D8 economic guard: only attach cache_control when the stable prefix is large
	// enough that the cache is not silently no-op'd (which would waste the creation
	// premium). Estimate over the WHOLE concatenated system text (that is the prefix
	// the cache marker on the last block covers).
	if cacheOn {
		total := 0
		for _, t := range texts {
			total += len([]rune(t))
		}
		if total >= cacheControlMinChars {
			blocks[len(blocks)-1].CacheControl = &cacheControl{Type: "ephemeral"}
		}
	}
	return blocks
}

// systemMessageText extracts the plain text from a system message (text field or
// concatenated text parts).
func systemMessageText(m aiservice.ChatMessage) string {
	if len(m.Content.Parts) > 0 {
		var b strings.Builder
		for _, p := range m.Content.Parts {
			if p.Type == aiservice.MessagePartTypeText {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return m.Content.Text
}

// buildAnthropicMessages maps every non-system ChatMessage to an Anthropic
// message. Consecutive role=tool results are merged into a SINGLE user turn's
// content array (Anthropic ordering: tool_result must immediately follow the
// assistant tool_use turn, and parallel results belong in one user turn).
func buildAnthropicMessages(msgs []aiservice.ChatMessage) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case aiservice.MessageRoleSystem:
			continue // handled in buildAnthropicSystem

		case aiservice.MessageRoleTool:
			// Merge this and any immediately-following tool results into one user turn.
			blocks := []anthropicContentBlock{toolResultBlock(m)}
			for i+1 < len(msgs) && msgs[i+1].Role == aiservice.MessageRoleTool {
				i++
				blocks = append(blocks, toolResultBlock(msgs[i]))
			}
			out = append(out, anthropicMessage{Role: "user", Content: blocks})

		case aiservice.MessageRoleAssistant:
			out = append(out, anthropicMessage{Role: "assistant", Content: assistantBlocks(m)})

		default: // user
			out = append(out, anthropicMessage{Role: "user", Content: userBlocks(m)})
		}
	}
	return out
}

// toolResultBlock folds a role=tool message into a tool_result content block.
func toolResultBlock(m aiservice.ChatMessage) anthropicContentBlock {
	return anthropicContentBlock{
		Type:      "tool_result",
		ToolUseID: m.ToolCallID,
		Content:   messagePlainText(m),
	}
}

// assistantBlocks builds the content blocks for an assistant turn: text first (if
// any), then one tool_use block per ToolCall (Anthropic input is an OBJECT, so the
// OAI Arguments STRING is unmarshalled back to raw JSON).
func assistantBlocks(m aiservice.ChatMessage) []anthropicContentBlock {
	var blocks []anthropicContentBlock
	if txt := messagePlainText(m); txt != "" {
		blocks = append(blocks, anthropicContentBlock{Type: "text", Text: txt})
	}
	for _, tc := range m.ToolCalls {
		input := json.RawMessage(strings.TrimSpace(tc.Function.Arguments))
		if len(input) == 0 || !json.Valid(input) {
			// Defensive: a malformed/empty arguments string still serialises as an
			// empty object so the request does not 400 on a missing input.
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, anthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	// An assistant turn must carry at least one block.
	if len(blocks) == 0 {
		blocks = append(blocks, anthropicContentBlock{Type: "text", Text: ""})
	}
	return blocks
}

// userBlocks builds the content blocks for a user turn: text and/or image parts.
func userBlocks(m aiservice.ChatMessage) []anthropicContentBlock {
	if len(m.Content.Parts) > 0 {
		blocks := make([]anthropicContentBlock, 0, len(m.Content.Parts))
		for _, p := range m.Content.Parts {
			switch p.Type {
			case aiservice.MessagePartTypeText:
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: p.Text})
			case aiservice.MessagePartTypeImageURL:
				if p.ImageURL != nil {
					blocks = append(blocks, imageBlock(p.ImageURL.URL))
				}
			}
		}
		if len(blocks) == 0 {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: ""})
		}
		return blocks
	}
	return []anthropicContentBlock{{Type: "text", Text: m.Content.Text}}
}

// imageBlock maps an image reference to an Anthropic image block. A base64 data
// URI is split into media_type + data; a plain http(s) URL uses the url source.
func imageBlock(u string) anthropicContentBlock {
	if strings.HasPrefix(u, "data:") {
		if mediaType, data, ok := splitDataURI(u); ok {
			return anthropicContentBlock{
				Type: "image",
				Source: &anthropicImageSource{
					Type:      "base64",
					MediaType: mediaType,
					Data:      data,
				},
			}
		}
	}
	return anthropicContentBlock{
		Type: "image",
		Source: &anthropicImageSource{
			Type: "url",
			URL:  u,
		},
	}
}

// splitDataURI parses "data:<media_type>;base64,<data>" into (media_type, data).
func splitDataURI(u string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(u, prefix) {
		return "", "", false
	}
	rest := u[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	meta := rest[:comma]
	data = rest[comma+1:]
	meta = strings.TrimSuffix(meta, ";base64")
	if meta == "" {
		meta = "image/png"
	}
	// Validate the payload is decodable so a malformed URI does not 400 the call.
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return "", "", false
	}
	return meta, data, true
}

// messagePlainText extracts plain text from a message (text field or text parts).
func messagePlainText(m aiservice.ChatMessage) string {
	if len(m.Content.Parts) > 0 {
		var b strings.Builder
		for _, p := range m.Content.Parts {
			if p.Type == aiservice.MessagePartTypeText {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return m.Content.Text
}

// buildAnthropicTools maps OAI tools to Anthropic tools (input_schema). Returns
// nil for empty input so `tools` is omitted (preserves the non-tool wire shape).
func buildAnthropicTools(tools []aiservice.Tool) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	return out
}

// ----------------------------------------------------------------------------
// Cache toggle (D6 3-layer AND gate)
// ----------------------------------------------------------------------------

// claudeCacheOn computes the D6 3-layer AND gate for the Claude ephemeral cache:
//
//	globalOn        = features.provider_prompt_cache.enabled (Layer 1)
//	policyAllows    = route.PromptCachePolicy ∈ {claude_ephemeral, auto} (Layer 2)
//	callerAsserts   = req.EnablePromptCache (Layer 3 — creation premium → caller
//	                  must assert prefix reuse)
//
// ALL three must agree. False ⇒ the builder omits cache_control entirely ⇒
// wire-identical to a non-cached Claude call. Default-deny: an absent global flag,
// an off/unknown policy, or a single-shot caller (EnablePromptCache=false) each
// keeps caching off.
func claudeCacheOn(route *registry.ResolvedRoute, req aiservice.ChatRequest) bool {
	if !aiservice.PromptCacheGloballyEnabled() {
		return false
	}
	switch route.PromptCachePolicy {
	case "claude_ephemeral", "auto":
		// policy allows
	default:
		return false
	}
	return req.EnablePromptCache
}

// ----------------------------------------------------------------------------
// finish-reason mapping
// ----------------------------------------------------------------------------

// mapAnthropicStopReason maps an Anthropic stop_reason to the unified finish
// reason vocabulary used across adapters.
func mapAnthropicStopReason(stop string) string {
	switch stop {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "":
		return ""
	default:
		return stop
	}
}

// ----------------------------------------------------------------------------
// Chat (non-stream)
// ----------------------------------------------------------------------------

// Chat performs a non-streaming Messages call.
func (a *ClaudeNativeAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	cacheOn := claudeCacheOn(route, req)
	body, err := json.Marshal(a.buildAnthropicRequest(route, req, cacheOn))
	if err != nil {
		return nil, fmt.Errorf("claude-native.Chat: marshal: %w", err)
	}

	respBytes, err := a.doPost(ctx, route, body)
	if err != nil {
		return nil, fmt.Errorf("claude-native.Chat: %w", err)
	}

	return a.parseAnthropicResponse(respBytes, route)
}

// parseAnthropicResponse decodes a non-streaming Messages response into the
// unified ChatResponse, applying the D4 PromptTokens normalisation.
func (a *ClaudeNativeAdapter) parseAnthropicResponse(respBytes []byte, route *registry.ResolvedRoute) (*aiservice.ChatResponse, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("claude-native.Chat: decode: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("claude-native.Chat: %w",
			aierr.New(0, "", resp.Error.Type, resp.Error.Message, nil))
	}

	var contentText strings.Builder
	var toolCalls []aiservice.ToolCall
	for _, blk := range resp.Content {
		switch blk.Type {
		case "text":
			contentText.WriteString(blk.Text)
		case "tool_use":
			toolCalls = append(toolCalls, aiservice.ToolCall{
				ID:   blk.ID,
				Type: "function",
				Function: aiservice.ToolCallFunction{
					Name:      blk.Name,
					Arguments: rawInputToArgs(blk.Input),
				},
			})
		}
	}

	usage := aiservice.TokenUsage{}
	if resp.Usage != nil {
		usage = anthropicUsageToTokenUsage(*resp.Usage)
	}

	model := resp.Model
	if model == "" {
		model = route.ProviderModelID
	}

	return &aiservice.ChatResponse{
		Content:      contentText.String(),
		ToolCalls:    toolCalls,
		FinishReason: mapAnthropicStopReason(resp.StopReason),
		Usage:        usage,
		Model:        model,
		Provider:     a.Name(),
	}, nil
}

// rawInputToArgs re-serialises an Anthropic tool_use input OBJECT back to the OAI
// Arguments STRING. An empty/invalid input becomes "{}".
func rawInputToArgs(input json.RawMessage) string {
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return "{}"
	}
	return trimmed
}

// anthropicUsageToTokenUsage applies the D4 normalisation: Anthropic reports the
// three buckets DISJOINT, so PromptTokens = input + read + creation. The 3-bucket
// cost formula carves read+write back OUT of PromptTokens, so the unified prompt
// total MUST be their sum.
func anthropicUsageToTokenUsage(u anthropicUsage) aiservice.TokenUsage {
	prompt := assembleAnthropicPromptTokens(u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	return aiservice.TokenUsage{
		PromptTokens:        prompt,
		CompletionTokens:    u.OutputTokens,
		TotalTokens:         prompt + u.OutputTokens,
		CachedPromptTokens:  u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens,
	}
}

// ----------------------------------------------------------------------------
// ChatStream
// ----------------------------------------------------------------------------

// ChatStream starts a streaming Messages call.
func (a *ClaudeNativeAdapter) ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	cacheOn := claudeCacheOn(route, req)
	body := a.buildAnthropicRequest(route, req, cacheOn)
	body.Stream = true

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("claude-native.ChatStream: marshal: %w", err)
	}

	httpResp, err := a.doStream(ctx, route, raw)
	if err != nil {
		return nil, fmt.Errorf("claude-native.ChatStream: %w", err)
	}

	ch := make(chan aiservice.ChatChunk, 64)
	go runAnthropicStream(httpResp.Body, ch, a.Name(), route.ProviderModelID, nil)
	return ch, nil
}

// ----------------------------------------------------------------------------
// SSE streaming (5B grammar; finding-#3 defensive usage capture)
// ----------------------------------------------------------------------------

// anthropicStreamEvent is the union of fields across the Anthropic SSE event
// types (message_start / content_block_start / content_block_delta /
// content_block_stop / message_delta / message_stop / error / ping). omitempty +
// pointers let one struct decode them all.
type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *anthropicError `json:"error"`
}

// runAnthropicStream reads an Anthropic SSE Messages stream from r and emits
// aiservice.ChatChunk values, closing ch when the stream ends or errors.
//
// It mirrors runOAIStream's contract: content deltas with IsFinal=false, exactly
// ONE terminal IsFinal=true chunk carrying the mapped finish_reason, assembled
// tool_calls, and the assembled Usage. It reuses the idle watchdog + 1MB scanner.
//
// Token-usage capture is DEFENSIVE (finding #3): usage fields may appear on
// message_start AND be re-sent/corrected by a DMXAPI proxy in later chunks. We
// keep running accumulators and apply max() on EVERY usage-bearing chunk so a
// stray 0 never clobbers an earlier non-zero capture and the largest value wins.
// output_tokens is captured last-largest from message_delta (never summed). If no
// prompt-side usage is ever observed, the terminal chunk carries an EXPLICIT
// PromptTokens=0 (never bill phantom tokens) plus a WARN.
func runAnthropicStream(
	r io.ReadCloser,
	ch chan<- aiservice.ChatChunk,
	provider string,
	defaultModel string,
	traceMeta *aiservice.TraceMetadata,
) {
	defer r.Close()
	defer close(ch)

	var idleWatcher *streamIdleWatcher
	if idle := streamIdleTimeout(); idle > 0 {
		var stopWatcher func()
		idleWatcher, stopWatcher = startStreamIdleWatcher(r, idle)
		defer stopWatcher()
	}

	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1*1024*1024)

	index := 0
	// Defensive usage accumulators (finding #3). -1 sentinel means "never observed"
	// so the post-stream no-usage guard can distinguish "0 reported" from "absent".
	capPrompt, capCreation, capRead, capOutput := 0, 0, 0, 0
	usageSeen := false
	finishReason := ""
	// pendingToolCalls accumulates per-index tool_use blocks. input_json_delta
	// fragments are concatenated into Arguments WITHOUT re-parsing.
	pendingToolCalls := map[int]*aiservice.ToolCall{}

	// foldUsage applies max() semantics to all four accumulators from a usage object.
	foldUsage := func(u *anthropicUsage) {
		if u == nil {
			return
		}
		if u.InputTokens > 0 || u.CacheCreationInputTokens > 0 || u.CacheReadInputTokens > 0 {
			usageSeen = true
		}
		capPrompt = maxNonZeroInt(capPrompt, u.InputTokens)
		capCreation = maxNonZeroInt(capCreation, u.CacheCreationInputTokens)
		capRead = maxNonZeroInt(capRead, u.CacheReadInputTokens)
		capOutput = maxNonZeroInt(capOutput, u.OutputTokens)
	}

	// assembleTerminalUsage builds the terminal Usage via the SAME D4 formula
	// (PromptTokens = capPrompt + capCreation + capRead). When no prompt-side usage
	// was ever seen, PromptTokens is EXPLICITLY 0 (it already is) and a WARN fires —
	// either way a non-nil Usage with explicit 0 prompt tokens bills at 0, never at
	// full price on phantom tokens.
	assembleTerminalUsage := func() *aiservice.TokenUsage {
		if !usageSeen {
			log.Warnw("claude-native stream: no prompt-side usage observed; billing 0 prompt tokens",
				"model", defaultModel)
		}
		prompt := assembleAnthropicPromptTokens(capPrompt, capRead, capCreation)
		return &aiservice.TokenUsage{
			PromptTokens:        prompt,
			CompletionTokens:    capOutput,
			TotalTokens:         prompt + capOutput,
			CachedPromptTokens:  capRead,
			CacheCreationTokens: capCreation,
		}
	}

	// emitTerminal builds, sends, and (the caller returns after) the single
	// IsFinal=true chunk with assembled tool_calls.
	emitTerminal := func(reason string, err error) {
		term := aiservice.ChatChunk{
			Index:         index,
			FinishReason:  reason,
			IsFinal:       true,
			Usage:         assembleTerminalUsage(),
			Provider:      provider,
			Model:         defaultModel,
			Err:           err,
			TraceMetadata: traceMeta,
		}
		if len(pendingToolCalls) > 0 {
			indexes := make([]int, 0, len(pendingToolCalls))
			for i := range pendingToolCalls {
				indexes = append(indexes, i)
			}
			sort.Ints(indexes)
			tcs := make([]aiservice.ToolCall, 0, len(indexes))
			for _, i := range indexes {
				tcs = append(tcs, *pendingToolCalls[i])
			}
			term.ToolCalls = tcs
		}
		ch <- term
	}

	for scanner.Scan() {
		if idleWatcher != nil {
			idleWatcher.mark()
		}
		line := scanner.Text()

		// Anthropic SSE carries `event: <name>` and `data: <json>` lines. We only
		// need the data lines — the JSON itself carries a `type` field. Skip blanks,
		// comments, and the event: lines.
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			emitTerminal(fmt.Sprintf("parse_error: %v", err),
				fmt.Errorf("aiservice stream parse error: %w", err))
			return
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				foldUsage(ev.Message.Usage)
			}

		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				tc := pendingToolCalls[ev.Index]
				if tc == nil {
					tc = &aiservice.ToolCall{Type: "function"}
					pendingToolCalls[ev.Index] = tc
				}
				if ev.ContentBlock.ID != "" {
					tc.ID = ev.ContentBlock.ID
				}
				if ev.ContentBlock.Name != "" {
					tc.Function.Name = ev.ContentBlock.Name
				}
			}

		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					ch <- aiservice.ChatChunk{
						Delta:    ev.Delta.Text,
						Index:    index,
						IsFinal:  false,
						Provider: provider,
						Model:    defaultModel,
					}
					index++
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					ch <- aiservice.ChatChunk{
						ReasoningDelta: ev.Delta.Thinking,
						Index:          index,
						IsFinal:        false,
						Provider:       provider,
						Model:          defaultModel,
					}
					index++
				}
			case "input_json_delta":
				// Accumulate per-index tool Arguments as a concatenated JSON string;
				// do NOT re-parse (the fragments are only valid once concatenated).
				if ev.Delta.PartialJSON != "" {
					tc := pendingToolCalls[ev.Index]
					if tc == nil {
						tc = &aiservice.ToolCall{Type: "function"}
						pendingToolCalls[ev.Index] = tc
					}
					tc.Function.Arguments += ev.Delta.PartialJSON
				}
			case "signature_delta":
				// ignore
			}

		case "content_block_stop":
			// nothing to flush — blocks are accumulated in place.

		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				finishReason = mapAnthropicStopReason(ev.Delta.StopReason)
			}
			// output_tokens (and any echoed prompt-side fields) via max() — never sum.
			foldUsage(ev.Usage)

		case "message_stop":
			emitTerminal(finishReason, nil)
			return

		case "error":
			msg := "anthropic stream error"
			typ := ""
			if ev.Error != nil {
				msg = ev.Error.Message
				typ = ev.Error.Type
			}
			emitTerminal(fmt.Sprintf("error: %s", msg),
				fmt.Errorf("claude-native stream: %w", aierr.New(0, "", typ, msg, nil)))
			return

		case "ping":
			// ignore keepalive
		}
	}

	// Idle-timeout: the watchdog closed the body after `idle` of no data.
	if idleWatcher != nil && idleWatcher.tripped.Load() {
		emitTerminal(fmt.Sprintf("idle_timeout: no stream data for %s", idleWatcher.idle),
			fmt.Errorf("aiservice stream idle timeout: no data for %s: %w", idleWatcher.idle, errno.ErrAIProviderTimeout))
		return
	}

	if err := scanner.Err(); err != nil {
		emitTerminal(fmt.Sprintf("scan_error: %v", err),
			fmt.Errorf("aiservice stream scan error: %w", err))
		return
	}

	// Stream ended without an explicit message_stop (e.g. [DONE] or clean EOF):
	// still emit exactly one terminal chunk.
	emitTerminal(finishReason, nil)
}

// ----------------------------------------------------------------------------
// HTTP plumbing (x-api-key + anthropic-version; NOT Bearer)
// ----------------------------------------------------------------------------

// anthropicHeaders builds the native Messages headers. Anthropic uses x-api-key
// (NOT Authorization: Bearer) on /v1/messages, plus the required anthropic-version.
func anthropicHeaders(apiKey string, stream bool) map[string]string {
	h := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         apiKey,
		"anthropic-version": anthropicVersion,
	}
	if stream {
		h["Accept"] = "text/event-stream"
	}
	return h
}

// doPost sends a non-streaming Messages POST and returns the full response body.
func (a *ClaudeNativeAdapter) doPost(ctx context.Context, route *registry.ResolvedRoute, body []byte) ([]byte, error) {
	url := route.Provider.BaseURL + anthropicMessagesPath

	resp, err := a.client.Do(&httpclient.Request{
		Method:      "POST",
		URL:         url,
		Body:        bytes.NewReader(body),
		Context:     ctx,
		Headers:     anthropicHeaders(route.Provider.APIKey, false),
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: anthropicPostMaxRetries},
	})
	if err != nil {
		return nil, wrapHTTPClientErr("claude-native doPost", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr("claude-native doPost", resp.StatusCode, b)
	}
	return io.ReadAll(resp.Body)
}

// doStream sends a streaming Messages POST and returns the raw *http.Response.
// The caller closes resp.Body. Retries are disabled (a streaming body cannot be
// replayed). Uses streamClient (no total timeout) so a long thinking stream is not
// truncated mid-read (Caution B of D7).
func (a *ClaudeNativeAdapter) doStream(ctx context.Context, route *registry.ResolvedRoute, body []byte) (*http.Response, error) {
	url := route.Provider.BaseURL + anthropicMessagesPath

	resp, err := a.streamClient.Do(&httpclient.Request{
		Method:      "POST",
		URL:         url,
		Body:        bytes.NewReader(body),
		Context:     ctx,
		Headers:     anthropicHeaders(route.Provider.APIKey, true),
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return nil, wrapHTTPClientErr("claude-native doStream", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, wrapHTTPStatusErr("claude-native doStream", resp.StatusCode, b)
	}
	return resp, nil
}
