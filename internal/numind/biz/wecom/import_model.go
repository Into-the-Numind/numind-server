package wecom

import "time"

// ImportBatchStatus defines the status of an import batch
const (
	ImportBatchStatusPending  = 1
	ImportBatchStatusArchived = 2
)

// ImportBatch represents a batch of messages imported via merged forwarding
type ImportBatch struct {
	ID        string    `json:"id" gorm:"primaryKey;type:varchar(64)"`
	UserID    int64     `json:"user_id" gorm:"index"`
	Title     string    `json:"title" gorm:"type:varchar(255)"`
	Speakers  string    `json:"speakers" gorm:"type:text"` // Comma-separated list of speakers
	Status    int       `json:"status" gorm:"default:1"`   // 1: Pending, 2: Archived
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ImportMessage covers the information extracted from merged history items
type ImportMessage struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	BatchID   string    `json:"batch_id" gorm:"type:varchar(64);index"`
	MsgTime   int64     `json:"msg_time"`
	Speaker   string    `json:"speaker" gorm:"type:varchar(128)"` // From sourcename
	Content   string    `json:"content" gorm:"type:text"`         // Extracted text content
	MsgType   string    `json:"msg_type" gorm:"type:varchar(32)"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName overrides the table name
func (ImportBatch) TableName() string {
	return "import_batches"
}

// TableName overrides the table name
func (ImportMessage) TableName() string {
	return "import_messages"
}
