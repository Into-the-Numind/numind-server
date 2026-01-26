package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContentTagger_CallDMXAPI(t *testing.T) {
	tagger := NewContentTagger(nil)
	prompt := "你好，请简单回答'收到'。"

	resp, err := tagger.callDMXAPI(prompt)
	if err != nil {
		t.Fatalf("callDMXAPI failed: %v", err)
	}

	fmt.Printf("DMXAPI Response: %s\n", resp)
	assert.NotEmpty(t, resp)
}

func TestContentTagger_AnalyzeWithDMX(t *testing.T) {
	tagger := NewContentTagger(nil)
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
