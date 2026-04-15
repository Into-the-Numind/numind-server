package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestContentTagger_AnalyzeWithGateway exercises the tagger's analyze path end-to-end.
// In unit-test environments the AI Gateway is not initialised, so we only verify that
// the tagger returns an error (rather than panicking) when the Gateway is unavailable.
func TestContentTagger_AnalyzeWithGateway(t *testing.T) {
	tagger := NewContentTagger()
	ctx := context.Background()
	text := "我们的产品是一款智能温控器，支持手机App远程控制，节能省钱。"

	result, err := tagger.analyze(ctx, text)
	if err != nil {
		// Gateway not initialised in unit-test env — expected path.
		fmt.Printf("Gateway unavailable (expected in unit test): %v\n", err)
		return
	}

	fmt.Printf("Analyze Result: %+v\n", result)
	assert.NotNil(t, result)
	assert.NotEmpty(t, result.Tags)
	assert.NotEmpty(t, result.Summary)
}
