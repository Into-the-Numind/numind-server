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

// IStore 是 Skill artifact 数据访问接口（mock 友好）。
//
// 所有方法都按 parent_user_id 过滤以强制租户隔离——业务层传入 parentUserID 后，
// 跨租户访问统一返回 ErrSkillArtifactNotFound（不区分"无权"和"不存在"以防资源枚举）。
//
// 参数命名约定：skillID/agentID 均为 uint（model.Skill / model.AgentSkillBinding ID 类型）。
//
// 事务约定：方法第一个参数若是 *gorm.DB，表示 caller 显式传入 tx；否则用 store 自带 db。
// Service 层进事务时调 *Tx 变体，确保所有写操作走同一个事务连接（SQLite single-conn 友好）。
type IStore interface {
	// Create 写入新 Skill 行（默认走 store.db）。
	// is_active 默认 true（GORM default:1 陷阱），实现内用 `Select("*")` 兜底（database.md §6）。
	Create(ctx context.Context, s *model.Skill) error

	// CreateTx 在显式 tx 内写入新 Skill。
	CreateTx(ctx context.Context, tx *gorm.DB, s *model.Skill) error

	// Get 按 (parentUserID, skillID) 查询 Skill（含 is_active=0，供详情/恢复用）。
	// 跨租户或不存在 → ErrSkillArtifactNotFound。
	Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error)

	// List 按租户列举活跃 Skill（is_active=1），支持 name LIKE 搜索 + 分页。
	// 排序：updated_at DESC。
	List(ctx context.Context, parentUserID uint, search string, offset, limit int) ([]model.Skill, int64, error)

	// Update 全量更新 Skill 行。
	// `db.Save` 安全（database.md §6b），bool 字段不丢。
	Update(ctx context.Context, s *model.Skill) error

	// UpdateTx 在显式 tx 内全字段 Save。
	UpdateTx(ctx context.Context, tx *gorm.DB, s *model.Skill) error

	// SoftDelete 事务内置 skill.is_active=0 + 级联 agent_skill_binding.is_active=0。
	// 返回受影响的 binding 行数（用于 controller 返回 affected_bindings）。
	SoftDelete(ctx context.Context, parentUserID, skillID uint) (affectedBindings int64, err error)

	// ListBoundAgents 返回装载了该 Skill 的活跃 Agent 列表（按 binding.bound_at DESC）。
	// 校验 parentUserID 所有权（skill 与 agent 都必须属同租户）。
	ListBoundAgents(ctx context.Context, parentUserID, skillID uint) ([]model.AgentDefinition, error)
}

// gormStore 是 IStore 的 GORM 实现。所有方法都在 *gorm.DB 上操作，可注入测试 DB。
type gormStore struct {
	db *gorm.DB
}

var _ IStore = (*gormStore)(nil)

// NewStore 构造默认 GORM 实现。
func NewStore(db *gorm.DB) IStore {
	return &gormStore{db: db}
}

// Create 写入新 Skill 行。使用 `Select("*")` 强制写所有列，绕过 GORM default:1 bool 陷阱
// （database.md §6）—— 调用者传 IsActive=false 才能落库成 false。
func (s *gormStore) Create(ctx context.Context, m *model.Skill) error {
	return s.CreateTx(ctx, s.db, m)
}

// CreateTx 在指定事务内写入 Skill；Select("*") 兜底 IsActive default:1 陷阱。
func (s *gormStore) CreateTx(ctx context.Context, tx *gorm.DB, m *model.Skill) error {
	if err := tx.WithContext(ctx).Select("*").Create(m).Error; err != nil {
		return fmt.Errorf("Skill store.CreateTx: %w", err)
	}
	return nil
}

// Get 按租户 + skill_id 查询；跨租户或不存在均返回 ErrSkillArtifactNotFound。
//
// 注意：不过滤 is_active —— restore 路径需要拿到软删 skill 复活，详情接口也需要展示。
// 业务层（service）按需过滤 is_active。
func (s *gormStore) Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error) {
	var m model.Skill
	err := s.db.WithContext(ctx).
		Where("id = ? AND parent_user_id = ?", skillID, parentUserID).
		First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSkillArtifactNotFound
		}
		return nil, fmt.Errorf("Skill store.Get: %w", err)
	}
	return &m, nil
}

