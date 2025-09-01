package book

import (
	"net/http"
	"strconv"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type BookController struct {
	b biz.IBiz
}

func New(b biz.IBiz) *BookController {
	return &BookController{b: b}
}

// ViewBookHTML 查看书籍HTML页面
func (ctrl *BookController) ViewBookHTML(c *gin.Context) {
	bookIDStr := c.Param("id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid book ID"), nil)
		return
	}

	// 获取书籍信息
	book, err := ctrl.b.Books().GetByID(c, uint(bookID))
	if err != nil {
		log.C(c).Errorw("Failed to get book", "error", err.Error(), "book_id", bookID)
		core.WriteResponse(c, err, nil)
		return
	}

	// 获取书籍的所有卡片
	_, cards, err := ctrl.b.Cards().ListByBook(c, uint(bookID), 0, 1000)
	if err != nil {
		log.C(c).Errorw("Failed to get book cards", "error", err.Error(), "book_id", bookID)
		core.WriteResponse(c, err, nil)
		return
	}

	// 创建HTML渲染器
	htmlRenderer := card.NewHTMLRenderer(pagination.GetDefaultConfig())

	// 渲染HTML
	htmlContent, err := htmlRenderer.RenderBookToHTML(book, cards)
	if err != nil {
		log.C(c).Errorw("Failed to render book HTML", "error", err.Error(), "book_id", bookID)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to render book HTML: "+err.Error()), nil)
		return
	}

	// 返回HTML内容
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, htmlContent)
}

// ViewBookImage 查看书籍图片（使用无头浏览器渲染）
func (ctrl *BookController) ViewBookImage(c *gin.Context) {
	bookIDStr := c.Param("id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid book ID"), nil)
		return
	}

	// 获取书籍信息
	book, err := ctrl.b.Books().GetByID(c, uint(bookID))
	if err != nil {
		log.C(c).Errorw("Failed to get book", "error", err.Error(), "book_id", bookID)
		core.WriteResponse(c, err, nil)
		return
	}

	// 获取书籍的所有卡片
	_, cards, err := ctrl.b.Cards().ListByBook(c, uint(bookID), 0, 1000)
	if err != nil {
		log.C(c).Errorw("Failed to get book cards", "error", err.Error(), "book_id", bookID)
		core.WriteResponse(c, err, nil)
		return
	}

	// 创建HTML渲染器
	htmlRenderer := card.NewHTMLRenderer(pagination.GetDefaultConfig())

	// 渲染HTML
	htmlContent, err := htmlRenderer.RenderBookToHTML(book, cards)
	if err != nil {
		log.C(c).Errorw("Failed to render book HTML", "error", err.Error(), "book_id", bookID)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to render book HTML: "+err.Error()), nil)
		return
	}

	// 使用无头浏览器渲染为图片
	headlessRenderer := card.NewSimpleHeadlessRenderer(pagination.GetDefaultConfig())

	// 创建一个临时的卡片来渲染整个书籍
	tempCard := &model.CardM{
		Model:         gorm.Model{ID: book.ID},
		ProcessedText: htmlContent, // 这里存储完整的HTML内容
		SortOrder:     1,
	}

	// 渲染为图片
	renderedCard, err := headlessRenderer.RenderCardToImage(tempCard)
	if err != nil {
		log.C(c).Errorw("Failed to render book image", "error", err.Error(), "book_id", bookID)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to render book image: "+err.Error()), nil)
		return
	}

	// 返回图片
	c.Header("Content-Type", "image/png")
	c.Data(http.StatusOK, "image/png", []byte(renderedCard.ImageURL))
}
