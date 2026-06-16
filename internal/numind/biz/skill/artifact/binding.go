package artifact

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// IBindingStore 是 agent_skill_binding 表数据访问接口（mock 友好）。
//
// 注意：本接口的 ListByAgent 接受 includeInactive 标志——绝大多数业务路径只要活跃的 binding，
// 但 Attach 复装路径需要查到已卸载（is_active=0）的 binding 行以复用 uk_agent_skill 唯一行。
//
// 参数命名约定：agentID/skillID 为 uint（与 model.AgentSkillBinding 字段类型一致）。
type IBindingStore interface {
	// Get 按 (agentID, skillID) 查 binding（含 is_active=0，复装路径必需）。
	// 不存在 → gorm.ErrRecordNotFound（caller 用 errors.Is 判断）。
	Get(ctx context.Context, agentID, skillID uint) (*model.AgentSkillBinding, error)

	// Create 写入新 binding 行。is_active 默认 true（GORM default:1 陷阱由 Select("*") 兜底）。
	Create(ctx context.Context, b *model.AgentSkillBinding) error

	// Update 全字段 Save binding（含 is_active true/false 切换，bool 不丢；database.md §6b）。
	Update(ctx context.Context, b *model.AgentSkillBinding) error

	// ListByAgent 列举 agent 装载的 binding。
	// includeInactive=false 时只返回 is_active=1，按 sort_order ASC。
	// includeInactive=true 时返回全部，按 bound_at DESC。
	ListByAgent(ctx context.Context, agentID uint, includeInactive bool) ([]model.AgentSkillBinding, error)

	// SoftDeleteByAgent 软删指定 binding（agent_id + skill_id 联合）。
	// 设置 is_active=0 + unbound_at=NOW()；不存在或已 inactive 时返回 ErrSkillArtifactNotFound。
	SoftDeleteByAgent(ctx context.Context, agentID, skillID uint) error

	// UpdateSortOrders 事务内批量更新 sort_order（按 skillIDOrder 顺序设 0,1,2,...）。
	// 只更新 is_active=1 的 binding。
	UpdateSortOrders(ctx context.Context, agentID uint, skillIDOrder []uint) error
}

// gormBindingStore 是 IBindingStore 的 GORM 实现。
type gormBindingStore struct {
	db *gorm.DB
}

var _ IBindingStore = (*gormBindingStore)(nil)

// NewBindingStore 构造默认 GORM 实现。
func NewBindingStore(db *gorm.DB) IBindingStore {
	return &gormBindingStore{db: db}
}

// Get 按联合主键查 binding；不区分 is_active（复装路径需要拿到 is_active=0 的行）。
func (s *gormBindingStore) Get(ctx context.Context, agentID, skillID uint) (*model.AgentSkillBinding, error) {
	var b model.AgentSkillBinding
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND skill_id = ?", agentID, skillID).
		First(&b).Error
	if err != nil {
		return nil, err // caller 用 errors.Is(err, gorm.ErrRecordNotFound) 判断
	}
	return &b, nil
}

// Create 用 `Select("*")` 写入所有列，兜底 IsActive default:1 陷阱。
func (s *gormBindingStore) Create(ctx context.Context, b *model.AgentSkillBinding) error {
	if err := s.db.WithContext(ctx).Select("*").Create(b).Error; err != nil {
		return fmt.Errorf("BindingStore.Create: %w", err)
	}
	return nil
}

// Update 用 db.Save 全字段写入；database.md §6b 验证 Save 对 bool 安全。
func (s *gormBindingStore) Update(ctx context.Context, b *model.AgentSkillBinding) error {
	if err := s.db.WithContext(ctx).Save(b).Error; err != nil {
		return fmt.Errorf("BindingStore.Update: %w", err)
	}
	return nil
}

// ListByAgent 按 agent 列举 binding。
func (s *gormBindingStore) ListByAgent(ctx context.Context, agentID uint, includeInactive bool) ([]model.AgentSkillBinding, error) {
	q := s.db.WithContext(ctx).Where("agent_id = ?", agentID)
	if !includeInactive {
		q = q.Where("is_active = ?", true).Order("sort_order ASC")
	} else {
		q = q.Order("bound_at DESC")
	}
	var items []model.AgentSkillBinding
	if err := q.Find(&items).Error; err != nil {
		return nil, fmt.Errorf("BindingStore.ListByAgent: %w", err)
	}
	return items, nil
}

