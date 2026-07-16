package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	FeishuConnectionNone               = "none"
	FeishuConnectionCreatingApp        = "creating_app"
	FeishuConnectionAppReady           = "app_ready"
	FeishuConnectionWaitingAppApproval = "waiting_app_approval"
	FeishuConnectionWaitingUserAuth    = "waiting_user_auth"
	FeishuConnectionConnected          = "connected"
	FeishuConnectionReauthRequired     = "reauth_required"
	FeishuConnectionError              = "error"
	FeishuConnectionDisconnecting      = "disconnecting"
)

// FeishuCapabilityOutcome is the strictly-derived, non-secret account metadata
// observed from one fixed catalog operation. It intentionally carries neither
// requested scopes nor raw CLI output; the status surface stores only the
// supported domain, its classified state, a true success time, and verified
// lark-cli release evidence.
type FeishuCapabilityOutcome struct {
	Domain      string
	State       string
	SucceededAt *time.Time
	CLIVersion  string
}

// FeishuConnectionEvidence is metadata proven by a controlled authorization
// flow. AppID is populated only from the local lark-cli configuration created
// by the official app flow; CLIVersion is populated only after the fixed binary
// probe succeeded during composition.
type FeishuConnectionEvidence struct {
	AppID      string
	CLIVersion string
}

const (
	FeishuCapabilityUnknown        = "unknown"
	FeishuCapabilityAvailable      = "available"
	FeishuCapabilityNeedsAppScope  = "needs_app_scope"
	FeishuCapabilityNeedsUserScope = "needs_user_scope"
	FeishuCapabilityRevoked        = "revoked"
	FeishuCapabilityResourceDenied = "resource_denied"
)

const (
	FeishuAuthPhaseCreateApp = "create_app"
	FeishuAuthPhaseAppScope  = "app_scope"
	FeishuAuthPhaseUserAuth  = "user_auth"
)

const (
	FeishuAuthSessionPending    = "pending"
	FeishuAuthSessionCompleted  = "completed"
	FeishuAuthSessionExpired    = "expired"
	FeishuAuthSessionRejected   = "rejected"
	FeishuAuthSessionFailed     = "failed"
	FeishuAuthSessionSuperseded = "superseded"
)

const (
	FeishuOperationNotStarted          = "not_started"
	FeishuOperationExecuting           = "executing"
	FeishuOperationWaitingConnection   = "waiting_connection"
	FeishuOperationWaitingAppScope     = "waiting_app_scope"
	FeishuOperationWaitingUserAuth     = "waiting_user_auth"
	FeishuOperationWaitingConfirmation = "waiting_confirmation"
	FeishuOperationSucceeded           = "succeeded"
	FeishuOperationFailed              = "failed"
	FeishuOperationUnknown             = "unknown"
	FeishuOperationCancelled           = "cancelled"
)

