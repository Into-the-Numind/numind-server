package model

import "time"

// UsageRecord 用量明细记录表 — 每次外部 API 调用记录一条
type UsageRecord struct {
	ID               uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID           uint   `gorm:"not null;index:idx_ur_user_created" json:"user_id"`
	ServiceType      string `gorm:"size:50;not null;index:idx_ur_service" json:"service_type"` // llm_chat, llm_vision, embedding, rerank, cos_upload, file_extract, vector_db
	Provider         string `gorm:"size:50;not null" json:"provider"`                          // ali, volc, dmxapi, cos, vikingdb, dashvector, bailian
	Model            string `gorm:"size:100" json:"model"`                                     // 模型名称，如 qwen-turbo, deepseek-v3-250324
	Operation        string `gorm:"size:100;not null;index:idx_ur_operation" json:"operation"` // 业务扣费点: sop_node_execute, salesrag_chat 等
	PromptTokens     int    `gorm:"default:0" json:"prompt_tokens"`
	CompletionTokens int    `gorm:"default:0" json:"completion_tokens"`
	TotalTokens      int    `gorm:"default:0" json:"total_tokens"`
	ReasoningTokens  int    `gorm:"default:0" json:"reasoning_tokens"`
	// CachedPromptTokens is the subset of PromptTokens served from the provider's
	// prompt cache (OpenAI usage.prompt_tokens_details.cached_tokens / DeepSeek
	// prompt_cache_hit_tokens, both via the OpenAI-compatible DMXAPI endpoint).
	// Additive: default 0 = no cache = identical to pre-cache billing audit.
	CachedPromptTokens    int    `gorm:"column:cached_prompt_tokens;default:0" json:"cached_prompt_tokens"`
	EstimatedPromptTokens int    `gorm:"default:0" json:"estimated_prompt_tokens"`
	BytesUploaded         int64  `gorm:"default:0" json:"bytes_uploaded"`  // COS 上传字节数
	ItemCount             int    `gorm:"default:0" json:"item_count"`      // 向量操作条数 / Rerank 文档数
	CostCents             int64  `gorm:"default:0" json:"cost_cents"`      // 预估成本（分）
	RevenueCents          int64  `gorm:"default:0" json:"revenue_cents"`   // 客户计费金额（分）
	BizRefType            string `gorm:"size:50" json:"biz_ref_type"`      // 关联业务对象类型: sop_run, sales_session 等
	BizRefID              uint   `gorm:"default:0" json:"biz_ref_id"`      // 关联业务对象 ID
	IsFallback            bool   `gorm:"default:false" json:"is_fallback"` // 是否为降级调用
	Metadata              string `gorm:"type:json" json:"metadata"`        // 额外上下文 JSON

	// AI Service Manager 扩展字段（nullable；历史数据保持 null）
	// TaskID links to task_profile.task_id; null = legacy data or non-AI call.
	// *string so zero value writes SQL NULL (not empty string) for legacy records.
	TaskID *string `gorm:"column:task_id;size:80;default:null" json:"task_id,omitempty"`
	// Unit describes how the call is priced: per_1m_tokens | per_call.
	// *string so zero value writes SQL NULL (not empty string) for legacy records.
	Unit *string `gorm:"column:unit;size:20;default:null" json:"unit,omitempty"`
	// CallCount is the number of API calls for per_call billing (OCR etc.).
	CallCount *int `gorm:"column:call_count;default:null" json:"call_count,omitempty"`
	// DurationSeconds is the audio duration (seconds) of an ASR response, written
	// for business-analysis metadata (e.g. "total audio minutes processed this
	// month", long-audio investigations). It is NOT used by the billing path —
	// ASR billing is per_call via pricing_rule. Left here as analytic observability.
	DurationSeconds *float64 `gorm:"column:duration_seconds;type:decimal(10,3);default:null" json:"duration_seconds,omitempty"`
	// PricingInputSnapshot records the input token price at time of call.
	PricingInputSnapshot *float64 `gorm:"column:pricing_input_snapshot;type:decimal(10,6);default:null" json:"pricing_input_snapshot,omitempty"`
	// PricingOutputSnapshot records the output token price at time of call.
	PricingOutputSnapshot *float64 `gorm:"column:pricing_output_snapshot;type:decimal(10,6);default:null" json:"pricing_output_snapshot,omitempty"`
	// PricingCallSnapshot records the per-call price at time of call.
	PricingCallSnapshot *float64 `gorm:"column:pricing_call_snapshot;type:decimal(10,6);default:null" json:"pricing_call_snapshot,omitempty"`
	// IsEstimated is true when streaming was interrupted and token count is estimated.
	IsEstimated bool `gorm:"column:is_estimated;default:false" json:"is_estimated"`

	CreatedAt time.Time `gorm:"index:idx_ur_user_created" json:"created_at"`
}

