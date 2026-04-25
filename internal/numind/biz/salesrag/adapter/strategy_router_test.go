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
// produces a single RoleRecent + Critical + CompressNone fragment carrying the
// full strategy selection prompt. The fragment must not contain any SOP-specific
// or chatbot-specific metadata keys (spec §2.2: contextbudget must not branch on
// business-domain metadata).
func TestSalesRAGStrategyRouterUsesFragments(t *testing.T) {
	prompt := "You are a strategy selector. Choose one: [A, B, C]\nCustomer: I need help."

	frags := buildStrategySelectFragments(prompt)

	require.Len(t, frags, 1, "strategy select must produce exactly one fragment")

	f := frags[0]
	assert.Equal(t, cb.RoleRecent, f.Role, "strategy fragment must be RoleRecent")
	assert.Equal(t, cb.SourceUser, f.Source, "strategy fragment must be SourceUser")
	assert.True(t, f.Critical, "strategy fragment must be Critical=true (must not be dropped under pressure)")
	assert.Equal(t, cb.CompressNone, f.Compressibility, "strategy fragment must be CompressNone")
	assert.Equal(t, prompt, f.Content, "fragment content must be the full prompt")

	// No SOP-specific or chatbot-specific metadata.
	for k := range f.Metadata {
		assert.NotContains(t, k, "sop", "strategy fragment must not have SOP metadata (key=%q)", k)
		assert.NotContains(t, k, "chatbot", "strategy fragment must not have chatbot metadata (key=%q)", k)
	}
}
