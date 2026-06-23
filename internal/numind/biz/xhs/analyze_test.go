package xhs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/model"

	"gorm.io/datatypes"
)

// withMockChatFn 在 t 的生命周期内替换包级 chatFn seam，并在 cleanup 时恢复，
// 让 biz/xhs 的单测无需接真实 aiservice Gateway 即可 mock LLM 响应。
func withMockChatFn(t *testing.T, fn func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	prev := chatFn
	chatFn = fn
	t.Cleanup(func() { chatFn = prev })
}

func strPtr(s string) *string { return &s }

// successAnalyzeChatFn 返回一个永远成功、返回合法 6 字段 JSON 的 chatFn stub，
// 供富化框架（enrich_test.go）的测试在驱动 job 到 done 时复用。
func successAnalyzeChatFn() func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validAnalyzeJSON()}, nil
	}
}

// validAnalyzeJSON 返回一个 6 字段齐全的合法 JSON 响应。
func validAnalyzeJSON() string {
	r := analyzeResult{
		TopicAngle:     "从职场新人视角切入",
		ViralReason:    "强情绪共鸣 + 实用清单",
		Borrowable:     "开头三连问钩子",
		TargetAudience: "一线城市职场女性",
		TitleFormula:   "数字 + 痛点 + 反转",
		OneLine:        "职场避坑指南，强共鸣强转化",
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// TestAnalyzeNote_ParsesSixFields 验证正常路径：LLM 返回合法 JSON → 6 字段全部写回 note。
func TestAnalyzeNote_ParsesSixFields(t *testing.T) {
	withMockChatFn(t, func(_ context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		assert.Equal(t, profile.XhsNoteAnalyze, taskID, "应使用 xhs.note_analyze task profile")
		return &aiservice.ChatResponse{Content: validAnalyzeJSON()}, nil
	})

	e := NewEnricher(newEnrichMockStore())
	note := &model.XhsTopicNote{ID: 1, Title: "标题", Content: "正文"}
	err := e.analyzeNote(context.Background(), 7, note)
	require.NoError(t, err)

	assert.Equal(t, "从职场新人视角切入", note.AITopicAngle)
	assert.Equal(t, "强情绪共鸣 + 实用清单", note.AIViralReason)
	assert.Equal(t, "开头三连问钩子", note.AIBorrowable)
	assert.Equal(t, "一线城市职场女性", note.AITargetAudience)
	assert.Equal(t, "数字 + 痛点 + 反转", note.AITitleFormula)
	assert.Equal(t, "职场避坑指南，强共鸣强转化", note.AIOneLine)
}

// TestAnalyzeNote_StripsMarkdownFence 验证 LLM 把 JSON 包在 ```json fenced 里时仍能解析。
func TestAnalyzeNote_StripsMarkdownFence(t *testing.T) {
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: "```json\n" + validAnalyzeJSON() + "\n```"}, nil
	})

	e := NewEnricher(newEnrichMockStore())
	note := &model.XhsTopicNote{ID: 2, Title: "标题"}
	require.NoError(t, e.analyzeNote(context.Background(), 7, note))
	assert.Equal(t, "从职场新人视角切入", note.AITopicAngle)
	assert.Equal(t, "职场避坑指南，强共鸣强转化", note.AIOneLine)
}

// TestAnalyzeNote_JSONErrorDegrades 验证 JSON 解析失败时降级：原始响应截断写入 ai_one_line，
// 其余字段留空，且不返回 error（尽力而为，不让整条富化失败）。
func TestAnalyzeNote_JSONErrorDegrades(t *testing.T) {
	const garbage = "这不是 JSON，只是模型的自由发挥"
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: garbage}, nil
	})

	e := NewEnricher(newEnrichMockStore())
	note := &model.XhsTopicNote{ID: 3, Title: "标题"}
	err := e.analyzeNote(context.Background(), 7, note)
	require.NoError(t, err, "解析失败应降级而非报错")
	assert.Equal(t, garbage, note.AIOneLine, "降级应把原始响应写入 ai_one_line")
	assert.Empty(t, note.AITopicAngle, "降级时其余字段留空")
	assert.Empty(t, note.AIViralReason)
}

// TestAnalyzeNote_EmptyObjectDegrades 验证空对象（6 字段全空）被视为解析失败 → 降级。
func TestAnalyzeNote_EmptyObjectDegrades(t *testing.T) {
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: "{}"}, nil
	})

	e := NewEnricher(newEnrichMockStore())
	note := &model.XhsTopicNote{ID: 4, Title: "标题"}
	require.NoError(t, e.analyzeNote(context.Background(), 7, note))
	assert.Equal(t, "{}", note.AIOneLine, "空对象降级写入原始响应")
}

