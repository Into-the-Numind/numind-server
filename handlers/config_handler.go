package handlers

import (
	"net/http"

	"numind-server/services"

	"github.com/gin-gonic/gin"
)

type ConfigHandler struct {
	configService *services.ConfigService
}

func NewConfigHandler(configService *services.ConfigService) *ConfigHandler {
	return &ConfigHandler{
		configService: configService,
	}
}

// GetConfigs 获取所有配置
func (h *ConfigHandler) GetConfigs(c *gin.Context) {
	configs, err := h.configService.GetConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取配置成功",
		"data":    configs,
	})
}

// UpdateConfig 更新配置
func (h *ConfigHandler) UpdateConfig(c *gin.Context) {
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "配置键不能为空",
			"data":    nil,
		})
		return
	}

	var req services.ConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err := h.configService.UpdateConfig(key, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "更新配置成功",
		"data":    nil,
	})
}
