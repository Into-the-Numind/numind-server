package model

import (
	"time"

	"gorm.io/datatypes"
)

// XhsTopicNote enrich status constants — see design §2.
const (
	XhsEnrichPending             = "pending"
	XhsEnrichEnriching           = "enriching"
	XhsEnrichDone                = "done"
	XhsEnrichPartial             = "partial" // 视频直链失效或部分失败
	XhsEnrichFailed              = "failed"
	XhsEnrichInsufficientCredits = "insufficient_credits"
)

// XhsTopicNote note type constants.
const (
	XhsNoteTypeNormal = "normal"
	XhsNoteTypeVideo  = "video"
)

// XhsTopicNote 小红书选题采集笔记 —— 客户浏览器插件采集后落入有数累积选题库。
// 显式字段，无软删除，照 MonitorNote 约定。去重防重复扣分依赖 ContentHash。
type XhsTopicNote struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      uint   `gorm:"not null;index:idx_xtn_user_crawled,priority:1;uniqueIndex:uk_xtn_user_note,priority:1" json:"user_id"`
	XhsNoteID   string `gorm:"size:100;not null;uniqueIndex:uk_xtn_user_note,priority:2" json:"xhs_note_id"`
	ContentHash string `gorm:"size:64;index:idx_xtn_hash" json:"-"` // SHA256(title+content+video_url)，防重复富化/扣分

	NoteType        string         `gorm:"size:20;default:'normal'" json:"note_type"` // normal/video
	Title           string         `gorm:"size:500" json:"title"`
	Content         string         `gorm:"type:text" json:"content"`
	Tags            datatypes.JSON `json:"tags"`
	CoverURL        string         `gorm:"size:1000" json:"cover_url"`
	NoteURL         string         `gorm:"size:1000" json:"note_url"`
	PublishedAt     *time.Time     `gorm:"index:idx_xtn_published" json:"published_at"`
	VideoURL        string         `gorm:"size:1000" json:"video_url"`
	VideoTranscript *string        `gorm:"type:text" json:"video_transcript"` // NULL=无转写(区分直链失效/未转)
	LikeCount       int            `gorm:"default:0" json:"like_count"`
	CollectCount    int            `gorm:"default:0" json:"collect_count"`
	CommentCount    int            `gorm:"default:0" json:"comment_count"`
	ShareCount      int            `gorm:"default:0" json:"share_count"`
	Comments        datatypes.JSON `json:"comments"` // 热门前 ≤10 条，每条 text ≤200 字
	AuthorName      string         `gorm:"size:200" json:"author_name"`
	AuthorLink      string         `gorm:"size:500" json:"author_link"`
	AuthorFollowers int            `gorm:"default:0" json:"author_followers"` // 取不到=0(已知限制)

	// 6 个 LLM 分析字段
	AITopicAngle     string `gorm:"type:text" json:"ai_topic_angle"`
	AIViralReason    string `gorm:"type:text" json:"ai_viral_reason"`
	AIBorrowable     string `gorm:"type:text" json:"ai_borrowable"`
	AITargetAudience string `gorm:"type:text" json:"ai_target_audience"`
	AITitleFormula   string `gorm:"type:text" json:"ai_title_formula"`
	AIOneLine        string `gorm:"size:500" json:"ai_one_line"`

	// EnrichStatus 枚举: pending / enriching / done / partial / failed / insufficient_credits
	EnrichStatus string     `gorm:"size:24;default:'pending';index:idx_xtn_enrich" json:"enrich_status"`
	CollectedAt  *time.Time `json:"collected_at"`                                            // 客户端采集时刻(payload 传入)
	CrawledAt    time.Time  `gorm:"index:idx_xtn_user_crawled,priority:2" json:"crawled_at"` // 服务端入库时刻
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TableName 返回 XhsTopicNote 对应的数据库表名。
func (XhsTopicNote) TableName() string { return "xhs_topic_note" }
