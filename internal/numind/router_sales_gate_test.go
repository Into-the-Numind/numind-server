// Package numind_test contains route-level integration tests that validate
// salesGroup.Use(FeaturePermission(FeatureKeySalesAgent)) actually gates
// run endpoints while leaving /sales-rag/check-permission open.
// This closes the S2 Reviewer Q8 gap (middleware mounting never had Go
// verification — only E2E coverage).
package numind_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// -----------------------------------------------------------------------------
// test harness
// -----------------------------------------------------------------------------

// newGateTestDB creates an in-memory SQLite DB with the schema required by
// FeaturePermission middleware: the `user` table (for ParentUserID lookup)
// and `user_feature_permission` (for grant records).
//
// We hand-roll the user table because AutoMigrate on model.User drags in
// MySQL ENUM types that SQLite rejects.
func newGateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Minimal user table (only the columns the middleware reads).
	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id INTEGER NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`).Error)

	require.NoError(t, db.AutoMigrate(&model.UserFeaturePermission{}))
	return db
}

// seedUser inserts a user with the given parentUserID (nil = parent account).
func seedUser(t *testing.T, db *gorm.DB, id uint, parentID *uint) {
	t.Helper()
	var pid interface{}
	if parentID != nil {
		pid = *parentID
	}
	require.NoError(t, db.Exec(`INSERT INTO user (id, parent_user_id) VALUES (?, ?)`, id, pid).Error)
}

// seedGrant inserts a user_feature_permission row for (sub, parent, featureKey).
func seedGrant(t *testing.T, db *gorm.DB, parent, sub uint, key string) {
	t.Helper()
	grant := &model.UserFeaturePermission{
		ParentUserID: parent,
		SubUserID:    sub,
		FeatureKey:   key,
	}
	require.NoError(t, db.Create(grant).Error)
}

// installStoreS injects a test-backed store.S singleton and returns a restorer.
// Uses NewTestDataStore (added in Task 0) because store.S is typed *datastore
// and NewTestStore returns IStore interface.
func installStoreS(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := store.S
	store.S = store.NewTestDataStore(db)
	t.Cleanup(func() { store.S = previous })
}

// setCurrentUserMW mounts a fabricated *model.User onto the gin context at
// key "current_user" (the key middleware.GetCurrentUser reads).
func setCurrentUserMW(user *model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if user != nil {
			c.Set("current_user", user)
		}
		c.Next()
	}
}

// newMiniRouter builds a minimal gin router that mirrors the sales route
// topology under test: authGroup has check-permission OUTSIDE salesGroup,
// and salesGroup carries the FeaturePermission middleware.
func newMiniRouter(t *testing.T, user *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	authGroup := r.Group("/v1")
	authGroup.Use(setCurrentUserMW(user))

	// check-permission: NOT behind FeaturePermission gate (D1)
	authGroup.GET("/sales-rag/check-permission", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"has_permission": true}})
	})

	// salesGroup: behind FeaturePermission gate (C1 under test)
	salesGroup := authGroup.Group("/sales-rag")
	salesGroup.Use(middleware.FeaturePermission(model.FeatureKeySalesAgent))
	{
		// 3 representative run endpoints from §4 (docs / chat / ocr)
		salesGroup.GET("/documents", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
		salesGroup.POST("/sessions/:id/chat", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
		salesGroup.POST("/ocr", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
	}
	return r
}

func mustUser(id uint, parentID *uint) *model.User {
	u := &model.User{ParentUserID: parentID}
	u.ID = id
	return u
}

// -----------------------------------------------------------------------------
// tests (H1-H6)
// -----------------------------------------------------------------------------

// TestGate_SubNoGrant_DocumentsListBlocked validates that a sub-user without
// a grant is blocked by the FeaturePermission gate on GET /sales-rag/documents.
func TestGate_SubNoGrant_DocumentsListBlocked(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)         // parent
	seedUser(t, db, 100, &parentID) // sub, no grant

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/documents", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// ErrForbidden maps to HTTP 403 (not 200); both the HTTP status and the
	// non-zero business code confirm the gate fired.
	require.Equal(t, http.StatusForbidden, w.Code, "gate must return HTTP 403; body: %s", w.Body.String())
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code, "gate must reject with non-zero biz code")
	// Do NOT assert on message text ("未开通 …") — brittle if copy changes. The
	// non-zero business code is the load-bearing signal.
}

// TestGate_SubNoGrant_ChatBlocked validates that a sub-user without a grant
// is blocked on POST /sales-rag/sessions/1/chat.
func TestGate_SubNoGrant_ChatBlocked(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodPost, "/v1/sales-rag/sessions/1/chat", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code)
}

// TestGate_SubNoGrant_OCRBlocked validates that a sub-user without a grant
// is blocked on POST /sales-rag/ocr.
func TestGate_SubNoGrant_OCRBlocked(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodPost, "/v1/sales-rag/ocr", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code)
}

// TestGate_SubNoGrant_CheckPermissionNotGated validates D1: /check-permission
// must NOT be behind the FeaturePermission gate, so a denied sub-user still
// gets HTTP 200 code:0 from that endpoint.
func TestGate_SubNoGrant_CheckPermissionNotGated(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "D1: check-permission must NOT be behind gate")
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "check-permission handler runs, business code=0")
}

// TestGate_Parent_PassesThrough validates that a parent account (ParentUserID=nil)
// is automatically let through the FeaturePermission gate.
func TestGate_Parent_PassesThrough(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	seedUser(t, db, 1, nil) // parent

	r := newMiniRouter(t, mustUser(1, nil))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/documents", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "parent passes gate, endpoint runs")
}

// TestGate_SubGranted_PassesThrough validates that a sub-user WITH a grant
// is let through the FeaturePermission gate.
func TestGate_SubGranted_PassesThrough(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedGrant(t, db, 1, 100, model.FeatureKeySalesAgent)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/documents", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "granted sub passes gate")
}
