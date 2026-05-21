package skill

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/model"
)

// AvailableForStudent returns the active agent_definitions visible to a learner.
// "Visible" means is_active=1 AND parent_user_id = learner's parent.
// For learners (parent_user_id is set): shows agents owned by their parent.
// For parent accounts themselves (parent_user_id NULL): shows their own agents,
// enabling the 试聊 (trial chat) flow from admin-web. The parent is acting as
// their own learner when trying out an agent they created.
func (s *service) AvailableForStudent(ctx context.Context, learnerUserID uint) ([]*model.AgentDefinition, error) {
	// 1. Look up learner to get parent_user_id.
	learner, err := s.ds.Users().GetByID(ctx, learnerUserID)
	if err != nil {
		return nil, fmt.Errorf("AvailableForStudent get user(%d): %w", learnerUserID, err)
	}

	// 2. Resolve effective parent id: own id if no parent (parent account itself
	// browses their own agents for 试聊), otherwise the registered parent id.
	var parentID uint
	if learner.ParentUserID != nil {
		parentID = *learner.ParentUserID
	} else {
		parentID = learnerUserID
	}

	// 3. List active agents owned by the parent account.
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
