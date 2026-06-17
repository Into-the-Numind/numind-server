package artifact

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/skill"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// UserLookup 是 artifact Service 解析 caller 机构/父子身份所需的最小用户查询接口。
// 抽象成接口便于单测注入 fake（in-memory sqlite 无 user 表）。生产由 store.NewUserStore 实现。
type UserLookup interface {
	GetByID(ctx context.Context, userID uint) (*model.User, error)
}

// bodyMaxBytes 是 body_md 的硬上限（200KB，含 frontmatter 重组后的总长）。
// 与 CreateRequest binding tag 的 `max=204800` 一致；double check 兜底防止 controller 漏校验。
const bodyMaxBytes = 200 * 1024

// CreateRequest 创建/更新 Skill 的入参（POST /v1/skills、PUT /v1/skills/:id 公用）。
//
// 字段对应 model.Skill：
//   - Name / Description / WhenToUse：单列存储，便于索引和搜索
//   - AllowedTools：JSON 序列化为 datatypes.JSON 入库
//   - BodyMd：MEDIUMTEXT 列；硬限 200KB（spec ADR-2）
//   - SourceType：枚举 custom/generated/imported_from_template（marketplace 留给 v2 #3）
//   - SourceTemplateID：若 source_type=imported_from_template，引用 skill_template.id
//
// 注意：Version / IsActive / CreatedBy / ParentUserID 由 service 内部决定，不在 Request 里。
type CreateRequest struct {
	Name             string   `json:"name" binding:"required,min=1,max=100"`
	Description      string   `json:"description" binding:"max=300"`
	WhenToUse        string   `json:"when_to_use" binding:"max=500"`
	AllowedTools     []string `json:"allowed_tools"`
	BodyMd           string   `json:"body_md" binding:"required,max=204800"`
	SourceType       string   `json:"source_type" binding:"omitempty,oneof=custom generated imported_from_template imported_from_marketplace"`
	SourceTemplateID *uint    `json:"source_template_id,omitempty"`
	// Visibility 三级可见性入参（T4）。binding 只允许 'institution' / 'sub_user'——
	// 'official' 故意不可绑定（仅 seed / import-template 路径设置）。空值时 service 按 isParent 决定默认值。
	Visibility string `json:"visibility" binding:"omitempty,oneof=institution sub_user"`
}

// Service 是 Skill artifact CRUD 业务编排层（spec §3.2）。
//
// 所有方法第一句校验 parentUserID 不为 0：
//   - controller 层应已通过 user.ParentUserID != nil 拦截子账户
//   - service 层再加 0 校验作为兜底（防御纵深），明确语义为"必须有显式父账户"
type Service struct {
	store      IStore
	versioning *Versioning
	users      UserLookup
	db         *gorm.DB
}

// 三级可见性常量（T4 skill-3tier-visibility）。
const (
	VisibilityOfficial    = "official"
	VisibilityInstitution = "institution"
	VisibilitySubUser     = "sub_user"
)

// NewService 构造 Service。底层用同一个 *gorm.DB 包出 IStore + IHistoryStore + Versioning。
// userStore 用 store.NewUserStore(db) 解析 caller 机构/父子身份（ListVisibleSkills 用）。
func NewService(db *gorm.DB) *Service {
	st := NewStore(db)
	historyStore := NewHistoryStore(db)
	return &Service{
		store:      st,
		versioning: NewVersioning(st, historyStore, db),
		users:      store.NewUserStore(db),
		db:         db,
	}
}

// NewServiceWithUsers 构造 Service 并显式注入 UserLookup（单测用 fake 时调用）。
func NewServiceWithUsers(db *gorm.DB, users UserLookup) *Service {
	st := NewStore(db)
	historyStore := NewHistoryStore(db)
	return &Service{
		store:      st,
		versioning: NewVersioning(st, historyStore, db),
		users:      users,
		db:         db,
	}
}

// computeCanEdit 计算 caller 对某行 skill 的可编辑性（T4 contract C）：
//   - true iff (caller 是父账户 AND row.visibility=='institution' AND row.parent_user_id==caller.ID)
//     OR (row.owner_user_id == caller.ID)
//   - 'official' 行对所有人只读（can_edit=false，本函数对 official 自然返回 false，
//     除非 owner==caller，但 official 的 owner=0/admin，普通用户永不命中）。
func computeCanEdit(sk *model.Skill, callerID uint, isParent bool) bool {
	if sk.Visibility == VisibilityOfficial {
		return false
	}
	if sk.OwnerUserID == callerID {
		return true
	}
	if isParent && sk.Visibility == VisibilityInstitution && sk.ParentUserID == callerID {
		return true
	}
	return false
}

