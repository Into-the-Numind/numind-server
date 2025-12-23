package sop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// SopExecutor SOP执行器
type SopExecutor struct {
	ds store.IStore
}

// NewSopExecutor 创建SOP执行器
func NewSopExecutor(ds store.IStore) *SopExecutor {
	return &SopExecutor{
		ds: ds,
	}
}

// LLMRequest 大模型请求结构
type LLMRequest struct {
	Model    string       `json:"model"`
	Messages []LLMMessage `json:"messages"`
	Stream   bool         `json:"stream"`
}

// LLMMessage 大模型消息结构
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMResponse 大模型响应结构
type LLMResponse struct {
	Choices []struct {
		Message LLMMessage `json:"message"`
	} `json:"choices"`
}

// StreamHandler 流式处理函数类型
type StreamHandler func(chunk string) error

// Execute 执行SOP流程
func (e *SopExecutor) Execute(ctx context.Context, run *model.SopRun, nodes []model.SopNode, text string) error {
	log.C(ctx).Infow("Starting SOP execution", "run_id", run.ID, "template_id", run.TemplateID)

	// 更新Run状态为running
	startTime := time.Now()
	if err := e.ds.Sop().UpdateRun(run.ID, map[string]interface{}{
		"status":     model.SopStatusRunning,
		"started_at": startTime,
	}); err != nil {
		return fmt.Errorf("failed to update run status: %w", err)
	}

	// 构建对话历史（用于保持上下文连贯）
	conversationHistory := []LLMMessage{}

	// 获取模板信息（用于预处理提示词）
	template, err := e.ds.Sop().GetTemplate(run.TemplateID)
	if err == nil && template != nil && template.Prompt != "" {
		// 如果模板有预处理提示词，添加到对话历史中
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "system",
			Content: template.Prompt,
		})
		log.C(ctx).Infow("Added template preprocessing prompt", "template_id", run.TemplateID)
	}

	currentInput := text

	// 按sort顺序执行节点（线性执行）
	for i, node := range nodes {
		log.C(ctx).Infow("Executing node", "run_id", run.ID, "node_id", node.ID, "node_name", node.Name, "sort", node.Sort)

		// 创建NodeRun记录
		nodeRun := &model.SopNodeRun{
			RunID:          run.ID,
			NodeID:         node.ID,
			TemplateID:     run.TemplateID,
			UserID:         run.UserID,
			ParentNodeID:   node.ParentID,
			Status:         model.SopStatusPending,
			Input:          currentInput,
			ConversationID: run.ConversationID,
			Sort:           i,
		}

		if err := e.ds.Sop().CreateNodeRun(nodeRun); err != nil {
			e.failRun(ctx, run.ID, fmt.Sprintf("failed to create node run: %v", err))
			return err
		}

		// 执行节点
		output, err := e.executeNode(ctx, &node, currentInput, conversationHistory)
		nodeEndTime := time.Now()
		latency := nodeEndTime.Sub(startTime).Milliseconds()

		if err != nil {
			// 节点执行失败
			log.C(ctx).Errorw("Node execution failed", "run_id", run.ID, "node_id", node.ID, "error", err)

			e.ds.Sop().UpdateNodeRun(nodeRun.ID, map[string]interface{}{
				"status":        model.SopStatusFailed,
				"error_message": err.Error(),
				"latency_ms":    latency,
				"finished_at":   nodeEndTime,
			})

			e.failRun(ctx, run.ID, fmt.Sprintf("node %s execution failed: %v", node.Name, err))
			return err
		}

		// 更新NodeRun为成功
		e.ds.Sop().UpdateNodeRun(nodeRun.ID, map[string]interface{}{
			"status":      model.SopStatusSucceeded,
			"output":      output,
			"latency_ms":  latency,
			"finished_at": nodeEndTime,
		})

		log.C(ctx).Infow("Node execution succeeded", "run_id", run.ID, "node_id", node.ID, "latency_ms", latency)

		// 将节点的输入输出加入对话历史
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "user",
			Content: currentInput,
		})
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "assistant",
			Content: output,
		})

		// 下一个节点的输入是当前节点的输出
		currentInput = output
		startTime = nodeEndTime
	}

	// 所有节点执行成功，生成最终Note
	finalOutput := currentInput
	note, err := e.CreateFinalNote(ctx, run, finalOutput)
	if err != nil {
		log.C(ctx).Errorw("Failed to create final note", "run_id", run.ID, "error", err)
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to create note: %v", err))
		return err
	}

	// 更新Run状态为succeeded
	finishTime := time.Now()
	if err := e.ds.Sop().UpdateRun(run.ID, map[string]interface{}{
		"status":        model.SopStatusSucceeded,
		"final_note_id": note.ID,
		"finished_at":   finishTime,
	}); err != nil {
		log.C(ctx).Errorw("Failed to update run status to succeeded", "run_id", run.ID, "error", err)
		return err
	}

	log.C(ctx).Infow("SOP execution completed successfully", "run_id", run.ID, "note_id", note.ID)
	return nil
}

