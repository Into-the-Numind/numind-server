package admin

import (
	"net/http"
	"strconv"
	"time"

	"numind-server/internal/numind/biz/admin"
	bookbiz "numind-server/internal/numind/biz/book"
	imagebiz "numind-server/internal/numind/biz/image"
	paymentbiz "numind-server/internal/numind/biz/payment"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

type AdminController struct {
	biz        admin.IAdminBiz
	paymentBiz paymentbiz.PaymentBiz
	bookBiz    bookbiz.BookBiz
	imageBiz   imagebiz.ImageBiz
	chatStore  store.ChatStore
}

func NewAdminController(biz admin.IAdminBiz, paymentBiz paymentbiz.PaymentBiz, bookBiz bookbiz.BookBiz, imageBiz imagebiz.ImageBiz, chatStore store.ChatStore) *AdminController {
	return &AdminController{
		biz:        biz,
		paymentBiz: paymentBiz,
		bookBiz:    bookBiz,
		imageBiz:   imageBiz,
		chatStore:  chatStore,
	}
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

// GetUsers 获取用户列表（使用 page/limit 参数）
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

// GetUserList 获取用户列表（后台管理系统专用，返回所有用户字段，使用 offset/limit 参数）
func (c *AdminController) GetUserList(ctx *gin.Context) {
	offset, _ := strconv.Atoi(ctx.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))

	users, total, err := c.biz.GetUserList(ctx, offset, limit)
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
		"message": "获取用户列表成功",
		"data": gin.H{
			"items":  users,
			"total":  total,
			"offset": offset,
			"limit":  limit,
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

// GetDashboardStats 获取仪表板统计信息
func (c *AdminController) GetDashboardStats(ctx *gin.Context) {
	stats, err := c.biz.GetDashboardStats(ctx)
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
		"message": "获取仪表板统计信息成功",
		"data":    stats,
	})
}

// GetUserGrowthTrend 获取用户增长趋势
// 支持三种时间范围：
//   - week: 本周（从本周一开始，按日统计，显示格式：1日、2日...）
//   - month: 本月（从本月1日开始，按日统计，显示格式：1日、2日...）
//   - year: 今年（从1月1日开始，按月统计，显示格式：1月、2月...）
//
// 参数：period (可选，默认 "month")
func (c *AdminController) GetUserGrowthTrend(ctx *gin.Context) {
	period := ctx.DefaultQuery("period", "month")

	trend, err := c.biz.GetUserGrowthTrend(ctx, period)
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
		"message": "获取用户增长趋势成功",
		"data":    trend,
	})
}

// GetBookGrowthTrend 获取笔记增长趋势
// 支持三种时间范围：
//   - week: 本周（从本周一开始，按日统计，显示格式：1日、2日...）
//   - month: 本月（从本月1日开始，按日统计，显示格式：1日、2日...）
//   - year: 今年（从1月1日开始，按月统计，显示格式：1月、2月...）
//
// 参数：period (可选，默认 "month")
func (c *AdminController) GetBookGrowthTrend(ctx *gin.Context) {
	period := ctx.DefaultQuery("period", "month")

	trend, err := c.biz.GetBookGrowthTrend(ctx, period)
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
		"message": "获取笔记增长趋势成功",
		"data":    trend,
	})
}

// GetPaymentList 获取支付记录列表（管理员）
func (c *AdminController) GetPaymentList(ctx *gin.Context) {
	offset, _ := strconv.Atoi(ctx.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))
	userIDStr := ctx.Query("user_id")
	statusStr := ctx.Query("status")
	channelStr := ctx.Query("channel")
	startDateStr := ctx.Query("start_date")
	endDateStr := ctx.Query("end_date")
	keyword := ctx.Query("keyword")

	req := &store.AdminPaymentListRequest{
		Offset:  offset,
		Limit:   limit,
		Keyword: keyword,
	}

	// 解析用户ID
	if userIDStr != "" {
		if id, err := strconv.ParseUint(userIDStr, 10, 32); err == nil {
			uid := uint(id)
			req.UserID = &uid
		}
	}

	// 解析状态
	if statusStr != "" {
		req.Status = &statusStr
	}

	// 解析渠道
	if channelStr != "" {
		req.Channel = &channelStr
	}

	// 解析开始日期
	if startDateStr != "" {
		if t, err := time.Parse("2006-01-02", startDateStr); err == nil {
			req.StartDate = &t
		}
	}

	// 解析结束日期
	if endDateStr != "" {
		if t, err := time.Parse("2006-01-02", endDateStr); err == nil {
			// 设置为当天的23:59:59
			t = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
			req.EndDate = &t
		}
	}

	payments, total, err := c.paymentBiz.ListPayments(ctx, req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": "获取支付记录列表失败: " + err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取支付记录列表成功",
		"data": gin.H{
			"items":  payments,
			"total":  total,
			"offset": offset,
			"limit":  limit,
		},
	})
}

