package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// EmbeddingSplitterConfig 语义切分器配置
type EmbeddingSplitterConfig struct {
	Threshold    float64 // 相似度阈值，默认 0.6
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
	cfg        EmbeddingSplitterConfig
	httpClient *http.Client
}

// NewEmbeddingSplitter 创建语义切分器
func NewEmbeddingSplitter(cfg EmbeddingSplitterConfig) *EmbeddingSplitter {
	// 设置默认值
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.6
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
	}
}

// splitInternal 内部切分逻辑
func (s *EmbeddingSplitter) splitInternal(text string) ([]EmbeddingChunk, error) {
	if text == "" {
		return []EmbeddingChunk{}, nil
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
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := s.httpClient.Post(s.cfg.ServerURL+"/split", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to call semantic server: %v (Please ensure python scripts/semantic_server.py is running)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("semantic server returned status %d: %s", resp.StatusCode, string(body))
	}

	var result SplitResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("semantic split error: %s", result.Error)
	}

	return result.Chunks, nil
}

// Split 执行语义切分
func (s *EmbeddingSplitter) Split(text string) ([]SplitChunk, error) {
	chunks, err := s.splitInternal(text)
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
	return s.splitInternal(text)
}

// IsAvailable 检查模型是否可用
func (s *EmbeddingSplitter) IsAvailable() bool {
	// 简单调用 health 接口
	resp, err := s.httpClient.Get(s.cfg.ServerURL + "/health")
	if err != nil {
		// 尝试检查脚本是否存在（作为后备检查，虽然主要看服务是否启动）
		scriptPath := "scripts/semantic_server.py"
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			scriptPath = "/app/scripts/semantic_server.py"
			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				return false
			}
		}
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
