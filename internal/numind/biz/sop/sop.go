package sop

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ISopBiz SOP业务逻辑接口
type ISopBiz interface {
	// Template operations
	CreateTemplate(ctx context.Context, name, description, prompt string) (*model.SopTemplate, error)
	GetTemplate(ctx context.Context, id uint) (*model.SopTemplate, error)
	ListTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error)
	UpdateTemplate(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteTemplate(ctx context.Context, id uint) error

	// Node operations
	CreateNode(ctx context.Context, node *model.SopNode) (*model.SopNode, error)
	GetNode(ctx context.Context, id uint) (*model.SopNode, error)
	ListNodesByTemplate(ctx context.Context, templateID uint) ([]model.SopNode, error)
	UpdateNode(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteNode(ctx context.Context, id uint) error

	// Execution operations
	ExecuteTemplate(ctx context.Context, templateID, userID uint, text string) (*model.SopRun, error)
	GetRun(ctx context.Context, id uint) (*model.SopRun, error)
	ListRuns(ctx context.Context, offset, limit int, userID *uint) ([]model.SopRun, int64, error)
	GetRunWithNodes(ctx context.Context, runID uint) (*model.SopRun, []model.SopNodeRun, error)
	ListExecutedTemplatesByUser(ctx context.Context, userID uint) ([]store.ExecutedTemplateInfo, error)

	// Step-by-step execution operations
	CreateRun(ctx context.Context, templateID, userID uint, text string) (*model.SopRun, error)
	GetNextNode(ctx context.Context, runID uint) (*model.SopNode, bool, error)
	ExecuteNodeStream(ctx context.Context, runID, nodeID uint, text string, handler func(event string, chunk string) error) error
	GetRunStatus(ctx context.Context, runID uint) (*RunStatus, error)

	// Note operations
	GetNote(ctx context.Context, id uint) (*model.SopNote, error)
	ListNotesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.SopNote, int64, error)

	// Chat operations
	ChatAfterRunStream(ctx context.Context, runID uint, conversationID string, question string, userID uint, handler func(event string, chunk string) error) error
	ListChatMessages(ctx context.Context, runID uint, userID uint) ([]model.SopChatMsg, error)
}

type sopBiz struct {
	ds       store.IStore
	executor *SopExecutor
}

// NewSopBiz 创建SOP业务逻辑实例
func NewSopBiz(ds store.IStore, executor *SopExecutor) ISopBiz {
	return &sopBiz{
		ds:       ds,
		executor: executor,
	}
}

// Template operations
func (b *sopBiz) CreateTemplate(ctx context.Context, name, description, prompt string) (*model.SopTemplate, error) {
	template := &model.SopTemplate{
		Name:        name,
		Description: description,
		Prompt:      prompt,
		Status:      model.SopNodeStatusActive,
	}

	if err := b.ds.Sop().CreateTemplate(template); err != nil {
		return nil, fmt.Errorf("failed to create template: %w", err)
	}

	return template, nil
}

func (b *sopBiz) GetTemplate(ctx context.Context, id uint) (*model.SopTemplate, error) {
	return b.ds.Sop().GetTemplate(id)
}

func (b *sopBiz) ListTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error) {
	return b.ds.Sop().ListTemplates(offset, limit)
}

func (b *sopBiz) UpdateTemplate(ctx context.Context, id uint, updates map[string]interface{}) error {
	return b.ds.Sop().UpdateTemplate(id, updates)
}

func (b *sopBiz) DeleteTemplate(ctx context.Context, id uint) error {
	return b.ds.Sop().DeleteTemplate(id)
}

// Node operations
func (b *sopBiz) CreateNode(ctx context.Context, node *model.SopNode) (*model.SopNode, error) {
	// 验证模板是否存在
	if _, err := b.ds.Sop().GetTemplate(node.TemplateID); err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("template not found")
		}
		return nil, err
	}

	// 如果没有父节点，设置为根节点
	if node.ParentID == nil {
		node.IsRoot = true
	}

	if err := b.ds.Sop().CreateNode(node); err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	return node, nil
}

func (b *sopBiz) GetNode(ctx context.Context, id uint) (*model.SopNode, error) {
	return b.ds.Sop().GetNode(id)
}

