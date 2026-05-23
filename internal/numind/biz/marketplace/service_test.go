package marketplace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/skill/artifact"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---- fakes ----

// fakeUserStore is a minimal in-memory UserStore for tests.
// Only GetByID is consulted by marketplace.service.verifyParent.
type fakeUserStore struct {
	users map[uint]*model.User
}

func newFakeUserStore() *fakeUserStore { return &fakeUserStore{users: map[uint]*model.User{}} }

func (f *fakeUserStore) addParent(id uint) {
	u := &model.User{ParentUserID: nil}
	u.ID = id
	f.users[id] = u
}
func (f *fakeUserStore) addChild(id uint, parentID uint) {
	pid := parentID
	u := &model.User{ParentUserID: &pid}
	u.ID = id
	f.users[id] = u
}

// Implement store.UserStore — only GetByID matters; rest are stubs.
func (f *fakeUserStore) Create(ctx context.Context, user *model.User) error { return nil }
func (f *fakeUserStore) Get(ctx context.Context, username string) (*model.User, error) {
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeUserStore) GetByID(ctx context.Context, userID uint) (*model.User, error) {
	if u, ok := f.users[userID]; ok {
		return u, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeUserStore) Update(ctx context.Context, user *model.User) error { return nil }
func (f *fakeUserStore) List(ctx context.Context, offset, limit int) (int64, []*model.User, error) {
	return 0, nil, nil
}
func (f *fakeUserStore) Delete(ctx context.Context, username string) error { return nil }
func (f *fakeUserStore) GetUser(userID uint) (*model.User, error) {
	return f.GetByID(context.Background(), userID)
}
func (f *fakeUserStore) GetUserByID(ctx context.Context, userID uint) (*model.User, error) {
	return f.GetByID(ctx, userID)
}
func (f *fakeUserStore) UpdateUser(ctx context.Context, user *model.User) error { return nil }

// fakeArtifactSvc is a minimal ArtifactService for tests.
type artifactCreateCall struct {
	ParentUserID uint
	CreatedBy    uint
	Req          artifact.CreateRequest
}
type fakeArtifactSvc struct {
	nextID         uint
	skills         map[uint]*model.Skill
	createCalls    []artifactCreateCall
	deleteCalls    []uint
	failNextCreate bool
	failNextDelete bool
}

func newFakeArtifactSvc() *fakeArtifactSvc {
	return &fakeArtifactSvc{nextID: 100, skills: map[uint]*model.Skill{}}
}

func (f *fakeArtifactSvc) Create(ctx context.Context, parentUserID, createdBy uint, req artifact.CreateRequest) (*model.Skill, error) {
	f.createCalls = append(f.createCalls, artifactCreateCall{ParentUserID: parentUserID, CreatedBy: createdBy, Req: req})
	if f.failNextCreate {
		f.failNextCreate = false
		return nil, errors.New("fakeArtifactSvc: forced create failure")
	}
	f.nextID++
	sk := &model.Skill{
		ParentUserID: parentUserID,
		Name:         req.Name,
		Description:  req.Description,
		WhenToUse:    req.WhenToUse,
		BodyMd:       req.BodyMd,
		SourceType:   req.SourceType,
		IsActive:     true,
	}
	sk.ID = f.nextID
	f.skills[sk.ID] = sk
	return sk, nil
}

func (f *fakeArtifactSvc) Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error) {
	sk, ok := f.skills[skillID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	if sk.ParentUserID != parentUserID {
		// #1's Service.Get enforces tenant filter — replicate.
		return nil, gorm.ErrRecordNotFound
	}
	return sk, nil
}

func (f *fakeArtifactSvc) Delete(ctx context.Context, parentUserID, skillID uint) (int64, error) {
	f.deleteCalls = append(f.deleteCalls, skillID)
	if f.failNextDelete {
		f.failNextDelete = false
		return 0, errors.New("fakeArtifactSvc: forced delete failure")
	}
	if sk, ok := f.skills[skillID]; ok && sk.ParentUserID == parentUserID {
		sk.IsActive = false
		return 0, nil
	}
	return 0, gorm.ErrRecordNotFound
}

// ---- helpers ----

// newTestService wires a real SQLite-backed marketplace store + fake user/artifact.
// Pre-seeds two parent accounts (uid=1, uid=2) and one child (uid=3, parent=1).
func newTestService(t *testing.T) (Service, *fakeArtifactSvc, *fakeUserStore, store.IMarketplaceStore, *gorm.DB) {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SkillMarketplace{}, &model.SkillSubscription{}, &model.AgentSkillBinding{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	mpStore := store.NewMarketplaceStore(db)
	users := newFakeUserStore()
	users.addParent(1)
	users.addParent(2)
	users.addChild(3, 1)

	art := newFakeArtifactSvc()
	svc := NewService(mpStore, art, users, db)
	return svc, art, users, mpStore, db
}

// seedSkillToFake adds a skill owned by parentUserID to fake artifact svc.
func seedSkill(t *testing.T, art *fakeArtifactSvc, id, parentUserID uint, body string) *model.Skill {
	t.Helper()
	sk := &model.Skill{
		ParentUserID: parentUserID,
		Name:         "Seed Skill",
		Description:  "seed description",
		WhenToUse:    "seed when",
		BodyMd:       body,
		IsActive:     true,
	}
	sk.ID = id
	art.skills[id] = sk
	if id > art.nextID {
		art.nextID = id
	}
	return sk
}

// realStubChat installs a sanitize.go chatFn that always returns "脱敏后正文" + a fixed-format
// promptFn (so all sanitize calls in service tests get a deterministic LLM output without hitting
// a real provider). Use this in tests where the sanitize-pipeline content isn't under test.
func realStubChat(t *testing.T) {
	t.Helper()
	// Hook is in sanitize.go's chatFn — see sanitize_test.go for the stubChat helper signature.
	stub, _ := stubChat(t, "脱敏后正文", 100, 50)
	withChatFn(t, stub)
	withPromptFn(t, func(name, fallback string) (string, int) { return "%s", 0 })
}

// ---- tests: SanitizePreview ----

func TestSanitizePreview_HappyPath(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "raw body with admin@example.com")
	realStubChat(t)

	out, err := svc.SanitizePreview(context.Background(), 1, 10)
	require.NoError(t, err)
	assert.Equal(t, "脱敏后正文", out)
}

func TestSanitizePreview_ChildBlocked(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)

	_, err := svc.SanitizePreview(context.Background(), 3, 10)
	assert.ErrorIs(t, err, ErrChildAccountCannotAccessMarketplace)
}

