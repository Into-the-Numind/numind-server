package marketplace

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// SetRecommended toggles the platform-recommended flag on a marketplace item.
//
// Admin-only: caller path goes through /v1/admin/marketplace/:id/recommend
// which is protected by admin_token middleware (spec §4.2). The service layer
// therefore does NOT verify a parent account — admin actions on behalf of the
// platform are not subject to the same tenant rules as user actions.
//
// Audit trail: admin_token middleware already records the admin operation per
// existing platform conventions; this method writes no additional audit log.
func (s *service) SetRecommended(ctx context.Context, marketplaceID uint, recommended bool) error {
	if err := s.store.UpdateRecommended(ctx, marketplaceID, recommended); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrMarketplaceNotFound
		}
		return fmt.Errorf("SetRecommended: %w", err)
	}
	return nil
}
