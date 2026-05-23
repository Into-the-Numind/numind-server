package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// newTestMarketplaceStore 创建 SQLite 内存 DB + AutoMigrate 两表 + 返回 store。
func newTestMarketplaceStore(t *testing.T) (IMarketplaceStore, *gorm.DB) {
	t.Helper()
	db := newTestDB(t, &model.SkillMarketplace{}, &model.SkillSubscription{})
	return NewMarketplaceStore(db), db
}

// sampleMarketplace 构造一个 publisher=1, source_skill=10 的默认 marketplace 行（is_public=true）。
func sampleMarketplace(name string) *model.SkillMarketplace {
	tagsJSON, _ := json.Marshal([]string{"销售"})
	toolsJSON, _ := json.Marshal([]string{"web_search"})
	return &model.SkillMarketplace{
		PublisherUserID: 1,
		SourceSkillID:   10,
		Name:            name,
		Description:     "sample for tests",
		WhenToUse:       "when sample needed",
		SanitizedBodyMD: "# " + name,
		AllowedTools:    datatypes.JSON(toolsJSON),
		CategoryTags:    datatypes.JSON(tagsJSON),
		IsPublic:        true,
	}
}

func TestMarketplaceStore_Create_And_GetByID(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))
	require.NotZero(t, mp.ID, "ID should be auto-assigned")

	got, err := s.GetByID(ctx, mp.ID)
	require.NoError(t, err)
	assert.Equal(t, mp.Name, got.Name)
	assert.True(t, got.IsPublic)
}

func TestMarketplaceStore_GetByID_NotFound(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	_, err := s.GetByID(ctx, 99999)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestMarketplaceStore_Create_NilInput(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	err := s.Create(context.Background(), nil)
	assert.Error(t, err)
}

func TestMarketplaceStore_GetActiveBySourceSkillID(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	// Publish two: one active, one unpublished, both same source_skill_id=10.
	active := sampleMarketplace("Active")
	require.NoError(t, s.Create(ctx, active))

	stale := sampleMarketplace("Stale")
	stale.IsPublic = false
	require.NoError(t, s.Create(ctx, stale))
	// Force the stale to is_public=false via UpdateColumn (database.md §6 default:true gotcha).
	require.NoError(t, s.UpdateIsPublic(ctx, stale.ID, false))

	got, err := s.GetActiveBySourceSkillID(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, active.ID, got.ID, "should return only the active one")
}

func TestMarketplaceStore_GetActiveBySourceSkillID_NoneActive(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))
	require.NoError(t, s.UpdateIsPublic(ctx, mp.ID, false))

	_, err := s.GetActiveBySourceSkillID(ctx, 10)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestMarketplaceStore_UpdateIsPublic_NotFound(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	err := s.UpdateIsPublic(context.Background(), 99999, false)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestMarketplaceStore_UpdateRecommended(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))
	assert.False(t, mp.IsPlatformRecommended)

	require.NoError(t, s.UpdateRecommended(ctx, mp.ID, true))

	got, err := s.GetByID(ctx, mp.ID)
	require.NoError(t, err)
	assert.True(t, got.IsPlatformRecommended)

	require.NoError(t, s.UpdateRecommended(ctx, mp.ID, false))
	got2, _ := s.GetByID(ctx, mp.ID)
	assert.False(t, got2.IsPlatformRecommended)
}

func TestMarketplaceStore_UpdateRecommended_NotFound(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	err := s.UpdateRecommended(context.Background(), 99999, true)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestMarketplaceStore_IncrementSubscribeCount(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))
	assert.Equal(t, uint(0), mp.SubscribeCount)

	require.NoError(t, s.IncrementSubscribeCount(ctx, nil, mp.ID, 1))
	got, _ := s.GetByID(ctx, mp.ID)
	assert.Equal(t, uint(1), got.SubscribeCount)

	require.NoError(t, s.IncrementSubscribeCount(ctx, nil, mp.ID, 3))
	got, _ = s.GetByID(ctx, mp.ID)
	assert.Equal(t, uint(4), got.SubscribeCount)

	require.NoError(t, s.IncrementSubscribeCount(ctx, nil, mp.ID, -2))
	got, _ = s.GetByID(ctx, mp.ID)
	assert.Equal(t, uint(2), got.SubscribeCount)
}

