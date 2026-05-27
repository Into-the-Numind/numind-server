package skill

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestService creates an in-memory SQLite-backed Service for unit tests.
// Returns the Service and raw *gorm.DB.
// Seeds parent user (ParentUserID nil) + child user (ParentUserID → parent).
func newTestService(t *testing.T) (Service, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)

	// AutoMigrate all tables needed by the service layer.
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.AgentDefinition{},
		&model.AgentDefinitionHistory{},
		&model.SkillTemplate{},
	))

	// Seed parent account (ParentUserID nil = parent).
	parent := &model.User{Username: "parent-test"}
	require.NoError(t, db.Create(parent).Error)

	// Seed child account (ParentUserID points to parent).
	childParentID := parent.ID
	child := &model.User{Username: "child-test", ParentUserID: &childParentID}
	require.NoError(t, db.Create(child).Error)

	ds := store.NewTestStore(db)
	return NewService(ds), db
}

// minCreateReq returns a minimal valid CreateRequest (Q6, Q7, Q12 filled).
func minCreateReq() CreateRequest {
	return CreateRequest{
		Name:        "测试 Agent",
		Description: "单测描述",
		Starters:    []string{},
		ToolFlags:   map[string]bool{},
		QuestionnaireAnswers: QuestionnaireAnswers{
			Q6:  []string{"answer_questions"},
			Q7:  []string{"text"},
			Q12: "friendly",
		},
	}
}

// seedParentUserID returns the ID of the seeded parent user.
func seedParentUserID(db *gorm.DB) uint {
	var u model.User
	db.Where("parent_user_id IS NULL").First(&u)
	return u.ID
}

// seedChildUserID returns the ID of the seeded child user.
func seedChildUserID(db *gorm.DB) uint {
	var u model.User
	db.Where("parent_user_id IS NOT NULL").First(&u)
	return u.ID
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestService_Create_succeeds(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	assert.NotZero(t, ad.ID)
	assert.Equal(t, parentID, ad.ParentUserID)
	assert.Equal(t, uint(1), ad.Version)
	assert.True(t, ad.IsActive)
	assert.NotEmpty(t, ad.GeneratedSkillBody)
}

func TestService_Create_childAccount_returns403(t *testing.T) {
	svc, db := newTestService(t)
	childID := seedChildUserID(db)

	_, err := svc.Create(context.Background(), childID, minCreateReq())
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrChildAccountForbidden)
}


func TestService_Create_isActive_true_by_default(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	// Verify DB row — GORM default:1 bool Create fixup must have applied.
	var row model.AgentDefinition
	require.NoError(t, db.First(&row, ad.ID).Error)
	assert.True(t, row.IsActive, "is_active should be true after Create")
}

func TestService_Create_writesHistoryV1(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	var hist model.AgentDefinitionHistory
	require.NoError(t, db.Where("agent_id = ? AND version = 1", ad.ID).First(&hist).Error)
	assert.Equal(t, ad.ID, hist.AgentID)
	assert.Equal(t, uint(1), hist.Version)
	assert.Equal(t, "首次发布", hist.ChangesSummary)
	assert.Contains(t, string(hist.Snapshot), "测试 Agent")
}

// TestService_Create_emptyToolFlags_derivesDefault: when frontend doesn't
// supply tool_flags (5-step questionnaire lacks a tool-selection step),
// the service must derive a sensible default from questionnaire_answers,
// otherwise runner.go short-circuits with 0 tools and learners see "failed".
func TestService_Create_emptyToolFlags_derivesDefault(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	req := minCreateReq()
	req.ToolFlags = nil // simulate frontend without tool step
	req.QuestionnaireAnswers.Q9 = "allow_search"
	req.QuestionnaireAnswers.Q7 = []string{"text", "csv"}

	ad, err := svc.Create(context.Background(), parentID, req)
	require.NoError(t, err)

	var flags map[string]bool
	require.NoError(t, json.Unmarshal(ad.ToolFlags, &flags))
	// Always-on basics
	assert.True(t, flags["kb_search"], "kb_search default on")
	assert.True(t, flags["learner_data_query"], "learner_data_query default on")
	assert.True(t, flags["memory_read"], "memory_read default on")
	assert.True(t, flags["memory_write"], "memory_write default on")
	assert.True(t, flags["get_current_date"], "get_current_date default on")
	assert.True(t, flags["ask_user_question"], "ask_user_question default on")
	// q9=allow_search → web tools on
	assert.True(t, flags["web_search"], "web_search on when q9=allow_search")
	assert.True(t, flags["web_fetch"], "web_fetch on when q9=allow_search")
	// q7 has text/csv → file_read on
	assert.True(t, flags["file_read"], "file_read on when q7 has materials")
	// Dangerous now default ON
	assert.True(t, flags["bash_exec"], "bash_exec default on")
	assert.True(t, flags["image_gen"], "image_gen default on")
	assert.True(t, flags["document_generate"], "document_generate default on")
}

