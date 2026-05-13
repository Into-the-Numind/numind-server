// Package admin_ai — Task Profile handlers (GET/PUT /v1/admin/ai/tasks/*).
// These endpoints let admins inspect and update the 14 fixed task profiles,
// including their capability requirements and service bindings.
package admin_ai

import (
	"errors"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// TaskProfileController handles admin CRUD for task profiles.
type TaskProfileController struct {
	biz aiservice_admin.IAIServiceAdminBiz
}

// NewTaskProfileController creates a new TaskProfileController.
func NewTaskProfileController(biz aiservice_admin.IAIServiceAdminBiz) *TaskProfileController {
	return &TaskProfileController{biz: biz}
}

// ----------------------------------------------------------------------------
// GET /v1/admin/ai/tasks
// ----------------------------------------------------------------------------

// ListTasks returns all task profiles (no pagination — fixed ~14 rows).
func (ctrl *TaskProfileController) ListTasks(c *gin.Context) {
	log.C(c).Infow("Admin list task profiles called")

	tasks, err := ctrl.biz.ListTasks(c.Request.Context())
	if err != nil {
		log.C(c).Errorw("Failed to list task profiles", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": tasks, "total": len(tasks)})
}

// ----------------------------------------------------------------------------
// GET /v1/admin/ai/tasks/:id
// ----------------------------------------------------------------------------

// GetTask returns a single task profile with its bound services resolved.
// ":id" is the string task_id (e.g. "sop.text"), NOT a numeric primary key.
func (ctrl *TaskProfileController) GetTask(c *gin.Context) {
	taskID := c.Param("id")
	log.C(c).Infow("Admin get task profile called", "task_id", taskID)

	detail, err := ctrl.biz.GetTask(c.Request.Context(), taskID)
	if err != nil {
		if errors.Is(err, errno.ErrAITaskNotFound) {
			core.WriteResponse(c, errno.ErrAITaskNotFound, nil)
			return
		}
		log.C(c).Errorw("Failed to get task profile", "task_id", taskID, "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, detail)
}

// ----------------------------------------------------------------------------
// PUT /v1/admin/ai/tasks/:id
// ----------------------------------------------------------------------------

// updateTaskReq is the request body for UpdateTask.
type updateTaskReq struct {
	Requirements       model.JSONMap `json:"requirements"`
	DefaultServiceID   *uint64       `json:"default_service_id"`
	FallbackServiceIDs []uint64      `json:"fallback_service_ids"`
	AllowedServiceIDs  []uint64      `json:"allowed_service_ids"`
	Reason             string        `json:"reason"`
}

// UpdateTask updates a task profile's requirements and/or service bindings.
// Query param: ?force=true bypasses capability mismatch and records a capability.override audit entry.
// On incompatibility without force, returns HTTP 422 with the incompatible_bindings list.
func (ctrl *TaskProfileController) UpdateTask(c *gin.Context) {
	taskID := c.Param("id")
	force := c.Query("force") == "true"
	log.C(c).Infow("Admin update task profile called", "task_id", taskID, "force", force)

	var req updateTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	bizReq := aiservice_admin.UpdateTaskRequest{
		Requirements:       req.Requirements,
		DefaultServiceID:   req.DefaultServiceID,
		FallbackServiceIDs: req.FallbackServiceIDs,
		AllowedServiceIDs:  req.AllowedServiceIDs,
		Reason:             req.Reason,
	}

	actorID, actorName := actorFromContext(c)
	result, err := ctrl.biz.UpdateTask(c.Request.Context(), taskID, bizReq, force, actorID, actorName)
	if err != nil {
		if errors.Is(err, errno.ErrAITaskNotFound) {
			core.WriteResponse(c, errno.ErrAITaskNotFound, nil)
			return
		}
		if errors.Is(err, errno.ErrAICapabilityOverrideRequiresReason) {
			core.WriteResponse(c, errno.ErrAICapabilityOverrideRequiresReason, nil)
			return
		}
		if isErrno(err) {
			core.WriteResponse(c, err, nil)
			return
		}
		log.C(c).Errorw("Failed to update task profile", "task_id", taskID, "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新失败，请稍后重试"), nil)
		return
	}

	// Capability mismatch without force: return 422 with details.
	if !result.Compatible {
		core.WriteResponse(c, errno.ErrAICapabilityMismatch.SetMessage("服务能力不满足任务需求，可使用 ?force=true 强制保存"), gin.H{
			"incompatible_bindings": result.IncompatibleBindings,
		})
		return
	}

	core.WriteResponse(c, nil, result)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/ai/services/:id/validate-against/:task_id
// ----------------------------------------------------------------------------

// ValidateAgainst checks whether a service satisfies a task's requirements without
// making any changes. Useful for pre-flight checks in the admin UI.
func (ctrl *TaskProfileController) ValidateAgainst(c *gin.Context) {
	serviceID, parseErr := parseID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}
	taskID := c.Param("task_id")
	log.C(c).Infow("Admin validate service against task", "service_id", serviceID, "task_id", taskID)

	result, err := ctrl.biz.ValidateServiceAgainstTask(c.Request.Context(), serviceID, taskID)
	if err != nil {
		if errors.Is(err, errno.ErrAITaskNotFound) {
			core.WriteResponse(c, errno.ErrAITaskNotFound, nil)
			return
		}
		if errors.Is(err, errno.ErrAIServiceNotFound) {
			core.WriteResponse(c, errno.ErrAIServiceNotFound, nil)
			return
		}
		log.C(c).Errorw("Failed to validate service against task", "service_id", serviceID, "task_id", taskID, "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("验证失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, result)
}
