package sop

import (
	"context"
	"time"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// CleanupDraftRuns 清理超过指定时间的 draft 状态 run（兜底机制）
// 正常情况下，draft 记录会在用户离开页面时被前端立即删除
// 这个任务只处理前端删除失败的异常情况（如浏览器崩溃、网络断开等）
func (b *sopBiz) CleanupDraftRuns(ctx context.Context, timeout time.Duration) error {
	cutoffTime := time.Now().Add(-timeout)

	// 删除满足以下条件的 run：
	// 1. status = 'draft'
	// 2. created_at < cutoffTime (默认 8 小时)
	result := b.ds.DB().
		Where("status = ? AND created_at < ?", model.SopStatusDraft, cutoffTime).
		Delete(&model.SopRun{})

	if result.Error != nil {
		log.C(ctx).Errorw("Failed to cleanup draft runs", "error", result.Error)
		return result.Error
	}

	if result.RowsAffected > 0 {
		log.C(ctx).Infow("Cleaned up draft runs", "count", result.RowsAffected, "cutoff_time", cutoffTime)
	}

	return nil
}