func (b *sopBiz) ListNodesByTemplate(ctx context.Context, templateID uint) ([]model.SopNode, error) {
	return b.ds.Sop().ListNodesByTemplate(templateID)
}

func (b *sopBiz) UpdateNode(ctx context.Context, id uint, updates map[string]interface{}) error {
	return b.ds.Sop().UpdateNode(id, updates)
}

func (b *sopBiz) DeleteNode(ctx context.Context, id uint) error {
	return b.ds.Sop().DeleteNode(id)
}

// RunStatus Run执行状态
type RunStatus struct {
	Status          string              `json:"status"`
	CurrentNodeSort int                 `json:"current_node_sort"`
	CompletedNodes  []CompletedNodeInfo `json:"completed_nodes"`
	NextNode        *NextNodeInfo       `json:"next_node,omitempty"`
	TotalNodes      int                 `json:"total_nodes"`
	CompletedCount  int                 `json:"completed_count"`
}

// CompletedNodeInfo 已完成节点信息
type CompletedNodeInfo struct {
	NodeID   uint   `json:"node_id"`
	NodeName string `json:"node_name"`
	Sort     int    `json:"sort"`
	Output   string `json:"output"` // 完整输出
	Thinking string `json:"thinking,omitempty"`
}

// NextNodeInfo 下一个节点信息
type NextNodeInfo struct {
	NodeID   uint   `json:"node_id"`
	NodeName string `json:"node_name"`
	Sort     int    `json:"sort"`
	IsFirst  bool   `json:"is_first"`
	HasNext  bool   `json:"has_next"`
}

// Execution operations
func (b *sopBiz) ExecuteTemplate(ctx context.Context, templateID, userID uint, text string) (*model.SopRun, error) {
	// 验证模板是否存在
	_, err := b.ds.Sop().GetTemplate(templateID)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	// 获取模板的所有节点
	nodes, err := b.ds.Sop().ListNodesByTemplate(templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template nodes: %w", err)
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("template has no nodes")
	}

	// 生成唯一的conversation_id
	conversationID := fmt.Sprintf("sop_%d_%d_%d", templateID, userID, time.Now().Unix())

	// 创建Run记录
	run := &model.SopRun{
		TemplateID:     templateID,
		UserID:         userID,
		Status:         model.SopStatusPending,
		ConversationID: conversationID,
	}

	if err := b.ds.Sop().CreateRun(run); err != nil {
		return nil, fmt.Errorf("failed to create run: %w", err)
	}

	// 异步执行SOP
	go func() {
		execCtx := context.Background()
		if err := b.executor.Execute(execCtx, run, nodes, text); err != nil {
			log.C(execCtx).Errorw("SOP execution failed", "run_id", run.ID, "error", err)
		}
	}()

	return run, nil
}

// CreateRun 创建Run（不立即执行）
func (b *sopBiz) CreateRun(ctx context.Context, templateID, userID uint, text string) (*model.SopRun, error) {
	// #region agent log
	func() {
		logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if logFile != nil {
			defer logFile.Close()
			logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"sop.go:210","message":"CreateRun biz entry","data":{"hypothesisId":"B","templateID":%d,"userID":%d},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), templateID, userID)
			logFile.WriteString(logEntry)
		}
	}()
	// #endregion
	// 验证模板是否存在
	_, err := b.ds.Sop().GetTemplate(templateID)
	// #region agent log
	func() {
		logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if logFile != nil {
			defer logFile.Close()
			hasErr := err != nil
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			}
			logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"sop.go:212","message":"GetTemplate result","data":{"hypothesisId":"E","error":%t,"errorMsg":%q},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), hasErr, errMsg)
			logFile.WriteString(logEntry)
		}
	}()
	// #endregion
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	// 生成唯一的conversation_id
	conversationID := fmt.Sprintf("sop_%d_%d_%d", templateID, userID, time.Now().Unix())

	// 创建Run记录（状态为pending）
	run := &model.SopRun{
		TemplateID:     templateID,
		UserID:         userID,
		Status:         model.SopStatusPending,
		ConversationID: conversationID,
	}

	// #region agent log
	func() {
		logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if logFile != nil {
			defer logFile.Close()
			logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"sop.go:228","message":"Before CreateRun store call","data":{"hypothesisId":"E","run":%q},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), conversationID)
			logFile.WriteString(logEntry)
		}
	}()
	// #endregion
	if err := b.ds.Sop().CreateRun(run); err != nil {
		// #region agent log
		func() {
			logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if logFile != nil {
				defer logFile.Close()
				logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"sop.go:229","message":"CreateRun store error","data":{"hypothesisId":"E","error":%q},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), err.Error())
				logFile.WriteString(logEntry)
			}
		}()
		// #endregion
		return nil, fmt.Errorf("failed to create run: %w", err)
	}

	// #region agent log
	func() {
		logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if logFile != nil {
			defer logFile.Close()
			logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"sop.go:232","message":"CreateRun biz success","data":{"hypothesisId":"E","runID":%d},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), run.ID)
			logFile.WriteString(logEntry)
		}
	}()
	// #endregion
	log.C(ctx).Infow("Created SOP run", "run_id", run.ID, "template_id", templateID, "user_id", userID)
	return run, nil
}

