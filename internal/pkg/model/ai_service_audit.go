package model

import "time"

// AIServiceAuditLog records all administrative changes to AI services and task profiles.
// Entries are immutable — only INSERT is allowed, never UPDATE or DELETE.
type AIServiceAuditLog struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ActorID    uint64    `gorm:"not null;index:idx_asal_actor_created" json:"actor_id"` // admin_user.id
	ActorName  string    `gorm:"size:100;not null" json:"actor_name"`
	Action     string    `gorm:"size:50;not null" json:"action"`                            // service.create | service.update | service.deprecate | task.bind | pricing.update | capability.override
	TargetType string    `gorm:"size:20;not null;index:idx_asal_target" json:"target_type"` // service | task_profile
	TargetID   uint64    `gorm:"not null;index:idx_asal_target" json:"target_id"`
	DiffJSON   JSONMap   `gorm:"column:diff_json;type:json" json:"diff_json"` // before/after diff
	Reason     string    `gorm:"type:text" json:"reason"`                     // required for capability.override
	CreatedAt  time.Time `gorm:"index:idx_asal_actor_created" json:"created_at"`
}

// TableName returns the table name for AIServiceAuditLog.
func (AIServiceAuditLog) TableName() string { return "ai_service_audit_log" }

// Audit action constants.
const (
	AuditActionServiceCreate      = "service.create"
	AuditActionServiceUpdate      = "service.update"
	AuditActionServiceDeprecate   = "service.deprecate"
	AuditActionServiceRestore     = "service.restore"
	AuditActionTaskBind           = "task.bind"
	AuditActionPricingUpdate      = "pricing.update"
	AuditActionCapabilityOverride = "capability.override"
)

// Audit target type constants.
const (
	AuditTargetService     = "service"
	AuditTargetTaskProfile = "task_profile"
)
