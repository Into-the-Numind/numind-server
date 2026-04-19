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
)
