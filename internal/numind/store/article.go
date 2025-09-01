package store

import (
	"numind-server/internal/pkg/model"
	"time"

	"gorm.io/gorm"
)

type IArticleStore interface {
	CreateArticle(article *model.ArticleM) error
	GetArticleByID(id uint) (*model.ArticleM, error)
	GetArticleByURL(url string) (*model.ArticleM, error)
	GetArticles(req *ArticleListRequest) ([]model.ArticleM, int64, error)
	UpdateArticle(article *model.ArticleM) error
	DeleteArticle(id uint) error
	BulkDeleteArticles(ids []uint) error
	UpdateArticleCategory(id, userID uint, categoryID *uint) error
	GetFavorites(userID uint, page, limit int) ([]model.ArticleM, int64, error)
	AddFavorite(userID, articleID uint) error
	RemoveFavorite(userID, articleID uint) error
}

type ArticleStore struct {
	db *gorm.DB
}

type ArticleListRequest struct {
	Page       int    `form:"page" binding:"min=1"`
	Limit      int    `form:"limit" binding:"min=1,max=100"`
	CategoryID *uint  `form:"category_id"`
	Keyword    string `form:"keyword"`
	UserID     *uint  `form:"user_id"`
}

func NewArticleStore(db *gorm.DB) IArticleStore {
	return &ArticleStore{db: db}
}

func (s *ArticleStore) CreateArticle(article *model.ArticleM) error {
	return s.db.Create(article).Error
}

func (s *ArticleStore) GetArticleByID(id uint) (*model.ArticleM, error) {
	var article model.ArticleM
	err := s.db.Preload("User").Preload("Category").First(&article, id).Error
	if err != nil {
		return nil, err
	}
	return &article, nil
}

func (s *ArticleStore) GetArticleByURL(url string) (*model.ArticleM, error) {
	var article model.ArticleM
	err := s.db.Where("url = ?", url).First(&article).Error
	if err != nil {
		return nil, err
	}
	return &article, nil
}

func (s *ArticleStore) GetArticles(req *ArticleListRequest) ([]model.ArticleM, int64, error) {
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

func (s *ArticleStore) UpdateArticle(article *model.ArticleM) error {
	return s.db.Save(article).Error
}

func (s *ArticleStore) DeleteArticle(id uint) error {
	return s.db.Delete(&model.ArticleM{}, id).Error
}

func (s *ArticleStore) BulkDeleteArticles(ids []uint) error {
	return s.db.Delete(&model.ArticleM{}, ids).Error
}

func (s *ArticleStore) UpdateArticleCategory(id, userID uint, categoryID *uint) error {
	var article model.ArticleM
	if err := s.db.Where("id = ? AND user_id = ?", id, userID).First(&article).Error; err != nil {
		return err
	}

	article.CategoryID = categoryID
	article.CategoryAt = time.Now()

	return s.db.Save(&article).Error
}

func (s *ArticleStore) GetFavorites(userID uint, page, limit int) ([]model.ArticleM, int64, error) {
	query := s.db.Model(&model.Favorite{}).
		Joins("JOIN articles ON favorites.article_id = articles.id").
		Where("favorites.user_id = ?", userID).
		Preload("Article.User").
		Preload("Article.Category")

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页
	offset := (page - 1) * limit
	var favorites []model.Favorite
	if err := query.Offset(offset).Limit(limit).Order("favorites.created_at DESC").Find(&favorites).Error; err != nil {
		return nil, 0, err
	}

	// 转换为文章列表
	var articles []model.ArticleM
	for _, fav := range favorites {
		articles = append(articles, fav.Article)
	}

	return articles, total, nil
}

func (s *ArticleStore) AddFavorite(userID, articleID uint) error {
	// 检查是否已收藏
	var existing model.Favorite
	err := s.db.Where("user_id = ? AND article_id = ?", userID, articleID).First(&existing).Error
	if err == nil {
		return gorm.ErrRecordNotFound // 已收藏
	}

	favorite := model.Favorite{
		UserID:    userID,
		ArticleID: articleID,
	}

	return s.db.Create(&favorite).Error
}

func (s *ArticleStore) RemoveFavorite(userID, articleID uint) error {
	return s.db.Where("user_id = ? AND article_id = ?", userID, articleID).Delete(&model.Favorite{}).Error
}