func TestSanitizePreview_SkillNotOwned(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body") // owned by user 1
	realStubChat(t)

	// user 2 tries to preview user 1's skill
	_, err := svc.SanitizePreview(context.Background(), 2, 10)
	require.Error(t, err, "must not silently use cross-tenant skill")
}

func TestSanitizePreview_EmptyBody(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "") // empty body
	realStubChat(t)

	_, err := svc.SanitizePreview(context.Background(), 1, 10)
	assert.ErrorIs(t, err, ErrSkillBodyEmpty)
}

// ---- tests: Publish ----

func TestPublish_HappyPath(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "raw body content")
	realStubChat(t)

	mp, err := svc.Publish(context.Background(), 1, PublishRequest{
		SkillID:                  10,
		CategoryTags:             []string{"销售"},
		ConfirmedSanitizedBodyMD: "脱敏后正文",
	})
	require.NoError(t, err)
	require.NotNil(t, mp)
	assert.NotZero(t, mp.ID)
	assert.Equal(t, uint(1), mp.PublisherUserID)
	assert.Equal(t, uint(10), mp.SourceSkillID)
	assert.True(t, mp.IsPublic)
	assert.Equal(t, "脱敏后正文", mp.SanitizedBodyMD)
}

func TestPublish_ChildBlocked(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 3, "body")
	realStubChat(t)

	_, err := svc.Publish(context.Background(), 3, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "x"})
	assert.ErrorIs(t, err, ErrChildAccountCannotAccessMarketplace)
}

