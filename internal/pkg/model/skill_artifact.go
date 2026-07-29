package model

import (
	"time"

	"gorm.io/datatypes"
)

// Skill 是 v2 引入的独立 skill 资产（agent-mode-v2-skill-as-artifact #1/3）。
// 与 v1 AgentDefinition.GeneratedSkillBody/CustomSkillBody 嵌入式 skill 不同，
// 本表是文件型资产，可独立 CRUD、版本回滚、装载到多个 Agent。
// v2 #2 接管 runtime 后通过 AgentSkillBinding 读取本表内容；
// 本期（v2 #1）runtime 不变，binding 仅作为新增数据存在。
//
// is_active 带 default:1（GORM bool default:true 陷阱场景 — database.md §6）：
//   - Create 路径必须用 `Select("*").Create(...)` 或 UpdateColumn fixup
//   - Update 路径用 `db.Save()` 或 `Updates(map)` 安全
type Skill struct {
	ID           uint `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentUserID uint `gorm:"type:int unsigned;not null;index:idx_skill_parent_active,priority:1;index:idx_skill_visibility,priority:1" json:"parent_user_id"`
	// OwnerUserID 是真正的创建者 user id（父账户自建=父 id；子账户自建=子账户 id）。
	// 与 ParentUserID（=机构 id）区分：可见性 'sub_user' 按 OwnerUserID 收敛。
	OwnerUserID uint `gorm:"type:int unsigned;not null;index:idx_skill_owner" json:"owner_user_id"`
	// Visibility 三级可见性（T4 skill-3tier-visibility）：
	//   'official'    → 所有机构所有用户可见（仅 admin / system seed 创建）
	//   'institution' → 机构内全员可见（parent_user_id 命中的父账户 + 全部子账户）；仅父账户可创建/设置
	//   'sub_user'    → 仅 OwnerUserID 可见（子账户自建默认 / 父账户创建私有技能）
	// DEFAULT='institution'（父账户自建默认）；子账户自建默认 'sub_user'（service 层决定）。
	Visibility       string         `gorm:"type:enum('official','institution','sub_user');not null;default:'institution';index:idx_skill_visibility,priority:2" json:"visibility"`
	Name             string         `gorm:"size:100;not null" json:"name"`
	Description      string         `gorm:"size:300;not null;default:''" json:"description"`
	WhenToUse        string         `gorm:"size:500;not null;default:''" json:"when_to_use"`
	AllowedTools     datatypes.JSON `gorm:"type:json;not null;default:(JSON_ARRAY())" json:"allowed_tools"`
	BodyMd           string         `gorm:"type:mediumtext;not null" json:"body_md"`
	SourceType       string         `gorm:"type:enum('generated','custom','imported_from_template','imported_from_marketplace');not null;default:'custom'" json:"source_type"`
	SourceTemplateID *uint          `gorm:"type:int unsigned;index:idx_skill_source_template" json:"source_template_id"`
	OriginType       string         `gorm:"type:enum('official','tenant','user');not null;default:'user'" json:"origin_type"`
	Version          uint           `gorm:"type:int unsigned;not null;default:1" json:"version"`
	IsActive         bool           `gorm:"type:tinyint(1);not null;default:1;index:idx_skill_parent_active,priority:2;index:idx_skill_visibility,priority:3" json:"is_active"`
	// SubscriptionID / MarketplaceID 标记本行是市场订阅的"引用指针"（T4 reference-mode）。
	// 任一非零 ⇒ 此行 body 非权威，运行时 loadDBSkill 改读 marketplace 当前 SanitizedBodyMD 快照。
	SubscriptionID uint      `gorm:"type:int unsigned;not null;default:0" json:"subscription_id"`
	MarketplaceID  uint      `gorm:"type:int unsigned;not null;default:0" json:"marketplace_id"`
	CreatedBy      uint      `gorm:"type:int unsigned;not null" json:"created_by"`
	CreatedAt      time.Time `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoUpdateTime;index:idx_skill_parent_active,priority:3,sort:desc" json:"updated_at"`

	// CanEdit 是非持久化的派生字段（不入库），由 biz 层 ListVisibleSkills/GetForCaller 计算，
	// 驱动前端编辑/删除按钮门控。'official' 行对所有人只读（can_edit=false）。
	CanEdit bool `gorm:"-" json:"can_edit"`
}

func (Skill) TableName() string { return "skill" }

// SkillHistory 是 Skill 每次保存的 append-only 快照。
// 用于版本回滚（rollback 创建新版本，不删旧），保留完整审计链。
type SkillHistory struct {
	ID        uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	SkillID   uint           `gorm:"type:int unsigned;not null;uniqueIndex:uk_skill_version,priority:1;index:idx_history_skill_created,priority:1" json:"skill_id"`
	Version   uint           `gorm:"type:int unsigned;not null;uniqueIndex:uk_skill_version,priority:2" json:"version"`
	Snapshot  datatypes.JSON `gorm:"not null" json:"snapshot"`
	CreatedBy uint           `gorm:"type:int unsigned;not null" json:"created_by"`
	CreatedAt time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime;index:idx_history_skill_created,priority:2,sort:desc" json:"created_at"`
}

func (SkillHistory) TableName() string { return "skill_history" }

// AgentSkillBinding 是 Agent 与 Skill 的多对多装载关系。
// 一个 Agent 可装载多个 Skill；一个 Skill 可被多个 Agent 装载（同租户内）。
// uk_agent_skill 防止重复装载（复装时改 is_active=1 + 更新 sort_order）。
//
// is_active 带 default:1（database.md §6 陷阱）。
type AgentSkillBinding struct {
	ID        uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	AgentID   uint       `gorm:"type:int unsigned;not null;uniqueIndex:uk_agent_skill,priority:1;index:idx_binding_agent_active_sort,priority:1" json:"agent_id"`
	SkillID   uint       `gorm:"type:int unsigned;not null;uniqueIndex:uk_agent_skill,priority:2;index:idx_binding_skill_active,priority:1" json:"skill_id"`
	SortOrder int16      `gorm:"type:smallint;not null;default:0;index:idx_binding_agent_active_sort,priority:3" json:"sort_order"`
	IsActive  bool       `gorm:"type:tinyint(1);not null;default:1;index:idx_binding_agent_active_sort,priority:2;index:idx_binding_skill_active,priority:2" json:"is_active"`
	BoundAt   time.Time  `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"bound_at"`
	UnboundAt *time.Time `gorm:"type:datetime" json:"unbound_at"`
}

func (AgentSkillBinding) TableName() string { return "agent_skill_binding" }
