package handlers

import (
	"net/http"
	"strconv"

	"numind-server/internal/middleware"
	"numind-server/internal/services"

	"github.com/gin-gonic/gin"
)

type ArticleHandler struct {
	articleService *services.ArticleService
}

func NewArticleHandler(articleService *services.ArticleService) *ArticleHandler {
	return &ArticleHandler{
		articleService: articleService,
	}
}

// FetchArticle 获取文章内容
func (h *ArticleHandler) FetchArticle(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	var req services.ArticleFetchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	article, err := h.articleService.FetchArticle(user.ID, &req)
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
		"message": "获取文章成功",
		"data":    article,
	})
}

// GetArticles 获取文章列表
func (h *ArticleHandler) GetArticles(c *gin.Context) {
	// 解析查询参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	categoryIDStr := c.Query("category_id")
	keyword := c.Query("keyword")
	userIDStr := c.Query("user_id")

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

	req := &services.ArticleListRequest{
		Page:       page,
		Limit:      limit,
		CategoryID: categoryID,
		Keyword:    keyword,
		UserID:     userID,
	}

	result, err := h.articleService.GetArticles(req)
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
		"message": "获取文章列表成功",
		"data":    result,
	})
}

// GetArticle 获取单个文章
func (h *ArticleHandler) GetArticle(c *gin.Context) {
	articleIDStr := c.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	article, err := h.articleService.GetArticle(uint(articleID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    1,
			"message": "文章不存在",
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取文章成功",
		"data":    article,
	})
}

// UpdateArticleCategory 更新文章分类
func (h *ArticleHandler) UpdateArticleCategory(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	articleIDStr := c.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	var req struct {
		CategoryID *uint `json:"category_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err = h.articleService.UpdateArticleCategory(uint(articleID), user.ID, req.CategoryID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "更新文章分类成功",
		"data":    nil,
	})
}

// DeleteArticle 删除文章
func (h *ArticleHandler) DeleteArticle(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	articleIDStr := c.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	err = h.articleService.DeleteArticle(uint(articleID), user.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "删除文章成功",
		"data":    nil,
	})
}

// AddFavorite 添加收藏
func (h *ArticleHandler) AddFavorite(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	articleIDStr := c.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	err = h.articleService.AddFavorite(user.ID, uint(articleID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "添加收藏成功",
		"data":    nil,
	})
}

// RemoveFavorite 移除收藏
func (h *ArticleHandler) RemoveFavorite(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	articleIDStr := c.Param("id")
	articleID, err := strconv.ParseUint(articleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文章ID格式错误",
			"data":    nil,
		})
		return
	}

	err = h.articleService.RemoveFavorite(user.ID, uint(articleID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "移除收藏成功",
		"data":    nil,
	})
}

// GetFavorites 获取收藏列表
func (h *ArticleHandler) GetFavorites(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	result, err := h.articleService.GetFavorites(user.ID, page, limit)
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
		"message": "获取收藏列表成功",
		"data":    result,
	})
}

// ParaphraseText 文本释义
func (h *ArticleHandler) ParaphraseText(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	var req services.ParaphraseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	result, err := h.articleService.ParaphraseText(&req)
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
		"message": "文本释义成功",
		"data": gin.H{
			"paraphrased_text": result,
		},
	})
}
