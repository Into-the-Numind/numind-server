package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// AdminImageListRequest 管理员图片列表查询请求
type AdminImageListRequest struct {
	Offset  int     `form:"offset"`
	Limit   int     `form:"limit"`
	UserID  *uint   `form:"user_id"`
	BookID  *uint   `form:"book_id"`
	Status  *string `form:"status"`
	Keyword string  `form:"keyword"` // 支持搜索文件名
}

type ImageStore interface {
	Create(ctx context.Context, image *model.ImageM) error
	BatchCreate(ctx context.Context, images []*model.ImageM) error
	GetByID(ctx context.Context, id uint) (*model.ImageM, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.ImageM, error)
	ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.ImageM, error)
	ListAll(ctx context.Context, req *AdminImageListRequest) (int64, []*model.ImageM, error) // 获取所有图片（用于后台管理等场景）
	Update(ctx context.Context, image *model.ImageM) error
	Delete(ctx context.Context, id uint) error
}

type images struct {
	db *gorm.DB
}

var _ ImageStore = (*images)(nil)

func NewImageStore(db *gorm.DB) ImageStore {
	return &images{db}
}

func (s *images) Create(ctx context.Context, image *model.ImageM) error {
	return s.db.WithContext(ctx).Create(image).Error
}

func (s *images) BatchCreate(ctx context.Context, images []*model.ImageM) error {
	return s.db.WithContext(ctx).Create(&images).Error
}

func (s *images) GetByID(ctx context.Context, id uint) (*model.ImageM, error) {
	var image model.ImageM
	err := s.db.WithContext(ctx).First(&image, id).Error
	if err != nil {
		return nil, err
	}
	return &image, nil
}

func (s *images) ListByUser(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.ImageM, err error) {
	err = s.db.WithContext(ctx).Where("user_id = ?", userID).
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *images) ListByBook(ctx context.Context, bookID uint, offset, limit int) (count int64, ret []*model.ImageM, err error) {
	err = s.db.WithContext(ctx).Where("book_id = ?", bookID).
		Offset(offset).Limit(defaultLimit(limit)).Order("id asc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

// ListAll 获取所有图片（管理员，支持多条件筛选）
func (s *images) ListAll(ctx context.Context, req *AdminImageListRequest) (int64, []*model.ImageM, error) {
	query := s.db.WithContext(ctx).Model(&model.ImageM{})

	// 应用过滤条件
	if req.UserID != nil {
		query = query.Where("user_id = ?", *req.UserID)
	}
	if req.BookID != nil {
		query = query.Where("book_id = ?", *req.BookID)
	}
	if req.Status != nil && *req.Status != "" {
		query = query.Where("status = ?", *req.Status)
	}
	if req.Keyword != "" {
		query = query.Where("file_name LIKE ?", "%"+req.Keyword+"%")
	}

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}

	// 分页查询
	var images []*model.ImageM
	if err := query.Order("id DESC").
		Offset(req.Offset).
		Limit(defaultLimit(req.Limit)).
		Find(&images).Error; err != nil {
		return 0, nil, err
	}

	return total, images, nil
}

func (s *images) Update(ctx context.Context, image *model.ImageM) error {
	return s.db.WithContext(ctx).Save(image).Error
}

func (s *images) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.ImageM{}, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}
