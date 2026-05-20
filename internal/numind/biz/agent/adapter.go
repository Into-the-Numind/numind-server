package agent

import (
	"context"
	"io"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"numind-server/internal/pkg/aiservice"
)

// aiserviceAdapter implements model.ToolCallingChatModel by bridging Eino's chat model
// interface to the numind-server aiservice.Chat/ChatStream functions. It preserves
// Langfuse trace propagation, billing (Reserve/Reconcile), and route fallback — all of
// which are transparent inside aiservice and require no extra wiring here.
//
// tools is immutable after construction; WithTools returns a new instance (defensive copy),
// making this safe for concurrent use across goroutines.
type aiserviceAdapter struct {
	modelName    string
	taskID       string
	tools        []*schema.ToolInfo // immutable after construction
	systemPrompt string             // #5 skill-system: injected by runner.Run; prepended as messages[0] when set
}

// Compile-time assertion: aiserviceAdapter must satisfy model.ToolCallingChatModel.
var _ einomodel.ToolCallingChatModel = (*aiserviceAdapter)(nil)

// NewAiserviceAdapter creates a new aiserviceAdapter with the given model name and taskID.
// tools is nil by default; call WithTools to bind tool schemas.
func NewAiserviceAdapter(modelName, taskID string) einomodel.ToolCallingChatModel {
	return &aiserviceAdapter{
		modelName: modelName,
		taskID:    taskID,
	}
}

// WithTools returns a new aiserviceAdapter instance with the given tools bound.
// It does NOT modify the receiver, making it safe for concurrent use.
func (a *aiserviceAdapter) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	// Defensive copy: allocate a new slice so caller mutations don't affect this instance.
	cloned := make([]*schema.ToolInfo, len(tools))
	copy(cloned, tools)
	return &aiserviceAdapter{
		modelName:    a.modelName,
		taskID:       a.taskID,
		tools:        cloned,
		systemPrompt: a.systemPrompt,
	}, nil
}

// Generate converts Eino []*schema.Message → aiservice.ChatRequest, calls aiservice.Chat,
// and converts the result back to *schema.Message. Langfuse trace in ctx is forwarded
// transparently through aiservice.
func (a *aiserviceAdapter) Generate(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	req := a.convertToAiserviceRequest(in)
	resp, err := aiservice.Chat(ctx, a.taskID, req)
	if err != nil {
		return nil, err
	}
	return convertToEinoMessage(resp), nil
}

// Stream converts Eino []*schema.Message → aiservice.ChatRequest, calls aiservice.ChatStream,
// and wraps the returned channel into an Eino *schema.StreamReader[*schema.Message].
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
		msgs = append(msgs, aiservice.ChatMessage{
			Role:    convertRole(m.Role),
			Content: aiservice.MessageContent{Text: m.Content},
		})
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
// any tool calls requested by the model.
func convertToEinoMessage(resp *aiservice.ChatResponse) *schema.Message {
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: resp.Content,
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
