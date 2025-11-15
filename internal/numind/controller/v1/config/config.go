package config

import (
	"strconv"
	"strings"

	"numind-server/internal/numind/biz"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/util"

	"github.com/gin-gonic/gin"
)

type ConfigController struct {
	b biz.IBiz
}

func New(b biz.IBiz) *ConfigController {
	return &ConfigController{b: b}
}

// CreateConfigRequest 创建配置请求
type CreateConfigRequest struct {
	Key         string `json:"key" binding:"required"`
	Value       string `json:"value" binding:"required"`
	Description string `json:"description"`
}

// UpdateConfigRequest 更新配置请求
type UpdateConfigRequest struct {
	Value       string `json:"value" binding:"required"`
	Description string `json:"description"`
}

// Create 创建配置
func (ctrl *ConfigController) Create(c *gin.Context) {
	log.C(c).Infow("Create config function called")

	var req CreateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	config, err := ctrl.b.Configs().Create(c, req.Key, req.Value, req.Description)
	if err != nil {
		log.C(c).Errorw("Failed to create config", "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, config)
}

// Get 获取单个配置
func (ctrl *ConfigController) Get(c *gin.Context) {
	log.C(c).Infow("Get config function called")

	key := c.Param("key")
	if key == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("配置键不能为空"), nil)
		return
	}

	config, err := ctrl.b.Configs().GetByKey(c, key)
	if err != nil {
		log.C(c).Errorw("Failed to get config", "key", key, "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, config)
}

// List 获取所有配置（用于 numind，支持字段过滤和压缩）
func (ctrl *ConfigController) List(c *gin.Context) {
	// 获取字段过滤参数
	fieldsStr := c.Query("fields")
	var fields []string
	if fieldsStr != "" {
		fields = strings.Split(fieldsStr, ",")
		// 清理字段名，移除空格
		for i, field := range fields {
			fields[i] = strings.TrimSpace(field)
		}
	}

	configs, err := ctrl.b.Configs().GetAll(c)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 如果指定了字段过滤，则过滤响应数据
	var responseData interface{}
	if len(fields) > 0 {
		responseData = util.FilterSliceFields(configs, fields)
	} else {
		responseData = configs
	}

	// 使用压缩响应以减少带宽使用
	core.WriteCompressedResponse(c, nil, responseData)
}

// ListWithPagination 分页获取系统配置列表（用于后台管理系统，返回所有字段）
func (ctrl *ConfigController) ListWithPagination(c *gin.Context) {
	log.C(c).Infow("List system configs with pagination function called")

	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "10")

	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	total, configs, err := ctrl.b.Configs().List(c, offset, limit)
	if err != nil {
		log.C(c).Errorw("Failed to list configs", "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	// 使用与后台管理系统一致的响应格式
	c.JSON(200, gin.H{
		"code":    0,
		"message": "获取系统配置列表成功",
		"data": gin.H{
			"items":  configs,
			"total":  total,
			"offset": offset,
			"limit":  limit,
		},
	})
}

// Update 更新配置
func (ctrl *ConfigController) Update(c *gin.Context) {
	log.C(c).Infow("Update config function called")

	key := c.Param("key")
	if key == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("配置键不能为空"), nil)
		return
	}

	var req UpdateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	config, err := ctrl.b.Configs().Update(c, key, req.Value, req.Description)
	if err != nil {
		log.C(c).Errorw("Failed to update config", "key", key, "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, config)
}

// Delete 删除配置
func (ctrl *ConfigController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete config function called")

	key := c.Param("key")
	if key == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("配置键不能为空"), nil)
		return
	}

	err := ctrl.b.Configs().Delete(c, key)
	if err != nil {
		log.C(c).Errorw("Failed to delete config", "key", key, "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// InitDefault 初始化默认配置
func (ctrl *ConfigController) InitDefault(c *gin.Context) {
	log.C(c).Infow("Init default configs function called")

	err := ctrl.b.Configs().InitDefaultConfigs(c)
	if err != nil {
		log.C(c).Errorw("Failed to init default configs", "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"message": "默认配置初始化成功"})
}
