package feedback

import (
	"context"
	"errors"

	"github.com/jinzhu/copier"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// FeedbackBiz 定义了 feedback 模块在 biz 层所实现的方法.
type FeedbackBiz interface {
	Create(ctx context.Context, userID uint, req *v1.CreateFeedbackRequest) error
	GetByID(ctx context.Context, feedbackID uint) (*v1.FeedbackResponse, error)
	GetByUserID(ctx context.Context, userID uint, offset, limit int) (*v1.ListFeedbackResponse, error)
	Delete(ctx context.Context, userID uint, feedbackID uint) error
	// 管理员方法
	ListAll(ctx context.Context, offset, limit int, userID *uint, status *int, feedbackType *string) (*v1.ListFeedbackResponse, error)
	Update(ctx context.Context, feedbackID uint, status *int, reply *string) error
	DeleteByAdmin(ctx context.Context, feedbackID uint) error
}

// FeedbackBiz 接口的实现.
type feedbackBiz struct {
	ds store.IStore
}

// 确保 feedbackBiz 实现了 FeedbackBiz 接口.
var _ FeedbackBiz = (*feedbackBiz)(nil)

// New 创建一个实现了 FeedbackBiz 接口的实例.
func New(ds store.IStore) *feedbackBiz {
	return &feedbackBiz{ds: ds}
}

// Create 是 FeedbackBiz 接口中 `Create` 方法的实现.
func (b *feedbackBiz) Create(ctx context.Context, userID uint, req *v1.CreateFeedbackRequest) error {
	feedback := &model.Feedback{
		UserID:  userID,
		Content: req.Content,
		Type:    req.Type,
		Status:  0, // 默认状态为待处理
	}

	return b.ds.Feedbacks().Create(ctx, feedback)
}

// GetByID 是 FeedbackBiz 接口中 `GetByID` 方法的实现.
func (b *feedbackBiz) GetByID(ctx context.Context, feedbackID uint) (*v1.FeedbackResponse, error) {
	feedback, err := b.ds.Feedbacks().GetByID(ctx, feedbackID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrPageNotFound
		}
		return nil, err
	}

	var resp v1.FeedbackResponse
	_ = copier.Copy(&resp, feedback)

	// 格式化时间
	resp.CreatedAt = feedback.CreatedAt.Format("2006-01-02 15:04:05")
	resp.UpdatedAt = feedback.UpdatedAt.Format("2006-01-02 15:04:05")

	// 添加用户信息
	if feedback.User.ID != 0 {
		resp.User = &v1.UserInfo{
			Username:  feedback.User.Username,
			Nickname:  feedback.User.Nickname,
			CreatedAt: feedback.User.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt: feedback.User.UpdatedAt.Format("2006-01-02 15:04:05"),
		}
	}

	return &resp, nil
}

// GetByUserID 是 FeedbackBiz 接口中 `GetByUserID` 方法的实现.
func (b *feedbackBiz) GetByUserID(ctx context.Context, userID uint, offset, limit int) (*v1.ListFeedbackResponse, error) {
	count, list, err := b.ds.Feedbacks().GetByUserID(ctx, userID, offset, limit)
	if err != nil {
		log.C(ctx).Errorw("Failed to list feedbacks from storage", "err", err)
		return nil, err
	}

	feedbacks := make([]*v1.FeedbackResponse, 0, len(list))
	for _, item := range list {
		feedback := item
		var resp v1.FeedbackResponse
		_ = copier.Copy(&resp, feedback)

		// 格式化时间
		resp.CreatedAt = feedback.CreatedAt.Format("2006-01-02 15:04:05")
		resp.UpdatedAt = feedback.UpdatedAt.Format("2006-01-02 15:04:05")

		// 添加用户信息
		if feedback.User.ID != 0 {
			resp.User = &v1.UserInfo{
				Username:  feedback.User.Username,
				Nickname:  feedback.User.Nickname,
				CreatedAt: feedback.User.CreatedAt.Format("2006-01-02 15:04:05"),
				UpdatedAt: feedback.User.UpdatedAt.Format("2006-01-02 15:04:05"),
			}
		}

		feedbacks = append(feedbacks, &resp)
	}

	log.C(ctx).Debugw("Get feedbacks from backend storage", "count", len(feedbacks))

	return &v1.ListFeedbackResponse{TotalCount: count, Feedbacks: feedbacks}, nil
}

// Delete 是 FeedbackBiz 接口中 `Delete` 方法的实现.
func (b *feedbackBiz) Delete(ctx context.Context, userID uint, feedbackID uint) error {
	// 先检查反馈是否存在且属于当前用户
	feedback, err := b.ds.Feedbacks().GetByID(ctx, feedbackID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPageNotFound
		}
		return err
	}

	// 检查反馈是否属于当前用户
	if feedback.UserID != userID {
		return errno.ErrUnauthorized
	}

	return b.ds.Feedbacks().Delete(ctx, feedbackID)
}

// ListAll 管理员查询所有反馈（支持筛选）
func (b *feedbackBiz) ListAll(ctx context.Context, offset, limit int, userID *uint, status *int, feedbackType *string) (*v1.ListFeedbackResponse, error) {
	count, list, err := b.ds.Feedbacks().ListAll(ctx, offset, limit, userID, status, feedbackType)
	if err != nil {
		log.C(ctx).Errorw("Failed to list all feedbacks from storage", "err", err)
		return nil, err
	}

	feedbacks := make([]*v1.FeedbackResponse, 0, len(list))
	for _, item := range list {
		feedback := item
		var resp v1.FeedbackResponse
		_ = copier.Copy(&resp, feedback)

		// 格式化时间
		resp.CreatedAt = feedback.CreatedAt.Format("2006-01-02 15:04:05")
		resp.UpdatedAt = feedback.UpdatedAt.Format("2006-01-02 15:04:05")

		// 添加用户信息
		if feedback.User.ID != 0 {
			resp.User = &v1.UserInfo{
				Username:  feedback.User.Username,
				Nickname:  feedback.User.Nickname,
				CreatedAt: feedback.User.CreatedAt.Format("2006-01-02 15:04:05"),
				UpdatedAt: feedback.User.UpdatedAt.Format("2006-01-02 15:04:05"),
			}
		}

		feedbacks = append(feedbacks, &resp)
	}

	log.C(ctx).Debugw("Get all feedbacks from backend storage", "count", len(feedbacks))

	return &v1.ListFeedbackResponse{TotalCount: count, Feedbacks: feedbacks}, nil
}

// Update 管理员更新反馈（更新状态和回复）
func (b *feedbackBiz) Update(ctx context.Context, feedbackID uint, status *int, reply *string) error {
	feedback, err := b.ds.Feedbacks().GetByID(ctx, feedbackID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPageNotFound
		}
		return err
	}

	// 更新状态
	if status != nil {
		feedback.Status = *status
	}

	// 更新回复
	if reply != nil {
		feedback.Reply = *reply
	}

	return b.ds.Feedbacks().Update(ctx, feedback)
}

// DeleteByAdmin 管理员删除反馈（不需要检查用户权限）
func (b *feedbackBiz) DeleteByAdmin(ctx context.Context, feedbackID uint) error {
	// 检查反馈是否存在
	_, err := b.ds.Feedbacks().GetByID(ctx, feedbackID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPageNotFound
		}
		return err
	}

	return b.ds.Feedbacks().Delete(ctx, feedbackID)
}
