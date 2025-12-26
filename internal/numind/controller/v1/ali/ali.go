package ali

import (
	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"

	"github.com/gin-gonic/gin"
)

// AliController 阿里云服务控制器
type AliController struct {
	aliBiz ali.AliBiz
}

// New 创建阿里云服务控制器实例
func New(aliBiz ali.AliBiz) *AliController {
	return &AliController{
		aliBiz: aliBiz,
	}
}

// GetFileUploadLease 获取文件上传租约
func (ctrl *AliController) GetFileUploadLease(c *gin.Context) {
	var r struct {
		FileName string `json:"file_name" binding:"required"`
	}

	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	url, headers, leaseId, err := ctrl.aliBiz.GetFileUploadLease(r.FileName)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"url":      url,
		"headers":  headers,
		"lease_id": leaseId,
	})
}

// AddFile 确认文件上传
func (ctrl *AliController) AddFile(c *gin.Context) {
	var r struct {
		LeaseId string `json:"lease_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	fileId, err := ctrl.aliBiz.AddFile(r.LeaseId)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"file_id": fileId,
	})
}

