package skill

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/model"
)

// AvailableForStudent returns the active agent_definitions visible to a learner.
// "Visible" means is_active=1 AND parent_user_id = learner's parent.
// Returns an empty slice (not an error) when the learner has no parent, i.e. they
// are a parent account themselves — parent accounts do not see their own agents here.
func (s *service) AvailableForStudent(ctx context.Context, learnerUserID uint) ([]*model.AgentDefinition, error) {
	// 1. Look up learner to get parent_user_id.
	learner, err := s.ds.Users().GetByID(ctx, learnerUserID)
	if err != nil {
		return nil, fmt.Errorf("AvailableForStudent get user(%d): %w", learnerUserID, err)
	}

	// 2. If no parent → caller is a parent account; return empty (not visible to self).
	if learner.ParentUserID == nil {
		return []*model.AgentDefinition{}, nil
	}

	// 3. List active agents owned by the parent account.
	parentID := *learner.ParentUserID
	items, _, err := s.skillStore.ListByParent(ctx, parentID, false /* activeOnly */, 0, 1000)
	if err != nil {
		return nil, fmt.Errorf("AvailableForStudent list for parent(%d): %w", parentID, err)
	}

	ptrs := make([]*model.AgentDefinition, len(items))
	for i := range items {
		cp := items[i]
		ptrs[i] = &cp
	}
	return ptrs, nil
}