func TestPublish_SkillNotOwned(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body") // owned by 1
	realStubChat(t)

	_, err := svc.Publish(context.Background(), 2, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "x"})
	require.Error(t, err, "cross-tenant must be blocked")
}

func TestPublish_AlreadyPublished(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "raw")
	realStubChat(t)
	ctx := context.Background()

	_, err := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"销售"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, err)

	_, err = svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"销售"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	assert.ErrorIs(t, err, ErrSkillAlreadyPublished)
}

func TestPublish_ConfirmationMismatch(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "raw")
	realStubChat(t) // returns "脱敏后正文"

	// User echoes back something totally different (>5% char delta).
	_, err := svc.Publish(context.Background(), 1, PublishRequest{
		SkillID:                  10,
		CategoryTags:             []string{"x"},
		ConfirmedSanitizedBodyMD: "完全不同的内容这里是用户篡改后的字符串非常长的内容用来超过 5% 的字符差异阈值",
	})
	assert.ErrorIs(t, err, ErrSanitizeConfirmationMismatch)
}

func TestPublish_EmptyBody(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "")
	realStubChat(t)

	_, err := svc.Publish(context.Background(), 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: ""})
	assert.ErrorIs(t, err, ErrSkillBodyEmpty)
}

// ---- tests: Unpublish ----

func TestUnpublish_HappyPath(t *testing.T) {
	svc, art, _, mpStore, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()

	mp, err := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, err)

	require.NoError(t, svc.Unpublish(ctx, 1, mp.ID))

	got, err := mpStore.GetByID(ctx, mp.ID)
	require.NoError(t, err)
	assert.False(t, got.IsPublic)
}

func TestUnpublish_ChildBlocked(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	err := svc.Unpublish(context.Background(), 3, 999)
	assert.ErrorIs(t, err, ErrChildAccountCannotAccessMarketplace)
}

func TestUnpublish_NotOwned(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()

	mp, err := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, err)

	// user 2 cannot unpublish user 1's marketplace item
	err = svc.Unpublish(ctx, 2, mp.ID)
	assert.ErrorIs(t, err, ErrSkillNotOwned)
}

func TestUnpublish_NotFound(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	err := svc.Unpublish(context.Background(), 1, 99999)
	assert.ErrorIs(t, err, ErrMarketplaceNotFound)
}

// ---- tests: List / Get ----

func TestList_DefaultsAndPagination(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body-a")
	seedSkill(t, art, 11, 1, "body-b")
	realStubChat(t)
	ctx := context.Background()

	_, err := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, err)
	_, err = svc.Publish(ctx, 1, PublishRequest{SkillID: 11, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, err)

	items, total, err := svc.List(ctx, BrowseQuery{Page: 1, PageSize: 10, Sort: "recent"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, items, 2)

	// page_size 0 → default 20
	_, _, err = svc.List(ctx, BrowseQuery{Sort: "recent"})
	require.NoError(t, err)

	// page_size > 100 capped
	_, _, err = svc.List(ctx, BrowseQuery{Page: 1, PageSize: 999, Sort: "recent"})
	require.NoError(t, err)
}

func TestGet_Public(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	got, err := svc.Get(ctx, mp.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, mp.ID, got.ID)
}

func TestGet_UnpublishedVisibleToPublisherOnly(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, svc.Unpublish(ctx, 1, mp.ID))

	// publisher (user 1) still sees it
	got, err := svc.Get(ctx, mp.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, mp.ID, got.ID)

	// non-publisher (user 2) gets 404
	_, err = svc.Get(ctx, mp.ID, 2)
	assert.ErrorIs(t, err, ErrMarketplaceNotFound)
}

func TestGet_NotFound(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	_, err := svc.Get(context.Background(), 99999, 1)
	assert.ErrorIs(t, err, ErrMarketplaceNotFound)
}

// ---- tests: Subscribe ----

