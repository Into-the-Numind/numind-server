package book

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz/markdown"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// CreateWithMarkdown 使用 Markdown 处理方式创建书籍
func (ctrl *BookController) CreateWithMarkdown(c *gin.Context) {
	log.C(c).Infow("Create book with Markdown function called")

	// 处理text和template_id参数
	var req CreateBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 从JWT token中获取用户ID
	userID, err := getUserIDFromToken(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("Failed to get user ID from token: "+err.Error()), nil)
		return
	}

	// 创建 Markdown 集成适配器
	markdownAdapter := markdown.NewMarkdownIntegrationAdapter(ctrl.b)

	// 异步创建book（使用 Markdown 处理）
	book, err := markdownAdapter.CreateBookAsync(c, userID, req.Text, req.TemplateID)
	if err != nil {
		log.C(c).Errorw("Failed to create book with Markdown", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to create book: "+err.Error()), nil)
		return
	}

	// 立即返回成功响应
	core.WriteResponse(c, nil, book)
}

// CreateEnhanced 增强版创建方法，支持选择处理方式
func (ctrl *BookController) CreateEnhanced(c *gin.Context) {
	log.C(c).Infow("Enhanced create book function called")

	// 处理text和template_id参数
	var req CreateBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 从JWT token中获取用户ID
	userID, err := getUserIDFromToken(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("Failed to get user ID from token: "+err.Error()), nil)
		return
	}

	// 检查是否启用 Markdown 处理模式
	useMarkdown := viper.GetBool("book.use_markdown_processing")
	if useMarkdown {
		log.C(c).Infow("Using Markdown processing mode")
		ctrl.createWithMarkdownProcessor(c, userID, req.Text, req.TemplateID)
	} else {
		log.C(c).Infow("Using legacy JSON processing mode")
		ctrl.createWithJSONProcessor(c, userID, req.Text, req.TemplateID)
	}
}

// ValidateMarkdown 验证 Markdown 内容格式（调试接口）
func (ctrl *BookController) ValidateMarkdown(c *gin.Context) {
	log.C(c).Infow("Validate Markdown function called")

	// 解析请求体
	var req struct {
		Markdown string `json:"markdown" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 创建 Markdown 适配器
	markdownAdapter := markdown.NewMarkdownIntegrationAdapter(ctrl.b)

	// 验证 Markdown 格式
	valid, errors := markdownAdapter.ValidateMarkdown(req.Markdown)

	response := map[string]interface{}{
		"valid":  valid,
		"errors": errors,
	}

	if valid {
		// 如果有效，尝试预览解析结果
		preview, err := markdownAdapter.PreviewMarkdown(c, req.Markdown)
		if err == nil {
			response["preview"] = map[string]interface{}{
				"title":          preview.Title,
				"cover_prompt":   preview.CoverPrompt,
				"content_blocks": len(preview.ContentBlocks),
				"block_types":    ctrl.getBlockTypes(preview.ContentBlocks),
			}
		}
	}

	core.WriteResponse(c, nil, response)
}

// PreviewMarkdownHTML 预览 Markdown 转换为 HTML（调试接口）
func (ctrl *BookController) PreviewMarkdownHTML(c *gin.Context) {
	log.C(c).Infow("Preview Markdown HTML function called")

	// 解析请求体
	var req struct {
		Markdown string `json:"markdown" binding:"required"`
		Title    string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 创建 Markdown 适配器
	markdownAdapter := markdown.NewMarkdownIntegrationAdapter(ctrl.b)

	// 转换为 HTML
	title := req.Title
	if title == "" {
		title = "预览"
	}

	html, err := markdownAdapter.ConvertMarkdownToHTML(req.Markdown, title)
	if err != nil {
		log.C(c).Errorw("Failed to convert Markdown to HTML", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to convert: "+err.Error()), nil)
		return
	}

	response := map[string]interface{}{
		"html":   html,
		"title":  title,
		"length": len(html),
	}

	core.WriteResponse(c, nil, response)
}

// TestAIMarkdownGeneration 测试 AI 生成 Markdown（调试接口）
func (ctrl *BookController) TestAIMarkdownGeneration(c *gin.Context) {
	log.C(c).Infow("Test AI Markdown generation function called")

	// 解析请求体
	var req struct {
		Text string `json:"text" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 创建 Markdown 适配器
	markdownAdapter := markdown.NewMarkdownIntegrationAdapter(ctrl.b)

	// 测试 AI 生成
	result, err := markdownAdapter.TestAIGeneration(c, req.Text)
	if err != nil {
		log.C(c).Errorw("Failed to generate Markdown", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to generate: "+err.Error()), nil)
		return
	}

	// 验证生成的 Markdown
	valid, errors := markdownAdapter.ValidateMarkdown(result)

	response := map[string]interface{}{
		"markdown":          result,
		"length":            len(result),
		"valid":             valid,
		"validation_errors": errors,
	}

	// 如果有效，添加预览信息
	if valid {
		if preview, err := markdownAdapter.PreviewMarkdown(c, result); err == nil {
			response["preview"] = map[string]interface{}{
				"title":          preview.Title,
				"cover_prompt":   preview.CoverPrompt,
				"content_blocks": len(preview.ContentBlocks),
				"block_types":    ctrl.getBlockTypes(preview.ContentBlocks),
			}
		}
	}

	core.WriteResponse(c, nil, response)
}

// GetMarkdownStats 获取 Markdown 处理统计信息
func (ctrl *BookController) GetMarkdownStats(c *gin.Context) {
	log.C(c).Infow("Get Markdown stats function called")

	// 获取书籍ID
	bookIDStr := c.Param("id")
	if bookIDStr == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Missing book ID"), nil)
		return
	}

	bookID, err := strconv.ParseUint(bookIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid book ID"), nil)
		return
	}

	// 创建 Markdown 适配器
	markdownAdapter := markdown.NewMarkdownIntegrationAdapter(ctrl.b)

	// 获取统计信息
	stats, err := markdownAdapter.GetStats(c, uint(bookID))
	if err != nil {
		log.C(c).Errorw("Failed to get Markdown stats", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to get stats: "+err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, stats)
}

// GetMarkdownConfigs 获取 Markdown 处理配置
func (ctrl *BookController) GetMarkdownConfigs(c *gin.Context) {
	log.C(c).Infow("Get Markdown configs function called")

	// 创建 Markdown 适配器
	markdownAdapter := markdown.NewMarkdownIntegrationAdapter(ctrl.b)

	// 获取所有配置
	configs := markdownAdapter.GetAllConfigs()

	response := map[string]interface{}{
		"configs":          configs,
		"markdown_enabled": viper.GetBool("book.use_markdown_processing"),
		"version":          "1.0.0",
	}

	core.WriteResponse(c, nil, response)
}

// 辅助方法

// getBlockTypes 获取内容块类型统计
func (ctrl *BookController) getBlockTypes(blocks []markdown.MarkdownContentBlock) map[string]int {
	types := make(map[string]int)
	for _, block := range blocks {
		types[block.Type]++
	}
	return types
}
