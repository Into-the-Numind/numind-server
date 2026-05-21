package skill

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newStudentQueryTestService creates an in-memory SQLite-backed Service seeded with:
//   - 1 parent user (ParentUserID nil)
//   - 1 child user  (ParentUserID → parent)
//   - 2 active agents owned by parent
//   - 1 inactive agent owned by parent
func newStudentQueryTestService(t *testing.T) (Service, *model.User, *model.User) {
	t.Helper()
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.AgentDefinition{},
		&model.AgentDefinitionHistory{},
		&model.SkillTemplate{},
	))

	// Seed parent.
	parent := &model.User{Username: "sq-parent"}
	require.NoError(t, db.Create(parent).Error)

	// Seed child.
	pid := parent.ID
	child := &model.User{Username: "sq-child", ParentUserID: &pid}
	require.NoError(t, db.Create(child).Error)

	// Seed 2 active agents owned by parent.
	for _, name := range []string{"Agent A", "Agent B"} {
		ad := &model.AgentDefinition{
			ParentUserID: parent.ID,
			Name:         name,
			IsActive:     true,
		}
		require.NoError(t, db.Create(ad).Error)
	}

	// Seed 1 inactive agent owned by parent.
	inactive := &model.AgentDefinition{
		ParentUserID: parent.ID,
		Name:         "Inactive Agent",
		IsActive:     false,
	}
	require.NoError(t, db.Create(inactive).Error)
	// Apply is_active=false fixup (database.md §6 GORM default:true gotcha).
	require.NoError(t, db.Model(inactive).UpdateColumn("is_active", false).Error)

	ds := store.NewTestStore(db)
	svc := NewService(ds)
	return svc, parent, child
}

// ---------------------------------------------------------------------------
// TestAvailableForStudent_Parent_Empty
// ---------------------------------------------------------------------------

// TestAvailableForStudent_Parent_Empty verifies that a parent account (no
// ParentUserID) receives an empty slice — not an error.
func TestAvailableForStudent_Parent_Empty(t *testing.T) {
	svc, parent, _ := newStudentQueryTestService(t)

	got, err := svc.AvailableForStudent(context.Background(), parent.ID)
	require.NoError(t, err)
	assert.Empty(t, got, "parent account should see 0 agents via AvailableForStudent")
}

// ---------------------------------------------------------------------------
// TestAvailableForStudent_Child_FiltersByParent
// ---------------------------------------------------------------------------

// TestAvailableForStudent_Child_FiltersByParent verifies that a child user
// receives only the agents belonging to their own parent.
func TestAvailableForStudent_Child_FiltersByParent(t *testing.T) {
	svc, _, child := newStudentQueryTestService(t)

	got, err := svc.AvailableForStudent(context.Background(), child.ID)
	require.NoError(t, err)
	// 2 active agents expected (not 3 — the inactive one is excluded).
	assert.Len(t, got, 2, "child should see exactly the 2 active agents from their parent")
	for _, ad := range got {
		assert.True(t, ad.IsActive, "all returned agents should be active")
	}
}

// ---------------------------------------------------------------------------
// TestAvailableForStudent_InactiveExcluded
// ---------------------------------------------------------------------------

// TestAvailableForStudent_InactiveExcluded verifies that is_active=false agents
// are not returned even though they belong to the parent.
func TestAvailableForStudent_InactiveExcluded(t *testing.T) {
	svc, _, child := newStudentQueryTestService(t)

	got, err := svc.AvailableForStudent(context.Background(), child.ID)
	require.NoError(t, err)

	for _, ad := range got {
		assert.NotEqual(t, "Inactive Agent", ad.Name, "inactive agent must not appear")
	}
}
