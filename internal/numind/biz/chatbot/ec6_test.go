package chatbot_test

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteChatbot_CleanupVisibility EC-6 验证 (对称 sop/ec6_test.go).
// 删除 chatbot 时同事务软删它的所有 visibility grant.
func TestDeleteChatbot_CleanupVisibility(t *testing.T) {
	db := newChatbotListFilterTestDB(t) // 复用 list_filter_test.go 的 helper
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbFilterUser(t, db, nil)
	sub1 := insertCbFilterUser(t, db, &parent)
	sub2 := insertCbFilterUser(t, db, &parent)
	cbID := insertCbFilterConfig(t, db, parent, "to-be-deleted", true)

	for _, subID := range []uint{sub1, sub2} {
		require.NoError(t, db.Create(&model.ChatbotVisibilityGrant{
			ParentUserID: parent,
			SubUserID:    subID,
			ChatbotID:    cbID,
		}).Error)
	}

	var beforeCount int64
	db.Model(&model.ChatbotVisibilityGrant{}).Where("chatbot_id = ?", cbID).Count(&beforeCount)
	require.Equal(t, int64(2), beforeCount)

	b := chatbot.NewChatbotBiz(ds, nil, nil)
	require.NoError(t, b.DeleteChatbot(ctx, parent, cbID))

	// 验证 chatbot 已软删
	var cbCount int64
	db.Model(&model.ChatbotConfig{}).Where("id = ?", cbID).Count(&cbCount)
	assert.Equal(t, int64(0), cbCount, "chatbot should be soft-deleted")

	// 验证 grants 已软删 + unscoped 仍可见
	var afterActive, unscoped int64
	db.Model(&model.ChatbotVisibilityGrant{}).Where("chatbot_id = ?", cbID).Count(&afterActive)
	db.Unscoped().Model(&model.ChatbotVisibilityGrant{}).Where("chatbot_id = ?", cbID).Count(&unscoped)
	assert.Equal(t, int64(0), afterActive, "grants should be soft-deleted")
	assert.Equal(t, int64(2), unscoped, "soft-deleted grants preserved for audit")
}

// TestDeleteChatbot_NoGrants 无 grants 时正常工作 (cleanup 幂等).
func TestDeleteChatbot_NoGrants(t *testing.T) {
	db := newChatbotListFilterTestDB(t)
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbFilterUser(t, db, nil)
	cbID := insertCbFilterConfig(t, db, parent, "no-grants", false)

	b := chatbot.NewChatbotBiz(ds, nil, nil)
	require.NoError(t, b.DeleteChatbot(ctx, parent, cbID))
}
