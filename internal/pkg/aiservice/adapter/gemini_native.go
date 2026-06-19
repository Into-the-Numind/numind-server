package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/aierr"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
)

// gemini_native.go is the Gemini-native adapter: it issues calls in Gemini's
// native generateContent format (POST {BaseURL}/v1beta/models/{model}:generateContent
// ?key={APIKey}) so the implicit-cache token count (usageMetadata.cachedContentTokenCount)
// becomes visible for billing. Gemini implicit caching is automatic on 2.5+; there
// is no explicit cachedContents API on DMXAPI (404), so the cache "toggle" for
// Gemini is simply whether a route is pointed at this adapter at all (D6). We never
// send cachedContent.
//
// T6 implements the ?key= URL build with the fullURL/redactedURL split (finding
// #4 — the key never reaches an error/log surface), the
// systemInstruction/contents/tools translation, the non-stream usage parse (D5 —
// promptTokenCount ALREADY includes cachedContentTokenCount, so NO double-count),
// SSE streaming, and the STATELESS functionResponse name recovery (finding #6 — a
// backward scan of req.Messages by ToolCallID, NOT an ephemeral per-request map).
//
// Nothing routes to this adapter unless an admin activates a gemini-native
// llm_provider row (is_active=1) and repoints a route at it (T8 two-step
// activation); until then this code is dormant.

const (
	// geminiV1betaPrefix is the path prefix appended to route.Provider.BaseURL
	// (e.g. https://www.dmxapi.cn → .../v1beta/models/{model}:{method}).
	geminiV1betaPrefix = "/v1beta/models/"

	// geminiPostMaxRetries mirrors anthropicPostMaxRetries: dmxapi.cn is a
	// third-party aggregator with occasional transient header-timeout blips, and a
	// non-streaming generateContent body can be safely replayed.
	geminiPostMaxRetries = 3

	// geminiUnknownToolName is the defensive fallback name used for a tool result
	// whose ToolCallID matches no prior functionCall and which carries no Name
	// (should not happen in a well-formed loop — finding #6). Emitting a literal
	// keeps the request serialisable rather than panicking.
	geminiUnknownToolName = "unknown_tool"
)

// Compile-time interface guards (see anthropic_native.go).
var _ aiservice.ChatProvider = (*GeminiNativeAdapter)(nil)
var _ ChatAdapter = (*GeminiNativeAdapter)(nil)

// GeminiNativeAdapter speaks the Gemini generateContent wire format.
type GeminiNativeAdapter struct {
	// client serves non-streaming calls. Mirrors DMXAPIAdapter.client.
	client *httpclient.Client
	// streamClient serves streaming calls (no total request timeout). Caution B
	// of D7. Mirrors DMXAPIAdapter.streamClient.
	streamClient *httpclient.Client
}

// NewGeminiNativeAdapter builds the adapter with the two-client http split
// (copied from dmxapi.go:105-114).
func NewGeminiNativeAdapter() *GeminiNativeAdapter {
	return &GeminiNativeAdapter{
		client:       httpclient.NewClient(httpclient.LLMConfig()),
		streamClient: httpclient.NewClient(httpclient.LLMStreamConfig()),
	}
}

// Name returns the adapter identifier. MUST equal the llm_provider.name of the
// native Gemini provider row and the literal in KnownNativeProviderNames().
// "gemini-native" is chosen so "dmxapi" is NOT a prefix of it (D1 naming-hazard
// mitigation).
func (a *GeminiNativeAdapter) Name() string { return "gemini-native" }

// ProviderType returns the provider category.
func (a *GeminiNativeAdapter) ProviderType() string { return "gemini" }

// Capabilities lists the capabilities this adapter supports.
func (a *GeminiNativeAdapter) Capabilities() []string { return []string{"chat"} }

// ----------------------------------------------------------------------------
// Gemini wire types
// ----------------------------------------------------------------------------

// geminiPart is one element of a content's parts array. The fields are a
// superset over text / inlineData / functionCall / functionResponse; omitempty
// keeps each emitted part to only the keys its kind needs. The pointers ensure
// an empty struct is omitted rather than serialised as `{}`.
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

