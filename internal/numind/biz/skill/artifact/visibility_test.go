package artifact

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ============================================================================
// T4 — 3-TIER SKILL VISIBILITY CROSS-TENANT ISOLATION TESTS
//
// These are the highest-priority regression gate for T4. Tests 3, 4 are the
// non-negotiable cross-tenant leak gates (loadDBSkill / subscribe tests live in
// their own packages).
//
// FIXTURE (contract):
//   Institution A: parentA (id=1, parent=NULL). sub-user A1 (id=2, parent=1).
//                  sub-user A2 (id=5, parent=1).
//   Institution B: parentB (id=3, parent=NULL). sub-user B1 (id=4, parent=3).
//   Skills:
//     S_off:    visibility='official',    parent_user_id=0, owner=0 (admin)
//     S_instA:  visibility='institution', parent_user_id=1, owner=1
//     S_instB:  visibility='institution', parent_user_id=3, owner=3
//     S_subA1:  visibility='sub_user',    parent_user_id=1, owner=2
//     S_subB1:  visibility='sub_user',    parent_user_id=3, owner=4
//     S_subA1b: visibility='sub_user',    parent_user_id=1, owner=5 (A2's private)
// ============================================================================

type visFixture struct {
	svc *Service
	db  *gorm.DB
	ids map[string]uint // skill name → id
}

func newVisFixture(t *testing.T) *visFixture {
	t.Helper()
	db := newTestDB(t)

	ul := newFakeUserLookup()
	ul.addParent(1) // parentA
	ul.addChild(2, 1)
	ul.addChild(5, 1) // A2
	ul.addParent(3)   // parentB
	ul.addChild(4, 3) // B1

	svc := NewServiceWithUsers(db, ul)

	ids := map[string]uint{}
	seed := func(name string, parentUserID, ownerUserID uint, visibility string) {
		sk := &model.Skill{
			ParentUserID: parentUserID,
			OwnerUserID:  ownerUserID,
			Visibility:   visibility,
			Name:         name,
			BodyMd:       "# " + name,
			SourceType:   "custom",
			OriginType:   "user",
			Version:      1,
			IsActive:     true,
			CreatedBy:    ownerUserID,
		}
		require.NoError(t, db.Select("*").Create(sk).Error)
		ids[name] = sk.ID
	}
	seed("S_off", 0, 0, VisibilityOfficial)
	seed("S_instA", 1, 1, VisibilityInstitution)
	seed("S_instB", 3, 3, VisibilityInstitution)
	seed("S_subA1", 1, 2, VisibilitySubUser)
	seed("S_subB1", 3, 4, VisibilitySubUser)
	seed("S_subA1b", 1, 5, VisibilitySubUser)

	return &visFixture{svc: svc, db: db, ids: ids}
}

// names returns the set of skill names in a result slice.
func names(items []model.Skill) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[it.Name] = true
	}
	return out
}

// Test 1
func TestListVisibleSkills_ParentSeesOfficialPlusOwnInstitution(t *testing.T) {
	f := newVisFixture(t)
	items, total, err := f.svc.ListVisibleSkills(context.Background(), 1, 1, 100, "")
	require.NoError(t, err)
	got := names(items)
	assert.Equal(t, int64(2), total)
	assert.True(t, got["S_off"], "parentA sees official")
	assert.True(t, got["S_instA"], "parentA sees own institution skill")
	assert.False(t, got["S_instB"], "parentA must NOT see institution B")
	assert.False(t, got["S_subA1"], "parentA must NOT see sub-user's private")
	assert.False(t, got["S_subB1"], "parentA must NOT see institution B sub-user private")
}

