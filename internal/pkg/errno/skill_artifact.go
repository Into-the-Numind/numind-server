package errno

// Skill Artifact v2 错误码（agent-mode-v2-skill-as-artifact）
//
// ADR-14（spec 2026-05-24-agent-mode-v2-skill-as-artifact-design.md §0）：
// v2 errno 使用 `SkillArtifact` 中缀，避免与 v1 `ErrSkill*`（指 agent_definition）语义冲突。
// 前端按 errno Code 字符串区分（如 `ResourceNotFound.SkillArtifact` vs `ResourceNotFound.Skill`）。
// 切勿复用 v1 `ErrSkillNotFound` / `ErrSkillVersionNotFound` / `ErrSkillVersionConflict` 等。
var (
	// ErrSkillArtifactNotFound 表示 skill artifact（独立 Skill 资产）不存在或无权访问。
	// 区别于 v1 ErrSkillNotFound——后者指 agent_definition 不存在。
	ErrSkillArtifactNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.SkillArtifact", Message: "skill artifact not found"}

	// ErrSkillArtifactVersionNotFound 表示 skill_artifact_history 中指定版本不存在。
	// 区别于 v1 ErrSkillVersionNotFound——后者指 agent_definition_history。
	ErrSkillArtifactVersionNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.SkillArtifactVersion", Message: "skill artifact version not found"}

	// ErrSkillArtifactBodyTooLarge 表示 body_md 超过 200KB 硬限（含 frontmatter 重组后总长）。
	// 触发位置：Create / Update 时长度校验失败。
	ErrSkillArtifactBodyTooLarge = &Errno{HTTP: 413, Code: "InvalidParameter.SkillArtifactBodyTooLarge", Message: "skill artifact body too large (max 200KB)"}

	// ErrSkillArtifactBindingExists 表示尝试装载 Skill 到 Agent 时已存在装载关系。
	// 触发位置：POST /v1/agents/:agent_id/skills 重复 attach。
	ErrSkillArtifactBindingExists = &Errno{HTTP: 409, Code: "Conflict.SkillArtifactBindingExists", Message: "skill artifact already bound to this agent"}

	// ErrSkillArtifactNameConflict 表示尝试装载一个与该 Agent 已绑定技能【同名】的技能。
	// 运行时按 name 解析绑定技能，两个同名活跃绑定会让 runner hard-error 整个 run 卡死，
	// 因此在 attach 时即拦截。触发位置：POST /v1/agents/:agent_id/skills 同名绑定。
	ErrSkillArtifactNameConflict = &Errno{HTTP: 409, Code: "Conflict.SkillArtifactNameConflict", Message: "another skill with the same name is already bound to this agent"}

	// ErrSkillArtifactFrontmatterInvalid 表示 frontmatter 解析失败（YAML 语法错误等）。
	// 业务层可选择 fallback：把 raw content 当 body_md，frontmatter 字段保留空——
	// 是否 fallback 由 service 层决定，errno 本身只表达"解析失败"语义。
	ErrSkillArtifactFrontmatterInvalid = &Errno{HTTP: 422, Code: "BizError.SkillArtifactFrontmatterInvalid", Message: "skill artifact frontmatter parse failed"}

	// ErrSkillVisibilityForbidden 表示子账户尝试创建/设置 'institution' 或 'official' 可见性。
	// 仅父账户可创建 'institution'；'official' 任何 API 调用都不允许（仅 admin seed / import-template）。
	// 触发位置（T4 skill-3tier-visibility）：Service.Create 子账户 clamp 守卫。
	ErrSkillVisibilityForbidden = &Errno{HTTP: 403, Code: "Forbidden.SkillVisibility", Message: "无权设置该技能可见性等级"}

	// ErrSkillSourceUnavailable 表示市场引用技能（reference pointer）的来源已下架/删除。
	// 运行时 loadDBSkill 走 jsonErr 软错误返回此语义文案（不杀 run）；此常量供一致性定义。
	ErrSkillSourceUnavailable = &Errno{HTTP: 404, Code: "ResourceNotFound.SkillSource", Message: "技能的市场来源已下架"}
)
