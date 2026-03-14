package service

import (
	"context"
	"fmt"
	"testing"

	"numind-server/internal/numind/biz/salesrag/adapter"

	"github.com/stretchr/testify/assert"
)

func TestContentTagger_CallDMXAPI(t *testing.T) {
	tagger := NewContentTagger()
	prompt := "你好，请简单回答'收到'。"

	messages := []adapter.ChatMessage{{Role: "user", Content: prompt}}
	resp, _, err := tagger.dmxClient.ChatCompletion(context.Background(), "qwen-turbo-latest", messages, 0.1, 1024)
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	fmt.Printf("DMXAPI Response: %s\n", resp)
	assert.NotEmpty(t, resp)
}

func TestContentTagger_AnalyzeWithDMX(t *testing.T) {
	tagger := NewContentTagger()
	ctx := context.Background()
	text := "我们的产品是一款智能温控器，支持手机App远程控制，节能省钱。"

	result, err := tagger.analyze(ctx, text)
	if err != nil {
		t.Fatalf("Analyze with DMX failed: %v", err)
	}

	fmt.Printf("Analyze Result: %+v\n", result)
	assert.NotNil(t, result)
	assert.NotEmpty(t, result.Tags)
	assert.NotEmpty(t, result.Summary)
}
