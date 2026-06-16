package errno

// AI Service Manager errors.
//
// Namespace: AIService.*
// Spec reference: docs/superpowers/specs/2026-04-15-ai-service-manager-design.md §4.3
//
// Note: The existing codebase uses string-based Code values (e.g. "Monitor.XhsFetchFailed")
// rather than numeric codes. These errors follow the same convention. The spec referenced
// codes 41001-41006 as numeric identifiers; this implementation maps them to the string
// naming pattern used project-wide.

var (
	// ErrAIServiceNotFound is returned when a requested AI service does not exist.
	ErrAIServiceNotFound = &Errno{HTTP: 404, Code: "AIService.ServiceNotFound", Message: "AI 服务不存在"}

	// ErrAITaskNotFound is returned when a requested task profile does not exist.
	ErrAITaskNotFound = &Errno{HTTP: 404, Code: "AIService.TaskNotFound", Message: "Task Profile 不存在"}

	// ErrAICapabilityMismatch is returned when the task's capability requirements
	// cannot be satisfied by the target service.
	ErrAICapabilityMismatch = &Errno{HTTP: 422, Code: "AIService.CapabilityMismatch", Message: "AI 服务能力不匹配，任务需求无法满足"}

	// ErrAIFallbackExhausted is returned when both primary and all fallback services fail.
	ErrAIFallbackExhausted = &Errno{HTTP: 502, Code: "AIService.FallbackExhausted", Message: "所有 AI 服务（含 fallback）均不可用"}

	// ErrAIServiceDeprecated is returned when an attempt is made to use a deprecated service.
	ErrAIServiceDeprecated = &Errno{HTTP: 410, Code: "AIService.ServiceDeprecated", Message: "AI 服务已下架"}

	// ErrAICapabilityOverrideRequiresReason is returned when a force-bind operation
	// (capability.override) is attempted without providing a reason.
	ErrAICapabilityOverrideRequiresReason = &Errno{HTTP: 400, Code: "AIService.OverrideRequiresReason", Message: "强制覆盖操作必须填写原因"}

	// ErrAIServiceUnbound is returned when a Task Profile has no default_service_id bound.
	ErrAIServiceUnbound = &Errno{HTTP: 424, Code: "AIService.Unbound", Message: "Task Profile 未绑定服务"}

	// ErrAIProviderTimeout is returned when the upstream AI provider call times out.
	ErrAIProviderTimeout = &Errno{HTTP: 504, Code: "AIService.ProviderTimeout", Message: "AI 服务调用超时"}

	// ErrAIProviderError is returned when the upstream AI provider returns a 5xx or
	// network-level error.
	ErrAIProviderError = &Errno{HTTP: 502, Code: "AIService.ProviderError", Message: "AI 服务调用失败"}

	// ErrImageTooLarge is returned when an uploaded image exceeds a provider's
	// pixel-dimension or byte-size limit (e.g. claude/dmxapi reject any side
	// >8000px). The upload-time normalizer (imageutil) handles most cases; this is
	// the last-resort mapping for an image that still gets rejected upstream.
	ErrImageTooLarge = &Errno{HTTP: 400, Code: "AIService.ImageTooLarge", Message: "图片过大，请换一张更小的图片"}

	// ErrAIRestoreRequiresReason is returned when a restore operation is attempted
	// without providing a reason.
	ErrAIRestoreRequiresReason = &Errno{HTTP: 400, Code: "AIService.RestoreRequiresReason", Message: "恢复操作必须填写原因"}

	// ErrAIProviderNotFound is returned when a requested llm_provider does not exist.
	ErrAIProviderNotFound = &Errno{HTTP: 404, Code: "AIService.ProviderNotFound", Message: "AI 供应商不存在"}

	// ErrAIProviderInUse is returned when a provider delete is attempted but active routes
	// still reference the provider.
	ErrAIProviderInUse = &Errno{HTTP: 409, Code: "AIService.ProviderInUse", Message: "AI 供应商被路由引用，无法删除"}

	// ErrAIServiceModelKeyExists is returned when a service create is attempted with a
	// model_key that already exists in ai_service (unique index violation).
	ErrAIServiceModelKeyExists = &Errno{HTTP: 409, Code: "AIService.ModelKeyExists", Message: "AI 服务 model_key 已存在"}
)