// GetNextNode 获取下一个待执行节点
func (b *sopBiz) GetNextNode(ctx context.Context, runID uint) (*model.SopNode, bool, error) {
	// 获取Run信息
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, false, fmt.Errorf("run not found: %w", err)
	}

	// 获取模板的所有节点（按sort排序）
	nodes, err := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get template nodes: %w", err)
	}

	if len(nodes) == 0 {
		return nil, false, fmt.Errorf("template has no nodes")
	}

	// 获取已执行的NodeRun（按sort排序）
	completedNodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get node runs: %w", err)
	}

	// 找出已完成的节点ID集合
	completedNodeIDs := make(map[uint]bool)
	for _, nodeRun := range completedNodeRuns {
		if nodeRun.Status == model.SopStatusSucceeded {
			completedNodeIDs[nodeRun.NodeID] = true
		}
	}

	// 找到第一个未执行的节点
	for _, node := range nodes {
		if !completedNodeIDs[node.ID] {
			// 检查是否还有下一个节点
			hasNext := false
			for i := 0; i < len(nodes); i++ {
				if nodes[i].Sort > node.Sort && !completedNodeIDs[nodes[i].ID] {
					hasNext = true
					break
				}
			}
			return &node, hasNext, nil
		}
	}

	// 所有节点都已执行完成
	return nil, false, nil
}

