package marketplace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// ============================================================================
// T4 — MARKETPLACE REFERENCE-MODE CROSS-TENANT ISOLATION TESTS (10, 11)
// ============================================================================

// Test 10 — Subscribe reference-mode: writes pointer in subscriber's tenant ONLY,
// no clone into publisher's tenant; subscriber never reads publisher's source skill.
func TestSubscribe_ReferenceMode_NoCrossTenantClone(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	// publisher = parent 1; subscriber = parent 2.
	seedSkill(t, art, db, 10, 1, "publisher private body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	// Count publisher-tenant skills BEFORE subscribe.
	var beforePub int64
	require.NoError(t, db.Model(&model.Skill{}).Where("parent_user_id = ?", 1).Count(&beforePub).Error)

	subID, sourceSkillID, err := svc.Subscribe(ctx, 2, mp.ID)
	require.NoError(t, err)
	assert.Equal(t, uint(10), sourceSkillID)

	// subscription is reference-mode.
	var sub model.SkillSubscription
	require.NoError(t, db.First(&sub, subID).Error)
	assert.Greater(t, sub.SourceSkillID, uint(0), "reference-mode: source_skill_id > 0")
	assert.Equal(t, uint(0), sub.ClonedSkillID, "reference-mode: cloned_skill_id == 0")

	// reference pointer exists in subscriber tenant (parent_user_id=2) ONLY.
	var subPtrCount int64
	require.NoError(t, db.Model(&model.Skill{}).
		Where("parent_user_id = ? AND marketplace_id = ?", 2, mp.ID).Count(&subPtrCount).Error)
	assert.Equal(t, int64(1), subPtrCount, "exactly one reference pointer in subscriber tenant")

	// NO new row written into publisher's tenant.
	var afterPub int64
	require.NoError(t, db.Model(&model.Skill{}).Where("parent_user_id = ?", 1).Count(&afterPub).Error)
	assert.Equal(t, beforePub, afterPub, "no row written into publisher's tenant")

	// no reference pointer in publisher tenant.
	var pubPtrCount int64
	require.NoError(t, db.Model(&model.Skill{}).
		Where("parent_user_id = ? AND marketplace_id > 0", 1).Count(&pubPtrCount).Error)
	assert.Equal(t, int64(0), pubPtrCount)

	// reference-mode does NOT call artifactSvc.Create (no clone).
	assert.Empty(t, art.createCalls, "reference-mode must not clone via artifact svc")
}

// Test 11a — sub-user can publish a skill they OWN (visibility=sub_user).
func TestPublish_SubUserCanPublishOwnSubUserSkill(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	realStubChat(t)
	ctx := context.Background()

	// child 3 (institution 1) OWNS a sub_user skill: parent_user_id=1 (institution),
	// owner_user_id=3 (the child), visibility='sub_user'.
	seedSkillOwned(t, art, db, 20, 1, 3, "sub_user", "child's own skill body")

	mp, err := svc.Publish(ctx, 3, PublishRequest{
		SkillID: 20, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文",
	})
	require.NoError(t, err)
	require.NotNil(t, mp)
	assert.Equal(t, uint(3), mp.PublisherUserID, "PublisherUserID = actual sub-user publisher")
	assert.Equal(t, uint(20), mp.SourceSkillID)
}

// Test 11b — sub-user cannot publish skills they do NOT own.
func TestPublish_SubUserCannotPublishOthers(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	realStubChat(t)
	ctx := context.Background()

	// institution skill owned by parent 1 (child 3's institution) — child does NOT own.
	seedSkillOwned(t, art, db, 21, 1, 1, "institution", "institution body")
	_, err := svc.Publish(ctx, 3, PublishRequest{SkillID: 21, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "x"})
	assert.ErrorIs(t, err, ErrSkillNotOwned, "sub-user can't publish institution skill they don't own")

	// another sub-user's private skill (owner=99) in same institution — not owned by 3.
	seedSkillOwned(t, art, db, 22, 1, 99, "sub_user", "other sub-user body")
	_, err = svc.Publish(ctx, 3, PublishRequest{SkillID: 22, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "x"})
	assert.ErrorIs(t, err, ErrSkillNotOwned, "sub-user can't publish another sub-user's private skill")
}

// Test 11c — parent can publish their own institution skill.
func TestPublish_ParentCanPublishOwnInstitutionSkill(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	realStubChat(t)
	ctx := context.Background()

	seedSkillOwned(t, art, db, 23, 1, 1, "institution", "parent institution body")
	mp, err := svc.Publish(ctx, 1, PublishRequest{SkillID: 23, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, err)
	assert.Equal(t, uint(1), mp.PublisherUserID)
}

// seedSkillOwned inserts a skill with explicit owner_user_id + visibility into the
// real DB (Publish reads from the real DB via getOwnedSkillForPublish).
func seedSkillOwned(t *testing.T, art *fakeArtifactSvc, db *gorm.DB, id, parentUserID, ownerUserID uint, visibility, body string) {
	t.Helper()
	sk := &model.Skill{
		ParentUserID: parentUserID,
		OwnerUserID:  ownerUserID,
		Visibility:   visibility,
		Name:         "Owned Skill",
		Description:  "owned",
		WhenToUse:    "when",
		BodyMd:       body,
		SourceType:   "custom",
		OriginType:   "user",
		Version:      1,
		IsActive:     true,
		CreatedBy:    ownerUserID,
	}
	sk.ID = id
	art.skills[id] = sk
	require.NoError(t, db.Select("*").Create(sk).Error)
}
