package model

import (
	"time"

	"gorm.io/datatypes"
)

// TokenEstimationProfile stores model/provider-level call-before-token estimation profiles.
// Multiple versions may exist; biz layer must maintain the active-version invariant via
// SELECT ... FOR UPDATE + deactivate old + insert new within a transaction.
// Query paths sort active rows by version DESC, id DESC.
// See spec §3.2.
//
// GORM `default:true` bool gotcha (see .claude/rules/database.md §6): when admin CRUD
// passes IsActive=false explicitly, GORM v2 treats the bool zero-value as "not set" and
// the DB DEFAULT TRUE silently wins. Future implementers (Tasks 7/11) MUST capture caller
// intent before Create and follow up with `UpdateColumn("is_active", false)` to persist
// the false. The same applies to IsFallback when its column default is true (currently
// false here, so safe).
type TokenEstimationProfile struct {
	ID                       uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Provider                 string         `gorm:"size:50;not null;default:''" json:"provider"`
	Model                    string         `gorm:"size:100;not null;default:''" json:"model"`
	ModelFamily              string         `gorm:"size:80;not null;default:''" json:"model_family"`
	ServiceType              string         `gorm:"size:30;not null;default:'llm_chat'" json:"service_type"`
	ProfileJSON              datatypes.JSON `gorm:"not null" json:"profile_json"`
	SafetyMultiplier         float64        `gorm:"type:decimal(8,4);not null;default:1.1500" json:"safety_multiplier"`
	CalibrationMultiplier    float64        `gorm:"type:decimal(8,4);not null;default:1.0000" json:"calibration_multiplier"`
	CalibrationSampleCount   int            `gorm:"not null;default:0" json:"calibration_sample_count"`
	CalibrationP50AbsError   *float64       `gorm:"type:decimal(8,4)" json:"calibration_p50_abs_error"`
	CalibrationP90AbsError   *float64       `gorm:"type:decimal(8,4)" json:"calibration_p90_abs_error"`
	CalibrationP99UnderRatio *float64       `gorm:"type:decimal(8,4)" json:"calibration_p99_under_ratio"`
	Version                  uint           `gorm:"not null;default:1" json:"version"`
	IsActive                 bool           `gorm:"not null;default:true" json:"is_active"`
	IsFallback               bool           `gorm:"not null;default:false" json:"is_fallback"`
	ChangeReason             string         `gorm:"size:255" json:"change_reason"`
	UpdatedBy                string         `gorm:"size:80" json:"updated_by"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

// TableName returns the table name for TokenEstimationProfile.
func (TokenEstimationProfile) TableName() string { return "token_estimation_profile" }

// ContextBudgetPolicy stores operation-level budget policies.
// Versioning is append-only: upsert inserts a new version and deactivates prior active rows.
// See spec §3.3.
//
// GORM `default:true` bool gotcha (see .claude/rules/database.md §6): both ChargeUser and
// IsActive carry `default:true`. When admin CRUD or seeders pass false explicitly, the
// false is silently flipped to true on Create. Critical for the `context_compression`
// policy which MUST persist `charge_user=false`. Tasks 7/11 must follow the
// capture-intent-then-UpdateColumn pattern from database.md §6.
type ContextBudgetPolicy struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Operation            string    `gorm:"size:80;not null" json:"operation"`
	ReservedOutputTokens int       `gorm:"not null" json:"reserved_output_tokens"`
	SafeRatio            float64   `gorm:"type:decimal(5,4);not null;default:0.8500" json:"safe_ratio"`
	FixedOverheadTokens  int       `gorm:"not null;default:512" json:"fixed_overhead_tokens"`
	SoftThresholdRatio   float64   `gorm:"type:decimal(5,4);not null;default:0.7000" json:"soft_threshold_ratio"`
	HardThresholdRatio   float64   `gorm:"type:decimal(5,4);not null;default:0.8500" json:"hard_threshold_ratio"`
	ChargeUser           bool      `gorm:"not null;default:true" json:"charge_user"`
	Description          string    `gorm:"size:255" json:"description"`
	Version              uint      `gorm:"not null;default:1" json:"version"`
	IsActive             bool      `gorm:"not null;default:true" json:"is_active"`
	ChangeReason         string    `gorm:"size:255" json:"change_reason"`
	UpdatedBy            string    `gorm:"size:80" json:"updated_by"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// TableName returns the table name for ContextBudgetPolicy.
func (ContextBudgetPolicy) TableName() string { return "context_budget_policy" }

// ContextSummary stores async/sync compression results scoped per user and conversation scope.
// Must not store cross-user content. All queries must constrain by owner_user_id + scope.
// See spec §3.4.
type ContextSummary struct {
	ID                    uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID                uint64         `gorm:"not null" json:"user_id"`
	OwnerUserID           *uint64        `gorm:"default:null" json:"owner_user_id"`
	ScopeType             string         `gorm:"size:40;not null" json:"scope_type"`
	ScopeID               string         `gorm:"size:100;not null" json:"scope_id"`
	SourceHash            string         `gorm:"size:64;not null" json:"source_hash"`
	SourceFragmentIDs     datatypes.JSON `gorm:"not null" json:"source_fragment_ids"`
	SummaryText           string         `gorm:"type:mediumtext;not null" json:"summary_text"`
	SummaryTokenEstimate  int            `gorm:"not null;default:0" json:"summary_token_estimate"`
	OriginalTokenEstimate int            `gorm:"not null;default:0" json:"original_token_estimate"`
	Model                 string         `gorm:"size:100;not null;default:''" json:"model"`
	Provider              string         `gorm:"size:50;not null;default:''" json:"provider"`
	Status                string         `gorm:"size:20;not null;default:'ready'" json:"status"`
	ErrorMessage          string         `gorm:"size:500" json:"error_message"`
	CreatedByOperation    string         `gorm:"size:80;not null;default:''" json:"created_by_operation"`
	ExpiresAt             *time.Time     `json:"expires_at"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

// TableName returns the table name for ContextSummary.
func (ContextSummary) TableName() string { return "context_summary" }

// ContextBudgetEvent is an audit and observability table for budget/compression events.
// Stores metadata only; must not store full prompt content.
// See spec §3.5.
type ContextBudgetEvent struct {
	ID                      uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID                  *uint64        `gorm:"default:null" json:"user_id"`
	Operation               string         `gorm:"size:80;not null" json:"operation"`
	TaskID                  string         `gorm:"size:80" json:"task_id"`
	Provider                string         `gorm:"size:50;not null;default:''" json:"provider"`
	Model                   string         `gorm:"size:100;not null;default:''" json:"model"`
	ContextWindow           int            `gorm:"not null;default:0" json:"context_window"`
	MaxOutputTokens         int            `gorm:"not null;default:0" json:"max_output_tokens"`
	ReservedOutputTokens    int            `gorm:"not null;default:0" json:"reserved_output_tokens"`
	FixedOverheadTokens     int            `gorm:"not null;default:0" json:"fixed_overhead_tokens"`
	SafeRatio               float64        `gorm:"type:decimal(5,4);not null;default:0.8500" json:"safe_ratio"`
	SafeInputBudget         int            `gorm:"not null;default:0" json:"safe_input_budget"`
	EstimatedBefore         int            `gorm:"not null;default:0" json:"estimated_before"`
	EstimatedAfter          int            `gorm:"not null;default:0" json:"estimated_after"`
	ActualPromptTokens      *int           `gorm:"default:null" json:"actual_prompt_tokens"`
	ActualCompletionTokens  *int           `gorm:"default:null" json:"actual_completion_tokens"`
	ReserveAmount           *int64         `gorm:"default:null" json:"reserve_amount"`
	ReconcileDelta          *int64         `gorm:"default:null" json:"reconcile_delta"`
	CompressionActions      datatypes.JSON `gorm:"default:null" json:"compression_actions"`
	DroppedFragmentCount    int            `gorm:"not null;default:0" json:"dropped_fragment_count"`
	SummarizedFragmentCount int            `gorm:"not null;default:0" json:"summarized_fragment_count"`
	CriticalFragmentCount   int            `gorm:"not null;default:0" json:"critical_fragment_count"`
	CalibrationRatio        *float64       `gorm:"type:decimal(10,4);default:null" json:"calibration_ratio"`
	TokenProfileID          *uint64        `gorm:"default:null" json:"token_profile_id"`
	BudgetPolicyID          *uint64        `gorm:"default:null" json:"budget_policy_id"`
	ReservationID           *uint64        `gorm:"default:null" json:"reservation_id"`
	UsageRecordID           *uint64        `gorm:"default:null" json:"usage_record_id"`
	Status                  string         `gorm:"size:30;not null;default:'ok'" json:"status"`
	ErrorCode               string         `gorm:"size:80" json:"error_code"`
	Metadata                datatypes.JSON `gorm:"default:null" json:"metadata"`
	CreatedAt               time.Time      `json:"created_at"`
}

// TableName returns the table name for ContextBudgetEvent.
func (ContextBudgetEvent) TableName() string { return "context_budget_event" }
