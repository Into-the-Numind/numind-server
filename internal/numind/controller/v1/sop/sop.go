package sop

import (
	"strconv"

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
