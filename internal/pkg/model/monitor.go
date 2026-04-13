package model

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Monitor note type constants
const (
	NoteTypeImage = "image"
	NoteTypeVideo = "video"
)

// Monitor briefing type constants
const (
	BriefingTypeDaily  = "daily"
	BriefingTypeWeekly = "weekly"
)

// MonitorBlogger 监控博主（显式字段 + 软删除，确保 JSON tag 一致性）
type MonitorBlogger struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at"`
	UserID              uint       `gorm:"not null;uniqueIndex:uk_user_blogger" json:"user_id"`
	XhsUserID           string     `gorm:"size:100;not null;uniqueIndex:uk_user_blogger" json:"xhs_user_id"`
	Nickname            string     `gorm:"size:200" json:"nickname"`
	AvatarURL           string     `gorm:"size:500" json:"avatar_url"`
	Bio                 string     `gorm:"type:text" json:"bio"`
	Followers           uint       `gorm:"default:0" json:"followers"`
	Category            string     `gorm:"size:100" json:"category"`
	IsActive            bool       `gorm:"default:true;index:idx_blogger_active" json:"is_active"`
	CheckError          string     `gorm:"size:500" json:"check_error"`
	ConsecutiveFailures uint       `gorm:"default:0" json:"consecutive_failures"`
	LastCheckAt         *time.Time `json:"last_check_at"`
	LastNoteAt          *time.Time `json:"last_note_at"`
	NextCheckAt         *time.Time `json:"next_check_at"`
}

func (MonitorBlogger) TableName() string { return "monitor_blogger" }

// MonitorNote 抓取的笔记 — 显式字段，无软删除
type MonitorNote struct {
	ID          uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      uint           `gorm:"not null;uniqueIndex:uk_user_note" json:"user_id"`
	BloggerID   uint           `gorm:"not null;index:idx_note_blogger" json:"blogger_id"`
	XhsNoteID   string         `gorm:"size:100;not null;uniqueIndex:uk_user_note" json:"xhs_note_id"`
	Title       string         `gorm:"size:500" json:"title"`
	Content     string         `gorm:"type:text" json:"content"`
	NoteType    string         `gorm:"size:20;default:image" json:"note_type"`
	Tags        datatypes.JSON `json:"tags"`
	Likes       uint           `gorm:"default:0" json:"likes"`
	Comments    uint           `gorm:"default:0" json:"comments"`
	Collects    uint           `gorm:"default:0" json:"collects"`
	Shares      uint           `gorm:"default:0" json:"shares"`
	Images      datatypes.JSON `json:"images"`
	VideoURL    string         `gorm:"size:1000" json:"video_url"`
	Transcript  string         `gorm:"type:text" json:"transcript"`
	AISummary   string         `gorm:"type:text" json:"ai_summary"`
	AITopics    datatypes.JSON `json:"ai_topics"`
	AICategory  string         `gorm:"size:100" json:"ai_category"`
	PublishedAt *time.Time     `gorm:"index:idx_note_published" json:"published_at"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func (MonitorNote) TableName() string { return "monitor_note" }

// MonitorBriefing 简报 — 显式字段
type MonitorBriefing struct {
	ID          uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      uint           `gorm:"not null;uniqueIndex:uk_briefing_user_type_period" json:"user_id"`
	Type        string         `gorm:"size:20;not null;uniqueIndex:uk_briefing_user_type_period" json:"type"`
	Title       string         `gorm:"size:200" json:"title"`
	Content     string         `gorm:"type:text" json:"content"`
	NoteCount   uint           `gorm:"default:0" json:"note_count"`
	Highlights  datatypes.JSON `json:"highlights"`
	Trends      datatypes.JSON `json:"trends"`
	PeriodStart *time.Time     `gorm:"type:date" json:"period_start"`
	PeriodEnd   *time.Time     `gorm:"type:date;uniqueIndex:uk_briefing_user_type_period" json:"period_end"`
	FeishuSent  bool           `gorm:"default:false" json:"feishu_sent"`
	CreatedAt   time.Time      `json:"created_at"`
}

func (MonitorBriefing) TableName() string { return "monitor_briefing" }

// MonitorConfig 监控配置 — 显式字段
type MonitorConfig struct {
	ID                  uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID              uint           `gorm:"not null;uniqueIndex" json:"user_id"`
	CrawlCron           string         `gorm:"size:50;default:0 */8 * * *" json:"crawl_cron"`
	BriefingCron        string         `gorm:"size:50;default:0 20 * * *" json:"briefing_cron"`
	BriefingType        string         `gorm:"size:20;default:daily" json:"briefing_type"`
	FeishuWebhook       string         `gorm:"size:500" json:"feishu_webhook"`
	FeishuBitableConfig datatypes.JSON `json:"feishu_bitable_config"`
	NotifyOnUpdate      bool           `gorm:"default:true" json:"notify_on_update"`
	XhsCookies          string         `gorm:"type:text" json:"-"`              // stored but never sent to frontend
	XhsNickname         string         `gorm:"size:200" json:"xhs_nickname"`    // display name of bound XHS account
	XhsUserID           string         `gorm:"size:100" json:"xhs_user_id"`     // XHS user ID of bound account
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

func (MonitorConfig) TableName() string { return "monitor_config" }
