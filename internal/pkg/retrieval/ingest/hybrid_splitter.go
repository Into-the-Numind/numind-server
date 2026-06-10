package ingest

import (
	"fmt"
	"log"
	"os"
)

// HybridSplitterConfig 混合切分器配置
type HybridSplitterConfig struct {
	// 规则切分配置
	RuleConfig EnhancedSplitterConfig

	// 语义切分配置
	SemanticConfig EmbeddingSplitterConfig

	// 阈值：文本长度超过此值才使用语义切分
	SemanticMinLength int
}

// HybridSplitter 混合切分器
type HybridSplitter struct {
	ruleSplitter      *EnhancedMarkdownSplitter
	semanticSplitter  *EmbeddingSplitter
	cfg               HybridSplitterConfig
	semanticAvailable bool
}

// NewHybridSplitter 创建混合切分器
func NewHybridSplitter(cfg HybridSplitterConfig) *HybridSplitter {
	// 设置默认值（与Split函数中的硬编码500保持一致）
	if cfg.SemanticMinLength == 0 {
		cfg.SemanticMinLength = 1500 // 500汉字 * 3字节
	}

	h := &HybridSplitter{
		cfg: cfg,
	}

	// 初始化规则切分器
	h.ruleSplitter = NewEnhancedMarkdownSplitter(cfg.RuleConfig)

	// 初始化语义切分器并检查可用性
	h.semanticSplitter = NewEmbeddingSplitter(cfg.SemanticConfig)
	h.semanticAvailable = h.semanticSplitter.IsAvailable()

	// 始终输出启动状态（不再依赖 SPLITTER_DEBUG 环境变量）
	if h.semanticAvailable {
		log.Println("[HybridSplitter] Semantic splitter available and ready")
	} else {
		log.Println("[HybridSplitter] WARNING: Semantic splitter not available, will use rule-based splitting. Dynamic reconnection is enabled.")
	}

	return h
}

// Split 执行切分（实现 TextSplitter 接口）
// 简化策略：
// 1. < 500字符：不切分，直接返回一个chunk
// 2. >= 500字符：优先语义切分，不可用则降级规则切分
func (h *HybridSplitter) Split(text string) ([]SplitChunk, error) {
	// [PRO] 动态重连机制：如果之前不可用，尝试重新检查一次并更新状态
	if !h.semanticAvailable {
		if h.semanticSplitter.IsAvailable() {
			h.semanticAvailable = true
			log.Println("[HybridSplitter] Semantic server detected! Switching to semantic splitting.")
		}
	}

	// 输出诊断日志（可以通过环境变量控制）
	if os.Getenv("SPLITTER_DEBUG") == "1" {
		log.Printf("[HybridSplitter] Text length: %d, SemanticAvailable: %v",
			len(text), h.semanticAvailable)
	}

	// 策略1：文本太短（< h.cfg.SemanticMinLength），不切分
	if len(text) < h.cfg.SemanticMinLength {
		if os.Getenv("SPLITTER_DEBUG") == "1" {
			log.Printf("[HybridSplitter] Text < %d bytes, no splitting needed", h.cfg.SemanticMinLength)
		}
		return []SplitChunk{{
			Content: text,
			Headers: []string{},
		}}, nil
	}

	// 策略2：文本足够长（>= h.cfg.SemanticMinLength），优先语义切分
	if h.semanticAvailable {
		log.Printf("[HybridSplitter] Using semantic splitting (text=%d bytes)", len(text))
		chunks, err := h.semanticSplitter.Split(text)
		if err != nil {
			// 语义切分失败，自动降级到规则切分（而非直接返回错误）
			log.Printf("[HybridSplitter] WARNING: Semantic split failed, falling back to rule-based: %v", err)
			return h.convertToSplitChunks(h.ruleSplitter.Split(text))
		}
		return chunks, nil
	}

	// 策略3：语义切分不可用，降级为规则切分
	log.Printf("[HybridSplitter] Semantic unavailable, using rule-based splitting (text=%d bytes)", len(text))
	return h.convertToSplitChunks(h.ruleSplitter.Split(text))
}

// SplitWithDetails 返回详细的切分信息
// 简化策略：
// 1. < 500字符：不切分，直接返回一个chunk
// 2. >= 500字符：优先语义切分，不可用则降级规则切分
func (h *HybridSplitter) SplitWithDetails(text string) ([]SplitChunk, map[string]interface{}, error) {
	// [PRO] 动态重连机制：如果之前不可用，尝试重新检查一次并更新状态
	if !h.semanticAvailable {
		if h.semanticSplitter.IsAvailable() {
			h.semanticAvailable = true
			log.Println("[HybridSplitter] Semantic server detected! Switching to semantic splitting.")
		}
	}

	details := map[string]interface{}{
		"semantic_available": h.semanticAvailable,
		"text_length":        len(text),
	}

	var chunks []SplitChunk
	var err error

	// 策略1：文本太短（< h.cfg.SemanticMinLength），不切分
	if len(text) < h.cfg.SemanticMinLength {
		details["strategy"] = "no_split"
		details["reason"] = fmt.Sprintf("text_too_short(<%d)", h.cfg.SemanticMinLength)
		chunks = []SplitChunk{{
			Content: text,
			Headers: []string{},
		}}
		return chunks, details, nil
	}

	// 策略2：文本足够长（>= h.cfg.SemanticMinLength），优先语义切分
	if h.semanticAvailable {
		details["strategy"] = "semantic"
		chunks, err = h.semanticSplitter.Split(text)
		if err != nil {
			// 语义切分失败，自动降级到规则切分
			log.Printf("[HybridSplitter] WARNING: Semantic split failed in SplitWithDetails, falling back: %v", err)
			details["strategy"] = "rule_fallback"
			details["semantic_error"] = err.Error()
			chunks, err = h.convertToSplitChunks(h.ruleSplitter.Split(text))
		}
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

// NewDefaultHybridSplitter 创建默认配置的混合切分器
// 简化策略：
// 1. < 500字符：不切分
// 2. >= 500字符：优先语义切分，不可用则降级规则切分
func NewDefaultHybridSplitter() *HybridSplitter {
	return NewHybridSplitter(HybridSplitterConfig{
		RuleConfig: EnhancedSplitterConfig{
			MaxChunkSize:    6000,
			MinChunkSize:    1500,
			OverlapSize:     300,
			EnableJieba:     true,
			ProtectMarkdown: true,
		},
		SemanticConfig: EmbeddingSplitterConfig{
			Threshold:    0.6,
			MinChunkSize: 500,
			MaxChunkSize: 2000,
			OverlapSize:  100,
		},
		SemanticMinLength: 1500,
	})
}
