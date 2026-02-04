package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// EmbeddingSplitterConfig 语义切分器配置
type EmbeddingSplitterConfig struct {
	Threshold    float64 // 相似度阈值，默认 0.6
	MinChunkSize int     // 最小切片大小，默认 100
	MaxChunkSize int     // 最大切片大小，默认 1000
	OverlapSize  int     // 重叠大小，默认 100
}

// EmbeddingChunk 语义切分结果
type EmbeddingChunk struct {
	Content          string  `json:"content"`
	BoundaryType     string  `json:"boundary_type"`
	SimilarityBefore *float64 `json:"similarity_before,omitempty"`
	AvgSimilarity    *float64 `json:"avg_similarity,omitempty"`
	SentenceCount    int     `json:"sentence_count"`
	HasPrefixOverlap bool    `json:"has_prefix_overlap,omitempty"`
	HasSuffixOverlap bool    `json:"has_suffix_overlap,omitempty"`
}

// EmbeddingSplitter 语义切分器（基于 bge-small 模型）
type EmbeddingSplitter struct {
	cfg EmbeddingSplitterConfig
}

// NewEmbeddingSplitter 创建语义切分器
func NewEmbeddingSplitter(cfg EmbeddingSplitterConfig) *EmbeddingSplitter {
	// 设置默认值
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.6
	}
	if cfg.MinChunkSize == 0 {
		cfg.MinChunkSize = 100
	}
	if cfg.MaxChunkSize == 0 {
		cfg.MaxChunkSize = 1000
	}
	if cfg.OverlapSize == 0 {
		cfg.OverlapSize = 100
	}

	return &EmbeddingSplitter{cfg: cfg}
}

// Split 执行语义切分
func (s *EmbeddingSplitter) Split(text string) ([]SplitChunk, error) {
	if text == "" {
		return []SplitChunk{}, nil
	}

	// 写入临时文件
	tmpFile, err := os.CreateTemp("", "semantic_split_*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(text); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("failed to write text: %w", err)
	}
	tmpFile.Close()

	// 调用 Python 脚本
	scriptPath := "scripts/semantic_splitter.py"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		scriptPath = "/app/scripts/semantic_splitter.py"
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("semantic splitter script not found")
		}
	}

	cmd := exec.Command(
		"python3",
		scriptPath,
		tmpFile.Name(),
		strconv.FormatFloat(s.cfg.Threshold, 'f', 2, 64),
		strconv.Itoa(s.cfg.MinChunkSize),
		strconv.Itoa(s.cfg.MaxChunkSize),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("semantic split failed: %v, stderr: %s", err, stderr.String())
	}

	// 解析结果
	var result struct {
		Success     bool             `json:"success"`
		Chunks      []EmbeddingChunk `json:"chunks"`
		Error       string           `json:"error"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("failed to parse result: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("semantic split error: %s", result.Error)
	}

	// 转换为 SplitChunk 格式
	var chunks []SplitChunk
	for _, ec := range result.Chunks {
		chunks = append(chunks, SplitChunk{
			Content: ec.Content,
			Headers: []string{}, // Embedding 切分没有标题信息
		})
	}

	return chunks, nil
}

// SplitEnhanced 返回详细的切分结果
func (s *EmbeddingSplitter) SplitEnhanced(text string) ([]EmbeddingChunk, error) {
	if text == "" {
		return []EmbeddingChunk{}, nil
	}

	// 写入临时文件
	tmpFile, err := os.CreateTemp("", "semantic_split_*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(text); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("failed to write text: %w", err)
	}
	tmpFile.Close()

	// 调用 Python 脚本
	scriptPath := "scripts/semantic_splitter.py"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		scriptPath = "/app/scripts/semantic_splitter.py"
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("semantic splitter script not found")
		}
	}

	cmd := exec.Command(
		"python3",
		scriptPath,
		tmpFile.Name(),
		strconv.FormatFloat(s.cfg.Threshold, 'f', 2, 64),
		strconv.Itoa(s.cfg.MinChunkSize),
		strconv.Itoa(s.cfg.MaxChunkSize),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("semantic split failed: %v, stderr: %s", err, stderr.String())
	}

	// 解析结果
	var result struct {
		Success     bool             `json:"success"`
		Chunks      []EmbeddingChunk `json:"chunks"`
		Error       string           `json:"error"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("failed to parse result: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("semantic split error: %s", result.Error)
	}

	return result.Chunks, nil
}

// IsAvailable 检查模型是否可用（兼容接口）
func (s *EmbeddingSplitter) IsAvailable() bool {
	scriptPath := "scripts/semantic_splitter.py"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		scriptPath = "/app/scripts/semantic_splitter.py"
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return false
		}
	}

	// 尝试运行一个简单的测试
	testFile, err := os.CreateTemp("", "test_*.txt")
	if err != nil {
		return false
	}
	defer os.Remove(testFile.Name())

	testFile.WriteString("测试句子。另一个句子。")
	testFile.Close()

	cmd := exec.Command("python3", scriptPath, testFile.Name(), "0.5", "10", "1000")
	return cmd.Run() == nil
}
