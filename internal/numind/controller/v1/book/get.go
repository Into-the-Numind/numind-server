package book

import (
	"time"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Get 获取一本卡册的详细信息
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

	core.WriteResponse(c, nil, book)
}
