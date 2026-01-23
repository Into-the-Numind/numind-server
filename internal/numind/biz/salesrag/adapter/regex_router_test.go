package adapter_test

import (
	"context"
	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/port"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegexRouter_AnalyzeIntent(t *testing.T) {
	router := adapter.NewRegexRouter()
	ctx := context.Background()

	// 1. Test ChitChat
	intent, rewrite, err := router.AnalyzeIntent(ctx, "你好", nil)
	assert.Nil(t, err)
	assert.Equal(t, port.IntentChitChat, intent)
	assert.Empty(t, rewrite)

	// 2. Test Ambiguous (Pronouns)
	intent, rewrite, err = router.AnalyzeIntent(ctx, "它多少钱？", []string{"上文中提到的产品A"})
	assert.Nil(t, err)
	assert.Equal(t, port.IntentAmbiguous, intent)
	// mock router returns query as is for rewrite if logic is simple, or specific string
	// For simple regex router we expect it to identify ambiguity but maybe not perfect rewrite without LLM

	// 3. Test Direct
	intent, _, err = router.AnalyzeIntent(ctx, "产品A的价格是多少", nil)
	assert.Nil(t, err)
	assert.Equal(t, port.IntentDirect, intent)
}
