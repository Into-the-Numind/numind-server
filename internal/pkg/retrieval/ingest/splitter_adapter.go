package ingest

import "fmt"

// Split strategy 归一值（只对外暴露这三种）。
const (
	StrategySemantic = "semantic"      // 走了语义切分
	StrategyFallback = "rule_fallback" // 走了规则兜底（语义不可用/失败,对用户/统计无区别）
	StrategyNoSplit  = "no_split"      // 文本过短未切分
)

// StrategyAwareSplitter 是可选接口：在切分的同时返回用了哪种策略 + 原因。
// pipeline 通过类型断言使用它（断言失败则降级用 TextSplitter.Split）,因此不污染
// 现有 TextSplitter 接口、不破坏既有 mock。
type StrategyAwareSplitter interface {
	// SplitWithStrategy 切分并返回 (chunks, strategy, detail, err)。
	// 不变式：err 恒为 nil 且 chunks 非空（除非 text 本身为空）——切块层永不让入库失败。
	SplitWithStrategy(text string) ([]SplitChunk, string, string, error)
}

// normalizeStrategy 把 HybridSplitter.SplitWithDetails 的 4 值
// (no_split/semantic/rule/rule_fallback) 归一为对外的 3 值。
// "rule"(语义从未可用) 与 "rule_fallback"(试了语义失败) 对用户/统计无区别 → 都算 rule_fallback。
func normalizeStrategy(s string) string {
	switch s {
	case StrategySemantic:
		return StrategySemantic
	case StrategyNoSplit:
		return StrategyNoSplit
	default: // "rule" / "rule_fallback" / 未知 → 一律兜底
		return StrategyFallback
	}
}

// detailFromSplitDetails 从 SplitWithDetails 的 details 里提取人类可读原因,用于留痕。
func detailFromSplitDetails(details map[string]interface{}) string {
	if details == nil {
		return ""
	}
	if e, ok := details["semantic_error"].(string); ok && e != "" {
		return "semantic_error: " + e
	}
	if r, ok := details["reason"].(string); ok && r != "" {
		return r
	}
	return ""
}

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

// SplitWithStrategy 切分并返回归一后的策略 + 原因。永不返回 err、永不返回空 chunk
// （除非 text 为空）——切块层绝不让入库失败（北极星不变式）。
func (a *SplitterAdapter) SplitWithStrategy(text string) ([]SplitChunk, string, string, error) {
	chunks, details, err := a.hybrid.SplitWithDetails(text)
	strategy := StrategyFallback
	if details != nil {
		if s, ok := details["strategy"].(string); ok {
			strategy = normalizeStrategy(s)
		}
	}
	detail := detailFromSplitDetails(details)

	if err != nil {
		// 兜底也出错：最后保底——整段文本作为 1 个 chunk,绝不让上传失败。
		strategy = StrategyFallback
		if detail == "" {
			detail = fmt.Sprintf("split_error: %v", err)
		}
		if len(chunks) == 0 {
			chunks = []SplitChunk{{Content: text, Headers: []string{}}}
		}
	}
	if len(chunks) == 0 && text != "" {
		chunks = []SplitChunk{{Content: text, Headers: []string{}}}
	}
	return chunks, strategy, detail, nil
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
			MaxChunkSize:    6000, // 2000汉字 * 3
			MinChunkSize:    1500, // 500汉字 * 3
			OverlapSize:     300,  // 100汉字 * 3
			EnableJieba:     true,
			ProtectMarkdown: true,
		},
		SemanticConfig: EmbeddingSplitterConfig{
			Threshold:    0.6,
			MinChunkSize: 500,  // 语义切分按字符计算，500字符
			MaxChunkSize: 2000, // 语义切分按字符计算，2000字符
			OverlapSize:  100,  // 语义切分按字符计算，100字符
		},
		SemanticMinLength: 1500, // 500汉字 * 3字节
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

// SplitWithStrategy 转发给底层 adapter,实现 StrategyAwareSplitter。
func (s *CompatibilitySplitter) SplitWithStrategy(text string) ([]SplitChunk, string, string, error) {
	return s.adapter.SplitWithStrategy(text)
}

// Close 释放资源
func (s *CompatibilitySplitter) Close() {
	s.adapter.Close()
}
