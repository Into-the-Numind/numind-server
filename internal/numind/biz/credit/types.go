package credit

import "time"

// Operation 是"单次 LLM 调用级"的业务操作枚举
// 一次 SOP run 如含 N 个 node = N 次 LLM 调用 = 触发 N 轮 Reserve/Reconcile
type Operation string

const (
	OpSopRun          Operation = "sop_run"
	OpSopChat         Operation = "sop_chat"
	OpSalesragChat    Operation = "salesrag_chat"
	OpChatbotChat     Operation = "chatbot_chat" // chatbot conversation (context-budget-compression feature)
	OpProfileAnalysis Operation = "profile_analysis"
	OpFileParse       Operation = "file_parse"
	OpStyleAnalysis   Operation = "style_analysis"
	OpOCR             Operation = "ocr"
)

// BudgetPrecheckInput holds the token-based inputs for CheckAndEstimateBudget.
// Unlike EstimationInput (which uses char count for R2 estimation), this input
// carries pre-computed token estimates from the context budget planner.
// Spec §6.1.1.
//
// Note (intentional divergence from spec §6.2 example): spec shows TokenProfileID
// and ContextBudgetEventID as *uint64 to express "absent". This implementation
// follows the S3 plan and uses value types with zero (0) as the absent sentinel:
//   - TokenProfileID == 0 → "no profile id" (treated as NULL in DB)
//   - ContextBudgetEventID == 0 → "no event id yet" (treated as NULL in DB)
//
// Callers from Gateway middleware (Task 5+) should pass 0 to signal absence,
// not a nil pointer. The DB layer (Task 1's *uint64 columns) handles the
// 0→NULL mapping inside reserveBudgetRow.
type BudgetPrecheckInput struct {
	UserID                    uint
	Operation                 string // raw billing operation (not yet normalized)
	EstimatedPromptTokens     int
	EstimatedCompletionTokens int
	Provider                  string
	Model                     string
	TokenProfileID            uint64 // 0 = no profile id
	ContextBudgetEventID      uint64 // 0 = no event id yet
}

// BudgetReservationInput extends BudgetPrecheckInput with reservation-only
// fields. Same value-vs-pointer convention as BudgetPrecheckInput; see its doc.
//
// IdempotencyKey == "" means "no idempotency key" (treated as no idempotency
// constraint). Spec §6.2 example uses *string; this struct uses string.
//
// Spec §6.1.2.
type BudgetReservationInput struct {
	BudgetPrecheckInput
	EstimatedCredits int64
	IdempotencyKey   string
	// Metadata is reserved for Task 5+ Gateway middleware to attach Langfuse
	// span metadata or trace context. The current credit-layer implementation
	// does NOT read this field — it is forwarded to UsageRecord/billing layer
	// by the caller (Gateway middleware), not by ReserveBudget itself.
	Metadata map[string]string
}

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
	ReferenceType   string // "sop_run" / "sop_chat" / "salesrag_chat"
	ReferenceID     string // 业务 ID
	Operation       Operation
	ReservedCredits int64
	CoefficientID   uint64
	Status          ReservationStatus
	ActualCostCents *int64
	Delta           *int64 // actual - reserved：正=补扣，负=退还
	FinalizeReason  *string
	IdempotencyKey  *string
	Items           []ReservationItem // FIFO 扣减明细
	CreatedAt       time.Time
	ReconciledAt    *time.Time

	// ActualPromptTokens / ActualCompletionTokens — caller 在 LLM 调用完成后、
	// defer FinalizeReservation 触发前写入；FinalizeReservation 把这两个值
	// 透传到 credit-reconcile span 的 metadata，用于 Langfuse 线上排障按真实
	// token 数对账 estimated vs actual。默认 0（未填写时 span 字段为 0，
	// 与改造前行为一致）。
	ActualPromptTokens     int
	ActualCompletionTokens int
}

// ReservationItem FIFO 扣减单项（快照）
//
// Dual-path: legacy reservations have PackageID set + SourceType/SourceID nil;
// new credits-mode reservations have PackageID nil + SourceType/SourceID set.
type ReservationItem struct {
	PackageID        *uint64
	SourceType       *string
	SourceID         *uint64
	Credits          int64
	PackageType      string // "trial" / "subscription" / "booster"
	PackageExpiresAt time.Time
	Seq              int // FIFO 顺序号（1, 2, ...）
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
	// TrialRemain is credits remaining in the trial_grant pool. Populated by
	// credits-mode GetBalance when MembershipService is wired. Pre-fix this
	// field was implicit in sub/booster sums; now it's its own field so the
	// pre-check total `SubRemain + BoosterRemain + TrialRemain` reflects the
	// real spendable balance.
	TrialRemain    int64      `json:"trial_remain"`
	TrialExpiresAt *time.Time `json:"trial_expires_at,omitempty"`
}
