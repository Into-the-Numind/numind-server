package store

import (
	"context"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// FeedbackStore 定义了 feedback 模块在 store 层所实现的方法.
type FeedbackStore interface {
	Create(ctx context.Context, feedback *model.Feedback) error
	GetByID(ctx context.Context, feedbackID uint) (*model.Feedback, error)
	GetByUserID(ctx context.Context, userID uint, offset, limit int) (int64, []*model.Feedback, error)
	Update(ctx context.Context, feedback *model.Feedback) error
	Delete(ctx context.Context, feedbackID uint) error
}

// FeedbackStore 接口的实现.
type feedbacks struct {
	db *gorm.DB
}

// 确保 feedbacks 实现了 FeedbackStore 接口.
var _ FeedbackStore = (*feedbacks)(nil)

func newFeedbacks(db *gorm.DB) *feedbacks {
	return &feedbacks{db}
}

// Create 插入一条 feedback 记录.
func (f *feedbacks) Create(ctx context.Context, feedback *model.Feedback) error {
	return f.db.Create(feedback).Error
}

// GetByID 根据ID查询指定 feedback 的数据库记录.
func (f *feedbacks) GetByID(ctx context.Context, feedbackID uint) (*model.Feedback, error) {
	var feedback model.Feedback
	if err := f.db.Preload("User").Where("id = ?", feedbackID).First(&feedback).Error; err != nil {
		return nil, err
	}
	return &feedback, nil
}

// GetByUserID 根据用户ID查询 feedback 列表.
func (f *feedbacks) GetByUserID(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.Feedback, err error) {
	query := f.db.Model(&model.Feedback{}).Where("user_id = ?", userID)

	// 获取总数
	if err := query.Count(&count).Error; err != nil {
		return 0, nil, err
	}

	// 分页查询
	err = query.Preload("User").Offset(offset).Limit(defaultLimit(limit)).Order("created_at DESC").Find(&ret).Error
	return
}

// Update 更新一条 feedback 数据库记录.
func (f *feedbacks) Update(ctx context.Context, feedback *model.Feedback) error {
	return f.db.Save(feedback).Error
}

// Delete 根据ID删除数据库 feedback 记录.
func (f *feedbacks) Delete(ctx context.Context, feedbackID uint) error {
	return f.db.Delete(&model.Feedback{}, feedbackID).Error
}