// resolveCaller 解析 caller 的机构 id + 父子身份（biz 层 ListVisibleSkills/Create 共用）。
//
//	instID  = caller.ParentUserID==nil ? caller.ID : *caller.ParentUserID
//	isParent = caller.ParentUserID == nil
func (s *Service) resolveCaller(ctx context.Context, callerUserID uint) (instID uint, isParent bool, err error) {
	u, err := s.users.GetByID(ctx, callerUserID)
	if err != nil {
		return 0, false, fmt.Errorf("resolveCaller: lookup user %d: %w", callerUserID, err)
	}
	if u.ParentUserID == nil {
		return u.ID, true, nil
	}
	return *u.ParentUserID, false, nil
}

// Create 创建新 Skill（T4 三级可见性版）。流程：
//  1. 兜底（callerUserID/instID == 0 → ErrPermissionDenied）
//  2. body 长度校验（防绕过 binding tag）
//  3. 可见性裁决（子账户强制 sub_user，越权 → ErrSkillVisibilityForbidden）
//  4. 事务内：写 skill 行 + 写 history v1 快照
//
// 字段语义（FROZEN）：
//   - OwnerUserID  = callerUserID（真正创建者）
//   - ParentUserID = instID（机构 id）
//   - CreatedBy    = callerUserID
//   - Visibility   = 子账户 → 强制 'sub_user'；父账户 → req.Visibility（空默认 'institution'，允许 'sub_user'）；
//     'official' 任何 API 路径都不允许（仅 seed/import-template）。
func (s *Service) Create(ctx context.Context, callerUserID, instID uint, isParent bool, req CreateRequest) (*model.Skill, error) {
	if callerUserID == 0 || instID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	if len(req.BodyMd) > bodyMaxBytes {
		return nil, errno.ErrSkillArtifactBodyTooLarge
	}

	visibility, err := resolveCreateVisibility(req.Visibility, isParent)
	if err != nil {
		return nil, err
	}

	tools, err := marshalTools(req.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("Create marshal tools: %w", err)
	}

	srcType := req.SourceType
	if srcType == "" {
		srcType = "custom"
	}

	originType := originTypeForSource(srcType, visibility)

	sk := &model.Skill{
		ParentUserID:     instID,
		OwnerUserID:      callerUserID,
		Visibility:       visibility,
		Name:             req.Name,
		Description:      req.Description,
		WhenToUse:        req.WhenToUse,
		AllowedTools:     tools,
		BodyMd:           req.BodyMd,
		SourceType:       srcType,
		SourceTemplateID: req.SourceTemplateID,
		OriginType:       originType,
		Version:          1,
		IsActive:         true,
		CreatedBy:        callerUserID,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 写 skill（Select("*") 在 store 内部处理）—— Tx 变体确保 SQLite single-conn 不死锁
		if err := s.store.CreateTx(ctx, tx, sk); err != nil {
			return err
		}
		// 写 history v1
		return s.versioning.writeSnapshotTx(ctx, tx, sk, callerUserID)
	})
	if err != nil {
		return nil, err
	}
	sk.CanEdit = true // 创建者对自己创建的行恒可编辑
	return sk, nil
}

// resolveCreateVisibility 裁决创建可见性（contract D 守卫）。
//   - 子账户（!isParent）：req.Visibility 必须为空或 'sub_user'，否则 ErrSkillVisibilityForbidden（不静默放行）。
//   - 父账户：空 → 默认 'institution'；允许 'institution' / 'sub_user'。
//   - 'official' 任何 API 路径都拒绝（binding 已挡，这里二次防御）。
func resolveCreateVisibility(reqVisibility string, isParent bool) (string, error) {
	if reqVisibility == VisibilityOfficial {
		return "", errno.ErrSkillVisibilityForbidden
	}
	if !isParent {
		// 子账户：只能 sub_user。空 → sub_user；显式 institution/official → 拒绝。
		if reqVisibility == "" || reqVisibility == VisibilitySubUser {
			return VisibilitySubUser, nil
		}
		return "", errno.ErrSkillVisibilityForbidden
	}
	// 父账户：空 → institution；允许 institution / sub_user。
	switch reqVisibility {
	case "":
		return VisibilityInstitution, nil
	case VisibilityInstitution, VisibilitySubUser:
		return reqVisibility, nil
	default:
		return "", errno.ErrSkillVisibilityForbidden
	}
}

