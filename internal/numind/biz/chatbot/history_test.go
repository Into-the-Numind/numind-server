package chatbot

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

func TestFetchRecentHistoryReturnsFullSessionHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(&model.ChatbotMessage{}))

	const sessionID uint = 42
	for i := 1; i <= 25; i++ {
		require.NoError(t, db.Create(&model.ChatbotMessage{
			SessionID: sessionID,
			UserID:    7,
			Role:      "user",
			Content:   fmt.Sprintf("message-%02d", i),
			Seq:       i,
		}).Error)
	}

	b := &chatbotBiz{ds: store.NewTestStore(db)}
	history := b.fetchRecentHistory(context.Background(), sessionID)

	require.Len(t, history, 25)
	require.Equal(t, "message-01", history[0].Content)
	require.Equal(t, "message-25", history[24].Content)
}
