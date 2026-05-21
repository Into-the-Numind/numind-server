package compliance

import "numind-server/internal/pkg/errno"

// Domain errors — 详细 errno 定义在 internal/pkg/errno/compliance.go (M3)
// 此处仅 re-export 供包内一致引用
var (
	ErrComplianceL0Violation       = errno.ErrComplianceL0Violation
	ErrComplianceL1Violation       = errno.ErrComplianceL1Violation
	ErrComplianceInjectionDetected = errno.ErrComplianceInjectionDetected
	ErrComplianceFenceViolation    = errno.ErrComplianceFenceViolation
	ErrComplianceScopeViolation    = errno.ErrComplianceScopeViolation
	ErrComplianceRuleNotFound      = errno.ErrComplianceRuleNotFound
)
