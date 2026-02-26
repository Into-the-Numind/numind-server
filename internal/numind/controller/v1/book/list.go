package book

import (
	"strconv"
	"strings"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/gin-gonic/gin"
)

type ListBookResponse struct {
	TotalCount int64          `json:"total_count"`
	Books      []*model.BookM `json:"books"`
}

// List 返回卡册列表
func (ctrl *BookController) List(c *gin.Context) {
	log.C(c).Infow("List book function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取分页参数
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "10")
	categoryIDStr := c.Query("category_id")

	// 获取字段过滤参数，用逗号分隔
	fieldsStr := c.Query("fields")
	var fields []string
	if fieldsStr != "" {
		fields = strings.Split(fieldsStr, ",")
		// 清理字段名，移除空格
		for i, field := range fields {
			fields[i] = strings.TrimSpace(field)
		}
	}

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

	var total int64
	var books []*model.BookM
	var listErr error

	// 如果指定了分类ID，按分类查询；否则按用户查询
	if categoryIDStr != "" {
		categoryID, parseErr := strconv.ParseUint(categoryIDStr, 10, 64)
		if parseErr != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter, nil)
			return
		}
		total, books, listErr = ctrl.b.Books().ListByCategory(c, uint(categoryID), offset, limit)
	} else {
		total, books, listErr = ctrl.b.Books().ListByUser(c, currentUser.ID, offset, limit)
	}

	if listErr != nil {
		core.WriteResponse(c, listErr, nil)
		return
	}

	// 统一展示规则：列表中的 image_url 也去掉 /opt 前缀
	for _, b := range books {
		if b != nil {
			b.ImageUrl = util.GetDisplayURL(b.ImageUrl)
		}
	}

	resp := &ListBookResponse{
		TotalCount: total,
		Books:      books,
	}

	// 如果指定了字段过滤，则过滤响应数据
	if len(fields) > 0 {
		filteredBooks := util.FilterSliceFields(books, fields)
		resp.Books = filteredBooks.([]*model.BookM)
	}

	// 使用标准响应，避免压缩导致的乱码问题
	core.WriteResponse(c, nil, resp)
}

// ListAll 返回所有卡册列表（用于后台管理等场景，返回所有字段）
func (ctrl *BookController) ListAll(c *gin.Context) {
	log.C(c).Infow("ListAll book function called")

	// 获取分页参数
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

	// 获取所有书籍（不限制用户）
	total, books, err := ctrl.b.Books().ListAll(c, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 统一展示规则：列表中的 image_url 也去掉 /opt 前缀
	for _, b := range books {
		if b != nil {
			b.ImageUrl = util.GetDisplayURL(b.ImageUrl)
		}
	}

	resp := &ListBookResponse{
		TotalCount: total,
		Books:      books,
	}

	// 使用标准响应，避免压缩导致的乱码问题
	core.WriteResponse(c, nil, resp)
}
