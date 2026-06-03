package admin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	agentbiz "numind-server/internal/numind/biz/agent"
	adminctl "numind-server/internal/numind/controller/v1/admin"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test DB + router helpers
// ---------------------------------------------------------------------------

func newARTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/agent_run_ctl_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id                         INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                    INTEGER NOT NULL DEFAULT 0,
			session_id                 TEXT    NOT NULL DEFAULT '',
			status                     TEXT    NOT NULL DEFAULT 'running',
			state_reason               TEXT    NOT NULL DEFAULT '',
			messages                   TEXT    NOT NULL DEFAULT '[]',
			reservation_id             INTEGER,
			terminal_metadata          TEXT,
			started_at                 DATETIME NOT NULL,
			ended_at                   DATETIME,
			compact_state              TEXT,
			compact_summary            TEXT,
			cancellation_requested_at  DATETIME,
			agent_definition_id        INTEGER NOT NULL DEFAULT 0,
			pending_question_json      TEXT,
			pending_question_at        DATETIME,
			created_at                 DATETIME,
			updated_at                 DATETIME,
			-- V1.5 板块 2 task 2.1 — context-management V2 columns
			compact_state_v2           TEXT,
			total_tokens_used_v2       INTEGER NOT NULL DEFAULT 0,
			use_compact_v2             INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2    INTEGER,
			is_pinned                  INTEGER NOT NULL DEFAULT 0,
			session_name               TEXT    NOT NULL DEFAULT '',
			is_deleted                 INTEGER NOT NULL DEFAULT 0,
			is_test                 INTEGER NOT NULL DEFAULT 0
		)`).Error)

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_definition (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id INTEGER NOT NULL DEFAULT 0,
			name           TEXT    NOT NULL DEFAULT ''
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newARRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := newARTestDB(t)
	s := store.NewTestStore(db)
	svc := agentbiz.NewAgentAdminService(s.AgentRuns(), nil)
	ctrl := adminctl.NewAgentRunController(svc)

	r := gin.New()
	v1 := r.Group("/v1/admin")
	v1.GET("/agent-runs", ctrl.List)
	v1.POST("/agent-runs/:id/cancel", ctrl.Cancel)
	return r, db
}

// ---------------------------------------------------------------------------
// TestAgentRun_Cancel_Happy_204
// ---------------------------------------------------------------------------

func TestAgentRun_Cancel_Happy_204(t *testing.T) {
	r, db := newARRouter(t)

	run := &model.AgentRun{
		UserID:    1,
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	path := fmt.Sprintf("/v1/admin/agent-runs/%d/cancel", run.ID)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// DB should have cancellation_requested_at set.
	var updated model.AgentRun
	require.NoError(t, db.First(&updated, run.ID).Error)
	assert.NotNil(t, updated.CancellationRequestedAt)
}

// ---------------------------------------------------------------------------
// TestAgentRun_Cancel_NotFound_404
// ---------------------------------------------------------------------------

func TestAgentRun_Cancel_NotFound_404(t *testing.T) {
	r, _ := newARRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/agent-runs/999999/cancel", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// TestAgentRun_Cancel_AlreadyTerminal_409
// ---------------------------------------------------------------------------

func TestAgentRun_Cancel_AlreadyTerminal_409(t *testing.T) {
	r, db := newARRouter(t)

	run := &model.AgentRun{
		UserID:    2,
		Status:    "completed",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	path := fmt.Sprintf("/v1/admin/agent-runs/%d/cancel", run.ID)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

// ---------------------------------------------------------------------------
// TestAgentRun_List_Happy
// ---------------------------------------------------------------------------

func TestAgentRun_List_Happy(t *testing.T) {
	r, db := newARRouter(t)

	// Seed 2 running runs.
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Create(&model.AgentRun{
			UserID:    uint(i + 1),
			Status:    "running",
			Messages:  datatypes.JSON(`[]`),
			StartedAt: time.Now(),
		}).Error)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/agent-runs?status=running&page=1&page_size=20", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(0), resp["code"])
	data := resp["data"].(map[string]interface{})
	assert.Equal(t, float64(2), data["total"])
	list := data["list"].([]interface{})
	assert.Len(t, list, 2)
}

// ---------------------------------------------------------------------------
// TestAgentRun_List_InvalidStatus_ReturnsEmpty
// ---------------------------------------------------------------------------

func TestAgentRun_List_InvalidStatus_ReturnsEmpty(t *testing.T) {
	r, _ := newARRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/agent-runs?status=nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]interface{})
	assert.Equal(t, float64(0), data["total"])
}
