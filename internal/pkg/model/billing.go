package model

import "time"

// UsageRecord 用量明细记录表 — 每次外部 API 调用记录一条
type UsageRecord struct {
	ID                    uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID                uint      `gorm:"not null;index:idx_ur_user_created" json:"user_id"`
	ServiceType           string    `gorm:"size:50;not null;index:idx_ur_service" json:"service_type"` // llm_chat, llm_vision, embedding, rerank, cos_upload, file_extract, vector_db
	Provider              string    `gorm:"size:50;not null" json:"provider"`                          // ali, volc, dmxapi, cos, vikingdb, dashvector, bailian
	Model                 string    `gorm:"size:100" json:"model"`                                     // 模型名称，如 qwen-turbo, deepseek-v3-250324
	Operation             string    `gorm:"size:100;not null;index:idx_ur_operation" json:"operation"` // 业务扣费点: sop_node_execute, salesrag_chat 等
	PromptTokens          int       `gorm:"default:0" json:"prompt_tokens"`
	CompletionTokens      int       `gorm:"default:0" json:"completion_tokens"`
	TotalTokens           int       `gorm:"default:0" json:"total_tokens"`
	ReasoningTokens       int       `gorm:"default:0" json:"reasoning_tokens"`
	EstimatedPromptTokens int       `gorm:"default:0" json:"estimated_prompt_tokens"`
	BytesUploaded         int64     `gorm:"default:0" json:"bytes_uploaded"`  // COS 上传字节数
	ItemCount             int       `gorm:"default:0" json:"item_count"`      // 向量操作条数 / Rerank 文档数
	CostCents             int64     `gorm:"default:0" json:"cost_cents"`      // 预估成本（分）
	RevenueCents          int64     `gorm:"default:0" json:"revenue_cents"`   // 客户计费金额（分）
	CreditsDeducted       int64     `gorm:"default:0" json:"credits_deducted"` // 本次操作扣减的积分数
	BizRefType            string    `gorm:"size:50" json:"biz_ref_type"`      // 关联业务对象类型: sop_run, sales_session 等
	BizRefID              uint      `gorm:"default:0" json:"biz_ref_id"`      // 关联业务对象 ID
	IsFallback            bool      `gorm:"default:false" json:"is_fallback"` // 是否为降级调用
	Metadata              string    `gorm:"type:json" json:"metadata"` // 额外上下文 JSON
	CreatedAt             time.Time `gorm:"index:idx_ur_user_created" json:"created_at"`
}

// TableName 指定表名
func (UsageRecord) TableName() string {
	return "usage_record"
}

// BillingAccount 用户计费账户
type BillingAccount struct {
	ID                  uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID              uint      `gorm:"not null;uniqueIndex" json:"user_id"`
	BalanceCents        int64     `gorm:"default:0" json:"balance_cents"`         // 当前余额（分）
	TotalConsumedCents  int64     `gorm:"default:0" json:"total_consumed_cents"`  // 累计消费（分）
	TotalRechargedCents int64     `gorm:"default:0" json:"total_recharged_cents"` // 累计充值（分）
	Status              string    `gorm:"size:20;default:'active'" json:"status"` // active, suspended, frozen
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// TableName 指定表名
func (BillingAccount) TableName() string {
	return "billing_account"
}

// PricingRule 定价规则表
type PricingRule struct {
	ID                     uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ServiceType            string    `gorm:"size:50;not null;uniqueIndex:uk_pricing_lookup" json:"service_type"`
	Provider               string    `gorm:"size:50;not null;uniqueIndex:uk_pricing_lookup" json:"provider"`
	Model                  string    `gorm:"size:100;uniqueIndex:uk_pricing_lookup" json:"model"`            // 空字符串 = 该 service+provider 的默认价格
	BillingMode            string    `gorm:"size:20;not null;default:'flat'" json:"billing_mode"`            // tiered_token | flat
	FlatUnit               string    `gorm:"size:10;not null;default:'call'" json:"flat_unit"`               // call | gb
	InputPricePerMTok      float64   `gorm:"type:decimal(10,4);default:0" json:"input_price_per_mtok"`       // 每百万输入 tokens 价格（元）
	OutputPricePerMTok     float64   `gorm:"type:decimal(10,4);default:0" json:"output_price_per_mtok"`      // 每百万输出 tokens 价格（元）
	PricePerCall           float64   `gorm:"type:decimal(10,4);default:0" json:"price_per_call"`             // 每次调用价格（元）
	PricePerGB             float64   `gorm:"type:decimal(10,4);default:0" json:"price_per_gb"`               // 每 GB 价格（元，COS 用）
	SellInputPricePerMTok  float64   `gorm:"type:decimal(10,4);default:0" json:"sell_input_price_per_mtok"`  // 售价：每百万输入 tokens（元）
	SellOutputPricePerMTok float64   `gorm:"type:decimal(10,4);default:0" json:"sell_output_price_per_mtok"` // 售价：每百万输出 tokens（元）
	SellPricePerCall       float64   `gorm:"type:decimal(10,4);default:0" json:"sell_price_per_call"`        // 售价：每次调用（元）
	SellPricePerGB         float64   `gorm:"type:decimal(10,4);default:0" json:"sell_price_per_gb"`          // 售价：每 GB（元）
	IsActive               bool      `gorm:"default:true" json:"is_active"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// TableName 指定表名
func (PricingRule) TableName() string {
	return "pricing_rule"
}

// PricingRuleTier 定价规则的分段配置（用于 tiered_token 模式）
type PricingRuleTier struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	RuleID      uint      `gorm:"not null;index:idx_rule_type" json:"rule_id"`
	TokenType   string    `gorm:"size:10;not null;index:idx_rule_type" json:"token_type"` // input | output
	MinTokens   uint      `gorm:"not null;default:0" json:"min_tokens"`
	MaxTokens   *uint     `json:"max_tokens"` // nil = 不限
	CostPerMTok float64   `gorm:"type:decimal(12,6);not null;default:0" json:"cost_per_mtok"`
	SellPerMTok float64   `gorm:"type:decimal(12,6);not null;default:0" json:"sell_per_mtok"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (PricingRuleTier) TableName() string {
	return "pricing_rule_tier"
}
