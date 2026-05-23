package agent

import (
	"context"
	"io"
	"sync"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/biz/agent/callctx"
	"numind-server/internal/pkg/aiservice"
)

// chatFn is the package-level seam used by tests to mock aiservice.Chat without
// starting a live gateway. Production code leaves this pointing at aiservice.Chat.
var chatFn = aiservice.Chat

// Usage records token consumption from a single aiservice.Chat call.
// Stashed by Generate keyed by call-id (from callctx.CallIDFromCtx).
// budgetgate.PostToolCall (#14/A8b) reads via LookupUsage to drive RecordUsage.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Model            string
	Provider         string
}

// aiserviceAdapter implements model.ToolCallingChatModel by bridging Eino's chat model
// interface to the numind-server aiservice.Chat/ChatStream functions. It preserves
// Langfuse trace propagation, billing (Reserve/Reconcile), and route fallback — all of
// which are transparent inside aiservice and require no extra wiring here.
//
// tools is immutable after construction; WithTools returns a new instance (defensive copy),
// making this safe for concurrent use across goroutines.
//
// usageStore holds Usage records keyed by call-id (16 hex chars).
// Lives on the adapter instance because (a) it is per-run state, (b) Eino
// passes the same adapter to multiple Generate calls in one ReAct loop.
// sync.Map chosen for concurrent Generate + LookupUsage from different goroutines.
//
// Note: usageStore is intentionally NOT cloned by WithTools — both old and new
// adapter instances share the same map. This is safe because each Eino agent
// uses one adapter instance per run; concurrent runs use distinct adapters
// constructed at the top of Run().
type aiserviceAdapter struct {
	modelName    string
	taskID       string
	tools        []*schema.ToolInfo // immutable after construction
	systemPrompt string             // #5 skill-system: injected by runner.Run; prepended as messages[0] when set
	usageStore   *sync.Map          // keyed by call-id string → Usage
}

// Compile-time assertion: aiserviceAdapter must satisfy model.ToolCallingChatModel.
var _ einomodel.ToolCallingChatModel = (*aiserviceAdapter)(nil)

// NewAiserviceAdapter creates a new aiserviceAdapter with the given model name and taskID.
// tools is nil by default; call WithTools to bind tool schemas.
func NewAiserviceAdapter(modelName, taskID string) einomodel.ToolCallingChatModel {
	return &aiserviceAdapter{
		modelName:  modelName,
		taskID:     taskID,
		usageStore: &sync.Map{},
	}
}

// WithTools returns a new aiserviceAdapter instance with the given tools bound.
// It does NOT modify the receiver, making it safe for concurrent use.
// usageStore pointer is preserved (shared) so budgetgate can look up usage
// on either the original or the tool-bound clone.
func (a *aiserviceAdapter) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	// Defensive copy: allocate a new slice so caller mutations don't affect this instance.
	cloned := make([]*schema.ToolInfo, len(tools))
	copy(cloned, tools)
	return &aiserviceAdapter{
		modelName:    a.modelName,
		taskID:       a.taskID,
		tools:        cloned,
		systemPrompt: a.systemPrompt,
		usageStore:   a.usageStore, // shared pointer — safe per-run isolation
	}, nil
}

// Generate converts Eino []*schema.Message → aiservice.ChatRequest, calls aiservice.Chat,
// and converts the result back to *schema.Message. Langfuse trace in ctx is forwarded
// transparently through aiservice.
// After a successful call, stashes Usage keyed by the call-id injected via callctx.
//
// Task 1.5: the aiservice.Chat call is wrapped by callAIServiceWithStripRetry which
// detects "multimodal not supported" errors and retries once with image parts stripped.
// This is a defence-in-depth layer; it triggers only when the capability matrix
// (Task 1.1) has a gap for the active model.
func (a *aiserviceAdapter) Generate(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	req := a.convertToAiserviceRequest(in)
	// Task 1.5: use the strip-retry wrapper instead of bare chatFn.
	resp, err := callAIServiceWithStripRetry(ctx, a.taskID, req, a.modelName)
	if err != nil {
		return nil, err
	}
	// Stash token usage keyed by call-id so PostToolCall (#14/A8b) can correlate.
	// Guard nil: manually-constructed adapters in tests may omit usageStore.
	if callID := callctx.CallIDFromCtx(ctx); callID != "" && a.usageStore != nil {
		a.usageStore.Store(callID, Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			Model:            resp.Model,
			Provider:         resp.Provider,
		})
	}
	return convertToEinoMessage(resp), nil
}

// LookupUsage returns the Usage stashed by the most recent Generate call that used
// the given call-id. Returns (Usage{}, false) if not found or if usageStore is nil.
func (a *aiserviceAdapter) LookupUsage(callID string) (Usage, bool) {
	if a.usageStore == nil {
		return Usage{}, false
	}
	v, ok := a.usageStore.Load(callID)
	if !ok {
		return Usage{}, false
	}
	u, ok := v.(Usage)
	return u, ok
}

// Stream converts Eino []*schema.Message → aiservice.ChatRequest, calls aiservice.ChatStream,
// and wraps the returned channel into an Eino *schema.StreamReader[*schema.Message].
//
// Note: strip-retry is not applied to Stream because Eino ReAct uses Generate only.
// If Stream is ever wired to ReAct in the future, wrap with
// callAIServiceWithStripRetry to maintain Layer 4 defense.
func (a *aiserviceAdapter) Stream(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	req := a.convertToAiserviceRequest(in)
	ch, err := aiservice.ChatStream(ctx, a.taskID, req)
	if err != nil {
		return nil, err
	}
	return wrapChannelAsStreamReader(ch), nil
}

