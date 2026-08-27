package agent

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
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	bizagent "numind-server/internal/numind/biz/agent"
	skillbiz "numind-server/internal/numind/biz/skill"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

func newCtrlTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// File-backed WAL DB avoids datetime(3) TEXT scan errors in SQLite.
	tmp := t.TempDir()
	dsn := tmp + "/ctrl_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Explicit DDL — plain DATETIME instead of datetime(3) so SQLite can scan.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS user (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			phone          TEXT    NOT NULL DEFAULT '',
			nickname       TEXT    NOT NULL DEFAULT '',
			avatar_url     TEXT    NOT NULL DEFAULT '',
			company_name   TEXT    NOT NULL DEFAULT '',
			parent_user_id INTEGER,
			username       TEXT    NOT NULL DEFAULT '',
			password       TEXT    NOT NULL DEFAULT '',
			is_admin       INTEGER NOT NULL DEFAULT 0,
			status         INTEGER NOT NULL DEFAULT 0,
			total_sop_runs INTEGER NOT NULL DEFAULT 0,
			last_login     DATETIME,
			created_at     DATETIME,
			updated_at     DATETIME,
			deleted_at     DATETIME
		)`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                   INTEGER NOT NULL DEFAULT 0,
			session_id                TEXT    NOT NULL DEFAULT '',
			status                    TEXT    NOT NULL DEFAULT 'running',
			state_reason              TEXT    NOT NULL DEFAULT '',
			terminal_metadata         TEXT,
			messages                  TEXT    NOT NULL DEFAULT '[]',
			reservation_id            INTEGER,
			started_at                DATETIME NOT NULL,
			ended_at                  DATETIME,
			compact_state             TEXT,
			compact_summary           TEXT,
			cancellation_requested_at DATETIME,
			agent_definition_id       INTEGER NOT NULL DEFAULT 0,
			pending_question_json     TEXT,
			pending_question_at       DATETIME,
			pending_external_action_json TEXT,
			pending_external_action_at   DATETIME,
			created_at                DATETIME,
			updated_at                DATETIME,
			-- V1.5 板块 2 task 2.1 — context-management V2 columns
			compact_state_v2          TEXT,
			total_tokens_used_v2      INTEGER NOT NULL DEFAULT 0,
			use_compact_v2            INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2   INTEGER,
			-- 会话管理字段
			is_pinned                 INTEGER NOT NULL DEFAULT 0,
			session_name              TEXT NOT NULL DEFAULT '',
			is_deleted                INTEGER NOT NULL DEFAULT 0,
			is_test                INTEGER NOT NULL DEFAULT 0
		)`).Error)
	require.NoError(t, db.AutoMigrate(
		&model.AgentDefinition{},
		&model.AgentDefinitionHistory{},
		&model.SkillTemplate{},
	))
	return db
}

// setupRouter builds a Gin engine with student query routes registered.
// authUser is injected as "current_user" for every request.
func setupRouter(t *testing.T, db *gorm.DB, authUser *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ds := store.NewTestStore(db)

	skillSvc := skillbiz.NewService(ds)
	querySvc := bizagent.NewStudentQueryService(ds.AgentRuns(), ds.Users())

	r := gin.New()
	auth := r.Group("")
	auth.Use(func(c *gin.Context) {
		c.Set("current_user", authUser)
		c.Next()
	})
	RegisterStudentQueryRoutes(auth, skillSvc, querySvc)
	return r
}

func seedCtrlRun(t *testing.T, db *gorm.DB, userID uint, sessionID string) uint64 {
	t.Helper()
	msgs, _ := json.Marshal([]string{})
	run := &model.AgentRun{
		UserID:    userID,
		SessionID: sessionID,
		Status:    "completed",
		Messages:  msgs,
		StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)
	return run.ID
}

// ---------------------------------------------------------------------------
// GET /agent-sessions/recent
// ---------------------------------------------------------------------------

