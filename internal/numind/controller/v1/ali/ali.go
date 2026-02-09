package ali

import (
	"encoding/base64"
	"fmt"
	"io"
	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// sanitizeFileName 清理文件名，防止路径遍历攻击
func sanitizeFileName(fileName string) string {
	// 移除路径分隔符
	fileName = strings.ReplaceAll(fileName, "/", "_")
	fileName = strings.ReplaceAll(fileName, "\\", "_")
	fileName = strings.ReplaceAll(fileName, "..", "_")

	// 移除控制字符
	var result strings.Builder
	for _, r := range fileName {
		if r >= 32 && r != 127 {
			result.WriteRune(r)
		}
	}
	fileName = result.String()

	// 限制文件名长度
	if len(fileName) > 255 {
		ext := filepath.Ext(fileName)
		baseName := fileName[:255-len(ext)]
		fileName = baseName + ext
	}

	return strings.TrimSpace(fileName)
}

// VisionAnalyze 视觉理解接口 (支持 Base64 上传)
func (ctrl *AliController) VisionAnalyze(c *gin.Context) {
	// 1. 获取用户ID
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 2. 获取 run_id 和 node_id（必须参数）
	runIDStr := c.PostForm("run_id")
	nodeIDStr := c.PostForm("node_id")

	if runIDStr == "" || nodeIDStr == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("run_id 和 node_id 为必填参数"), nil)
		return
	}

	runID, err := strconv.ParseUint(runIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的 run_id"), nil)
		return
	}

	nodeID, err := strconv.ParseUint(nodeIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的 node_id"), nil)
		return
	}

	// 3. 获取上传的文件 (multipart/form-data)
	file, err := c.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 4. 检查文件大小 (百炼限制 7MB)
	if file.Size > 7*1024*1024 {
		core.WriteResponse(c, errno.ErrInvalidParameter, "文件大小不能超过 7MB")
		return
	}

	// 5. 读取文件内容并转换为 Base64
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

	// 6. 保存图片到数据库（参考 uploadFileToCOS 的逻辑）
	var sopFile *model.SopFile
	if err := func() error {
		// 验证文件名
		fileName := sanitizeFileName(file.Filename)
		if fileName == "" {
			fileName = "image.jpg"
		}

		// 获取文件扩展名
		ext := strings.ToLower(filepath.Ext(fileName))
		if ext == "" {
			ext = ".jpg" // 默认扩展名
		}

		// 生成安全的文件名和对象键
		timestamp := time.Now().UnixNano()
		safeFileName := fmt.Sprintf("vision_%d_%d%s", user.ID, timestamp, ext)
		objectKey := fmt.Sprintf("vision/%d/%d/%s", user.ID, runID, safeFileName)

		// 上传到COS
		var cosURL string
		if util.IsCOSEnabled() {
			cosURL, err = util.UploadBytesToCOS(c.Request.Context(), objectKey,
				file.Header.Get("Content-Type"), content)
			if err != nil {
				log.C(c).Warnw("COS上传失败，继续处理", "error", err, "object_key", objectKey)
				cosURL = ""
			} else {
				log.C(c).Infow("图片上传到COS成功", "cos_url", cosURL, "object_key", objectKey)
			}
		}

		// 创建数据库记录
		runIDUint := uint(runID)
		nodeIDUint := uint(nodeID)
		sopFile = &model.SopFile{
			UserID:    user.ID,
			RunID:     &runIDUint,
			NodeID:    &nodeIDUint,
			FileName:  fileName,
			FileURL:   cosURL,
			FileType:  file.Header.Get("Content-Type"),
			FileSize:  file.Size,
			FileExt:   ext,
			Status:    "uploaded",
			ObjectKey: objectKey,
		}

		// 如果COS上传失败，记录错误但不阻止保存
		if cosURL == "" && util.IsCOSEnabled() {
			sopFile.Status = "uploaded_no_cos"
			sopFile.ErrorMsg = "COS上传失败，但文件已保存"
		}

		// 保存到数据库
		ds := store.S
		if err := ds.Sop().CreateFile(sopFile); err != nil {
			return fmt.Errorf("创建文件记录失败: %w", err)
		}

		log.C(c).Infow("图片保存成功",
			"file_id", sopFile.ID,
			"filename", fileName,
			"size", file.Size,
			"cos_url", cosURL,
			"run_id", runID,
			"node_id", nodeID)

		return nil
	}(); err != nil {
		log.C(c).Errorw("保存图片失败", "error", err)
		// 继续执行分析，不阻止分析流程
	}

	// 7. 获取 Prompt (可选)
	prompt := c.PostForm("prompt")

	// 8. 调用 Biz 层进行分析
	result, err := ctrl.aliBiz.QianwenVision(c.Request.Context(), fmt.Sprintf("data:image/jpeg;base64,%s", encoded), prompt, "qwen3-vl-flash")
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 9. 返回结果（包含文件ID）
	response := gin.H{
		"content": result,
	}
	if sopFile != nil {
		response["file_id"] = sopFile.ID
	}
	core.WriteResponse(c, nil, response)
}