// GetPayment 获取支付记录详情（管理员）
func (c *AdminController) GetPayment(ctx *gin.Context) {
	// 支持通过 out_trade_no 或 id 查询
	outTradeNo := ctx.Param("out_trade_no")
	if outTradeNo == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "订单号不能为空",
			"data":    nil,
		})
		return
	}

	// 先尝试通过 out_trade_no 查询
	payment, err := c.paymentBiz.GetPaymentByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		// 如果通过 out_trade_no 查询失败，尝试通过 ID 查询
		if id, parseErr := strconv.ParseUint(outTradeNo, 10, 32); parseErr == nil {
			payment, err = c.paymentBiz.GetPaymentByID(ctx, uint(id))
		}
	}

	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{
			"code":    1,
			"message": "支付记录不存在",
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取支付记录成功",
		"data":    payment,
	})
}

// GetBook 获取笔记详情（管理员，包含图片信息）
func (c *AdminController) GetBook(ctx *gin.Context) {
	bookIDStr := ctx.Param("id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "笔记ID格式错误",
			"data":    nil,
		})
		return
	}

	// 获取笔记基本信息
	book, err := c.bookBiz.GetByID(ctx, uint(bookID))
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{
			"code":    1,
			"message": "笔记不存在",
			"data":    nil,
		})
		return
	}

	// 获取该笔记的所有图片
	_, images, err := c.imageBiz.ListByBook(ctx, uint(bookID), 0, 1000) // 获取所有图片
	if err != nil {
		// 记录错误但不影响主要操作
		images = []*model.ImageM{}
	}

	// 创建BookResponse
	bookResponse := model.NewBookResponse(book)
	if len(images) > 0 {
		bookResponse.AddImages(images)
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取笔记详情成功",
		"data":    bookResponse,
	})
}

// GetImageList 获取图片列表（管理员）
func (c *AdminController) GetImageList(ctx *gin.Context) {
	offset, _ := strconv.Atoi(ctx.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))
	userIDStr := ctx.Query("user_id")
	bookIDStr := ctx.Query("book_id")
	statusStr := ctx.Query("status")
	keyword := ctx.Query("keyword")

	req := &store.AdminImageListRequest{
		Offset:  offset,
		Limit:   limit,
		Keyword: keyword,
	}

	// 解析用户ID
	if userIDStr != "" {
		if id, err := strconv.ParseUint(userIDStr, 10, 32); err == nil {
			uid := uint(id)
			req.UserID = &uid
		}
	}

	// 解析笔记ID
	if bookIDStr != "" {
		if id, err := strconv.ParseUint(bookIDStr, 10, 32); err == nil {
			bid := uint(id)
			req.BookID = &bid
		}
	}

	// 解析状态
	if statusStr != "" {
		req.Status = &statusStr
	}

	images, total, err := c.imageBiz.ListAll(ctx, req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": "获取图片列表失败: " + err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取图片列表成功",
		"data": gin.H{
			"items":  images,
			"total":  total,
			"offset": offset,
			"limit":  limit,
		},
	})
}

// GetBookSessions 获取笔记的会话列表（管理员）
// GET /v1/admin/books/:id/sessions
// 查询参数：
//   - offset: 偏移量，默认 0
//   - limit: 每页数量，默认 10
func (c *AdminController) GetBookSessions(ctx *gin.Context) {
	bookIDStr := ctx.Param("id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "笔记ID格式错误",
			"data":    nil,
		})
		return
	}

	offset, _ := strconv.Atoi(ctx.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))

	sessions, total, err := c.chatStore.ListSessionsByBookIDForAdmin(ctx, uint(bookID), offset, limit)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": "获取会话列表失败: " + err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取会话列表成功",
		"data": gin.H{
			"total":    total,
			"sessions": sessions,
			"offset":   offset,
			"limit":    limit,
		},
	})
}

// GetSessionMessages 根据会话ID获取聊天记录（管理员）
// GET /v1/admin/sessions/:session_id/messages
func (c *AdminController) GetSessionMessages(ctx *gin.Context) {
	sessionIDStr := ctx.Param("session_id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "会话ID格式错误",
			"data":    nil,
		})
		return
	}

	session, err := c.chatStore.GetSessionWithMessagesForAdmin(ctx, uint(sessionID))
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{
			"code":    1,
			"message": "会话不存在",
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取聊天记录成功",
		"data":    session,
	})
}
