package ingest

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// semanticProbeTTL 是语义服务可用性的重探周期：超过此时长则在下次切分时重探一次,
// 让语义服务崩溃后恢复、或启动窗口后就绪时能被自动重新启用,无需重启容器。
const semanticProbeTTL = 30 * time.Second

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
	ruleSplitter     *EnhancedMarkdownSplitter
	semanticSplitter *EmbeddingSplitter
	cfg              HybridSplitterConfig

	// 并发安全:semanticAvailable/lastProbeAt 在单例上被多次切分调用读写,必须加锁(go test -race)。
	mu                sync.Mutex
	semanticAvailable bool
	lastProbeAt       time.Time
}

// refreshAvailability 返回语义服务当前是否可用,带 TTL 周期重探(check-on-call,不起常驻 goroutine)。
// 探活的 HTTP 调用在锁外执行(短超时 probeClient),只在锁内读写状态字段,避免长时间持锁卡住入库。
func (h *HybridSplitter) refreshAvailability() bool {
	if h.semanticSplitter == nil {
		return false
	}
	h.mu.Lock()
	needProbe := h.lastProbeAt.IsZero() || time.Since(h.lastProbeAt) > semanticProbeTTL
	cur := h.semanticAvailable
	h.mu.Unlock()

	if !needProbe {
		return cur
	}

	avail := h.semanticSplitter.IsAvailable() // 短超时,锁外
	h.mu.Lock()
	if avail != h.semanticAvailable {
		log.Printf("[HybridSplitter] semantic availability changed: %v -> %v", h.semanticAvailable, avail)
	}
	h.semanticAvailable = avail
	h.lastProbeAt = time.Now()
	h.mu.Unlock()
	return avail
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

	// 初始化语义切分器并检查可用性(记 lastProbeAt,纳入 TTL 周期重探)
	h.semanticSplitter = NewEmbeddingSplitter(cfg.SemanticConfig)
	h.semanticAvailable = h.semanticSplitter.IsAvailable()
	h.lastProbeAt = time.Now()

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
	// 带 TTL 周期重探的可用性检查(并发安全;语义崩溃恢复后能自动重新启用)
	available := h.refreshAvailability()

	// 输出诊断日志（可以通过环境变量控制）
	if os.Getenv("SPLITTER_DEBUG") == "1" {
		log.Printf("[HybridSplitter] Text length: %d, SemanticAvailable: %v",
			len(text), available)
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
	if available {
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
	// 带 TTL 周期重探的可用性检查(并发安全)
	available := h.refreshAvailability()

	details := map[string]interface{}{
		"semantic_available": available,
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
	if available {
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
		// 策略3：语义切分不可用，降级为规则切分。
		// 注:内部用 "rule" 表示"语义从未可用",会被 normalizeStrategy 归一为 rule_fallback；
		// 改这个字面量需同步 normalizeStrategy。
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

// IsSemanticAvailable 检查语义切分是否可用(并发安全读)
func (h *HybridSplitter) IsSemanticAvailable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
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
			// 与 NewCompatibilitySplitter 的兜底档保持一致(好兜底,贴近语义档),
			// 避免直接用 NewSplitterAdapter 的路径退回 6000 大块。
			MaxChunkSize:    1800,
			MinChunkSize:    900,
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
