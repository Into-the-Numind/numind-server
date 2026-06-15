package chatbot

import (
	"context"
	"testing"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyVisionBillOnly verifies the inline-image routing: bill-only flag is
// injected, ContextFragments cleared, Messages replaced with the vision messages.
func TestApplyVisionBillOnly(t *testing.T) {
	req := &aiservice.ChatRequest{
		Messages:         []aiservice.ChatMessage{{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "old"}}},
		ContextFragments: []cb.ContextFragment{{}}, // non-empty before
	}
	visionMsgs := []aiservice.ChatMessage{{
		Role: aiservice.MessageRoleUser,
		Content: aiservice.MessageContent{Parts: []aiservice.MessagePart{
			{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://x/i.png"}},
		}},
	}}

	ctx := applyVisionBillOnly(context.Background(), req, visionMsgs)

	assert.True(t, aismw.GatewayBillingOnlyFromCtx(ctx), "bill-only must be injected so image Parts survive")
	assert.Nil(t, req.ContextFragments, "ContextFragments must be cleared (fragment renderer would drop image parts)")
	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content.Parts, 1)
	assert.Equal(t, aiservice.MessagePartTypeImageURL, req.Messages[0].Content.Parts[0].Type)
}

func TestBuildVisionMessages_WithKB(t *testing.T) {
	b := &chatbotBiz{}
	config := &model.ChatbotConfig{SystemPrompt: "你是助手"}
	history := []model.ChatbotMessage{
		{Role: "user", Content: "上一句"},
		{Role: "assistant", Content: "上一答"},
	}
	userParts := []aiservice.MessagePart{
		{Type: aiservice.MessagePartTypeText, Text: "看这张图"},
		{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://x/i.png"}},
	}

	msgs := b.buildVisionMessages(config, history, []string{"资料A", "资料B"}, userParts)

	require.Len(t, msgs, 4) // system + 2 history + user
	// system folds grounding + 参考资料 when KB present
	assert.Equal(t, aiservice.MessageRoleSystem, msgs[0].Role)
	assert.Contains(t, msgs[0].Content.Text, "你是助手")
	assert.Contains(t, msgs[0].Content.Text, chatbotGroundingPrompt)
	assert.Contains(t, msgs[0].Content.Text, "参考资料")
	assert.Contains(t, msgs[0].Content.Text, "资料A")
	// history preserved with roles
	assert.Equal(t, aiservice.MessageRoleUser, msgs[1].Role)
	assert.Equal(t, "上一句", msgs[1].Content.Text)
	assert.Equal(t, aiservice.MessageRoleAssistant, msgs[2].Role)
	// last = user message carrying multimodal Parts (not Text)
	last := msgs[3]
	assert.Equal(t, aiservice.MessageRoleUser, last.Role)
	assert.Empty(t, last.Content.Text)
	require.Len(t, last.Content.Parts, 2)
	assert.Equal(t, aiservice.MessagePartTypeImageURL, last.Content.Parts[1].Type)
}

func TestBuildVisionMessages_NoKB(t *testing.T) {
	b := &chatbotBiz{}
	config := &model.ChatbotConfig{SystemPrompt: "SP"}
	msgs := b.buildVisionMessages(config, nil, nil,
		[]aiservice.MessagePart{{Type: aiservice.MessagePartTypeText, Text: "hi"}})

	require.Len(t, msgs, 2) // system + user (no history)
	assert.Equal(t, "SP", msgs[0].Content.Text, "no grounding/参考资料 when no KB chunks")
	assert.NotContains(t, msgs[0].Content.Text, "参考资料")
}

func TestMessageAttachmentsFrom(t *testing.T) {
	assert.Nil(t, messageAttachmentsFrom(nil), "empty → nil so column stays SQL NULL")
	atts := []*model.AgentAttachment{
		{ID: 1, Filename: "a.png", MimeType: "image/png"},
		nil, // skipped
		{ID: 2, Filename: "b.jpg", MimeType: "image/jpeg"},
	}
	got := messageAttachmentsFrom(atts)
	require.Len(t, got, 2)
	assert.Equal(t, uint64(1), got[0].ID)
	assert.Equal(t, "image/png", got[0].MimeType)
	assert.Equal(t, "b.jpg", got[1].Filename)
}