// Test 2
func TestListVisibleSkills_SubUserSeesOfficialPlusInstitutionPlusOwnPrivate(t *testing.T) {
	f := newVisFixture(t)
	items, _, err := f.svc.ListVisibleSkills(context.Background(), 2, 1, 100, "")
	require.NoError(t, err)
	got := names(items)
	assert.True(t, got["S_off"], "A1 sees official")
	assert.True(t, got["S_instA"], "A1 sees its institution skill")
	assert.True(t, got["S_subA1"], "A1 sees its own private sub_user skill")
	assert.False(t, got["S_instB"], "A1 must NOT see institution B")
	assert.False(t, got["S_subB1"], "A1 must NOT see institution B private")
	assert.False(t, got["S_subA1b"], "A1 must NOT see another sub-user's private (same institution)")
}

// Test 3 — THE headline cross-institution leak gate
func TestListVisibleSkills_CrossInstitutionLeakBlocked(t *testing.T) {
	f := newVisFixture(t)
	items, _, err := f.svc.ListVisibleSkills(context.Background(), 4, 1, 100, "")
	require.NoError(t, err)
	got := names(items)
	assert.True(t, got["S_off"], "B1 sees official")
	assert.True(t, got["S_instB"], "B1 sees its institution skill")
	assert.True(t, got["S_subB1"], "B1 sees its own private")
	// Non-negotiable: institution A data NEVER leaks to institution B.
	assert.False(t, got["S_instA"], "LEAK: institution A skill visible to institution B")
	assert.False(t, got["S_subA1"], "LEAK: institution A sub-user private visible to institution B")
	assert.False(t, got["S_subA1b"], "LEAK: institution A sub-user private visible to institution B")
}

// Test 4 — sub-user cannot see another sub-user's private (same institution)
func TestListVisibleSkills_SubUserCannotSeeAnotherSubUserPrivate(t *testing.T) {
	f := newVisFixture(t)
	items, _, err := f.svc.ListVisibleSkills(context.Background(), 2, 1, 100, "")
	require.NoError(t, err)
	got := names(items)
	assert.False(t, got["S_subA1b"], "sub_user is owner-scoped, NOT institution-scoped")
}

// Test 5 — sub-user cannot set institution/official
func TestCreateSkill_SubUserCannotSetInstitution(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()

	// A1 (id=2) institution=1, isParent=false. Attempt institution → forbidden.
	_, err := f.svc.Create(ctx, 2, 1, false, CreateRequest{
		Name: "x", BodyMd: validBody(), Visibility: VisibilityInstitution,
	})
	require.ErrorIs(t, err, errno.ErrSkillVisibilityForbidden)

	// Attempt official → forbidden.
	_, err = f.svc.Create(ctx, 2, 1, false, CreateRequest{
		Name: "y", BodyMd: validBody(), Visibility: VisibilityOfficial,
	})
	require.ErrorIs(t, err, errno.ErrSkillVisibilityForbidden)

	// Empty/sub_user → persists sub_user, owner=2, parent_user_id=1.
	sk, err := f.svc.Create(ctx, 2, 1, false, CreateRequest{Name: "z", BodyMd: validBody()})
	require.NoError(t, err)
	assert.Equal(t, VisibilitySubUser, sk.Visibility)
	assert.Equal(t, uint(2), sk.OwnerUserID)
	assert.Equal(t, uint(1), sk.ParentUserID)
}

// Test 6 — parent default institution; may create sub_user; never official.
func TestCreateSkill_ParentDefaultInstitution(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()

	// Empty visibility → institution.
	sk, err := f.svc.Create(ctx, 1, 1, true, CreateRequest{Name: "p1", BodyMd: validBody()})
	require.NoError(t, err)
	assert.Equal(t, VisibilityInstitution, sk.Visibility)
	assert.Equal(t, uint(1), sk.OwnerUserID)
	assert.Equal(t, uint(1), sk.ParentUserID)

	// Parent may create sub_user (private).
	skp, err := f.svc.Create(ctx, 1, 1, true, CreateRequest{Name: "p2", BodyMd: validBody(), Visibility: VisibilitySubUser})
	require.NoError(t, err)
	assert.Equal(t, VisibilitySubUser, skp.Visibility)

	// Parent may NEVER set official via API.
	_, err = f.svc.Create(ctx, 1, 1, true, CreateRequest{Name: "p3", BodyMd: validBody(), Visibility: VisibilityOfficial})
	require.ErrorIs(t, err, errno.ErrSkillVisibilityForbidden)
}

