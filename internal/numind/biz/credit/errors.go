package credit

import "errors"

// Credits biz 层 typed sentinel errors
// 调用方使用 errors.Is(err, credit.ErrXxx) 区分业务错误 vs 真错误

var (
	// ErrInsufficientCredits 余额不足（预扣或扣减时判定）
	ErrInsufficientCredits = errors.New("credit: insufficient balance")

	// ErrAlreadyFinalized Reservation 已是终态（reconciled/refunded/expired），再次 Reconcile/Refund 返回此错误
	// 调用方应 errors.Is 判断后忽略（幂等性保证）
	ErrAlreadyFinalized = errors.New("credit: reservation already finalized")

	// ErrReservationNotFound Reservation 记录不存在
	ErrReservationNotFound = errors.New("credit: reservation not found")

	// ErrCoefficientNotFound 估算系数未配置（provider+model+operation 组合无 is_active=1 行）
	ErrCoefficientNotFound = errors.New("credit: estimation coefficient not found for model")

	// ErrCoefficientConcurrent UpdateCoefficient 并发 retry 3 次后仍失败
	// HTTP 503 语义：建议 controller 层包装为 errno.ErrCoefficientConcurrent 返回给前端
	ErrCoefficientConcurrent = errors.New("credit: coefficient update concurrent conflict, retry exhausted")

	// ErrUnknownBudgetOperation is returned by CheckAndEstimateBudget when the
	// caller passes a billing operation that is not in the budgetOperationMap
	// AND user billing context is present (i.e., the caller intends to charge).
	// Fail-closed: unknown operations MUST NOT silently bill via a default operation.
	//
	// Spec §6.1.1 names this error "ErrContextConfigInvalid" at the Gateway
	// layer (defined in the contextbudget package, Task 5+). Future callers
	// at the Gateway middleware boundary should wrap this credit-package
	// error as contextbudget.ErrContextConfigInvalid to surface a uniform
	// typed sentinel to upstream code without creating a credit→contextbudget
	// import cycle. See spec §10.
	ErrUnknownBudgetOperation = errors.New("credit: unknown budget operation — cannot normalize to a chargeable credit operation")
)
