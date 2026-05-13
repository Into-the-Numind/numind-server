package errno

var (
	// OK 代表请求成功.
	OK = &Errno{HTTP: 200, Code: "", Message: ""}

	// InternalServerError 表示所有未知的服务器端错误.
	InternalServerError = &Errno{HTTP: 500, Code: "InternalError", Message: "Internal server error."}

	// ErrPageNotFound 表示路由不匹配错误.
	ErrPageNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.PageNotFound", Message: "Page not found."}

	// ErrBind 表示参数绑定错误.
	ErrBind = &Errno{HTTP: 400, Code: "InvalidParameter.BindError", Message: "Error occurred while binding the request body to the struct."}

	// ErrInvalidParameter 表示所有验证失败的错误.
	ErrInvalidParameter = &Errno{HTTP: 400, Code: "InvalidParameter", Message: "Parameter verification failed."}

	// ErrSignToken 表示签发 JWT Token 时出错.
	ErrSignToken = &Errno{HTTP: 401, Code: "AuthFailure.SignTokenError", Message: "Error occurred while signing the JSON web token."}

	// ErrTokenInvalid 表示 JWT Token 格式错误.
	ErrTokenInvalid = &Errno{HTTP: 401, Code: "AuthFailure.TokenInvalid", Message: "Token was invalid."}

	// ErrUnauthorized 表示请求没有被授权.
	ErrUnauthorized = &Errno{HTTP: 401, Code: "AuthFailure.Unauthorized", Message: "Unauthorized."}

	// ErrForbidden 表示请求被禁止.
	ErrForbidden = &Errno{HTTP: 403, Code: "AuthFailure.Forbidden", Message: "Forbidden."}

	// ErrInternalServer 表示内部服务器错误.
	ErrInternalServer = &Errno{HTTP: 500, Code: "InternalError", Message: "Internal server error."}

	// SOP / Chatbot 可见范围权限相关错误码 (sop-chatbot-visibility-scope).
	// 注: ErrChatbotNotFound 已存在于 config.go:4 (Code: "Config.ChatbotNotFound"), 直接复用.

	// ErrSopTemplateNotFound 表示 SOP 模板不存在.
	ErrSopTemplateNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.SopTemplateNotFound", Message: "SOP template was not found."}

	// ErrEntityNotOwnedByCaller 表示 caller 尝试操作不属于自己的实体 (SOP / chatbot).
	// 用于跨父账户越权场景: 父账户 A 尝试配置父账户 B 创建的实体.
	ErrEntityNotOwnedByCaller = &Errno{HTTP: 403, Code: "FailedOperation.EntityNotOwnedByCaller", Message: "The entity is not owned by the caller."}

	// ErrVisibilityPermissionDenied 表示子用户尝试调用 visibility 配置端点.
	// 与现有 ErrPermissionDenied / ErrForbidden 区分以便监控本功能滥用单独告警.
	ErrVisibilityPermissionDenied = &Errno{HTTP: 403, Code: "FailedOperation.VisibilityPermissionDenied", Message: "Only parent accounts can configure visibility scope."}

	// ErrCrossParentSubUser 表示父账户提交的 sub_user_id 用户存在, 但 parent_user_id 不属于自己.
	// 与 ErrSubUserNotFound 区分: 前者是 "存在但越权", 后者是 "用户不存在".
	ErrCrossParentSubUser = &Errno{HTTP: 422, Code: "InvalidParameter.CrossParentSubUser", Message: "One or more sub_user_ids do not belong to the caller."}

	// ErrSubUserNotFound 表示 sub_user_ids 中存在数据库中不存在的用户 ID (含软删).
	ErrSubUserNotFound = &Errno{HTTP: 422, Code: "InvalidParameter.SubUserNotFound", Message: "One or more sub_user_ids do not exist."}
)
