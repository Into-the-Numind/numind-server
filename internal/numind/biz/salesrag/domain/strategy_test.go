package domain_test

import (
	"testing"

	"numind-server/internal/numind/biz/salesrag/domain"

	"github.com/stretchr/testify/assert"
)

func TestStrategyModels(t *testing.T) {
	meta := domain.MetaStrategy{
		ID:          "M-T01",
		Name:        "信任建立体系",
		Description: "核心处理客户的怀疑、试探、索要证据、抱怨服务等信任类问题",
	}
	basic := domain.BasicStrategy{
		ID:      "P-001",
		MetaID:  "M-T01",
		Name:    "交付边界与位势重构",
		Content: "具体策略内容",
	}
	assert.Equal(t, "M-T01", meta.ID)
	assert.Equal(t, "P-001", basic.ID)
	assert.Equal(t, "M-T01", basic.MetaID)
}