// originTypeForSource 把 source_type + visibility 映射到 legacy origin_type（保留向后兼容字段）。
func originTypeForSource(srcType, visibility string) string {
	if visibility == VisibilityOfficial {
		return "official"
	}
	switch srcType {
	case "imported_from_template":
		return "official"
	case "imported_from_marketplace":
		return "tenant"
	default:
		return "user"
	}
}

// List 分页列举本租户下活跃 Skill。
//
// 分页归一化：
//   - page < 1 → 1
//   - pageSize < 1 → 20
//   - pageSize > 100 → 100
//
// search 非空时按 name LIKE 过滤（前缀+包含；底层用 `%xxx%`）。
func (s *Service) List(ctx context.Context, parentUserID uint, page, pageSize int, search string) ([]model.Skill, int64, error) {
	if parentUserID == 0 {
		return nil, 0, errno.ErrPermissionDenied
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize
	return s.store.List(ctx, parentUserID, search, offset, pageSize)
}

// ListVisibleSkills 是用户端 GET /v1/skills 的三级可见性列表（T4 contract C，安全核心）。
//
// 流程：
//  1. 解析 caller → instID + isParent
//  2. store.ListVisible 按 (official | institution&inst | sub_user&owner) 谓词过滤
//  3. 给每行盖上派生 CanEdit 字段（驱动前端编辑/删除按钮门控）
func (s *Service) ListVisibleSkills(ctx context.Context, callerUserID uint, page, pageSize int, search string) ([]model.Skill, int64, error) {
	if callerUserID == 0 {
		return nil, 0, errno.ErrPermissionDenied
	}
	instID, isParent, err := s.resolveCaller(ctx, callerUserID)
	if err != nil {
		return nil, 0, err
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	items, total, err := s.store.ListVisible(ctx, instID, callerUserID, search, offset, pageSize)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		items[i].CanEdit = computeCanEdit(&items[i], callerUserID, isParent)
	}
	return items, total, nil
}

// Get 详情接口（内部/admin 路径）；含 is_active=0 的 skill。
// 跨租户或不存在 → ErrSkillArtifactNotFound。仍按 parent_user_id 严格过滤（不放三级可见性）。
func (s *Service) Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	return s.store.Get(ctx, parentUserID, skillID)
}

// GetForCaller 是用户端 GET /v1/skills/:id 的三级可见性详情（含 CanEdit）。
// 不可见或不存在 → ErrSkillArtifactNotFound。
func (s *Service) GetForCaller(ctx context.Context, callerUserID, skillID uint) (*model.Skill, error) {
	if callerUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	instID, isParent, err := s.resolveCaller(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	sk, err := s.store.GetForCaller(ctx, instID, callerUserID, skillID)
	if err != nil {
		return nil, err
	}
	sk.CanEdit = computeCanEdit(sk, callerUserID, isParent)
	return sk, nil
}

// Update 全量更新 Skill（T4 can_edit 门控版）。流程：
//  1. caller 兜底 + body 长度校验
//  2. 解析 caller → instID/isParent，按三级可见性取 skill
//  3. can_edit 守卫：不可编辑（含跨租户 / 'official' 只读 / 他人 sub_user）→ ErrSkillArtifactNotFound
//  4. 事务内：覆盖字段 + version+1 + Save + 写 history 快照
//
// SourceType / SourceTemplateID 也允许通过 Update 修改。Visibility 不在 Update 改（避免越权升级；
// 后续如需可加专门的 SetVisibility 接口）。
func (s *Service) Update(ctx context.Context, callerUserID, skillID uint, req CreateRequest) (*model.Skill, error) {
	if callerUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	if len(req.BodyMd) > bodyMaxBytes {
		return nil, errno.ErrSkillArtifactBodyTooLarge
	}

	instID, isParent, err := s.resolveCaller(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	sk, err := s.store.GetForCaller(ctx, instID, callerUserID, skillID)
	if err != nil {
		return nil, err
	}
	// can_edit 守卫：不可编辑（'official' 只读 / 他人 sub_user / 非父账户的 institution）→ 不存在（不暴露存在性）。
	if !computeCanEdit(sk, callerUserID, isParent) {
		return nil, errno.ErrSkillArtifactNotFound
	}

	tools, err := marshalTools(req.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("Update marshal tools: %w", err)
	}

	sk.Name = req.Name
	sk.Description = req.Description
	sk.WhenToUse = req.WhenToUse
	sk.AllowedTools = tools
	sk.BodyMd = req.BodyMd
	if req.SourceType != "" {
		sk.SourceType = req.SourceType
		switch req.SourceType {
		case "imported_from_template":
			sk.OriginType = "official"
		case "imported_from_marketplace":
			sk.OriginType = "tenant"
		default:
			sk.OriginType = "user"
		}
	}
	sk.SourceTemplateID = req.SourceTemplateID
	sk.Version++

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.store.UpdateTx(ctx, tx, sk); err != nil {
			return err
		}
		return s.versioning.writeSnapshotTx(ctx, tx, sk, callerUserID)
	})
	if err != nil {
		return nil, err
	}
	sk.CanEdit = true
	return sk, nil
}

