package services

import (
	"encoding/json"
	"fmt"
	"time"

	"numind-server/configs/config"
	"numind-server/internal/models"

	"gorm.io/gorm"
)

type ArticleService struct {
	db  *gorm.DB
	cfg *config.Config
}

type ArticleFetchRequest struct {
	URL string `json:"url" binding:"required"`
}

type ArticleCreateRequest struct {
	URL         string                   `json:"url" binding:"required"`
	Title       string                   `json:"title" binding:"required"`
	AccountName string                   `json:"account_name" binding:"required"`
	PublishTime string                   `json:"publish_time" binding:"required"`
	Content     []map[string]interface{} `json:"content" binding:"required"`
	RawHTML     string                   `json:"raw_html"`
	CategoryID  *uint                    `json:"category_id"`
	ContentTxt  string                   `json:"content_txt"`
}

type ArticleUpdateRequest struct {
	Title       *string `json:"title"`
	AccountName *string `json:"account_name"`
	CategoryID  *uint   `json:"category_id"`
	Summary     *string `json:"summary"`
	ContentTxt  *string `json:"content_txt"`
}

type ArticleListRequest struct {
	Page       int    `form:"page" binding:"min=1"`
	Limit      int    `form:"limit" binding:"min=1,max=100"`
	CategoryID *uint  `form:"category_id"`
	Keyword    string `form:"keyword"`
	UserID     *uint  `form:"user_id"`
}

type ArticleListResponse struct {
	Items []models.Article `json:"items"`
	Total int64            `json:"total"`
	Page  int              `json:"page"`
	Limit int              `json:"limit"`
	Pages int              `json:"pages"`
}

type ParaphraseRequest struct {
	Text string `json:"text" binding:"required"`
}

func NewArticleService(db *gorm.DB, cfg *config.Config) *ArticleService {
	return &ArticleService{
		db:  db,
		cfg: cfg,
	}
}

// FetchArticle 获取文章内容
func (s *ArticleService) FetchArticle(userID uint, req *ArticleFetchRequest) (*models.Article, error) {
	// 检查文章是否已存在
	var existingArticle models.Article
	err := s.db.Where("url = ?", req.URL).First(&existingArticle).Error
	if err == nil {
		// 文章已存在，返回现有文章
		return &existingArticle, nil
	}

	// 获取文章内容（这里需要实现网页抓取逻辑）
	articleData, err := s.scrapeArticle(req.URL)
	if err != nil {
		return nil, fmt.Errorf("获取文章内容失败: %v", err)
	}

	// 将Content转换为JSON
	contentJSON, err := json.Marshal(articleData.Content)
	if err != nil {
		return nil, fmt.Errorf("序列化内容失败: %v", err)
	}

	// 创建新文章
	article := models.Article{
		UserID:      userID,
		URL:         req.URL,
		Title:       articleData.Title,
		AccountName: articleData.AccountName,
		PublishTime: articleData.PublishTime,
		Content:     models.JSON(contentJSON),
		ContentTxt:  articleData.ContentTxt,
		CreatedAt:   time.Now(),
		CategoryAt:  time.Now(),
	}

	if err := s.db.Create(&article).Error; err != nil {
		return nil, fmt.Errorf("保存文章失败: %v", err)
	}

	return &article, nil
}

// GetArticles 获取文章列表
func (s *ArticleService) GetArticles(req *ArticleListRequest) (*ArticleListResponse, error) {
	query := s.db.Model(&models.Article{}).Preload("User").Preload("Category")

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
		return nil, err
	}

	// 分页
	offset := (req.Page - 1) * req.Limit
	var articles []models.Article
	if err := query.Offset(offset).Limit(req.Limit).Order("created_at DESC").Find(&articles).Error; err != nil {
		return nil, err
	}

	pages := int((total + int64(req.Limit) - 1) / int64(req.Limit))

	return &ArticleListResponse{
		Items: articles,
		Total: total,
		Page:  req.Page,
		Limit: req.Limit,
		Pages: pages,
	}, nil
}

// GetArticle 获取单个文章
func (s *ArticleService) GetArticle(articleID uint) (*models.Article, error) {
	var article models.Article
	err := s.db.Preload("User").Preload("Category").First(&article, articleID).Error
	if err != nil {
		return nil, err
	}
	return &article, nil
}

// UpdateArticleCategory 更新文章分类
func (s *ArticleService) UpdateArticleCategory(articleID, userID uint, categoryID *uint) error {
	var article models.Article
	if err := s.db.Where("id = ? AND user_id = ?", articleID, userID).First(&article).Error; err != nil {
		return fmt.Errorf("文章不存在或无权限")
	}

	article.CategoryID = categoryID
	article.CategoryAt = time.Now()

	return s.db.Save(&article).Error
}

// DeleteArticle 删除文章
func (s *ArticleService) DeleteArticle(articleID, userID uint) error {
	return s.db.Where("id = ? AND user_id = ?", articleID, userID).Delete(&models.Article{}).Error
}

// AddFavorite 添加收藏
func (s *ArticleService) AddFavorite(userID, articleID uint) error {
	// 检查是否已收藏
	var existing models.Favorite
	err := s.db.Where("user_id = ? AND article_id = ?", userID, articleID).First(&existing).Error
	if err == nil {
		return fmt.Errorf("文章已收藏")
	}

	favorite := models.Favorite{
		UserID:    userID,
		ArticleID: articleID,
		CreatedAt: time.Now(),
	}

	return s.db.Create(&favorite).Error
}

// RemoveFavorite 移除收藏
func (s *ArticleService) RemoveFavorite(userID, articleID uint) error {
	return s.db.Where("user_id = ? AND article_id = ?", userID, articleID).Delete(&models.Favorite{}).Error
}

// GetFavorites 获取用户收藏列表
func (s *ArticleService) GetFavorites(userID uint, page, limit int) (*ArticleListResponse, error) {
	query := s.db.Model(&models.Favorite{}).
		Joins("JOIN articles ON favorites.article_id = articles.id").
		Where("favorites.user_id = ?", userID).
		Preload("Article.User").
		Preload("Article.Category")

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	// 分页
	offset := (page - 1) * limit
	var favorites []models.Favorite
	if err := query.Offset(offset).Limit(limit).Order("favorites.created_at DESC").Find(&favorites).Error; err != nil {
		return nil, err
	}

	// 转换为文章列表
	var articles []models.Article
	for _, fav := range favorites {
		articles = append(articles, fav.Article)
	}

	pages := int((total + int64(limit) - 1) / int64(limit))

	return &ArticleListResponse{
		Items: articles,
		Total: total,
		Page:  page,
		Limit: limit,
		Pages: pages,
	}, nil
}

// ParaphraseText 文本释义
func (s *ArticleService) ParaphraseText(req *ParaphraseRequest) (string, error) {
	// 这里需要调用AI服务进行文本释义
	// 暂时返回原文
	return req.Text, nil
}

// scrapeArticle 抓取文章内容（简化实现）
func (s *ArticleService) scrapeArticle(url string) (*ArticleCreateRequest, error) {
	// 这里应该实现实际的网页抓取逻辑
	// 暂时返回模拟数据
	return &ArticleCreateRequest{
		URL:         url,
		Title:       "示例文章标题",
		AccountName: "示例公众号",
		PublishTime: time.Now().Format("2006-01-02 15:04:05"),
		Content: []map[string]interface{}{
			{"type": "text", "content": "示例文章内容"},
		},
		ContentTxt: "示例文章纯文本内容",
	}, nil
}
