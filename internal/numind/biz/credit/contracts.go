package credit

import (
	"context"
	"errors"
	"time"

	"numind-server/internal/pkg/model"
)

// AdminTestConsumer is the credit-package-local interface for the admin_test
// pool used by Agent Builder "试聊" path (#12 agent-mode-billing-integration).
//
// **Decoupling**: defined in credit/ to break the import cycle credit → budget
// → agent → salesrag → credit. The actual implementation lives in biz/budget
// (budget.adminTestConsumer); the biz.go wire layer adapts it to satisfy this
// interface (Status return-type conversion).
//
// Implementations MUST return ErrAdminTestExhausted (or the global
// errno.ErrAdminTestExhausted) when the pool is empty — Reserve treats it as
// the terminal "no more试聊配额" signal and refuses to fall back to正式三池.
type AdminTestConsumer interface {
	Consume(ctx context.Context, parentUserID uint, amount int64) (txID uint64, err error)
	Refund(ctx context.Context, parentUserID uint, txID uint64, refundAmount int64) error
	Status(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error)
}

// AdminTestStatus is the credit-local view of the parent's current-month
// admin_test grant. Mirrors budget.AdminTestStatus 1:1 by field — adapter
// converts between the two at the wire-layer boundary.
type AdminTestStatus struct {
	Granted      int64
	Used         int64
	Remaining    int64
	PeriodStart  time.Time
	PeriodEnd    time.Time
	DaysToExpire int
}

// ErrAdminTestExhausted is the credit-local sentinel mirroring
// budget.ErrAdminTestExhausted. Adapter at wire layer may pass through either —
// callers should compare via errors.Is.
var ErrAdminTestExhausted = errors.New("admin_test pool exhausted for parent user this month")

// ICreditService 是所有 AI 消耗点的统一 credits 计费入口
// Singleton，由 wire.go 注入；底层直接调用 creditsImpl（三池 SOT：trial_grant +
// credit_cycle + user_booster_balance）。Post legacy-deprecation (T1) the
// legacyTierImpl dispatch is removed.
//
// 并发安全：所有方法都是事务安全，Reserve/Reconcile/Refund 使用 SELECT ... FOR UPDATE 行锁
//
// 使用模式（见 spec §1.6）：
//
//	svc := creditSvc  // singleton
//	pre, err := svc.CheckAndEstimate(ctx, user, op, in)
//	if err != nil { return err }
//	rsv, err := svc.Reserve(ctx, user, op, pre.EstimatedCredits, pre.CoefficientID, &idempKey)
//	var actualCost int64
//	var opErr error
//	defer svc.FinalizeReservation(ctx, rsv, &actualCost, &opErr)
//	// ... LLM 调用 ...
//	actualCost, _ = pricing.CalculateCost(...)
type ICreditService interface {
	// CheckAndEstimate 运行前检查；计算 R2 估算 + 查余额；不足返回 ErrInsufficientCredits。
	CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error)

	// Reserve 预扣（Eager：同事务 DeductCreditsTx FIFO + 写 credit_reservation + items）。
	// idempotencyKey 允许 nil（InnoDB UNIQUE 允许多 NULL 共存，退化为非幂等）。
	Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error)

	// Reconcile 对账（幂等：终态 reservation 返回 ErrAlreadyFinalized sentinel）
	// 按 item.seq ASC 遍历，delta<0 退还、delta>0 补扣
	Reconcile(ctx context.Context, reservationID uint64, actualCostCents int64) error

	// Refund 退还（幂等，同上）
	// 按 item.seq ASC 原路退还到 item.package_id
	Refund(ctx context.Context, reservationID uint64, reason string) error

	// FinalizeReservation 唯一 defer 出口
	//   opErr 非 nil → Refund
	//   actualCost 已采集 → Reconcile
	//   否则 → Refund with reason=no_actual_cost
	// 传 rsv=nil 时 no-op 返回 nil
	FinalizeReservation(ctx context.Context, rsv *Reservation, actualCostCents *int64, opErr *error) error

	// GetBalance 余额查询：返回三池（sub + booster + trial）credits 分布。
	GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error)

	// CheckAndEstimateBudget is the budget-aware precheck entry point. It
	// normalizes the raw operation via budgetOperationMap (spec §6.1.1), then
	// computes EstimatedCredits from token counts via pricing. Unknown
	// operations return ErrUnknownBudgetOperation (fail-closed — never silently
	// bill via a default operation).
	//
	// Does NOT create a reservation. This is a parallel API to CheckAndEstimate
	// — the R2 char-based path is preserved unchanged.
	CheckAndEstimateBudget(ctx context.Context, user *model.User, input BudgetPrecheckInput) (*PreCheckResult, error)

	// ReserveBudget creates a credit_reservation with estimation_source='context_budget',
	// coefficient_id=NULL, and the token-profile/event metadata from BudgetReservationInput.
	// Spec §6.1.2.
	ReserveBudget(ctx context.Context, user *model.User, input BudgetReservationInput) (*Reservation, error)

	// ListConsumptionLog 返回用户「平账后真实消耗」流水（每动作一行，数据源
	// credit_reservation status=reconciled）。page 1-based、pageSize 默认 20 上限
	// 100（方法内归一化），返回总数。只读。
	ListConsumptionLog(ctx context.Context, userID uint, page, pageSize int) ([]ConsumptionLogItem, int64, error)
	// ReserveAgentTest is the Agent Builder "试聊" path prededuct (#12 agent-mode-billing-integration).
	// Reads from the parent user's admin_test pool (credit_admin_test_grant) — does NOT
	// fallback to trial/subscription/booster三池. Returns errno.ErrAdminTestExhausted on empty pool.
	// parentUser must be a parent account (ParentUserID == nil); enforcement is caller-side.
	ReserveAgentTest(ctx context.Context, parentUser *model.User, estimated int64, idempotencyKey *string) (*Reservation, error)

	// ReconcileAgentTest reconciles an agent_test reservation against actual cost (#12).
	// refund>0 → AdminTestConsumer.Refund; refund<0 → top up via Consume; both atomic.
	// Returns error if reservation is not agent_test or already reconciled.
	ReconcileAgentTest(ctx context.Context, reservationID uint64, actualCostCents int64) error
}
