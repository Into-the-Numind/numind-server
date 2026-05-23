package marketplace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

// helper: build a marketplace row in-memory for clone tests (not persisted).
func makeMarketplaceRow(id uint, name, body string) *model.SkillMarketplace {
	tools, _ := json.Marshal([]string{"web_search", "file_read"})
	return &model.SkillMarketplace{
		Name:            name,
		Description:     "源描述",
		WhenToUse:       "什么时候用",
		SanitizedBodyMD: body,
		AllowedTools:    datatypes.JSON(tools),
		SourceSkillID:   42,
		PublisherUserID: 1,
	}
}

func TestCloneToSubscriber_WritesCorrectFields(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	s := svc.(*service)

	mp := makeMarketplaceRow(0, "销售调研", "脱敏正文")
	mp.ID = 100

	clonedID, err := s.cloneToSubscriber(context.Background(), mp, 2)
	require.NoError(t, err)
	require.NotZero(t, clonedID)

	require.Len(t, art.createCalls, 1)
	call := art.createCalls[0]
	assert.Equal(t, uint(2), call.ParentUserID, "rule 7: cloned skill must belong to subscriber tenant")
	assert.Equal(t, uint(2), call.CreatedBy)
	assert.Equal(t, "imported_from_marketplace", call.Req.SourceType)
	assert.Equal(t, "销售调研", call.Req.Name)
	assert.Equal(t, "脱敏正文", call.Req.BodyMd)
	assert.Equal(t, "什么时候用", call.Req.WhenToUse)
	assert.ElementsMatch(t, []string{"web_search", "file_read"}, call.Req.AllowedTools)
}

func TestCloneToSubscriber_DescriptionEnrichedWithProvenance(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	s := svc.(*service)

	mp := makeMarketplaceRow(0, "X", "body")
	mp.ID = 555
	mp.Description = "原描述"

	_, err := s.cloneToSubscriber(context.Background(), mp, 2)
	require.NoError(t, err)

	require.Len(t, art.createCalls, 1)
	desc := art.createCalls[0].Req.Description
	assert.True(t, strings.HasPrefix(desc, "原描述"), "must prefix subscriber-visible description with original")
	assert.Contains(t, desc, "订阅自市场", "provenance marker")
	assert.Contains(t, desc, "marketplace_id=555", "include marketplace id for traceability")
}

func TestCloneToSubscriber_ArtifactFailure_WrapsError(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	s := svc.(*service)

	mp := makeMarketplaceRow(0, "X", "body")
	mp.ID = 100
	art.failNextCreate = true

	_, err := s.cloneToSubscriber(context.Background(), mp, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloneToSubscriber:", "error must be wrapped with caller name")
	assert.Contains(t, err.Error(), "forced create failure", "underlying provider cause preserved")
}

func TestCloneToSubscriber_NoLangfuseContext_NoPanic(t *testing.T) {
	// Default context.Background() has no TraceCtx — must not panic.
	svc, _, _, _, _ := newTestService(t)
	s := svc.(*service)

	mp := makeMarketplaceRow(0, "X", "body")
	mp.ID = 100

	_, err := s.cloneToSubscriber(context.Background(), mp, 2)
	require.NoError(t, err)
}

func TestUnsubscribeCleanup_DelegatesToArtifactDelete(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	s := svc.(*service)

	seedSkill(t, art, 200, 2, "body")

	err := s.unsubscribeCleanup(context.Background(), 2, 200, 999)
	require.NoError(t, err)
	assert.Contains(t, art.deleteCalls, uint(200), "must call artifactSvc.Delete with cloned skill id")
	assert.False(t, art.skills[200].IsActive, "soft-deleted by fakeArtifactSvc")
}

func TestUnsubscribeCleanup_DeleteFailure_WrapsError(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	s := svc.(*service)

	seedSkill(t, art, 200, 2, "body")
	art.failNextDelete = true

	err := s.unsubscribeCleanup(context.Background(), 2, 200, 999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsubscribeCleanup:", "wrapped with caller name")
	assert.Contains(t, err.Error(), "forced delete failure", "underlying provider cause preserved")
}