// executeNode 执行单个节点
func (e *SopExecutor) executeNode(ctx context.Context, node *model.SopNode, input string, history []LLMMessage) (string, error) {
	// 检查API密钥是否配置
	if node.APIKey == "" {
		log.C(ctx).Errorw("Node API key is empty", "node_id", node.ID, "node_name", node.Name)
		return "", fmt.Errorf("node %s (ID: %d) API key is not configured, please update the node with a valid API key", node.Name, node.ID)
	}

	// 脱敏日志：只显示前4位和后4位
	maskedKey := node.APIKey
	if len(node.APIKey) > 8 {
		maskedKey = node.APIKey[:4] + "****" + node.APIKey[len(node.APIKey)-4:]
	}
	log.C(ctx).Infow("Executing node with LLM API",
		"node_id", node.ID,
		"node_name", node.Name,
		"base_url", node.BaseURL,
		"model", node.ModelName,
		"api_key_masked", maskedKey)

	// 构建请求消息
	messages := make([]LLMMessage, len(history))
	copy(messages, history)

	// 添加当前输入
	// 如果节点有prompt模板，使用模板；否则直接使用输入
	var userMessage string
	if node.Prompt != "" {
		userMessage = fmt.Sprintf("%s\n\n%s", node.Prompt, input)
	} else {
		userMessage = input
	}

	messages = append(messages, LLMMessage{
		Role:    "user",
		Content: userMessage,
	})

	// 构建请求
	reqBody := LLMRequest{
		Model:    node.ModelName,
		Messages: messages,
		Stream:   false,
	}

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// 记录请求信息（用于调试）
	log.C(ctx).Infow("LLM API request details",
		"url", node.BaseURL,
		"model", node.ModelName,
		"messages_count", len(messages),
		"request_body_length", len(reqData))

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", node.BaseURL, bytes.NewBuffer(reqData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.APIKey)
	req.Header.Set("User-Agent", "numind-server/1.0")

	// 设置超时
	client := &http.Client{
		Timeout: time.Duration(node.TimeoutSeconds) * time.Second,
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call LLM API: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.C(ctx).Errorw("LLM API error response",
			"status_code", resp.StatusCode,
			"url", node.BaseURL,
			"model", node.ModelName,
			"response_body", string(body))
		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(body))
	}

	log.C(ctx).Infow("LLM API response received",
		"status_code", resp.StatusCode,
		"body_length", len(body))

	// 解析响应
	var llmResp LLMResponse
	if err := json.Unmarshal(body, &llmResp); err != nil {
		log.C(ctx).Errorw("Failed to decode LLM response",
			"error", err,
			"body", string(body))
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(llmResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	output := llmResp.Choices[0].Message.Content
	return output, nil
}

// ExecuteNodeStream 流式执行单个节点（公开方法）
func (e *SopExecutor) ExecuteNodeStream(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, handler StreamHandler) (string, error) {
	// 检查API密钥是否配置
	if node.APIKey == "" {
		log.C(ctx).Errorw("Node API key is empty", "node_id", node.ID, "node_name", node.Name)
		return "", fmt.Errorf("node %s (ID: %d) API key is not configured, please update the node with a valid API key", node.Name, node.ID)
	}

	// 脱敏日志：只显示前4位和后4位
	maskedKey := node.APIKey
	if len(node.APIKey) > 8 {
		maskedKey = node.APIKey[:4] + "****" + node.APIKey[len(node.APIKey)-4:]
	}
	log.C(ctx).Infow("Executing node with LLM API (stream)",
		"node_id", node.ID,
		"node_name", node.Name,
		"base_url", node.BaseURL,
		"model", node.ModelName,
		"api_key_masked", maskedKey)

	// 构建请求消息
	messages := make([]LLMMessage, len(history))
	copy(messages, history)

	// 添加当前输入
	// 如果节点有prompt模板，使用模板；否则直接使用输入
	var userMessage string
	if node.Prompt != "" {
		userMessage = fmt.Sprintf("%s\n\n%s", node.Prompt, input)
	} else {
		userMessage = input
	}

	messages = append(messages, LLMMessage{
		Role:    "user",
		Content: userMessage,
	})

	// 构建请求（启用流式）
	reqBody := LLMRequest{
		Model:    node.ModelName,
		Messages: messages,
		Stream:   true,
	}

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", node.BaseURL, bytes.NewBuffer(reqData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.APIKey)
	req.Header.Set("User-Agent", "numind-server/1.0")

	// 对于流式响应，不能使用Client.Timeout（会限制整个响应读取时间）
	// 而是使用Transport的ResponseHeaderTimeout来控制响应头超时
	// 流式响应的读取时间由context控制
	transport := &http.Transport{
		ResponseHeaderTimeout: time.Duration(node.TimeoutSeconds) * time.Second, // 响应头超时
		IdleConnTimeout:       90 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		// 不设置Timeout，让流式响应可以持续读取
		// Timeout会限制整个请求-响应周期，包括读取响应体的时间
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call LLM API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.C(ctx).Errorw("LLM API error response",
			"status_code", resp.StatusCode,
			"url", node.BaseURL,
			"model", node.ModelName,
			"response_body", string(body))
		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 流式读取响应
	var fullOutput strings.Builder
	scanner := bufio.NewScanner(resp.Body)

	// 设置更大的缓冲区以处理长行
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB缓冲区

	for scanner.Scan() {
		// 检查context是否被取消
		select {
		case <-ctx.Done():
			log.C(ctx).Warnw("Context cancelled during stream", "node_id", node.ID)
			// 即使context取消，也返回已累积的输出
			if fullOutput.Len() > 0 {
				return fullOutput.String(), nil
			}
			return "", fmt.Errorf("stream cancelled: %w", ctx.Err())
		default:
		}

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
								// 累积完整输出
								fullOutput.WriteString(content)
								// 调用handler发送chunk
								if err := handler(content); err != nil {
									log.C(ctx).Warnw("Stream handler error, but continuing to read full response", "error", err)
									// 不立即返回错误，继续处理完整响应
									// 如果客户端断开连接，handler会返回错误，但我们继续读取完整响应以便保存
								}
							}
						}
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.C(ctx).Errorw("Scanner error during stream", "error", err)
		// 即使scanner出错，也返回已累积的输出（如果有）
		if fullOutput.Len() > 0 {
			return fullOutput.String(), nil
		}
		return "", fmt.Errorf("scanner error: %w", err)
	}

	output := fullOutput.String()
	if output == "" {
		return "", fmt.Errorf("LLM returned empty response")
	}

	return output, nil
}

