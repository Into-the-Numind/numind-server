package monitor

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/llm"
	"numind-server/internal/pkg/model"
)

// IMonitorBiz defines the monitor business logic interface
type IMonitorBiz interface {
	// Blogger
	AddBlogger(ctx context.Context, userID uint, xhsUserID string) (*model.MonitorBlogger, error)
	GetBlogger(ctx context.Context, userID, bloggerID uint) (*model.MonitorBlogger, error)
	ListBloggers(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBlogger, int64, error)
	UpdateBlogger(ctx context.Context, userID, bloggerID uint, category *string, isActive *bool) error
	DeleteBlogger(ctx context.Context, userID, bloggerID uint) error

	// Crawl triggers
	CheckBlogger(ctx context.Context, userID, bloggerID uint) error
	CheckBatch(ctx context.Context, userID uint, bloggerIDs []uint) error

	// Notes
	GetNote(ctx context.Context, userID, noteID uint) (*model.MonitorNote, error)
	ListNotes(ctx context.Context, userID uint, bloggerID *uint, dateFrom, dateTo *time.Time, sortBy string, offset, limit int) ([]model.MonitorNote, int64, error)
	AnalyzeNote(ctx context.Context, userID, noteID uint) error

	// Briefings
	GetBriefing(ctx context.Context, userID, briefingID uint) (*model.MonitorBriefing, error)
	ListBriefings(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBriefing, int64, error)
	GenerateBriefing(ctx context.Context, userID uint) (*model.MonitorBriefing, error)

	// Config
	GetConfig(ctx context.Context, userID uint) (*model.MonitorConfig, error)
	UpdateConfig(ctx context.Context, userID uint, cfg *model.MonitorConfig) error

	// XHS Account Binding (QR Login)
	CreateQRLogin(ctx context.Context, userID uint) (qrID, code, qrURL string, err error)
	CheckQRStatus(ctx context.Context, userID uint, qrID string) (status int, message string, err error)
	CompleteQRLogin(ctx context.Context, userID uint, qrID string) error
	GetXhsBindStatus(ctx context.Context, userID uint) (bound bool, nickname, xhsUserID string, err error)
	UnbindXhs(ctx context.Context, userID uint) error

	// Stats
	GetStats(ctx context.Context, userID uint) (*store.MonitorStats, error)

	// Permission
	CheckPermission(ctx context.Context, userID uint) (bool, error)

	// Scheduler lifecycle
	StartScheduler(ctx context.Context) error
	StopScheduler()
	RefreshUserSchedule(userID uint, cfg *model.MonitorConfig) error
}

// MonitorBiz implements IMonitorBiz
type MonitorBiz struct {
	store     store.IStore
	llm       *llm.DMXAPIClient
	cooldown  *CooldownManager
	scheduler *MonitorScheduler
}

// Compile-time interface check
var _ IMonitorBiz = (*MonitorBiz)(nil)

// NewMonitorBiz 创建 MonitorBiz 实例
func NewMonitorBiz(s store.IStore, llmClient *llm.DMXAPIClient, cooldown *CooldownManager) *MonitorBiz {
	return &MonitorBiz{
		store:    s,
		llm:      llmClient,
		cooldown: cooldown,
	}
}

// ========== Note read methods ==========

// GetNote 获取单条笔记（含所有权校验）
func (mb *MonitorBiz) GetNote(ctx context.Context, userID, noteID uint) (*model.MonitorNote, error) {
	note, err := mb.store.Monitor().GetNote(ctx, noteID)
	if err != nil {
		return nil, fmt.Errorf("GetNote: %w", err)
	}
	if note.UserID != userID {
		return nil, errno.ErrForbidden
	}
	return note, nil
}

