package store

import (
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type IAdminStore interface {
	GetArticles(req *AdminArticleListRequest) ([]model.ArticleM, int64, error)
	GetArticle(id uint) (*model.ArticleM, error)
	CreateArticle(article *model.ArticleM) error
	UpdateArticle(id uint, updates map[string]interface{}) error
	DeleteArticle(id uint) error
	BulkDeleteArticles(ids []uint) error
	GetUsers(page, limit int) ([]model.User, int64, error)
	GetUserList(offset, limit int) ([]model.User, int64, error) // 后台管理系统专用，返回所有用户字段
	UpdateUser(id uint, updates map[string]interface{}) error
	DeleteUser(id uint) error
	GetCategories() ([]model.CategoryM, error)
	CreateCategory(category *model.CategoryM) error
	UpdateCategory(id uint, updates map[string]interface{}) error
	DeleteCategory(id uint) error
	GetStats() (*AdminStats, error)
}

type AdminStore struct {
	db *gorm.DB
}

type AdminArticleListRequest struct {
	Page       int    `form:"page" binding:"min=1"`
	Limit      int    `form:"limit" binding:"min=1,max=100"`
	CategoryID *uint  `form:"category_id"`
	Keyword    string `form:"keyword"`
	UserID     *uint  `form:"user_id"`
}

type AdminStats struct {
	TotalUsers      int64 `json:"total_users"`
	TotalArticles   int64 `json:"total_articles"`
	TotalCategories int64 `json:"total_categories"`
	TotalProxies    int64 `json:"total_proxies"`
	TotalFeedbacks  int64 `json:"total_feedbacks"`
}

func NewAdminStore(db *gorm.DB) IAdminStore {
	return &AdminStore{db: db}
}

func (s *AdminStore) GetArticles(req *AdminArticleListRequest) ([]model.ArticleM, int64, error) {
	query := s.db.Model(&model.ArticleM{}).Preload("User").Preload("Category")

	// 应用过滤条件
	if req.CategoryID != nil {
		query = query.Where("category_id = ?", *req.CategoryID)
	}
	if req.Keyword != "" {
		query = query.Where("title LIKE ? OR content_txt LIKE ?", "%"+req.Keyword+"%", "%"+req.Keyword+"%")
	}
	if req.UserID != nil {
		query = query.Where("user_id = ?", *req.UserID)
	}

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页
	offset := (req.Page - 1) * req.Limit
	var articles []model.ArticleM
	if err := query.Offset(offset).Limit(req.Limit).Order("created_at DESC").Find(&articles).Error; err != nil {
		return nil, 0, err
	}

	return articles, total, nil
}

func (s *AdminStore) GetArticle(id uint) (*model.ArticleM, error) {
	var article model.ArticleM
	err := s.db.Preload("User").Preload("Category").First(&article, id).Error
	if err != nil {
		return nil, err
	}
	return &article, nil
}

func (s *AdminStore) CreateArticle(article *model.ArticleM) error {
	return s.db.Create(article).Error
}

func (s *AdminStore) UpdateArticle(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.ArticleM{}).Where("id = ?", id).Updates(updates).Error
}

func (s *AdminStore) DeleteArticle(id uint) error {
	return s.db.Delete(&model.ArticleM{}, id).Error
}

func (s *AdminStore) BulkDeleteArticles(ids []uint) error {
	return s.db.Delete(&model.ArticleM{}, ids).Error
}

func (s *AdminStore) GetUsers(page, limit int) ([]model.User, int64, error) {
	query := s.db.Model(&model.User{})

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页
	offset := (page - 1) * limit
	var users []model.User
	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&users).Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

// GetUserList 获取用户列表（后台管理系统专用，返回所有用户字段，使用 offset/limit 参数）
func (s *AdminStore) GetUserList(offset, limit int) ([]model.User, int64, error) {
	query := s.db.Model(&model.User{})

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询，返回所有用户字段
	var users []model.User
	if err := query.Offset(offset).Limit(limit).Order("id DESC").Find(&users).Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (s *AdminStore) UpdateUser(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

func (s *AdminStore) DeleteUser(id uint) error {
	return s.db.Delete(&model.User{}, id).Error
}

func (s *AdminStore) GetCategories() ([]model.CategoryM, error) {
	var categories []model.CategoryM
	err := s.db.Find(&categories).Error
	return categories, err
}

func (s *AdminStore) CreateCategory(category *model.CategoryM) error {
	return s.db.Create(category).Error
}

func (s *AdminStore) UpdateCategory(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.CategoryM{}).Where("id = ?", id).Updates(updates).Error
}

func (s *AdminStore) DeleteCategory(id uint) error {
	return s.db.Delete(&model.CategoryM{}, id).Error
}

func (s *AdminStore) GetStats() (*AdminStats, error) {
	var stats AdminStats

	// 统计用户数
	s.db.Model(&model.User{}).Count(&stats.TotalUsers)

	// 统计文章数
	s.db.Model(&model.ArticleM{}).Count(&stats.TotalArticles)

	// 统计分类数
	s.db.Model(&model.CategoryM{}).Count(&stats.TotalCategories)

	// 统计代理数
	s.db.Model(&model.ProxyServerM{}).Count(&stats.TotalProxies)

	// 统计反馈数
	s.db.Model(&model.Feedback{}).Count(&stats.TotalFeedbacks)

	return &stats, nil
}
