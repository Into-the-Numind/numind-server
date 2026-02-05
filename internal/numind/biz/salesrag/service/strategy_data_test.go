package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadStrategies(t *testing.T) {
	metas, basics := LoadStrategies()

	// 验证综合策略加载
	assert.NotEmpty(t, metas, "综合策略不应为空")
	assert.Equal(t, 6, len(metas), "应有6个综合策略")

	// 验证第一个综合策略
	assert.Equal(t, "M-T01", metas[0].ID)
	assert.Equal(t, "信任建立与证据碾压系统", metas[0].Name)

	// 验证基础策略加载
	assert.NotEmpty(t, basics, "基础策略不应为空")

	// 验证基础策略与综合策略的关联
	foundP001 := false
	for _, b := range basics {
		if b.ID == "P-001" {
			foundP001 = true
			assert.Equal(t, "M-P06", b.MetaID) // P-001 属于专业边界系统
			break
		}
	}
	assert.True(t, foundP001, "应能找到 P-001 基础策略")
}

func TestGetMetaStrategyByID(t *testing.T) {
	metas, _ := LoadStrategies()
	metaMap := make(map[string]interface{})
	for _, m := range metas {
		metaMap[m.ID] = m
	}
	_, exists := metaMap["M-T01"]
	assert.True(t, exists, "应能通过ID找到综合策略")
}