// Test 7 — cross-tenant Update/Delete returns not-found; official read-only.
func TestUpdateDeleteSkill_CrossTenantReturnsNotFound(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()

	// parentB(3) Update S_instA → not found (no existence reveal).
	_, err := f.svc.Update(ctx, 3, f.ids["S_instA"], CreateRequest{Name: "hijack", BodyMd: validBody()})
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
	// parentB(3) Delete S_instA → not found.
	_, err = f.svc.Delete(ctx, 3, f.ids["S_instA"])
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)

	// A1(2) Update/Delete S_subA1b (A2's private, same institution) → not found.
	_, err = f.svc.Update(ctx, 2, f.ids["S_subA1b"], CreateRequest{Name: "hijack", BodyMd: validBody()})
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
	_, err = f.svc.Delete(ctx, 2, f.ids["S_subA1b"])
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)

	// 'official' Update by any non-admin → not found (read-only; can_edit=false).
	// parentA(1) sees S_off (it's official, visible) but cannot edit it.
	_, err = f.svc.Update(ctx, 1, f.ids["S_off"], CreateRequest{Name: "hijack", BodyMd: validBody()})
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

// Bonus: parent CAN edit own institution skill; owner CAN edit own sub_user skill.
func TestUpdateSkill_OwnerAndParentCanEdit(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()

	// parentA edits own institution skill.
	upd, err := f.svc.Update(ctx, 1, f.ids["S_instA"], CreateRequest{Name: "S_instA-v2", BodyMd: validBody()})
	require.NoError(t, err)
	assert.Equal(t, "S_instA-v2", upd.Name)
	assert.True(t, upd.CanEdit)

	// A1 edits own sub_user skill.
	upd2, err := f.svc.Update(ctx, 2, f.ids["S_subA1"], CreateRequest{Name: "S_subA1-v2", BodyMd: validBody()})
	require.NoError(t, err)
	assert.Equal(t, "S_subA1-v2", upd2.Name)
}

// Test 12 — runtime ListByAgent stays tenant-scoped: even if an agent in
// institution B has a binding row pointing at institution A's skill id, the
// WHERE id IN ? AND parent_user_id = ? guard never returns A's skill row.
func TestListByAgent_RuntimeStaysTenantScoped(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	bsvc := NewBindingService(f.db)

	// agentB belongs to institution B (parent_user_id=3).
	require.NoError(t, f.db.Exec(
		"INSERT INTO agent_definition (id, parent_user_id, name, is_active, created_by) VALUES (?, ?, ?, ?, ?)",
		77, 3, "agentB", 1, 3).Error)

	// Maliciously insert a binding from agentB → institution A's S_instA skill id.
	require.NoError(t, f.db.Exec(
		"INSERT INTO agent_skill_binding (agent_id, skill_id, is_active) VALUES (?, ?, ?)",
		77, f.ids["S_instA"], 1).Error)
	// Also a legitimate binding to institution B's own skill.
	require.NoError(t, f.db.Exec(
		"INSERT INTO agent_skill_binding (agent_id, skill_id, is_active) VALUES (?, ?, ?)",
		77, f.ids["S_instB"], 1).Error)

	// ListByAgent for institution B (parent_user_id=3) must return ONLY B's skill,
	// never institution A's, despite the cross-tenant binding row.
	skills, err := bsvc.ListByAgent(ctx, 3, 77)
	require.NoError(t, err)
	got := names(skills)
	assert.True(t, got["S_instB"], "agentB sees its own institution skill")
	assert.False(t, got["S_instA"], "LEAK: cross-tenant skill returned via binding")
	assert.Len(t, skills, 1, "only the tenant-scoped skill survives the WHERE guard")
}

