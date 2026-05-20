package compact

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateTokens_ASCII(t *testing.T) {
	// "hello" = 5 chars * 0.25 = 1.25 → 1
	assert.Equal(t, 1, EstimateTokens("hello"))
}

func TestEstimateTokens_ChineseMix(t *testing.T) {
	// "hello 你好" = "hello "(6 ASCII = 1.5) + "你好"(2 CJK = 3) = 4.5 → 4
	assert.Equal(t, 4, EstimateTokens("hello 你好"))
}

func TestEstimateTokens_JapaneseKana(t *testing.T) {
	// カタカナ = 4 katakana chars * 1.5 = 6
	assert.Equal(t, 6, EstimateTokens("カタカナ"))
}

func TestEstimateTokens_KoreanHangul(t *testing.T) {
	// 한글 = 2 hangul chars * 1.5 = 3
	assert.Equal(t, 3, EstimateTokens("한글"))
}

func TestEstimateTokens_CJKExtension(t *testing.T) {
	// 𠀀 (U+20000) — single CJK Ext-B char * 1.5 = 1
	assert.Equal(t, 1, EstimateTokens("𠀀"))
}

func TestEstimateTokens_EmptyString(t *testing.T) {
	assert.Equal(t, 0, EstimateTokens(""))
}

func TestMockCompactProvider_HappyPath(t *testing.T) {
	m := &MockCompactProvider{PlaceholderSummary: "this is a summary placeholder long enough to estimate tokens"}
	ctx := context.Background()
	req := &CompactRequest{
		Messages:        []Message{{Role: "user", Content: "hello world this is a longer input message"}},
		SystemPrompt:    FullCompactSystemPrompt(),
		MaxOutputTokens: 8000,
	}
	got, err := m.Compact(ctx, req)
	require.NoError(t, err)
	assert.Contains(t, got.Summary, "summary placeholder")
	assert.Greater(t, got.OutputTokens, 0)
	assert.Greater(t, got.InputTokens, 0)
}

func TestMockCompactProvider_FailureSequence(t *testing.T) {
	wantErr := errors.New("simulated provider failure")
	m := &MockCompactProvider{
		PlaceholderSummary: "ok",
		FailureSequence:    []error{wantErr, nil}, // 1st fails, 2nd succeeds
	}
	ctx := context.Background()
	req := &CompactRequest{Messages: []Message{{Role: "user", Content: "x"}}}

	_, err := m.Compact(ctx, req)
	assert.ErrorIs(t, err, wantErr)

	got, err := m.Compact(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "ok", got.Summary)
}

func TestMockCompactProvider_FailureSequence_PastEnd(t *testing.T) {
	m := &MockCompactProvider{
		PlaceholderSummary: "ok",
		FailureSequence:    []error{errors.New("first")},
	}
	ctx := context.Background()
	req := &CompactRequest{Messages: []Message{{Role: "user"}}}

	// 1st: fail
	_, err := m.Compact(ctx, req)
	assert.Error(t, err)
	// 2nd: past end of FailureSequence → success
	_, err = m.Compact(ctx, req)
	assert.NoError(t, err)
	// 3rd: still success
	_, err = m.Compact(ctx, req)
	assert.NoError(t, err)
}

func TestJoinMessages_ConcatenatesContent(t *testing.T) {
	msgs := []Message{{Content: "a"}, {Content: "b"}, {Content: ""}}
	got := joinMessages(msgs)
	assert.Equal(t, "a\nb\n\n", got)
}
