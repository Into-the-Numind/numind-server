package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type BookStore interface {
	Create(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
	GetByIDs(ctx context.Context, ids []uint, books *[]*model.BookM) error // 根据ID列表获取books
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.BookM, error)
	ListByCategory(ctx context.Context, categoryID uint, offset, limit int) (int64, []*model.BookM, error)
	ListAll(ctx context.Context, offset, limit int) (int64, []*model.BookM, error) // 新增：获取所有书籍
	Update(ctx context.Context, book *model.BookM) error
	Delete(ctx context.Context, id uint) error
	DeleteBatch(ctx context.Context, ids []uint) error
	CountByUserAndStatus(ctx context.Context, userID uint, excludeStatus string, exclude bool) (int64, error)
	CountByUserAndStatusAndDeleted(ctx context.Context, userID uint, excludeStatus string, exclude bool) (int64, error)
	UpdateUserBookStatsOnDelete(ctx context.Context, userID uint, bookStatus string) error
	UpdateUserBookStatsOnBatchDelete(ctx context.Context, books []*model.BookM) error
	UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error
}

type books struct {
	db *gorm.DB
}

var _ BookStore = (*books)(nil)

func NewBookStore(db *gorm.DB) BookStore {
	return &books{db}
}

func (s *books) Create(ctx context.Context, book *model.BookM) error {
	return s.db.WithContext(ctx).Create(book).Error
}

func (s *books) GetByID(ctx context.Context, id uint) (*model.BookM, error) {
	var book model.BookM
	// 使用显式的WHERE条件，确保能正确查询
	// GORM会自动添加 deleted_at IS NULL 条件（因为BookM继承了gorm.Model）
	err := s.db.WithContext(ctx).
		Where("id = ?", id).
		Preload("Category").
		First(&book).Error
	if err != nil {
		// 记录详细的错误信息，便于调试
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.C(ctx).Warnw("笔记不存在或已被删除", "book_id", id)
		} else {
			log.C(ctx).Errorw("查询笔记失败", "book_id", id, "error", err)
		}
		return nil, err
	}
	log.C(ctx).Debugw("成功获取笔记", "book_id", id, "user_id", book.UserID, "status", book.Status, "title", book.Title)
	return &book, nil
}

func (s *books) GetByIDs(ctx context.Context, ids []uint, books *[]*model.BookM) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("id IN (?)", ids).Find(books).Error
}

func (s *books) ListByUser(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.BookM, err error) {
	err = s.db.WithContext(ctx).Where("user_id = ? AND status != ?", userID, model.BookStatusFailed).
		Preload("Category").
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *books) ListByCategory(ctx context.Context, categoryID uint, offset, limit int) (count int64, ret []*model.BookM, err error) {
	err = s.db.WithContext(ctx).Where("category_id = ? AND status != ?", categoryID, model.BookStatusFailed).
		Preload("Category").
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

// ListAll 获取所有书籍（用于后台管理等场景，返回所有状态的书籍）
func (s *books) ListAll(ctx context.Context, offset, limit int) (count int64, ret []*model.BookM, err error) {
	err = s.db.WithContext(ctx).
		Preload("Category").
		Offset(offset).Limit(defaultLimit(limit)).Order("id ASC").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *books) Update(ctx context.Context, book *model.BookM) error {
	return s.db.WithContext(ctx).Save(book).Error
}

func (s *books) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.BookM{}, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func (s *books) DeleteBatch(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}

	// 先获取要删除的books信息，用于更新用户统计
	var books []*model.BookM
	if err := s.db.WithContext(ctx).Where("id IN (?)", ids).Find(&books).Error; err != nil {
		return err
	}

	// 删除books
	if err := s.db.WithContext(ctx).Where("id IN (?)", ids).Delete(&model.BookM{}).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	// 更新用户统计
	if err := s.UpdateUserBookStatsOnBatchDelete(ctx, books); err != nil {
		// 记录错误但不影响删除操作
		// 这里可以考虑记录日志
	}

	return nil
}

// CountByUserAndStatus 统计用户指定状态的书本数量
func (s *books) CountByUserAndStatus(ctx context.Context, userID uint, excludeStatus string, exclude bool) (int64, error) {
	var count int64
	query := s.db.WithContext(ctx).Model(&model.BookM{}).Where("user_id = ?", userID)

	if exclude {
		query = query.Where("status != ?", excludeStatus)
	} else {
		query = query.Where("status = ?", excludeStatus)
	}

	err := query.Count(&count).Error
	return count, err
}

// CountByUserAndStatusAndDeleted 统计用户指定状态且未删除的书本数量
func (s *books) CountByUserAndStatusAndDeleted(ctx context.Context, userID uint, excludeStatus string, exclude bool) (int64, error) {
	var count int64
	query := s.db.WithContext(ctx).Model(&model.BookM{}).Where("user_id = ?", userID)

	if exclude {
		query = query.Where("status != ?", excludeStatus)
	} else {
		query = query.Where("status = ?", excludeStatus)
	}

	// 只统计未删除的记录（deleted_at IS NULL）
	err := query.Where("deleted_at IS NULL").Count(&count).Error
	return count, err
}

// UpdateUserBookStatsOnDelete 删除book时更新用户统计
func (s *books) UpdateUserBookStatsOnDelete(ctx context.Context, userID uint, bookStatus string) error {
	// 如果book状态不是failed，需要减少用户的book_num统计
	if bookStatus != model.BookStatusFailed {
		// 使用数据库的原子操作来减少book_num字段
		return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
			UpdateColumn("book_num", gorm.Expr("book_num - ?", 1)).Error
	}
	return nil
}

// UpdateUserBookStatsOnBatchDelete 批量删除book时更新用户统计
func (s *books) UpdateUserBookStatsOnBatchDelete(ctx context.Context, books []*model.BookM) error {
	// 按用户ID分组统计需要减少的book数量
	userBookCounts := make(map[uint]int)
	for _, book := range books {
		// 只统计非failed状态的book，因为failed状态的book不影响book_num
		if book.Status != model.BookStatusFailed {
			userBookCounts[book.UserID]++
		}
	}

	// 批量更新每个用户的book_num统计
	for userID, count := range userBookCounts {
		if count > 0 {
			if err := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
				UpdateColumn("book_num", gorm.Expr("book_num - ?", count)).Error; err != nil {
				return err
			}
		}
	}

	return nil
}

// UpdateUserBookStatsOnStatusChange 当book状态变化时更新用户统计
func (s *books) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
	// 如果状态从非failed变为failed，需要减少book_num和book_all_num
	if oldStatus != model.BookStatusFailed && newStatus == model.BookStatusFailed {
		return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
			UpdateColumns(map[string]interface{}{
				"book_num":     gorm.Expr("book_num - ?", 1),
				"book_all_num": gorm.Expr("book_all_num - ?", 1),
			}).Error
	}

	// 如果状态从failed变为非failed，需要增加book_all_num
	if oldStatus == model.BookStatusFailed && newStatus != model.BookStatusFailed {
		return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
			UpdateColumn("book_all_num", gorm.Expr("book_all_num + ?", 1)).Error
	}

	return nil
}