// Delete 软删 Skill（is_active=0）+ 级联软删所有 agent_skill_binding（事务）。T4 can_edit 门控版。
// 返回受影响的活跃 binding 数（供 controller 响应 affected_bindings）。
//
// 流程：解析 caller → 三级可见性取 skill → can_edit 守卫 → 按行实际 parent_user_id 软删。
// 不可编辑（跨租户 / 'official' / 他人 sub_user）→ ErrSkillArtifactNotFound。
// 历史 history 不删（保留审计链；ADR-9）。
func (s *Service) Delete(ctx context.Context, callerUserID, skillID uint) (int64, error) {
	if callerUserID == 0 {
		return 0, errno.ErrPermissionDenied
	}
	instID, isParent, err := s.resolveCaller(ctx, callerUserID)
	if err != nil {
		return 0, err
	}
	sk, err := s.store.GetForCaller(ctx, instID, callerUserID, skillID)
	if err != nil {
		return 0, err
	}
	if !computeCanEdit(sk, callerUserID, isParent) {
		return 0, errno.ErrSkillArtifactNotFound
	}
	// 用行实际 parent_user_id 走原 SoftDelete（保持级联 binding 软删逻辑）。
	return s.store.SoftDelete(ctx, sk.ParentUserID, skillID)
}

// DeleteInternal 是按 (parentUserID, skillID) 严格软删的内部路径（marketplace 退订引用指针用）。
// 不做 can_edit 解析（caller 已是引用指针所属租户，自管）。
func (s *Service) DeleteInternal(ctx context.Context, parentUserID, skillID uint) (int64, error) {
	if parentUserID == 0 {
		return 0, errno.ErrPermissionDenied
	}
	return s.store.SoftDelete(ctx, parentUserID, skillID)
}

// ListBoundAgents 返回装载该 Skill 的活跃 Agent 列表（spec /v1/skills/:id/agents）。
func (s *Service) ListBoundAgents(ctx context.Context, parentUserID, skillID uint) ([]model.AgentDefinition, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	return s.store.ListBoundAgents(ctx, parentUserID, skillID)
}