// TestService_Create_emptyToolFlags_noSearch: all tools remain default ON in new design.
func TestService_Create_emptyToolFlags_noSearch(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	req := minCreateReq()
	req.ToolFlags = nil
	req.QuestionnaireAnswers.Q9 = "no_web_search"
	req.QuestionnaireAnswers.Q7 = []string{"voice"}

	ad, err := svc.Create(context.Background(), parentID, req)
	require.NoError(t, err)

	var flags map[string]bool
	require.NoError(t, json.Unmarshal(ad.ToolFlags, &flags))
	assert.True(t, flags["web_search"], "web_search is default on")
	assert.True(t, flags["web_fetch"], "web_fetch is default on")
	assert.True(t, flags["file_read"], "file_read is default on")
	// Basics still on
	assert.True(t, flags["kb_search"])
	assert.True(t, flags["memory_read"])
}

func TestService_Create_userSuppliedToolFlags_winsOverDefault(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	req := minCreateReq()
	// User explicitly disables kb_search even though q9=allow_search would default-on.
	req.ToolFlags = map[string]bool{"kb_search": false, "web_search": true}
	req.QuestionnaireAnswers.Q9 = "allow_search"

	ad, err := svc.Create(context.Background(), parentID, req)
	require.NoError(t, err)

	var flags map[string]bool
	require.NoError(t, json.Unmarshal(ad.ToolFlags, &flags))
	assert.False(t, flags["kb_search"], "user explicit false wins")
	assert.True(t, flags["web_search"])
	// Basics NOT auto-added since user supplied non-empty map
	_, hasMemoryRead := flags["memory_read"]
	assert.False(t, hasMemoryRead, "user-supplied map is authoritative; default not merged in")
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestService_Get_returnsActive(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	created, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	got, err := svc.Get(context.Background(), parentID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.True(t, got.IsActive)
}

func TestService_Get_returnsSoftDeleted(t *testing.T) {
	// Get uses GetByIDIncludeInactive so soft-deleted agents are accessible.
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	created, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	require.NoError(t, svc.SoftDelete(context.Background(), parentID, created.ID))

	got, err := svc.Get(context.Background(), parentID, created.ID)
	require.NoError(t, err)
	assert.False(t, got.IsActive)
}

func TestService_Get_otherUserAgent_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	created, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	// Create a second parent user and try to access the first parent's agent.
	other := &model.User{Username: "other-parent"}
	require.NoError(t, db.Create(other).Error)

	_, err = svc.Get(context.Background(), other.ID, created.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestService_List_filtersOnParentUserID(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	// Create 2 agents for parent.
	_, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	_, err = svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	// Create a second parent and add 1 agent for them.
	other := &model.User{Username: "other-parent2"}
	require.NoError(t, db.Create(other).Error)
	_, err = svc.Create(context.Background(), other.ID, minCreateReq())
	require.NoError(t, err)

	items, total, err := svc.List(context.Background(), parentID, false, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, items, 2)
	for _, it := range items {
		assert.Equal(t, parentID, it.ParentUserID)
	}
}

func TestService_List_includeInactiveFlag(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	require.NoError(t, svc.SoftDelete(context.Background(), parentID, ad.ID))

	// Without includeInactive: soft-deleted not returned.
	items, total, err := svc.List(context.Background(), parentID, false, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, items)

	// With includeInactive: soft-deleted returned.
	items, total, err = svc.List(context.Background(), parentID, true, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, items, 1)
}

// ---------------------------------------------------------------------------
// Patch
// ---------------------------------------------------------------------------

func TestService_Patch_succeeds(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	newName := "更新后名字"
	patched, err := svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, newName, patched.Name)
	assert.Equal(t, uint(2), patched.Version)
}

func TestService_Patch_rejectsAdvancedModeChange(t *testing.T) {
	// PatchRequest has no AdvancedMode field — compile-time enforcement.
	// This test verifies AdvancedMode is NOT changed by Patch.
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	assert.False(t, ad.AdvancedMode)

	newName := "patch-name"
	patched, err := svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{Name: &newName})
	require.NoError(t, err)
	// AdvancedMode must still be false after Patch.
	assert.False(t, patched.AdvancedMode)
}

func TestService_Patch_writesHistory(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	newName := "history-test"
	_, err = svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{Name: &newName})
	require.NoError(t, err)

	// History should now have 2 rows: v1 (首次发布) + v2 (patch).
	var count int64
	db.Model(&model.AgentDefinitionHistory{}).Where("agent_id = ?", ad.ID).Count(&count)
	assert.Equal(t, int64(2), count)
}

