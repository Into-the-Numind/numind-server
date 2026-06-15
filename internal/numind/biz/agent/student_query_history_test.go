package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// mkHistRun inserts an agent_run with an explicit started_at / pinned flag so we
// can exercise the time-window removal and ordering of ListAllHistorySessions.
func mkHistRun(t *testing.T, db *gorm.DB, userID uint, sessionID string, started time.Time, pinned bool) {
	t.Helper()
	msgs, _ := json.Marshal([]string{})
	require.NoError(t, db.Create(&model.AgentRun{
		UserID:    userID,
		SessionID: sessionID,
		Status:    "completed",
		Messages:  msgs,
		StartedAt: started,
		IsPinned:  pinned,
	}).Error)
}

// adaptive-session-titles US4: ListAllHistorySessions must return ALL sessions,
// including ones older than the previously-hardcoded 30-day window.
func TestListAllHistorySessions_NoTimeWindow_ReturnsAll(t *testing.T) {
	svc, db := newSQService(t)
	now := time.Now()
	mkHistRun(t, db, 1, "recent", now.Add(-time.Hour), false)
	mkHistRun(t, db, 1, "ancient", now.AddDate(0, 0, -60), false) // > 30d old

	got, err := svc.ListAllHistorySessions(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 2, "both sessions returned — no 30-day truncation")
	sids := map[string]bool{}
	for _, s := range got {
		sids[s.SessionID] = true
	}
	assert.True(t, sids["recent"], "recent session present")
	assert.True(t, sids["ancient"], "60-day-old session must NOT be truncated (US4)")
}

// US4: newest on top — pinned sessions first, then by started_at DESC.
func TestListAllHistorySessions_OrderPinnedThenNewest(t *testing.T) {
	svc, db := newSQService(t)
	now := time.Now()
	mkHistRun(t, db, 1, "older", now.Add(-3*time.Hour), false)
	mkHistRun(t, db, 1, "newer", now.Add(-1*time.Hour), false)
	mkHistRun(t, db, 1, "pinned-old", now.Add(-10*time.Hour), true)

	got, err := svc.ListAllHistorySessions(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "pinned-old", got[0].SessionID, "pinned session first")
	assert.Equal(t, "newer", got[1].SessionID, "then newest started_at")
	assert.Equal(t, "older", got[2].SessionID)
}
