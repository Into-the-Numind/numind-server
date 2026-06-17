package errno

// Skill 系统错误码（agent-mode-skill-system #5/14）
// 500 级内部错误复用 InternalServerError，本文件不重复定义。
var (
	// ErrSkillNameInvalid 表示 skill 名称不合法（长度/字符不合规范）。
	ErrSkillNameInvalid = &Errno{HTTP: 400, Code: "InvalidParameter.SkillNameInvalid", Message: "skill name invalid"}

	// ErrChildAccountForbidden 表示子账户尝试访问 skill 系统（仅父账户可操作）。
	ErrChildAccountForbidden = &Errno{HTTP: 403, Code: "AuthFailure.ChildAccountForbidden", Message: "child account cannot access skills"}

	// ErrSkillNotFound 表示 agent_definition 不存在或无权访问。
	ErrSkillNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.Skill", Message: "skill not found"}

	// ErrSkillVersionNotFound 表示 agent_definition_history 中指定版本不存在。
	ErrSkillVersionNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.SkillVersion", Message: "skill version not found"}

	// ErrTemplateNotFound 表示 skill_template 不存在。
	ErrTemplateNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.Template", Message: "template not found"}

	// ErrSkillVersionConflict 表示并发更新时检测到版本冲突。
	ErrSkillVersionConflict = &Errno{HTTP: 409, Code: "BizError.SkillVersionConflict", Message: "skill version conflict — concurrent update detected"}

	// ErrSkillBuilderFailed 表示 skill_builder 组装 SKILL.md 失败（如必填题缺失）。
	ErrSkillBuilderFailed = &Errno{HTTP: 422, Code: "BizError.SkillBuilderFailed", Message: "skill body builder failed"}
)