// List 按租户列举活跃 Skill（is_active=1）。search 非空时按 name LIKE 过滤。
//
// 索引利用：idx_skill_parent_active (parent_user_id, is_active, updated_at DESC)。
func (s *gormStore) List(ctx context.Context, parentUserID uint, search string, offset, limit int) ([]model.Skill, int64, error) {
	q := s.db.WithContext(ctx).Model(&model.Skill{}).
		Where("parent_user_id = ? AND is_active = ?", parentUserID, true)
	if search != "" {
		q = q.Where("name LIKE ?", "%"+search+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("Skill store.List count: %w", err)
	}

	var items []model.Skill
	if err := q.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("Skill store.List find: %w", err)
	}
	return items, total, nil
}

// Update 使用 `db.Save` 全字段写入；bool 字段（IsActive）零值不丢失（database.md §6b 已验证）。
func (s *gormStore) Update(ctx context.Context, m *model.Skill) error {
	return s.UpdateTx(ctx, s.db, m)
}

// UpdateTx 在指定事务内 Save。
func (s *gormStore) UpdateTx(ctx context.Context, tx *gorm.DB, m *model.Skill) error {
	if err := tx.WithContext(ctx).Save(m).Error; err != nil {
		return fmt.Errorf("Skill store.UpdateTx: %w", err)
	}
	return nil
}

// SoftDelete 事务内做两件事：
//  1. skill.is_active = 0（按 parent_user_id 校验所有权）
//  2. 级联 agent_skill_binding.is_active = 0 + unbound_at = NOW()，
//     仅影响该 skill_id 下所有 is_active=1 的 binding。
//
// 跨租户或 skill 不存在 → ErrSkillArtifactNotFound。
// 返回受影响的 binding 行数（已生效软删的）。
func (s *gormStore) SoftDelete(ctx context.Context, parentUserID, skillID uint) (int64, error) {
	var affected int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 找 skill 确认所有权 + 是否存在
		var sk model.Skill
		if err := tx.Where("id = ? AND parent_user_id = ?", skillID, parentUserID).First(&sk).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrSkillArtifactNotFound
			}
			return fmt.Errorf("SoftDelete get skill: %w", err)
		}

		// 2. 软删 skill（用 Update map 避免 default:1 陷阱）
		if err := tx.Model(&model.Skill{}).
			Where("id = ?", sk.ID).
			Updates(map[string]interface{}{"is_active": false}).Error; err != nil {
			return fmt.Errorf("SoftDelete skill: %w", err)
		}

		// 3. 级联软删活跃 binding
		now := time.Now()
		res := tx.Model(&model.AgentSkillBinding{}).
			Where("skill_id = ? AND is_active = ?", sk.ID, true).
			Updates(map[string]interface{}{
				"is_active":  false,
				"unbound_at": &now,
			})
		if res.Error != nil {
			return fmt.Errorf("SoftDelete cascade bindings: %w", res.Error)
		}
		affected = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// ListBoundAgents 通过 agent_skill_binding 反查装载该 Skill 的活跃 Agent。
//
// 双重租户校验：
//   - skill 必须属于 parentUserID（不存在 → ErrSkillArtifactNotFound）
//   - agent_definition.parent_user_id = parentUserID（JOIN 时强制）
func (s *gormStore) ListBoundAgents(ctx context.Context, parentUserID, skillID uint) ([]model.AgentDefinition, error) {
	// 先校验 skill 所有权
	if _, err := s.Get(ctx, parentUserID, skillID); err != nil {
		return nil, err
	}

	var agents []model.AgentDefinition
	err := s.db.WithContext(ctx).
		Table("agent_definition AS ad").
		Select("ad.*").
		Joins("INNER JOIN agent_skill_binding AS b ON b.agent_id = ad.id").
		Where("b.skill_id = ? AND b.is_active = ? AND ad.parent_user_id = ? AND ad.is_active = ?",
			skillID, true, parentUserID, true).
		Order("b.bound_at DESC").
		Scan(&agents).Error
	if err != nil {
		return nil, fmt.Errorf("Skill store.ListBoundAgents: %w", err)
	}
	return agents, nil
}
