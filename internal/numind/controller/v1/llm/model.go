package llm

import (
	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// LLMController 用户端 LLM 模型与偏好控制器
type LLMController struct {
	router *llmrouter.Router
}

// NewLLMController 创建 LLMController 实例
func NewLLMController(router *llmrouter.Router) *LLMController {
	return &LLMController{router: router}
}

// ListModels 获取可用的 LLM 模型列表
// GET /v1/llm/models
func (ctrl *LLMController) ListModels(c *gin.Context) {
	models, defaultKey, err := ctrl.router.GetModels(c.Request.Context())
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"list":              models,
		"default_model_key": defaultKey,
	})
}

// GetPreference 获取当前用户所有功能的模型偏好
// GET /v1/llm/preference
func (ctrl *LLMController) GetPreference(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	prefs, err := ctrl.router.GetPreferences(c.Request.Context(), user.ID)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, prefs)
}

// savePreferenceRequest 保存用户偏好请求体
type savePreferenceRequest struct {
	Feature  string `json:"feature" binding:"required"`
	ModelKey string `json:"model_key" binding:"required"`
	Thinking bool   `json:"thinking"`
}

// SavePreference 保存当前用户的模型偏好
// PUT /v1/llm/preference
func (ctrl *LLMController) SavePreference(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req savePreferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	if err := ctrl.router.SavePreference(c.Request.Context(), user.ID, req.Feature, req.ModelKey, req.Thinking); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