// ListNotes 分页查询笔记
func (mb *MonitorBiz) ListNotes(ctx context.Context, userID uint, bloggerID *uint, dateFrom, dateTo *time.Time, sortBy string, offset, limit int) ([]model.MonitorNote, int64, error) {
	return mb.store.Monitor().ListNotes(ctx, userID, bloggerID, dateFrom, dateTo, sortBy, offset, limit)
}

// ========== Briefing read methods ==========

// GetBriefing 获取单条简报（含所有权校验）
func (mb *MonitorBiz) GetBriefing(ctx context.Context, userID, briefingID uint) (*model.MonitorBriefing, error) {
	briefing, err := mb.store.Monitor().GetBriefing(ctx, briefingID)
	if err != nil {
		return nil, fmt.Errorf("GetBriefing: %w", err)
	}
	if briefing.UserID != userID {
		return nil, errno.ErrForbidden
	}
	return briefing, nil
}

// ListBriefings 分页查询简报
func (mb *MonitorBiz) ListBriefings(ctx context.Context, userID uint, offset, limit int) ([]model.MonitorBriefing, int64, error) {
	return mb.store.Monitor().ListBriefings(ctx, userID, offset, limit)
}

// ========== Config methods ==========

// GetConfig 获取用户的监控配置
func (mb *MonitorBiz) GetConfig(ctx context.Context, userID uint) (*model.MonitorConfig, error) {
	return mb.store.Monitor().GetConfig(ctx, userID)
}

// UpdateConfig 更新用户的监控配置
func (mb *MonitorBiz) UpdateConfig(ctx context.Context, userID uint, cfg *model.MonitorConfig) error {
	cfg.UserID = userID
	if err := mb.store.Monitor().UpsertConfig(ctx, cfg); err != nil {
		return fmt.Errorf("UpdateConfig: %w", err)
	}
	// Refresh cron scheduler — stub for now, Task 8 will implement
	return mb.RefreshUserSchedule(userID, cfg)
}

// ========== Stats & Permission ==========

// GetStats 获取用户的监控统计概览
func (mb *MonitorBiz) GetStats(ctx context.Context, userID uint) (*store.MonitorStats, error) {
	return mb.store.Monitor().GetStats(ctx, userID)
}

// CheckPermission 检查用户是否有监控功能权限
// 主用户（parent_user_id 为 NULL）自动拥有全部功能权限；
// 子用户需要显式授权 content_monitor 才有权限。
// 此端点注册在 FeaturePermission 中间件之外，因此需要在 biz 层自行校验。
func (mb *MonitorBiz) CheckPermission(ctx context.Context, userID uint) (bool, error) {
	has, err := mb.store.Customers().HasFeaturePermission(ctx, userID, model.FeatureKeyContentMonitor)
	if err != nil {
		return false, fmt.Errorf("CheckPermission: %w", err)
	}
	return has, nil
}

// ========== Crawl triggers ==========

// CheckBlogger 触发单个博主的爬取检查
// 验证所有权，检查冷却时间，然后调用 CrawlBloggers。
func (mb *MonitorBiz) CheckBlogger(ctx context.Context, userID, bloggerID uint) error {
	// 验证博主归属
	blogger, err := mb.store.Monitor().GetBlogger(ctx, bloggerID)
	if err != nil {
		return fmt.Errorf("CheckBlogger: %w", err)
	}
	if blogger.UserID != userID {
		return errno.ErrForbidden
	}

	// 检查冷却时间
	if !mb.cooldown.CanCheck(userID) {
		return errno.ErrCheckCooldown
	}

	// 记录冷却时间
	mb.cooldown.RecordCheck(userID)

	return mb.CrawlBloggers(ctx, userID, []uint{bloggerID})
}

