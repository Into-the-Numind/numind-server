// Package numind_test contains route-level integration tests that validate
// the salesrag route topology:
//   - /sales-rag/check-permission is publicly accessible (no gate)
//   - salesDocGroup (knowledge-base CRUD: /ingest, /documents/*) has NO
//     FeaturePermission gate — all authenticated users may manage their
//     own documents (data isolation handled by biz/store layer via user_id)
//   - salesChatGroup (sessions, messages, chat, ocr, analyze-*) IS gated by
//     FeaturePermission(FeatureKeySalesAgent) — only sales-agent users can
//     run the actual sales-rag conversation features
//
// Originally added to close the S2 Reviewer Q8 gap (middleware mounting
// had no Go verification — only E2E coverage). Updated for feature
// salesrag-kb-public (2026-05-21) to reflect the doc/chat group split.
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

	customerbiz "numind-server/internal/numind/biz/customer"
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
	// sop-salesrag-parent-scope Task 3: 销售智能体 owner tag 表必须存在,
	// 即使父账户也需要 Layer 0 检查 (spec D2 移除父账户硬 bypass)
	require.NoError(t, db.AutoMigrate(&model.SalesAgentOwner{}))
	return db
}

// seedSalesAgentOwner 把父账户加入销售智能体 owner 表
// (sop-salesrag-parent-scope Task 3). spec D2: 父账户不再硬 bypass,
// 必须显式存在于 sales_agent_owner 表才能访问销售智能体.
func seedSalesAgentOwner(t *testing.T, db *gorm.DB, parentID uint) {
	t.Helper()
	require.NoError(t, db.Create(&model.SalesAgentOwner{ParentUserID: parentID}).Error)
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

// installCheckFeaturePermissionFunc 注入 middleware.CheckFeaturePermissionFunc
// (sop-salesrag-parent-scope Task 3): 镜像 numind.go run() 中的 wiring,
// 因为测试不走 run() 启动序列, 必须显式注入函数指针, 否则 middleware nil
// guard 会触发 500.
func installCheckFeaturePermissionFunc(t *testing.T) {
	t.Helper()
	previous := middleware.CheckFeaturePermissionFunc
	cb := customerbiz.New(store.S)
	middleware.CheckFeaturePermissionFunc = cb.CheckFeaturePermission
	t.Cleanup(func() { middleware.CheckFeaturePermissionFunc = previous })
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
// topology under test:
//   - authGroup has check-permission OUTSIDE all salesGroups (no gate)
//   - salesDocGroup (documents CRUD) has NO FeaturePermission gate
//   - salesChatGroup (chat / ocr) carries FeaturePermission gate
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

	// salesDocGroup: knowledge-base CRUD, NO gate (feature: salesrag-kb-public)
	salesDocGroup := authGroup.Group("/sales-rag")
	{
		salesDocGroup.GET("/documents", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
	}

	// salesChatGroup: behind FeaturePermission gate (C1 under test)
	salesChatGroup := authGroup.Group("/sales-rag")
	salesChatGroup.Use(middleware.FeaturePermission(model.FeatureKeySalesAgent))
	{
		salesChatGroup.POST("/sessions/:id/chat", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
		salesChatGroup.POST("/ocr", func(c *gin.Context) {
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

// TestGate_SubNoGrant_DocumentsListAllowed validates that a sub-user without
// a sales_agent grant CAN access GET /sales-rag/documents — the knowledge-base
// CRUD endpoints were moved off the FeaturePermission gate by feature
// salesrag-kb-public (2026-05-21). Data isolation is enforced at the biz/store
// layer via user_id, not at the route layer.
func TestGate_SubNoGrant_DocumentsListAllowed(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	installCheckFeaturePermissionFunc(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)         // parent
	seedUser(t, db, 100, &parentID) // sub, no grant
	seedSalesAgentOwner(t, db, 1)   // parent owner tag is irrelevant for docs

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/documents", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "documents endpoint is ungated; body: %s", w.Body.String())
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "ungated handler runs, business code=0")
}

// TestGate_SubNoGrant_ChatBlocked validates that a sub-user without a grant
// is blocked on POST /sales-rag/sessions/1/chat.
func TestGate_SubNoGrant_ChatBlocked(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	installCheckFeaturePermissionFunc(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedSalesAgentOwner(t, db, 1)

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
	installCheckFeaturePermissionFunc(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedSalesAgentOwner(t, db, 1)

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
	installCheckFeaturePermissionFunc(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedSalesAgentOwner(t, db, 1)

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

// TestGate_Parent_PassesThrough validates that a parent account with owner tag
// passes through the FeaturePermission gate (sop-salesrag-parent-scope Task 3:
// spec D2 — 父账户必须在 sales_agent_owner 表中, 不再硬 bypass). Uses the chat
// endpoint because /documents was moved off the gate by salesrag-kb-public.
func TestGate_Parent_PassesThrough(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	installCheckFeaturePermissionFunc(t)
	seedUser(t, db, 1, nil)       // parent
	seedSalesAgentOwner(t, db, 1) // parent owner tag (Layer 0 必查)

	r := newMiniRouter(t, mustUser(1, nil))
	req := httptest.NewRequest(http.MethodPost, "/v1/sales-rag/sessions/1/chat", nil)
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
// is let through the FeaturePermission gate. Uses the chat endpoint because
// /documents was moved off the gate by salesrag-kb-public.
func TestGate_SubGranted_PassesThrough(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	installCheckFeaturePermissionFunc(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedSalesAgentOwner(t, db, 1)
	seedGrant(t, db, 1, 100, model.FeatureKeySalesAgent)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodPost, "/v1/sales-rag/sessions/1/chat", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "granted sub passes gate")
}