// ExecuteNodeStreamWithThinking 流式执行单个节点，并分离思考内容和实际内容
func (e *SopExecutor) ExecuteNodeStreamWithThinking(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, handler StreamHandler) (string, string, error) {
	// 检查API密钥是否配置
	if node.APIKey == "" {
		log.C(ctx).Errorw("Node API key is empty", "node_id", node.ID, "node_name", node.Name)
		return "", "", fmt.Errorf("node %s (ID: %d) API key is not configured, please update the node with a valid API key", node.Name, node.ID)
	}

	// 脱敏日志：只显示前4位和后4位
	maskedKey := node.APIKey
	if len(node.APIKey) > 8 {
		maskedKey = node.APIKey[:4] + "****" + node.APIKey[len(node.APIKey)-4:]
	}
	log.C(ctx).Infow("Executing node with LLM API (stream with thinking)",
		"node_id", node.ID,
		"node_name", node.Name,
		"base_url", node.BaseURL,
		"model", node.ModelName,
		"api_key_masked", maskedKey)

	// 构建请求消息
	messages := make([]LLMMessage, len(history))
	copy(messages, history)

	// 添加当前输入
	// 如果节点有prompt模板，使用模板；否则直接使用输入
	var userMessage string
	if node.Prompt != "" {
		userMessage = fmt.Sprintf("%s\n\n%s", node.Prompt, input)
	} else {
		userMessage = input
	}

	messages = append(messages, LLMMessage{
		Role:    "user",
		Content: userMessage,
	})

	// 构建请求（启用流式）
	reqBody := LLMRequest{
		Model:    node.ModelName,
		Messages: messages,
		Stream:   true,
	}

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", node.BaseURL, bytes.NewBuffer(reqData))
	if err != nil {
		return "", "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.APIKey)
	req.Header.Set("User-Agent", "numind-server/1.0")

	// 对于流式响应，不能使用Client.Timeout（会限制整个响应读取时间）
	// 而是使用Transport的ResponseHeaderTimeout来控制响应头超时
	// 流式响应的读取时间由context控制
	transport := &http.Transport{
		ResponseHeaderTimeout: time.Duration(node.TimeoutSeconds) * time.Second, // 响应头超时
		IdleConnTimeout:       90 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		// 不设置Timeout，让流式响应可以持续读取
		// Timeout会限制整个请求-响应周期，包括读取响应体的时间
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to call LLM API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.C(ctx).Errorw("LLM API error response",
			"status_code", resp.StatusCode,
			"url", node.BaseURL,
			"model", node.ModelName,
			"response_body", string(body))
		return "", "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 流式读取响应
	var fullOutput strings.Builder
	scanner := bufio.NewScanner(resp.Body)

	// 设置更大的缓冲区以处理长行
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB缓冲区

	for scanner.Scan() {
		// 检查context是否被取消
		select {
		case <-ctx.Done():
			log.C(ctx).Warnw("Context cancelled during stream", "node_id", node.ID)
			if fullOutput.Len() > 0 {
				// 如果已累积输出，尝试提取思考内容
				thinking := extractThinkingContent(fullOutput.String())
				actual := removeThinkingContent(fullOutput.String())
				return actual, thinking, nil
			}
			return "", "", fmt.Errorf("stream cancelled: %w", ctx.Err())
		default:
		}

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
								// 累积完整输出
								fullOutput.WriteString(content)

								// 调用handler发送chunk
								if err := handler(content); err != nil {
									log.C(ctx).Warnw("Stream handler error, but continuing to read full response", "error", err)
								}
							}
						}
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.C(ctx).Errorw("Scanner error during stream", "error", err)
		if fullOutput.Len() > 0 {
			thinking := extractThinkingContent(fullOutput.String())
			actual := removeThinkingContent(fullOutput.String())
			return actual, thinking, nil
		}
		return "", "", fmt.Errorf("scanner error: %w", err)
	}

	output := fullOutput.String()
	if output == "" {
		return "", "", fmt.Errorf("LLM returned empty response")
	}

	// 从完整输出中提取思考内容和实际内容
	thinking := extractThinkingContent(output)
	actual := removeThinkingContent(output)

	return actual, thinking, nil
}