func TestService_Patch_questionnaireChange_rebuildsBody(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	origBody := ad.GeneratedSkillBody

	newQA := QuestionnaireAnswers{
		Q6:  []string{"generate_content"},
		Q7:  []string{"image"},
		Q12: "professional",
	}
	patched, err := svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{QuestionnaireAnswers: &newQA})
	require.NoError(t, err)
	assert.NotEqual(t, origBody, patched.GeneratedSkillBody)
	assert.Contains(t, patched.GeneratedSkillBody, "专业严谨的风格")
}

// ---------------------------------------------------------------------------
// SoftDelete
// ---------------------------------------------------------------------------

func TestService_SoftDelete_idempotent(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	require.NoError(t, svc.SoftDelete(context.Background(), parentID, ad.ID))
	// Second call must also return nil (idempotent).
	require.NoError(t, svc.SoftDelete(context.Background(), parentID, ad.ID))

	var row model.AgentDefinition
	require.NoError(t, db.First(&row, ad.ID).Error)
	assert.False(t, row.IsActive)
}

func TestService_SoftDelete_writesHistory(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	require.NoError(t, svc.SoftDelete(context.Background(), parentID, ad.ID))

	var hist model.AgentDefinitionHistory
	require.NoError(t, db.Where("agent_id = ? AND version = 2", ad.ID).First(&hist).Error)
	assert.Equal(t, "软删除", hist.ChangesSummary)
}

// ---------------------------------------------------------------------------
// ListHistory
// ---------------------------------------------------------------------------

func TestService_ListHistory_includesSoftDeleted(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	require.NoError(t, svc.SoftDelete(context.Background(), parentID, ad.ID))

	histories, err := svc.ListHistory(context.Background(), parentID, ad.ID)
	require.NoError(t, err)
	// Expect v2 (soft delete) + v1 (create) = 2 entries.
	assert.Len(t, histories, 2)
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

func TestService_Restore_succeeds(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	// Patch to create v2.
	newName := "patched-name"
	_, err = svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{Name: &newName})
	require.NoError(t, err)

	// Restore to v1.
	restored, err := svc.Restore(context.Background(), parentID, ad.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, uint(3), restored.Version)
	assert.True(t, restored.IsActive)
	assert.Equal(t, ad.ID, restored.ID)
}

func TestService_Restore_versionNotFound_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	_, err = svc.Restore(context.Background(), parentID, ad.ID, 99)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillVersionNotFound)
}

func TestService_Restore_createsNewVersion(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	restored, err := svc.Restore(context.Background(), parentID, ad.ID, 1)
	require.NoError(t, err)
	// v1 already existed; restore creates v2.
	assert.Equal(t, uint(2), restored.Version)

	// History should have 2 rows.
	var count int64
	db.Model(&model.AgentDefinitionHistory{}).Where("agent_id = ?", ad.ID).Count(&count)
	assert.Equal(t, int64(2), count)

	// Latest history entry summary = "从 v1 恢复".
	var hist model.AgentDefinitionHistory
	require.NoError(t, db.Where("agent_id = ? AND version = 2", ad.ID).First(&hist).Error)
	assert.Equal(t, "从 v1 恢复", hist.ChangesSummary)
}