// geminiInlineData carries a base64-encoded image (mimeType + data).
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// geminiFunctionCall is the assistant's tool invocation. Args is an OBJECT (the
// OAI Arguments STRING is unmarshalled into it).
type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// geminiFunctionResponse is the tool-result reply. Gemini matches by NAME (not
// id), so name MUST be recovered statelessly from the message history (finding
// #6). response is wrapped in {"result": <text>}.
type geminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// geminiContent is one element of the `contents` array (or the
// `systemInstruction`). Role is "user" or "model" (omitted on systemInstruction).
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

// geminiFunctionDeclaration is one declared tool. Gemini uses `parameters` (the
// raw JSON Schema), unlike Anthropic's input_schema.
type geminiFunctionDeclaration struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// geminiTool wraps the functionDeclarations array (Gemini nests tools one level
// deeper than OAI/Anthropic).
type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

// geminiThinkingConfig toggles thinking on a thinking-capable Gemini model.
type geminiThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts"`
}

// geminiGenerationConfig carries sampling + output-cap knobs.
type geminiGenerationConfig struct {
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

// geminiRequest is the native generateContent request body. NOTE the deliberate
// absence of a cachedContent field — DMXAPI 404s the explicit cachedContents API,
// so we rely on implicit caching only and NEVER send cachedContent (5C).
type geminiRequest struct {
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	Contents          []geminiContent         `json:"contents"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

// geminiUsageMetadata mirrors the response usageMetadata object.
type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

// geminiResponse is the non-streaming (and per-chunk streaming) response body.
type geminiResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
	Error         *geminiError         `json:"error"`
}

// geminiCandidate is one generation candidate. We only ever use candidates[0].
type geminiCandidate struct {
	Content      *geminiRespContent `json:"content"`
	FinishReason string             `json:"finishReason"`
}

// geminiRespContent is a candidate's content (role + parts).
type geminiRespContent struct {
	Role  string           `json:"role"`
	Parts []geminiRespPart `json:"parts"`
}

// geminiRespPart is one response part (text and/or functionCall).
type geminiRespPart struct {
	Text         string                  `json:"text"`
	FunctionCall *geminiRespFunctionCall `json:"functionCall"`
}

// geminiRespFunctionCall is a tool invocation in a response (args is a raw
// OBJECT we re-serialise to the OAI Arguments STRING).
type geminiRespFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// geminiError is the structured error object (200-with-error and non-2xx alike).
type geminiError struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ----------------------------------------------------------------------------
// Request build
// ----------------------------------------------------------------------------

// buildGeminiRequest assembles the native generateContent request body. There is
// NO cache toggle to evaluate here: Gemini implicit caching is automatic on 2.5+
// and cannot be sent/disabled server-side (5C / D6), so the only knob is whether
// a route points at this adapter at all. cachedContent is never sent.
func (a *GeminiNativeAdapter) buildGeminiRequest(
	route *registry.ResolvedRoute,
	req aiservice.ChatRequest,
) geminiRequest {
	body := geminiRequest{
		SystemInstruction: buildGeminiSystemInstruction(req.Messages),
		Contents:          buildGeminiContents(req.Messages),
		Tools:             buildGeminiTools(req.Tools),
	}

	gc := geminiGenerationConfig{MaxOutputTokens: req.MaxTokens}
	if req.Temperature > 0 {
		t := req.Temperature
		gc.Temperature = &t
	}
	// Thinking only when the model supports it, the caller opted in, and the model
	// is NOT intrinsic-thinking (mirror the OAI-wire thinking gate).
	if route.SupportsThinking && req.Thinking && !route.ThinkingOnly {
		gc.ThinkingConfig = &geminiThinkingConfig{IncludeThoughts: true}
	}
	body.GenerationConfig = &gc

	return body
}

// buildGeminiSystemInstruction concatenates all role=system messages into a
// single systemInstruction content. Returns nil when there is no system text
// (omitempty drops the field).
func buildGeminiSystemInstruction(msgs []aiservice.ChatMessage) *geminiContent {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role != aiservice.MessageRoleSystem {
			continue
		}
		if t := systemMessageText(m); t != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(t)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return &geminiContent{Parts: []geminiPart{{Text: b.String()}}}
}

// buildGeminiContents maps every non-system ChatMessage to a gemini content.
// Role map: assistant→model, user/tool→user. Consecutive tool results are folded
// into a SINGLE user content's parts array (Gemini expects functionResponse parts
// to follow the model's functionCall turn).
func buildGeminiContents(msgs []aiservice.ChatMessage) []geminiContent {
	out := make([]geminiContent, 0, len(msgs))

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case aiservice.MessageRoleSystem:
			continue // handled in buildGeminiSystemInstruction

		case aiservice.MessageRoleTool:
			// Merge this and any immediately-following tool results into one user content.
			parts := []geminiPart{toolResponsePart(m, msgs, i)}
			for i+1 < len(msgs) && msgs[i+1].Role == aiservice.MessageRoleTool {
				i++
				parts = append(parts, toolResponsePart(msgs[i], msgs, i))
			}
			out = append(out, geminiContent{Role: "user", Parts: parts})

		case aiservice.MessageRoleAssistant:
			out = append(out, geminiContent{Role: "model", Parts: geminiAssistantParts(m)})

		default: // user
			out = append(out, geminiContent{Role: "user", Parts: geminiUserParts(m)})
		}
	}
	return out
}

// geminiAssistantParts builds the parts for an assistant turn: text first (if
// any), then one functionCall part per ToolCall (the OAI Arguments STRING is
// unmarshalled into an args OBJECT).
func geminiAssistantParts(m aiservice.ChatMessage) []geminiPart {
	var parts []geminiPart
	if txt := messagePlainText(m); txt != "" {
		parts = append(parts, geminiPart{Text: txt})
	}
	for _, tc := range m.ToolCalls {
		args := json.RawMessage(strings.TrimSpace(tc.Function.Arguments))
		if len(args) == 0 || !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		parts = append(parts, geminiPart{
			FunctionCall: &geminiFunctionCall{Name: tc.Function.Name, Args: args},
		})
	}
	if len(parts) == 0 {
		parts = append(parts, geminiPart{Text: ""})
	}
	return parts
}

// geminiUserParts builds the parts for a user turn: text and/or image parts.
func geminiUserParts(m aiservice.ChatMessage) []geminiPart {
	if len(m.Content.Parts) > 0 {
		parts := make([]geminiPart, 0, len(m.Content.Parts))
		for _, p := range m.Content.Parts {
			switch p.Type {
			case aiservice.MessagePartTypeText:
				parts = append(parts, geminiPart{Text: p.Text})
			case aiservice.MessagePartTypeImageURL:
				if p.ImageURL != nil {
					if part, ok := geminiImagePart(p.ImageURL.URL); ok {
						parts = append(parts, part)
					}
				}
			}
		}
		if len(parts) == 0 {
			parts = append(parts, geminiPart{Text: ""})
		}
		return parts
	}
	return []geminiPart{{Text: m.Content.Text}}
}

// geminiImagePart maps an image reference to an inlineData part. Only base64 data
// URIs are supported (Gemini inlineData carries mimeType+data); a plain http(s)
// URL has no native inline form here, so it is dropped (ok=false) — the upstream
// imageutil normalisation already converts images to data URIs for inline send.
func geminiImagePart(u string) (geminiPart, bool) {
	if mediaType, data, ok := splitDataURI(u); ok {
		return geminiPart{InlineData: &geminiInlineData{MimeType: mediaType, Data: data}}, true
	}
	return geminiPart{}, false
}

// toolResponsePart folds a role=tool message into a functionResponse part. The
// tool NAME is recovered STATELESSLY (finding #6): a backward scan of msgs for
// the nearest preceding assistant ToolCall whose ID == m.ToolCallID. This is
// thread-safe (no shared mutable state) and correct across an arbitrary number of
// agent turns AND out-of-order parallel tool results within one turn (each result
// is matched by its own ToolCallID). Fallback when no match: m.Name if present,
// else the literal "unknown_tool" (so the request still serialises).
//
// idx is the index of m within msgs; the scan walks idx-1..0.
func toolResponsePart(m aiservice.ChatMessage, msgs []aiservice.ChatMessage, idx int) geminiPart {
	name := recoverToolName(m, msgs, idx)
	return geminiPart{
		FunctionResponse: &geminiFunctionResponse{
			Name:     name,
			Response: map[string]interface{}{"result": messagePlainText(m)},
		},
	}
}

// recoverToolName statelessly resolves the function name for a tool-result
// message by scanning backwards over msgs for the matching ToolCallID in the
// nearest preceding assistant turn (finding #6).
func recoverToolName(m aiservice.ChatMessage, msgs []aiservice.ChatMessage, idx int) string {
	if m.ToolCallID != "" {
		for j := idx - 1; j >= 0; j-- {
			if msgs[j].Role != aiservice.MessageRoleAssistant {
				continue
			}
			for _, tc := range msgs[j].ToolCalls {
				if tc.ID == m.ToolCallID {
					return tc.Function.Name
				}
			}
		}
	}
	// Defensive fallback — should not happen in a well-formed agent loop.
	if name := toolMessageName(m); name != "" {
		return name
	}
	log.Warnw("gemini-native: could not recover tool name for functionResponse; using fallback",
		"tool_call_id", m.ToolCallID)
	return geminiUnknownToolName
}

// toolMessageName extracts a tool name carried directly on a tool message, if the
// caller set one. The unified ChatMessage has no dedicated Name field, so this is
// a defensive no-op today (returns "") and exists so the fallback ordering in
// recoverToolName matches the spec (msg.Name → "unknown_tool").
func toolMessageName(_ aiservice.ChatMessage) string {
	return ""
}

// buildGeminiTools maps OAI tools to Gemini's nested functionDeclarations shape.
// Returns nil for empty input so `tools` is omitted (preserves the non-tool wire
// shape).
func buildGeminiTools(tools []aiservice.Tool) []geminiTool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]geminiFunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, geminiFunctionDeclaration{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}

// ----------------------------------------------------------------------------
// finish-reason mapping
// ----------------------------------------------------------------------------

// mapGeminiFinishReason maps a Gemini finishReason to the unified finish-reason
// vocabulary. A present functionCall overrides STOP → tool_calls.
func mapGeminiFinishReason(reason string, hasFunctionCall bool) string {
	if hasFunctionCall {
		return "tool_calls"
	}
	switch reason {
	case "STOP", "SAFETY", "RECITATION":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "":
		return ""
	default:
		return strings.ToLower(reason)
	}
}

// synthGeminiToolID synthesises a stable per-response tool-call ID. Gemini does
// not return IDs; downstream OAI-shaped consumers expect a non-empty ID.
func synthGeminiToolID(name string, index int) string {
	return "gemini-call-" + name + strconv.Itoa(index)
}

// ----------------------------------------------------------------------------
// Chat (non-stream)
// ----------------------------------------------------------------------------

// Chat performs a non-streaming generateContent call.
func (a *GeminiNativeAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	body, err := json.Marshal(a.buildGeminiRequest(route, req))
	if err != nil {
		return nil, fmt.Errorf("gemini-native.Chat: marshal: %w", err)
	}

	respBytes, err := a.doPost(ctx, route, body)
	if err != nil {
		return nil, fmt.Errorf("gemini-native.Chat: %w", err)
	}

	return a.parseGeminiResponse(respBytes, route)
}

// parseGeminiResponse decodes a non-streaming generateContent response into the
// unified ChatResponse, applying the D5 token semantics (promptTokenCount ALREADY
// includes cachedContentTokenCount → NO double-count; no creation bucket).
func (a *GeminiNativeAdapter) parseGeminiResponse(respBytes []byte, route *registry.ResolvedRoute) (*aiservice.ChatResponse, error) {
	var resp geminiResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("gemini-native.Chat: decode: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("gemini-native.Chat: %w",
			aierr.New(resp.Error.Code, "", resp.Error.Status, resp.Error.Message, nil))
	}

	var contentText strings.Builder
	var toolCalls []aiservice.ToolCall
	finishReason := ""
	if len(resp.Candidates) > 0 {
		cand := resp.Candidates[0]
		finishReason = cand.FinishReason
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				if part.Text != "" {
					contentText.WriteString(part.Text)
				}
				if part.FunctionCall != nil {
					idx := len(toolCalls)
					toolCalls = append(toolCalls, aiservice.ToolCall{
						ID:   synthGeminiToolID(part.FunctionCall.Name, idx),
						Type: "function",
						Function: aiservice.ToolCallFunction{
							Name:      part.FunctionCall.Name,
							Arguments: rawInputToArgs(part.FunctionCall.Args),
						},
					})
				}
			}
		}
	}

	usage := aiservice.TokenUsage{}
	if resp.UsageMetadata != nil {
		usage = geminiUsageToTokenUsage(*resp.UsageMetadata)
	}

	return &aiservice.ChatResponse{
		Content:      contentText.String(),
		ToolCalls:    toolCalls,
		FinishReason: mapGeminiFinishReason(finishReason, len(toolCalls) > 0),
		Usage:        usage,
		Model:        route.ProviderModelID,
		Provider:     a.Name(),
	}, nil
}

// geminiUsageToTokenUsage applies the D5 normalisation: promptTokenCount is the
// FULL input and ALREADY includes cachedContentTokenCount (a subset), so
// PromptTokens = promptTokenCount (do NOT add cached). Gemini's implicit cache
// has no separate creation bucket → CacheCreationTokens = 0.
func geminiUsageToTokenUsage(u geminiUsageMetadata) aiservice.TokenUsage {
	return aiservice.TokenUsage{
		PromptTokens:        u.PromptTokenCount,
		CompletionTokens:    u.CandidatesTokenCount,
		TotalTokens:         u.TotalTokenCount,
		ReasoningTokens:     u.ThoughtsTokenCount,
		CachedPromptTokens:  u.CachedContentTokenCount,
		CacheCreationTokens: 0,
	}
}

// ----------------------------------------------------------------------------
// ChatStream
// ----------------------------------------------------------------------------

// ChatStream starts a streaming generateContent call.
func (a *GeminiNativeAdapter) ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	raw, err := json.Marshal(a.buildGeminiRequest(route, req))
	if err != nil {
		return nil, fmt.Errorf("gemini-native.ChatStream: marshal: %w", err)
	}

	httpResp, err := a.doStream(ctx, route, raw)
	if err != nil {
		return nil, fmt.Errorf("gemini-native.ChatStream: %w", err)
	}

	ch := make(chan aiservice.ChatChunk, 64)
	go runGeminiStream(httpResp.Body, ch, a.Name(), route.ProviderModelID, nil)
	return ch, nil
}

// ----------------------------------------------------------------------------
// SSE streaming (:streamGenerateContent?alt=sse → `data: {GenerateContentResponse}`)
// ----------------------------------------------------------------------------

// runGeminiStream reads a Gemini SSE stream from r and emits aiservice.ChatChunk
// values, closing ch when the stream ends or errors.
//
// It mirrors runOAIStream's contract: content deltas with IsFinal=false, exactly
// ONE terminal IsFinal=true chunk carrying the mapped finish_reason, assembled
// tool_calls, and the assembled Usage. It reuses the idle watchdog + 1MB scanner.
//
// Each SSE line is a full GenerateContentResponse JSON object. usageMetadata
// normally appears on the FINAL chunk; we fold it via max() on every chunk that
// carries it (defensive, mirrors the Anthropic capture discipline) so a proxy
// re-send cannot clobber an earlier value. functionCall parts arrive whole (not
// fragmented) and are assembled in arrival order. If no usage is ever observed
// the terminal chunk carries an EXPLICIT PromptTokens=0 (never bill phantom
// tokens).
func runGeminiStream(
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
	finishReason := ""
	// Defensive usage accumulators (mirror the Anthropic capture: max() per chunk).
	capPrompt, capCompletion, capTotal, capCached, capThoughts := 0, 0, 0, 0, 0
	usageSeen := false
	// toolCalls accumulates functionCall parts in arrival order (Gemini sends them
	// whole, so no per-index fragment reassembly is required).
	var toolCalls []aiservice.ToolCall

	foldUsage := func(u *geminiUsageMetadata) {
		if u == nil {
			return
		}
		if u.PromptTokenCount > 0 || u.CandidatesTokenCount > 0 || u.TotalTokenCount > 0 || u.CachedContentTokenCount > 0 {
			usageSeen = true
		}
		capPrompt = maxNonZeroInt(capPrompt, u.PromptTokenCount)
		capCompletion = maxNonZeroInt(capCompletion, u.CandidatesTokenCount)
		capTotal = maxNonZeroInt(capTotal, u.TotalTokenCount)
		capCached = maxNonZeroInt(capCached, u.CachedContentTokenCount)
		capThoughts = maxNonZeroInt(capThoughts, u.ThoughtsTokenCount)
	}

	// assembleTerminalUsage builds the terminal Usage via the D5 semantics
	// (PromptTokens = promptTokenCount, NO double-count). When no usage was ever
	// seen, all buckets are explicitly 0 so a non-nil Usage bills at 0 (never full
	// price on phantom tokens).
	assembleTerminalUsage := func() *aiservice.TokenUsage {
		if !usageSeen {
			log.Warnw("gemini-native stream: no usage metadata observed; billing 0 prompt tokens",
				"model", defaultModel)
		}
		return &aiservice.TokenUsage{
			PromptTokens:        capPrompt,
			CompletionTokens:    capCompletion,
			TotalTokens:         capTotal,
			ReasoningTokens:     capThoughts,
			CachedPromptTokens:  capCached,
			CacheCreationTokens: 0,
		}
	}

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
		if len(toolCalls) > 0 {
			term.ToolCalls = toolCalls
		}
		ch <- term
	}

	for scanner.Scan() {
		if idleWatcher != nil {
			idleWatcher.mark()
		}
		line := scanner.Text()

		// Gemini SSE carries `data: <json>` lines (alt=sse). Skip blanks, comments,
		// and any event: lines. The JSON itself is a full GenerateContentResponse.
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

		var resp geminiResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			emitTerminal(fmt.Sprintf("parse_error: %v", err),
				fmt.Errorf("aiservice stream parse error: %w", err))
			return
		}

		if resp.Error != nil {
			emitTerminal(fmt.Sprintf("error: %s", resp.Error.Message),
				fmt.Errorf("gemini-native stream: %w",
					aierr.New(resp.Error.Code, "", resp.Error.Status, resp.Error.Message, nil)))
			return
		}

		foldUsage(resp.UsageMetadata)

		if len(resp.Candidates) > 0 {
			cand := resp.Candidates[0]
			if cand.FinishReason != "" {
				finishReason = cand.FinishReason
			}
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if part.Text != "" {
						ch <- aiservice.ChatChunk{
							Delta:    part.Text,
							Index:    index,
							IsFinal:  false,
							Provider: provider,
							Model:    defaultModel,
						}
						index++
					}
					if part.FunctionCall != nil {
						idx := len(toolCalls)
						toolCalls = append(toolCalls, aiservice.ToolCall{
							ID:   synthGeminiToolID(part.FunctionCall.Name, idx),
							Type: "function",
							Function: aiservice.ToolCallFunction{
								Name:      part.FunctionCall.Name,
								Arguments: rawInputToArgs(part.FunctionCall.Args),
							},
						})
					}
				}
			}
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

	// Stream ended cleanly ([DONE] or EOF): emit exactly one terminal chunk with
	// the finish reason (functionCall present overrides → tool_calls). toolCalls is
	// already in arrival order, which is the order Gemini emits them.
	emitTerminal(mapGeminiFinishReason(finishReason, len(toolCalls) > 0), nil)
}

// ----------------------------------------------------------------------------
// HTTP plumbing (?key= query param; NOT Bearer — finding #4 redaction)
// ----------------------------------------------------------------------------

// buildGeminiURL constructs the generateContent / streamGenerateContent URL with
// the auth key in the `?key=` QUERY param (DMXAPI Gemini rejects Bearer). For a
// streaming call alt=sse is appended. This returns the FULL url (with the live
// key) and is used ONLY as the http request target — every error/log/trace
// surface MUST use redactGeminiURL(...) on the result (finding #4).
func (a *GeminiNativeAdapter) buildGeminiURL(route *registry.ResolvedRoute, method string, stream bool) string {
	url := route.Provider.BaseURL + geminiV1betaPrefix + route.ProviderModelID + ":" + method + "?key=" + route.Provider.APIKey
	if stream {
		url += "&alt=sse"
	}
	return url
}

// geminiHeaders builds the native generateContent headers. The auth key lives in
// the URL (?key=), NOT in any header — so there is deliberately no Authorization /
// x-api-key / x-goog-api-key header (would be rejected by DMXAPI and would also
// risk leaking the key into header logs).
func geminiHeaders(stream bool) map[string]string {
	h := map[string]string{
		"Content-Type": "application/json",
	}
	if stream {
		h["Accept"] = "text/event-stream"
	}
	return h
}

// doPost sends a non-streaming generateContent POST and returns the full body.
// All error surfaces use the redacted URL so the key never leaks (finding #4).
func (a *GeminiNativeAdapter) doPost(ctx context.Context, route *registry.ResolvedRoute, body []byte) ([]byte, error) {
	fullURL := a.buildGeminiURL(route, "generateContent", false)
	redactedURL := redactGeminiURL(fullURL)

	resp, err := a.client.Do(&httpclient.Request{
		Method:      "POST",
		URL:         fullURL,
		Body:        bytes.NewReader(body),
		Context:     ctx,
		Headers:     geminiHeaders(false),
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: geminiPostMaxRetries},
	})
	if err != nil {
		// Go's net/http folds the FULL url (incl. ?key=<live key>) into the
		// transport error string; scrub it BEFORE wrapping so the live key never
		// reaches an error/log/trace surface (finding #4 — P0 leak via url.Error).
		safeErr := sanitizeGeminiTransportErr(route.Provider.APIKey, err)
		return nil, wrapHTTPClientErr("gemini-native doPost ("+redactedURL+")", safeErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr("gemini-native doPost ("+redactedURL+")", resp.StatusCode, b)
	}
	return io.ReadAll(resp.Body)
}

// doStream sends a streaming generateContent POST and returns the raw
// *http.Response. The caller closes resp.Body. Retries are disabled (a streaming
// body cannot be replayed). Uses streamClient (no total timeout) so a long stream
// is not truncated mid-read (Caution B of D7). All error surfaces use the
// redacted URL (finding #4).
func (a *GeminiNativeAdapter) doStream(ctx context.Context, route *registry.ResolvedRoute, body []byte) (*http.Response, error) {
	fullURL := a.buildGeminiURL(route, "streamGenerateContent", true)
	redactedURL := redactGeminiURL(fullURL)

	resp, err := a.streamClient.Do(&httpclient.Request{
		Method:      "POST",
		URL:         fullURL,
		Body:        bytes.NewReader(body),
		Context:     ctx,
		Headers:     geminiHeaders(true),
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		// Scrub the live key out of the transport error before wrapping (finding
		// #4 — Go's url.Error embeds the full ?key=<live key> URL).
		safeErr := sanitizeGeminiTransportErr(route.Provider.APIKey, err)
		return nil, wrapHTTPClientErr("gemini-native doStream ("+redactedURL+")", safeErr)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, wrapHTTPStatusErr("gemini-native doStream ("+redactedURL+")", resp.StatusCode, b)
	}
	return resp, nil
}
