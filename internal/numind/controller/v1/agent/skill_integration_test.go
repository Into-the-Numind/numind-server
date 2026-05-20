// Package agent_test contains integration tests for the agent skill HTTP handlers.
// Tests use in-memory SQLite + httptest to cover the full controller → biz → store chain.
// Business rule unit tests live in biz/skill; these tests focus on the HTTP contract
// (status codes, response envelope shape, auth enforcement, cross-cutting path parsing).
package agent_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/skill"
	agentctl "numind-server/internal/numind/controller/v1/agent"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test setup helpers
// ---------------------------------------------------------------------------

// newTestDB creates a fresh in-memory SQLite DB with the tables required by
// the skill controller tests.  Each call returns an independent DB so parallel
// tests are fully isolated.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // SQLite in-memory is single-connection
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.AgentDefinition{},
		&model.AgentDefinitionHistory{},
		&model.SkillTemplate{},
	))
	return db
}

// newTestEngine builds a *gin.Engine wired to the full controller→biz→store
// stack backed by the given DB.  The mock auth middleware reads X-Test-UserID
// header and injects a minimal model.User into the gin context so handlers can
// call middleware.GetCurrentUser without a real JWT.
func newTestEngine(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	ds := store.NewTestStore(db)
	svc := skill.NewService(ds)
	ctrl := agentctl.NewSkillController(svc)

	gin.SetMode(gin.TestMode)
	engine := gin.New()

	// Mock auth middleware: injects current_user from X-Test-UserID header.
	engine.Use(func(c *gin.Context) {
		raw := c.GetHeader("X-Test-UserID")
		if raw != "" {
			id64, err := strconv.ParseUint(raw, 10, 64)
			if err == nil && id64 > 0 {
				user := &model.User{}
				user.ID = uint(id64)
				c.Set("current_user", user)
			}
		}
		c.Next()
	})

	agentGroup := engine.Group("/v1/agent")
	{
		skills := agentGroup.Group("/skills")
		{
			skills.POST("", ctrl.Create)
			skills.GET("", ctrl.List)
			skills.GET("/:id", ctrl.Get)
			skills.PATCH("/:id", ctrl.Patch)
			skills.DELETE("/:id", ctrl.Delete)
			skills.GET("/:id/history", ctrl.ListHistory)
			skills.POST("/:id/restore/:version", ctrl.Restore)
			skills.POST("/:id/advanced-toggle", ctrl.AdvancedToggle)
		}
		agentGroup.GET("/skill-templates", ctrl.ListTemplates)
	}
	return engine
}

// seedUsers inserts parent (ID=100) and child (ID=200, ParentUserID=100) users.
// We use raw db.Exec so we don't depend on GORM autoIncrement forcing specific IDs.
// Note: model.User.TableName() returns "user" (not "users").
func seedUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Insert parent user (id=100, parent_user_id=NULL)
	require.NoError(t, db.Exec(`INSERT INTO "user" (id, created_at, updated_at, username, password, status) VALUES (100, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'parent', 'x', 0)`).Error)
	// Insert child user (id=200, parent_user_id=100)
	require.NoError(t, db.Exec(`INSERT INTO "user" (id, created_at, updated_at, username, password, parent_user_id, status) VALUES (200, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'child', 'x', 100, 0)`).Error)
}

