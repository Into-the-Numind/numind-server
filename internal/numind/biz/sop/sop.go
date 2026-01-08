package sop

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"

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
	CheckRunOwnership(ctx context.Context, runID, userID uint) (bool, error)
	ListRuns(ctx context.Context, offset, limit int, userID *uint) ([]model.SopRun, int64, error)
	GetRunWithNodes(ctx context.Context, runID uint) (*model.SopRun, []model.SopNodeRun, error)
	ListExecutedTemplatesByUser(ctx context.Context, userID uint) ([]store.ExecutedTemplateInfo, error)
	ListTemplateRunsWithDetails(ctx context.Context, userID, templateID uint, offset, limit int) ([]v1.TemplateRunHistoryResponse, int64, error)

	// Step-by-step execution operations
	CreateRun(ctx context.Context, templateID, userID uint, text string) (*model.SopRun, error)
	GetNextNode(ctx context.Context, runID uint) (*model.SopNode, bool, error)
	ExecuteNodeStream(ctx context.Context, runID, nodeID uint, text string, handler func(event string, chunk string) error) error
	GetRunStatus(ctx context.Context, runID uint) (*RunStatus, error)

	// Note operations
	GetNote(ctx context.Context, id uint) (*model.SopNote, error)
	ListNotesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.SopNote, int64, error)

	// Chat operations
	ChatAfterRunStream(ctx context.Context, runID uint, conversationID string, question string, userID uint, deepThinking bool, regenerateMsgID uint, handler func(event string, chunk string) error) error
	ListChatMessages(ctx context.Context, runID uint, userID uint) ([]model.SopChatMsg, error)

	// Admin operations
	CleanZombieRuns(ctx context.Context, timeout time.Duration) error
}

