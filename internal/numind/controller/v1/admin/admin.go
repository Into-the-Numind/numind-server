package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"numind-server/internal/numind/biz/admin"
	"numind-server/internal/numind/store"
)

type AdminController struct {
	biz admin.IAdminBiz
}

func NewAdminController(biz admin.IAdminBiz) *AdminController {
	return &AdminController{biz: biz}
}

// GetArticles 获取文章列表（管理员）
func (c *AdminController) GetArticles(ctx *gin.Context) {
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))
	categoryIDStr := ctx.Query("category_id")
	keyword := ctx.Query("keyword")
	userIDStr := ctx.Query("user_id")

	var categoryID *uint
	if categoryIDStr != "" {
		if id, err := strconv.ParseUint(categoryIDStr, 10, 32); err == nil {
			catID := uint(id)
			categoryID = &catID
		}
	}

	var userID *uint
	if userIDStr != "" {
		if id, err := strconv.ParseUint(userIDStr, 10, 32); err == nil {
			uid := uint(id)
			userID = &uid
		}
	}

	req := &store.AdminArticleListRequest{
		Page:       page,
		Limit:      limit,
		CategoryID: categoryID,
		Keyword:    keyword,
		UserID:     userID,
	}

	articles, total, err := c.biz.GetArticles(ctx, req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	pages := int((total + int64(req.Limit) - 1) / int64(req.Limit))

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取文章列表成功",
		"data": gin.H{
			"items": articles,
			"total": total,
			"page":  req.Page,
			"limit": req.Limit,
			"pages": pages,
		},
	})
}

// GetArticle 获取单个文章（管理员）
func (c *AdminController) GetArticle(ctx *gin.Context) {
	articleIDStr := ctx.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	article, err := c.biz.GetArticle(ctx, uint(articleID))
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{
			"code":    1,
			"message": "文章不存在",
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取文章成功",
		"data":    article,
	})
}

// CreateArticle 创建文章（管理员）
func (c *AdminController) CreateArticle(ctx *gin.Context) {
	var req admin.AdminArticleCreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	article, err := c.biz.CreateArticle(ctx, &req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "创建文章成功",
		"data":    article,
	})
}

// UpdateArticle 更新文章（管理员）
func (c *AdminController) UpdateArticle(ctx *gin.Context) {
	articleIDStr := ctx.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	var req admin.AdminArticleUpdateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err = c.biz.UpdateArticle(ctx, uint(articleID), &req)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "更新文章成功",
		"data":    nil,
	})
}

// DeleteArticle 删除文章（管理员）
func (c *AdminController) DeleteArticle(ctx *gin.Context) {
	articleIDStr := ctx.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	err = c.biz.DeleteArticle(ctx, uint(articleID))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "删除文章成功",
		"data":    nil,
	})
}

// BulkDeleteArticles 批量删除文章
func (c *AdminController) BulkDeleteArticles(ctx *gin.Context) {
	var req struct {
		ArticleIDs []uint `json:"article_ids" binding:"required"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err := c.biz.BulkDeleteArticles(ctx, req.ArticleIDs)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "批量删除文章成功",
		"data":    nil,
	})
}

// GetUsers 获取用户列表
func (c *AdminController) GetUsers(ctx *gin.Context) {
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))

	users, total, err := c.biz.GetUsers(ctx, page, limit)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	pages := int((total + int64(limit) - 1) / int64(limit))

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取用户列表成功",
		"data": gin.H{
			"items": users,
			"total": total,
			"page":  page,
			"limit": limit,
			"pages": pages,
		},
	})
}

// UpdateUser 更新用户
func (c *AdminController) UpdateUser(ctx *gin.Context) {
	userIDStr := ctx.Param("id")
	userID, err := strconv.ParseUint(userIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "用户ID格式错误",
			"data":    nil,
		})
		return
	}

	var req admin.AdminUserUpdateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err = c.biz.UpdateUser(ctx, uint(userID), &req)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "更新用户成功",
		"data":    nil,
	})
}

// DeleteUser 删除用户
func (c *AdminController) DeleteUser(ctx *gin.Context) {
	userIDStr := ctx.Param("id")
	userID, err := strconv.ParseUint(userIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "用户ID格式错误",
			"data":    nil,
		})
		return
	}

	err = c.biz.DeleteUser(ctx, uint(userID))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "删除用户成功",
		"data":    nil,
	})
}

// GetCategories 获取分类列表
func (c *AdminController) GetCategories(ctx *gin.Context) {
	categories, err := c.biz.GetCategories(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取分类列表成功",
		"data":    categories,
	})
}

// CreateCategory 创建分类
func (c *AdminController) CreateCategory(ctx *gin.Context) {
	var req admin.AdminCategoryCreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	category, err := c.biz.CreateCategory(ctx, &req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "创建分类成功",
		"data":    category,
	})
}

// UpdateCategory 更新分类
func (c *AdminController) UpdateCategory(ctx *gin.Context) {
	categoryIDStr := ctx.Param("id")
	categoryID, err := strconv.ParseUint(categoryIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "分类ID格式错误",
			"data":    nil,
		})
		return
	}

	var req admin.AdminCategoryUpdateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err = c.biz.UpdateCategory(ctx, uint(categoryID), &req)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "更新分类成功",
		"data":    nil,
	})
}

// DeleteCategory 删除分类
func (c *AdminController) DeleteCategory(ctx *gin.Context) {
	categoryIDStr := ctx.Param("id")
	categoryID, err := strconv.ParseUint(categoryIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "分类ID格式错误",
			"data":    nil,
		})
		return
	}

	err = c.biz.DeleteCategory(ctx, uint(categoryID))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "删除分类成功",
		"data":    nil,
	})
}

// GetStats 获取统计信息
func (c *AdminController) GetStats(ctx *gin.Context) {
	stats, err := c.biz.GetStats(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取统计信息成功",
		"data":    stats,
	})
}
