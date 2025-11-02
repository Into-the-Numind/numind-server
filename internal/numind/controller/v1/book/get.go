package book

import (
	"time"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Get 获取笔记的详细信息
func (ctrl *BookController) Get(c *gin.Context) {
	log.C(c).Infow("Get book function called")

	idStr := c.Param("id")
	bookID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	book, err := ctrl.b.Books().GetByID(c, uint(bookID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// Update the ViewTime field to current time when book is viewed
	now := time.Now()
	book.ViewTime = &now
	if err := ctrl.b.Books().Update(c, book); err != nil {
		log.C(c).Errorw("Failed to update book view time", "error", err)
		// Don't return error here as the main operation (getting book) succeeded
	}

	// 获取该笔记的所有图片
	_, images, err := ctrl.b.Images().ListByBook(c, uint(bookID), 0, 1000) // 获取所有图片
	if err != nil {
		log.C(c).Errorw("Failed to get book images", "error", err)
		// 不返回错误，因为主要操作（获取笔记）成功了
	}

	// 获取该笔记的所有卡片
	_, cards, err := ctrl.b.Cards().ListByBook(c, uint(bookID), 0, 1000) // 获取所有卡片
	if err != nil {
		log.C(c).Errorw("Failed to get book cards", "error", err)
		// 不返回错误，因为主要操作（获取笔记）成功了
	}

	// 创建BookResponse
	bookResponse := model.NewBookResponse(book)
	if len(images) > 0 {
		bookResponse.AddImages(images)
	}
	if len(cards) > 0 {
		bookResponse.AddCards(cards)
	}

	core.WriteResponse(c, nil, bookResponse)
}
