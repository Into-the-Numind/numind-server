package service

import (
	"fmt"
	"log"
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
	// 设置默认值
	if cfg.SemanticMinLength == 0 {
		cfg.SemanticMinLength = 2000 // 超过2000字符才使用语义切分
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
func (h *HybridSplitter) Split(text string) ([]SplitChunk, error) {
	strategy := h.selectStrategy(text)

	switch strategy {
	case StrategySemanticOnly:
		if h.semanticAvailable {
			return h.semanticSplitter.Split(text)
		}
		return h.convertToSplitChunks(h.ruleSplitter.Split(text))

	case StrategyHybrid:
		return h.hybridSplit(text)

	case StrategyAuto:
		if len(text) > h.cfg.SemanticMinLength && h.semanticAvailable {
			return h.semanticSplitter.Split(text)
		}
		return h.convertToSplitChunks(h.ruleSplitter.Split(text))

	default: // StrategyRuleOnly
		return h.convertToSplitChunks(h.ruleSplitter.Split(text))
	}
}

// selectStrategy 选择切分策略
func (h *HybridSplitter) selectStrategy(text string) SplitStrategy {
	// 如果语义切分不可用，强制使用规则
	if !h.semanticAvailable {
		return StrategyRuleOnly
	}

	return h.cfg.Strategy
}

// hybridSplit 混合切分：先规则切分，再对大块进行语义切分
func (h *HybridSplitter) hybridSplit(text string) ([]SplitChunk, error) {
	// 步骤 1：先用规则切分
	ruleChunks, err := h.ruleSplitter.Split(text)
	if err != nil {
		return nil, fmt.Errorf("rule split failed: %w", err)
	}

	// 步骤 2：检查每个 chunk，如果太大则用语义切分细化
	var finalChunks []SplitChunk

	for _, chunk := range ruleChunks {
		// 如果 chunk 太大，用语义切分细化
		if len(chunk.Content) > h.cfg.SemanticConfig.MaxChunkSize {
			semanticChunks, err := h.semanticSplitter.Split(chunk.Content)
			if err != nil {
				// 语义切分失败，保留原 chunk
				log.Printf("[HybridSplitter] Semantic split failed for chunk, using rule chunk: %v", err)
				finalChunks = append(finalChunks, chunk.ConvertToSplitChunk())
				continue
			}

			// 将语义切分结果继承原 chunk 的 Headers
			for _, sc := range semanticChunks {
				sc.Headers = chunk.Headers
				finalChunks = append(finalChunks, sc)
			}
		} else {
			finalChunks = append(finalChunks, chunk.ConvertToSplitChunk())
		}
	}

	return finalChunks, nil
}

// SplitWithDetails 返回详细的切分信息
func (h *HybridSplitter) SplitWithDetails(text string) ([]SplitChunk, map[string]interface{}, error) {
	strategy := h.selectStrategy(text)

	details := map[string]interface{}{
		"strategy":            strategy.String(),
		"semantic_available":  h.semanticAvailable,
		"text_length":         len(text),
	}

	var chunks []SplitChunk
	var err error

	switch strategy {
	case StrategySemanticOnly:
		if h.semanticAvailable {
			semanticChunks, semErr := h.semanticSplitter.SplitEnhanced(text)
			if semErr != nil {
				err = semErr
				break
			}
			// 转换为 SplitChunk
			for _, sc := range semanticChunks {
				chunks = append(chunks, SplitChunk{
					Content: sc.Content,
					Headers: []string{},
				})
			}
			details["semantic_chunks"] = semanticChunks
		} else {
			chunks, err = h.convertToSplitChunks(h.ruleSplitter.Split(text))
		}

	case StrategyHybrid:
		chunks, err = h.hybridSplit(text)
		details["hybrid"] = true

	case StrategyAuto:
		if len(text) > h.cfg.SemanticMinLength && h.semanticAvailable {
			chunks, err = h.semanticSplitter.Split(text)
			details["auto_selected"] = "semantic"
		} else {
			chunks, err = h.convertToSplitChunks(h.ruleSplitter.Split(text))
			details["auto_selected"] = "rule"
		}

	default:
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
		Strategy:          StrategyAuto, // 默认自动选择
		SemanticMinLength: 2000,         // 超过2000字符使用语义切分
	})
}