// convertToAiserviceRequest converts Eino []*schema.Message to aiservice.ChatRequest,
// including any tool schemas bound to this adapter instance.
func (a *aiserviceAdapter) convertToAiserviceRequest(in []*schema.Message) aiservice.ChatRequest {
	msgs := make([]aiservice.ChatMessage, 0, len(in)+1)
	// #5 skill-system: prepend systemPrompt as messages[0] when set by runner.Run.
	// If caller already passed a system message in `in`, the runner-injected prompt
	// still goes first (LLM treats first system message as authoritative).
	if a.systemPrompt != "" {
		msgs = append(msgs, aiservice.ChatMessage{
			Role:    aiservice.MessageRoleSystem,
			Content: aiservice.MessageContent{Text: a.systemPrompt},
		})
	}
	for _, m := range in {
		am := aiservice.ChatMessage{
			Role:    convertRole(m.Role),
			Content: aiservice.MessageContent{Text: m.Content},
		}
		// ReAct loop: when Eino reposts a tool-result message back to the model,
		// the upstream OpenAI-compatible API requires tool_call_id. When the
		// previous assistant turn requested tool invocations, the tool_calls
		// array must be re-sent so the provider can correlate the response.
		// Without these, DMXAPI / Ali / Volc return HTTP 400 and runner terminates
		// with model_error before any tool result ever lands.
		if m.ToolCallID != "" {
			am.ToolCallID = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			am.ToolCalls = make([]aiservice.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, aiservice.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: aiservice.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
		// Thinking-mode passthrough: DMXAPI deepseek-v4-pro and AiHubMix
		// reasoning models require reasoning_content from prior assistant turns
		// to be echoed back. Without this the provider returns HTTP 400
		// ("The reasoning_content in the thinking mode must be passed back").
		// Empty string on non-thinking models is harmless (omitempty drops it).
		if m.ReasoningContent != "" {
			am.ReasoningContent = m.ReasoningContent
		}
		msgs = append(msgs, am)
	}

	req := aiservice.ChatRequest{
		Messages: msgs,
	}
	if a.modelName != "" {
		req.ModelOverride = a.modelName
	}
	// Convert bound Eino ToolInfo → aiservice.Tool for function-calling.
	if len(a.tools) > 0 {
		req.Tools = convertToolInfos(a.tools)
	}
	return req
}

// convertRole maps Eino schema.RoleType to aiservice.MessageRole.
func convertRole(r schema.RoleType) aiservice.MessageRole {
	switch r {
	case schema.System:
		return aiservice.MessageRoleSystem
	case schema.Assistant:
		return aiservice.MessageRoleAssistant
	case schema.Tool:
		return aiservice.MessageRoleTool
	default:
		return aiservice.MessageRoleUser
	}
}

// convertToEinoMessage converts aiservice.ChatResponse to *schema.Message, including
// any tool calls requested by the model and the thinking-mode reasoning content
// when the provider returned it.
//
// ReasoningContent must survive the response→schema.Message hop so the ReAct
// loop can echo it back on the next request (thinking-mode providers like
// DMXAPI deepseek-v4-pro require this; see ChatMessage.ReasoningContent docs).
func convertToEinoMessage(resp *aiservice.ChatResponse) *schema.Message {
	msg := &schema.Message{
		Role:             schema.Assistant,
		Content:          resp.Content,
		ReasoningContent: resp.ReasoningContent,
	}
	if len(resp.ToolCalls) > 0 {
		msg.ToolCalls = make([]schema.ToolCall, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: schema.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
	}
	return msg
}

// convertToolInfos converts Eino []*schema.ToolInfo to aiservice []Tool for the ChatRequest.
func convertToolInfos(infos []*schema.ToolInfo) []aiservice.Tool {
	tools := make([]aiservice.Tool, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		t := aiservice.Tool{
			Type: "function",
			Function: aiservice.ToolFunction{
				Name:        info.Name,
				Description: info.Desc,
			},
		}
		// Convert ParamsOneOf → JSON Schema map for the aiservice.ToolFunction.Parameters.
		if info.ParamsOneOf != nil {
			if js, err := info.ParamsOneOf.ToJSONSchema(); err == nil && js != nil {
				// Serialise through a map so aiservice receives plain map[string]interface{}.
				params := map[string]interface{}{
					"type":       "object",
					"properties": js.Properties,
				}
				// required is needed by OpenAI-compatible strict function-calling mode
				// (Task 7a reviewer N1): without it the model can omit mandatory params
				// and the backend returns 400 instead of being pre-blocked by the LLM.
				if len(js.Required) > 0 {
					params["required"] = js.Required
				}
				t.Function.Parameters = params
			}
		}
		tools = append(tools, t)
	}
	return tools
}

// wrapChannelAsStreamReader wraps a <-chan aiservice.ChatChunk into a
// *schema.StreamReader[*schema.Message] via an Eino Pipe. A goroutine reads
// from the channel and forwards chunks; io.EOF signals normal stream end.
func wrapChannelAsStreamReader(ch <-chan aiservice.ChatChunk) *schema.StreamReader[*schema.Message] {
	sr, sw := schema.Pipe[*schema.Message](16)
	go func() {
		defer sw.Close()
		for chunk := range ch {
			if chunk.Err != nil {
				sw.Send(nil, chunk.Err)
				return
			}
			if chunk.IsFinal {
				break
			}
			msg := &schema.Message{
				Role:    schema.Assistant,
				Content: chunk.Delta,
			}
			if closed := sw.Send(msg, nil); closed {
				// Consumer closed the reader early; drain channel to avoid goroutine leak.
				for range ch { //nolint:revive
				}
				return
			}
		}
		// Signal normal stream end.
		sw.Send(nil, io.EOF)
	}()
	return sr
}
