package sop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// SopController 用户端SOP控制器
type SopController struct {
	sopBiz sop.ISopBiz
}

// NewSopController 创建用户端SOP控制器
func NewSopController(sopBiz sop.ISopBiz) *SopController {
	return &SopController{
		sopBiz: sopBiz,
	}
}

// ExecuteTemplate 执行SOP模板（用户端）
func (ctrl *SopController) ExecuteTemplate(c *gin.Context) {
	log.C(c).Infow("User execute SOP template called")

	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	var req v1.ExecuteSopTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: "+err.Error()), nil)
		return
	}

	// 使用token中的用户ID
	run, err := ctrl.sopBiz.ExecuteTemplate(c, uint(templateID), user.ID, req.Text)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, run)
}

// GetRun 获取SOP执行记录（用户端，只能查看自己的）
func (ctrl *SopController) GetRun(c *gin.Context) {
	log.C(c).Infow("User get SOP run called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	run, err := ctrl.sopBiz.GetRun(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	// 验证是否是用户自己的记录
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	core.WriteResponse(c, nil, run)
}

// GetRunDetail 获取SOP执行详情（用户端）
func (ctrl *SopController) GetRunDetail(c *gin.Context) {
	log.C(c).Infow("User get SOP run detail called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	run, nodeRuns, err := ctrl.sopBiz.GetRunWithNodes(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	// 验证是否是用户自己的记录
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"run":       run,
		"node_runs": nodeRuns,
	})
}

// ListMyRuns 获取当前用户的SOP执行记录列表
func (ctrl *SopController) ListMyRuns(c *gin.Context) {
	log.C(c).Infow("User list my SOP runs called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	uid := user.ID
	runs, total, err := ctrl.sopBiz.ListRuns(c, offset, limit, &uid)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"runs":  runs,
	})
}

// ListMyNotes 获取当前用户的SOP笔记列表
func (ctrl *SopController) ListMyNotes(c *gin.Context) {
	log.C(c).Infow("User list my SOP notes called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	notes, total, err := ctrl.sopBiz.ListNotesByUser(c, user.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"notes": notes,
	})
}

// GetNote 获取SOP笔记详情（用户端）
func (ctrl *SopController) GetNote(c *gin.Context) {
	log.C(c).Infow("User get SOP note called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的笔记ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	note, err := ctrl.sopBiz.GetNote(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("笔记不存在"), nil)
		return
	}

	// 验证是否是用户自己的笔记
	if note.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此笔记"), nil)
		return
	}

	core.WriteResponse(c, nil, note)
}

// ListTemplates 获取可用的SOP模板列表（用户端，只显示active的）
func (ctrl *SopController) ListTemplates(c *gin.Context) {
	log.C(c).Infow("User list SOP templates called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	templates, _, err := ctrl.sopBiz.ListTemplates(c, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	// 只返回active状态的模板
	activeTemplates := []interface{}{}
	for _, t := range templates {
		if t.Status == "active" {
			activeTemplates = append(activeTemplates, t)
		}
	}

	core.WriteResponse(c, nil, gin.H{
		"total":     len(activeTemplates),
		"templates": activeTemplates,
	})
}

// CreateRun 创建SOP执行（不立即执行）
func (ctrl *SopController) CreateRun(c *gin.Context) {
	log.C(c).Infow("User create SOP run called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	var req v1.CreateSopRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: "+err.Error()), nil)
		return
	}

	run, err := ctrl.sopBiz.CreateRun(c, req.TemplateID, user.ID, req.Text)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, run)
}

// GetNextNode 获取下一个待执行节点
func (ctrl *SopController) GetNextNode(c *gin.Context) {
	log.C(c).Infow("User get next node called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	node, hasNext, err := ctrl.sopBiz.GetNextNode(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	if node == nil {
		core.WriteResponse(c, nil, gin.H{
			"node":     nil,
			"has_next": false,
			"message":  "所有节点已执行完成",
		})
		return
	}

	core.WriteResponse(c, nil, v1.NextNodeResponse{
		NodeID:   node.ID,
		NodeName: node.Name,
		Sort:     node.Sort,
		IsFirst:  len(run.ConversationID) > 0, // 简化判断，实际应该检查是否有已完成的节点
		HasNext:  hasNext,
	})
}

