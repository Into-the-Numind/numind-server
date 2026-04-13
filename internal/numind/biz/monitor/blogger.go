package monitor

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// AddBlogger 添加监控博主
// xhs-service profile fetch will be added in Task 6 (crawler.go).
func (mb *MonitorBiz) AddBlogger(ctx context.Context, userID uint, xhsUserID string) (*model.MonitorBlogger, error) {
	blogger := &model.MonitorBlogger{
		UserID:    userID,
		XhsUserID: xhsUserID,
		IsActive:  true,
	}
	if err := mb.store.Monitor().CreateBlogger(ctx, blogger); err != nil {
		return nil, fmt.Errorf("AddBlogger: %w", err)
	}
	return blogger, nil
}

// GetBlogger 获取单个博主（含所有权校验）
func (mb *MonitorBiz) GetBlogger(ctx context.Context, userID, bloggerID uint) (*model.MonitorBlogger, error) {
	blogger, err := mb.store.Monitor().GetBlogger(ctx, bloggerID)
	if err != nil {
		return nil, fmt.Errorf("GetBlogger: %w", err)
	}
	if blogger.UserID != userID {
		return nil, errno.ErrForbidden
	}
	return blogger, nil
}

// ListBloggers 分页查询用户的监控博主列表
func (mb *MonitorBiz) ListBloggers(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBlogger, int64, error) {
	return mb.store.Monitor().ListBloggers(ctx, userID, offset, limit)
}

// UpdateBlogger 更新博主信息（含所有权校验）
func (mb *MonitorBiz) UpdateBlogger(ctx context.Context, userID, bloggerID uint, category *string, isActive *bool) error {
	blogger, err := mb.store.Monitor().GetBlogger(ctx, bloggerID)
	if err != nil {
		return fmt.Errorf("UpdateBlogger: %w", err)
	}
	if blogger.UserID != userID {
		return errno.ErrForbidden
	}
	if category != nil {
		blogger.Category = *category
	}
	if isActive != nil {
		blogger.IsActive = *isActive
		if *isActive {
			// Reset failure state when reactivating
			blogger.ConsecutiveFailures = 0
			blogger.CheckError = ""
		}
	}
	return mb.store.Monitor().UpdateBlogger(ctx, blogger)
}

// DeleteBlogger 删除博主（含所有权校验，软删除）
func (mb *MonitorBiz) DeleteBlogger(ctx context.Context, userID, bloggerID uint) error {
	blogger, err := mb.store.Monitor().GetBlogger(ctx, bloggerID)
	if err != nil {
		return fmt.Errorf("DeleteBlogger: %w", err)
	}
	if blogger.UserID != userID {
		return errno.ErrForbidden
	}
	return mb.store.Monitor().DeleteBlogger(ctx, bloggerID)
}
