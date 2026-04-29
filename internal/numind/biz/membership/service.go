// Package membership provides business logic for the membership/credits subsystem.
// It covers: trial grant, subscription grant/renewal, booster purchase, and
// credit consumption. All writes go through explicit DB transactions.
package membership

import (
	"gorm.io/gorm"

	membershipstore "numind-server/internal/numind/store/membership"
)

// MembershipService is the entry-point for all membership-related business
// operations. Obtain an instance via NewMembershipService and call individual
// methods (GrantTrial, GrantSubscription, …) on it.
type MembershipService struct {
	db    *gorm.DB
	store membershipstore.IMembershipStore
}

// NewMembershipService constructs a MembershipService backed by db.
// The IMembershipStore is built from db so that tests can inject an in-memory
// SQLite DB and get a fully functional service without any mocking.
func NewMembershipService(db *gorm.DB) *MembershipService {
	return &MembershipService{
		db:    db,
		store: membershipstore.NewMembershipStore(db),
	}
}
