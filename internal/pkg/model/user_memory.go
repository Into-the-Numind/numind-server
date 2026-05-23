package model

import "time"

// UserMemoryProfile 是 Agent Mode V1.5 Layer A 的用户画像表（per-user 单行）。
// 存 work_context / personal_context / top_of_mind 等元信息 + dialectic 推理 cache。
//
// 字段 total_facts 由应用层在 Create / BatchCreate / Archive / BulkArchiveByConfidence
// 内 IncrTotalFacts(delta) 同事务维护，每日 cron 对账修正 drift。
//
// 所有 bool 字段都用 zero-value false（无 `default:true` 陷阱，见 .claude/rules/database.md §6）。
type UserMemoryProfile struct {
	UserID                 uint       `gorm:"primaryKey;not null" json:"user_id"`
	WorkContext            string     `gorm:"type:text" json:"work_context"`
	PersonalContext        string     `gorm:"type:text" json:"personal_context"`
	TopOfMind              string     `gorm:"type:text" json:"top_of_mind"`
	CachedInsight          string     `gorm:"type:text" json:"cached_insight"`
	CachedInsightAt        *time.Time `json:"cached_insight_at"`
	CachedInsightFactCount int        `gorm:"not null;default:0" json:"cached_insight_fact_count"`
	TotalFacts             int        `gorm:"not null;default:0" json:"total_facts"`
	// ExtractionCountSinceRebuild 由 Task 3.3 ExtractorService 在每次成功抽取后自增 1.
	// 累计到 ExtractionCountRebuildThreshold (默认 5) 时触发 RebuildNarrative, 重置 0.
	ExtractionCountSinceRebuild int        `gorm:"not null;default:0" json:"extraction_count_since_rebuild"`
	LastExtractionAt            *time.Time `json:"last_extraction_at"`
	LastExtractionSessionID     string     `gorm:"size:64;not null;default:''" json:"last_extraction_session_id"`
	CreatedAt                   time.Time  `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt                   time.Time  `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName overrides GORM's default plural naming.
func (UserMemoryProfile) TableName() string { return "user_memory_profile" }

// UserMemoryFact 是 Agent Mode V1.5 Layer A 的用户 fact 表（每用户多条）。
//
// SubjectID 是 V2 Layer B 预留字段：
//   - V1.5 = Layer A（对使用 agent 的真实 user 画像），SubjectID 必须为 nil
//   - V2   = Layer B（对使用者关注的对象画像），SubjectID 填业务实体 ID
//
// store 层在 Create / BatchCreate 时显式拒绝非 nil SubjectID（返回 ErrLayerBNotSupported）。
// 等 V2 启用时去除此校验。
//
// IsArchived 是 bool 软删除，zero-value=false（无 `default:true` 陷阱）。
type UserMemoryFact struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UUID   string `gorm:"size:64;uniqueIndex;not null;column:uuid" json:"uuid"`
	UserID uint   `gorm:"not null;index" json:"user_id"`
	// V2 Layer B 预留: V1.5 必须全 NULL (store 层 Create/BatchCreate 拒绝非 NULL 写入)
	SubjectID         *string    `gorm:"size:64;column:subject_id" json:"subject_id,omitempty"`
	Content           string     `gorm:"type:text;not null" json:"content"`
	Category          string     `gorm:"size:32;not null" json:"category"`
	Confidence        float64    `gorm:"type:decimal(3,2);not null" json:"confidence"`
	Importance        float64    `gorm:"type:decimal(3,2);not null;default:0.50" json:"importance"`
	SourceSessionID   string     `gorm:"size:64;not null;default:''" json:"source_session_id"`
	SourceMessageUUID string     `gorm:"size:64;not null;default:''" json:"source_message_uuid"`
	SourceExtractedAt time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP" json:"source_extracted_at"`
	LastUsedAt        *time.Time `json:"last_used_at"`
	UseCount          int        `gorm:"not null;default:0" json:"use_count"`
	EmbeddingHash     string     `gorm:"size:64;not null;default:''" json:"embedding_hash"`
	IsArchived        bool       `gorm:"not null;default:false" json:"is_archived"`
	CreatedAt         time.Time  `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time  `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName overrides GORM's default plural naming.
func (UserMemoryFact) TableName() string { return "user_memory_facts" }

// Memory fact category 枚举常量。store / biz / DB CHECK 约束都用同一组值。
const (
	MemoryFactCategoryPreference = "preference"
	MemoryFactCategoryKnowledge  = "knowledge"
	MemoryFactCategoryContext    = "context"
	MemoryFactCategoryBehavior   = "behavior"
	MemoryFactCategoryGoal       = "goal"
	MemoryFactCategoryCorrection = "correction"
)

// AllMemoryFactCategories 枚举 6 类合法 category（供 biz 层校验）。
var AllMemoryFactCategories = []string{
	MemoryFactCategoryPreference,
	MemoryFactCategoryKnowledge,
	MemoryFactCategoryContext,
	MemoryFactCategoryBehavior,
	MemoryFactCategoryGoal,
	MemoryFactCategoryCorrection,
}
