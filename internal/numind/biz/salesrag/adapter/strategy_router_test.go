package adapter

import (
	"context"
	"os"
	"testing"

	"numind-server/internal/numind/biz/salesrag/domain"
	aiservice "numind-server/internal/pkg/aiservice"
	cb "numind-server/internal/pkg/contextbudget"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain initialises a minimal aiservice singleton so that tests which
// exercise code paths reaching the AI Gateway do not panic on Default().
func TestMain(m *testing.M) {
	gw := aiservice.Build(aiservice.Deps{})
	aiservice.SetDefault(gw)
	os.Exit(m.Run())
}

func TestStrategyRouter_SelectMetaStrategy(t *testing.T) {
	router := NewStrategyRouter()

	metas := []domain.MetaStrategy{
		{ID: "M-T01", Name: "信任建立", Description: "处理信任问题"},
		{ID: "M-P02", Name: "专业边界", Description: "处理边界试探，如电话语音请求"},
	}

	// 测试电话请求应匹配 M-P06
	metaID, err := router.SelectMetaStrategy(context.Background(), "能打个电话聊聊吗", nil, metas)
	assert.NoError(t, err)
	assert.NotEmpty(t, metaID)
}

func TestStrategyRouter_SelectBasicStrategy(t *testing.T) {
	router := NewStrategyRouter()

	basics := []domain.BasicStrategy{
		{ID: "P-001", Name: "交付边界", Description: "拒绝语音电话请求"},
		{ID: "T-001", Name: "证据攻击", Description: "提供案例证据"},
	}

	// 测试电话请求应匹配 P-001
	dummyDecisionTree := "如果客户要电话，选择 P-001"
	basicID, err := router.SelectBasicStrategy(context.Background(), "能打个电话聊聊吗", nil, dummyDecisionTree, basics)
	assert.NoError(t, err)
	assert.NotEmpty(t, basicID)
}

// TestSalesRAGStrategyRouterUsesFragments verifies that buildStrategySelectFragments
// produces two fragments per spec §9.2 system/user separation (P2-2 fix):
//   - fragment[0]: RoleImmutable + SourceSystem for the strategy-selection instruction block
//   - fragment[1]: RoleRecent + SourceUser for the customer's current message
//
// Neither fragment may carry SOP-specific or chatbot-specific metadata keys
// (spec §2.2: contextbudget must not branch on business-domain metadata).
func TestSalesRAGStrategyRouterUsesFragments(t *testing.T) {
	// Construct a prompt that matches the actual template format used by
	// SelectMetaStrategy / SelectBasicStrategy so the separator logic fires.
	prompt := "你是一个销售策略分析师。根据客户的消息，从以下综合策略系统中选择最匹配的一个。\n\n" +
		"## 可选策略系统\n1. [M-A01] 策略A: 描述A\n\n" +
		"## 对话历史\n无\n\n" +
		"## 客户当前消息\n" +
		"我想了解一下价格\n\n" +
		"## 输出要求\n**必须且只能选择 1 个最匹配的策略ID**。\n请严格按照以下JSON格式输出"

	frags := buildStrategySelectFragments(prompt)

	require.Len(t, frags, 2, "strategy select must produce exactly two fragments (system + user) after P2-2 fix")

	// Fragment 0: system instruction block
	sys := frags[0]
	assert.Equal(t, cb.RoleImmutable, sys.Role, "system instruction fragment must be RoleImmutable (spec §9.2)")
	assert.Equal(t, cb.SourceSystem, sys.Source, "system instruction fragment must be SourceSystem")
	assert.True(t, sys.Critical, "system fragment must be Critical=true")
	assert.Equal(t, cb.CompressNone, sys.Compressibility, "system fragment must be CompressNone")
	assert.NotEmpty(t, sys.Content, "system fragment content must be non-empty")

	// Fragment 1: user query
	usr := frags[1]
	assert.Equal(t, cb.RoleRecent, usr.Role, "user query fragment must be RoleRecent")
	assert.Equal(t, cb.SourceUser, usr.Source, "user query fragment must be SourceUser")
	assert.True(t, usr.Critical, "user fragment must be Critical=true (must not be dropped under pressure)")
	assert.Equal(t, cb.CompressNone, usr.Compressibility, "user fragment must be CompressNone")
	assert.Contains(t, usr.Content, "我想了解一下价格", "user fragment must contain the customer query")

	// No SOP-specific or chatbot-specific metadata in either fragment.
	for i, f := range frags {
		for k := range f.Metadata {
			assert.NotContains(t, k, "sop", "fragment[%d] must not have SOP metadata (key=%q)", i, k)
			assert.NotContains(t, k, "chatbot", "fragment[%d] must not have chatbot metadata (key=%q)", i, k)
		}
	}
}

// TestSalesRAGStrategyRouterUsesFragments_FallbackWhenNoSeparator verifies that
// when the prompt template does not contain the expected "## 客户当前消息\n"
// separator (e.g. after a template change), buildStrategySelectFragments falls
// back to a single RoleImmutable system fragment rather than panicking or
// producing malformed output.
func TestSalesRAGStrategyRouterUsesFragments_FallbackWhenNoSeparator(t *testing.T) {
	prompt := "Simple prompt without the standard section separator."

	frags := buildStrategySelectFragments(prompt)

	require.Len(t, frags, 1, "fallback path must produce exactly one fragment")
	assert.Equal(t, cb.RoleImmutable, frags[0].Role, "fallback fragment must be RoleImmutable")
	assert.Equal(t, cb.SourceSystem, frags[0].Source, "fallback fragment must be SourceSystem")
	assert.Equal(t, prompt, frags[0].Content, "fallback fragment content must be the full prompt")
}
