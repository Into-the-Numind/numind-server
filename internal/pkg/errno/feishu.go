package errno

// 飞书集成错误码 (feishu-integration §5)。
//
// 设计约束（design.md §8）：飞书工具的 Execute 失败一律 returnSoftError（绝不
// 硬错误杀 run）。下列 sentinel 供工具/client/service 层分类用途：
//   - ErrLarkNotConnected / ErrLarkReauthRequired 在工具内被翻译成软错误提示，
//     引导用户去连接/重新授权（不终止 agent run）。
//   - 工具/client 常用 fmt.Errorf("...: %w", ErrXxx) 包装，errno.Decode 经
//     errors.As 解 wrap 链仍能命中（见 errno.go Decode）。

var (
	// ErrLarkNotConnected 用户尚未连接飞书（user_third_party_account 无记录）。
	// HTTP 400（前置条件未满足，需先走连接流）。
	ErrLarkNotConnected = &Errno{HTTP: 400, Code: "Lark.NotConnected", Message: "尚未连接飞书，请先在设置中连接飞书账号"}

	// ErrLarkReauthRequired token 过期且无法刷新（无 refresh_token 或刷新失败）。
	// 工具层据此提示用户重新授权；HTTP 401（授权失效）。
	ErrLarkReauthRequired = &Errno{HTTP: 401, Code: "Lark.ReauthRequired", Message: "飞书授权已过期，请重新授权"}

	// ErrLarkCallFailed 飞书开放平台接口调用失败（上游错误，用 SetMessage 附细节）。
	// HTTP 502（Bad Gateway，区分本服务自身错误与上游飞书错误）。
	ErrLarkCallFailed = &Errno{HTTP: 502, Code: "Lark.CallFailed", Message: "飞书接口调用失败"}

	// ErrFeishuLifecycleConflict means the authorization state changed without
	// a safe live replacement action in this response.
	ErrFeishuLifecycleConflict = &Errno{HTTP: 409, Code: "Conflict.FeishuLifecycle", Message: "飞书授权状态已更新，请使用最新步骤"}

	// ErrFeishuDependencyUnavailable is a stable retryable response for the
	// CLI, Feishu API, or persistence dependencies behind authorization.
	ErrFeishuDependencyUnavailable = &Errno{HTTP: 503, Code: "ServiceUnavailable.Feishu", Message: "飞书授权服务暂时不可用，请稍后重试"}
)

// 注：device-code 方案（G2-authorize）移除了 redirect-OAuth callback，原
// ErrLarkStateInvalid（state 校验失败）已随之删除。