// TestAnalyzeNote_SetsBillingContext 验证 analyzeNote 给下游 ctx 设置了 billing 上下文
// （userID + operation），这是用户被扣分的前提（design §4.2）。
func TestAnalyzeNote_SetsBillingContext(t *testing.T) {
	var gotUserID uint
	var gotOp string
	var gotMaxTokens int
	var gotTemp float64
	withMockChatFn(t, func(ctx context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		if bc := billing.FromContext(ctx); bc != nil {
			gotUserID = bc.UserID
			gotOp = bc.Operation
		}
		gotMaxTokens = req.MaxTokens
		gotTemp = req.Temperature
		return &aiservice.ChatResponse{Content: validAnalyzeJSON()}, nil
	})

	e := NewEnricher(newEnrichMockStore())
	note := &model.XhsTopicNote{ID: 5, Title: "标题"}
	require.NoError(t, e.analyzeNote(context.Background(), 42, note))

	assert.Equal(t, uint(42), gotUserID, "billing context userID 应为传入 userID")
	assert.Equal(t, "xhs_note_analyze", gotOp, "billing operation 应为 xhs_note_analyze")
	assert.Equal(t, analyzeMaxTokens, gotMaxTokens, "MaxTokens 应为 800")
	assert.InDelta(t, 0.3, gotTemp, 1e-9, "Temperature 应为 0.3")
}

// TestAnalyzeNote_LLMErrorReturnsError 验证 LLM 调用本身报错时 analyzeNote 返回 error
// （由 processJob 兜底置 failed）。
func TestAnalyzeNote_LLMErrorReturnsError(t *testing.T) {
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, assertErr{}
	})

	e := NewEnricher(newEnrichMockStore())
	note := &model.XhsTopicNote{ID: 6, Title: "标题"}
	err := e.analyzeNote(context.Background(), 7, note)
	require.Error(t, err)
}

type assertErr struct{}

func (assertErr) Error() string { return "llm boom" }

// TestBuildAnalyzeUserPrompt_TruncatesAndIncludesFields 验证 prompt 拼装：含 title/content/
// transcript/tags/comments，且超长 content 被按字符截断。
func TestBuildAnalyzeUserPrompt_TruncatesAndIncludesFields(t *testing.T) {
	longContent := strings.Repeat("正", maxAnalyzeContentRunes+500)
	tags, _ := json.Marshal([]string{"职场", "成长"})
	comments, _ := json.Marshal([]xhsComment{{Author: "小明", Text: "太有用了"}})
	note := &model.XhsTopicNote{
		Title:           "标题X",
		Content:         longContent,
		VideoTranscript: strPtr("视频里说了一些话"),
		Tags:            datatypes.JSON(tags),
		Comments:        datatypes.JSON(comments),
	}

	prompt := buildAnalyzeUserPrompt(note)
	assert.Contains(t, prompt, "标题X")
	assert.Contains(t, prompt, "视频里说了一些话")
	assert.Contains(t, prompt, "职场、成长")
	assert.Contains(t, prompt, "小明：太有用了")

	// 正文部分应被截断到 maxAnalyzeContentRunes 个字符（不含其它字段干扰，单独验证 truncateRunes）。
	truncated := truncateRunes(longContent, maxAnalyzeContentRunes)
	assert.Equal(t, maxAnalyzeContentRunes, len([]rune(truncated)))
	assert.Contains(t, prompt, truncated)
}

// TestTruncateRunes 验证多字节字符按 rune 截断不切坏 UTF-8。
func TestTruncateRunes(t *testing.T) {
	assert.Equal(t, "你好", truncateRunes("你好世界", 2))
	assert.Equal(t, "你好世界", truncateRunes("你好世界", 10))
	assert.Equal(t, "", truncateRunes("你好", 0))
}

// TestAnalyzeNote_OneLineTruncatedToDBSize 验证 ai_one_line 超过 DB size:500 时被截断。
func TestAnalyzeNote_OneLineTruncatedToDBSize(t *testing.T) {
	longOneLine := strings.Repeat("总", maxAnalyzeOneLineRunes+100)
	r := analyzeResult{TopicAngle: "a", OneLine: longOneLine}
	b, _ := json.Marshal(r)
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: string(b)}, nil
	})

	e := NewEnricher(newEnrichMockStore())
	note := &model.XhsTopicNote{ID: 8}
	require.NoError(t, e.analyzeNote(context.Background(), 7, note))
	assert.Equal(t, maxAnalyzeOneLineRunes, len([]rune(note.AIOneLine)), "ai_one_line 应截断到 500 字符")
}
