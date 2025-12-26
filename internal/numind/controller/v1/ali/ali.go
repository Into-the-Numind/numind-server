package ali

import (
	"encoding/base64"
	"io"
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

// VisionAnalyze 视觉理解接口 (支持 Base64 上传)
func (ctrl *AliController) VisionAnalyze(c *gin.Context) {
	// 1. 获取上传的文件 (multipart/form-data)
	file, err := c.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 2. 检查文件大小 (百炼限制 7MB)
	if file.Size > 7*1024*1024 {
		core.WriteResponse(c, errno.ErrInvalidParameter, "文件大小不能超过 7MB")
		return
	}

	// 3. 读取文件内容并转换为 Base64
	f, err := file.Open()
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	encoded := base64.StdEncoding.EncodeToString(content)

	// 4. 获取 Prompt (可选)
	prompt := c.PostForm("prompt")

	// 5. 调用 Biz 层进行分析
	result, err := ctrl.aliBiz.QianwenVision(c.Request.Context(), encoded, prompt)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"content": result,
	})
}
