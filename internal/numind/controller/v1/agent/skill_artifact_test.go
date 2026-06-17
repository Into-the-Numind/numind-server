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
			owner_user_id       INTEGER NOT NULL DEFAULT 0,
			visibility          TEXT    NOT NULL DEFAULT 'institution',
			name                TEXT    NOT NULL,
			description         TEXT    NOT NULL DEFAULT '',
			when_to_use         TEXT    NOT NULL DEFAULT '',
			allowed_tools       TEXT    NOT NULL DEFAULT '[]',
			body_md             TEXT    NOT NULL DEFAULT '',
			source_type         TEXT    NOT NULL DEFAULT 'custom',
			source_template_id  INTEGER,
			origin_type         TEXT    NOT NULL DEFAULT 'user',
			version             INTEGER NOT NULL DEFAULT 1,
			is_active           INTEGER NOT NULL DEFAULT 1,
			subscription_id     INTEGER NOT NULL DEFAULT 0,
			marketplace_id      INTEGER NOT NULL DEFAULT 0,
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
			system_prompt          TEXT,
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
		`CREATE TABLE skill_template (
			id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			name                   TEXT    NOT NULL,
			description            TEXT,
			icon_url               TEXT,
			category_tags          TEXT,
			questionnaire_answers  TEXT    NOT NULL,
			default_tool_flags     TEXT,
			display_order          INTEGER NOT NULL DEFAULT 100,
			is_active              INTEGER NOT NULL DEFAULT 1,
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
		skills.POST("/import-template", ctrl.ImportTemplate)
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
		agents.GET("", ctrl.ListAgentSkills)
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

// TestSkillArtifact_Create_ChildAccount_CreatesSubUserSkill (T4): 子账户不再被 403 拦截，
// 而是成功创建一条 visibility='sub_user' 的私有技能（owner=子账户 200，parent_user_id=机构 100）。
func TestSkillArtifact_Create_ChildAccount_CreatesSubUserSkill(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	status, resp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(200))
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, 0, resp.Code)

	// 落库校验：sub_user 私有技能。
	var row model.Skill
	require.NoError(t, db.Where("name = ?", "Test Skill").First(&row).Error)
	assert.Equal(t, "sub_user", row.Visibility, "child create defaults to sub_user")
	assert.Equal(t, uint(200), row.OwnerUserID, "owner = child user id")
	assert.Equal(t, uint(100), row.ParentUserID, "parent_user_id = institution (child's parent)")
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

// TestSkillArtifact_ListAgentSkills_HappyPath 验证 GET /v1/agents/:id/skills 正确获取当前 agent 所装载的活跃 skills 列表。
func TestSkillArtifact_ListAgentSkills_HappyPath(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	// 1. 创建 2 个 skills
	createStatus1, createResp1 := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", minimalCreateBody(), withUser(100))
	require.Equal(t, http.StatusOK, createStatus1)
	var sk1 model.Skill
	require.NoError(t, json.Unmarshal(createResp1.Data, &sk1))

	// Distinct name: binding two same-named skills to one agent is now rejected
	// (Attach dup-name guard — same-named bindings brick the run at name resolution).
	createBody2 := map[string]interface{}{"name": "Test Skill 2", "body_md": "# Test 2\nSome body."}
	createStatus2, createResp2 := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills", createBody2, withUser(100))
	require.Equal(t, http.StatusOK, createStatus2)
	var sk2 model.Skill
	require.NoError(t, json.Unmarshal(createResp2.Data, &sk2))

	// 2. 插入一条 agent 数据
	require.NoError(t, db.Exec(
		`INSERT INTO agent_definition (id, parent_user_id, name, created_by) VALUES (42, 100, 'My Agent', 100)`).Error)

	// 3. 装载这两个 skills 到 agent 42
	statusAttach1, respAttach1 := doArtifactRequest(t, engine, http.MethodPost, "/v1/agents/42/skills", map[string]interface{}{"skill_id": sk1.ID, "sort_order": 1}, withUser(100))
	require.Equal(t, http.StatusOK, statusAttach1, "attach1 err: %+v", respAttach1)
	statusAttach2, respAttach2 := doArtifactRequest(t, engine, http.MethodPost, "/v1/agents/42/skills", map[string]interface{}{"skill_id": sk2.ID, "sort_order": 2}, withUser(100))
	require.Equal(t, http.StatusOK, statusAttach2, "attach2 err: %+v", respAttach2)

	// 4. 获取列表，并验证排序和数量
	statusList, respList := doArtifactRequest(t, engine, http.MethodGet, "/v1/agents/42/skills", nil, withUser(100))
	require.Equal(t, http.StatusOK, statusList)
	assert.Equal(t, 0, respList.Code)

	var listData struct {
		List  []model.Skill `json:"list"`
		Total int           `json:"total"`
	}
	require.NoError(t, json.Unmarshal(respList.Data, &listData))
	assert.Equal(t, 2, listData.Total)
	require.Len(t, listData.List, 2)
	assert.Equal(t, sk1.ID, listData.List[0].ID)
	assert.Equal(t, sk2.ID, listData.List[1].ID)
}

// TestSkillArtifact_ImportTemplate_HappyPath 验证一键从官方模板克隆/导入为本租户独立技能。
func TestSkillArtifact_ImportTemplate_HappyPath(t *testing.T) {
	db := newArtifactTestDB(t)
	seedArtifactUsers(t, db)
	engine := newArtifactTestEngine(t, db)

	// 1. 种入一个 skill_template
	require.NoError(t, db.Exec(
		`INSERT INTO skill_template (id, name, description, questionnaire_answers, default_tool_flags) VALUES (1, '爆款分析师', '分析小红书', '{"q6":["analyze_data"],"q7":["text"],"q12":"friendly"}', '{"code_sandbox":true}')`).Error)

	// 2. 模拟请求
	body := map[string]interface{}{
		"template_id": 1,
	}
	status, resp := doArtifactRequest(t, engine, http.MethodPost, "/v1/skills/import-template", body, withUser(100))
	require.Equal(t, http.StatusOK, status, "body=%+v", resp)
	assert.Equal(t, 0, resp.Code)

	var created model.Skill
	require.NoError(t, json.Unmarshal(resp.Data, &created))
	assert.Equal(t, "爆款分析师", created.Name)
	assert.Equal(t, "imported_from_template", created.SourceType)
	assert.Equal(t, "official", created.OriginType)
	assert.NotZero(t, created.SourceTemplateID)
	assert.Equal(t, uint(1), *created.SourceTemplateID)

	// 3. 验证编译出的 BodyMd 中包含核心要素
	assert.Contains(t, created.BodyMd, "分析小红书")
	assert.Contains(t, created.BodyMd, "任务类型")
}