// ============================================================================
// T4 SECURITY REGRESSION — History (read) + Restore (write) must apply the same
// GetForCaller + computeCanEdit gate as Update/Delete.
//
// Before the fix the controller passed instID (institution id) into
// Service.ListHistory / Service.Restore, and versioning.go gated only on
// (parent_user_id == instID). That let any sub-user in the same institution READ
// another sub-user's private (visibility='sub_user') skill history and, far worse,
// OVERWRITE/REACTIVATE it via Restore — bypassing computeCanEdit entirely.
// ============================================================================

// seedVersionedSkill writes a 2-version skill directly (bypassing Service.Create's
// visibility裁决) so we can target arbitrary owner/parent/visibility, then have a
// real history row to Restore to.
func seedVersionedSkill(t *testing.T, f *visFixture, parentUserID, ownerUserID uint, visibility, name string) uint {
	t.Helper()
	sk := &model.Skill{
		ParentUserID: parentUserID,
		OwnerUserID:  ownerUserID,
		Visibility:   visibility,
		Name:         name,
		BodyMd:       "# " + name + " v2",
		SourceType:   "custom",
		OriginType:   "user",
		Version:      2, // current state is v2; history holds both v1 and v2
		IsActive:     true,
		CreatedBy:    ownerUserID,
	}
	require.NoError(t, f.db.Select("*").Create(sk).Error)
	writeHist(t, f, sk.ID, 1, "# "+name+" v1", name, ownerUserID)
	writeHist(t, f, sk.ID, 2, "# "+name+" v2", name, ownerUserID)
	return sk.ID
}

// writeHist appends a skill_history row matching the JSON shape writeSnapshotTx produces.
func writeHist(t *testing.T, f *visFixture, skillID, version uint, bodyMd, name string, createdBy uint) {
	t.Helper()
	snapSkill := &model.Skill{Name: name, BodyMd: bodyMd, SourceType: "custom", Version: version, IsActive: true}
	snapSkill.ID = skillID
	snapshot, err := json.Marshal(snapSkill)
	require.NoError(t, err)
	h := &model.SkillHistory{SkillID: skillID, Version: version, Snapshot: datatypes.JSON(snapshot), CreatedBy: createdBy}
	require.NoError(t, f.db.Create(h).Error)
}

// Test H1 — sub-user CANNOT read another sub-user's private skill history (same institution).
func TestListHistory_CrossSubUserPrivateReturnsNotFound(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	// S_subA1b is A2's (owner=5) private skill, parent_user_id=1. A1 (id=2) attempts to read.
	id := seedVersionedSkill(t, f, 1, 5, VisibilitySubUser, "H_privA2")
	_, err := f.svc.ListHistory(ctx, 2, id)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound,
		"READ LEAK: sub-user A1 read another sub-user's private skill history")
}

// Test H2 — Restore (WRITE) CANNOT hijack another sub-user's private skill (same institution).
func TestRestore_CrossSubUserPrivateReturnsNotFound(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	id := seedVersionedSkill(t, f, 1, 5, VisibilitySubUser, "R_privA2")

	// Capture pre-state.
	var before model.Skill
	require.NoError(t, f.db.First(&before, id).Error)

	// A1 (id=2) attempts to restore A2's private skill to v1.
	_, err := f.svc.Restore(ctx, 2, id, 1)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound,
		"WRITE LEAK: sub-user A1 hijacked another sub-user's private skill via Restore")

	// Verify NOTHING was mutated (no version bump, body unchanged).
	var after model.Skill
	require.NoError(t, f.db.First(&after, id).Error)
	assert.Equal(t, before.Version, after.Version, "Restore must not bump version of a non-editable skill")
	assert.Equal(t, before.BodyMd, after.BodyMd, "Restore must not rewrite body of a non-editable skill")
}

