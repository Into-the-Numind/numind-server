package image

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// BatchCreate 批量创建图片记录
func (ctrl *ImageController) BatchCreate(c *gin.Context) {
	log.C(c).Infow("Batch create images function called")

	var req []*model.ImageM
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	for _, img := range req {
		if _, err := govalidator.ValidateStruct(img); err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
			return
		}
	}

	if err := ctrl.b.Images().BatchCreate(c, req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// BatchUpload 支持批量上传图片文件
func (ctrl *ImageController) BatchUpload(c *gin.Context) {
	log.C(c).Infow("Batch upload images function called")

	form, err := c.MultipartForm()
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid multipart form"), nil)
		return
	}

	files := form.File["files"]
	if len(files) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("No files uploaded"), nil)
		return
	}

	var urls []string
	for _, file := range files {
		filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), file.Filename)
		savePath := filepath.Join("uploads", filename)

		// 确保目录存在
		os.MkdirAll("uploads", os.ModePerm)

		if err := c.SaveUploadedFile(file, savePath); err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("Upload failed: "+err.Error()), nil)
			return
		}

		// 假设图片可通过 /static/ 访问
		url := "/static/" + filename
		urls = append(urls, url)
	}

	core.WriteResponse(c, nil, gin.H{"urls": urls})
}