// CheckBatch 批量触发博主的爬取检查
// 验证参数（1-50 个博主，全部属于当前用户且未删除），检查冷却时间，然后调用 CrawlBloggers。
func (mb *MonitorBiz) CheckBatch(ctx context.Context, userID uint, bloggerIDs []uint) error {
	// 验证数量
	if len(bloggerIDs) == 0 || len(bloggerIDs) > 50 {
		return fmt.Errorf("CheckBatch: blogger count must be between 1 and 50, got %d", len(bloggerIDs))
	}

	// 验证所有博主归属（单次 IN 查询代替 O(n) 循环）
	bloggers, err := mb.store.Monitor().ListBloggersByIDs(ctx, bloggerIDs)
	if err != nil {
		return fmt.Errorf("CheckBatch: %w", err)
	}
	if len(bloggers) != len(bloggerIDs) {
		return errno.ErrPageNotFound
	}
	for _, b := range bloggers {
		if b.UserID != userID {
			return errno.ErrForbidden
		}
	}

	// 检查冷却时间
	if !mb.cooldown.CanCheck(userID) {
		return errno.ErrCheckCooldown
	}

	// 记录冷却时间
	mb.cooldown.RecordCheck(userID)

	return mb.CrawlBloggers(ctx, userID, bloggerIDs)
}

// AnalyzeNote 触发单条笔记的 AI 分析
// 验证所有权，检查冷却时间，注入计费上下文，调用 AnalyzeSingleNote。
func (mb *MonitorBiz) AnalyzeNote(ctx context.Context, userID, noteID uint) error {
	// 1. 获取笔记并验证所有权
	note, err := mb.store.Monitor().GetNote(ctx, noteID)
	if err != nil {
		return fmt.Errorf("AnalyzeNote: %w", err)
	}
	if note.UserID != userID {
		return errno.ErrForbidden
	}

	// 2. 检查冷却时间
	if !mb.cooldown.CanAnalyze(noteID) {
		return errno.ErrAnalyzeCooldown
	}

	// 3. 创建 Langfuse trace
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "monitor-analyze-note",
		langfuse.WithUserID(userID),
		langfuse.WithTraceInput(map[string]interface{}{
			"note_id": noteID,
			"title":   note.Title,
		}),
		langfuse.WithTraceTags("monitor"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 4. 注入计费上下文
	ctx = billing.WithBilling(ctx, userID, "monitor_analyze")

	// 5. 执行分析
	if err := mb.AnalyzeSingleNote(ctx, note); err != nil {
		return fmt.Errorf("AnalyzeNote: %w", err)
	}

	// 6. 记录冷却时间（成功后才记录）
	mb.cooldown.RecordAnalyze(noteID)

	return nil
}

// GenerateBriefing 生成简报（默认使用用户配置的简报类型，若无配置则生成日报）
func (mb *MonitorBiz) GenerateBriefing(ctx context.Context, userID uint) (*model.MonitorBriefing, error) {
	// 读取用户配置确定简报类型
	briefingType := model.BriefingTypeDaily
	cfg, err := mb.store.Monitor().GetConfig(ctx, userID)
	if err == nil && cfg != nil && cfg.BriefingType != "" {
		briefingType = cfg.BriefingType
	}

	return mb.GenerateUserBriefing(ctx, userID, briefingType)
}

// StartScheduler 启动 cron 调度器，加载所有用户的活跃配置并注册定时任务
func (mb *MonitorBiz) StartScheduler(ctx context.Context) error {
	mb.scheduler = NewMonitorScheduler(mb)
	return mb.scheduler.Start(ctx)
}

// StopScheduler 优雅停止 cron 调度器
func (mb *MonitorBiz) StopScheduler() {
	if mb.scheduler != nil {
		mb.scheduler.Stop()
	}
}

// RefreshUserSchedule 刷新指定用户的调度计划（配置变更后调用）
func (mb *MonitorBiz) RefreshUserSchedule(userID uint, cfg *model.MonitorConfig) error {
	if mb.scheduler == nil {
		return nil
	}
	return mb.scheduler.RefreshUser(userID, cfg.CrawlCron, cfg.BriefingCron, cfg.BriefingType)
}