// extractThinkingContent 从完整输出中提取思考内容
func extractThinkingContent(output string) string {
	// 查找思考部分的开始标记
	thinkingMarkers := []string{
		"已思考",
		"**已思考**",
		"思考",
		"**思考**",
	}

	var thinkingStart int = -1
	for _, marker := range thinkingMarkers {
		if idx := strings.Index(output, marker); idx != -1 {
			thinkingStart = idx
			break
		}
	}

	if thinkingStart == -1 {
		return ""
	}

	// 查找思考部分的结束标记
	endMarkers := []string{
		"请开始",
		"开始创作",
		"创作要求",
		"\n\n[完整",
	}

	var thinkingEnd int = len(output)
	for _, marker := range endMarkers {
		if idx := strings.Index(output[thinkingStart:], marker); idx != -1 {
			thinkingEnd = thinkingStart + idx
			break
		}
	}

	if thinkingEnd > thinkingStart {
		return output[thinkingStart:thinkingEnd]
	}

	return ""
}

// removeThinkingContent 从完整输出中移除思考内容，返回实际内容
func removeThinkingContent(output string) string {
	thinking := extractThinkingContent(output)
	if thinking == "" {
		return output
	}

	// 移除思考部分
	result := strings.Replace(output, thinking, "", 1)
	return strings.TrimSpace(result)
}

// CreateFinalNote 创建最终笔记（公开方法）
func (e *SopExecutor) CreateFinalNote(ctx context.Context, run *model.SopRun, content string) (*model.SopNote, error) {
	// 获取模板信息
	template, err := e.ds.Sop().GetTemplate(run.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	note := &model.SopNote{
		Content:    content,
		Title:      fmt.Sprintf("SOP执行结果 - %s", template.Name),
		UserID:     run.UserID,
		TemplateID: run.TemplateID,
		RunID:      run.ID,
	}

	if err := e.ds.Sop().CreateNote(note); err != nil {
		return nil, fmt.Errorf("failed to create note: %w", err)
	}

	return note, nil
}

// failRun 标记Run为失败
func (e *SopExecutor) failRun(ctx context.Context, runID uint, errorMsg string) {
	finishTime := time.Now()
	if err := e.ds.Sop().UpdateRun(runID, map[string]interface{}{
		"status":        model.SopStatusFailed,
		"error_message": errorMsg,
		"finished_at":   finishTime,
	}); err != nil {
		log.C(ctx).Errorw("Failed to update run status to failed", "run_id", runID, "error", err)
	}
}
