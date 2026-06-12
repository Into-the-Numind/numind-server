package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mergeResumeTranscript contract (answer-resume-lifecycle F2): a resumed run's
// final persistence must APPEND the new leg to the pre-yield transcript
// instead of clobbering it (dev run 148 lost its original prompt + first-leg
// research). Non-resume runs (empty prior) are a strict no-op.
func TestMergeResumeTranscript(t *testing.T) {
	leg2 := []map[string]any{
		{"role": "user", "content": "用户已回答你的问题：……"},
		{"role": "tool_group", "tool_calls": []any{}},
		{"role": "assistant", "content": "报告已生成完毕！"},
	}
	prior := json.RawMessage(`[
		{"role":"user","content":"原始调研请求"},
		{"role":"assistant","content":"先搜索一下"},
		{"role":"user","content":"用户已回答你的问题：……"}
	]`)

	t.Run("empty prior is a no-op (non-resume runs)", func(t *testing.T) {
		got := mergeResumeTranscript(nil, leg2)
		assert.Equal(t, leg2, got)
		got = mergeResumeTranscript(json.RawMessage(`[]`), leg2)
		assert.Equal(t, leg2, got)
		got = mergeResumeTranscript(json.RawMessage(`null`), leg2)
		assert.Equal(t, leg2, got)
	})

	t.Run("invalid prior is a no-op", func(t *testing.T) {
		got := mergeResumeTranscript(json.RawMessage(`{not json`), leg2)
		assert.Equal(t, leg2, got)
	})

	t.Run("resume appends and dedups the duplicated answer turn", func(t *testing.T) {
		got := mergeResumeTranscript(prior, leg2)
		// prior(3) + leg2(3) - 1 duplicated leading user turn = 5
		assert.Len(t, got, 5)
		assert.Equal(t, "原始调研请求", got[0]["content"])
		assert.Equal(t, "用户已回答你的问题：……", got[2]["content"], "answer turn kept once (from prior)")
		assert.Equal(t, "报告已生成完毕！", got[4]["content"])
		// No second copy of the answer message.
		count := 0
		for _, turn := range got {
			if turn["content"] == "用户已回答你的问题：……" {
				count++
			}
		}
		assert.Equal(t, 1, count)
	})

	t.Run("no dedup when contents differ", func(t *testing.T) {
		other := json.RawMessage(`[{"role":"user","content":"别的话"}]`)
		got := mergeResumeTranscript(other, leg2)
		assert.Len(t, got, 4)
		assert.Equal(t, "别的话", got[0]["content"])
		assert.Equal(t, "用户已回答你的问题：……", got[1]["content"])
	})

	t.Run("no dedup when leg2 does not start with a user turn", func(t *testing.T) {
		legNoUser := []map[string]any{{"role": "assistant", "content": "直接回答"}}
		got := mergeResumeTranscript(prior, legNoUser)
		assert.Len(t, got, 4)
	})

	t.Run("multi-turn prior preserved verbatim", func(t *testing.T) {
		got := mergeResumeTranscript(prior, leg2)
		b, _ := json.Marshal(got[0:3])
		var roundTrip []map[string]any
		_ = json.Unmarshal(b, &roundTrip)
		assert.Equal(t, "assistant", roundTrip[1]["role"])
	})
}