func TestMarketplaceStore_IncrementSubscribeCount_UnderflowGuard(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))
	// current = 0; delta = -1 should be no-op (no underflow).
	require.NoError(t, s.IncrementSubscribeCount(ctx, nil, mp.ID, -1))
	got, _ := s.GetByID(ctx, mp.ID)
	assert.Equal(t, uint(0), got.SubscribeCount, "underflow must be guarded")
}

func TestMarketplaceStore_IncrementSubscribeCount_InTx(t *testing.T) {
	s, db := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))

	err := db.Transaction(func(tx *gorm.DB) error {
		return s.IncrementSubscribeCount(ctx, tx, mp.ID, 5)
	})
	require.NoError(t, err)

	got, _ := s.GetByID(ctx, mp.ID)
	assert.Equal(t, uint(5), got.SubscribeCount)
}

func TestMarketplaceStore_IncrementSubscribeCount_TxRollback(t *testing.T) {
	s, db := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))

	rollbackErr := assert.AnError
	err := db.Transaction(func(tx *gorm.DB) error {
		_ = s.IncrementSubscribeCount(ctx, tx, mp.ID, 5)
		return rollbackErr // force rollback
	})
	assert.ErrorIs(t, err, rollbackErr)

	got, _ := s.GetByID(ctx, mp.ID)
	assert.Equal(t, uint(0), got.SubscribeCount, "rollback must revert increment")
}

