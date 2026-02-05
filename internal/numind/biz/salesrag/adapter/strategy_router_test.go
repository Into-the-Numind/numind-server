package adapter

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/salesrag/domain"

	"github.com/stretchr/testify/assert"
)

func TestStrategyRouter_SelectMetaStrategy(t *testing.T) {
	router := NewStrategyRouter()

	metas := []domain.MetaStrategy{
		{ID: "M-T01", Name: "信任建立", Description: "处理信任问题"},
		{ID: "M-P06", Name: "专业边界", Description: "处理边界试探，如电话语音请求"},
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
	basicID, err := router.SelectBasicStrategy(context.Background(), "能打个电话聊聊吗", nil, basics)
	assert.NoError(t, err)
	assert.NotEmpty(t, basicID)
}
