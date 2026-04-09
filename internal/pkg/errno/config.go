package errno

var (
	ErrChatbotNotFound       = &Errno{HTTP: 404, Code: "Config.ChatbotNotFound", Message: "智能体不存在"}
	ErrChatbotNotPublished   = &Errno{HTTP: 403, Code: "Config.ChatbotNotPublished", Message: "智能体未发布"}
	ErrKnowledgeBaseNotFound = &Errno{HTTP: 404, Code: "Config.KnowledgeBaseNotFound", Message: "知识库不存在"}
	ErrMaxNodesExceeded      = &Errno{HTTP: 400, Code: "Config.MaxNodesExceeded", Message: "SOP步骤数已达上限(20)"}
	ErrTemplateNotPublished  = &Errno{HTTP: 403, Code: "Config.TemplateNotPublished", Message: "SOP模板未发布"}
	ErrSessionNotFound       = &Errno{HTTP: 404, Code: "Config.SessionNotFound", Message: "对话会话不存在"}
	ErrNotParentUser         = &Errno{HTTP: 403, Code: "Config.NotParentUser", Message: "仅限主账号操作"}
	ErrMaxDocsExceeded       = &Errno{HTTP: 400, Code: "Config.MaxDocsExceeded", Message: "知识库文档数已达上限(10)"}
	ErrFileTooLarge          = &Errno{HTTP: 400, Code: "Config.FileTooLarge", Message: "文件大小超过限制(50MB)"}
	ErrTooManyFiles          = &Errno{HTTP: 400, Code: "Config.TooManyFiles", Message: "单次最多上传5个文件"}
)
