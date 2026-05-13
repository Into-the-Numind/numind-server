package errno

// Credits system error codes
// See spec: numind-server/docs/superpowers/specs/2026-04-18-credits-system-design.md §3.12

var (
	// ErrInsufficientCredits 积分不足
	// HTTP 402（Payment Required）
	ErrInsufficientCredits = &Errno{HTTP: 402, Code: "Credits.Insufficient", Message: "积分不足"}

	// ErrMembershipRequired 购买加量包需要会员资格
	// HTTP 403（用户已认证但权限不够）
	ErrMembershipRequired = &Errno{HTTP: 403, Code: "Membership.Required", Message: "需要会员资格才能购买加量包"}

	// ErrBoosterNotAvailableForLegacy 老会员制（legacy_tier）不支持加量包（P4a 决策）
	ErrBoosterNotAvailableForLegacy = &Errno{HTTP: 403, Code: "Booster.LegacyTierNotAllowed", Message: "老会员制暂不支持加量包，到期升级后可购"}

	// ErrCoefficientConcurrent 系数并发更新冲突 retry 耗尽
	ErrCoefficientConcurrent = &Errno{HTTP: 503, Code: "Coefficient.Concurrent", Message: "系数更新繁忙，请稍后重试"}

	// ErrTierInPeriod 在期会员不可购买同类或更低类型（防提前续费）
	ErrTierInPeriod = &Errno{HTTP: 400, Code: "Tier.InPeriod", Message: "当前会员在期，不可购买同类或更低类型"}

	// ErrTrialAlreadyPurchased 已购买过体验卡，不可重复购买
	ErrTrialAlreadyPurchased = &Errno{HTTP: 400, Code: "Trial.AlreadyPurchased", Message: "您已购买过体验卡"}

	// ErrTrialNotAvailableInPeriod 在期会员不支持购买体验卡
	ErrTrialNotAvailableInPeriod = &Errno{HTTP: 400, Code: "Trial.NotAvailableInPeriod", Message: "在期会员不支持购买体验卡"}

	// ErrMembershipSelfPurchaseDisabled Q1 B2B2C: C 端不支持自购会员（trial/monthly/yearly），
	// 只能通过父账户"帮开通"路径赋予。booster(加量包) 不受此限制。
	ErrMembershipSelfPurchaseDisabled = &Errno{HTTP: 403, Code: "Membership.SelfPurchaseDisabled", Message: "请联系管理员开通会员"}

	// ErrTrialAlreadyGranted 该账户已使用过体验包，不可重复开通（Phase 2）
	// HTTP 409（Conflict）
	ErrTrialAlreadyGranted = &Errno{HTTP: 409, Code: "Trial.AlreadyGranted", Message: "该账户已使用过体验包"}

	// ErrTrialNotAllowedForActivePro 已是 Pro 会员，不能再开通试用包（Phase 2）
	// HTTP 409（Conflict）
	ErrTrialNotAllowedForActivePro = &Errno{HTTP: 409, Code: "Trial.NotAllowedForActivePro", Message: "已是 Pro 会员，不能再开通试用包"}

	// ErrChildNotMember 子账户当前不是会员（Phase 2 B2B2C 补丁）
	// HTTP 403（Forbidden）
	ErrChildNotMember = &Errno{HTTP: 403, Code: "Membership.ChildNotMember", Message: "子账户当前不是会员"}

	// ErrNotActiveMember 当前不是会员状态（非试用、标准或高级）（Phase 2）
	// HTTP 403（Forbidden）
	ErrNotActiveMember = &Errno{HTTP: 403, Code: "Membership.NotActiveMember", Message: "当前不是会员状态"}

	// ErrBoosterQuantityExceedsLimit 单次购买加量包数量超过 10000 份限制（Phase 2）
	// HTTP 400（Bad Request）
	ErrBoosterQuantityExceedsLimit = &Errno{HTTP: 400, Code: "Booster.QuantityExceedsLimit", Message: "单次最多购买 10000 份"}

	// ErrSubscriptionExpired 订阅已过期（Phase 2）
	// HTTP 410（Gone）
	ErrSubscriptionExpired = &Errno{HTTP: 410, Code: "Subscription.Expired", Message: "订阅已过期"}

	// ErrIdempotencyKeyConflict 幂等键冲突：同一幂等键被用于不同请求体（Phase 2）
	// HTTP 409（Conflict）
	ErrIdempotencyKeyConflict = &Errno{HTTP: 409, Code: "Idempotency.KeyConflict", Message: "幂等键冲突（同一 key 不同请求体）"}

	// ErrSystemMaintenance 系统维护中，暂时不可用（Phase 2/3）
	// HTTP 503（Service Unavailable）
	ErrSystemMaintenance = &Errno{HTTP: 503, Code: "System.Maintenance", Message: "系统维护中"}

	// ErrInvalidProductType 不支持的产品类型（Phase 2 §5.10）
	// 订单接口只接受 booster；trial/monthly/yearly 必须走 grant 路径。
	// HTTP 400（Bad Request）
	ErrInvalidProductType = &Errno{HTTP: 400, Code: "Order.InvalidProductType", Message: "订单接口仅支持加量包，会员请通过管理员开通"}
)
