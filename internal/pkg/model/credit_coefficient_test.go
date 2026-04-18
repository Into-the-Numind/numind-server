package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newTestDB 创建一个 in-memory SQLite 用于 model 层的 AutoMigrate/CRUD 测试。
// 注意：SQLite 不支持 MySQL 的 ENUM/DECIMAL 精度等全部特性，这里只验证 GORM 标签
// 的基础映射（字段存在、主键、索引、auto-increment），真正的 DDL 生效验证在 Task A.4
// 通过 MySQL 执行 migration 完成。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func TestCreditEstimationCoefficient_AutoMigrate(t *testing.T) {
	db := newTestDB(t)
	err := db.AutoMigrate(&CreditEstimationCoefficient{})
	require.NoError(t, err)

	assert.True(t, db.Migrator().HasTable(&CreditEstimationCoefficient{}),
		"table credit_estimation_coefficient should exist after AutoMigrate")

	for _, col := range []string{
		"id", "provider", "model", "operation",
		"char_to_token_ratio", "completion_prompt_ratio", "safety_buffer_pct",
		"version", "is_active", "change_reason", "updated_by",
		"created_at", "updated_at",
	} {
		assert.True(t, db.Migrator().HasColumn(&CreditEstimationCoefficient{}, col),
			"column %s should exist", col)
	}
}

func TestCreditEstimationCoefficient_TableName(t *testing.T) {
	assert.Equal(t, "credit_estimation_coefficient", CreditEstimationCoefficient{}.TableName())
}

func TestCreditEstimationCoefficient_CreateAndQuery(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&CreditEstimationCoefficient{}))

	row := CreditEstimationCoefficient{
		Provider:              "ali",
		Model:                 "qwen-turbo",
		Operation:             "sop_run",
		CharToTokenRatio:      1.500,
		CompletionPromptRatio: 0.500,
		SafetyBufferPct:       0.200,
		Version:               1,
		IsActive:              true,
		ChangeReason:          "initial seed",
		UpdatedBy:             "system",
	}
	require.NoError(t, db.Create(&row).Error)
	assert.NotZero(t, row.ID)

	var got CreditEstimationCoefficient
	require.NoError(t, db.First(&got, row.ID).Error)
	assert.Equal(t, "ali", got.Provider)
	assert.Equal(t, "qwen-turbo", got.Model)
	assert.Equal(t, "sop_run", got.Operation)
	assert.InDelta(t, 1.500, got.CharToTokenRatio, 0.0001)
	assert.InDelta(t, 0.500, got.CompletionPromptRatio, 0.0001)
	assert.InDelta(t, 0.200, got.SafetyBufferPct, 0.0001)
	assert.Equal(t, uint(1), got.Version)
	assert.True(t, got.IsActive)
	assert.Equal(t, "initial seed", got.ChangeReason)
	assert.Equal(t, "system", got.UpdatedBy)
	assert.WithinDuration(t, time.Now(), got.CreatedAt, 5*time.Second)
	assert.WithinDuration(t, time.Now(), got.UpdatedAt, 5*time.Second)
}

func TestCreditEstimationCoefficient_UniqueIndex(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&CreditEstimationCoefficient{}))

	base := CreditEstimationCoefficient{
		Provider:              "ali",
		Model:                 "qwen-turbo",
		Operation:             "sop_run",
		CharToTokenRatio:      1.500,
		CompletionPromptRatio: 0.500,
		SafetyBufferPct:       0.200,
		Version:               1,
		IsActive:              true,
	}
	require.NoError(t, db.Create(&base).Error)

	// 相同 (provider, model, operation, version) 必须失败（唯一索引 uk_provider_model_op_version）
	dup := base
	dup.ID = 0
	dup.IsActive = false
	assert.Error(t, db.Create(&dup).Error, "duplicate (provider, model, operation, version) should violate unique index")

	// 仅 version 不同应当成功（append-only 新版本）
	nextVer := base
	nextVer.ID = 0
	nextVer.Version = 2
	assert.NoError(t, db.Create(&nextVer).Error)
}
