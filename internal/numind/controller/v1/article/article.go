package article

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"numind-server/internal/numind/biz/article"
	"numind-server/internal/numind/store"
)

type ArticleController struct {
	biz article.IArticleBiz
}

func NewArticleController(biz article.IArticleBiz) *ArticleController {
	return &ArticleController{biz: biz}
}

// FetchArticle 获取文章内容
func (c *ArticleController) FetchArticle(ctx *gin.Context) {
	user := ctx.MustGet("user").(map[string]interface{})
	userID := uint(user["id"].(float64))

	var req struct {
		URL string `json:"url" binding:"required"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	article, err := c.biz.FetchArticle(ctx, userID, req.URL)
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
		"message": "获取文章成功",
		"data":    article,
	})
}

// GetArticles 获取文章列表
func (c *ArticleController) GetArticles(ctx *gin.Context) {
	// 解析查询参数
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

	req := &store.ArticleListRequest{
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

// GetArticle 获取单个文章
func (c *ArticleController) GetArticle(ctx *gin.Context) {
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

// UpdateArticleCategory 更新文章分类
func (c *ArticleController) UpdateArticleCategory(ctx *gin.Context) {
	user := ctx.MustGet("user").(map[string]interface{})
	userID := uint(user["id"].(float64))

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

	var req struct {
		CategoryID *uint `json:"category_id"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err = c.biz.UpdateArticleCategory(ctx, uint(articleID), userID, req.CategoryID)
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
		"message": "更新文章分类成功",
		"data":    nil,
	})
}

// DeleteArticle 删除文章
func (c *ArticleController) DeleteArticle(ctx *gin.Context) {
	user := ctx.MustGet("user").(map[string]interface{})
	userID := uint(user["id"].(float64))

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

	err = c.biz.DeleteArticle(ctx, uint(articleID), userID)
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

// AddFavorite 添加收藏
func (c *ArticleController) AddFavorite(ctx *gin.Context) {
	user := ctx.MustGet("user").(map[string]interface{})
	userID := uint(user["id"].(float64))

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

	err = c.biz.AddFavorite(ctx, userID, uint(articleID))
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
		"message": "添加收藏成功",
		"data":    nil,
	})
}

// RemoveFavorite 移除收藏
func (c *ArticleController) RemoveFavorite(ctx *gin.Context) {
	user := ctx.MustGet("user").(map[string]interface{})
	userID := uint(user["id"].(float64))

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

	err = c.biz.RemoveFavorite(ctx, userID, uint(articleID))
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
		"message": "移除收藏成功",
		"data":    nil,
	})
}

// GetFavorites 获取收藏列表
func (c *ArticleController) GetFavorites(ctx *gin.Context) {
	user := ctx.MustGet("user").(map[string]interface{})
	userID := uint(user["id"].(float64))

	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))

	articles, total, err := c.biz.GetFavorites(ctx, userID, page, limit)
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
		"message": "获取收藏列表成功",
		"data": gin.H{
			"items": articles,
			"total": total,
			"page":  page,
			"limit": limit,
			"pages": pages,
		},
	})
}

// ParaphraseText 文本释义
func (c *ArticleController) ParaphraseText(ctx *gin.Context) {
	var req struct {
		Text string `json:"text" binding:"required"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	result, err := c.biz.ParaphraseText(ctx, req.Text)
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
		"message": "文本释义成功",
		"data": gin.H{
			"paraphrased_text": result,
		},
	})
}
