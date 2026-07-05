package model

import (
	"time"

	"gorm.io/datatypes"
)

// XHS script note type constants.
const (
	XhsScriptNoteTypeNormal = "normal"
	XhsScriptNoteTypeVideo  = "video"
)

// XHS script video transcribe status constants.
const (
	XhsScriptTranscribePending      = "pending"
	XhsScriptTranscribeTranscribing = "transcribing"
	XhsScriptTranscribeReady        = "ready"
	XhsScriptTranscribeFailed       = "failed"
	XhsScriptTranscribeEmpty        = "empty"
)

// XHS script generation status constants.
const (
	XhsScriptGenerateNotReady   = "not_ready"
	XhsScriptGenerateReady      = "ready"
	XhsScriptGenerateGenerating = "generating"
	XhsScriptGenerateGenerated  = "generated"
	XhsScriptGenerateFailed     = "failed"
)

// XHS script quota bucket constants.
const (
	XhsScriptQuotaBucketFree = "free"
	XhsScriptQuotaBucketPaid = "paid"
)

// XHS script quota ledger reason constants.
const (
	XhsScriptLedgerReasonTrialGrant = "trial_grant"
	XhsScriptLedgerReasonPurchase   = "purchase"
	XhsScriptLedgerReasonGeneration = "generation"
)

// XHS script quota ledger ref type constants.
const (
	XhsScriptLedgerRefTypeGeneration = "generation"
	XhsScriptLedgerRefTypePurchase   = "purchase"
)

type XhsScriptUserProfile struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      uint      `gorm:"not null;uniqueIndex:uk_xsup_user" json:"user_id"`
	ProfileText string    `gorm:"type:text" json:"profile_text"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (XhsScriptUserProfile) TableName() string { return "xhs_script_user_profile" }

type XhsScriptQuotaAccount struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID        uint      `gorm:"not null;uniqueIndex:uk_xsqa_user" json:"user_id"`
	FreeRemaining int64     `gorm:"not null;default:3" json:"free_remaining"`
	PaidRemaining int64     `gorm:"not null;default:0" json:"paid_remaining"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (XhsScriptQuotaAccount) TableName() string { return "xhs_script_quota_account" }

type XhsScriptNote struct {
	ID           uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID       uint           `gorm:"not null;index:idx_xsn_user_created,priority:1;uniqueIndex:uk_xsn_user_source,priority:1" json:"user_id"`
	SourceNoteID string         `gorm:"size:100;not null;uniqueIndex:uk_xsn_user_source,priority:2" json:"source_note_id"`
	NoteURL      string         `gorm:"size:1000" json:"note_url"`
	NoteType     string         `gorm:"size:20;not null;default:'normal'" json:"note_type"`
	Title        string         `gorm:"size:500" json:"title"`
	Description  string         `gorm:"type:text" json:"description"`
	Tags         datatypes.JSON `json:"tags"`

	LikeCount    int64          `gorm:"not null;default:0" json:"like_count"`
	CollectCount int64          `gorm:"not null;default:0" json:"collect_count"`
	CommentCount int64          `gorm:"not null;default:0" json:"comment_count"`
	HotComments  datatypes.JSON `json:"hot_comments"`

	CoverURL         string    `gorm:"size:1000" json:"cover_url"`
	VideoURL         string    `gorm:"size:1000" json:"video_url"`
	VideoTranscript  *string   `gorm:"type:text" json:"video_transcript"`
	TranscribeStatus string    `gorm:"size:24;not null;default:'pending';index:idx_xsn_transcribe" json:"transcribe_status"`
	GenerateStatus   string    `gorm:"size:24;not null;default:'not_ready';index:idx_xsn_generate" json:"generate_status"`
	LastError        string    `gorm:"type:text" json:"last_error"`
	CreatedAt        time.Time `gorm:"index:idx_xsn_user_created,priority:2" json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (XhsScriptNote) TableName() string { return "xhs_script_note" }

type XhsScriptGeneration struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID           uint      `gorm:"not null;index:idx_xsg_user_note,priority:1" json:"user_id"`
	NoteID           uint64    `gorm:"not null;index:idx_xsg_user_note,priority:2;uniqueIndex:uk_xsg_note_version,priority:1" json:"note_id"`
	Version          int       `gorm:"not null;uniqueIndex:uk_xsg_note_version,priority:2" json:"version"`
	ScriptText       string    `gorm:"type:longtext" json:"script_text"`
	PromptTokens     int       `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int       `gorm:"not null;default:0" json:"completion_tokens"`
	CreatedAt        time.Time `json:"created_at"`
}

func (XhsScriptGeneration) TableName() string { return "xhs_script_generation" }

type XhsScriptQuotaLedger struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint      `gorm:"not null;index:idx_xsql_user_created,priority:1;uniqueIndex:uk_xsql_idem,priority:1" json:"user_id"`
	Delta     int64     `gorm:"not null" json:"delta"`
	Bucket    string    `gorm:"size:20;not null" json:"bucket"`
	Reason    string    `gorm:"size:50;not null;uniqueIndex:uk_xsql_idem,priority:2" json:"reason"`
	RefType   string    `gorm:"size:50;not null;uniqueIndex:uk_xsql_idem,priority:3" json:"ref_type"`
	RefID     string    `gorm:"size:128;not null;index:idx_xsql_ref;uniqueIndex:uk_xsql_idem,priority:4" json:"ref_id"`
	CreatedAt time.Time `gorm:"index:idx_xsql_user_created,priority:2" json:"created_at"`
}

func (XhsScriptQuotaLedger) TableName() string { return "xhs_script_quota_ledger" }

type XhsScriptAnalyticsEvent struct {
	ID          uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	EventID     string         `gorm:"size:100;not null;uniqueIndex:uk_xsae_event" json:"event_id"`
	EventName   string         `gorm:"size:100;not null;index:idx_xsae_event_name" json:"event_name"`
	AnonymousID string         `gorm:"size:100;index:idx_xsae_anonymous" json:"anonymous_id"`
	UserID      *uint          `gorm:"index:idx_xsae_user" json:"user_id"`
	SessionID   string         `gorm:"size:100;index:idx_xsae_session" json:"session_id"`
	Path        string         `gorm:"size:1000" json:"path"`
	Properties  datatypes.JSON `json:"properties"`
	CreatedAt   time.Time      `gorm:"index:idx_xsae_created" json:"created_at"`
}

func (XhsScriptAnalyticsEvent) TableName() string { return "xhs_script_analytics_event" }
