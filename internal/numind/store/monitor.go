package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// MonitorStats 监控统计概览
type MonitorStats struct {
	TotalBloggers  int64                  `json:"total_bloggers"`
	ActiveBloggers int64                  `json:"active_bloggers"`
	TotalNotes     int64                  `json:"total_notes"`
	NotesThisWeek  int64                  `json:"notes_this_week"`
	TotalBriefings int64                  `json:"total_briefings"`
	LatestBriefing *model.MonitorBriefing `json:"latest_briefing"`
}

// IMonitorStore 定义监控模块的数据库操作接口
type IMonitorStore interface {
	// Blogger
	CreateBlogger(ctx context.Context, b *model.MonitorBlogger) error
	GetBlogger(ctx context.Context, id uint) (*model.MonitorBlogger, error)
	ListBloggers(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBlogger, int64, error)
	UpdateBlogger(ctx context.Context, b *model.MonitorBlogger) error
	DeleteBlogger(ctx context.Context, id uint) error // soft delete

	// Note
	CreateNote(ctx context.Context, n *model.MonitorNote) error
	GetNote(ctx context.Context, id uint) (*model.MonitorNote, error)
	ListNotes(ctx context.Context, userID uint, bloggerID *uint, dateFrom, dateTo *time.Time, sortBy string, offset, limit int) ([]model.MonitorNote, int64, error)
	UpdateNote(ctx context.Context, n *model.MonitorNote) error
	GetNoteByXhsID(ctx context.Context, userID uint, xhsNoteID string) (*model.MonitorNote, error)

	// Briefing
	CreateBriefing(ctx context.Context, b *model.MonitorBriefing) error
	GetBriefing(ctx context.Context, id uint) (*model.MonitorBriefing, error)
	ListBriefings(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBriefing, int64, error)
	UpdateBriefingSent(ctx context.Context, id uint, sent bool) error

	// Config
	GetConfig(ctx context.Context, userID uint) (*model.MonitorConfig, error)
	UpsertConfig(ctx context.Context, c *model.MonitorConfig) error
	ListAllActiveConfigs(ctx context.Context) ([]model.MonitorConfig, error)

	// Stats
	GetStats(ctx context.Context, userID uint) (*MonitorStats, error)

	// Batch queries (for scheduler)
	ListActiveBloggersByUser(ctx context.Context, userID uint) ([]model.MonitorBlogger, error)
	ListNotesByPeriod(ctx context.Context, userID uint, from, to time.Time) ([]model.MonitorNote, error)
}

type monitorStore struct {
	db *gorm.DB
}

var _ IMonitorStore = (*monitorStore)(nil)

// NewMonitorStore 创建一个 IMonitorStore 实例
func NewMonitorStore(db *gorm.DB) IMonitorStore {
	return &monitorStore{db: db}
}

// ========== Blogger ==========

// CreateBlogger 创建监控博主，MySQL 1062 duplicate key → ErrBloggerAlreadyMonitored
func (s *monitorStore) CreateBlogger(ctx context.Context, b *model.MonitorBlogger) error {
	if err := s.db.WithContext(ctx).Create(b).Error; err != nil {
		var mysqlErr *mysql.MySQLError
		if ok := errors.As(err, &mysqlErr); ok && mysqlErr.Number == 1062 {
			return errno.ErrBloggerAlreadyMonitored
		}
		return fmt.Errorf("CreateBlogger: %w", err)
	}
	return nil
}

// GetBlogger 获取单个博主
func (s *monitorStore) GetBlogger(ctx context.Context, id uint) (*model.MonitorBlogger, error) {
	var blogger model.MonitorBlogger
	if err := s.db.WithContext(ctx).First(&blogger, id).Error; err != nil {
		return nil, fmt.Errorf("GetBlogger: %w", err)
	}
	return &blogger, nil
}