func TestMarketplaceStore_List_Pagination(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	// Insert 5 marketplaces.
	for i := 0; i < 5; i++ {
		require.NoError(t, s.Create(ctx, sampleMarketplace("Item-"+string(rune('A'+i)))))
	}

	// page 1 (size 2)
	items, total, err := s.List(ctx, ListOptions{Sort: "recent", Offset: 0, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Len(t, items, 2)

	// page 3 (size 2) — only 1 item left
	items, total, err = s.List(ctx, ListOptions{Sort: "recent", Offset: 4, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Len(t, items, 1)
}

func TestMarketplaceStore_List_FiltersIsPublic(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	pub := sampleMarketplace("Public-1")
	require.NoError(t, s.Create(ctx, pub))

	priv := sampleMarketplace("Private-1")
	require.NoError(t, s.Create(ctx, priv))
	require.NoError(t, s.UpdateIsPublic(ctx, priv.ID, false))

	items, total, err := s.List(ctx, ListOptions{Sort: "recent", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, pub.ID, items[0].ID)
}

func TestMarketplaceStore_List_IncludeUnpublishedForPublisher(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	pub := sampleMarketplace("Public-1")
	require.NoError(t, s.Create(ctx, pub))

	priv := sampleMarketplace("Private-1")
	require.NoError(t, s.Create(ctx, priv))
	require.NoError(t, s.UpdateIsPublic(ctx, priv.ID, false))

	items, total, err := s.List(ctx, ListOptions{
		PublisherUserID:    1,
		IncludeUnpublished: true,
		Sort:               "recent",
		Limit:              10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, items, 2)
}

func TestMarketplaceStore_List_FULLTEXT_SkippedOnSQLite(t *testing.T) {
	t.Skip("FULLTEXT MATCH AGAINST requires MySQL; verify in dev integration test")
}

func TestMarketplaceStore_List_RecommendedSortFirst(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	plain := sampleMarketplace("Plain")
	require.NoError(t, s.Create(ctx, plain))

	recommended := sampleMarketplace("Recommended")
	require.NoError(t, s.Create(ctx, recommended))
	require.NoError(t, s.UpdateRecommended(ctx, recommended.ID, true))

	items, _, err := s.List(ctx, ListOptions{Sort: "recommended", Limit: 10})
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, recommended.ID, items[0].ID, "platform-recommended must rank first")
}

func TestMarketplaceStore_CreateSubscription_And_GetSubscription(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))

	sub := &model.SkillSubscription{
		SubscriberUserID: 2,
		MarketplaceID:    mp.ID,
		ClonedSkillID:    777,
	}
	require.NoError(t, s.CreateSubscription(ctx, nil, sub))
	require.NotZero(t, sub.ID)

	got, err := s.GetSubscription(ctx, 2, mp.ID)
	require.NoError(t, err)
	assert.Equal(t, sub.ID, got.ID)
	assert.Equal(t, uint(777), got.ClonedSkillID)
}

func TestMarketplaceStore_CreateSubscription_DuplicateBlocked(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))

	first := &model.SkillSubscription{SubscriberUserID: 2, MarketplaceID: mp.ID, ClonedSkillID: 777}
	require.NoError(t, s.CreateSubscription(ctx, nil, first))

	dup := &model.SkillSubscription{SubscriberUserID: 2, MarketplaceID: mp.ID, ClonedSkillID: 778}
	err := s.CreateSubscription(ctx, nil, dup)
	assert.Error(t, err, "UNIQUE(subscriber_user_id, marketplace_id) must block dup")
}

func TestMarketplaceStore_GetSubscription_NotFound(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	_, err := s.GetSubscription(context.Background(), 99, 99)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestMarketplaceStore_DeleteSubscription_Idempotent(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))

	sub := &model.SkillSubscription{SubscriberUserID: 2, MarketplaceID: mp.ID, ClonedSkillID: 777}
	require.NoError(t, s.CreateSubscription(ctx, nil, sub))

	require.NoError(t, s.DeleteSubscription(ctx, nil, 2, mp.ID))
	_, err := s.GetSubscription(ctx, 2, mp.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// 第二次 delete 不报错（idempotent）
	require.NoError(t, s.DeleteSubscription(ctx, nil, 2, mp.ID))
}

func TestMarketplaceStore_DeleteSubscription_InTx(t *testing.T) {
	s, db := newTestMarketplaceStore(t)
	ctx := context.Background()

	mp := sampleMarketplace("Item-A")
	require.NoError(t, s.Create(ctx, mp))

	sub := &model.SkillSubscription{SubscriberUserID: 2, MarketplaceID: mp.ID, ClonedSkillID: 777}
	require.NoError(t, s.CreateSubscription(ctx, nil, sub))

	err := db.Transaction(func(tx *gorm.DB) error {
		return s.DeleteSubscription(ctx, tx, 2, mp.ID)
	})
	require.NoError(t, err)

	_, err = s.GetSubscription(ctx, 2, mp.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestMarketplaceStore_ListMySubscriptions_CrossTenantIsolation(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	mpA := sampleMarketplace("MP-A")
	require.NoError(t, s.Create(ctx, mpA))
	mpB := sampleMarketplace("MP-B")
	require.NoError(t, s.Create(ctx, mpB))

	// userA subscribes mpA; userB subscribes mpB.
	require.NoError(t, s.CreateSubscription(ctx, nil, &model.SkillSubscription{
		SubscriberUserID: 100, MarketplaceID: mpA.ID, ClonedSkillID: 1000,
	}))
	require.NoError(t, s.CreateSubscription(ctx, nil, &model.SkillSubscription{
		SubscriberUserID: 200, MarketplaceID: mpB.ID, ClonedSkillID: 2000,
	}))

	// userA must see only their own subscription.
	itemsA, totalA, err := s.ListMySubscriptions(ctx, 100, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), totalA)
	require.Len(t, itemsA, 1)
	assert.Equal(t, uint(1000), itemsA[0].Subscription.ClonedSkillID)
	assert.Equal(t, "MP-A", itemsA[0].Marketplace.Name, "JOIN should hydrate marketplace")

	// userB sees only their own.
	itemsB, totalB, err := s.ListMySubscriptions(ctx, 200, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), totalB)
	require.Len(t, itemsB, 1)
	assert.Equal(t, "MP-B", itemsB[0].Marketplace.Name)

	// userC (no subscriptions) sees empty.
	itemsC, totalC, err := s.ListMySubscriptions(ctx, 999, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(0), totalC)
	assert.Empty(t, itemsC)
}

func TestMarketplaceStore_ListMySubscriptions_Pagination(t *testing.T) {
	s, _ := newTestMarketplaceStore(t)
	ctx := context.Background()

	// Create 4 marketplaces + 4 subscriptions for user 100.
	for i := 0; i < 4; i++ {
		mp := sampleMarketplace("Item-" + string(rune('A'+i)))
		require.NoError(t, s.Create(ctx, mp))
		require.NoError(t, s.CreateSubscription(ctx, nil, &model.SkillSubscription{
			SubscriberUserID: 100, MarketplaceID: mp.ID, ClonedSkillID: uint(1000 + i),
		}))
	}

	items, total, err := s.ListMySubscriptions(ctx, 100, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(4), total)
	assert.Len(t, items, 2)

	items2, _, err := s.ListMySubscriptions(ctx, 100, 2, 2)
	require.NoError(t, err)
	assert.Len(t, items2, 2)

	items3, _, err := s.ListMySubscriptions(ctx, 100, 4, 2)
	require.NoError(t, err)
	assert.Empty(t, items3)
}