// seedTemplate inserts one active SkillTemplate with ID=1.
func seedTemplate(t *testing.T, db *gorm.DB) {
	t.Helper()
	qa, _ := json.Marshal(map[string]interface{}{
		"q6":  []string{"analyze_data"},
		"q7":  []string{"text"},
		"q12": "professional",
	})
	require.NoError(t, db.Exec(
		`INSERT INTO skill_template (id, name, questionnaire_answers, display_order, is_active, created_at, updated_at) VALUES (1, 'Test Tpl', ?, 100, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		string(qa),
	).Error)
}

// apiResponse is the generic envelope returned by core.WriteResponse.
type apiResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// doRequest is a convenience helper that fires a request at the engine and
// decodes the response envelope.
func doRequest(t *testing.T, engine *gin.Engine, method, path string, body interface{}, headers map[string]string) (int, apiResponse) {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	var resp apiResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp), "body: %s", w.Body.String())
	return w.Code, resp
}

// withParent returns headers with X-Test-UserID=100 (parent account).
func withParent() map[string]string { return map[string]string{"X-Test-UserID": "100"} }

// withChild returns headers with X-Test-UserID=200 (child account).
func withChild() map[string]string { return map[string]string{"X-Test-UserID": "200"} }

// withNoAuth returns empty headers (unauthenticated).
func withNoAuth() map[string]string { return map[string]string{} }

// validCreateBody returns a minimal valid CreateRequest JSON payload.
func validCreateBody() map[string]interface{} {
	return map[string]interface{}{
		"name":        "My Agent",
		"description": "A test agent for unit tests",
		"questionnaire_answers": map[string]interface{}{
			"q6":  []string{"analyze_data"},
			"q7":  []string{"text"},
			"q12": "professional",
		},
	}
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

// TestCreate_HappyPath verifies a parent account can create an agent skill.
func TestCreate_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	status, resp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	require.Equal(t, http.StatusOK, status, "body code: %d", resp.Code)
	assert.Equal(t, 0, resp.Code)
	assert.NotEmpty(t, resp.Data)

	// Verify persisted in DB.
	var count int64
	require.NoError(t, db.Model(&model.AgentDefinition{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestCreate_ChildAccount_Forbidden verifies child accounts get 403.
func TestCreate_ChildAccount_Forbidden(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	status, resp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withChild())
	assert.Equal(t, http.StatusForbidden, status)
	assert.NotEqual(t, 0, resp.Code)
}

// TestCreate_Unauthenticated_Unauthorized verifies unauthenticated requests get 401.
func TestCreate_Unauthenticated_Unauthorized(t *testing.T) {
	db := newTestDB(t)
	engine := newTestEngine(t, db)

	status, resp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withNoAuth())
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.NotEqual(t, 0, resp.Code)
}

// TestCreate_MissingRequiredField returns 400 when "name" is absent.
func TestCreate_MissingRequiredField(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	body := map[string]interface{}{
		// name is missing — binding:"required" should reject this
		"description": "no name",
	}
	status, resp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", body, withParent())
	assert.Equal(t, http.StatusBadRequest, status)
	assert.NotEqual(t, 0, resp.Code)
}

// TestCreate_MissingQuestionnaire_422 verifies that a missing q6/q7/q12 returns 422.
func TestCreate_MissingQuestionnaire_422(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	body := map[string]interface{}{
		"name":                  "Missing QA",
		"questionnaire_answers": map[string]interface{}{}, // all empty
	}
	status, resp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", body, withParent())
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.NotEqual(t, 0, resp.Code)
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

// TestList_HappyPath verifies list returns created agents.
func TestList_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	// Create two agents.
	doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())

	status, resp := doRequest(t, engine, http.MethodGet, "/v1/agent/skills", nil, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var data struct {
		List  []json.RawMessage `json:"list"`
		Total int               `json:"total"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Equal(t, 2, data.Total)
	assert.Len(t, data.List, 2)
}

// TestList_Unauthenticated returns 401.
func TestList_Unauthenticated(t *testing.T) {
	db := newTestDB(t)
	engine := newTestEngine(t, db)

	status, _ := doRequest(t, engine, http.MethodGet, "/v1/agent/skills", nil, withNoAuth())
	assert.Equal(t, http.StatusUnauthorized, status)
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

// TestGet_HappyPath creates an agent and retrieves it by ID.
func TestGet_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))
	require.NotZero(t, created.ID)

	status, resp := doRequest(t, engine, http.MethodGet, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10), nil, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var got model.AgentDefinition
	require.NoError(t, json.Unmarshal(resp.Data, &got))
	assert.Equal(t, created.ID, got.ID)
}

