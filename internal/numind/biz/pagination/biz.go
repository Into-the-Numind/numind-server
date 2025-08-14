package pagination

import (
	"encoding/json"
	"fmt"
)

// PaginationBiz 定义分页业务接口
type PaginationBiz interface {
	// PaginateElements 对元素数组进行分页
	PaginateElements(elements []Element) (*PaginatedContent, error)

	// PaginateFromJSON 从JSON字符串进行分页
	PaginateFromJSON(jsonStr string) (*PaginatedContent, error)

	// GetConfig 获取当前配置
	GetConfig() *PaginationConfig

	// UpdateConfig 更新配置
	UpdateConfig(config *PaginationConfig) error
}

// paginationBiz 分页业务实现
type paginationBiz struct {
	engine *PaginationEngine
	config *PaginationConfig
}

// NewPaginationBiz 创建新的分页业务实例
func NewPaginationBiz() PaginationBiz {
	config := GetDefaultConfig()
	engine := NewPaginationEngine(config)

	return &paginationBiz{
		engine: engine,
		config: config,
	}
}

// PaginateElements 分页元素
func (p *paginationBiz) PaginateElements(elements []Element) (*PaginatedContent, error) {
	return p.engine.PaginateElements(elements)
}

// PaginateElementsWithConfig 使用指定配置分页元素
func (p *paginationBiz) PaginateElementsWithConfig(elements []Element, config *PaginationConfig) (*PaginatedContent, error) {
	engine := NewPaginationEngine(config)
	return engine.PaginateElements(elements)
}

// PaginateFromJSON 从JSON字符串进行分页
func (p *paginationBiz) PaginateFromJSON(jsonStr string) (*PaginatedContent, error) {
	var elements []Element
	if err := json.Unmarshal([]byte(jsonStr), &elements); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return p.engine.PaginateElements(elements)
}

// GetConfig 获取当前配置
func (p *paginationBiz) GetConfig() *PaginationConfig {
	return p.config
}

// UpdateConfig 更新配置
func (p *paginationBiz) UpdateConfig(config *PaginationConfig) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	p.config = config
	p.engine = NewPaginationEngine(config)
	return nil
}
