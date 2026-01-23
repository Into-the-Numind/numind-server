package salesrag

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"

	"encoding/json"
	"numind-server/internal/numind/biz/salesrag"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type SalesRAGController struct {
	b biz.IBiz
}

func NewSalesRAGController(b biz.IBiz) *SalesRAGController {
	return &SalesRAGController{b: b}
}

// Ingest 处理知识库文档上传
func (ctrl *SalesRAGController) Ingest(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	defer file.Close()

	// Parse additional fields
	description := c.DefaultPostForm("description", "")
	tagsStr := c.DefaultPostForm("tags", "")

	var tags []string
	if tagsStr != "" {
		if strings.HasPrefix(tagsStr, "[") {
			_ = json.Unmarshal([]byte(tagsStr), &tags)
		} else {
			tags = strings.Split(tagsStr, ",")
		}
	}

	opts := salesrag.IngestOptions{
		Description: description,
		Tags:        tags,
	}

	// 获取当前用户
	userID, _ := c.Get("userID")

	docID, err := ctrl.b.SalesRAG().Ingest(c, userID.(uint), header.Filename, file, opts)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]uint{"document_id": docID})
}

// Chat 基于知识库的销售智能体聊天
func (ctrl *SalesRAGController) Chat(c *gin.Context) {
	var r struct {
		Query       string            `json:"query" binding:"required"`
		SalesStage  domain.SalesStage `json:"sales_stage"`
		DocumentIDs []uint            `json:"document_ids"`
	}

	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	verdict, err := ctrl.b.SalesRAG().Retrieve(c, r.Query, r.SalesStage, r.DocumentIDs)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, verdict)
}

// ListDocuments 获取文档列表
func (ctrl *SalesRAGController) ListDocuments(c *gin.Context) {
	userID, _ := c.Get("userID")
	docs, err := ctrl.b.SalesRAG().ListDocuments(c, userID.(uint))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, docs)
}

// DeleteDocument 删除文档
func (ctrl *SalesRAGController) DeleteDocument(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	userID, _ := c.Get("userID")
	if err := ctrl.b.SalesRAG().DeleteDocument(c, userID.(uint), uint(id)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// UpdateDocument 更新文档
func (ctrl *SalesRAGController) UpdateDocument(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var req salesrag.UpdateDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	userID, _ := c.Get("userID")
	if err := ctrl.b.SalesRAG().UpdateDocument(c, userID.(uint), uint(id), req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}