func TestSubscribe_HappyPath_WritesToSubscriberTenant(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	// user 2 subscribes
	clonedID, err := svc.Subscribe(ctx, 2, mp.ID)
	require.NoError(t, err)
	require.NotZero(t, clonedID)

	// Verify cloned skill was created with parent_user_id=2 (cross-tenant rule 7)
	require.NotEmpty(t, art.createCalls)
	last := art.createCalls[len(art.createCalls)-1]
	assert.Equal(t, uint(2), last.ParentUserID, "cloned skill must belong to subscriber (rule 7)")
	assert.Equal(t, uint(2), last.CreatedBy)
	assert.Equal(t, "imported_from_marketplace", last.Req.SourceType)
	assert.Contains(t, last.Req.Description, "订阅自市场")

	// Verify subscribe_count was incremented in DB
	var refreshed model.SkillMarketplace
	require.NoError(t, db.First(&refreshed, mp.ID).Error)
	assert.Equal(t, uint(1), refreshed.SubscribeCount)
}

func TestSubscribe_ChildBlocked(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	_, err := svc.Subscribe(ctx, 3, mp.ID)
	assert.ErrorIs(t, err, ErrChildAccountCannotAccessMarketplace)
}

func TestSubscribe_SelfSubscribeForbidden(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	_, err := svc.Subscribe(ctx, 1, mp.ID) // user 1 = publisher
	assert.ErrorIs(t, err, ErrSelfSubscribeForbidden)
}

func TestSubscribe_AlreadySubscribed(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	_, err := svc.Subscribe(ctx, 2, mp.ID)
	require.NoError(t, err)

	_, err = svc.Subscribe(ctx, 2, mp.ID)
	assert.ErrorIs(t, err, ErrAlreadySubscribed)
}

func TestSubscribe_UnpublishedNotSubscribable(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	require.NoError(t, svc.Unpublish(ctx, 1, mp.ID))

	_, err := svc.Subscribe(ctx, 2, mp.ID)
	assert.ErrorIs(t, err, ErrMarketplaceNotFound, "unpublished should appear as not-found to subscribers")
}

func TestSubscribe_Phase1FailureNoOrphan(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	art.failNextCreate = true
	_, err := svc.Subscribe(ctx, 2, mp.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clone skill")

	// No subscription written.
	var count int64
	require.NoError(t, db.Model(&model.SkillSubscription{}).Where("subscriber_user_id = ?", 2).Count(&count).Error)
	assert.Equal(t, int64(0), count)

	// subscribe_count not incremented.
	var refreshed model.SkillMarketplace
	require.NoError(t, db.First(&refreshed, mp.ID).Error)
	assert.Equal(t, uint(0), refreshed.SubscribeCount)
}

// ---- tests: Unsubscribe ----

func TestUnsubscribe_HappyPath(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	clonedID, _ := svc.Subscribe(ctx, 2, mp.ID)

	require.NoError(t, svc.Unsubscribe(ctx, 2, mp.ID))

	// cloned skill soft-deleted by fakeArtifactSvc.Delete
	assert.Contains(t, art.deleteCalls, clonedID)
	assert.False(t, art.skills[clonedID].IsActive)

	// subscription row removed
	var count int64
	require.NoError(t, db.Model(&model.SkillSubscription{}).Where("subscriber_user_id = ?", 2).Count(&count).Error)
	assert.Equal(t, int64(0), count)

	// subscribe_count back to 0
	var refreshed model.SkillMarketplace
	require.NoError(t, db.First(&refreshed, mp.ID).Error)
	assert.Equal(t, uint(0), refreshed.SubscribeCount)
}

func TestUnsubscribe_NotFound(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	err := svc.Unsubscribe(context.Background(), 2, 99999)
	assert.ErrorIs(t, err, ErrSubscriptionNotFound)
}

func TestUnsubscribe_ChildBlocked(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	err := svc.Unsubscribe(context.Background(), 3, 999)
	assert.ErrorIs(t, err, ErrChildAccountCannotAccessMarketplace)
}

func TestUnsubscribe_NotOwnedByCaller(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	_, _ = svc.Subscribe(ctx, 2, mp.ID)

	// user 1 (publisher, not subscriber) cannot unsubscribe user 2's subscription
	err := svc.Unsubscribe(ctx, 1, mp.ID)
	assert.ErrorIs(t, err, ErrSubscriptionNotFound) // appears as 404 to non-owner
}

// ---- tests: ListMySubscriptions ----

func TestListMySubscriptions_EmptyAndPopulated(t *testing.T) {
	svc, art, _, _, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()

	items, total, err := svc.ListMySubscriptions(ctx, 2, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, items)

	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	_, _ = svc.Subscribe(ctx, 2, mp.ID)

	items, total, err = svc.ListMySubscriptions(ctx, 2, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, mp.Name, items[0].Marketplace.Name)
	assert.Equal(t, 0, items[0].AgentCount, "no agent_skill_binding inserted → AgentCount=0")
}

func TestListMySubscriptions_AgentCountHydrated(t *testing.T) {
	svc, art, _, _, db := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})
	clonedID, _ := svc.Subscribe(ctx, 2, mp.ID)

	// Insert 2 agent_skill_binding rows for cloned skill — simulate user 2
	// loaded the cloned skill into 2 agents.
	now := time.Now()
	require.NoError(t, db.Create(&model.AgentSkillBinding{AgentID: 100, SkillID: clonedID, IsActive: true, BoundAt: now}).Error)
	require.NoError(t, db.Create(&model.AgentSkillBinding{AgentID: 101, SkillID: clonedID, IsActive: true, BoundAt: now}).Error)
	// And one inactive binding (must not count).
	// database.md §6 default:true bool gotcha + GORM-SQLite interaction: even Select("*")
	// can't force is_active=false through the GORM layer reliably in tests. Use raw SQL
	// to bypass entirely — fully deterministic.
	require.NoError(t, db.Exec(
		`INSERT INTO agent_skill_binding (agent_id, skill_id, sort_order, is_active, bound_at) VALUES (?, ?, 0, 0, ?)`,
		102, clonedID, now,
	).Error)

	items, _, err := svc.ListMySubscriptions(ctx, 2, 0, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, 2, items[0].AgentCount, "only active bindings count")
}

