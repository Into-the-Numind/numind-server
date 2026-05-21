package errno

var (
	ErrComplianceL0Violation       = &Errno{HTTP: 422, Code: "BizError.ComplianceL0Violation", Message: "这个问题有点超出我的范围，我更擅长帮你解决学习相关事项。"}
	ErrComplianceL1Violation       = &Errno{HTTP: 422, Code: "BizError.ComplianceL1Violation", Message: "这个问题暂时无法回答"}
	ErrComplianceInjectionDetected = &Errno{HTTP: 422, Code: "BizError.ComplianceInjectionDetected", Message: "检测到不安全的输入内容，无法处理"}
	ErrComplianceFenceViolation    = &Errno{HTTP: 422, Code: "BizError.ComplianceFenceViolation", Message: "系统内部错误，请重试"}
	ErrComplianceScopeViolation    = &Errno{HTTP: 500, Code: "BizError.ComplianceScopeViolation", Message: "系统内部错误"}
	ErrComplianceRuleNotFound      = &Errno{HTTP: 404, Code: "BizError.ComplianceRuleNotFound", Message: "规则不存在"}
)