// ---------------------------------------------------------------------------
// AdvancedToggle
// ---------------------------------------------------------------------------

func TestService_AdvancedToggle_succeeds(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	assert.False(t, ad.AdvancedMode)

	toggled, err := svc.AdvancedToggle(context.Background(), parentID, ad.ID)
	require.NoError(t, err)
	assert.True(t, toggled.AdvancedMode)
	assert.Equal(t, uint(2), toggled.Version)
}

func TestService_AdvancedToggle_alreadyAdvanced_returns422(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	_, err = svc.AdvancedToggle(context.Background(), parentID, ad.ID)
	require.NoError(t, err)

	// Second toggle must fail.
	_, err = svc.AdvancedToggle(context.Background(), parentID, ad.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrAlreadyInAdvancedMode)
}

func TestService_AdvancedToggle_copiesGeneratedToCustom(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)
	generatedBody := ad.GeneratedSkillBody
	require.NotEmpty(t, generatedBody)

	toggled, err := svc.AdvancedToggle(context.Background(), parentID, ad.ID)
	require.NoError(t, err)
	assert.Equal(t, generatedBody, toggled.CustomSkillBody,
		"CustomSkillBody should be a copy of GeneratedSkillBody on first toggle")
}

func TestService_AdvancedToggle_preservesQuestionnaireAnswers(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	toggled, err := svc.AdvancedToggle(context.Background(), parentID, ad.ID)
	require.NoError(t, err)

	// QuestionnaireAnswers should be preserved after toggle.
	var qa QuestionnaireAnswers
	require.NoError(t, json.Unmarshal(toggled.QuestionnaireAnswers, &qa))
	assert.Equal(t, []string{"answer_questions"}, qa.Q6)
}

// ---------------------------------------------------------------------------
// ListTemplates
// ---------------------------------------------------------------------------

func TestService_ListTemplates_succeeds(t *testing.T) {
	svc, db := newTestService(t)

	// Seed a template directly.
	tmpl := &model.SkillTemplate{
		Name:                 "示例模板",
		QuestionnaireAnswers: []byte(`{"q6":["analyze_data"],"q7":["text"],"q12":"friendly"}`),
		IsActive:             true,
		DisplayOrder:         10,
	}
	require.NoError(t, db.Create(tmpl).Error)

	list, err := svc.ListTemplates(context.Background())
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "示例模板", list[0].Name)
}

// ---------------------------------------------------------------------------
// Additional coverage: error paths
// ---------------------------------------------------------------------------

func TestService_Get_nonexistentID_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	_, err := svc.Get(context.Background(), parentID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}

func TestService_Patch_nonexistentID_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	newName := "x"
	_, err := svc.Patch(context.Background(), parentID, 99999, PatchRequest{Name: &newName})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}

func TestService_SoftDelete_nonexistentID_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	err := svc.SoftDelete(context.Background(), parentID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}

func TestService_ListHistory_nonexistentAgent_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	_, err := svc.ListHistory(context.Background(), parentID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}

func TestService_Restore_nonexistentAgent_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	_, err := svc.Restore(context.Background(), parentID, 99999, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}

func TestService_AdvancedToggle_nonexistentID_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	_, err := svc.AdvancedToggle(context.Background(), parentID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}

func TestService_List_childAccount_returns403(t *testing.T) {
	svc, db := newTestService(t)
	childID := seedChildUserID(db)

	_, _, err := svc.List(context.Background(), childID, false, 1, 20)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrChildAccountForbidden)
}

func TestService_SoftDelete_childAccount_returns403(t *testing.T) {
	svc, db := newTestService(t)
	childID := seedChildUserID(db)

	err := svc.SoftDelete(context.Background(), childID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrChildAccountForbidden)
}

func TestService_Patch_otherUserAgent_returns404(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	other := &model.User{Username: "other-patch"}
	require.NoError(t, db.Create(other).Error)

	newName := "x"
	_, err = svc.Patch(context.Background(), other.ID, ad.ID, PatchRequest{Name: &newName})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSkillNotFound)
}
