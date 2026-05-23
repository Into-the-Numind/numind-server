// Package agent_test — HTTP-level tests for SkillArtifactController.
//
// 用 in-memory SQLite + httptest 覆盖完整 controller → biz → store 链路。
// 业务规则单测在 biz/skill/artifact/*_test.go；这里聚焦 HTTP 契约：
//   - 200 happy path（create/list/detail）
//   - 400 binding 缺字段
//   - 403 子账户 / 401 未登录
//   - 404 跨租户 detail
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

	"numind-server/internal/numind/biz/skill/artifact"
	agentctl "numind-server/internal/numind/controller/v1/agent"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test setup helpers
// ---------------------------------------------------------------------------

// newArtifactTestDB 创建 in-memory SQLite + 退化版 skill / skill_history /
// agent_skill_binding / agent_definition / user 表。
//
// 不用 GORM AutoMigrate：source_type 是 MySQL ENUM、allowed_tools 默认 `JSON_ARRAY()`，
// SQLite 不支持，AutoMigrate 会把 DDL 原样写过去失败。
// 参考 biz/skill/artifact/testhelper_test.go 的同套 DDL（保持一致避免漂移）。
func newArtifactTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// model.User 走 GORM AutoMigrate（字段较多，DDL 列全列繁琐且易漂移）。
	// 其他 4 张表用 raw DDL（含 MySQL ENUM 退化），与 biz/skill/artifact/testhelper_test.go 一致。
	require.NoError(t, db.AutoMigrate(&model.User{}))

	ddls := []string{
		`CREATE TABLE skill (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id      INTEGER NOT NULL,
			name                TEXT    NOT NULL,
			description         TEXT    NOT NULL DEFAULT '',
			when_to_use         TEXT    NOT NULL DEFAULT '',
			allowed_tools       TEXT    NOT NULL DEFAULT '[]',
			body_md             TEXT    NOT NULL DEFAULT '',
			source_type         TEXT    NOT NULL DEFAULT 'custom',
			source_template_id  INTEGER,
			version             INTEGER NOT NULL DEFAULT 1,
			is_active           INTEGER NOT NULL DEFAULT 1,
			created_by          INTEGER NOT NULL,
			created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE skill_history (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			skill_id    INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			snapshot    TEXT    NOT NULL,
			created_by  INTEGER NOT NULL,
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE UNIQUE INDEX uk_skill_version ON skill_history (skill_id, version)`,
		`CREATE TABLE agent_skill_binding (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id    INTEGER NOT NULL,
			skill_id    INTEGER NOT NULL,
			sort_order  INTEGER NOT NULL DEFAULT 0,
			is_active   INTEGER NOT NULL DEFAULT 1,
			bound_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			unbound_at  DATETIME
		)`,
		`CREATE UNIQUE INDEX uk_agent_skill ON agent_skill_binding (agent_id, skill_id)`,
		`CREATE TABLE agent_definition (
			id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id         INTEGER NOT NULL,
			name                   TEXT    NOT NULL,
			description            TEXT,
			icon_url               TEXT,
			welcome_message        TEXT,
			starters               TEXT,
			questionnaire_answers  TEXT,
			generated_skill_body   TEXT,
			advanced_mode          INTEGER NOT NULL DEFAULT 0,
			custom_skill_body      TEXT,
			tool_flags             TEXT,
			credit_cap_per_session INTEGER,
			daily_credit_cap       INTEGER,
			version                INTEGER NOT NULL DEFAULT 1,
			is_active              INTEGER NOT NULL DEFAULT 1,
			source_template_id     INTEGER,
			created_by             INTEGER NOT NULL,
			created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, ddl := range ddls {
		require.NoError(t, db.Exec(ddl).Error)
	}
	return db
}

// newArtifactTestEngine 装配 gin engine：mock auth middleware（X-Test-UserID 头）
// + SkillArtifactController + 全 11 路由（保持和 router.go 同序）。
func newArtifactTestEngine(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()

	skillSvc := artifact.NewService(db)
	bindingSvc := artifact.NewBindingService(db)
	ctrl := agentctl.NewSkillArtifactController(skillSvc, bindingSvc)

	gin.SetMode(gin.TestMode)
	engine := gin.New()

	// Mock auth middleware：X-Test-UserID 头解析为 *model.User 注入 current_user。
	// 不存在 X-Test-UserID = 未登录路径（controller 应 401）。
	engine.Use(func(c *gin.Context) {
		raw := c.GetHeader("X-Test-UserID")
		if raw == "" {
			c.Next()
			return
		}
		id64, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id64 == 0 {
			c.Next()
			return
		}
		var u model.User
		// 从 DB 读真实 user 行（含 parent_user_id），保证子账户场景能识别。
		if err := db.First(&u, uint(id64)).Error; err == nil {
			c.Set("current_user", &u)
		}
		c.Next()
	})

	skills := engine.Group("/v1/skills")
	{
		skills.POST("", ctrl.CreateSkill)
		skills.GET("", ctrl.ListSkills)
		skills.GET("/:id", ctrl.GetSkill)
		skills.PUT("/:id", ctrl.UpdateSkill)
		skills.DELETE("/:id", ctrl.DeleteSkill)
		skills.GET("/:id/history", ctrl.ListSkillHistory)
		skills.POST("/:id/restore/:version", ctrl.RestoreSkill)
		skills.GET("/:id/agents", ctrl.ListSkillBoundAgents)
	}
	agents := engine.Group("/v1/agents/:id/skills")
	{
		agents.POST("", ctrl.AttachSkill)
		agents.DELETE("/:skill_id", ctrl.DetachSkill)
		agents.PUT("/reorder", ctrl.ReorderSkills)
	}
	return engine
}

// seedArtifactUsers 插入 parent(100) + child(200) + parent(300) 三个 user，
// 用于跨租户测试（300 是另一父账户，访问 100 的 skill 应 404）。
func seedArtifactUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO "user" (id, username, password, status) VALUES (100, 'parent', 'x', 0)`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO "user" (id, username, password, parent_user_id, status) VALUES (200, 'child', 'x', 100, 0)`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO "user" (id, username, password, status) VALUES (300, 'other-parent', 'x', 0)`).Error)
}

