package store

import (
	"fmt"
	"time"

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
	GetDashboardStats() (*DashboardStats, error)
	GetUserGrowthTrend(period string) ([]GrowthTrendItem, error)
	GetBookGrowthTrend(period string) ([]GrowthTrendItem, error)
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

// DashboardStats 仪表板统计信息
type DashboardStats struct {
	TodayNewUsers int64 `json:"today_new_users"` // 今日新增用户
	TotalUsers    int64 `json:"total_users"`     // 用户总数
	TodayNewBooks int64 `json:"today_new_books"` // 今日新增笔记
	TotalBooks    int64 `json:"total_books"`     // 笔记总数
}

// GrowthTrendItem 增长趋势项
type GrowthTrendItem struct {
	Date  string `json:"date"`  // 日期，格式：YYYY-MM-DD 或 MM-DD
	Count int64  `json:"count"` // 数量
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
	if err := query.Offset(offset).Limit(limit).Order("id ASC").Find(&users).Error; err != nil {
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

// GetDashboardStats 获取仪表板统计信息
func (s *AdminStore) GetDashboardStats() (*DashboardStats, error) {
	var stats DashboardStats

	// 获取今日开始时间（00:00:00）
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// 统计用户总数
	if err := s.db.Model(&model.User{}).Count(&stats.TotalUsers).Error; err != nil {
		return nil, err
	}

	// 统计今日新增用户
	if err := s.db.Model(&model.User{}).
		Where("created_at >= ?", todayStart).
		Count(&stats.TodayNewUsers).Error; err != nil {
		return nil, err
	}

	// 统计笔记总数
	if err := s.db.Model(&model.BookM{}).Count(&stats.TotalBooks).Error; err != nil {
		return nil, err
	}

	// 统计今日新增笔记
	if err := s.db.Model(&model.BookM{}).
		Where("created_at >= ?", todayStart).
		Count(&stats.TodayNewBooks).Error; err != nil {
		return nil, err
	}

	return &stats, nil
}

// GetUserGrowthTrend 获取用户增长趋势
func (s *AdminStore) GetUserGrowthTrend(period string) ([]GrowthTrendItem, error) {
	now := time.Now()
	var startTime time.Time
	var endTime time.Time
	var dateFormat string
	var incrementFunc func(time.Time) time.Time
	var displayFormatFunc func(time.Time) string

	switch period {
	case "week": // 本周
		// 获取本周一
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7 // 周日算作第7天
		}
		startTime = time.Date(now.Year(), now.Month(), now.Day()-weekday+1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01-02" // 按日统计
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d日", t.Day()) }
	case "month": // 本月
		startTime = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01-02" // 按日统计
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d日", t.Day()) }
	case "year": // 今年
		startTime = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01" // 按月统计
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d月", int(t.Month())) }
	default:
		// 默认本月
		startTime = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01-02"
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d日", t.Day()) }
	}

	// 使用 SQL 查询按日期分组统计
	type Result struct {
		Date  string
		Count int64
	}

	var results []Result
	// 使用子查询来避免 ONLY_FULL_GROUP_BY 错误
	query := `
		SELECT date, COUNT(*) as count
		FROM (
			SELECT DATE_FORMAT(created_at, ?) as date
			FROM user
			WHERE created_at >= ? AND created_at <= ? AND deleted_at IS NULL
		) as date_list
		GROUP BY date
		ORDER BY date ASC
	`

	if err := s.db.Raw(query, dateFormat, startTime, endTime).Scan(&results).Error; err != nil {
		return nil, err
	}

	// 创建日期到数量的映射
	dataMap := make(map[string]int64)
	for _, r := range results {
		dataMap[r.Date] = r.Count
	}

	// 填充所有日期，即使没有数据也返回 0
	items := make([]GrowthTrendItem, 0)
	current := startTime
	for !current.After(endTime) {
		// 根据 period 生成日期字符串用于匹配数据库结果
		var dateStr string
		if period == "year" {
			dateStr = current.Format("01") // 月份格式：01, 02, ...
		} else {
			dateStr = current.Format("01-02") // 日期格式：01-02, 01-03, ...
		}

		count := int64(0)
		if val, exists := dataMap[dateStr]; exists {
			count = val
		}

		items = append(items, GrowthTrendItem{
			Date:  displayFormatFunc(current),
			Count: count,
		})

		current = incrementFunc(current)
	}

	return items, nil
}

// GetBookGrowthTrend 获取笔记增长趋势
func (s *AdminStore) GetBookGrowthTrend(period string) ([]GrowthTrendItem, error) {
	now := time.Now()
	var startTime time.Time
	var endTime time.Time
	var dateFormat string
	var incrementFunc func(time.Time) time.Time
	var displayFormatFunc func(time.Time) string

	switch period {
	case "week": // 本周
		// 获取本周一
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7 // 周日算作第7天
		}
		startTime = time.Date(now.Year(), now.Month(), now.Day()-weekday+1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01-02" // 按日统计
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d日", t.Day()) }
	case "month": // 本月
		startTime = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01-02" // 按日统计
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d日", t.Day()) }
	case "year": // 今年
		startTime = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01" // 按月统计
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d月", int(t.Month())) }
	default:
		// 默认本月
		startTime = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		endTime = now
		dateFormat = "01-02"
		incrementFunc = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
		displayFormatFunc = func(t time.Time) string { return fmt.Sprintf("%d日", t.Day()) }
	}

	// 使用 SQL 查询按日期分组统计
	type Result struct {
		Date  string
		Count int64
	}

	var results []Result
	// 使用子查询来避免 ONLY_FULL_GROUP_BY 错误
	query := `
		SELECT date, COUNT(*) as count
		FROM (
			SELECT DATE_FORMAT(created_at, ?) as date
			FROM book
			WHERE created_at >= ? AND created_at <= ? AND deleted_at IS NULL
		) as date_list
		GROUP BY date
		ORDER BY date ASC
	`

	if err := s.db.Raw(query, dateFormat, startTime, endTime).Scan(&results).Error; err != nil {
		return nil, err
	}

	// 创建日期到数量的映射
	dataMap := make(map[string]int64)
	for _, r := range results {
		dataMap[r.Date] = r.Count
	}

	// 填充所有日期，即使没有数据也返回 0
	items := make([]GrowthTrendItem, 0)
	current := startTime
	for !current.After(endTime) {
		// 根据 period 生成日期字符串用于匹配数据库结果
		var dateStr string
		if period == "year" {
			dateStr = current.Format("01") // 月份格式：01, 02, ...
		} else {
			dateStr = current.Format("01-02") // 日期格式：01-02, 01-03, ...
		}

		count := int64(0)
		if val, exists := dataMap[dateStr]; exists {
			count = val
		}

		items = append(items, GrowthTrendItem{
			Date:  displayFormatFunc(current),
			Count: count,
		})

		current = incrementFunc(current)
	}

	return items, nil
}