func TestStudentQueryCtrl_ListRecentSessions_OK(t *testing.T) {
	db := newCtrlTestDB(t)
	user := &model.User{}
	user.ID = 10
	r := setupRouter(t, db, user)

	seedCtrlRun(t, db, 10, "s1")
	seedCtrlRun(t, db, 10, "s2")
	// Unrelated user — must not appear.
	seedCtrlRun(t, db, 99, "other")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agent-sessions/recent?limit=5", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Frontend expects raw array (web-v3 src/api/agent.ts:32 → RecentSession[]).
	list := resp["data"].([]interface{})
	assert.Len(t, list, 2)
}

// ---------------------------------------------------------------------------
// GET /sessions/:id/snapshot — forbidden for another user's run
// ---------------------------------------------------------------------------

func TestStudentQueryCtrl_GetSessionSnapshot_Forbidden(t *testing.T) {
	db := newCtrlTestDB(t)
	// Auth user is 20, but the run belongs to 30.
	user := &model.User{}
	user.ID = 20
	r := setupRouter(t, db, user)

	// Hotfix session-snapshot-uuid-contract: URL takes session_id (string) now,
	// not run.id (uint64). Seed with "snap-sess" and query that same string.
	_ = seedCtrlRun(t, db, 30, "snap-sess")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sessions/snap-sess/snapshot", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestStudentQueryCtrl_GetSessionSnapshot_ReturnsRequestedOlderRunPage(t *testing.T) {
	db := newCtrlTestDB(t)
	user := &model.User{}
	user.ID = 42
	r := setupRouter(t, db, user)

	startedAt := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 101; i++ {
		messages, err := json.Marshal([]map[string]string{{
			"role":    "user",
			"content": fmt.Sprintf("history-run-%03d", i),
		}})
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.AgentRun{
			UserID:    user.ID,
			SessionID: "paged-session",
			Status:    "completed",
			Messages:  messages,
			StartedAt: startedAt.Add(time.Duration(i) * time.Second),
		}).Error)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sessions/paged-session/snapshot?offset=100&limit=100", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Messages   []map[string]interface{} `json:"messages"`
			Offset     int                      `json:"offset"`
			NextOffset int                      `json:"next_offset"`
			HasMore    bool                     `json:"has_more"`
			TotalRuns  int64                    `json:"total_runs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 100, resp.Data.Offset)
	assert.Equal(t, 101, resp.Data.NextOffset)
	assert.False(t, resp.Data.HasMore)
	assert.Equal(t, int64(101), resp.Data.TotalRuns)
	require.Len(t, resp.Data.Messages, 1)
	assert.Equal(t, "history-run-000", resp.Data.Messages[0]["text"])
}

// ---------------------------------------------------------------------------
// GET /tenant-settings/support-contact
// ---------------------------------------------------------------------------

func TestStudentQueryCtrl_GetSupportContact_OK(t *testing.T) {
	db := newCtrlTestDB(t)
	user := &model.User{}
	user.ID = 1
	r := setupRouter(t, db, user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tenant-settings/support-contact", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]interface{})
	assert.Equal(t, "客服", data["name"])
}

// ---------------------------------------------------------------------------
// GET /agent-skills/available — parent returns empty
// ---------------------------------------------------------------------------

func TestStudentQueryCtrl_ListAvailableSkills_ParentEmpty(t *testing.T) {
	db := newCtrlTestDB(t)
	// Seed a parent user (no ParentUserID).
	parent := &model.User{Username: "ctrl-parent"}
	require.NoError(t, db.Create(parent).Error)

	r := setupRouter(t, db, parent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agent-skills/available", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]interface{})
	list := data["list"].([]interface{})
	assert.Empty(t, list)
}

// ---------------------------------------------------------------------------
// GET /agent-runs/:id — forbidden for another user's run
// ---------------------------------------------------------------------------

func TestStudentQueryCtrl_GetRun_Forbidden(t *testing.T) {
	db := newCtrlTestDB(t)
	user := &model.User{}
	user.ID = 50
	r := setupRouter(t, db, user)

	runID := seedCtrlRun(t, db, 60, "run-other")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/agent-runs/%d", runID), nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}
