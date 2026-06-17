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

// minCreateReq returns a minimal valid CreateRequest. SystemPrompt is now the
// single required prompt field (questionnaire removed from the user-facing path).
func minCreateReq() CreateRequest {
	return CreateRequest{
		Name:         "测试 Agent",
		Description:  "单测描述",
		SystemPrompt: "你是一个友好的助手。",
		Starters:     []string{},
		ToolFlags:    map[string]bool{},
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
	// GeneratedSkillBody is no longer populated on Create — new agents use
	// SystemPrompt via the V2 runtime path.
	assert.Empty(t, ad.GeneratedSkillBody)
}

func TestService_Create_childAccount_returns403(t *testing.T) {
	svc, db := newTestService(t)
	childID := seedChildUserID(db)

	_, err := svc.Create(context.Background(), childID, minCreateReq())
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrChildAccountForbidden)
}

// TestService_Create_emptySystemPrompt_returnsError: SystemPrompt is now the
// single required prompt field (questionnaire removed). Blank/whitespace-only
// prompts are rejected so every new agent has a runtime prompt.
func TestService_Create_emptySystemPrompt_returnsError(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	cases := []struct {
		name   string
		prompt string
	}{
		{name: "empty", prompt: ""},
		{name: "whitespace only", prompt: "   \n\t  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := minCreateReq()
			req.SystemPrompt = tc.prompt

			var beforeCount int64
			require.NoError(t, db.Model(&model.AgentDefinition{}).Count(&beforeCount).Error)

			_, err := svc.Create(context.Background(), parentID, req)
			require.Error(t, err)
			assert.ErrorIs(t, err, errno.ErrInvalidParameter)

			var afterCount int64
			require.NoError(t, db.Model(&model.AgentDefinition{}).Count(&afterCount).Error)
			assert.Equal(t, beforeCount, afterCount, "no new AgentDefinition row should be persisted when validation fails")
		})
	}
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
// supply tool_flags, the service must derive a sensible default,
// otherwise runner.go short-circuits with 0 tools and learners see "failed".
func TestService_Create_emptyToolFlags_derivesDefault(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	req := minCreateReq()
	req.ToolFlags = nil // simulate frontend without tool step

	ad, err := svc.Create(context.Background(), parentID, req)
	require.NoError(t, err)

	var flags map[string]bool
	require.NoError(t, json.Unmarshal(ad.ToolFlags, &flags))
	// tool_flags now uses the risk-CATEGORY keys (matching AgentAdvancedEdit),
	// full-open by default. Baseline tools (kb_search/web_search/file_read/memory_*)
	// are always-on at runtime independent of tool_flags, so they are NOT listed here.
	assert.True(t, flags["code_sandbox"], "code_sandbox default on (→ bash_exec)")
	assert.True(t, flags["dangerous"], "dangerous default on")
	// media/image_gen removed (2026-06-17): 文生图不再当开关，永远可用。
	_, hasMedia := flags["media"]
	assert.False(t, hasMedia, "media category removed (image_gen ungated)")
	// Old raw tool-name keys are gone (namespace unified to categories).
	_, hasRawToolKey := flags["web_search"]
	assert.False(t, hasRawToolKey, "default no longer writes raw tool-name keys")
}

// TestService_Create_emptyToolFlags_categoryDefault: the derived default is the
// full-open risk-category set. The old per-tool derivation was removed —
// baseline tools are always-on at runtime, not gated by tool_flags.
func TestService_Create_emptyToolFlags_categoryDefault(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	req := minCreateReq()
	req.ToolFlags = nil

	ad, err := svc.Create(context.Background(), parentID, req)
	require.NoError(t, err)

	var flags map[string]bool
	require.NoError(t, json.Unmarshal(ad.ToolFlags, &flags))
	assert.Len(t, flags, 2, "default is exactly the 2 risk categories (media/image_gen ungated)")
	assert.True(t, flags["code_sandbox"])
	assert.True(t, flags["dangerous"])
}

func TestService_Create_userSuppliedToolFlags_winsOverDefault(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	req := minCreateReq()
	// User explicitly disables kb_search; the supplied map is authoritative.
	req.ToolFlags = map[string]bool{"kb_search": false, "web_search": true}

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

// TestService_Patch_systemPromptChange persists an edited SystemPrompt and
// bumps the version (Build/GeneratedSkillBody rebuild was removed).
func TestService_Patch_systemPromptChange(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	newPrompt := "你现在是一个专业严谨的分析助手。"
	patched, err := svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{SystemPrompt: &newPrompt})
	require.NoError(t, err)
	assert.Equal(t, newPrompt, patched.SystemPrompt)
	assert.Equal(t, uint(2), patched.Version)
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

// ---------------------------------------------------------------------------
// SystemPrompt validation (T2)
// ---------------------------------------------------------------------------

func TestCreateAgent_SystemPromptTooLong(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	// Construct a system_prompt that exceeds 64KB.
	req := minCreateReq()
	req.SystemPrompt = string(make([]byte, SystemPromptMaxLen+1))

	_, err := svc.Create(context.Background(), parentID, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSystemPromptTooLong)
}

func TestCreateAgent_SystemPromptOK(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	// 1KB system_prompt — well within the 64KB cap.
	req := minCreateReq()
	req.SystemPrompt = string(make([]byte, 1024))

	ad, err := svc.Create(context.Background(), parentID, req)
	require.NoError(t, err)
	assert.Len(t, ad.SystemPrompt, 1024, "system_prompt should be persisted as-is")

	// Verify DB row has the value.
	var row model.AgentDefinition
	require.NoError(t, db.First(&row, ad.ID).Error)
	assert.Len(t, row.SystemPrompt, 1024, "DB row should store system_prompt")
}

func TestPatchAgent_SystemPromptTooLong(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	// Create a valid agent first.
	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	// Attempt to patch with an oversized system_prompt.
	oversized := string(make([]byte, SystemPromptMaxLen+1))
	_, err = svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{SystemPrompt: &oversized})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrSystemPromptTooLong)
}

func TestPatchAgent_SystemPromptOK(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	// Create a valid agent first.
	ad, err := svc.Create(context.Background(), parentID, minCreateReq())
	require.NoError(t, err)

	// Patch with a valid 2KB system_prompt.
	prompt := string(make([]byte, 2048))
	updated, err := svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{SystemPrompt: &prompt})
	require.NoError(t, err)
	assert.Len(t, updated.SystemPrompt, 2048, "updated system_prompt should be returned")

	// Verify DB row.
	var row model.AgentDefinition
	require.NoError(t, db.First(&row, ad.ID).Error)
	assert.Len(t, row.SystemPrompt, 2048, "DB row should have updated system_prompt")
}

func TestPatchAgent_SystemPromptNil_unchanged(t *testing.T) {
	svc, db := newTestService(t)
	parentID := seedParentUserID(db)

	// Create agent with a specific system_prompt.
	req := minCreateReq()
	req.SystemPrompt = "original prompt"
	ad, err := svc.Create(context.Background(), parentID, req)
	require.NoError(t, err)

	// Patch with SystemPrompt = nil → should not change system_prompt.
	newName := "updated name"
	updated, err := svc.Patch(context.Background(), parentID, ad.ID, PatchRequest{Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, "original prompt", updated.SystemPrompt, "nil system_prompt patch should leave field unchanged")
}
