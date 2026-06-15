package model

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	// 公告类型
	AnnouncementTypePlain  = "plain"  // 纯公告
	AnnouncementTypeSurvey = "survey" // 问卷

	// 公告状态
	AnnouncementStatusDraft     = "draft"     // 草稿
	AnnouncementStatusPublished = "published" // 已发布
	AnnouncementStatusArchived  = "archived"  // 已归档

	// 受众扩展位（V1 只用 all）
	AnnouncementAudienceAll = "all"

	// 问卷题型
	SurveyQuestionTypeSingle = "single" // 单选
	SurveyQuestionTypeMulti  = "multi"  // 多选
	SurveyQuestionTypeRating = "rating" // 评分
	SurveyQuestionTypeText   = "text"   // 开放文本

	// rating 风格
	SurveyRatingStyleStar = "star" // 星级
	SurveyRatingStyleNPS  = "nps"  // NPS
)

// Announcement 公告/问卷主表（notification-center §1.1）。
// is_important 带 gorm:"default:0"，普通 bool 零值 false 不触发 default-bool 坑
// （default:0 与零值一致），但 Create 路径仍建议遵循 database.md §6 的 fixup 模式。
// 唯一带软删除（gorm.DeletedAt + index）的表。
type Announcement struct {
	ID          uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Type        string         `gorm:"size:16;not null;default:'plain';index:idx_ann_type" json:"type"`
	Title       string         `gorm:"size:200;not null" json:"title"`
	Content     string         `gorm:"type:longtext;not null" json:"content"`
	IsImportant bool           `gorm:"type:tinyint(1);not null;default:0" json:"is_important"`
	Audience    string         `gorm:"size:32;not null;default:'all'" json:"audience"`
	Status      string         `gorm:"size:16;not null;default:'draft';index:idx_ann_status_pub,priority:1" json:"status"`
	PublishedAt *time.Time     `gorm:"type:datetime;index:idx_ann_status_pub,priority:2" json:"published_at"`
	ExpiresAt   *time.Time     `gorm:"type:datetime" json:"expires_at"`
	CreatedBy   uint           `gorm:"type:int unsigned;not null" json:"created_by"`
	CreatedAt   time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoUpdateTime" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index:idx_ann_deleted" json:"deleted_at"`
}

// TableName returns the table name for Announcement.
func (Announcement) TableName() string { return "announcement" }

// AnnouncementRead 已读回执（notification-center §1.2）。
// UNIQUE(announcement_id, user_id) 保证一人一条；read_at 保留首次已读时间（幂等）。
// 无软删除。
type AnnouncementRead struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	AnnouncementID uint64    `gorm:"type:bigint unsigned;not null;uniqueIndex:uk_annread,priority:1" json:"announcement_id"`
	UserID         uint      `gorm:"type:int unsigned;not null;uniqueIndex:uk_annread,priority:2;index:idx_annread_user" json:"user_id"`
	ReadAt         time.Time `gorm:"type:datetime;not null" json:"read_at"`
	CreatedAt      time.Time `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
}

// TableName returns the table name for AnnouncementRead.
func (AnnouncementRead) TableName() string { return "announcement_read" }

// SurveyQuestion 问卷题目（notification-center §1.3）。
// options 为 single/multi 的选项数组；rating/text 时为 NULL。
// required 带 gorm:"default:1"（default:true bool 坑场景，见 database.md §6）：
// Create 路径需 *bool 入参或 UpdateColumn fixup 以保证 required=false 正确落库。
// 无软删除。
type SurveyQuestion struct {
	ID             uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	AnnouncementID uint64         `gorm:"type:bigint unsigned;not null;index:idx_sq_ann" json:"announcement_id"`
	OrderIndex     int            `gorm:"type:int;not null;default:0" json:"order_index"`
	QuestionType   string         `gorm:"size:16;not null" json:"question_type"`
	Title          string         `gorm:"size:500;not null" json:"title"`
	Options        datatypes.JSON `json:"options"`
	RatingMax      *int           `gorm:"type:int" json:"rating_max"`
	RatingStyle    string         `gorm:"size:10" json:"rating_style"`
	Required       bool           `gorm:"type:tinyint(1);not null;default:1" json:"required"`
	CreatedAt      time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
}

// TableName returns the table name for SurveyQuestion.
func (SurveyQuestion) TableName() string { return "survey_question" }

// SurveyResponse 答卷（notification-center §1.4，一人一份）。
// UNIQUE(announcement_id, user_id) 兜底一人一答竞态。无软删除。
type SurveyResponse struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	AnnouncementID uint64    `gorm:"type:bigint unsigned;not null;uniqueIndex:uk_sr,priority:1;index:idx_sr_ann" json:"announcement_id"`
	UserID         uint      `gorm:"type:int unsigned;not null;uniqueIndex:uk_sr,priority:2" json:"user_id"`
	SubmittedAt    time.Time `gorm:"type:datetime;not null" json:"submitted_at"`
	CreatedAt      time.Time `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
}

// TableName returns the table name for SurveyResponse.
func (SurveyResponse) TableName() string { return "survey_response" }

// SurveyAnswer 单题答案（notification-center §1.5）。
// answer_options 为选中的选项值数组（single 1 个 / multi N 个）；
// answer_rating 为 rating 值；answer_text 为开放文本。无软删除。
type SurveyAnswer struct {
	ID            uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	ResponseID    uint64         `gorm:"type:bigint unsigned;not null;index:idx_sa_response" json:"response_id"`
	QuestionID    uint64         `gorm:"type:bigint unsigned;not null;index:idx_sa_question" json:"question_id"`
	AnswerOptions datatypes.JSON `json:"answer_options"`
	AnswerRating  *int           `gorm:"type:int" json:"answer_rating"`
	AnswerText    string         `gorm:"type:text" json:"answer_text"`
	CreatedAt     time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
}

// TableName returns the table name for SurveyAnswer.
func (SurveyAnswer) TableName() string { return "survey_answer" }
