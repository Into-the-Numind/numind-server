package store

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newRegenCleanupTestDB builds an isolated in-memory SQLite DB with just the
// three tables CleanupDownstreamForRegeneration touches. FK constraints are
// disabled so we can migrate the node-run / note / chat tables without their
// associated run/node/user tables (which carry MySQL-only column types).
func newRegenCleanupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err, "open sqlite in-memory DB")
	require.NoError(t, db.AutoMigrate(
		&model.SopNodeRun{},
		&model.SopNote{},
		&model.SopChatMsg{},
	), "auto-migrate")
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestCleanupDownstreamForRegeneration_DeletesDownstreamAtomically verifies the
// transactional cleanup removes downstream node runs (sort > afterSort), the
// run's notes, and the run's chat messages — scoped to the target run only.
func TestCleanupDownstreamForRegeneration_DeletesDownstreamAtomically(t *testing.T) {
	db := newRegenCleanupTestDB(t)
	s := NewSopStore(db)

	const targetRun = uint(1)
	const otherRun = uint(2)

	// Target run: node runs at sort 0,1,2,3.
	for _, sort := range []int{0, 1, 2, 3} {
		require.NoError(t, db.Create(&model.SopNodeRun{RunID: targetRun, NodeID: uint(10 + sort), Sort: sort, Status: model.SopStatusSucceeded}).Error)
	}
	// Target run: notes + chat messages.
	require.NoError(t, db.Create(&model.SopNote{RunID: targetRun, UserID: 1, TemplateID: 1, Content: "final note"}).Error)
	require.NoError(t, db.Create(&model.SopChatMsg{RunID: targetRun, UserID: 1, Role: "user", Content: "q"}).Error)
	require.NoError(t, db.Create(&model.SopChatMsg{RunID: targetRun, UserID: 1, Role: "assistant", Content: "a"}).Error)

	// Other run: must be untouched.
	require.NoError(t, db.Create(&model.SopNodeRun{RunID: otherRun, NodeID: 99, Sort: 3, Status: model.SopStatusSucceeded}).Error)
	require.NoError(t, db.Create(&model.SopNote{RunID: otherRun, UserID: 1, TemplateID: 1, Content: "other note"}).Error)
	require.NoError(t, db.Create(&model.SopChatMsg{RunID: otherRun, UserID: 1, Role: "user", Content: "other q"}).Error)

	// Regenerate from sort=1 → downstream is sort > 1.
	require.NoError(t, s.CleanupDownstreamForRegeneration(targetRun, 1))

	// Target run node runs: sort 0,1 survive; sort 2,3 gone.
	var remainingSorts []int
	require.NoError(t, db.Model(&model.SopNodeRun{}).Where("run_id = ?", targetRun).Order("sort ASC").Pluck("sort", &remainingSorts).Error)
	require.Equal(t, []int{0, 1}, remainingSorts, "only upstream node runs (sort <= afterSort) survive")

	// Target run notes + chat fully removed.
	var noteCount, chatCount int64
	require.NoError(t, db.Model(&model.SopNote{}).Where("run_id = ?", targetRun).Count(&noteCount).Error)
	require.NoError(t, db.Model(&model.SopChatMsg{}).Where("run_id = ?", targetRun).Count(&chatCount).Error)
	require.Zero(t, noteCount, "target run notes deleted")
	require.Zero(t, chatCount, "target run chat messages deleted")

	// Other run untouched.
	var otherNodeRuns, otherNotes, otherChat int64
	require.NoError(t, db.Model(&model.SopNodeRun{}).Where("run_id = ?", otherRun).Count(&otherNodeRuns).Error)
	require.NoError(t, db.Model(&model.SopNote{}).Where("run_id = ?", otherRun).Count(&otherNotes).Error)
	require.NoError(t, db.Model(&model.SopChatMsg{}).Where("run_id = ?", otherRun).Count(&otherChat).Error)
	require.Equal(t, int64(1), otherNodeRuns, "other run node runs untouched")
	require.Equal(t, int64(1), otherNotes, "other run notes untouched")
	require.Equal(t, int64(1), otherChat, "other run chat untouched")
}
