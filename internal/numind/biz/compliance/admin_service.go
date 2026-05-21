package compliance

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// AdminService provides admin CRUD operations for compliance rules (M-C1a).
// Every write invalidates the TTLCache for the affected parent_user_id to
// prevent stale rules being served during the TTL window.
type AdminService struct {
	store store.IComplianceStore
	cache *TTLCache
}

// NewAdminService constructs an AdminService.
func NewAdminService(s store.IComplianceStore, c *TTLCache) *AdminService {
	return &AdminService{store: s, cache: c}
}

// ListOpts holds optional filter parameters for List.
type ListOpts struct {
	ParentUserID uint
	RuleType     string
	IsActive     *bool // nil = all, true = active only, false = inactive only
	Page         int
	PageSize     int
}

// ListResult is returned by List.
type ListResult struct {
	Rules []*model.ComplianceRule
	Total int64
}

// List returns compliance rules, optionally filtered, with pagination.
func (s *AdminService) List(ctx context.Context, opts ListOpts) (*ListResult, error) {
	if opts.Page < 1 {
		opts.Page = 1
	}
	if opts.PageSize <= 0 || opts.PageSize > 100 {
		opts.PageSize = 20
	}

	rules, total, err := s.store.ListRulesAdmin(ctx, store.ListAdminOpts{
		ParentUserID: opts.ParentUserID,
		RuleType:     opts.RuleType,
		IsActive:     opts.IsActive,
		Offset:       (opts.Page - 1) * opts.PageSize,
		Limit:        opts.PageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("AdminService.List: %w", err)
	}
	return &ListResult{Rules: rules, Total: total}, nil
}

// CreateRequest holds fields for creating a new compliance rule.
type CreateRequest struct {
	ParentUserID uint
	RuleType     string
	RuleText     string
	Priority     int
	IsActive     *bool // *bool to handle default:true gotcha
}

// Create inserts a new compliance rule and invalidates the cache.
// IsActive defaults to true when nil (server side default).
func (s *AdminService) Create(ctx context.Context, req CreateRequest) (*model.ComplianceRule, error) {
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	priority := req.Priority
	if priority == 0 {
		priority = 100
	}

	rule := &model.ComplianceRule{
		ParentUserID: req.ParentUserID,
		RuleType:     req.RuleType,
		RuleText:     req.RuleText,
		Priority:     priority,
		IsActive:     active,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := s.store.CreateRule(ctx, rule); err != nil {
		return nil, fmt.Errorf("AdminService.Create: %w", err)
	}
	// Invalidate cache so the next compliance check reads the freshly inserted rule.
	s.cache.Invalidate(req.ParentUserID)
	return rule, nil
}

// Get fetches a single compliance rule by ID.
func (s *AdminService) Get(ctx context.Context, id uint64) (*model.ComplianceRule, error) {
	rule, err := s.store.GetRule(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("AdminService.Get: %w", err)
	}
	return rule, nil
}

// PatchRequest holds fields that may be partially updated.
// Nil pointer fields are left unchanged.
type PatchRequest struct {
	RuleText *string
	RuleType *string
	Priority *int
	IsActive *bool
}

// Patch applies partial updates to an existing compliance rule and invalidates cache.
func (s *AdminService) Patch(ctx context.Context, id uint64, req PatchRequest) (*model.ComplianceRule, error) {
	// Fetch current rule first to get parent_user_id for cache invalidation.
	existing, err := s.store.GetRule(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("AdminService.Patch get: %w", err)
	}

	updates := make(map[string]interface{})
	if req.RuleText != nil {
		updates["rule_text"] = *req.RuleText
	}
	if req.RuleType != nil {
		updates["rule_type"] = *req.RuleType
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if len(updates) == 0 {
		return existing, nil
	}
	updates["updated_at"] = time.Now()

	if err := s.store.UpdateRule(ctx, id, updates); err != nil {
		return nil, fmt.Errorf("AdminService.Patch update: %w", err)
	}
	// Invalidate cache so stale rules are not served.
	s.cache.Invalidate(existing.ParentUserID)

	// Re-fetch updated rule.
	updated, err := s.store.GetRule(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("AdminService.Patch re-fetch: %w", err)
	}
	return updated, nil
}

// Delete soft-deletes a compliance rule (sets is_active=false) and invalidates cache.
func (s *AdminService) Delete(ctx context.Context, id uint64) error {
	// Fetch first to get parent_user_id for cache invalidation.
	existing, err := s.store.GetRule(ctx, id)
	if err != nil {
		return fmt.Errorf("AdminService.Delete get: %w", err)
	}
	if err := s.store.SoftDeleteRule(ctx, id); err != nil {
		return fmt.Errorf("AdminService.Delete soft-delete: %w", err)
	}
	s.cache.Invalidate(existing.ParentUserID)
	return nil
}