// SoftDeleteByAgent 软删指定 binding。不存在或已 inactive 返回 ErrSkillArtifactNotFound。
func (s *gormBindingStore) SoftDeleteByAgent(ctx context.Context, agentID, skillID uint) error {
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&model.AgentSkillBinding{}).
		Where("agent_id = ? AND skill_id = ? AND is_active = ?", agentID, skillID, true).
		Updates(map[string]interface{}{
			"is_active":  false,
			"unbound_at": &now,
		})
	if res.Error != nil {
		return fmt.Errorf("BindingStore.SoftDeleteByAgent: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return errno.ErrSkillArtifactNotFound
	}
	return nil
}

// UpdateSortOrders 事务内按数组顺序批量更新 sort_order，只影响 is_active=1 binding。
func (s *gormBindingStore) UpdateSortOrders(ctx context.Context, agentID uint, skillIDOrder []uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i, sid := range skillIDOrder {
			err := tx.Model(&model.AgentSkillBinding{}).
				Where("agent_id = ? AND skill_id = ? AND is_active = ?", agentID, sid, true).
				Updates(map[string]interface{}{"sort_order": int16(i)}).Error
			if err != nil {
				return fmt.Errorf("BindingStore.UpdateSortOrders agent=%d skill=%d: %w", agentID, sid, err)
			}
		}
		return nil
	})
}

// BindingService 是 Agent-Skill 装载关系业务编排层（spec §3.2）。
//
// 所有方法第一句校验 parentUserID != 0（S3 reviewer P2-1 修复）。
// 所有写路径都先校验 agent_id 属于 parentUserID，防跨租户 attach。
type BindingService struct {
	store      IBindingStore
	skillStore IStore
	db         *gorm.DB
}

// NewBindingService 构造 BindingService。
func NewBindingService(db *gorm.DB) *BindingService {
	return &BindingService{
		store:      NewBindingStore(db),
		skillStore: NewStore(db),
		db:         db,
	}
}

// validateAgentOwnership 校验 agent_id 属于 parentUserID。
// 不存在或跨租户 → ErrSkillArtifactNotFound（spec：不区分以防资源枚举）。
func (b *BindingService) validateAgentOwnership(ctx context.Context, parentUserID, agentID uint) error {
	var ad model.AgentDefinition
	err := b.db.WithContext(ctx).
		Select("id", "parent_user_id").
		Where("id = ?", agentID).
		First(&ad).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSkillArtifactNotFound
		}
		return fmt.Errorf("validateAgentOwnership: %w", err)
	}
	if ad.ParentUserID != parentUserID {
		return errno.ErrSkillArtifactNotFound
	}
	return nil
}

// Attach 装载 Skill 到 Agent。
//
// 流程：
//  1. 子账户兜底（parentUserID == 0 → 403）
//  2. 校验 agent 和 skill 同属 parentUserID
//  3. 查现有 binding：
//     - 不存在 → Create 新 binding（is_active=1, sort_order=入参, bound_at=NOW(), unbound_at=NULL）
//     - 存在且 is_active=1 → 返回 ErrSkillArtifactBindingExists（重复 attach 错误）
//     - 存在且 is_active=0（曾卸载）→ 改 is_active=1 + 更新 sort_order + 清空 unbound_at + bound_at=NOW()（复装路径）
//
// uk_agent_skill 唯一约束保证每 (agent, skill) 只有 1 行，复装时不新增行。
func (b *BindingService) Attach(ctx context.Context, parentUserID, agentID, skillID uint, sortOrder int) error {
	if parentUserID == 0 {
		return errno.ErrPermissionDenied
	}

	// 校验 agent 所属租户
	if err := b.validateAgentOwnership(ctx, parentUserID, agentID); err != nil {
		return err
	}
	// 校验 skill 所属租户（同时验存在性）
	sk, err := b.skillStore.Get(ctx, parentUserID, skillID)
	if err != nil {
		return err
	}

	// 重名守卫：运行时按 name 解析 Agent 的绑定技能，两个同名活跃绑定会让 runner
	// hard-error 整个 run 卡死（agent 不可用）。在 attach 时即拦截同名绑定。
	// best-effort：列表失败不硬阻塞 attach（与下方 binding 流程一致的降级姿态）。
	bound, lerr := b.ListByAgent(ctx, parentUserID, agentID)
	if lerr == nil {
		for i := range bound {
			if bound[i].ID != skillID && bound[i].Name == sk.Name {
				return errno.ErrSkillArtifactNameConflict.SetMessage(
					"已绑定同名技能「%s」，请先重命名其中一个再装载", sk.Name)
			}
		}
	}

	existing, err := b.store.Get(ctx, agentID, skillID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("Attach get binding: %w", err)
		}
		// 不存在 → 新建
		newBinding := &model.AgentSkillBinding{
			AgentID:   agentID,
			SkillID:   skillID,
			SortOrder: int16(sortOrder),
			IsActive:  true,
			BoundAt:   time.Now(),
		}
		return b.store.Create(ctx, newBinding)
	}

	// 已存在
	if existing.IsActive {
		return errno.ErrSkillArtifactBindingExists
	}
	// 复装路径：复活
	existing.IsActive = true
	existing.SortOrder = int16(sortOrder)
	existing.BoundAt = time.Now()
	existing.UnboundAt = nil
	return b.store.Update(ctx, existing)
}

