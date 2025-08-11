package util

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
)

// CompressRequest 压缩请求数据，减少带宽占用
func CompressRequest(data interface{}) ([]byte, error) {
	// 将数据转换为JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data to JSON: %w", err)
	}

	// 使用gzip压缩
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	
	if _, err := gw.Write(jsonData); err != nil {
		return nil, fmt.Errorf("failed to write to gzip writer: %w", err)
	}
	
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// CompressText 压缩文本数据
func CompressText(text string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	
	if _, err := gw.Write([]byte(text)); err != nil {
		return nil, fmt.Errorf("failed to write text to gzip writer: %w", err)
	}
	
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// CompressMessages 压缩消息数组，用于AI模型请求
func CompressMessages(messages []map[string]string) ([]byte, error) {
	// 计算压缩前的数据大小
	originalSize := 0
	for _, msg := range messages {
		if content, ok := msg["content"]; ok {
			originalSize += len(content)
		}
	}

	// 如果数据较小，不进行压缩
	if originalSize < 1024 { // 小于1KB不压缩
		jsonData, err := json.Marshal(messages)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal messages: %w", err)
		}
		return jsonData, nil
	}

	// 压缩数据
	compressedData, err := CompressRequest(messages)
	if err != nil {
		return nil, err
	}

	// 计算压缩率
	compressionRatio := float64(len(compressedData)) / float64(originalSize) * 100
	
	// 如果压缩效果不好（压缩后反而更大），返回原始数据
	if compressionRatio > 90 {
		jsonData, err := json.Marshal(messages)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal messages: %w", err)
		}
		return jsonData, nil
	}

	return compressedData, nil
}

// CompressPrompt 压缩提示词，减少AI模型请求的带宽
func CompressPrompt(prompt string) ([]byte, error) {
	// 如果提示词较短，不进行压缩
	if len(prompt) < 512 { // 小于512字符不压缩
		return []byte(prompt), nil
	}

	// 压缩提示词
	compressedData, err := CompressText(prompt)
	if err != nil {
		return nil, err
	}

	// 计算压缩率
	compressionRatio := float64(len(compressedData)) / float64(len(prompt)) * 100
	
	// 如果压缩效果不好，返回原始数据
	if compressionRatio > 90 {
		return []byte(prompt), nil
	}

	return compressedData, nil
}

// DecompressResponse 解压缩响应数据
func DecompressResponse(compressedData []byte) ([]byte, error) {
	// 检查是否是gzip压缩的数据
	if !isGzipCompressed(compressedData) {
		return compressedData, nil // 不是压缩数据，直接返回
	}

	// 解压缩
	gr, err := gzip.NewReader(bytes.NewReader(compressedData))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	decompressedData, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("failed to read decompressed data: %w", err)
	}

	return decompressedData, nil
}

// isGzipCompressed 检查数据是否是gzip压缩的
func isGzipCompressed(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	// gzip魔数：0x1f 0x8b
	return data[0] == 0x1f && data[1] == 0x8b
}

// GetCompressionStats 获取压缩统计信息
func GetCompressionStats(originalData, compressedData []byte) map[string]interface{} {
	originalSize := len(originalData)
	compressedSize := len(compressedData)
	
	compressionRatio := float64(compressedSize) / float64(originalSize) * 100
	bytesSaved := originalSize - compressedSize
	
	return map[string]interface{}{
		"original_size":     originalSize,
		"compressed_size":   compressedSize,
		"compression_ratio": fmt.Sprintf("%.2f%%", compressionRatio),
		"bytes_saved":       bytesSaved,
		"efficiency":        fmt.Sprintf("%.2f%%", (1-compressionRatio/100)*100),
	}
}