// ListHistory 列出 skill 版本历史（T4 三级可见性 + can_edit 门控版）。
//
// 安全（与 Update/Delete 同源）：history 暴露 created_by / diff_summary（含 body 行级增删数），
// 因此必须按 *可编辑性* 门控，而非裸 parent_user_id。否则同机构另一子账户可读他人私有
// （visibility='sub_user'）skill 的历史元数据。流程：
//  1. 解析 caller → instID/isParent
//  2. 按三级可见性取 skill（GetForCaller，不可见/不存在 → ErrSkillArtifactNotFound）
//  3. can_edit 守卫：不可编辑（'official' 只读 / 他人 sub_user / 非父的 institution）→ ErrSkillArtifactNotFound
//  4. 用行实际 parent_user_id 委托 versioning.ListHistory（保持 history 查询语义）
func (s *Service) ListHistory(ctx context.Context, callerUserID, skillID uint) ([]HistoryItem, error) {
	if callerUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	instID, isParent, err := s.resolveCaller(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	sk, err := s.store.GetForCaller(ctx, instID, callerUserID, skillID)
	if err != nil {
		return nil, err
	}
	if !computeCanEdit(sk, callerUserID, isParent) {
		return nil, errno.ErrSkillArtifactNotFound
	}
	return s.versioning.ListHistory(ctx, sk.ParentUserID, skillID)
}

// Restore 从历史快照回滚 skill（T4 三级可见性 + can_edit 门控版）。
//
// 安全（与 Update/Delete 同源，写路径——最高危）：Restore 改写 name/body_md/allowed_tools 等
// 业务字段并强制 is_active=true（可复活软删 skill）。因此必须按 *可编辑性* 门控，而非裸
// parent_user_id。否则同机构另一子账户可劫持/复活他人私有 skill，绕过 computeCanEdit 守卫。
// 流程：
//  1. 解析 caller → instID/isParent
//  2. 按三级可见性取 skill（GetForCaller，不可见/不存在 → ErrSkillArtifactNotFound）
//  3. can_edit 守卫：不可编辑 → ErrSkillArtifactNotFound（不暴露存在性）
//  4. 用行实际 parent_user_id 委托 versioning.Restore
func (s *Service) Restore(ctx context.Context, callerUserID, skillID, version uint) (*model.Skill, error) {
	if callerUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	instID, isParent, err := s.resolveCaller(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	sk, err := s.store.GetForCaller(ctx, instID, callerUserID, skillID)
	if err != nil {
		return nil, err
	}
	if !computeCanEdit(sk, callerUserID, isParent) {
		return nil, errno.ErrSkillArtifactNotFound
	}
	return s.versioning.Restore(ctx, sk.ParentUserID, skillID, version)
}

// marshalTools 把 []string allowedTools 序列化为 datatypes.JSON。
// 当 tools 为 nil 或空时返回 `[]` 而非 `null`（DB 列默认 JSON_ARRAY()，保持语义一致）。
func marshalTools(tools []string) (datatypes.JSON, error) {
	if tools == nil {
		tools = []string{}
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(b), nil
}

// ImportTemplate 一键从官方模板克隆/导入为本租户独立技能。
//
// T4：模板导入是 admin/seed 来源，盖 visibility='official'（contract item 6：
// 仅 admin/seed/import-template 路径可设 'official'，普通 API Create 拒绝）。
// 因此走内部 createWithVisibility 绕过 resolveCreateVisibility 的 'official' 拒绝守卫。
func (s *Service) ImportTemplate(ctx context.Context, parentUserID, createdBy uint, templateID uint64) (*model.Skill, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}

	var tpl model.SkillTemplate
	if err := s.db.WithContext(ctx).First(&tpl, templateID).Error; err != nil {
		return nil, errno.ErrTemplateNotFound.SetMessage("技能模板未找到 (id=%d)", templateID)
	}

	ad := &model.AgentDefinition{
		Name:                 tpl.Name,
		Description:          tpl.Description,
		QuestionnaireAnswers: tpl.QuestionnaireAnswers,
	}
	bodyMd, err := skill.Build(ad)
	if err != nil {
		return nil, fmt.Errorf("ImportTemplate compile body: %w", err)
	}

	var allowedTools []string
	if len(tpl.DefaultToolFlags) > 0 {
		var flags map[string]bool
		if err := json.Unmarshal(tpl.DefaultToolFlags, &flags); err == nil {
			for k, v := range flags {
				if v {
					allowedTools = append(allowedTools, k)
				}
			}
		}
	}

	tplID := uint(templateID)
	req := CreateRequest{
		Name:             tpl.Name,
		Description:      tpl.Description,
		WhenToUse:        "",
		AllowedTools:     allowedTools,
		BodyMd:           bodyMd,
		SourceType:       "imported_from_template",
		SourceTemplateID: &tplID,
	}

	// import-template 是受信任路径，盖 'official'（绕过 API Create 的 official 拒绝守卫）。
	// owner/parent 都设为导入方 parentUserID（机构持有）。
	return s.createWithVisibility(ctx, parentUserID, parentUserID, createdBy, VisibilityOfficial, req)
}

// createWithVisibility 是受信任的内部创建（ImportTemplate / 未来 admin seed 用），可显式设 visibility
// （含 'official'，绕过 resolveCreateVisibility 的 API 守卫）。普通用户路径绝不可调用此方法。
func (s *Service) createWithVisibility(ctx context.Context, ownerUserID, instID, createdBy uint, visibility string, req CreateRequest) (*model.Skill, error) {
	if ownerUserID == 0 || instID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	if len(req.BodyMd) > bodyMaxBytes {
		return nil, errno.ErrSkillArtifactBodyTooLarge
	}
	tools, err := marshalTools(req.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("createWithVisibility marshal tools: %w", err)
	}
	srcType := req.SourceType
	if srcType == "" {
		srcType = "custom"
	}
	sk := &model.Skill{
		ParentUserID:     instID,
		OwnerUserID:      ownerUserID,
		Visibility:       visibility,
		Name:             req.Name,
		Description:      req.Description,
		WhenToUse:        req.WhenToUse,
		AllowedTools:     tools,
		BodyMd:           req.BodyMd,
		SourceType:       srcType,
		SourceTemplateID: req.SourceTemplateID,
		OriginType:       originTypeForSource(srcType, visibility),
		Version:          1,
		IsActive:         true,
		CreatedBy:        createdBy,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.store.CreateTx(ctx, tx, sk); err != nil {
			return err
		}
		return s.versioning.writeSnapshotTx(ctx, tx, sk, createdBy)
	})
	if err != nil {
		return nil, err
	}
	return sk, nil
}
