package errno

// Skill Marketplace v2 错误码 (agent-mode-v2-skill-marketplace, spec §4.3 修订版).
//
// HTTP status codes assigned per RFC 7231 semantics:
//   - 403 forbidden: tenant-rule violations (child account, not owner)
//   - 404 not found: missing marketplace/subscription
//   - 409 conflict: duplicate state (already published / already subscribed / self-subscribe)
//   - 422 unprocessable: confirmation hash mismatch (well-formed but semantically invalid)
//   - 503 service unavailable: sanitize LLM down
//   - 400 bad request: empty skill body etc.
//
// Code namespace: `Marketplace.*` — distinct from `SkillArtifact.*` (#1) and
// `Skill.*` (#5 v1 internal skill). Frontend dispatches on Code string.
//
// biz/marketplace re-aliases these via `var Err... = errno.Err...` so the
// existing `errors.Is(err, biz.Err...)` checks (controller mapping, tests)
// keep working. core.WriteResponse → errno.Decode unwraps the chain and
// uses HTTP / Code / Message from the *Errno pointer.
var (
	// ErrChildAccountCannotAccessMarketplace 表示子账户尝试访问 marketplace 系统。
	// 仅父账户 (User.ParentUserID IS NULL) 可使用 marketplace.
	ErrChildAccountCannotAccessMarketplace = &Errno{HTTP: 403, Code: "Marketplace.ChildAccountForbidden", Message: "子账户无法访问技能市场"}

	// ErrSkillNotOwned 表示发布方尝试操作不属于自己的 Skill（跨租户违规）。
	ErrSkillNotOwned = &Errno{HTTP: 403, Code: "Marketplace.SkillNotOwned", Message: "无权操作此技能"}

	// ErrSkillAlreadyPublished 表示同一 source_skill_id 已存在活跃的 marketplace 行。
	// S2-D1 uniqueness pre-check. HTTP 409 Conflict 符合 RFC 7231 — 资源已存在的冲突。
	ErrSkillAlreadyPublished = &Errno{HTTP: 409, Code: "Marketplace.SkillAlreadyPublished", Message: "该技能已上架，请先下架再重新发布"}

	// ErrSelfSubscribeForbidden 表示发布方尝试订阅自己发布的项目（无意义）。
	// HTTP 409 Conflict — 状态冲突 (publisher == subscriber).
	ErrSelfSubscribeForbidden = &Errno{HTTP: 409, Code: "Marketplace.SelfSubscribeForbidden", Message: "不能订阅自己发布的技能"}

	// ErrAlreadySubscribed 表示同一 (subscriber_user_id, marketplace_id) 已有订阅。
	// UNIQUE 约束 pre-check. HTTP 409 Conflict.
	ErrAlreadySubscribed = &Errno{HTTP: 409, Code: "Marketplace.AlreadySubscribed", Message: "已订阅该技能"}

	// ErrMarketplaceNotFound 表示市场项目不存在 / 已下架 / 对当前 caller 不可见。
	ErrMarketplaceNotFound = &Errno{HTTP: 404, Code: "Marketplace.NotFound", Message: "市场项目不存在或已下架"}

	// ErrSubscriptionNotFound 表示订阅记录不存在（含跨用户尝试取消订阅别人的订阅）。
	ErrSubscriptionNotFound = &Errno{HTTP: 404, Code: "Marketplace.SubscriptionNotFound", Message: "订阅记录不存在"}

	// ErrSanitizeUnavailable 表示脱敏 LLM 服务调用失败（quota / 网络 / provider down 等）。
	// 业务层将原始错误用 fmt.Errorf("%w: %s", ErrSanitizeUnavailable, cause) 包装，
	// errors.Is(err, ErrSanitizeUnavailable) 仍可识别。
	ErrSanitizeUnavailable = &Errno{HTTP: 503, Code: "Marketplace.SanitizeUnavailable", Message: "脱敏服务暂不可用，请稍后重试"}

	// ErrSanitizeConfirmationMismatch 表示前端 confirmed_sanitized_body 与服务端重跑
	// 结果差异 >5% (S2-D2 normalized hash check)，疑似篡改。
	// HTTP 422 — request 语法合法但语义不可处理.
	ErrSanitizeConfirmationMismatch = &Errno{HTTP: 422, Code: "Marketplace.SanitizeConfirmationMismatch", Message: "脱敏内容与确认不符，请重新预览"}

	// ErrSkillBodyEmpty 表示 Skill body_md 为空字符串（无法发布或脱敏）。
	ErrSkillBodyEmpty = &Errno{HTTP: 400, Code: "Marketplace.SkillBodyEmpty", Message: "技能正文为空，无法发布"}
)
