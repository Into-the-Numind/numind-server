package ingest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeStrategy(t *testing.T) {
	cases := map[string]string{
		"semantic":      "semantic",
		"no_split":      "no_split",
		"rule":          "rule_fallback", // 语义从未可用 → 归一为兜底
		"rule_fallback": "rule_fallback",
		"":              "rule_fallback", // 未知 → 保守算兜底
		"garbage":       "rule_fallback",
	}
	for in, want := range cases {
		if got := normalizeStrategy(in); got != want {
			t.Errorf("normalizeStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSplitWithStrategy_FallbackWhenSemanticUnavailable: 本地无语义服务(localhost:9093
// 连接被拒)→ 走规则兜底 → 归一 strategy=rule_fallback,chunks 非空,err 恒 nil(永不失败)。
func TestSplitWithStrategy_FallbackWhenSemanticUnavailable(t *testing.T) {
	a := NewSplitterAdapter()
	longText := strings.Repeat("这是一段用于触发切分的中文测试内容。", 200) // 远超 1500 字节阈值

	chunks, strategy, _, err := a.SplitWithStrategy(longText)

	assert.NoError(t, err, "SplitWithStrategy 永不返回 err")
	assert.NotEmpty(t, chunks, "兜底也必须产出非空 chunk")
	assert.Equal(t, StrategyFallback, strategy, "本地无语义服务应归一为 rule_fallback")
}

// TestSplitWithStrategy_NoSplitShortText: 短文本不切分 → strategy=no_split。
func TestSplitWithStrategy_NoSplitShortText(t *testing.T) {
	a := NewSplitterAdapter()
	chunks, strategy, _, err := a.SplitWithStrategy("短文本,无需切分。")

	assert.NoError(t, err)
	assert.NotEmpty(t, chunks)
	assert.Equal(t, StrategyNoSplit, strategy, "短文本应为 no_split")
}

// TestFallbackChunkSize_Reasonable (T3): 规则兜底切出的块应贴近语义档(≤~1800+overlap),
// 不再是 6000 字大块;长文本应切成多块。
func TestFallbackChunkSize_Reasonable(t *testing.T) {
	a := NewSplitterAdapter()
	longText := strings.Repeat("创业要经历几个阶段,每个阶段的核心任务都不同。", 300) // ~6000+ 字

	chunks, strategy, _, err := a.SplitWithStrategy(longText)
	assert.NoError(t, err)
	assert.Equal(t, StrategyFallback, strategy)
	assert.GreaterOrEqual(t, len(chunks), 2, "长文本兜底应切成多块")

	for i, c := range chunks {
		// MaxChunkSize 1800 + overlap 300 + 少量余量(markdown/jieba 边界)
		assert.LessOrEqualf(t, len([]rune(c.Content)), 2400, "块 %d 过大(应贴近 1800 档): %d 字", i, len([]rune(c.Content)))
	}
}