// ExecuteNodeStream 流式执行指定节点
func (b *sopBiz) ExecuteNodeStream(ctx context.Context, runID, nodeID uint, text string, handler func(event string, chunk string) error) error {
	// 获取Run信息
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return fmt.Errorf("run not found: %w", err)
	}

	// 获取节点信息
	node, err := b.ds.Sop().GetNode(nodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	// 验证节点属于该模板
	if node.TemplateID != run.TemplateID {
		return fmt.Errorf("node does not belong to this template")
	}

	// 获取模板的所有节点，用于判断当前节点是否是最后一个节点
	allNodes, err := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
	if err != nil {
		return fmt.Errorf("failed to get template nodes: %w", err)
	}

	// 找到最大的sort值（最后一个节点）
	maxSort := -1
	for _, n := range allNodes {
		if n.Sort > maxSort {
			maxSort = n.Sort
		}
	}

	// 判断当前节点是否是最后一个节点
	isLastNode := node.Sort == maxSort

	// 检查是否已存在该节点的执行记录（支持重复执行）
	existingNodeRun, err := b.ds.Sop().GetNodeRunByRunAndNode(runID, nodeID)
	if err != nil {
		return fmt.Errorf("failed to check existing node run: %w", err)
	}

	// 获取已执行的NodeRun（用于构建上下文）
	allNodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return fmt.Errorf("failed to get node runs: %w", err)
	}

	// 过滤出已完成的其他节点（排除当前要重新执行的节点）
	completedNodeRuns := []model.SopNodeRun{}
	for _, nodeRun := range allNodeRuns {
		if nodeRun.NodeID != nodeID && nodeRun.Status == model.SopStatusSucceeded {
			completedNodeRuns = append(completedNodeRuns, nodeRun)
		}
	}

	// 构建对话历史
	conversationHistory := []LLMMessage{}

	// 添加模板的预处理prompt
	template, err := b.ds.Sop().GetTemplate(run.TemplateID)
	if err == nil && template != nil && template.Prompt != "" {
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "system",
			Content: template.Prompt,
		})
	}

	// 添加所有已执行节点的输入输出（按sort排序，排除当前要重新执行的节点）
	// 按sort顺序添加对话历史
	for i := 0; i < 1000; i++ { // 假设最多1000个节点
		found := false
		for _, nodeRun := range completedNodeRuns {
			if nodeRun.Sort == i && nodeRun.Status == model.SopStatusSucceeded {
				conversationHistory = append(conversationHistory, LLMMessage{
					Role:    "user",
					Content: nodeRun.Input,
				})
				conversationHistory = append(conversationHistory, LLMMessage{
					Role:    "assistant",
					Content: nodeRun.Output,
				})
				found = true
				break
			}
		}
		if !found {
			break
		}
	}

	// 确定当前节点的输入
	var currentInput string
	if text != "" {
		// 如果用户提供了新text，使用新text
		currentInput = text
	} else if isLastNode {
		// 如果是最后一个节点，整合所有前面节点的输出
		// 按sort顺序收集前面所有节点的输出
		var previousOutputs []string
		for i := 0; i < node.Sort; i++ {
			// 找到sort为i的已执行节点
			for _, nodeRun := range completedNodeRuns {
				if nodeRun.Sort == i && nodeRun.Status == model.SopStatusSucceeded {
					previousOutputs = append(previousOutputs, nodeRun.Output)
					break
				}
			}
		}

		// 整合所有前面节点的输出
		if len(previousOutputs) > 0 {
			// 构建整合后的输入，替换prompt中的占位符
			integratedInput := ""
			for i, output := range previousOutputs {
				integratedInput += fmt.Sprintf("[STAGE_%d_CACHE] = %s\n\n", i+1, output)
			}
			currentInput = integratedInput
		} else {
			return fmt.Errorf("最后一个节点需要前面节点的输出，但未找到已完成的节点")
		}
	} else {
		// 其他节点使用上一个节点的输出
		if len(completedNodeRuns) > 0 {
			// 找到最后一个已完成的节点
			lastNodeRun := completedNodeRuns[0]
			for _, nodeRun := range completedNodeRuns {
				if nodeRun.Sort > lastNodeRun.Sort && nodeRun.Status == model.SopStatusSucceeded {
					lastNodeRun = nodeRun
				}
			}
			if lastNodeRun.Status == model.SopStatusSucceeded {
				currentInput = lastNodeRun.Output
			} else {
				return fmt.Errorf("no valid previous node output found")
			}
		} else {
			// 第一个节点，没有输入也没有上一个节点的输出
			// 检查是否是第一个节点（sort为0或最小）
			isFirstNode := true
			for _, n := range allNodes {
				if n.Sort < node.Sort {
					isFirstNode = false
					break
				}
			}
			if isFirstNode {
				return fmt.Errorf("第一个节点需要提供输入内容（text参数或上传文件）")
			}
			return fmt.Errorf("no input provided and no previous node output")
		}
	}

	// 更新Run状态为running（如果是第一个节点或重新执行）
	if len(completedNodeRuns) == 0 && existingNodeRun == nil {
		startTime := time.Now()
		if err := b.ds.Sop().UpdateRun(runID, map[string]interface{}{
			"status":     model.SopStatusRunning,
			"started_at": startTime,
		}); err != nil {
			return fmt.Errorf("failed to update run status: %w", err)
		}
	}

	var nodeRun *model.SopNodeRun
	var isUpdate bool

	if existingNodeRun != nil {
		// 如果已存在，更新现有记录（重新执行）
		log.C(ctx).Infow("Node run already exists, will update it for re-execution",
			"run_id", runID, "node_id", nodeID, "existing_node_run_id", existingNodeRun.ID)
		nodeRun = existingNodeRun
		isUpdate = true

		// 更新节点执行状态和时间（清空之前的输出、错误信息和 token 统计）
		updateData := map[string]interface{}{
			"status":            model.SopStatusRunning,
			"input":             currentInput,
			"started_at":        time.Now(),
			"output":            "",  // 清空之前的输出
			"thinking":          "",  // 清空之前的思考内容
			"error_message":     "",  // 清空之前的错误信息
			"finished_at":       nil, // 清空完成时间
			"latency_ms":        0,   // 重置延迟
			"prompt_tokens":     0,   // 重置 token 统计
			"completion_tokens": 0,   // 重置 token 统计
			"total_tokens":      0,   // 重置 token 统计
			"reasoning_tokens":  0,   // 重置 token 统计
		}

		if err := b.ds.Sop().UpdateNodeRun(nodeRun.ID, updateData); err != nil {
			return fmt.Errorf("failed to update node run: %w", err)
		}

		// 重新加载nodeRun以获取最新数据
		nodeRun, err = b.ds.Sop().GetNodeRun(nodeRun.ID)
		if err != nil {
			return fmt.Errorf("failed to reload node run: %w", err)
		}
	} else {
		// 如果不存在，创建新记录
		nodeRun = &model.SopNodeRun{
			RunID:          runID,
			NodeID:         nodeID,
			TemplateID:     run.TemplateID,
			UserID:         run.UserID,
			ParentNodeID:   node.ParentID,
			Status:         model.SopStatusRunning,
			Input:          currentInput,
			ConversationID: run.ConversationID,
			Sort:           node.Sort,
			StartedAt:      &[]time.Time{time.Now()}[0],
		}

		if err := b.ds.Sop().CreateNodeRun(nodeRun); err != nil {
			return fmt.Errorf("failed to create node run: %w", err)
		}
		isUpdate = false
	}

	log.C(ctx).Infow("Node run prepared for execution",
		"run_id", runID, "node_id", nodeID, "node_run_id", nodeRun.ID, "is_update", isUpdate)

	// 执行节点（流式），返回完整输出、思考内容和 token 使用统计
	startTime := time.Now()
	// 深度思考模式：开启 enable_thinking
	output, thinking, usage, err := b.executor.ExecuteNodeStreamWithThinking(ctx, node, currentInput, conversationHistory, func(event string, chunk string) error {
		// 直接透传事件给上层 handler
		return handler(event, chunk)
	}, isLastNode, true, run.ConversationID)
	nodeEndTime := time.Now()
	latency := nodeEndTime.Sub(startTime).Milliseconds()

	if err != nil {
		// 节点执行失败
		b.ds.Sop().UpdateNodeRun(nodeRun.ID, map[string]interface{}{
			"status":        model.SopStatusFailed,
			"error_message": err.Error(),
			"latency_ms":    latency,
			"finished_at":   nodeEndTime,
		})
		return fmt.Errorf("node execution failed: %w", err)
	}

	// 更新NodeRun为成功，同时保存思考内容和 token 使用统计
	updateData := map[string]interface{}{
		"status":      model.SopStatusSucceeded,
		"output":      output,
		"thinking":    thinking,
		"latency_ms":  latency,
		"finished_at": nodeEndTime,
	}
	
	// 保存 token 使用统计（如果存在）
	if usage != nil {
		updateData["prompt_tokens"] = usage.PromptTokens
		updateData["completion_tokens"] = usage.CompletionTokens
		updateData["total_tokens"] = usage.TotalTokens
		updateData["reasoning_tokens"] = usage.ReasoningTokens
		log.C(ctx).Infow("Saving token usage to node run",
			"node_run_id", nodeRun.ID,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
			"reasoning_tokens", usage.ReasoningTokens)
	}
	
	if err := b.ds.Sop().UpdateNodeRun(nodeRun.ID, updateData); err != nil {
		return fmt.Errorf("failed to update node run: %w", err)
	}

	// 检查是否所有节点都执行完成
	allNodeRunsForCheck, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err == nil {
		allNodes, _ := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
		// 统计每个节点的最新成功记录（用于处理重复执行的情况）
		nodeStatusMap := make(map[uint]bool) // nodeID -> has succeeded record
		for _, nr := range allNodeRunsForCheck {
			if nr.Status == model.SopStatusSucceeded {
				nodeStatusMap[nr.NodeID] = true
			}
		}
		completedCount := len(nodeStatusMap)
		if completedCount == len(allNodes) {
			// 所有节点执行完成，生成最终Note
			finalOutput := output
			note, err := b.executor.CreateFinalNote(ctx, run, finalOutput)
			if err == nil {
				finishTime := time.Now()
				b.ds.Sop().UpdateRun(runID, map[string]interface{}{
					"status":        model.SopStatusSucceeded,
					"final_note_id": note.ID,
					"finished_at":   finishTime,
				})
			}
		}
	}

	return nil
}

