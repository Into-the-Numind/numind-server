package sop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// Execute 执行SOP流程
func (e *SopExecutor) Execute(ctx context.Context, run *model.SopRun, nodes []model.SopNode, initialInput string) error {
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

	currentInput := initialInput

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
	note, err := e.createFinalNote(ctx, run, finalOutput)
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

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", node.BaseURL, bytes.NewBuffer(reqData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// 设置API密钥
	if node.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+node.APIKey)
	}

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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var llmResp LLMResponse
	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(llmResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	output := llmResp.Choices[0].Message.Content
	return output, nil
}

// createFinalNote 创建最终笔记
func (e *SopExecutor) createFinalNote(ctx context.Context, run *model.SopRun, content string) (*model.SopNote, error) {
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
