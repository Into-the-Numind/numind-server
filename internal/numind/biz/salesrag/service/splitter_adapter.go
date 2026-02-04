package service

// SplitterAdapter 切分器适配器
// 用于兼容旧的 MarkdownSplitter 接口和新的混合切分器
type SplitterAdapter struct {
	hybrid *HybridSplitter
}

// NewSplitterAdapter 创建适配器（使用混合切分器）
func NewSplitterAdapter() *SplitterAdapter {
	return &SplitterAdapter{
		hybrid: NewDefaultHybridSplitter(),
	}
}

// NewSplitterAdapterWithHybridConfig 使用自定义混合配置创建适配器
func NewSplitterAdapterWithHybridConfig(cfg HybridSplitterConfig) *SplitterAdapter {
	return &SplitterAdapter{
		hybrid: NewHybridSplitter(cfg),
	}
}

// Split 实现旧接口，内部使用混合切分器
func (a *SplitterAdapter) Split(text string) ([]SplitChunk, error) {
	return a.hybrid.Split(text)
}

// SplitWithDetails 返回详细的切分信息
func (a *SplitterAdapter) SplitWithDetails(text string) ([]SplitChunk, map[string]interface{}, error) {
	return a.hybrid.SplitWithDetails(text)
}

// IsSemanticAvailable 检查语义切分是否可用
func (a *SplitterAdapter) IsSemanticAvailable() bool {
	return a.hybrid.IsSemanticAvailable()
}

// Close 释放资源
func (a *SplitterAdapter) Close() {
	// 混合切分器可能需要关闭资源
}

// CompatibilitySplitter 完全兼容旧接口的切分器
// 可以直接替换原有的 NewMarkdownSplitter
type CompatibilitySplitter struct {
	cfg     SplitterConfig
	adapter *SplitterAdapter
}

// NewCompatibilitySplitter 创建兼容切分器（使用混合策略）
// 简化策略：
// 1. < 500字符：不切分
// 2. >= 500字符：优先语义切分，不可用则降级规则切分
func NewCompatibilitySplitter(cfg SplitterConfig) *CompatibilitySplitter {
	// 创建混合配置
	hybridCfg := HybridSplitterConfig{
		RuleConfig: EnhancedSplitterConfig{
			MaxChunkSize:    cfg.MaxChunkSize,
			MinChunkSize:    cfg.MinChunkSize,
			OverlapSize:     100,
			EnableJieba:     true,
			ProtectMarkdown: true,
		},
		SemanticConfig: EmbeddingSplitterConfig{
			Threshold:    0.6,
			MinChunkSize: 100,
			MaxChunkSize: cfg.MaxChunkSize,
			OverlapSize:  100,
		},
		Strategy:          StrategyAuto,
		SemanticMinLength: 500, // 简化为500字符阈值
	}

	return &CompatibilitySplitter{
		cfg:     cfg,
		adapter: NewSplitterAdapterWithHybridConfig(hybridCfg),
	}
}

// Split 实现完全兼容的接口
func (s *CompatibilitySplitter) Split(text string) ([]SplitChunk, error) {
	return s.adapter.Split(text)
}

// Close 释放资源
func (s *CompatibilitySplitter) Close() {
	s.adapter.Close()
}