type sopBiz struct {
	ds       store.IStore
	executor *SopExecutor
	// runningRuns 用于存储正在执行的任务 ID，防止并发冲突（互斥锁）
	runningRuns sync.Map
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
	Input    string `json:"input"`  // 节点输入
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

	// 生成唯一的conversation_id（改用纳秒级时间戳，彻底解决碰撞问题）
	conversationID := fmt.Sprintf("sop_%d_%d_%d", templateID, userID, time.Now().UnixNano())

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

	// 生成唯一的conversation_id（改用纳秒级时间戳，彻底解决碰撞问题）
	conversationID := fmt.Sprintf("sop_%d_%d_%d", templateID, userID, time.Now().UnixNano())

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
	// 互斥锁检测：防止同一个 RunID 的任务在后台并发执行（解决“执行互斥锁”问题）
	if _, loaded := b.runningRuns.LoadOrStore(runID, struct{}{}); loaded {
		log.C(ctx).Warnw("Detected concurrent execution attempt", "run_id", runID)
		return fmt.Errorf("该任务正在处理中，请勿重复操作")
	}
	defer b.runningRuns.Delete(runID)

	// 使用合并查询一次性获取所有需要的数据（优化性能）
	execCtx, err := b.ds.Sop().GetExecutionContext(runID, nodeID)
	if err != nil {
		return err
	}

	run := execCtx.Run
	node := execCtx.Node
	template := execCtx.Template
	allNodes := execCtx.AllNodes
	allNodeRuns := execCtx.AllNodeRuns
	existingNodeRun := execCtx.ExistingNodeRun

	// 找到最大的sort值（最后一个节点）
	maxSort := -1
	for _, n := range allNodes {
		if n.Sort > maxSort {
			maxSort = n.Sort
		}
	}

	// 判断当前节点是否是最后一个节点
	isLastNode := node.Sort == maxSort

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
	if template != nil && template.Prompt != "" {
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "system",
			Content: template.Prompt,
		})
	}

	// 构建历史：严格过滤出当前节点之前的最新成功记录（解决“幻觉注入”问题）
	// 我们按 Sort 分组，每组只取最新的一条
	latestRunsMap := make(map[int]model.SopNodeRun)
	for _, nr := range allNodeRuns {
		if nr.Sort < node.Sort && nr.Status == model.SopStatusSucceeded && nr.Output != "" {
			existing, ok := latestRunsMap[nr.Sort]
			if !ok || nr.CreatedAt.After(existing.CreatedAt) {
				latestRunsMap[nr.Sort] = nr
			}
		}
	}

	relevantNodeRuns := []model.SopNodeRun{}
	for _, nr := range latestRunsMap {
		relevantNodeRuns = append(relevantNodeRuns, nr)
	}

	// 按Sort字段排序
	sort.Slice(relevantNodeRuns, func(i, j int) bool {
		return relevantNodeRuns[i].Sort < relevantNodeRuns[j].Sort
	})

	// 添加到对话历史
	for _, nodeRun := range relevantNodeRuns {
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "user",
			Content: nodeRun.Input,
		})
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "assistant",
			Content: nodeRun.Output,
		})
	}

	// 确定当前节点的输入
	var currentInput string
	if text != "" {
		// 如果用户提供了新text，使用新text
		currentInput = text
	} else {
		// 统一逻辑：使用上一个已完成节点的输出作为当前输入
		// AI 会通过已经构建好的 conversationHistory 看到之前所有步骤的完整记录
		if len(completedNodeRuns) > 0 {
			// 找到当前节点之前（Sort最大）且已成功的节点
			var lastNodeRun *model.SopNodeRun
			for i := range completedNodeRuns {
				nr := &completedNodeRuns[i]
				if nr.Sort < node.Sort && nr.Status == model.SopStatusSucceeded {
					if lastNodeRun == nil || nr.Sort > lastNodeRun.Sort {
						lastNodeRun = nr
					}
				}
			}

			if lastNodeRun != nil {
				currentInput = lastNodeRun.Output
			} else {
				// 检查是否真的是第一个节点
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
				return fmt.Errorf("未找到前序节点的有效输出")
			}
		} else {
			// 如果没有任何已完成节点，必须是第一个节点且必须有输入
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

	// 确全局执行记录（Run）的状态标记为正在执行（解决“状态僵死”问题）
	// 无论是第一个节点启动，还是中间节点重生成，都必须确保整体状态为 running
	if run.Status != model.SopStatusRunning {
		updateData := map[string]interface{}{
			"status":        model.SopStatusRunning,
			"error_message": "", // 重新执行时清空之前的全局错误
		}
		// 如果是第一次真正启动（没有开始时间），记录开始时间
		if run.StartedAt == nil {
			updateData["started_at"] = time.Now()
		}
		if err := b.ds.Sop().UpdateRun(runID, updateData); err != nil {
			return fmt.Errorf("failed to update run status to running: %w", err)
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

		// 联动清理（解决“时空推演矛盾”）：既然中间节点重做了，后续的所有步骤、笔记和对话都已经失效，必须清除以防冲突
		log.C(ctx).Infow("Regeneration triggered, cleaning up downstream records", "run_id", runID, "after_sort", node.Sort)

		// 1. 删除后续节点的执行记录
		if err := b.ds.Sop().DeleteNodeRunsAfterSort(runID, node.Sort); err != nil {
			log.C(ctx).Warnw("Failed to cleanup downstream node runs", "error", err)
		}
		// 2. 删除该任务关联的最终笔记
		if err := b.ds.Sop().DeleteNotesByRun(runID); err != nil {
			log.C(ctx).Warnw("Failed to cleanup run notes", "error", err)
		}
		// 3. 删除该任务关联的对话消息（历史已变，追问需重来）
		if err := b.ds.Sop().DeleteChatMessagesByRun(runID); err != nil {
			log.C(ctx).Warnw("Failed to cleanup run chat messages", "error", err)
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
		// 节点执行失败，但仍然尝试保存已生成的中间内容和 Token 消耗（Issue 3 & 4）
		updateData := map[string]interface{}{
			"status":        model.SopStatusFailed,
			"output":        output,   // 保存已生成的文字
			"thinking":      thinking, // 保存已生成的思考
			"error_message": err.Error(),
			"latency_ms":    latency,
			"finished_at":   nodeEndTime,
		}
		if usage != nil {
			updateData["prompt_tokens"] = usage.PromptTokens
			updateData["completion_tokens"] = usage.CompletionTokens
			updateData["total_tokens"] = usage.TotalTokens
			updateData["reasoning_tokens"] = usage.ReasoningTokens
			updateData["estimated_prompt_tokens"] = usage.EstimatedPromptTokens
		}
		b.ds.Sop().UpdateNodeRun(nodeRun.ID, updateData)
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
		updateData["estimated_prompt_tokens"] = usage.EstimatedPromptTokens
		log.C(ctx).Infow("Saving token usage to node run",
			"node_run_id", nodeRun.ID,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
			"reasoning_tokens", usage.ReasoningTokens,
			"estimated_prompt_tokens", usage.EstimatedPromptTokens)
	}

	if err := b.ds.Sop().UpdateNodeRun(nodeRun.ID, updateData); err != nil {
		return fmt.Errorf("failed to update node run: %w", err)
	}

	// 检查是否所有节点都执行完成（重新获取最新的 NodeRuns 状态）
	allNodeRunsForCheck, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err == nil {
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
				Input:    nodeRun.Input,  // 节点输入
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

// CheckRunOwnership 检查Run是否属于指定用户（轻量级权限验证）
func (b *sopBiz) CheckRunOwnership(ctx context.Context, runID, userID uint) (bool, error) {
	return b.ds.Sop().CheckRunOwnership(runID, userID)
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

func (b *sopBiz) ListTemplateRunsWithDetails(ctx context.Context, userID, templateID uint, offset, limit int) ([]v1.TemplateRunHistoryResponse, int64, error) {
	// 验证template是否存在
	_, err := b.ds.Sop().GetTemplate(templateID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, 0, fmt.Errorf("模板不存在")
		}
		return nil, 0, err
	}

	// 获取模板的所有节点，用于计算total_nodes
	nodes, err := b.ds.Sop().ListNodesByTemplate(templateID)
	if err != nil {
		return nil, 0, fmt.Errorf("获取模板节点失败: %w", err)
	}
	totalNodes := len(nodes)

	// 获取runs列表
	runs, total, err := b.ds.Sop().ListRunsByUserAndTemplate(userID, templateID, offset, limit)
	if err != nil {
		return nil, 0, err
	}

	if len(runs) == 0 {
		return []v1.TemplateRunHistoryResponse{}, total, nil
	}

	// 提取所有run_id
	runIDs := make([]uint, len(runs))
	for i, run := range runs {
		runIDs[i] = run.ID
	}

	// 批量获取node执行记录
	nodeRunsMap, err := b.ds.Sop().ListNodeRunsByRuns(runIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("获取节点执行记录失败: %w", err)
	}

	// 批量获取对话记录
	chatMsgsMap, err := b.ds.Sop().ListChatMessagesByRuns(runIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("获取对话记录失败: %w", err)
	}

	// 提取所有node_id用于查询文件
	nodeIDs := make([]uint, 0)
	for _, nodeRuns := range nodeRunsMap {
		for _, nodeRun := range nodeRuns {
			if nodeRun.Node != nil {
				nodeIDs = append(nodeIDs, nodeRun.Node.ID)
			}
		}
	}

	// 批量获取文件列表（按node_id分组）
	filesMap := make(map[uint][]model.SopFile)
	if len(nodeIDs) > 0 {
		filesMap, err = b.ds.Sop().ListFilesByNodeRuns(nodeIDs)
		if err != nil {
			// 文件获取失败不影响主流程，记录日志即可
			log.C(ctx).Warnw("获取文件列表失败", "error", err)
		}
	}

	// 组装结果
	result := make([]v1.TemplateRunHistoryResponse, len(runs))
	for i, run := range runs {
		nodeRuns := nodeRunsMap[run.ID]
		chatMsgs := chatMsgsMap[run.ID]

		// 计算已完成节点数
		completedCount := 0
		for _, nodeRun := range nodeRuns {
			if nodeRun.Status == model.SopStatusSucceeded {
				completedCount++
			}
		}

		// 转换nodeRuns
		nodeRunInfos := make([]v1.TemplateNodeRunInfo, len(nodeRuns))
		for j, nodeRun := range nodeRuns {
			nodeName := ""
			nodeID := uint(0)
			if nodeRun.Node != nil {
				nodeName = nodeRun.Node.Name
				nodeID = nodeRun.Node.ID
			}

			// 获取该节点关联的文件（需要同时匹配node_id和run_id）
			// 因为同一个template的不同run可能使用相同的node_id，所以必须按run_id过滤
			allFiles := filesMap[nodeID]
			files := make([]model.SopFile, 0)
			for _, file := range allFiles {
				// 只返回属于当前run的文件
				if file.RunID != nil && *file.RunID == run.ID {
					files = append(files, file)
				}
			}
			fileInfos := make([]v1.TemplateFileInfo, len(files))
			for k, file := range files {
				fileInfos[k] = v1.TemplateFileInfo{
					ID:       file.ID,
					FileName: file.FileName,
					FileURL:  file.FileURL,
					FileSize: file.FileSize,
					FileType: file.FileType,
				}
			}

			// 清理input和output，过滤PDF格式代码等无效内容
			cleanedInput := cleanPDFFormatCode(nodeRun.Input)
			cleanedOutput := cleanPDFFormatCode(nodeRun.Output)
			cleanedThinking := cleanPDFFormatCode(nodeRun.Thinking)

			// 生成输出预览（截取前200字符）
			outputPreview := ""
			if len(cleanedOutput) > 200 {
				outputPreview = cleanedOutput[:200] + "..."
			} else {
				outputPreview = cleanedOutput
			}

			// 格式化时间
			finishedAtStr := (*string)(nil)
			if nodeRun.FinishedAt != nil {
				finishedAtStr = new(string)
				*finishedAtStr = nodeRun.FinishedAt.Format(time.RFC3339)
			}

			nodeRunInfos[j] = v1.TemplateNodeRunInfo{
				ID:            nodeRun.ID,
				NodeID:        nodeID,
				NodeName:      nodeName,
				Sort:          nodeRun.Sort,
				Status:        nodeRun.Status,
				FinishedAt:    finishedAtStr,
				Input:         cleanedInput,
				Output:        cleanedOutput,
				Thinking:      cleanedThinking,
				OutputPreview: outputPreview,
				Files:         fileInfos,
			}
		}

		// 转换chatMessages
		chatMsgInfos := make([]v1.TemplateChatMessageInfo, len(chatMsgs))
		for j, msg := range chatMsgs {
			chatMsgInfos[j] = v1.TemplateChatMessageInfo{
				ID:                    msg.ID,
				Role:                  msg.Role,
				Content:               msg.Content,
				CreatedAt:             msg.CreatedAt.Format(time.RFC3339),
				PromptTokens:          msg.PromptTokens,
				CompletionTokens:      msg.CompletionTokens,
				TotalTokens:           msg.TotalTokens,
				ReasoningTokens:       msg.ReasoningTokens,
				EstimatedPromptTokens: msg.EstimatedPromptTokens,
			}
		}

		result[i] = v1.TemplateRunHistoryResponse{
			ID:             run.ID,
			TemplateID:     run.TemplateID,
			Status:         run.Status,
			CreatedAt:      run.CreatedAt.Format(time.RFC3339),
			UpdatedAt:      run.UpdatedAt.Format(time.RFC3339),
			CompletedCount: completedCount,
			TotalNodes:     totalNodes,
			NodeRuns:       nodeRunInfos,
			ChatMessages:   chatMsgInfos,
		}
	}

	return result, total, nil
}

// ChatAfterRunStream Run完成后的对话流式接口
func (b *sopBiz) ChatAfterRunStream(ctx context.Context, runID uint, conversationID string, question string, userID uint, deepThinking bool, regenerateMsgID uint, handler func(event string, chunk string) error) error {
	// 互斥锁检测（解决“执行互斥锁”问题）
	if _, loaded := b.runningRuns.LoadOrStore(runID, struct{}{}); loaded {
		log.C(ctx).Warnw("Detected concurrent chat attempt", "run_id", runID)
		return fmt.Errorf("该任务正在处理中，请勿重复操作")
	}
	defer b.runningRuns.Delete(runID)

	// 验证Run

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
	_, err = b.ds.Sop().GetTemplate(run.TemplateID)
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

	// 3. 获取已有的聊天消息（通过 /v1/sop/chat/stream API 产生的历史对话）
	// ListChatMessagesByRun 会获取同一个 run_id 下的所有聊天记录，按 seq 排序
	// 这些记录都在同一个 conversation_id 下，确保大模型有完整的对话记忆
	chatMessages, err := b.ds.Sop().ListChatMessagesByRun(runID)
	if err != nil {
		return fmt.Errorf("failed to list chat messages: %w", err)
	}

	// 处理重新生成逻辑
	if regenerateMsgID > 0 {
		log.C(ctx).Infow("Processing regeneration request", "regenerate_msg_id", regenerateMsgID)

		targetIndex := -1
		for i, msg := range chatMessages {
			if msg.ID == regenerateMsgID {
				targetIndex = i
				break
			}
		}

		if targetIndex == -1 {
			return fmt.Errorf("message to regenerate not found: %d", regenerateMsgID)
		}

		targetMsg := chatMessages[targetIndex]
		if targetMsg.Role != "assistant" {
			return fmt.Errorf("can only regenerate assistant message")
		}

		// 确定要排除的消息索引列表（目标消息 + 可能存在的前置用户消息）
		excludeIndices := make(map[int]bool)
		excludeIndices[targetIndex] = true

		if targetIndex > 0 {
			prevMsg := chatMessages[targetIndex-1]
			if prevMsg.Role == "user" {
				excludeIndices[targetIndex-1] = true
			}
		}

		// 删除旧记录并构建新切片（解决数据库记录重复问题）
		newChatMessages := make([]model.SopChatMsg, 0, len(chatMessages))
		for i, msg := range chatMessages {
			if excludeIndices[i] {
				// 物理删除旧记录，确保数据库只保留最新生成的对话
				if err := b.ds.Sop().DeleteChatMessage(msg.ID); err != nil {
					log.C(ctx).Warnw("Failed to delete chat message during regeneration", "msg_id", msg.ID, "error", err)
				}
			} else {
				newChatMessages = append(newChatMessages, msg)
			}
		}
		chatMessages = newChatMessages

		log.C(ctx).Infow("Regeneration process: old records deleted", "target_msg_id", targetMsg.ID)
	}

	// 构建对话历史：仅包含节点输入/输出 -> 已有聊天消息
	// 注意：不包含模板或节点的 Prompt，使 AI 回归纯净助手状态
	history := []LLMMessage{}
	// 1. 添加所有成功执行的节点输入/输出对（前序步骤的内容）
	for _, nr := range nodeRuns {
		if nr.Status != model.SopStatusSucceeded {
			continue
		}
		history = append(history, LLMMessage{Role: "user", Content: nr.Input})
		history = append(history, LLMMessage{Role: "assistant", Content: nr.Output})
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
	// 将当前问题加入 history
	history = append(history, LLMMessage{Role: "user", Content: question})

	// 调用模型流式生成回答
	// 传入空字符串作为 input，这样执行器不会拼装节点的 Prompt，AI 保持普通助手身份
	var answerBuf strings.Builder
	_, thinking, usage, err := b.executor.ExecuteNodeStreamWithThinking(
		ctx,
		&lastNode,
		"", // 传入空字符串，避免触发 node.Prompt 拼接
		history,
		func(event string, chunk string) error {
			if event == "message" {
				answerBuf.WriteString(chunk)
			}
			if event == "done" {
				return nil // 拦截 done 事件，稍后随着 ID 一起发送
			}
			return handler(event, chunk)
		},
		true,         // isLastNode：标记为最后一个节点（用于日志和统计，所有节点统一使用 prompt + input 格式）
		deepThinking, // deepThinking
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
		Thinking:       thinking, // 保存思考过程
		Seq:            maxSeq + 2,
	}

	// 保存 token 使用统计（如果存在）
	if usage != nil {
		assistantMsg.PromptTokens = usage.PromptTokens
		assistantMsg.CompletionTokens = usage.CompletionTokens
		assistantMsg.TotalTokens = usage.TotalTokens
		assistantMsg.ReasoningTokens = usage.ReasoningTokens
		assistantMsg.EstimatedPromptTokens = usage.EstimatedPromptTokens
		log.C(ctx).Infow("Saving token usage to chat message",
			"run_id", runID,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
			"reasoning_tokens", usage.ReasoningTokens)
	}

	if err := b.ds.Sop().CreateChatMessage(assistantMsg); err != nil {
		return fmt.Errorf("failed to save assistant message: %w", err)
	}

	// 发送包含 message_id 的完成事件
	donePayload := fmt.Sprintf(`{"status":"completed","message_id":%d}`, assistantMsg.ID)
	return handler("done", donePayload)
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

// CleanZombieRuns 清理僵尸任务（将长时间运行中的任务标记为失败）
func (b *sopBiz) CleanZombieRuns(ctx context.Context, timeout time.Duration) error {
	affected, err := b.ds.Sop().ResetZombieRuns(timeout)
	if err != nil {
		return fmt.Errorf("failed to reset zombie runs: %w", err)
	}
	if affected > 0 {
		log.C(ctx).Infow("Zombies cleaned successfully", "count", affected, "timeout", timeout)
	}
	return nil
}

// cleanPDFFormatCode 清理PDF格式代码等无效内容
// 如果检测到内容是PDF格式代码（如FilterFlateDecode、stream、endstream等），返回空字符串
func cleanPDFFormatCode(content string) string {
	if content == "" {
		return ""
	}

	// 检测PDF格式代码的关键词
	pdfKeywords := []string{
		"FilterFlateDecode",
		"stream",
		"endstream",
		"endobj",
		"obj",
		"Length",
		"PDF-",
		"BT",
		"ET",
		"Tj",
		"TJ",
	}

	// 检查内容中是否包含多个PDF格式关键词
	keywordCount := 0
	contentLower := strings.ToLower(content)
	for _, keyword := range pdfKeywords {
		if strings.Contains(contentLower, strings.ToLower(keyword)) {
			keywordCount++
		}
	}

	// 如果包含3个或以上的PDF关键词，且内容看起来像PDF格式代码，则认为是无效内容
	if keywordCount >= 3 {
		// 进一步检查：如果内容中PDF格式代码的比例很高，则认为是无效内容
		// 检查是否包含大量PDF对象标记（如数字+空格+数字+空格+obj）
		objPattern := `\d+\s+\d+\s+obj`
		if matched, _ := regexp.MatchString(objPattern, content); matched {
			// 检查内容长度，如果很长且包含大量PDF格式代码，则认为是PDF原始内容
			if len(content) > 500 && keywordCount >= 5 {
				log.C(context.Background()).Warnw("检测到PDF格式代码，已过滤", "content_length", len(content), "keyword_count", keywordCount)
				return "" // 返回空字符串，表示内容无效
			}
		}
	}

	// 如果内容看起来正常，返回原内容
	return content
}
