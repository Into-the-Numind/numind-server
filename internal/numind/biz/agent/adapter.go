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

// chatStreamFn is the package-level seam used by tests to mock aiservice.ChatStream
// without starting a live gateway. Production code leaves this pointing at
// aiservice.ChatStream. Mirrors the chatFn pattern.
var chatStreamFn = aiservice.ChatStream

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

	// maxOutputTokens caps the model's per-turn output (req.MaxTokens). Resolved
	// once at construction from the agent model's capability_json.max_output_tokens
	// (clamped to a safe range). 0 → leave unset (provider default). Without this,
	// a thinking model (deepseek-v4-pro emits reasoning FIRST, tool_calls LAST) can
	// exhaust the provider's default output budget on reasoning and truncate the
	// trailing tool call mid-JSON → dev run 133 ("unexpected end of JSON input").
	maxOutputTokens int

	// V1.5 v2-compact-adapter-integration — V2 compact hook（适配 Eino per-ReAct-round
	// 的 Generate 调用层）。nil → V2 prevention chain 不启用，行为退化为"直通 LLM"。
	// 由 runner.go 在 useCompactV2 == true 时注入；通过 WithTools 复制实例时共享指针
	// （保 per-Run 状态一致性，与 usageStore 同理）。
	compactor *adapterCompactor
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
		compactor:    a.compactor,  // shared per-Run state (consecutiveFailures 等)
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
	// V1.5 v2-compact-adapter-integration — PRE-CALL V2 compact：
	// 每一轮 ReAct 调 LLM 之前评估 in 的 token 用量，达 85% → 跑 L3 autocompact
	// 替换 in 成 [system, summary, recent 5]；达 95% + 连续 3 次失败 → ErrContextExhausted。
	// V2 prevention 真正生效的位置就是这里（之前在 runner outer loop 因 run.Messages
	// 为空而长期 dormant）。
	//
	// compactor == nil 时整段跳过 → 行为完全等价于 V2 集成前。
	if a.compactor != nil {
		compacted, didCompact, cerr := a.compactor.MaybeCompact(ctx, in)
		if cerr != nil {
			// ErrContextExhausted 或其他不可恢复错误 → 上抛让 runner terminate
			return nil, cerr
		}
		if didCompact {
			in = compacted
		}
	}

	req := a.convertToAiserviceRequest(in)
	// Task 1.5: use the strip-retry wrapper instead of bare chatFn.
	resp, err := callAIServiceWithStripRetry(ctx, a.taskID, req, a.modelName)

	// V1.5 v2-compact-adapter-integration — POST-CALL PTL recovery：
	// aiservice 返回 prompt_too_long → 强制 autocompact 一次 + retry。
	// 这是替代已删的 V1 ReactiveCompact 链的唯一恢复机制；MaybeCompact 的 85%
	// prevention 失败（如 contextWindow 配错或 char/4 估算严重偏差）时这里兜底。
	if err != nil && a.compactor != nil && isPromptTooLongErr(err) {
		recovered, didCompact, rerr := a.compactor.ForcePTLRecover(ctx, in)
		if rerr != nil {
			// PTL recovery 自己失败（如 ErrContextExhausted）→ 上抛 recovery error
			return nil, rerr
		}
		if didCompact {
			// 用 compact 后的 in 重试一次
			req = a.convertToAiserviceRequest(recovered)
			resp, err = callAIServiceWithStripRetry(ctx, a.taskID, req, a.modelName)
		}
		// 重试仍失败 → 让原 err 冒泡（保持错误链可追溯）
	}

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
	ch, err := chatStreamFn(ctx, a.taskID, req)
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
//
// Forwarded fields (consumeEinoStream reads all four):
//   - chunk.Delta            -> msg.Content
//   - chunk.ReasoningDelta   -> msg.ReasoningContent
//   - chunk.ToolCalls        -> msg.ToolCalls (assembled on terminal chunk)
//   - chunk.FinishReason     -> msg.ResponseMeta.FinishReason
//
// Before the 2026-05-28 fix this only forwarded chunk.Delta — so any LLM
// response that emitted reasoning_content (thinking models) or tool_calls
// (function-calling) instead of plain content was silently dropped at the
// eino boundary. react.Agent.Stream() received zero schema.Message chunks,
// the SSE consumer terminated with step_count=0, and the frontend showed an
// empty assistant bubble (dev agent_run 48/54, agent_id=100003 web_search).
func wrapChannelAsStreamReader(ch <-chan aiservice.ChatChunk) *schema.StreamReader[*schema.Message] {
	sr, sw := schema.Pipe[*schema.Message](16)
	go func() {
		defer sw.Close()
		for chunk := range ch {
			if chunk.Err != nil {
				sw.Send(nil, chunk.Err)
				return
			}

			// Terminal chunk carries finish_reason + assembled tool_calls.
			// Forward both as a final schema.Message so consumeEinoStream can
			// dispatch tool calls and observe the step boundary before EOF.
			if chunk.IsFinal {
				if chunk.FinishReason == "" && len(chunk.ToolCalls) == 0 {
					break
				}
				finalMsg := &schema.Message{Role: schema.Assistant}
				if chunk.FinishReason != "" {
					finalMsg.ResponseMeta = &schema.ResponseMeta{FinishReason: chunk.FinishReason}
				}
				if len(chunk.ToolCalls) > 0 {
					finalMsg.ToolCalls = make([]schema.ToolCall, 0, len(chunk.ToolCalls))
					for _, tc := range chunk.ToolCalls {
						finalMsg.ToolCalls = append(finalMsg.ToolCalls, schema.ToolCall{
							ID:   tc.ID,
							Type: tc.Type,
							Function: schema.FunctionCall{
								Name:      tc.Function.Name,
								Arguments: tc.Function.Arguments,
							},
						})
					}
				}
				_ = sw.Send(finalMsg, nil)
				break
			}

			// Interim chunk — skip if neither content nor reasoning carried
			// data so we don't emit no-op messages downstream.
			if chunk.Delta == "" && chunk.ReasoningDelta == "" {
				continue
			}
			msg := &schema.Message{
				Role:             schema.Assistant,
				Content:          chunk.Delta,
				ReasoningContent: chunk.ReasoningDelta,
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
