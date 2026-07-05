package errno

var (
	ErrXhsScriptNoteNotFound       = &Errno{HTTP: 404, Code: "ResourceNotFound.XhsScriptNoteNotFound", Message: "小红书视频笔记不存在"}
	ErrXhsScriptVideoOnly          = &Errno{HTTP: 400, Code: "InvalidParameter.XhsScriptVideoOnly", Message: "当前版本只支持小红书视频笔记"}
	ErrXhsScriptProfileRequired    = &Errno{HTTP: 400, Code: "InvalidParameter.XhsScriptProfileRequired", Message: "请先填写产品/服务定位"}
	ErrXhsScriptTranscriptNotReady = &Errno{HTTP: 409, Code: "FailedOperation.XhsScriptTranscriptNotReady", Message: "视频转写尚未完成"}
	ErrXhsScriptQuotaInsufficient  = &Errno{HTTP: 402, Code: "FailedOperation.XhsScriptQuotaInsufficient", Message: "生成次数不足，请购买后继续"}
)