// GetRunStatus 获取Run执行状态
func (b *sopBiz) GetRunStatus(ctx context.Context, runID uint) (*RunStatus, error) {
	// 获取Run信息
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("run not found: %w", err)
	}

	// 获取模板的所有节点
	allNodes, err := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template nodes: %w", err)
	}

	// 获取已执行的NodeRun
	completedNodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node runs: %w", err)
	}

	// 构建已完成节点信息
	completedNodes := []CompletedNodeInfo{}
	completedNodeIDs := make(map[uint]bool)
	currentNodeSort := -1

	for _, nodeRun := range completedNodeRuns {
		if nodeRun.Status == model.SopStatusSucceeded {
			completedNodeIDs[nodeRun.NodeID] = true
			completedNodes = append(completedNodes, CompletedNodeInfo{
				NodeID:   nodeRun.NodeID,
				NodeName: nodeRun.Node.Name,
				Sort:     nodeRun.Sort,
				Output:   nodeRun.Output, // 返回完整输出
				Thinking: nodeRun.Thinking,
			})
			if nodeRun.Sort > currentNodeSort {
				currentNodeSort = nodeRun.Sort
			}
		}
	}

	// 找到下一个节点
	var nextNode *NextNodeInfo
	for _, node := range allNodes {
		if !completedNodeIDs[node.ID] {
			// 检查是否还有下一个节点
			hasNext := false
			for i := 0; i < len(allNodes); i++ {
				if allNodes[i].Sort > node.Sort && !completedNodeIDs[allNodes[i].ID] {
					hasNext = true
					break
				}
			}
			nextNode = &NextNodeInfo{
				NodeID:   node.ID,
				NodeName: node.Name,
				Sort:     node.Sort,
				IsFirst:  len(completedNodes) == 0,
				HasNext:  hasNext,
			}
			break
		}
	}

	return &RunStatus{
		Status:          run.Status,
		CurrentNodeSort: currentNodeSort,
		CompletedNodes:  completedNodes,
		NextNode:        nextNode,
		TotalNodes:      len(allNodes),
		CompletedCount:  len(completedNodes),
	}, nil
}

