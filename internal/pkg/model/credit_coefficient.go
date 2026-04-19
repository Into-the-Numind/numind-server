package model

import "time"

// CreditEstimationCoefficient R2 估算系数表（append-only 版本）
// 每次修改参数 = 插入新 version，老 version 保留对账历史 reservation。
// 同一 (provider, model, operation) 至多有一行 is_active=true。
// 详见 spec §2.3 / §2.9。
type CreditEstimationCoefficient struct {
	ID                    uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Provider              string    `gorm:"size:50;not null;uniqueIndex:uk_provider_model_op_version,priority:1" json:"provider"`
	Model                 string    `gorm:"size:100;not null;uniqueIndex:uk_provider_model_op_version,priority:2" json:"model"`
	Operation             string    `gorm:"size:50;not null;uniqueIndex:uk_provider_model_op_version,priority:3" json:"operation"`
	CharToTokenRatio      float64   `gorm:"type:decimal(6,3);not null" json:"char_to_token_ratio"`
	CompletionPromptRatio float64   `gorm:"type:decimal(6,3);not null" json:"completion_prompt_ratio"`
	SafetyBufferPct       float64   `gorm:"type:decimal(5,3);not null;default:0.200" json:"safety_buffer_pct"`
	Version               uint      `gorm:"not null;uniqueIndex:uk_provider_model_op_version,priority:4" json:"version"`
	IsActive              bool      `gorm:"not null;default:false" json:"is_active"`
	ChangeReason          string    `gorm:"size:255" json:"change_reason,omitempty"`
	UpdatedBy             string    `gorm:"size:64" json:"updated_by,omitempty"`
	CreatedAt             time.Time `gorm:"autoCreateTime:milli" json:"created_at"`
	UpdatedAt             time.Time `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName 指定表名
func (CreditEstimationCoefficient) TableName() string { return "credit_estimation_coefficient" }