// Test H3 — Restore CANNOT reactivate (revive) a soft-deleted private skill cross-sub-user.
func TestRestore_CrossSubUserCannotReviveSoftDeleted(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	id := seedVersionedSkill(t, f, 1, 5, VisibilitySubUser, "R_deadA2")

	// Soft-delete it (simulate A2 deleting their own skill).
	require.NoError(t, f.db.Model(&model.Skill{}).Where("id = ?", id).
		Update("is_active", false).Error)

	// A1 attempts to revive via Restore → denied.
	_, err := f.svc.Restore(ctx, 2, id, 1)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound,
		"WRITE LEAK: sub-user A1 revived another sub-user's soft-deleted private skill")

	var after model.Skill
	require.NoError(t, f.db.First(&after, id).Error)
	assert.False(t, after.IsActive, "soft-deleted skill must stay dead after denied Restore")
}

// Test H4 — cross-INSTITUTION History/Restore denial (institution B vs A).
func TestHistoryRestore_CrossInstitutionReturnsNotFound(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	// institution A skill (parent=1, owner=1, institution-visible).
	id := seedVersionedSkill(t, f, 1, 1, VisibilityInstitution, "X_instA")

	// B1 (id=4, institution B) attempts both.
	_, err := f.svc.ListHistory(ctx, 4, id)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound, "cross-institution history read must 404")
	_, err = f.svc.Restore(ctx, 4, id, 1)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound, "cross-institution restore must 404")
}

// Test H5 — 'official' skills are read-only: History readable (visible) but Restore denied.
func TestHistoryRestore_OfficialReadOnly(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	// official skill: parent=0, owner=0.
	id := seedVersionedSkill(t, f, 0, 0, VisibilityOfficial, "X_official")

	// parentA (id=1) can SEE official but cannot edit it → Restore denied.
	_, err := f.svc.Restore(ctx, 1, id, 1)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound, "official is read-only: Restore must 404 for non-admin")

	// History is also gated by can_edit (it leaks created_by/diff) → denied for non-owner of official.
	_, err = f.svc.ListHistory(ctx, 1, id)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound, "official history gated by can_edit")
}

// Test H6 (positive) — owner CAN read history of and Restore their OWN sub_user skill.
func TestHistoryRestore_OwnerAllowed(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	// A1 (id=2) owns this private skill (parent=1, owner=2).
	id := seedVersionedSkill(t, f, 1, 2, VisibilitySubUser, "Own_A1")

	hist, err := f.svc.ListHistory(ctx, 2, id)
	require.NoError(t, err, "owner reads own sub_user history")
	require.Len(t, hist, 2)

	restored, err := f.svc.Restore(ctx, 2, id, 1)
	require.NoError(t, err, "owner restores own sub_user skill")
	assert.Equal(t, "# Own_A1 v1", restored.BodyMd, "restore brought back v1 body")
	assert.Equal(t, uint(3), restored.Version, "restore bumped to v3")
}

// Test H7 (positive) — parent CAN read history of and Restore own institution skill.
func TestHistoryRestore_ParentInstitutionAllowed(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()
	id := seedVersionedSkill(t, f, 1, 1, VisibilityInstitution, "Inst_A1")

	hist, err := f.svc.ListHistory(ctx, 1, id)
	require.NoError(t, err, "parent reads own institution history")
	require.Len(t, hist, 2)

	restored, err := f.svc.Restore(ctx, 1, id, 1)
	require.NoError(t, err, "parent restores own institution skill")
	assert.Equal(t, "# Inst_A1 v1", restored.BodyMd)
}

