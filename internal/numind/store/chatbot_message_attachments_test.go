package store

import (
	"context"
	"testing"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestChatbotMessage_AttachmentsRoundTrip verifies the serializer:json field
// round-trips through CreateMessage → ListMessages (chatbot-image-recognition T2).
func TestChatbotMessage_AttachmentsRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/cm.db?_busy_timeout=5000"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ChatbotMessage{}))

	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	userMsg := &model.ChatbotMessage{
		SessionID: 1, UserID: 7, Role: "user", Content: "看图", Seq: 1,
		Attachments: []model.MessageAttachment{
			{ID: 11, Filename: "chart.png", MimeType: "image/png"},
			{ID: 12, Filename: "photo.jpg", MimeType: "image/jpeg"},
		},
	}
	require.NoError(t, s.CreateMessage(ctx, userMsg))

	asstMsg := &model.ChatbotMessage{SessionID: 1, UserID: 7, Role: "assistant", Content: "这是柱状图", Seq: 2}
	require.NoError(t, s.CreateMessage(ctx, asstMsg))

	msgs, total, err := s.ListMessages(ctx, 1, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, msgs, 2)

	// user message (seq 1) attachments round-trip
	require.Len(t, msgs[0].Attachments, 2)
	assert.Equal(t, uint64(11), msgs[0].Attachments[0].ID)
	assert.Equal(t, "chart.png", msgs[0].Attachments[0].Filename)
	assert.Equal(t, "image/png", msgs[0].Attachments[0].MimeType)
	assert.Equal(t, uint64(12), msgs[0].Attachments[1].ID)

	// assistant message (seq 2) has no attachments
	assert.Empty(t, msgs[1].Attachments)
}
