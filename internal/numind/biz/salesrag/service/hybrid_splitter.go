package service

import (
	"log"
	"os"
)

// HybridSplitterConfig 混合切分器配置
type HybridSplitterConfig struct {
	// 规则切分配置
	RuleConfig EnhancedSplitterConfig

	// 语义切分配置
	SemanticConfig EmbeddingSplitterConfig

	// 策略选择
	Strategy SplitStrategy

	// 阈值：文本长度超过此值才使用语义切分
	SemanticMinLength int
}

// SplitStrategy 切分策略
type SplitStrategy int

const (
	// StrategyRuleOnly 仅使用规则切分
	StrategyRuleOnly SplitStrategy = iota

	// StrategySemanticOnly 仅使用语义切分
	StrategySemanticOnly

	// StrategyHybrid 混合策略：先规则后语义优化
	StrategyHybrid

	// StrategyAuto 自动选择：长文本用语义，短文本用规则
	StrategyAuto
)

// HybridSplitter 混合切分器
type HybridSplitter struct {
	ruleSplitter     *EnhancedMarkdownSplitter
	semanticSplitter *EmbeddingSplitter
	cfg              HybridSplitterConfig
	semanticAvailable bool
}

// NewHybridSplitter 创建混合切分器
func NewHybridSplitter(cfg HybridSplitterConfig) *HybridSplitter {
	// 设置默认值（与Split函数中的硬编码500保持一致）
	if cfg.SemanticMinLength == 0 {
		cfg.SemanticMinLength = 500 // 简化为500字符阈值
	}

	h := &HybridSplitter{
		cfg: cfg,
	}

	// 初始化规则切分器
	h.ruleSplitter = NewEnhancedMarkdownSplitter(cfg.RuleConfig)

	// 初始化语义切分器并检查可用性
	h.semanticSplitter = NewEmbeddingSplitter(cfg.SemanticConfig)
	h.semanticAvailable = h.semanticSplitter.IsAvailable()

	if !h.semanticAvailable {
		log.Println("[HybridSplitter] Semantic splitter not available, falling back to rule-based only")
	}

	return h
}

// Split 执行切分（实现 TextSplitter 接口）
// 简化策略：
// 1. < 500字符：不切分，直接返回一个chunk
// 2. >= 500字符：优先语义切分，不可用则降级规则切分
func (h *HybridSplitter) Split(text string) ([]SplitChunk, error) {
	// 输出诊断日志（可以通过环境变量控制）
	if os.Getenv("SPLITTER_DEBUG") == "1" {
		log.Printf("[HybridSplitter] Text length: %d, SemanticAvailable: %v",
			len(text), h.semanticAvailable)
	}

	// 策略1：文本太短（<500字符），不切分
	if len(text) < 500 {
		if os.Getenv("SPLITTER_DEBUG") == "1" {
			log.Println("[HybridSplitter] Text < 500 chars, no splitting needed")
		}
		return []SplitChunk{{
			Content: text,
			Headers: []string{},
		}}, nil
	}

	// 策略2：文本足够长（>=500字符），优先语义切分
	if h.semanticAvailable {
		if os.Getenv("SPLITTER_DEBUG") == "1" {
			log.Println("[HybridSplitter] Text >= 500 chars, using semantic splitting")
		}
		return h.semanticSplitter.Split(text)
	}

	// 策略3：语义切分不可用，降级为规则切分
	if os.Getenv("SPLITTER_DEBUG") == "1" {
		log.Println("[HybridSplitter] Semantic unavailable, fallback to rule splitting")
	}
	return h.convertToSplitChunks(h.ruleSplitter.Split(text))
}

// SplitWithDetails 返回详细的切分信息
// 简化策略：
// 1. < 500字符：不切分，直接返回一个chunk
// 2. >= 500字符：优先语义切分，不可用则降级规则切分
func (h *HybridSplitter) SplitWithDetails(text string) ([]SplitChunk, map[string]interface{}, error) {
	details := map[string]interface{}{
		"semantic_available": h.semanticAvailable,
		"text_length":        len(text),
	}

	var chunks []SplitChunk
	var err error

	// 策略1：文本太短（<500字符），不切分
	if len(text) < 500 {
		details["strategy"] = "no_split"
		details["reason"] = "text_too_short(<500)"
		chunks = []SplitChunk{{
			Content: text,
			Headers: []string{},
		}}
		return chunks, details, nil
	}

	// 策略2：文本足够长（>=500字符），优先语义切分
	if h.semanticAvailable {
		details["strategy"] = "semantic"
		chunks, err = h.semanticSplitter.Split(text)
	} else {
		// 策略3：语义切分不可用，降级为规则切分
		details["strategy"] = "rule"
		details["reason"] = "semantic_unavailable"
		chunks, err = h.convertToSplitChunks(h.ruleSplitter.Split(text))
	}

	if err != nil {
		return nil, details, err
	}

	details["chunk_count"] = len(chunks)
	return chunks, details, nil
}

// IsSemanticAvailable 检查语义切分是否可用
func (h *HybridSplitter) IsSemanticAvailable() bool {
	return h.semanticAvailable
}

// convertToSplitChunks 将 EnhancedSplitChunk 转换为 SplitChunk
func (h *HybridSplitter) convertToSplitChunks(chunks []EnhancedSplitChunk, err error) ([]SplitChunk, error) {
	if err != nil {
		return nil, err
	}
	var result []SplitChunk
	for _, c := range chunks {
		result = append(result, c.ConvertToSplitChunk())
	}
	return result, nil
}

// String 返回策略名称
func (s SplitStrategy) String() string {
	switch s {
	case StrategyRuleOnly:
		return "rule_only"
	case StrategySemanticOnly:
		return "semantic_only"
	case StrategyHybrid:
		return "hybrid"
	case StrategyAuto:
		return "auto"
	default:
		return "unknown"
	}
}

// NewDefaultHybridSplitter 创建默认配置的混合切分器
// 简化策略：
// 1. < 500字符：不切分
// 2. >= 500字符：优先语义切分，不可用则降级规则切分
func NewDefaultHybridSplitter() *HybridSplitter {
	return NewHybridSplitter(HybridSplitterConfig{
		RuleConfig: EnhancedSplitterConfig{
			MaxChunkSize:    1000,
			MinChunkSize:    200,
			OverlapSize:     100,
			EnableJieba:     true,
			ProtectMarkdown: true,
		},
		SemanticConfig: EmbeddingSplitterConfig{
			Threshold:    0.6,
			MinChunkSize: 100,
			MaxChunkSize: 1000,
			OverlapSize:  100,
		},
		Strategy:          StrategyAuto,
		SemanticMinLength: 500, // 简化为500字符阈值
	})
}
