package model

import "time"

// TaskProfile defines the capability requirements and routing configuration
// for a named AI task (e.g. "sop.text", "salesrag.embed").
// Task Profiles are managed via the admin panel and consumed by the Gateway.
type TaskProfile struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TaskID           string    `gorm:"size:80;not null;uniqueIndex" json:"task_id"` // e.g. sop.text
	DisplayName      string    `gorm:"size:100;not null" json:"display_name"`
	Description      string    `gorm:"type:text" json:"description"`
	ServiceType      string    `gorm:"size:20;not null" json:"service_type"`                   // llm | ocr | asr
	Requirements     JSONMap   `gorm:"column:requirements;type:json" json:"requirements"`      // capability requirements
	DefaultServiceID *uint64   `gorm:"index:idx_tp_default_service" json:"default_service_id"` // FK → ai_service.id
	UserSelectable   bool      `gorm:"default:false" json:"user_selectable"`                   // expose in C-side ModelSelector?
	ExtraMetadata    JSONMap   `gorm:"column:extra_metadata;type:json" json:"extra_metadata"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	// Associations (loaded on demand)
	DefaultService *AIService           `gorm:"foreignKey:DefaultServiceID" json:"default_service,omitempty"`
	Services       []TaskProfileService `gorm:"foreignKey:TaskProfileID" json:"services,omitempty"`
}

// TableName returns the table name for TaskProfile.
func (TaskProfile) TableName() string { return "task_profile" }

// TaskProfileService records which AI services are bound to a Task Profile,
// either as fallback candidates or as user-selectable allowed services.
type TaskProfileService struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TaskProfileID uint64    `gorm:"not null;uniqueIndex:uk_profile_service_role" json:"task_profile_id"`
	ServiceID     uint64    `gorm:"not null;uniqueIndex:uk_profile_service_role" json:"service_id"`
	Role          string    `gorm:"size:20;not null;uniqueIndex:uk_profile_service_role" json:"role"` // fallback | allowed
	Priority      int       `gorm:"default:0" json:"priority"`
	CreatedAt     time.Time `json:"created_at"`

	// Associations (loaded on demand)
	TaskProfile *TaskProfile `gorm:"foreignKey:TaskProfileID" json:"task_profile,omitempty"`
	Service     *AIService   `gorm:"foreignKey:ServiceID" json:"service,omitempty"`
}

// TableName returns the table name for TaskProfileService.
func (TaskProfileService) TableName() string { return "task_profile_service" }

// Task profile service role constants.
const (
	TaskProfileRoleFallback = "fallback"
	TaskProfileRoleAllowed  = "allowed"
)
