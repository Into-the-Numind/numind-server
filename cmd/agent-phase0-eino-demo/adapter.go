// Package main implements the Eino + aiservice integration demo for Phase 0 verification (A2).
//
// adapter.go bridges Eino's ChatModel interface to the numind-server aiservice.Chat/ChatStream
// functions, preserving Langfuse trace propagation, billing (Reserve/Reconcile), and route
// fallback — all of which live inside aiservice and are transparent to this adapter.
package main

import (
	"context"
	"io"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"numind-server/internal/pkg/aiservice"
)

const demoTaskID = "phase0-eino-demo"

// AiserviceAdapter implements the deprecated model.ChatModel interface (which still works in Eino
// v0.8.13 and is accepted by react.AgentConfig.Model). Langfuse trace propagates through ctx —
// no extra wiring needed because aiservice.Chat reads tc := langfuse.FromContext(ctx) internally.
type AiserviceAdapter struct {
	// modelName is passed as ChatRequest.ModelOverride so that aiservice routes to the correct
	// provider. Leave empty to use the task profile's default service.
	modelName string
}

// Compile-time assertion: AiserviceAdapter must satisfy model.ChatModel.
var _ einomodel.ChatModel = (*AiserviceAdapter)(nil)

// BindTools satisfies the deprecated model.ChatModel interface. For this demo the tools are
// declared in react.AgentConfig.ToolsConfig — the ChatModel itself does not need to manage them.
// A full production implementation would convert schema.ToolInfo → aiservice.Tool and stash them;
// here we accept the call silently to satisfy the interface.
func (a *AiserviceAdapter) BindTools(tools []*schema.ToolInfo) error {
	// No-op for demo: react.AgentConfig wires tools through ToolsNodeConfig, not through the model.
	// The model only needs to understand the tool schema when the framework calls Generate/Stream
	// after it has already embedded tool definitions in the message (via system prompt or provider
	// native tool-use API). For this demo we rely on a simple system message path.
	return nil
}

// Generate converts Eino schema.Message → aiservice.ChatRequest, calls aiservice.Chat (3-arg
// form), and converts the result back to *schema.Message. Langfuse trace in ctx is forwarded.
func (a *AiserviceAdapter) Generate(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	req := convertToAiserviceRequest(in, a.modelName)
	resp, err := aiservice.Chat(ctx, demoTaskID, req)
	if err != nil {
		return nil, err
	}
	return convertToEinoMessage(resp), nil
}

// Stream converts Eino schema.Message → aiservice.ChatRequest, calls aiservice.ChatStream
// (3-arg form), and wraps the returned channel into an Eino *schema.StreamReader.
func (a *AiserviceAdapter) Stream(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	req := convertToAiserviceRequest(in, a.modelName)
	ch, err := aiservice.ChatStream(ctx, demoTaskID, req)
	if err != nil {
		return nil, err
	}
	return wrapChannelAsStreamReader(ch), nil
}

// convertToAiserviceRequest converts Eino []*schema.Message to aiservice.ChatRequest.
func convertToAiserviceRequest(in []*schema.Message, modelName string) aiservice.ChatRequest {
	msgs := make([]aiservice.ChatMessage, 0, len(in))
	for _, m := range in {
		msgs = append(msgs, aiservice.ChatMessage{
			Role:    convertRole(m.Role),
			Content: aiservice.MessageContent{Text: m.Content},
		})
	}
	req := aiservice.ChatRequest{
		Messages: msgs,
	}
	if modelName != "" {
		req.ModelOverride = modelName
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

// convertToEinoMessage converts aiservice.ChatResponse to *schema.Message.
func convertToEinoMessage(resp *aiservice.ChatResponse) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: resp.Content,
	}
}

// wrapChannelAsStreamReader wraps a <-chan aiservice.ChatChunk into a *schema.StreamReader[*schema.Message].
// It launches a goroutine that reads from the channel and writes to the Eino pipe.
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
		// Send EOF via io.EOF to signal normal stream end.
		sw.Send(nil, io.EOF)
	}()
	return sr
}