// (apiResponse 类型在同包 skill_integration_test.go 已定义，直接复用)

func doArtifactRequest(t *testing.T, engine *gin.Engine, method, path string, body interface{}, headers map[string]string) (int, apiResponse) {
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
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp), "body=%s", w.Body.String())
	return w.Code, resp
}

func withUser(userID uint) map[string]string {
	return map[string]string{"X-Test-UserID": strconv.FormatUint(uint64(userID), 10)}
}

// minimalCreateBody 是 POST /v1/skills 的最小合法 body。
func minimalCreateBody() map[string]interface{} {
	return map[string]interface{}{
		"name":    "Test Skill",
		"body_md": "# Test\nSome body.",
	}
}

// ---------------------------------------------------------------------------
// 200 Happy paths
// ---------------------------------------------------------------------------

// TestSkillArtifact_Create_HappyPath：parent 创建 skill 返回 200 + DB 有行。
func TestSkillArtifact_Create_HappyPath(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	status, resp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(100))
	require.Equal(t, http.StatusOK, status, "body=%+v", resp)
	assert.Equal(t, 0, resp.Code)

	// Verify DB row created
	var count int64
	require.NoError(t, db.Model(&model.Skill{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestSkillArtifact_List_HappyPath：创建 2 个 skill 后 list 返回 total=2。
func TestSkillArtifact_List_HappyPath(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(100))
	doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(100))

	status, resp := doArtifactRequest(t, engine, http.MethodGet, "/v1/skills?page=1&page_size=10", nil, withUser(100))
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

// TestSkillArtifact_GetDetail_HappyPath：详情含 skill + bound_agents 两字段。
func TestSkillArtifact_GetDetail_HappyPath(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	createStatus, createResp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(100))
	require.Equal(t, http.StatusOK, createStatus)
	var created model.Skill
	require.NoError(t, json.Unmarshal(createResp.Data, &created))
	require.NotZero(t, created.ID)

	path := "/v1/skills/" + strconv.FormatUint(uint64(created.ID), 10)
	status, resp := doArtifactRequest(t, engine, http.MethodGet, path, nil, withUser(100))
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var data struct {
		Skill       model.Skill       `json:"skill"`
		BoundAgents []json.RawMessage `json:"bound_agents"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Equal(t, created.ID, data.Skill.ID)
	assert.NotNil(t, data.BoundAgents) // 即使 0 个 agent 也是 []，不是 null
}

// ---------------------------------------------------------------------------
// 400 binding errors
// ---------------------------------------------------------------------------

// TestSkillArtifact_Create_MissingName_400：name 缺失返回 400。
func TestSkillArtifact_Create_MissingName_400(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	body := map[string]interface{}{
		// name 缺失
		"body_md": "no name",
	}
	status, resp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", body, withUser(100))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.NotEqual(t, 0, resp.Code)
}

// TestSkillArtifact_Create_MissingBodyMd_400：body_md 缺失返回 400。
func TestSkillArtifact_Create_MissingBodyMd_400(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	body := map[string]interface{}{
		"name": "Only name",
		// body_md 缺失
	}
	status, resp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", body, withUser(100))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.NotEqual(t, 0, resp.Code)
}

// ---------------------------------------------------------------------------
// 403 sub-account / 401 unauthenticated
// ---------------------------------------------------------------------------

// TestSkillArtifact_Create_ChildAccount_403：子账户访问返回 403。
func TestSkillArtifact_Create_ChildAccount_403(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	status, resp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(200))
	assert.Equal(t, http.StatusForbidden, status)
	assert.NotEqual(t, 0, resp.Code)
}

// TestSkillArtifact_List_Unauthenticated_401：未登录返回 401。
func TestSkillArtifact_List_Unauthenticated_401(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	status, resp := doArtifactRequest(t, engine, http.MethodGet, "/v1/skills", nil, map[string]string{})
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.NotEqual(t, 0, resp.Code)
}

// ---------------------------------------------------------------------------
// 404 cross-tenant
// ---------------------------------------------------------------------------

// TestSkillArtifact_GetDetail_CrossTenant_404：parent=300 访问 parent=100 的 skill 返回 404。
func TestSkillArtifact_GetDetail_CrossTenant_404(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	// parent=100 创建一个 skill
	createStatus, createResp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(100))
	require.Equal(t, http.StatusOK, createStatus)
	var created model.Skill
	require.NoError(t, json.Unmarshal(createResp.Data, &created))

	// parent=300 尝试访问 → 404
	path := "/v1/skills/" + strconv.FormatUint(uint64(created.ID), 10)
	status, resp := doArtifactRequest(t, engine, http.MethodGet, path, nil, withUser(300))
	assert.Equal(t, http.StatusNotFound, status)
	assert.NotEqual(t, 0, resp.Code)
}

// TestSkillArtifact_Delete_HappyPath_ReturnsAffectedBindings：删除返回 affected_bindings。
func TestSkillArtifact_Delete_HappyPath_ReturnsAffectedBindings(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	createStatus, createResp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(100))
	require.Equal(t, http.StatusOK, createStatus)
	var created model.Skill
	require.NoError(t, json.Unmarshal(createResp.Data, &created))

	path := "/v1/skills/" + strconv.FormatUint(uint64(created.ID), 10)
	status, resp := doArtifactRequest(t, engine, http.MethodDelete, path, nil, withUser(100))
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	var data struct {
		AffectedBindings int64 `json:"affected_bindings"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Equal(t, int64(0), data.AffectedBindings) // 没装载过任何 agent
}
