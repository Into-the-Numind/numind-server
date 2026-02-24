package rag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// StreamHandler 流式处理函数类型
type StreamHandler func(chunk string) error

// GenerateRAGResponseStream 生成带RAG的流式回答
func GenerateRAGResponseStream(
	ctx context.Context,
	ds store.IStore,
	aliBiz ali.AliBiz,
	volcBiz volc.VolcBiz,
	userID uint,
	query string,
	bookID uint,
	handler StreamHandler,
) error {
	// 1. 检索相关笔记
	log.C(ctx).Infow("开始检索相关笔记", "user_id", userID, "query", query, "book_id", bookID)
	notes, err := RetrieveNotes(ctx, ds, userID, query, bookID, 5)
	if err != nil {
		log.C(ctx).Errorw("检索笔记失败", "error", err)
		// 即使检索失败，也继续生成回答
		notes = []NoteContent{}
	}

	log.C(ctx).Infow("检索到相关笔记", "count", len(notes))

	// 2. 构建上下文
	contextText := buildContext(query, notes)

	// 3. 构建提示词
	prompt := buildRAGPrompt(query, contextText)

	// 4. 调用流式API
	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	// 优先使用火山方舟，失败后降级到阿里百炼
	err = callVolcStream(ctx, messages, handler)
	if err != nil {
		log.C(ctx).Warnw("火山方舟流式API失败，尝试阿里百炼", "error", err)
		err = callAliStream(ctx, aliBiz, messages, handler)
		if err != nil {
			log.C(ctx).Errorw("所有流式API都失败", "error", err)
			return fmt.Errorf("流式API调用失败: %w", err)
		}
	}

	return nil
}

// buildContext 构建上下文文本
func buildContext(query string, notes []NoteContent) string {
	if len(notes) == 0 {
		return "未找到相关笔记内容。"
	}

	var builder strings.Builder
	builder.WriteString("以下是基于用户笔记检索到的相关内容：\n\n")

	for i, note := range notes {
		builder.WriteString(fmt.Sprintf("【笔记 %d: %s】\n", i+1, note.BookTitle))
		builder.WriteString(note.Content)
		builder.WriteString("\n\n")
	}

	return builder.String()
}

// buildRAGPrompt 构建RAG提示词
func buildRAGPrompt(query, context string) string {
	return fmt.Sprintf(`你是一位智能助手，专门帮助用户基于他们创建的笔记内容回答问题。

%s

## 用户问题
%s

## 回答要求
1. 基于上述上下文信息回答用户的问题
2. 如果上下文中包含相关信息，请优先使用这些信息
3. 如果上下文中没有相关信息，可以基于你的知识回答，但要说明这是基于通用知识
4. 回答要准确、简洁、有帮助
5. 使用中文回答

请直接回答用户的问题，不要包含"根据上下文"等前缀。`, context, query)
}

// callVolcStream 调用火山方舟流式API
func callVolcStream(ctx context.Context, messages []map[string]string, handler StreamHandler) error {
	baseURL := viper.GetString("volc.base_url")
	if baseURL == "" {
		return fmt.Errorf("volc base_url not configured")
	}
	url := baseURL + "/chat/completions"

	bodyMap := map[string]interface{}{
		"model":       viper.GetString("volc.model"),
		"messages":    messages,
		"max_tokens":  4000,
		"temperature": 0.7,
		"stream":      true,
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("volc.api_key"))

	// 使用包级别共享 Transport 复用连接池
	client := &http.Client{Transport: streamTransport}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP错误: %d", resp.StatusCode)
	}

	// 流式读取响应
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				if choices, ok := m["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								if err := handler(content); err != nil {
									return err
								}
							}
						}
					}
				}
			}
		}
	}

	return scanner.Err()
}

// callAliStream 调用阿里百炼流式API
func callAliStream(ctx context.Context, aliBiz ali.AliBiz, messages []map[string]string, handler StreamHandler) error {
	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"

	bodyMap := map[string]interface{}{
		"model":       viper.GetString("ali.text.model"),
		"messages":    messages,
		"max_tokens":  4000,
		"temperature": 0.7,
		"stream":      true,
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("ali.text.api_key"))

	// 使用包级别共享 Transport 复用连接池
	client := &http.Client{Transport: streamTransport}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP错误: %d", resp.StatusCode)
	}

	// 流式读取响应
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				if choices, ok := m["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								if err := handler(content); err != nil {
									return err
								}
							}
						}
					}
				}
			}
		}
	}

	return scanner.Err()
}