// TestGet_NotFound returns 404 for a non-existent ID.
func TestGet_NotFound(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	status, _ := doRequest(t, engine, http.MethodGet, "/v1/agent/skills/99999", nil, withParent())
	assert.Equal(t, http.StatusNotFound, status)
}

// TestGet_CrossAccountForbidden verifies user 100 cannot see user 200's agent.
func TestGet_CrossAccountForbidden(t *testing.T) {
	db := newTestDB(t)
	// Use a second parent account with id=300 for isolation.
	// Note: model.User.TableName() returns "user" (not "users").
	require.NoError(t, db.Exec(`INSERT INTO "user" (id, created_at, updated_at, username, password, status) VALUES (100, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'parent1', 'x', 0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO "user" (id, created_at, updated_at, username, password, status) VALUES (300, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'parent2', 'x', 0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO "user" (id, created_at, updated_at, username, password, parent_user_id, status) VALUES (200, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'child', 'x', 100, 0)`).Error)
	engine := newTestEngine(t, db)

	// parent1 (100) creates an agent
	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))

	// parent2 (300) tries to get it — should get 404 (ownership hidden)
	status, _ := doRequest(t, engine, http.MethodGet, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10), nil, map[string]string{"X-Test-UserID": "300"})
	assert.Equal(t, http.StatusNotFound, status)
}

// ---------------------------------------------------------------------------
// Patch tests
// ---------------------------------------------------------------------------

// TestPatch_HappyPath updates name and verifies the response reflects the change.
func TestPatch_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))

	newName := "Updated Agent"
	patch := map[string]interface{}{"name": newName}
	status, resp := doRequest(t, engine, http.MethodPatch, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10), patch, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var updated model.AgentDefinition
	require.NoError(t, json.Unmarshal(resp.Data, &updated))
	assert.Equal(t, newName, updated.Name)
	assert.Equal(t, created.Version+1, updated.Version)
}

// TestPatch_NotFound returns 404 for unknown skill ID.
func TestPatch_NotFound(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	status, _ := doRequest(t, engine, http.MethodPatch, "/v1/agent/skills/99999", map[string]interface{}{"name": "x"}, withParent())
	assert.Equal(t, http.StatusNotFound, status)
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

// TestDelete_HappyPath soft-deletes an agent and verifies it is excluded from list.
func TestDelete_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))

	status, resp := doRequest(t, engine, http.MethodDelete, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10), nil, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	// List should now return 0 active skills.
	_, listResp := doRequest(t, engine, http.MethodGet, "/v1/agent/skills", nil, withParent())
	var listData struct {
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(listResp.Data, &listData))
	assert.Equal(t, 0, listData.Total)
}

// TestDelete_Idempotent verifies double-delete returns 200 (idempotent).
func TestDelete_Idempotent(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))
	path := "/v1/agent/skills/" + strconv.FormatUint(created.ID, 10)

	status1, _ := doRequest(t, engine, http.MethodDelete, path, nil, withParent())
	status2, _ := doRequest(t, engine, http.MethodDelete, path, nil, withParent())
	assert.Equal(t, http.StatusOK, status1)
	assert.Equal(t, http.StatusOK, status2)
}

// ---------------------------------------------------------------------------
// ListHistory tests
// ---------------------------------------------------------------------------

