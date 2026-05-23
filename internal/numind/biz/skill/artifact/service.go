package artifact

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

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
	SourceType       string   `json:"source_type" binding:"omitempty,oneof=custom generated imported_from_template"`
	SourceTemplateID *uint    `json:"source_template_id,omitempty"`
}

// Service 是 Skill artifact CRUD 业务编排层（spec §3.2）。
//
// 所有方法第一句校验 parentUserID 不为 0：
//   - controller 层应已通过 user.ParentUserID != nil 拦截子账户
//   - service 层再加 0 校验作为兜底（防御纵深），明确语义为"必须有显式父账户"
type Service struct {
	store      IStore
	versioning *Versioning
	db         *gorm.DB
}

// NewService 构造 Service。底层用同一个 *gorm.DB 包出 IStore + IHistoryStore + Versioning。
func NewService(db *gorm.DB) *Service {
	store := NewStore(db)
	historyStore := NewHistoryStore(db)
	return &Service{
		store:      store,
		versioning: NewVersioning(store, historyStore, db),
		db:         db,
	}
}

// Create 创建新 Skill。流程：
//  1. 子账户兜底（parentUserID == 0 → ErrPermissionDenied）
//  2. body 长度校验（防绕过 binding tag）
//  3. 事务内：写 skill 行 + 写 history v1 快照
//
// createdBy 通常 = parentUserID（父账户自建），但允许差异（预留多人协作）。
func (s *Service) Create(ctx context.Context, parentUserID, createdBy uint, req CreateRequest) (*model.Skill, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	if len(req.BodyMd) > bodyMaxBytes {
		return nil, errno.ErrSkillArtifactBodyTooLarge
	}

	tools, err := marshalTools(req.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("Create marshal tools: %w", err)
	}

	srcType := req.SourceType
	if srcType == "" {
		srcType = "custom"
	}

	sk := &model.Skill{
		ParentUserID:     parentUserID,
		Name:             req.Name,
		Description:      req.Description,
		WhenToUse:        req.WhenToUse,
		AllowedTools:     tools,
		BodyMd:           req.BodyMd,
		SourceType:       srcType,
		SourceTemplateID: req.SourceTemplateID,
		Version:          1,
		IsActive:         true,
		CreatedBy:        createdBy,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 写 skill（Select("*") 在 store 内部处理）—— Tx 变体确保 SQLite single-conn 不死锁
		if err := s.store.CreateTx(ctx, tx, sk); err != nil {
			return err
		}
		// 写 history v1
		return s.versioning.writeSnapshotTx(ctx, tx, sk, createdBy)
	})
	if err != nil {
		return nil, err
	}
	return sk, nil
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

// Get 详情接口；含 is_active=0 的 skill（详情页可展示软删 skill 的历史）。
// 跨租户或不存在 → ErrSkillArtifactNotFound。
func (s *Service) Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	return s.store.Get(ctx, parentUserID, skillID)
}

// Update 全量更新 Skill。流程：
//  1. 子账户兜底 + body 长度校验
//  2. 取当前 skill 校验所有权
//  3. 事务内：覆盖字段 + version+1 + Save + 写 history 快照
//
// SourceType / SourceTemplateID 也允许通过 Update 修改（spec 没禁止；用户改 source_type 是合理的）。
func (s *Service) Update(ctx context.Context, parentUserID, skillID uint, req CreateRequest) (*model.Skill, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	if len(req.BodyMd) > bodyMaxBytes {
		return nil, errno.ErrSkillArtifactBodyTooLarge
	}

	sk, err := s.store.Get(ctx, parentUserID, skillID)
	if err != nil {
		return nil, err
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
	}
	sk.SourceTemplateID = req.SourceTemplateID
	sk.Version++

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.store.UpdateTx(ctx, tx, sk); err != nil {
			return err
		}
		return s.versioning.writeSnapshotTx(ctx, tx, sk, parentUserID)
	})
	if err != nil {
		return nil, err
	}
	return sk, nil
}

// Delete 软删 Skill（is_active=0）+ 级联软删所有 agent_skill_binding（事务）。
// 返回受影响的活跃 binding 数（供 controller 响应 affected_bindings）。
//
// 历史 history 不删（保留审计链；ADR-9）。
func (s *Service) Delete(ctx context.Context, parentUserID, skillID uint) (int64, error) {
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
