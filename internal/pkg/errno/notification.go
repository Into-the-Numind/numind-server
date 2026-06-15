package errno

// Notification center error codes (notification-center §4)

var (
	// ErrFeatureDisabled feature flag 关闭时所有通知路由返回此错误。
	// HTTP 404（功能未启用对外表现为不存在）
	ErrFeatureDisabled = &Errno{HTTP: 404, Code: "ResourceNotFound.FeatureDisabled", Message: "功能未启用"}

	// ErrAnnouncementNotFound 公告不存在或对当前用户不可见。
	// HTTP 404（Not Found）
	ErrAnnouncementNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.AnnouncementNotFound", Message: "公告不存在"}

	// ErrAnnouncementNotSurvey 对非 survey 类型公告提交答卷。
	// HTTP 400（Bad Request）
	ErrAnnouncementNotSurvey = &Errno{HTTP: 400, Code: "InvalidParameter.NotASurvey", Message: "该公告不是问卷"}

	// ErrSurveyAlreadySubmitted 重复提交答卷（一人一答）。
	// HTTP 409（Conflict）
	ErrSurveyAlreadySubmitted = &Errno{HTTP: 409, Code: "FailedOperation.SurveyAlreadySubmitted", Message: "您已提交过该问卷"}

	// ErrSurveyValidation 答案/题目校验失败（用 SetMessage 附细节）。
	// HTTP 400（Bad Request）
	ErrSurveyValidation = &Errno{HTTP: 400, Code: "InvalidParameter.SurveyValidation", Message: "问卷校验失败"}

	// ErrAnnouncementStatus 状态非法（publish 非 draft / 改已发布问卷题目 等）。
	// HTTP 400（Bad Request）
	ErrAnnouncementStatus = &Errno{HTTP: 400, Code: "FailedOperation.InvalidAnnouncementStatus", Message: "公告状态不允许此操作"}
)