// ExecuteNodeStream 流式执行指定节点
func (ctrl *SopController) ExecuteNodeStream(c *gin.Context) {
	log.C(c).Infow("User execute SOP node stream called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	nodeID, err := strconv.ParseUint(c.Param("node_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的节点ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	var req v1.ExecuteSopNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// text是可选的，所以不强制要求
		req.Text = ""
	}

	// 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用nginx缓冲

	// 获取Flusher（用于实时刷新）
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Streaming not supported"), nil)
		return
	}

	// 创建带心跳的 context，用于定期发送心跳保持连接
	heartbeatCtx, heartbeatCancel := context.WithCancel(c.Request.Context())
	defer heartbeatCancel()

	// 启动心跳 goroutine，每 15 秒发送一次注释行（SSE 心跳），更频繁地保持连接活跃
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-heartbeatTicker.C:
				// 发送 SSE 注释行（以 : 开头）作为心跳
				// 检查连接是否仍然有效
				select {
				case <-c.Request.Context().Done():
					return
				default:
					// 发送心跳注释行
					if _, err := c.Writer.WriteString(": heartbeat\n\n"); err != nil {
						log.C(c).Warnw("Failed to send heartbeat", "error", err)
						return
					}
					flusher.Flush()
				}
			}
		}
	}()

	// 流式执行节点
	err = ctrl.sopBiz.ExecuteNodeStream(heartbeatCtx, uint(runID), uint(nodeID), req.Text, func(chunk string) error {
		// 检查客户端是否断开连接
		select {
		case <-c.Request.Context().Done():
			log.C(c).Infow("Client disconnected during stream")
			return c.Request.Context().Err()
		default:
		}

		// 发送SSE格式的数据（需要对JSON进行转义）
		chunkJSON, _ := json.Marshal(chunk)
		data := fmt.Sprintf("data: %s\n\n", string(chunkJSON))
		if _, err := c.Writer.WriteString(data); err != nil {
			log.C(c).Warnw("Failed to write chunk to client", "error", err)
			return err
		}

		// 立即刷新，确保数据实时发送
		flusher.Flush()
		return nil
	})

	if err != nil {
		// 检查是否是客户端断开连接
		if c.Request.Context().Err() != nil {
			log.C(c).Infow("Client disconnected during stream", "error", err)
			return // 客户端断开，不需要发送错误
		}

		// 发送错误事件
		errorMsg, _ := json.Marshal(err.Error())
		errorData := fmt.Sprintf("event: error\ndata: %s\n\n", string(errorMsg))
		c.Writer.WriteString(errorData)
		flusher.Flush()
		return
	}

	// 发送完成事件
	doneData := "event: done\ndata: {\"status\":\"completed\"}\n\n"
	c.Writer.WriteString(doneData)
	flusher.Flush()
}

// GetRunStatus 获取Run执行状态
func (ctrl *SopController) GetRunStatus(c *gin.Context) {
	log.C(c).Infow("User get SOP run status called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	status, err := ctrl.sopBiz.GetRunStatus(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	// 转换为API响应格式
	response := v1.RunStatusResponse{
		Status:          status.Status,
		CurrentNodeSort: status.CurrentNodeSort,
		TotalNodes:      status.TotalNodes,
		CompletedCount:  status.CompletedCount,
	}

	// 转换已完成节点
	completedNodes := make([]v1.CompletedNodeInfo, len(status.CompletedNodes))
	for i, node := range status.CompletedNodes {
		completedNodes[i] = v1.CompletedNodeInfo{
			NodeID:        node.NodeID,
			NodeName:      node.NodeName,
			Sort:          node.Sort,
			OutputPreview: node.OutputPreview,
		}
	}
	response.CompletedNodes = completedNodes

	// 转换下一个节点
	if status.NextNode != nil {
		response.NextNode = &v1.NextNodeInfo{
			NodeID:   status.NextNode.NodeID,
			NodeName: status.NextNode.NodeName,
			Sort:     status.NextNode.Sort,
			IsFirst:  status.NextNode.IsFirst,
			HasNext:  status.NextNode.HasNext,
		}
	}

	core.WriteResponse(c, nil, response)
}
