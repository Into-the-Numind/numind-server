package book

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// Delete 删除单本卡册
func (ctrl *BookController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete book function called")

	idStr := c.Param("id")
	bookID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	if err := ctrl.b.Books().Delete(c, uint(bookID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// DeleteBatch 批量删除卡册
func (ctrl *BookController) DeleteBatch(c *gin.Context) {
	log.C(c).Infow("Batch delete books function called")

	idStrs := c.QueryArray("bookID")
	if len(idStrs) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("bookID is required"), nil)
		return
	}

	ids := make([]uint, 0, len(idStrs))
	for _, s := range idStrs {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("invalid bookID"), nil)
			return
		}
		ids = append(ids, uint(v))
	}

	if err := ctrl.b.Books().DeleteBatch(c, ids); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
