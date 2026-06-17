package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EmbeddingSplitterConfig 语义切分器配置
type EmbeddingSplitterConfig struct {
	Threshold    float64 // 相似度阈值，默认 0.5
	MinChunkSize int     // 最小切片大小，默认 500
	MaxChunkSize int     // 最大切片大小，默认 4000
	OverlapSize  int     // 重叠大小，默认 100
	ServerURL    string  // 语义切分服务地址，默认 http://localhost:9093
}

// EmbeddingChunk 语义切分结果
type EmbeddingChunk struct {
	Content          string   `json:"content"`
	BoundaryType     string   `json:"boundary_type"`
	SimilarityBefore *float64 `json:"similarity_before,omitempty"`
	AvgSimilarity    *float64 `json:"avg_similarity,omitempty"`
	SentenceCount    int      `json:"sentence_count"`
	HasPrefixOverlap bool     `json:"has_prefix_overlap,omitempty"`
	HasSuffixOverlap bool     `json:"has_suffix_overlap,omitempty"`
}

// SplitRequest 请求结构体
type SplitRequest struct {
	Text         string  `json:"text"`
	Threshold    float64 `json:"threshold"`
	MinChunkSize int     `json:"min_chunk_size"`
	MaxChunkSize int     `json:"max_chunk_size"`
	OverlapSize  int     `json:"overlap_size"`
}

// SplitResponse 响应结构体
type SplitResponse struct {
	Success     bool             `json:"success"`
	Chunks      []EmbeddingChunk `json:"chunks"`
	TotalChunks int              `json:"total_chunks"`
	Error       string           `json:"error"`
}

// EmbeddingSplitter 语义切分器（基于 bge-small 模型）
type EmbeddingSplitter struct {
	cfg         EmbeddingSplitterConfig
	httpClient  *http.Client // /split 用,长超时(长文本慢)
	probeClient *http.Client // /health 探活用,短超时——绝不能复用 600s client,否则语义服务慢响应会卡死入库
}

// NewEmbeddingSplitter 创建语义切分器
func NewEmbeddingSplitter(cfg EmbeddingSplitterConfig) *EmbeddingSplitter {
	// 设置默认值
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.5
	}
	if cfg.MinChunkSize == 0 {
		cfg.MinChunkSize = 500
	}
	if cfg.MaxChunkSize == 0 {
		cfg.MaxChunkSize = 4000
	}
	if cfg.OverlapSize == 0 {
		cfg.OverlapSize = 100
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = "http://localhost:9093"
	}

	return &EmbeddingSplitter{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 600 * time.Second, // 增加超时到 600s，长文本处理可能比较慢
		},
		probeClient: &http.Client{
			Timeout: 5 * time.Second, // 健康探活短超时,防慢响应卡死入库 goroutine
		},
	}
}

// splitInternal 内部切分逻辑。第二个返回值 retryable 表示该错误是否为瞬时错误
// (网络失败 / 5xx)——值得重试一次;4xx / 解码 / 业务失败不重试。
func (s *EmbeddingSplitter) splitInternal(text string) ([]EmbeddingChunk, bool, error) {
	if text == "" {
		return []EmbeddingChunk{}, false, nil
	}

	reqBody := SplitRequest{
		Text:         text,
		Threshold:    s.cfg.Threshold,
		MinChunkSize: s.cfg.MinChunkSize,
		MaxChunkSize: s.cfg.MaxChunkSize,
		OverlapSize:  s.cfg.OverlapSize,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := s.httpClient.Post(s.cfg.ServerURL+"/split", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		// 网络/连接/超时 → 瞬时,可重试
		return nil, true, fmt.Errorf("failed to call semantic server: %w (Please ensure python scripts/semantic_server.py is running)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		retryable := resp.StatusCode >= 500 // 5xx 瞬时可重试;4xx 是请求问题,重试无意义
		return nil, retryable, fmt.Errorf("semantic server returned status %d: %s", resp.StatusCode, string(body))
	}

	var result SplitResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, false, fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.Success {
		return nil, false, fmt.Errorf("semantic split error: %s", result.Error)
	}

	return result.Chunks, false, nil
}

// Split 执行语义切分。瞬时错误(网络/5xx)重试一次再返回(最终由 HybridSplitter 兜底)。
func (s *EmbeddingSplitter) Split(text string) ([]SplitChunk, error) {
	chunks, retryable, err := s.splitInternal(text)
	if err != nil && retryable {
		time.Sleep(200 * time.Millisecond)
		chunks, _, err = s.splitInternal(text)
	}
	if err != nil {
		return nil, err
	}

	// 转换为 SplitChunk 格式
	var result []SplitChunk
	for _, ec := range chunks {
		result = append(result, SplitChunk{
			Content: ec.Content,
			Headers: []string{}, // Embedding 切分没有标题信息
		})
	}

	return result, nil
}

// SplitEnhanced 返回详细的切分结果
func (s *EmbeddingSplitter) SplitEnhanced(text string) ([]EmbeddingChunk, error) {
	chunks, _, err := s.splitInternal(text)
	return chunks, err
}

// healthResponse 语义服务健康检查响应
type healthResponse struct {
	Status     string `json:"status"`
	ModelReady bool   `json:"model_ready"`
}

// IsAvailable 检查语义切分服务是否可用且模型已加载。用短超时 probeClient,
// 绝不复用 /split 的 600s client——否则语义服务慢响应会卡死调用方(入库 goroutine)。
func (s *EmbeddingSplitter) IsAvailable() bool {
	resp, err := s.probeClient.Get(s.cfg.ServerURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	// 解析响应，确认模型实际已加载（而非仅服务运行中）
	var health healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		// 解析失败时降级为仅检查 HTTP 状态码
		return true
	}

	return health.ModelReady
}
