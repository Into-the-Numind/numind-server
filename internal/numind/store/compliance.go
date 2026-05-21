package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/compliance_scope"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ListAdminOpts holds filter + pagination params for ListRulesAdmin.
type ListAdminOpts struct {
	ParentUserID uint
	RuleType     string
	IsActive     *bool // nil = all
	Offset       int
	Limit        int
}

// IComplianceStore defines the store interface for L1 compliance rules and
// the append-only audit log. See agent-mode-compliance-3layer spec §3.1.
type IComplianceStore interface {
	// ListRulesByParent returns compliance rules for a given parent account.
	// When activeOnly=true, only is_active=1 rules are returned.
	// Results are sorted priority ASC, then created_at DESC.
	ListRulesByParent(ctx context.Context, parentUserID uint, activeOnly bool) ([]*model.ComplianceRule, error)

	// ListRulesAdmin returns rules with optional filters and pagination (M-C1a admin endpoint).
	// Returns (rules, totalCount, error). parentUserID=0 means no filter on that column.
	ListRulesAdmin(ctx context.Context, opts ListAdminOpts) ([]*model.ComplianceRule, int64, error)

	// GetRule fetches a single compliance rule by ID.
	// Returns errno.ErrComplianceRuleNotFound when no row matches.
	GetRule(ctx context.Context, id uint64) (*model.ComplianceRule, error)

	// CreateRule inserts a new compliance rule.
	// Includes UpdateColumn fixup for the GORM default:true bool gotcha
	// when caller sets IsActive=false (see database.md §6).
	CreateRule(ctx context.Context, rule *model.ComplianceRule) error

	// UpdateRule applies a map of column updates to the rule identified by id.
	// Map form is mandatory to avoid GORM struct zero-value skipping false bools.
	UpdateRule(ctx context.Context, id uint64, updates map[string]interface{}) error

	// SoftDeleteRule sets is_active=0 via UpdateColumn (preserves audit refs).
	SoftDeleteRule(ctx context.Context, id uint64) error

	// WriteAuditLog appends one compliance decision record (append-only).
	WriteAuditLog(ctx context.Context, entry *model.ComplianceAuditLog) error
}

type complianceStore struct{ db *gorm.DB }

var _ IComplianceStore = (*complianceStore)(nil)

func newCompliance(db *gorm.DB) IComplianceStore { return &complianceStore{db: db} }

// ListRulesAdmin returns compliance rules with optional filters and pagination.
// parentUserID=0 skips the parent_user_id filter (cross-tenant admin view).
func (s *complianceStore) ListRulesAdmin(ctx context.Context, opts ListAdminOpts) ([]*model.ComplianceRule, int64, error) {
	q := s.db.WithContext(ctx).Model(&model.ComplianceRule{})
	if opts.ParentUserID != 0 {
		q = q.Where("parent_user_id = ?", opts.ParentUserID)
	}
	if opts.RuleType != "" {
		q = q.Where("rule_type = ?", opts.RuleType)
	}
	if opts.IsActive != nil {
		q = q.Where("is_active = ?", *opts.IsActive)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("complianceStore.ListRulesAdmin.Count: %w", err)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	var rules []*model.ComplianceRule
	if err := q.Order("priority ASC, created_at DESC").Offset(opts.Offset).Limit(limit).Find(&rules).Error; err != nil {
		return nil, 0, fmt.Errorf("complianceStore.ListRulesAdmin.Find: %w", err)
	}
	return rules, total, nil
}

// ListRulesByParent returns rules for the given parent account.
// ctx is wrapped with WithSkipScope("compliance_self") to prevent the
// scope_validator GORM Before-Query hook from re-entering audit logging
// (which would cause a recursive write loop). See spec §9.1 + plan §3 M6.
func (s *complianceStore) ListRulesByParent(ctx context.Context, parentUserID uint, activeOnly bool) ([]*model.ComplianceRule, error) {
	ctx = compliance_scope.WithSkipScope(ctx, "compliance_self")
	var rules []*model.ComplianceRule
	q := s.db.WithContext(ctx).Where("parent_user_id = ?", parentUserID)
	if activeOnly {
		q = q.Where("is_active = ?", true)
	}
	q = q.Order("priority ASC, created_at DESC")
	if err := q.Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("complianceStore.ListRulesByParent: %w", err)
	}
	return rules, nil
}

// GetRule fetches a single rule by ID.
func (s *complianceStore) GetRule(ctx context.Context, id uint64) (*model.ComplianceRule, error) {
	var rule model.ComplianceRule
	if err := s.db.WithContext(ctx).First(&rule, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrComplianceRuleNotFound
		}
		return nil, fmt.Errorf("complianceStore.GetRule: %w", err)
	}
	return &rule, nil
}

// CreateRule inserts a new compliance rule.
// Critical: captures caller's IsActive intent before Create, then applies
// UpdateColumn fixup if GORM's default:true silently flipped false→true.
// See database.md §6 for the full explanation of this gotcha.
func (s *complianceStore) CreateRule(ctx context.Context, rule *model.ComplianceRule) error {
	wantActive := rule.IsActive
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		return fmt.Errorf("complianceStore.CreateRule: %w", err)
	}
	// GORM may have silently written DB default (true) because bool zero-value
	// false is treated as "field not set". UpdateColumn bypasses hooks and
	// updated_at, keeping the operation invisible in audit surfaces.
	if !wantActive && rule.IsActive {
		if err := s.db.WithContext(ctx).Model(rule).UpdateColumn("is_active", false).Error; err != nil {
			return fmt.Errorf("complianceStore.CreateRule (fixup): %w", err)
		}
		rule.IsActive = false
	}
	return nil
}

// UpdateRule applies partial updates via map form. Map form is required to
// prevent GORM struct zero-value from silently dropping false bool fields.
// See database.md §6b for the dangerous struct-form alternative.
func (s *complianceStore) UpdateRule(ctx context.Context, id uint64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).Model(&model.ComplianceRule{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("complianceStore.UpdateRule: %w", err)
	}
	return nil
}

// SoftDeleteRule sets is_active=0 via UpdateColumn. Does not hard-delete the
// row so that existing audit log entries referencing the rule_id remain valid.
// See spec §1.5 (no FK on compliance_audit_log.rule_id, intentional).
func (s *complianceStore) SoftDeleteRule(ctx context.Context, id uint64) error {
	if err := s.db.WithContext(ctx).Model(&model.ComplianceRule{}).Where("id = ?", id).UpdateColumn("is_active", false).Error; err != nil {
		return fmt.Errorf("complianceStore.SoftDeleteRule: %w", err)
	}
	return nil
}

// WriteAuditLog appends one compliance decision record.
// ctx is wrapped with WithSkipScope("compliance_self") to avoid the
// scope_validator hook triggering another WriteAuditLog call (recursive loop).
// See spec §9.1 + plan §3 M6.
func (s *complianceStore) WriteAuditLog(ctx context.Context, entry *model.ComplianceAuditLog) error {
	ctx = compliance_scope.WithSkipScope(ctx, "compliance_self")
	if err := s.db.WithContext(ctx).Create(entry).Error; err != nil {
		return fmt.Errorf("complianceStore.WriteAuditLog: %w", err)
	}
	return nil
}