// ListBloggers 分页查询用户的监控博主列表
func (s *monitorStore) ListBloggers(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBlogger, int64, error) {
	var list []model.MonitorBlogger
	var total int64

	query := s.db.WithContext(ctx).Model(&model.MonitorBlogger{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("ListBloggers count: %w", err)
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("ListBloggers find: %w", err)
	}

	return list, total, nil
}

// UpdateBlogger 更新博主信息
func (s *monitorStore) UpdateBlogger(ctx context.Context, b *model.MonitorBlogger) error {
	if err := s.db.WithContext(ctx).Save(b).Error; err != nil {
		return fmt.Errorf("UpdateBlogger: %w", err)
	}
	return nil
}

// DeleteBlogger 软删除博主（gorm.Model 自带 DeletedAt）
func (s *monitorStore) DeleteBlogger(ctx context.Context, id uint) error {
	if err := s.db.WithContext(ctx).Delete(&model.MonitorBlogger{}, id).Error; err != nil {
		return fmt.Errorf("DeleteBlogger: %w", err)
	}
	return nil
}

// ========== Note ==========

// CreateNote 创建笔记
func (s *monitorStore) CreateNote(ctx context.Context, n *model.MonitorNote) error {
	if err := s.db.WithContext(ctx).Create(n).Error; err != nil {
		return fmt.Errorf("CreateNote: %w", err)
	}
	return nil
}

// GetNote 获取单条笔记
func (s *monitorStore) GetNote(ctx context.Context, id uint) (*model.MonitorNote, error) {
	var note model.MonitorNote
	if err := s.db.WithContext(ctx).First(&note, id).Error; err != nil {
		return nil, fmt.Errorf("GetNote: %w", err)
	}
	return &note, nil
}

// ListNotes 分页查询笔记，支持按博主、日期范围过滤和自定义排序
func (s *monitorStore) ListNotes(ctx context.Context, userID uint, bloggerID *uint, dateFrom, dateTo *time.Time, sortBy string, offset, limit int) ([]model.MonitorNote, int64, error) {
	var list []model.MonitorNote
	var total int64

	query := s.db.WithContext(ctx).Model(&model.MonitorNote{}).Where("user_id = ?", userID)

	if bloggerID != nil {
		query = query.Where("blogger_id = ?", *bloggerID)
	}
	if dateFrom != nil {
		query = query.Where("published_at >= ?", *dateFrom)
	}
	if dateTo != nil {
		query = query.Where("published_at <= ?", *dateTo)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("ListNotes count: %w", err)
	}

	// 允许的排序字段白名单，防止 SQL 注入
	orderClause := "published_at DESC"
	if sortBy != "" {
		allowed := map[string]bool{
			"published_at":     true,
			"likes":            true,
			"comments":         true,
			"collects":         true,
			"shares":           true,
			"created_at":       true,
			"published_at asc": true,
			"likes desc":       true,
			"comments desc":    true,
			"collects desc":    true,
		}
		lower := strings.ToLower(sortBy)
		if allowed[lower] {
			orderClause = sortBy
		}
	}

	if err := query.Order(orderClause).Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("ListNotes find: %w", err)
	}

	return list, total, nil
}

// UpdateNote 更新笔记
func (s *monitorStore) UpdateNote(ctx context.Context, n *model.MonitorNote) error {
	if err := s.db.WithContext(ctx).Save(n).Error; err != nil {
		return fmt.Errorf("UpdateNote: %w", err)
	}
	return nil
}

// GetNoteByXhsID 根据用户ID和小红书笔记ID查询笔记（用于去重）
func (s *monitorStore) GetNoteByXhsID(ctx context.Context, userID uint, xhsNoteID string) (*model.MonitorNote, error) {
	var note model.MonitorNote
	if err := s.db.WithContext(ctx).Where("user_id = ? AND xhs_note_id = ?", userID, xhsNoteID).First(&note).Error; err != nil {
		return nil, fmt.Errorf("GetNoteByXhsID: %w", err)
	}
	return &note, nil
}

// ========== Briefing ==========

// CreateBriefing 创建简报
func (s *monitorStore) CreateBriefing(ctx context.Context, b *model.MonitorBriefing) error {
	if err := s.db.WithContext(ctx).Create(b).Error; err != nil {
		return fmt.Errorf("CreateBriefing: %w", err)
	}
	return nil
}

// GetBriefing 获取单条简报
func (s *monitorStore) GetBriefing(ctx context.Context, id uint) (*model.MonitorBriefing, error) {
	var briefing model.MonitorBriefing
	if err := s.db.WithContext(ctx).First(&briefing, id).Error; err != nil {
		return nil, fmt.Errorf("GetBriefing: %w", err)
	}
	return &briefing, nil
}