// Detach 卸载 Skill（软删 binding）。已卸载或不存在 → ErrSkillArtifactNotFound。
func (b *BindingService) Detach(ctx context.Context, parentUserID, agentID, skillID uint) error {
	if parentUserID == 0 {
		return errno.ErrPermissionDenied
	}
	if err := b.validateAgentOwnership(ctx, parentUserID, agentID); err != nil {
		return err
	}
	return b.store.SoftDeleteByAgent(ctx, agentID, skillID)
}

// Reorder 按 skillIDs 数组顺序批量更新 sort_order（0, 1, 2, ...）。
//
// 流程：
//  1. 子账户兜底
//  2. 校验 agent 属租户
//  3. 事务内逐条 update（只影响活跃 binding；非活跃或不存在的 skillID 静默跳过）
//
// 不校验 skillIDs 是否完整覆盖该 agent 的所有活跃 binding——caller 决定全量或部分重排。
func (b *BindingService) Reorder(ctx context.Context, parentUserID, agentID uint, skillIDs []uint) error {
	if parentUserID == 0 {
		return errno.ErrPermissionDenied
	}
	if err := b.validateAgentOwnership(ctx, parentUserID, agentID); err != nil {
		return err
	}
	return b.store.UpdateSortOrders(ctx, agentID, skillIDs)
}

// ListByAgent 列出 agent 装载的活跃 Skill（按 sort_order ASC）。
//
// 返回 model.Skill 数组（前端只关心 skill 元数据，不需要 binding 行），
// 内部按 binding.sort_order 排序后再批量取对应 skill 行。
func (b *BindingService) ListByAgent(ctx context.Context, parentUserID, agentID uint) ([]model.Skill, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}
	if err := b.validateAgentOwnership(ctx, parentUserID, agentID); err != nil {
		return nil, err
	}

	bindings, err := b.store.ListByAgent(ctx, agentID, false)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return []model.Skill{}, nil
	}

	// 收集 skill_id（保持 sort_order ASC 顺序）
	skillIDs := make([]uint, 0, len(bindings))
	for _, bd := range bindings {
		skillIDs = append(skillIDs, bd.SkillID)
	}

	// 批量取 skill 行（按租户过滤；活跃只）
	var skills []model.Skill
	err = b.db.WithContext(ctx).
		Where("id IN ? AND parent_user_id = ? AND is_active = ?", skillIDs, parentUserID, true).
		Find(&skills).Error
	if err != nil {
		return nil, fmt.Errorf("BindingService.ListByAgent fetch skills: %w", err)
	}

	// 按 sort_order 重新排序——SQL IN 不保证顺序
	byID := make(map[uint]model.Skill, len(skills))
	for _, sk := range skills {
		byID[sk.ID] = sk
	}
	ordered := make([]model.Skill, 0, len(skillIDs))
	for _, sid := range skillIDs {
		if sk, ok := byID[sid]; ok {
			ordered = append(ordered, sk)
		}
	}
	return ordered, nil
}