// TableName 指定表名
func (UsageRecord) TableName() string {
	return "usage_record"
}

// PricingRule 定价规则表
type PricingRule struct {
	ID                     uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	ServiceType            string  `gorm:"size:50;not null;uniqueIndex:uk_pricing_lookup" json:"service_type"`
	Provider               string  `gorm:"size:50;not null;uniqueIndex:uk_pricing_lookup" json:"provider"`
	Model                  string  `gorm:"size:100;uniqueIndex:uk_pricing_lookup" json:"model"`                                               // 空字符串 = 该 service+provider 的默认价格
	BillingMode            string  `gorm:"size:20;not null;default:'flat'" json:"billing_mode"`                                               // tiered_token | flat
	FlatUnit               string  `gorm:"size:10;not null;default:'call'" json:"flat_unit"`                                                  // call | gb
	InputPricePerMTok      float64 `gorm:"column:input_price_per_m_tok;type:decimal(10,4);default:0" json:"input_price_per_mtok"`             // 每百万输入 tokens 价格（元）
	OutputPricePerMTok     float64 `gorm:"column:output_price_per_m_tok;type:decimal(10,4);default:0" json:"output_price_per_mtok"`           // 每百万输出 tokens 价格（元）
	PricePerCall           float64 `gorm:"column:price_per_call;type:decimal(10,4);default:0" json:"price_per_call"`                          // 每次调用价格（元）
	PricePerGB             float64 `gorm:"column:price_per_gb;type:decimal(10,4);default:0" json:"price_per_gb"`                              // 每 GB 价格（元，COS 用）
	SellInputPricePerMTok  float64 `gorm:"column:sell_input_price_per_m_tok;type:decimal(10,4);default:0" json:"sell_input_price_per_mtok"`   // 售价：每百万输入 tokens（元）
	SellOutputPricePerMTok float64 `gorm:"column:sell_output_price_per_m_tok;type:decimal(10,4);default:0" json:"sell_output_price_per_mtok"` // 售价：每百万输出 tokens（元）
	// CachedInputPricePerMTok is the COST price (¥/MTok) for cache-HIT prompt
	// tokens. Pointer + no default tag ⇒ column is NULLABLE; nil ⇒ "not set" ⇒
	// the cached portion is billed at the full InputPricePerMTok (byte-identical
	// to pre-cache behavior). Paired with SellCachedInputPricePerMTok.
	CachedInputPricePerMTok *float64 `gorm:"column:cached_input_price_per_m_tok;type:decimal(10,4)" json:"cached_input_price_per_mtok,omitempty"` // 成本价：每百万缓存命中输入 tokens（元）；NULL=未设置，按全价计费
	// SellCachedInputPricePerMTok is the SELL price (¥/MTok) for cache-HIT prompt
	// tokens. nil ⇒ the cached portion is billed at the full SellInputPricePerMTok.
	// Paired with CachedInputPricePerMTok (set together or both NULL).
	SellCachedInputPricePerMTok *float64  `gorm:"column:sell_cached_input_price_per_m_tok;type:decimal(10,4)" json:"sell_cached_input_price_per_mtok,omitempty"` // 售价：每百万缓存命中输入 tokens（元）；NULL=未设置，按全价计费
	SellPricePerCall            float64   `gorm:"column:sell_price_per_call;type:decimal(10,4);default:0" json:"sell_price_per_call"`                            // 售价：每次调用（元）
	SellPricePerGB              float64   `gorm:"column:sell_price_per_gb;type:decimal(10,4);default:0" json:"sell_price_per_gb"`                                // 售价：每 GB（元）
	CreditMultiplier            float64   `gorm:"column:credit_multiplier;type:decimal(5,2);default:1.00" json:"credit_multiplier"`                              // 积分消耗倍率，默认 1.00
	IsActive                    bool      `gorm:"default:true" json:"is_active"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
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
	CostPerMTok float64   `gorm:"column:cost_per_mtok;type:decimal(12,6);not null;default:0" json:"cost_per_mtok"`
	SellPerMTok float64   `gorm:"column:sell_per_mtok;type:decimal(12,6);not null;default:0" json:"sell_per_mtok"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (PricingRuleTier) TableName() string {
	return "pricing_rule_tier"
}
