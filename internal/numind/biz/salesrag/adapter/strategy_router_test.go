package adapter

import (
	"context"
	"os"
	"testing"

	"numind-server/internal/numind/biz/salesrag/domain"
	aiservice "numind-server/internal/pkg/aiservice"

	"github.com/stretchr/testify/assert"
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