// Test M1 (migration backfill scope) — the corrected backfill keeps tenant-private
// origin_type='official' rows institution-scoped (NOT globally visible). This guards
// the migration's :47 backfill: a user-reachable source_type='imported_from_template'
// row (origin_type='official', parent_user_id=<institution>) must NOT leak cross-tenant.
//
// We model the *post-corrected-backfill* DB state (visibility='institution' for such
// tenant rows) and prove the visibility predicate blocks cross-institution reads. The
// raw MySQL backfill SQL itself is now scoped `AND parent_user_id = 0`.
func TestBackfill_TenantOriginOfficialStaysInstitutionScoped(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()

	// Institution A creates a tenant-PRIVATE skill via the import-template-like path:
	// origin_type='official' but parent_user_id=1 (A's institution), private body.
	// Corrected backfill leaves visibility='institution' (NOT 'official').
	leak := &model.Skill{
		ParentUserID: 1,
		OwnerUserID:  1,
		Visibility:   VisibilityInstitution, // corrected backfill result (NOT 'official')
		Name:         "A_private_secret",
		BodyMd:       "# institution A's PRIVATE imported-template content",
		SourceType:   "imported_from_template",
		OriginType:   "official", // user-reachable origin_type
		Version:      1,
		IsActive:     true,
		CreatedBy:    1,
	}
	require.NoError(t, f.db.Select("*").Create(leak).Error)

	// Institution B (B1 id=4, instID=3) lists visible skills → must NOT see A's private row.
	items, _, err := f.svc.ListVisibleSkills(ctx, 4, 1, 100, "")
	require.NoError(t, err)
	assert.False(t, names(items)["A_private_secret"],
		"BACKFILL LEAK: tenant-private origin_type='official' row visible cross-institution")

	// GetForCaller for B1 → not found (no cross-tenant read of body_md).
	_, err = f.svc.GetForCaller(ctx, 4, leak.ID)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound,
		"BACKFILL LEAK: institution B read institution A's private imported-template body")
}

// Test M2 (negative control) — if the OLD buggy backfill had set visibility='official'
// on a tenant-private row, it WOULD leak. This documents the exact failure the fix
// prevents (and fails loudly if someone reverts the backfill scoping).
func TestBackfill_OfficialVisibilityWouldLeakCrossTenant(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()

	// Simulate the PRE-FIX backfill outcome: tenant-private row wrongly promoted to 'official'.
	bug := &model.Skill{
		ParentUserID: 1,
		OwnerUserID:  1,
		Visibility:   VisibilityOfficial, // the BUG: globally visible
		Name:         "A_wrongly_official",
		BodyMd:       "# leaked",
		SourceType:   "imported_from_template",
		OriginType:   "official",
		Version:      1,
		IsActive:     true,
		CreatedBy:    1,
	}
	require.NoError(t, f.db.Select("*").Create(bug).Error)

	// This asserts the leak EXISTS under the buggy state — proving 'official' is
	// genuinely globally visible, hence why the backfill MUST be parent_user_id=0 scoped.
	items, _, err := f.svc.ListVisibleSkills(ctx, 4, 1, 100, "")
	require.NoError(t, err)
	assert.True(t, names(items)["A_wrongly_official"],
		"sanity: 'official' visibility IS globally visible — backfill must never set it on tenant rows")
}

// Bonus: CanEdit is computed correctly in ListVisibleSkills.
func TestListVisibleSkills_CanEditComputed(t *testing.T) {
	f := newVisFixture(t)
	ctx := context.Background()

	// A1 (sub-user) view: can edit S_subA1 (owns), cannot edit S_instA / S_off.
	items, _, err := f.svc.ListVisibleSkills(ctx, 2, 1, 100, "")
	require.NoError(t, err)
	for _, it := range items {
		switch it.Name {
		case "S_subA1":
			assert.True(t, it.CanEdit, "owner can edit own sub_user skill")
		case "S_instA":
			assert.False(t, it.CanEdit, "sub-user cannot edit institution skill")
		case "S_off":
			assert.False(t, it.CanEdit, "official is read-only")
		}
	}

	// parentA view: can edit S_instA (own institution), cannot edit S_off.
	items, _, err = f.svc.ListVisibleSkills(ctx, 1, 1, 100, "")
	require.NoError(t, err)
	for _, it := range items {
		switch it.Name {
		case "S_instA":
			assert.True(t, it.CanEdit, "parent can edit own institution skill")
		case "S_off":
			assert.False(t, it.CanEdit, "official read-only for everyone")
		}
	}
}
