package services

import (
	"fmt"
	"numind-server/internal/pkg/model"
	"time"

	"numind-server/configs/config"

	"gorm.io/gorm"
)

type AdminService struct {
	db  *gorm.DB
	cfg *config.Config
}

type AdminArticleListRequest struct {
	Page       int    `form:"page" binding:"min=1"`
	Limit      int    `form:"limit" binding:"min=1,max=100"`
	CategoryID *uint  `form:"category_id"`
	Keyword    string `form:"keyword"`
	UserID     *uint  `form:"user_id"`
}

type AdminArticleListResponse struct {
	Items []model.Article `json:"items"`
	Total int64           `json:"total"`
	Page  int             `json:"page"`
	Limit int             `json:"limit"`
	Pages int             `json:"pages"`
}

type AdminArticleCreateRequest struct {
	Title       string `json:"title" binding:"required"`
	CategoryID  *uint  `json:"category_id"`
	Summary     string `json:"summary"`
	Content     string `json:"content"`
	ContentTxt  string `json:"content_txt"`
	AccountName string `json:"account_name"`
	Status      int    `json:"status"`
}

type AdminArticleUpdateRequest struct {
	Title       *string `json:"title"`
	CategoryID  *uint   `json:"category_id"`
	Summary     *string `json:"summary"`
	Content     *string `json:"content"`
	ContentTxt  *string `json:"content_txt"`
	AccountName *string `json:"account_name"`
	Status      *int    `json:"status"`
}

type AdminUserUpdateRequest struct {
	Username *string `json:"username"`
	Nickname *string `json:"nickname"`
	Phone    *string `json:"phone"`
	Password *string `json:"password"`
	Status   *int    `json:"status"`
}

type AdminCategoryCreateRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type AdminCategoryUpdateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type AdminProxyCreateRequest struct {
	IPAddress string `json:"ip_address" binding:"required"`
	Port      int    `json:"port" binding:"required"`
	Protocol  string `json:"protocol"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Location  string `json:"location"`
	Remarks   string `json:"remarks"`
}

type AdminProxyUpdateRequest struct {
	IPAddress *string `json:"ip_address"`
	Port      *int    `json:"port"`
	Protocol  *string `json:"protocol"`
	Username  string  `json:"username"`
	Password  string  `json:"password"`
	Location  string  `json:"location"`
	Status    *int    `json:"status"`
	Remarks   string  `json:"remarks"`
}

type AdminFeedbackUpdateRequest struct {
	Status *int   `json:"status"`
	Reply  string `json:"reply"`
}

type AdminAboutUsUpdateRequest struct {
	Content string `json:"content" binding:"required"`
}

type AdminAgreementUpdateRequest struct {
	Content string `json:"content" binding:"required"`
}

type AdminStatsResponse struct {
	TotalUsers      int64 `json:"total_users"`
	TotalArticles   int64 `json:"total_articles"`
	TotalCategories int64 `json:"total_categories"`
	TotalProxies    int64 `json:"total_proxies"`
	TotalFeedbacks  int64 `json:"total_feedbacks"`
}

func NewAdminService(db *gorm.DB, cfg *config.Config) *AdminService {
	return &AdminService{
		db:  db,
		cfg: cfg,
	}
}

// GetArticles 获取文章列表（管理员）
func (s *AdminService) GetArticles(req *AdminArticleListRequest) (*AdminArticleListResponse, error) {
	query := s.db.Model(&model.Article{}).Preload("User").Preload("Category")

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
	var articles []model.Article
	if err := query.Offset(offset).Limit(req.Limit).Order("created_at DESC").Find(&articles).Error; err != nil {
		return nil, err
	}

	pages := int((total + int64(req.Limit) - 1) / int64(req.Limit))

	return &AdminArticleListResponse{
		Items: articles,
		Total: total,
		Page:  req.Page,
		Limit: req.Limit,
		Pages: pages,
	}, nil
}

// GetArticle 获取单个文章（管理员）
func (s *AdminService) GetArticle(articleID uint) (*model.Article, error) {
	var article model.Article
	err := s.db.Preload("User").Preload("Category").First(&article, articleID).Error
	if err != nil {
		return nil, err
	}
	return &article, nil
}

// CreateArticle 创建文章（管理员）
func (s *AdminService) CreateArticle(req *AdminArticleCreateRequest) (*model.Article, error) {
	article := model.Article{
		Title:       req.Title,
		CategoryID:  req.CategoryID,
		Summary:     req.Summary,
		ContentTxt:  req.ContentTxt,
		AccountName: req.AccountName,
		CreatedAt:   time.Now(),
		CategoryAt:  time.Now(),
	}

	if err := s.db.Create(&article).Error; err != nil {
		return nil, err
	}

	return &article, nil
}

// UpdateArticle 更新文章（管理员）
func (s *AdminService) UpdateArticle(articleID uint, req *AdminArticleUpdateRequest) error {
	var article model.Article
	if err := s.db.First(&article, articleID).Error; err != nil {
		return err
	}

	// 更新字段
	if req.Title != nil {
		article.Title = *req.Title
	}
	if req.CategoryID != nil {
		article.CategoryID = req.CategoryID
	}
	if req.Summary != nil {
		article.Summary = *req.Summary
	}
	if req.ContentTxt != nil {
		article.ContentTxt = *req.ContentTxt
	}
	if req.AccountName != nil {
		article.AccountName = *req.AccountName
	}

	return s.db.Save(&article).Error
}

// DeleteArticle 删除文章（管理员）
func (s *AdminService) DeleteArticle(articleID uint) error {
	return s.db.Delete(&model.Article{}, articleID).Error
}

// BulkDeleteArticles 批量删除文章
func (s *AdminService) BulkDeleteArticles(articleIDs []uint) error {
	return s.db.Delete(&model.Article{}, articleIDs).Error
}

// GetUsers 获取用户列表
func (s *AdminService) GetUsers(page, limit int) (*ArticleListResponse, error) {
	query := s.db.Model(&model.User{})

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	// 分页
	offset := (page - 1) * limit
	var users []model.User
	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&users).Error; err != nil {
		return nil, err
	}

	// 转换为文章列表格式（复用结构）
	var articles []model.Article
	for _, user := range users {
		// 这里需要转换，暂时返回空
		fmt.Println(user)
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

// UpdateUser 更新用户
func (s *AdminService) UpdateUser(userID uint, req *AdminUserUpdateRequest) error {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		return err
	}

	// 更新字段
	if req.Username != nil {
		user.Username = *req.Username
	}
	if req.Nickname != nil {
		user.Nickname = *req.Nickname
	}
	if req.Phone != nil {
		user.Phone = *req.Phone
	}
	if req.Status != nil {
		user.Status = *req.Status
	}

	return s.db.Save(&user).Error
}

// DeleteUser 删除用户
func (s *AdminService) DeleteUser(userID uint) error {
	return s.db.Delete(&model.User{}, userID).Error
}

// GetCategories 获取分类列表
func (s *AdminService) GetCategories() ([]model.Category, error) {
	var categories []model.Category
	err := s.db.Find(&categories).Error
	return categories, err
}

// CreateCategory 创建分类
func (s *AdminService) CreateCategory(req *AdminCategoryCreateRequest) (*model.Category, error) {
	category := model.Category{
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   time.Now(),
	}

	if err := s.db.Create(&category).Error; err != nil {
		return nil, err
	}

	return &category, nil
}

// UpdateCategory 更新分类
func (s *AdminService) UpdateCategory(categoryID uint, req *AdminCategoryUpdateRequest) error {
	var category model.Category
	if err := s.db.First(&category, categoryID).Error; err != nil {
		return err
	}

	if req.Name != nil {
		category.Name = *req.Name
	}
	if req.Description != nil {
		category.Description = *req.Description
	}

	return s.db.Save(&category).Error
}

// DeleteCategory 删除分类
func (s *AdminService) DeleteCategory(categoryID uint) error {
	return s.db.Delete(&model.Category{}, categoryID).Error
}

// GetProxies 获取代理列表
func (s *AdminService) GetProxies(page, limit int) (*ArticleListResponse, error) {
	query := s.db.Model(&model.ProxyServer{})

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	// 分页
	offset := (page - 1) * limit
	var proxies []model.ProxyServer
	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&proxies).Error; err != nil {
		return nil, err
	}

	// 转换为文章列表格式（复用结构）
	var articles []model.Article

	pages := int((total + int64(limit) - 1) / int64(limit))

	return &ArticleListResponse{
		Items: articles,
		Total: total,
		Page:  page,
		Limit: limit,
		Pages: pages,
	}, nil
}

// CreateProxy 创建代理
func (s *AdminService) CreateProxy(req *AdminProxyCreateRequest) (*model.ProxyServer, error) {
	proxy := model.ProxyServer{
		IPAddress:   req.IPAddress,
		Port:        req.Port,
		Protocol:    req.Protocol,
		Username:    req.Username,
		Password:    req.Password,
		Location:    req.Location,
		Remarks:     req.Remarks,
		Status:      1,
		IsAutoAdded: 0,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.db.Create(&proxy).Error; err != nil {
		return nil, err
	}

	return &proxy, nil
}

// UpdateProxy 更新代理
func (s *AdminService) UpdateProxy(proxyID uint, req *AdminProxyUpdateRequest) error {
	var proxy model.ProxyServer
	if err := s.db.First(&proxy, proxyID).Error; err != nil {
		return err
	}

	if req.IPAddress != nil {
		proxy.IPAddress = *req.IPAddress
	}
	if req.Port != nil {
		proxy.Port = *req.Port
	}
	if req.Protocol != nil {
		proxy.Protocol = *req.Protocol
	}
	if req.Username != "" {
		proxy.Username = req.Username
	}
	if req.Password != "" {
		proxy.Password = req.Password
	}
	if req.Location != "" {
		proxy.Location = req.Location
	}
	if req.Status != nil {
		proxy.Status = *req.Status
	}
	if req.Remarks != "" {
		proxy.Remarks = req.Remarks
	}

	proxy.UpdatedAt = time.Now()
	return s.db.Save(&proxy).Error
}

// DeleteProxy 删除代理
func (s *AdminService) DeleteProxy(proxyID uint) error {
	return s.db.Delete(&model.ProxyServer{}, proxyID).Error
}

// BulkDeleteProxies 批量删除代理
func (s *AdminService) BulkDeleteProxies(proxyIDs []uint) error {
	return s.db.Delete(&model.ProxyServer{}, proxyIDs).Error
}

// GetFeedbacks 获取反馈列表
func (s *AdminService) GetFeedbacks(page, limit int) (*ArticleListResponse, error) {
	query := s.db.Model(&model.Feedback{}).Preload("User")

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	// 分页
	offset := (page - 1) * limit
	var feedbacks []model.Feedback
	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&feedbacks).Error; err != nil {
		return nil, err
	}

	// 转换为文章列表格式（复用结构）
	var articles []model.Article

	pages := int((total + int64(limit) - 1) / int64(limit))

	return &ArticleListResponse{
		Items: articles,
		Total: total,
		Page:  page,
		Limit: limit,
		Pages: pages,
	}, nil
}

// UpdateFeedback 更新反馈
func (s *AdminService) UpdateFeedback(feedbackID uint, req *AdminFeedbackUpdateRequest) error {
	var feedback model.Feedback
	if err := s.db.First(&feedback, feedbackID).Error; err != nil {
		return err
	}

	if req.Status != nil {
		feedback.Status = *req.Status
	}
	if req.Reply != "" {
		feedback.Reply = req.Reply
	}

	feedback.UpdatedAt = time.Now()
	return s.db.Save(&feedback).Error
}

// DeleteFeedback 删除反馈
func (s *AdminService) DeleteFeedback(feedbackID uint) error {
	return s.db.Delete(&model.Feedback{}, feedbackID).Error
}

// BulkDeleteFeedbacks 批量删除反馈
func (s *AdminService) BulkDeleteFeedbacks(feedbackIDs []uint) error {
	return s.db.Delete(&model.Feedback{}, feedbackIDs).Error
}

// GetAboutUs 获取关于我们
func (s *AdminService) GetAboutUs() (*model.AboutUs, error) {
	var aboutUs model.AboutUs
	err := s.db.First(&aboutUs).Error
	if err == gorm.ErrRecordNotFound {
		// 创建默认记录
		aboutUs = model.AboutUs{
			Content:   "关于我们的内容",
			UpdatedAt: time.Now(),
		}
		s.db.Create(&aboutUs)
	}
	return &aboutUs, nil
}

// UpdateAboutUs 更新关于我们
func (s *AdminService) UpdateAboutUs(req *AdminAboutUsUpdateRequest) error {
	var aboutUs model.AboutUs
	err := s.db.First(&aboutUs).Error
	if err == gorm.ErrRecordNotFound {
		aboutUs = model.AboutUs{
			Content:   req.Content,
			UpdatedAt: time.Now(),
		}
		return s.db.Create(&aboutUs).Error
	}

	aboutUs.Content = req.Content
	aboutUs.UpdatedAt = time.Now()
	return s.db.Save(&aboutUs).Error
}

// GetAgreement 获取协议
func (s *AdminService) GetAgreement(agreementType string) (*model.Agreement, error) {
	var agreement model.Agreement
	err := s.db.Where("type = ?", agreementType).First(&agreement).Error
	return &agreement, err
}

// UpdateAgreement 更新协议
func (s *AdminService) UpdateAgreement(agreementType string, req *AdminAgreementUpdateRequest) error {
	var agreement model.Agreement
	err := s.db.Where("type = ?", agreementType).First(&agreement).Error
	if err == gorm.ErrRecordNotFound {
		agreement = model.Agreement{
			Type:      agreementType,
			Content:   req.Content,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		return s.db.Create(&agreement).Error
	}

	agreement.Content = req.Content
	agreement.UpdatedAt = time.Now()
	return s.db.Save(&agreement).Error
}

// GetStats 获取统计信息
func (s *AdminService) GetStats() (*AdminStatsResponse, error) {
	var stats AdminStatsResponse

	// 统计用户数
	s.db.Model(&model.User{}).Count(&stats.TotalUsers)

	// 统计文章数
	s.db.Model(&model.Article{}).Count(&stats.TotalArticles)

	// 统计分类数
	s.db.Model(&model.Category{}).Count(&stats.TotalCategories)

	// 统计代理数
	s.db.Model(&model.ProxyServer{}).Count(&stats.TotalProxies)

	// 统计反馈数
	s.db.Model(&model.Feedback{}).Count(&stats.TotalFeedbacks)

	return &stats, nil
}

// FetchNewProxies 获取新代理
func (s *AdminService) FetchNewProxies() error {
	// 这里应该实现代理获取逻辑
	// 暂时返回nil
	return nil
}

// CleanupExpiredData 清理过期数据
func (s *AdminService) CleanupExpiredData() error {
	// 这里应该实现数据清理逻辑
	// 暂时返回nil
	return nil
}
