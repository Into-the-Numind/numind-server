package errno

var (
	ErrXhsUserNotFound         = &Errno{HTTP: 404, Code: "Monitor.XhsUserNotFound", Message: "小红书用户不存在"}
	ErrXhsFetchFailed          = &Errno{HTTP: 502, Code: "Monitor.XhsFetchFailed", Message: "小红书数据获取失败"}
	ErrBloggerAlreadyMonitored = &Errno{HTTP: 409, Code: "Monitor.BloggerAlreadyMonitored", Message: "该博主已在监控列表中"}
	ErrInvalidCronExpression   = &Errno{HTTP: 400, Code: "Monitor.InvalidCronExpression", Message: "无效的 cron 表达式"}
	ErrCheckCooldown           = &Errno{HTTP: 429, Code: "Monitor.CheckCooldown", Message: "操作过于频繁，请稍后再试"}
	ErrAnalyzeCooldown         = &Errno{HTTP: 429, Code: "Monitor.AnalyzeCooldown", Message: "AI 分析操作过于频繁，请稍后再试"}
	ErrBriefingAlreadyExists   = &Errno{HTTP: 409, Code: "Monitor.BriefingAlreadyExists", Message: "该周期的简报已存在"}
	ErrXhsNotBound             = &Errno{HTTP: 400, Code: "Monitor.XhsNotBound", Message: "小红书账号未绑定"}
	ErrXhsQRSessionNotFound    = &Errno{HTTP: 404, Code: "Monitor.XhsQRSessionNotFound", Message: "二维码会话不存在或已过期"}
	ErrXhsQRLoginFailed        = &Errno{HTTP: 502, Code: "Monitor.XhsQRLoginFailed", Message: "小红书二维码登录失败"}
)
