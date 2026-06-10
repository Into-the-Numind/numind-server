// Package chatbot — grounding_test.go
//
// Regression guard for the chatbot KB-RAG fix (T2.1 "回答怪怪的" 根因):
//   - when KB chunks are retrieved, BuildChatContextFragments must inject a
//     hard-constraint grounding system fragment + label each evidence fragment with
//     a citable "[知识N] (相关度:X%)" header + derive importance from the rerank score;
//   - when NO KB chunks are present (纯聊天), it must emit NEITHER a grounding
//     fragment NOR any evidence — leaving the legacy plain-chat behavior intact.
//
// Pure unit test: no AI / DB / network dependency.
package chatbot

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/retrieval/domain"
)

func TestBuildChatContextFragments_GroundingWhenKBPresent(t *testing.T) {
	const systemPrompt = "你是知识库助手。"
	history := []model.ChatbotMessage{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好，有什么可以帮你？"},
	}
	currentMessage := "公司的退款政策是什么？"

	kbChunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 11, DocumentName: "退款政策.pdf", Content: "退款须在 7 天内申请。", Score: 0.92},
		{ID: "c2", DocumentID: 12, DocumentName: "FAQ.md", Content: "已使用商品不支持退款。", Score: 0.41},
	}

	frags := BuildChatContextFragments(systemPrompt, history, currentMessage, kbChunks, chatStreamRecentTurns)
	require.NotEmpty(t, frags)

	// ① head is still the immutable system prompt, untouched.
	assert.Equal(t, string(cb.RoleImmutable), string(frags[0].Role))
	assert.Equal(t, systemPrompt, frags[0].Content)

	// Locate the grounding fragment + evidence fragments.
	var grounding *cb.ContextFragment
	var evidence []cb.ContextFragment
	for i := range frags {
		f := frags[i]
		switch {
		case f.ID == "kb-grounding":
			g := f
			grounding = &g
		case f.Role == cb.RoleEvidence:
			evidence = append(evidence, f)
		}
	}

	// ① grounding system fragment exists, is a system fragment, and carries the hard
	// constraint wording ("仅依据" + "不要编造").
	require.NotNil(t, grounding, "grounding system fragment must exist when KB chunks present")
	assert.Equal(t, string(cb.RoleImmutable), string(grounding.Role), "grounding must be an immutable system fragment")
	assert.Equal(t, string(cb.SourceSystem), string(grounding.Source), "grounding must render as a system message")
	assert.Contains(t, grounding.Content, "仅依据", "grounding must hard-constrain answers to the retrieved material")
	assert.Contains(t, grounding.Content, "不要编造", "grounding must forbid fabrication")

	// grounding must be ordered AFTER the system prompt and BEFORE the first evidence.
	require.Len(t, evidence, 2)
	assert.Greater(t, grounding.Order, frags[0].Order, "grounding sits after the system prompt")
	assert.Less(t, grounding.Order, evidence[0].Order, "grounding sits before evidence")

	// ② evidence content carries the citable "[知识N] (相关度:X%)" prefix (1-based N).
	assert.Contains(t, evidence[0].Content, "[知识1] (相关度:92%)", "evidence[0] must carry the labeled prefix")
	assert.Contains(t, evidence[0].Content, "退款须在 7 天内申请。", "evidence[0] must retain chunk content")
	assert.Contains(t, evidence[1].Content, "[知识2] (相关度:41%)", "evidence[1] must carry the labeled prefix")

	// ③ importance is derived from chunk.Score (scoreToImportance), NOT the old hard-coded 7.
	assert.Equal(t, scoreToImportance(0.92), evidence[0].Importance, "importance must follow the rerank score")
	assert.Equal(t, scoreToImportance(0.41), evidence[1].Importance)
	assert.NotEqual(t, 7, evidence[0].Importance, "importance must no longer be hard-coded 7")
	assert.Equal(t, string(cb.SourceKB), string(evidence[0].Source))
}

func TestBuildChatContextFragments_PureChatNoKB(t *testing.T) {
	const systemPrompt = "你是闲聊助手。"
	history := []model.ChatbotMessage{
		{Role: "user", Content: "讲个笑话"},
	}
	currentMessage := "再来一个"

	// ④ No KB chunks → no grounding fragment, no evidence — pure-chat behavior.
	frags := BuildChatContextFragments(systemPrompt, history, currentMessage, nil, chatStreamRecentTurns)
	require.NotEmpty(t, frags)

	for _, f := range frags {
		assert.NotEqual(t, "kb-grounding", f.ID, "pure chat must NOT inject a grounding fragment")
		assert.NotEqual(t, cb.RoleEvidence, f.Role, "pure chat must NOT inject evidence fragments")
		assert.NotEqual(t, cb.SourceKB, f.Source, "pure chat must NOT inject KB-sourced fragments")
	}

	// head untouched + current user message preserved verbatim.
	assert.Equal(t, systemPrompt, frags[0].Content)
	assert.Equal(t, currentMessage, frags[len(frags)-1].Content)
}

func TestParseCitedSources(t *testing.T) {
	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 11, DocumentName: "A.pdf"},
		{ID: "c2", DocumentID: 12, DocumentName: "B.md"},
		{ID: "c3", DocumentID: 13, DocumentName: "C.txt"},
	}

	t.Run("dedup and sort by index", func(t *testing.T) {
		answer := "根据 [知识2] 和 [1]，再次参考 [知识2]。"
		got := parseCitedSources(answer, chunks)
		require.Len(t, got, 2)
		assert.Equal(t, 1, got[0].Index)
		assert.Equal(t, "c1", got[0].ChunkID)
		assert.Equal(t, "A.pdf", got[0].DocumentName)
		assert.Equal(t, 2, got[1].Index)
		assert.Equal(t, "c2", got[1].ChunkID)
	})

	t.Run("out-of-range citation ignored", func(t *testing.T) {
		got := parseCitedSources("参考 [9] 与 [知识1]", chunks)
		require.Len(t, got, 1)
		assert.Equal(t, 1, got[0].Index)
	})

	t.Run("no citation returns nil", func(t *testing.T) {
		assert.Nil(t, parseCitedSources("没有任何引用的回答。", chunks))
	})

	t.Run("no chunks returns nil", func(t *testing.T) {
		assert.Nil(t, parseCitedSources("参考 [1]", nil))
	})
}

// TestScoreToImportance locks the [0,1]→[0,10] mapping copied from salesrag.
func TestScoreToImportance(t *testing.T) {
	cases := []struct {
		score float32
		want  int
	}{
		{-0.5, 0}, {0, 0}, {0.41, 4}, {0.92, 9}, {1.0, 10}, {1.5, 10},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("score=%.2f", c.score), func(t *testing.T) {
			assert.Equal(t, c.want, scoreToImportance(c.score))
		})
	}
}

// TestChatbotGroundingPrompt_HardConstraintWording is a belt-and-suspenders guard
// that the production grounding constant keeps its hard-constraint phrasing — the
// fragment test above asserts on a built fragment, this asserts on the source const.
func TestChatbotGroundingPrompt_HardConstraintWording(t *testing.T) {
	for _, want := range []string{"仅依据", "不要编造", "知识库中暂无相关信息"} {
		assert.True(t, strings.Contains(chatbotGroundingPrompt, want),
			"grounding prompt must contain %q", want)
	}
}
