package errno

// Document system error codes (document-system §3.7)
//
// 复用 ErrFeatureDisabled（notification.go）作为 feature flag 关闭时的 404。

var (
	// ErrDocumentNotFound 文档不存在或不属于当前用户（跨用户访问也返回此，不泄露存在性）。
	// HTTP 404
	ErrDocumentNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.DocumentNotFound", Message: "文档不存在"}

	// ErrDocumentSourceForbidden 源 object key 前缀非 agent-outputs/{自己userID}/（越权/非法来源）。
	// HTTP 403
	ErrDocumentSourceForbidden = &Errno{HTTP: 403, Code: "Forbidden.DocumentSourceForbidden", Message: "无权打开该来源文件"}

	// ErrDocumentSourceExpired 源 COS 对象已不存在（生成后久未打开被 GC / 已过期）。
	// HTTP 410
	ErrDocumentSourceExpired = &Errno{HTTP: 410, Code: "ResourceGone.DocumentSourceExpired", Message: "原文件已过期，无法打开"}

	// ErrDocumentNotEditable 该类型文件不支持在线编辑（仅文本类可编：md/txt/html/docx）。
	// HTTP 422
	ErrDocumentNotEditable = &Errno{HTTP: 422, Code: "InvalidParameter.DocumentNotEditable", Message: "该文件类型不支持在线编辑"}

	// ErrDocumentParseFailed 源文件解析为可编辑 markdown 失败。
	// HTTP 422
	ErrDocumentParseFailed = &Errno{HTTP: 422, Code: "InvalidParameter.DocumentParseFailed", Message: "文件解析失败"}

	// ErrDocumentTooLarge 文档正文超过大小上限（2MB）。
	// HTTP 400
	ErrDocumentTooLarge = &Errno{HTTP: 400, Code: "InvalidParameter.DocumentTooLarge", Message: "文档内容超过大小上限"}

	// ErrDocumentExportFormat 导出格式非法（仅 md/pdf/docx）。
	// HTTP 400
	ErrDocumentExportFormat = &Errno{HTTP: 400, Code: "InvalidParameter.DocumentExportFormat", Message: "不支持的导出格式"}

	// ErrDocumentExportUnavailable 当前环境不支持该格式导出（沙箱未启用，pdf/docx 不可导）。
	// HTTP 503
	ErrDocumentExportUnavailable = &Errno{HTTP: 503, Code: "ServiceUnavailable.DocumentExportUnavailable", Message: "当前环境暂不支持该格式导出，可先导出 Markdown"}

	// ErrDocumentExportBusy 同一用户已有导出在进行中（每用户单并发导出守卫）。
	// HTTP 429
	ErrDocumentExportBusy = &Errno{HTTP: 429, Code: "FailedOperation.DocumentExportBusy", Message: "已有导出任务进行中，请稍后再试"}
)
