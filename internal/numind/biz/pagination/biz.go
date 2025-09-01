package pagination

import (
	"encoding/json"
	"fmt"
)

// PaginationBiz 定义分页业务接口
type PaginationBiz interface {
	// PaginateElements 对元素数组进行分页（使用标准分页）
	PaginateElements(elements []Element) (*PaginatedContent, error)

	// PaginateElementsByLines 对元素数组进行按行分页
	PaginateElementsByLines(elements []Element) (*PaginatedContent, error)

	// PaginateFromJSON 从JSON字符串进行分页
	PaginateFromJSON(jsonStr string) (*PaginatedContent, error)

	// PaginateFromJSONByLines 从JSON字符串进行按行分页
	PaginateFromJSONByLines(jsonStr string) (*PaginatedContent, error)

	// GetConfig 获取当前配置
	GetConfig() *PaginationConfig

	// UpdateConfig 更新配置
	UpdateConfig(config *PaginationConfig) error
}

// paginationBiz 分页业务实现
type paginationBiz struct {
	engine        *PaginationEngine
	lineEngine    *LineBasedPaginationEngine
	config        *PaginationConfig
}

// NewPaginationBiz 创建新的分页业务实例
func NewPaginationBiz() PaginationBiz {
	// 尝试从Viper配置加载，如果失败则使用默认配置
	config := LoadConfigFromViper()
	engine := NewPaginationEngine(config)
	lineEngine := NewLineBasedPaginationEngine(config)

	return &paginationBiz{
		engine:     engine,
		lineEngine: lineEngine,
		config:     config,
	}
}

// PaginateElements 分页元素
func (p *paginationBiz) PaginateElements(elements []Element) (*PaginatedContent, error) {
	return p.engine.PaginateElements(elements)
}

// PaginateElementsByLines 按行分页元素
func (p *paginationBiz) PaginateElementsByLines(elements []Element) (*PaginatedContent, error) {
	return p.lineEngine.PaginateByLines(elements)
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

// PaginateFromJSONByLines 从JSON字符串进行按行分页
func (p *paginationBiz) PaginateFromJSONByLines(jsonStr string) (*PaginatedContent, error) {
	var elements []Element
	if err := json.Unmarshal([]byte(jsonStr), &elements); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return p.lineEngine.PaginateByLines(elements)
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