// FeishuCLIVault stores one encrypted lark-cli HOME snapshot per user.
type FeishuCLIVault struct {
	UserID     uint      `gorm:"type:bigint unsigned;primaryKey;autoIncrement:false" json:"user_id"`
	Generation uint64    `gorm:"not null" json:"generation"`
	Ciphertext []byte    `gorm:"type:longblob;not null" json:"-"`
	KeyVersion string    `gorm:"size:32;not null" json:"key_version"`
	Checksum   string    `gorm:"size:64;not null" json:"checksum"`
	Revision   uint64    `gorm:"not null" json:"revision"`
	CreatedAt  time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt  time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the feishu CLI vault table name.
func (FeishuCLIVault) TableName() string { return "feishu_cli_vault" }

// FeishuAuthSession stores restart-safe metadata for one external authorization
// step. Verification URLs and plaintext device codes are deliberately excluded;
// protocol v2 may retain only the authenticated ciphertext needed to resume.
type FeishuAuthSession struct {
	ID                         string         `gorm:"type:char(36);primaryKey" json:"id"`
	UserID                     uint           `gorm:"type:bigint unsigned;not null;index:idx_feishu_auth_session_user_generation,priority:1" json:"user_id"`
	Generation                 uint64         `gorm:"not null;index:idx_feishu_auth_session_user_generation,priority:2" json:"generation"`
	OperationID                *string        `gorm:"type:char(36);index:idx_feishu_auth_session_operation" json:"operation_id,omitempty"`
	Phase                      string         `gorm:"size:32;not null" json:"phase"`
	RequestedScopesJSON        datatypes.JSON `gorm:"type:json;not null" json:"requested_scopes_json"`
	State                      string         `gorm:"size:32;not null;index:idx_feishu_auth_session_lease,priority:1" json:"state"`
	ProtocolVersion            uint8          `gorm:"type:tinyint unsigned;not null;default:1" json:"-"`
	ResumeCredentialCiphertext []byte         `gorm:"type:longblob" json:"-"`
	ResumeKeyVersion           string         `gorm:"size:32" json:"-"`
	ResumeExpiresAt            *time.Time     `json:"-"`
	ScopeHash                  string         `gorm:"type:char(64)" json:"-"`
	LeaseOwner                 string         `gorm:"size:128" json:"-"`
	LeaseUntil                 *time.Time     `gorm:"index:idx_feishu_auth_session_lease,priority:2" json:"lease_until,omitempty"`
	ExpiresAt                  time.Time      `gorm:"not null" json:"expires_at"`
	CreatedAt                  time.Time      `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt                  time.Time      `gorm:"not null;autoUpdateTime" json:"updated_at"`
	CompletedAt                *time.Time     `json:"completed_at,omitempty"`
}

// TableName returns the Feishu auth session table name.
func (FeishuAuthSession) TableName() string { return "feishu_auth_session" }

// FeishuOperation stores an encrypted, idempotent lark-cli invocation and its audit state.
type FeishuOperation struct {
	ID                 string         `gorm:"type:char(36);primaryKey" json:"id"`
	UserID             uint           `gorm:"type:bigint unsigned;not null;uniqueIndex:uniq_feishu_operation_user_key,priority:1;index:idx_feishu_operation_user_generation,priority:1" json:"user_id"`
	Generation         uint64         `gorm:"not null;index:idx_feishu_operation_user_generation,priority:2" json:"generation"`
	AgentRunID         uint64         `gorm:"not null;index:idx_feishu_operation_agent_tool,priority:1" json:"agent_run_id"`
	ToolCallID         string         `gorm:"size:128;not null;index:idx_feishu_operation_agent_tool,priority:2" json:"tool_call_id"`
	IdempotencyKey     string         `gorm:"size:191;not null;uniqueIndex:uniq_feishu_operation_user_key,priority:2" json:"idempotency_key"`
	CommandPath        string         `gorm:"size:255;not null" json:"command_path"`
	Domain             string         `gorm:"size:32;not null" json:"domain"`
	RiskLevel          string         `gorm:"size:32;not null" json:"risk_level"`
	RequestCiphertext  []byte         `gorm:"type:longblob;not null" json:"-"`
	KeyVersion         string         `gorm:"size:32;not null" json:"key_version"`
	RequestFingerprint string         `gorm:"size:64;not null" json:"request_fingerprint"`
	State              string         `gorm:"size:32;not null;index:idx_feishu_operation_lease,priority:1" json:"state"`
	AttemptCount       uint           `gorm:"type:int unsigned;not null;default:0" json:"attempt_count"`
	LeaseOwner         string         `gorm:"size:128" json:"-"`
	LeaseUntil         *time.Time     `gorm:"index:idx_feishu_operation_lease,priority:2" json:"lease_until,omitempty"`
	ErrorType          string         `gorm:"size:64" json:"error_type,omitempty"`
	ErrorSubtype       string         `gorm:"size:128" json:"error_subtype,omitempty"`
	ErrorCode          string         `gorm:"size:128" json:"error_code,omitempty"`
	ResultCiphertext   []byte         `gorm:"type:longblob" json:"-"`
	ResultSummaryJSON  datatypes.JSON `gorm:"type:json" json:"result_summary_json,omitempty"`
	CreatedAt          time.Time      `gorm:"not null;autoCreateTime" json:"created_at"`
	StartedAt          *time.Time     `json:"started_at,omitempty"`
	UpdatedAt          time.Time      `gorm:"not null;autoUpdateTime" json:"updated_at"`
	FinishedAt         *time.Time     `json:"finished_at,omitempty"`
}

// TableName returns the Feishu operation table name.
func (FeishuOperation) TableName() string { return "feishu_operation" }

// FeishuOperationProofConsumption is the durable, one-shot binding between a
// succeeded empty-resource create and the single operation allowed to use it
// as an overwrite-confirmation exemption.
type FeishuOperationProofConsumption struct {
	SourceOperationID   string    `gorm:"type:char(36);primaryKey" json:"source_operation_id"`
	ConsumerOperationID string    `gorm:"type:char(36);not null;uniqueIndex:uniq_feishu_proof_consumer" json:"consumer_operation_id"`
	UserID              uint      `gorm:"type:bigint unsigned;not null;index:idx_feishu_proof_audit,priority:1" json:"user_id"`
	Generation          uint64    `gorm:"not null;index:idx_feishu_proof_audit,priority:2" json:"generation"`
	AgentRunID          uint64    `gorm:"not null;index:idx_feishu_proof_audit,priority:3" json:"agent_run_id"`
	CreatedAt           time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the Feishu proof-consumption table name.
func (FeishuOperationProofConsumption) TableName() string {
	return "feishu_operation_proof_consumption"
}

// FeishuOperationExecutionGate serializes real business CLI invocations for
// one user's active account generation across every server process.
type FeishuOperationExecutionGate struct {
	UserID      uint       `gorm:"type:bigint unsigned;primaryKey;autoIncrement:false" json:"user_id"`
	Generation  uint64     `gorm:"not null" json:"generation"`
	LeaseOwner  string     `gorm:"size:128;not null;default:''" json:"-"`
	OperationID string     `gorm:"type:char(36);not null;default:''" json:"operation_id,omitempty"`
	LeaseUntil  *time.Time `gorm:"index:idx_feishu_execution_gate_lease" json:"lease_until,omitempty"`
	UpdatedAt   time.Time  `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the Feishu business execution gate table name.
func (FeishuOperationExecutionGate) TableName() string {
	return "feishu_operation_execution_gate"
}
