package book

import (
	"context"
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

	// 🔍 异步删除笔记向量（从向量数据库中移除）
	if ctrl.b.Rag() != nil {
		go func() {
			// 使用新的 context，避免原 context 被取消
			vectorCtx := context.Background()
			if err := ctrl.b.Rag().DeleteBookVector(vectorCtx, uint(bookID)); err != nil {
				log.C(vectorCtx).Errorw("异步删除笔记向量失败", "error", err, "book_id", bookID)
				// 向量删除失败不影响笔记删除，只记录错误
			} else {
				log.C(vectorCtx).Infow("✅ 笔记向量删除成功", "book_id", bookID)
			}
		}()
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

	// 🔍 异步批量删除笔记向量（从向量数据库中移除）
	if ctrl.b.Rag() != nil {
		go func() {
			// 使用新的 context，避免原 context 被取消
			vectorCtx := context.Background()
			for _, bookID := range ids {
				if err := ctrl.b.Rag().DeleteBookVector(vectorCtx, bookID); err != nil {
					log.C(vectorCtx).Errorw("异步删除笔记向量失败", "error", err, "book_id", bookID)
					// 向量删除失败不影响笔记删除，只记录错误
				} else {
					log.C(vectorCtx).Infow("✅ 笔记向量删除成功", "book_id", bookID)
				}
			}
		}()
	}

	core.WriteResponse(c, nil, nil)
}