// TestListHistory_HappyPath verifies history accumulates across operations.
func TestListHistory_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	// Create → generates version 1 history.
	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))

	// Patch → generates version 2 history.
	doRequest(t, engine, http.MethodPatch, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10), map[string]interface{}{"name": "v2"}, withParent())

	status, resp := doRequest(t, engine, http.MethodGet, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10)+"/history", nil, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var histData struct {
		List  []json.RawMessage `json:"list"`
		Total int               `json:"total"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &histData))
	assert.Equal(t, 2, histData.Total)
}

// TestListHistory_AfterSoftDelete verifies history is accessible for deleted skills.
func TestListHistory_AfterSoftDelete(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))
	path := "/v1/agent/skills/" + strconv.FormatUint(created.ID, 10)

	doRequest(t, engine, http.MethodDelete, path, nil, withParent())

	// History should still be accessible.
	status, resp := doRequest(t, engine, http.MethodGet, path+"/history", nil, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var histData struct {
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &histData))
	assert.GreaterOrEqual(t, histData.Total, 1)
}

// ---------------------------------------------------------------------------
// Restore tests
// ---------------------------------------------------------------------------

// TestRestore_HappyPath creates v1, patches to v2, restores v1 → get v3.
func TestRestore_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))
	idPath := "/v1/agent/skills/" + strconv.FormatUint(created.ID, 10)

	// Patch → v2
	doRequest(t, engine, http.MethodPatch, idPath, map[string]interface{}{"name": "v2 name"}, withParent())

	// Restore v1
	status, resp := doRequest(t, engine, http.MethodPost, idPath+"/restore/1", nil, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var restored model.AgentDefinition
	require.NoError(t, json.Unmarshal(resp.Data, &restored))
	assert.Equal(t, uint(3), restored.Version, "restore bumps version to max+1=3")
}

// TestRestore_VersionNotFound returns 404 for a non-existent version.
func TestRestore_VersionNotFound(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))

	status, _ := doRequest(t, engine, http.MethodPost, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10)+"/restore/999", nil, withParent())
	assert.Equal(t, http.StatusNotFound, status)
}

// ---------------------------------------------------------------------------
// AdvancedToggle tests
// ---------------------------------------------------------------------------

// TestAdvancedToggle_HappyPath verifies toggling advanced mode succeeds once.
func TestAdvancedToggle_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))
	assert.False(t, created.AdvancedMode)

	status, resp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills/"+strconv.FormatUint(created.ID, 10)+"/advanced-toggle", nil, withParent())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var toggled model.AgentDefinition
	require.NoError(t, json.Unmarshal(resp.Data, &toggled))
	assert.True(t, toggled.AdvancedMode)
}

// TestAdvancedToggle_AlreadyAdvanced_422 verifies second toggle returns 422.
func TestAdvancedToggle_AlreadyAdvanced_422(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	engine := newTestEngine(t, db)

	_, createResp := doRequest(t, engine, http.MethodPost, "/v1/agent/skills", validCreateBody(), withParent())
	var created model.AgentDefinition
	require.NoError(t, json.Unmarshal(createResp.Data, &created))
	idPath := "/v1/agent/skills/" + strconv.FormatUint(created.ID, 10)

	// First toggle
	doRequest(t, engine, http.MethodPost, idPath+"/advanced-toggle", nil, withParent())

	// Second toggle should fail
	status, _ := doRequest(t, engine, http.MethodPost, idPath+"/advanced-toggle", nil, withParent())
	assert.Equal(t, http.StatusUnprocessableEntity, status)
}

// ---------------------------------------------------------------------------
// ListTemplates tests
// ---------------------------------------------------------------------------

// TestListTemplates_HappyPath verifies templates are returned (no auth required for business, but controller uses user).
func TestListTemplates_HappyPath(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db)
	seedTemplate(t, db)
	engine := newTestEngine(t, db)

	// ListTemplates does not require auth (no GetCurrentUser call).
	status, resp := doRequest(t, engine, http.MethodGet, "/v1/agent/skill-templates", nil, withNoAuth())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var data struct {
		List  []json.RawMessage `json:"list"`
		Total int               `json:"total"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Equal(t, 1, data.Total)
	assert.Len(t, data.List, 1)
}

// TestListTemplates_Empty verifies empty list returns total=0.
func TestListTemplates_Empty(t *testing.T) {
	db := newTestDB(t)
	engine := newTestEngine(t, db)

	status, resp := doRequest(t, engine, http.MethodGet, "/v1/agent/skill-templates", nil, withNoAuth())
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var data struct {
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Equal(t, 0, data.Total)
}