// ListBriefings 分页查询用户的简报列表
func (s *monitorStore) ListBriefings(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBriefing, int64, error) {
	var list []model.MonitorBriefing
	var total int64

	query := s.db.WithContext(ctx).Model(&model.MonitorBriefing{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("ListBriefings count: %w", err)
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("ListBriefings find: %w", err)
	}

	return list, total, nil
}

// UpdateBriefingSent 更新简报的飞书发送状态
func (s *monitorStore) UpdateBriefingSent(ctx context.Context, id uint, sent bool) error {
	if err := s.db.WithContext(ctx).Model(&model.MonitorBriefing{}).Where("id = ?", id).Update("feishu_sent", sent).Error; err != nil {
		return fmt.Errorf("UpdateBriefingSent: %w", err)
	}
	return nil
}

// ========== Config ==========

// GetConfig 获取用户的监控配置
func (s *monitorStore) GetConfig(ctx context.Context, userID uint) (*model.MonitorConfig, error) {
	var config model.MonitorConfig
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&config).Error; err != nil {
		return nil, fmt.Errorf("GetConfig: %w", err)
	}
	return &config, nil
}

// UpsertConfig 创建或更新用户的监控配置
func (s *monitorStore) UpsertConfig(ctx context.Context, c *model.MonitorConfig) error {
	if err := s.db.WithContext(ctx).Where("user_id = ?", c.UserID).Assign(*c).FirstOrCreate(c).Error; err != nil {
		return fmt.Errorf("UpsertConfig: %w", err)
	}
	return nil
}

// ListAllActiveConfigs 获取所有监控配置（调度器启动时加载）
func (s *monitorStore) ListAllActiveConfigs(ctx context.Context) ([]model.MonitorConfig, error) {
	var configs []model.MonitorConfig
	if err := s.db.WithContext(ctx).Find(&configs).Error; err != nil {
		return nil, fmt.Errorf("ListAllActiveConfigs: %w", err)
	}
	return configs, nil
}

// ========== Stats ==========

// GetStats 获取用户的监控统计概览
func (s *monitorStore) GetStats(ctx context.Context, userID uint) (*MonitorStats, error) {
	stats := &MonitorStats{}

	db := s.db.WithContext(ctx)

	// 博主总数
	if err := db.Model(&model.MonitorBlogger{}).Where("user_id = ?", userID).Count(&stats.TotalBloggers).Error; err != nil {
		return nil, fmt.Errorf("GetStats totalBloggers: %w", err)
	}

	// 活跃博主数
	if err := db.Model(&model.MonitorBlogger{}).Where("user_id = ? AND is_active = ?", userID, true).Count(&stats.ActiveBloggers).Error; err != nil {
		return nil, fmt.Errorf("GetStats activeBloggers: %w", err)
	}

	// 笔记总数
	if err := db.Model(&model.MonitorNote{}).Where("user_id = ?", userID).Count(&stats.TotalNotes).Error; err != nil {
		return nil, fmt.Errorf("GetStats totalNotes: %w", err)
	}

	// 本周笔记数
	weekAgo := time.Now().AddDate(0, 0, -7)
	if err := db.Model(&model.MonitorNote{}).Where("user_id = ? AND created_at >= ?", userID, weekAgo).Count(&stats.NotesThisWeek).Error; err != nil {
		return nil, fmt.Errorf("GetStats notesThisWeek: %w", err)
	}

	// 简报总数
	if err := db.Model(&model.MonitorBriefing{}).Where("user_id = ?", userID).Count(&stats.TotalBriefings).Error; err != nil {
		return nil, fmt.Errorf("GetStats totalBriefings: %w", err)
	}

	// 最新简报
	var latestBriefing model.MonitorBriefing
	err := db.Where("user_id = ?", userID).Order("created_at DESC").First(&latestBriefing).Error
	if err == nil {
		stats.LatestBriefing = &latestBriefing
	} else if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("GetStats latestBriefing: %w", err)
	}

	return stats, nil
}

// ========== Batch queries (for scheduler) ==========

// ListActiveBloggersByUser 获取用户的所有活跃博主（调度器用）
func (s *monitorStore) ListActiveBloggersByUser(ctx context.Context, userID uint) ([]model.MonitorBlogger, error) {
	var bloggers []model.MonitorBlogger
	if err := s.db.WithContext(ctx).Where("user_id = ? AND is_active = ?", userID, true).Find(&bloggers).Error; err != nil {
		return nil, fmt.Errorf("ListActiveBloggersByUser: %w", err)
	}
	return bloggers, nil
}

// ListNotesByPeriod 查询用户在指定时间段的笔记（简报生成用）
func (s *monitorStore) ListNotesByPeriod(ctx context.Context, userID uint, from, to time.Time) ([]model.MonitorNote, error) {
	var notes []model.MonitorNote
	if err := s.db.WithContext(ctx).Where("user_id = ? AND published_at BETWEEN ? AND ?", userID, from, to).Find(&notes).Error; err != nil {
		return nil, fmt.Errorf("ListNotesByPeriod: %w", err)
	}
	return notes, nil
}
