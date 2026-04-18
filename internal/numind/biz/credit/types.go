package credit

import "time"

// Operation 是"单次 LLM 调用级"的业务操作枚举
// 一次 SOP run 如含 N 个 node = N 次 LLM 调用 = 触发 N 轮 Reserve/Reconcile
type Operation string

const (
	OpSopRun          Operation = "sop_run"
	OpSopChat         Operation = "sop_chat"
	OpSalesragChat    Operation = "salesrag_chat"
	OpProfileAnalysis Operation = "profile_analysis"
	OpFileParse       Operation = "file_parse"
	OpStyleAnalysis   Operation = "style_analysis"
	OpOCR             Operation = "ocr"
)

// ReservationStatus 预扣记录状态机：reserved → reconciled | refunded | expired
type ReservationStatus string

const (
	StatusReserved   ReservationStatus = "reserved"
	StatusReconciled ReservationStatus = "reconciled"
	StatusRefunded   ReservationStatus = "refunded"
	StatusExpired    ReservationStatus = "expired"
)

// EstimationInput 估算函数输入
type EstimationInput struct {
	PromptChars int    // 前端/后端渲染得到的 prompt 字符数
	Model       string // "qwen-turbo" / "deepseek-v3-2-251201" / ...
	Provider    string // "ali" / "volc" / "dmxapi" / "baidu"
}

// PreCheckResult 运行前检查结果
// 调用方据 SkipDeduction 决定是否进入 Reserve/Reconcile 扣减块
type PreCheckResult struct {
	SkipDeduction    bool             // legacy_tier = true，credits = false
	Sufficient       bool             // credits 模式下余额是否足够
	EstimatedCredits int64            // 预估消耗积分
	CoefficientID    uint64           // 外键 credit_estimation_coefficient.id
	Balance          BalanceBreakdown // 当前余额快照
	Reason           string           // legacy_tier 次数不足时填入 CanRunSOP 返回的中文原因
}

// Reservation 预扣记录
type Reservation struct {
	ID              uint64
	UserID          uint
	ReferenceType   string            // "sop_run" / "sop_chat" / "salesrag_chat"
	ReferenceID     string            // 业务 ID
	Operation       Operation
	ReservedCredits int64
	CoefficientID   uint64
	Status          ReservationStatus
	ActualCostCents *int64
	Delta           *int64  // actual - reserved：正=补扣，负=退还
	FinalizeReason  *string
	IdempotencyKey  *string
	Items           []ReservationItem // FIFO 扣减明细
	CreatedAt       time.Time
	ReconciledAt    *time.Time
}

// ReservationItem FIFO 扣减单项（快照）
type ReservationItem struct {
	PackageID        uint64
	Credits          int64
	PackageType      string // "trial" / "subscription" / "booster"
	PackageExpiresAt time.Time
	Seq              int // FIFO 顺序号（1, 2, ...）
}

// PackageDeduction DeductCreditsTx 返回的扣减明细
type PackageDeduction struct {
	PackageID   uint64
	Credits     int64
	PackageType string
	ExpiresAt   time.Time
}

// BalanceBreakdown GetBalance 返回的余额视图（按 billing_mode 分发）
// JSON 短字段名与现有 numind-web-v3/src/api/credits.ts 对齐
type BalanceBreakdown struct {
	BillingMode string `json:"billing_mode"` // "credits" | "legacy_tier"

	// credits 模式字段
	SubRemain                int64      `json:"sub_remain"`
	SubTotal                 int64      `json:"sub_total"`
	SubExpiresAt             *time.Time `json:"sub_expires_at,omitempty"`
	BoosterRemain            int64      `json:"booster_remain"`
	BoosterTotal             int64      `json:"booster_total"`
	BoosterEarliestExpiresAt *time.Time `json:"booster_earliest_expires_at,omitempty"`

	// legacy_tier 模式字段
	RemainingRuns *int `json:"remaining_runs,omitempty"` // nil = premium unlimited
	MonthlyLimit  *int `json:"monthly_limit,omitempty"`
}
