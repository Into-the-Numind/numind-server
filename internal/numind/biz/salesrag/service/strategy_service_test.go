package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewStrategyService(t *testing.T) {
	svc := NewStrategyService()
	assert.NotNil(t, svc)
}

func TestStrategyService_DetermineStrategy_ReturnsStrategy(t *testing.T) {
	svc := NewStrategyService()

	// 测试电话请求场景
	strategy, err := svc.DetermineStrategy(context.Background(), "能打个电话聊聊吗", nil)

	// 由于实际会调用LLM，这里只验证返回非空
	// 在集成测试中再验证具体策略选择的准确性
	assert.NoError(t, err)
	assert.NotNil(t, strategy)
	assert.NotEmpty(t, strategy.ID)
	assert.NotEmpty(t, strategy.Content)
}

func TestStrategyService_GetBasicsByMetaID(t *testing.T) {
	svc := NewStrategyService()

	// 测试获取 M-T01 下的基础策略
	basics := svc.GetBasicsByMetaID("M-T01")
	assert.NotEmpty(t, basics)

	// 验证所有返回的基础策略都属于 M-T01
	for _, b := range basics {
		assert.Equal(t, "M-T01", b.MetaID)
	}
}
