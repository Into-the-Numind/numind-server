package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// fakeMarketplaceReader is an in-memory MarketplaceSnapshotReader for T4 reference
// pointer tests. It tracks which marketplace ids were queried — and CRUCIALLY,
// it has NO access to any skill table, so a passing test proves loadDBSkill reads
// ONLY the marketplace snapshot, never the publisher's private skill body.
type fakeMarketplaceReader struct {
	rows        map[uint]fakeMpRow
	queriedIDs  []uint
	publisherDB map[uint]string // a "publisher private skill body" the test asserts is NEVER read
}

type fakeMpRow struct {
	body     string
	isPublic bool
	exists   bool
}

func (f *fakeMarketplaceReader) GetSnapshot(_ context.Context, marketplaceID uint) (string, bool, bool) {
	f.queriedIDs = append(f.queriedIDs, marketplaceID)
	r, ok := f.rows[marketplaceID]
	if !ok || !r.exists {
		return "", false, false
	}
	return r.body, r.isPublic, true
}

// referencePointerSkill builds a reference-pointer DB skill (marketplace_id set).
// BodyMd is a STALE seed snapshot — the test asserts the CURRENT marketplace body
// is used instead.
func referencePointerSkill(id, marketplaceID uint, name, staleBody string) *model.Skill {
	return &model.Skill{
		ID:            id,
		ParentUserID:  2, // subscriber's tenant
		OwnerUserID:   2,
		Visibility:    "institution",
		Name:          name,
		BodyMd:        staleBody,
		Version:       1,
		IsActive:      true,
		MarketplaceID: marketplaceID,
		AllowedTools:  []byte(`[]`),
	}
}

// Test 8 — reference pointer loads the marketplace CURRENT snapshot, NOT the
// publisher's private skill body nor the stale seed body.
func TestLoadDBSkill_ReferenceReadsMarketplaceSnapshot_NotPublisherTenant(t *testing.T) {
	const mpID = uint(900)
	staleSeed := "STALE SEED BODY — must not be used"
	publisherPrivateBody := "PUBLISHER PRIVATE BODY — must NEVER be read cross-tenant"
	currentSnapshot := "CURRENT marketplace snapshot after publisher re-publish"

	reader := &fakeMarketplaceReader{
		rows: map[uint]fakeMpRow{
			mpID: {body: currentSnapshot, isPublic: true, exists: true},
		},
		// publisherDB is intentionally unreachable from loadDBSkill — its presence
		// here is a documentation device: the reader can't return it.
		publisherDB: map[uint]string{42: publisherPrivateBody},
	}

	sk := referencePointerSkill(7, mpID, "订阅技能", staleSeed)
	ctx, turn := buildTurnWithSkills(t, sk)

	tool := NewLoadSkillToolWithMarketplace(nil, reader)
	ack := execLoadSkill(t, tool, ctx, "订阅技能")

	require.Equal(t, "loaded", ack["status"])
	body, _ := ack["body"].(string)
	assert.Contains(t, body, currentSnapshot, "must load CURRENT marketplace snapshot")
	assert.NotContains(t, body, staleSeed, "must NOT use the stale seeded pointer body")
	assert.NotContains(t, body, publisherPrivateBody, "must NEVER read publisher's private skill body")

	// Proof of cross-tenant guard: lookup was BY marketplace_id only.
	require.Equal(t, []uint{mpID}, reader.queriedIDs, "loadDBSkill must query ONLY by marketplace_id")
	require.Len(t, turn.PendingSkills, 1)
	assert.Contains(t, turn.PendingSkills[0].Body, currentSnapshot)
}

// Test 9 — fail-soft when the marketplace row is gone or unpublished. Must return
// a soft jsonErr tool result, NOT a Go error (NodeRunError would kill the run).
func TestLoadDBSkill_ReferenceFailSoftWhenMarketplaceGone(t *testing.T) {
	t.Run("row deleted", func(t *testing.T) {
		const mpID = uint(901)
		reader := &fakeMarketplaceReader{rows: map[uint]fakeMpRow{}} // empty → not found
		sk := referencePointerSkill(8, mpID, "下架技能", "stale")
		ctx, turn := buildTurnWithSkills(t, sk)

		out, goErr := NewLoadSkillToolWithMarketplace(nil, reader).
			Execute(ctx, ToolInput(`{"name":"下架技能"}`))
		require.NoError(t, goErr, "fail-soft: must NOT return a Go error (would kill the run)")

		ack := decodeAck(t, out)
		assert.Equal(t, "error", ack["status"], "soft error status")
		assert.Contains(t, ack["error"], "市场来源已下架")
		assert.Empty(t, turn.PendingSkills, "no body injected on fail-soft")
	})

	t.Run("unpublished is_public=0", func(t *testing.T) {
		const mpID = uint(902)
		reader := &fakeMarketplaceReader{rows: map[uint]fakeMpRow{
			mpID: {body: "x", isPublic: false, exists: true},
		}}
		sk := referencePointerSkill(9, mpID, "已下架技能", "stale")
		ctx, _ := buildTurnWithSkills(t, sk)

		out, goErr := NewLoadSkillToolWithMarketplace(nil, reader).
			Execute(ctx, ToolInput(`{"name":"已下架技能"}`))
		require.NoError(t, goErr)
		ack := decodeAck(t, out)
		assert.Equal(t, "error", ack["status"])
		assert.Contains(t, ack["error"], "市场来源已下架")
	})
}

// decodeAck unmarshals a ToolResult JSON ack into a map.
func decodeAck(t *testing.T, out ToolResult) map[string]any {
	t.Helper()
	ack := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(out), &ack))
	return ack
}