func (b *sopBiz) GetRun(ctx context.Context, id uint) (*model.SopRun, error) {
	return b.ds.Sop().GetRun(id)
}

func (b *sopBiz) ListRuns(ctx context.Context, offset, limit int, userID *uint) ([]model.SopRun, int64, error) {
	return b.ds.Sop().ListRuns(offset, limit, userID)
}

func (b *sopBiz) GetRunWithNodes(ctx context.Context, runID uint) (*model.SopRun, []model.SopNodeRun, error) {
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, nil, err
	}

	nodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return nil, nil, err
	}

	return run, nodeRuns, nil
}

// Note operations
func (b *sopBiz) GetNote(ctx context.Context, id uint) (*model.SopNote, error) {
	return b.ds.Sop().GetNote(id)
}

func (b *sopBiz) ListNotesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.SopNote, int64, error) {
	return b.ds.Sop().ListNotesByUser(userID, offset, limit)
}

func (b *sopBiz) ListExecutedTemplatesByUser(ctx context.Context, userID uint) ([]store.ExecutedTemplateInfo, error) {
	return b.ds.Sop().ListExecutedTemplatesByUser(userID)
}

// ChatAfterRunStream 基于已完成的Run继续对话（SSE）
func (b *sopBiz) ChatAfterRunStream(ctx context.Context, runID uint, conversationID string, question string, userID uint, handler func(event string, chunk string) error) error {
	// 校验 run
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return fmt.Errorf("run not found: %w", err)
	}
	if run.UserID != userID {
		return fmt.Errorf("no permission to access this run")
	}
	if conversationID != "" && run.ConversationID != conversationID {
		return fmt.Errorf("conversation_id mismatch with run")
	}
	// 使用 run 中的会话ID
	conversationID = run.ConversationID

	// 获取模板、节点、节点执行记录
	template, err := b.ds.Sop().GetTemplate(run.TemplateID)
	if err != nil {
		return fmt.Errorf("template not found: %w", err)
	}
	nodes, err := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("template has no nodes")
	}
	// 找到最后一个节点，作为对话模型配置载体
	lastNode := nodes[0]
	for _, n := range nodes {
		if n.Sort > lastNode.Sort {
			lastNode = n
		}
	}

	nodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return fmt.Errorf("failed to list node runs: %w", err)
	}

	// 构建对话历史：模板 prompt -> 节点输入/输出 -> 已有聊天消息
	// 注意：同一个 run_id 下的所有聊天记录都在同一个 conversation_id 下，会全部保留
	history := []LLMMessage{}
	// 1. 添加模板的 system prompt（如果有）
	if template != nil && template.Prompt != "" {
		history = append(history, LLMMessage{Role: "system", Content: template.Prompt})
	}
	// 2. 添加所有成功执行的节点输入/输出对（前四步的对话记录）
	for _, nr := range nodeRuns {
		if nr.Status != model.SopStatusSucceeded {
			continue
		}
		history = append(history, LLMMessage{Role: "user", Content: nr.Input})
		history = append(history, LLMMessage{Role: "assistant", Content: nr.Output})
	}
	// 3. 添加已有的聊天消息（通过 /v1/sop/chat/stream API 产生的历史对话）
	// ListChatMessagesByRun 会获取同一个 run_id 下的所有聊天记录，按 seq 排序
	// 这些记录都在同一个 conversation_id 下，确保大模型有完整的对话记忆
	chatMessages, err := b.ds.Sop().ListChatMessagesByRun(runID)
	if err != nil {
		return fmt.Errorf("failed to list chat messages: %w", err)
	}
	maxSeq := 0
	for _, msg := range chatMessages {
		if msg.Seq > maxSeq {
			maxSeq = msg.Seq
		}
		history = append(history, LLMMessage{Role: msg.Role, Content: msg.Content})
	}

	// 保存当前用户提问
	userMsg := &model.SopChatMsg{
		RunID:          runID,
		ConversationID: conversationID,
		UserID:         userID,
		Role:           "user",
		Content:        question,
		Seq:            maxSeq + 1,
	}
	if err := b.ds.Sop().CreateChatMessage(userMsg); err != nil {
		return fmt.Errorf("failed to save chat message: %w", err)
	}
	history = append(history, LLMMessage{Role: "user", Content: question})

	// 调用模型流式生成回答
	var answerBuf strings.Builder
	_, _, _, err = b.executor.ExecuteNodeStreamWithThinking(
		ctx,
		&lastNode,
		question,
		history,
		func(event string, chunk string) error {
			if event == "message" {
				answerBuf.WriteString(chunk)
			}
			return handler(event, chunk)
		},
		true, // isLastNode：使用纯输入，避免重复拼prompt
		true, // deepThinking
		conversationID,
	)
	if err != nil {
		return err
	}

	// 保存助手回复
	assistantMsg := &model.SopChatMsg{
		RunID:          runID,
		ConversationID: conversationID,
		UserID:         userID,
		Role:           "assistant",
		Content:        answerBuf.String(),
		Seq:            maxSeq + 2,
	}
	if err := b.ds.Sop().CreateChatMessage(assistantMsg); err != nil {
		return fmt.Errorf("failed to save assistant message: %w", err)
	}

	return nil
}

// ListChatMessages 获取指定run的聊天记录（需校验归属）
func (b *sopBiz) ListChatMessages(ctx context.Context, runID uint, userID uint) ([]model.SopChatMsg, error) {
	// 校验 run 归属
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("run not found: %w", err)
	}
	if run.UserID != userID {
		return nil, fmt.Errorf("no permission to access this run")
	}

	msgs, err := b.ds.Sop().ListChatMessagesByRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to list chat messages: %w", err)
	}
	return msgs, nil
}