func TestListMySubscriptions_ChildBlocked(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	_, _, err := svc.ListMySubscriptions(context.Background(), 3, 0, 10)
	assert.ErrorIs(t, err, ErrChildAccountCannotAccessMarketplace)
}

// ---- tests: SetRecommended (admin) ----

func TestSetRecommended_TogglesAndPersists(t *testing.T) {
	svc, art, _, mpStore, _ := newTestService(t)
	seedSkill(t, art, 10, 1, "body")
	realStubChat(t)
	ctx := context.Background()
	mp, _ := svc.Publish(ctx, 1, PublishRequest{SkillID: 10, CategoryTags: []string{"x"}, ConfirmedSanitizedBodyMD: "脱敏后正文"})

	require.NoError(t, svc.SetRecommended(ctx, mp.ID, true))
	got, _ := mpStore.GetByID(ctx, mp.ID)
	assert.True(t, got.IsPlatformRecommended)

	require.NoError(t, svc.SetRecommended(ctx, mp.ID, false))
	got2, _ := mpStore.GetByID(ctx, mp.ID)
	assert.False(t, got2.IsPlatformRecommended)
}

func TestSetRecommended_NotFound(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	err := svc.SetRecommended(context.Background(), 99999, true)
	assert.ErrorIs(t, err, ErrMarketplaceNotFound)
}

// ---- helpers used in normalize/delta test ----

func TestNormalizeForCompare_CollapsesWhitespace(t *testing.T) {
	a := "Hello\n\nWorld   foo"
	b := "hello world foo"
	assert.Equal(t, b, normalizeForCompare(a))
}

func TestCharDelta_ZeroForIdentical(t *testing.T) {
	assert.Equal(t, 0.0, charDelta("abc", "abc"))
}

func TestCharDelta_FractionWhenDifferentLength(t *testing.T) {
	// "abcdefghij" (10) vs "abc" (3) → delta = 7/10 = 0.7
	assert.InDelta(t, 0.7, charDelta(strings.Repeat("a", 10), "abc"), 0.001)
}
