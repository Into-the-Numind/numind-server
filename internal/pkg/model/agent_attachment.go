package model

import "time"

// AgentAttachment records a file uploaded by a user in agent mode.
// Files are stored in COS; this row tracks the URL plus async multimodal
// fallback fields (vision_description / ocr_text / text_fallback) that are
// populated by the background FallbackService (task 1.2).
//
// The table is created by migration 20260523_120000_agent_attachment_fallback.sql.
type AgentAttachment struct {
	ID       uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID   uint   `gorm:"not null;index:idx_aa_user"  json:"user_id"`
	URL      string `gorm:"type:text;not null"         json:"url"`
	Filename string `gorm:"size:255"                   json:"filename"`
	MimeType string `gorm:"size:128"                   json:"mime_type"`
	Size     int64  `gorm:"default:0"                  json:"size"`

	// Modality is detected at upload time.
	// Valid values: "image" | "pdf" | "audio" | "document" | "unknown".
	// "document" covers office docs (docx/doc/pptx/xlsx/rtf), extracted to text
	// locally via parser.DocumentParser. "unknown" means mime detection failed;
	// the fallback worker skips those rows.
	Modality string `gorm:"size:32;default:'unknown'" json:"modality"`

	// Width and Height are populated during fallback generation for image files.
	Width  *int `gorm:"default:null" json:"width,omitempty"`
	Height *int `gorm:"default:null" json:"height,omitempty"`

	// ── Async fallback fields (task 1.2) ────────────────────────────────────

	// OCRText contains Baidu OCR extraction (image modality only). Nil means
	// OCR was not run or returned an empty result.
	OCRText *string `gorm:"type:text" json:"ocr_text,omitempty"`

	// VisionDescription contains the VLM visual description (image modality).
	VisionDescription *string `gorm:"type:text" json:"vision_description,omitempty"`

	// TextFallback is the composed fallback text consumed by buildAgentInput
	// (task 1.3) when the active model does not accept the file's modality.
	// On terminal generation failure this is still set to a degraded message
	// (e.g. "[图片：{filename}，描述生成失败：{error}]") so task 1.3 always
	// has something to inject.
	TextFallback *string `gorm:"type:text" json:"text_fallback,omitempty"`

	// FallbackReady is true once fallback generation has completed (success or
	// terminal failure). A value of false means the worker has not yet processed
	// this row. WaitReady polls this field.
	//
	// GORM v2 default:false gotcha: this field has no `gorm:"default:true"` tag
	// so the zero-value bool is fine — false is the correct DB default.
	FallbackReady bool `gorm:"default:false" json:"fallback_ready"`

	// FallbackError is non-nil when generation reached the retry limit.
	// TextFallback is still set to a degraded message in that case.
	FallbackError *string `gorm:"type:text" json:"fallback_error,omitempty"`

	FallbackStartedAt   *time.Time `json:"fallback_started_at,omitempty"`
	FallbackCompletedAt *time.Time `json:"fallback_completed_at,omitempty"`

	// RetryCount tracks how many times the worker has attempted generation.
	// Capped at maxRetries (3). Once retryCount == maxRetries and the last
	// attempt failed, FallbackReady is set true and FallbackError recorded.
	RetryCount uint8 `gorm:"default:0" json:"retry_count"`

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// TableName returns the MySQL table name for GORM.
func (AgentAttachment) TableName() string { return "agent_attachment" }
