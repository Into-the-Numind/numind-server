package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// newTestUGMStore 构建测试用 IUserGlobalMemoryStore（SQLite WAL 文件 DB）。
func newTestUGMStore(t *testing.T) IUserGlobalMemoryStore {
	t.Helper()
	db := newTestDB(t, &model.UserGlobalMemory{})
	return NewUserGlobalMemoryStore(db)
}

// sampleUGM 构建一个 UserGlobalMemory（confidence=1.0 默认）。
func sampleUGM(userID uint, key, value, kind string) *model.UserGlobalMemory {
	return &model.UserGlobalMemory{
		UserID:     userID,
		Kind:       kind,
		KeyName:    key,
		Value:      value,
		Confidence: 1.0,
		SourceType: "agent_tool",
	}
}

// ─── Upsert happy path ────────────────────────────────────────────────────────

// TestStore_UserGlobalMemory_Upsert_Insert 验证首次 Upsert 写入行。
func TestStore_UserGlobalMemory_Upsert_Insert(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	m := sampleUGM(1, "pref_lang", "Chinese", "preference")
	require.NoError(t, s.Upsert(ctx, m))
	require.NotZero(t, m.ID)

	got, err := s.GetByUserKey(ctx, 1, "pref_lang")
	require.NoError(t, err)
	assert.Equal(t, "Chinese", got.Value)
	assert.Equal(t, "preference", got.Kind)
}

// TestStore_UserGlobalMemory_Upsert_Update 验证同 (user_id, key_name) 重复 Upsert 后只有 1 行且值已更新。
func TestStore_UserGlobalMemory_Upsert_Update(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "key1", "v1", "fact")))
	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "key1", "v2", "fact")))

	items, err := s.ListByUserKind(ctx, 1, "fact", 50)
	require.NoError(t, err)
	require.Len(t, items, 1, "Upsert same key must not create duplicate rows")
	assert.Equal(t, "v2", items[0].Value, "value must be updated on conflict")
}

// TestStore_UserGlobalMemory_Upsert_KindUpdate 验证 kind 字段在冲突更新中也能被更新。
func TestStore_UserGlobalMemory_Upsert_KindUpdate(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "mykey", "val", "fact")))

	m2 := sampleUGM(1, "mykey", "val-updated", "learning")
	require.NoError(t, s.Upsert(ctx, m2))

	got, err := s.GetByUserKey(ctx, 1, "mykey")
	require.NoError(t, err)
	assert.Equal(t, "learning", got.Kind)
	assert.Equal(t, "val-updated", got.Value)
}

// ─── Upsert 并发 100 goroutine ────────────────────────────────────────────────

// TestStore_UserGlobalMemory_Upsert_Concurrent100 验证 100 goroutine 同 (user, key) Upsert 后只有 1 行。
// 需要 SQLite WAL + busy_timeout 避免 SQLITE_BUSY 错误。
func TestStore_UserGlobalMemory_Upsert_Concurrent100(t *testing.T) {
	// 使用 WAL 文件 DB（newTestDB 已配置 _journal_mode=WAL&_busy_timeout=5000）
	db := newTestDB(t, &model.UserGlobalMemory{})
	s := NewUserGlobalMemoryStore(db)
	ctx := context.Background()

	const goroutines = 100
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m := &model.UserGlobalMemory{
				UserID:     42,
				Kind:       "fact",
				KeyName:    "concurrent_key",
				Value:      fmt.Sprintf("value_%d", idx),
				Confidence: 1.0,
				SourceType: "agent_tool",
			}
			errs[idx] = s.Upsert(ctx, m)
		}(i)
	}
	wg.Wait()

	// 允许部分 SQLITE_BUSY 错误（WAL 并发上限），但最终行数必须为 1
	var successCount int
	for _, err := range errs {
		if err == nil {
			successCount++
		}
	}
	assert.Greater(t, successCount, 0, "at least some upserts must succeed")

	items, err := s.ListByUserKind(ctx, 42, "fact", 50)
	require.NoError(t, err)
	assert.Len(t, items, 1, "concurrent upsert on same key must result in exactly 1 row")
}

// ─── GetByUserKey ─────────────────────────────────────────────────────────────

// TestStore_UserGlobalMemory_GetByUserKey_Hit 验证命中路径。
func TestStore_UserGlobalMemory_GetByUserKey_Hit(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "mykey", "myval", "preference")))

	got, err := s.GetByUserKey(ctx, 1, "mykey")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "myval", got.Value)
}

// TestStore_UserGlobalMemory_GetByUserKey_Miss 验证未命中返回 gorm.ErrRecordNotFound（包装后）。
func TestStore_UserGlobalMemory_GetByUserKey_Miss(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	_, err := s.GetByUserKey(ctx, 1, "nonexistent")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "must wrap gorm.ErrRecordNotFound")
}

// ─── ListByUserKind ───────────────────────────────────────────────────────────

// TestStore_UserGlobalMemory_ListByUserKind_Filter 验证 kind 过滤 + limit 生效。
func TestStore_UserGlobalMemory_ListByUserKind_Filter(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "k1", "v1", "fact")))
	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "k2", "v2", "fact")))
	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "k3", "v3", "preference")))

	facts, err := s.ListByUserKind(ctx, 1, "fact", 10)
	require.NoError(t, err)
	assert.Len(t, facts, 2)

	prefs, err := s.ListByUserKind(ctx, 1, "preference", 10)
	require.NoError(t, err)
	assert.Len(t, prefs, 1)
}

// TestStore_UserGlobalMemory_ListByUserKind_Limit 验证 limit 截断。
func TestStore_UserGlobalMemory_ListByUserKind_Limit(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, s.Upsert(ctx, sampleUGM(1, fmt.Sprintf("key%d", i), "val", "fact")))
	}

	limited, err := s.ListByUserKind(ctx, 1, "fact", 3)
	require.NoError(t, err)
	assert.Len(t, limited, 3)
}

// ─── DeleteByUserKey ──────────────────────────────────────────────────────────

// TestStore_UserGlobalMemory_DeleteByUserKey 验证只删对应 key。
func TestStore_UserGlobalMemory_DeleteByUserKey(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "key_to_delete", "v1", "fact")))
	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "key_to_keep", "v2", "fact")))

	require.NoError(t, s.DeleteByUserKey(ctx, 1, "key_to_delete"))

	_, err := s.GetByUserKey(ctx, 1, "key_to_delete")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))

	kept, err := s.GetByUserKey(ctx, 1, "key_to_keep")
	require.NoError(t, err)
	assert.Equal(t, "v2", kept.Value)
}

// ─── DeleteByUser ─────────────────────────────────────────────────────────────

// TestStore_UserGlobalMemory_DeleteByUser 验证 DeleteByUser 删该 user 全部、不影响其他 user。
func TestStore_UserGlobalMemory_DeleteByUser(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "u1k1", "v", "fact")))
	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "u1k2", "v", "fact")))
	require.NoError(t, s.Upsert(ctx, sampleUGM(2, "u2k1", "v", "fact")))

	require.NoError(t, s.DeleteByUser(ctx, 1))

	u1Items, err := s.ListByUserKind(ctx, 1, "fact", 50)
	require.NoError(t, err)
	assert.Empty(t, u1Items, "user 1 memories must all be deleted")

	u2Items, err := s.ListByUserKind(ctx, 2, "fact", 50)
	require.NoError(t, err)
	assert.Len(t, u2Items, 1, "user 2 memories must be untouched")
}

// ─── 跨 user 隔离 ─────────────────────────────────────────────────────────────

// TestStore_UserGlobalMemory_CrossUserIsolation 验证 GetByUserKey + ListByUserKind 不跨 user 泄漏。
func TestStore_UserGlobalMemory_CrossUserIsolation(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "shared_key", "user1_val", "preference")))

	// user 2 GetByUserKey 同名 key → miss
	_, err := s.GetByUserKey(ctx, 2, "shared_key")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "user_B must not see user_A's key")

	// user 2 ListByUserKind → empty
	items, err := s.ListByUserKind(ctx, 2, "preference", 10)
	require.NoError(t, err)
	assert.Empty(t, items, "user_B must not see user_A's kind items")
}

// ─── Different keys same user ──────────────────────────────────────────────────

// TestStore_UserGlobalMemory_DifferentKeys_NoConflict 验证同 user 不同 key 各自独立存在。
func TestStore_UserGlobalMemory_DifferentKeys_NoConflict(t *testing.T) {
	s := newTestUGMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "key_a", "val_a", "fact")))
	require.NoError(t, s.Upsert(ctx, sampleUGM(1, "key_b", "val_b", "fact")))

	items, err := s.ListByUserKind(ctx, 1, "fact", 50)
	require.NoError(t, err)
	assert.Len(t, items, 2, "different keys for same user must not conflict")
}
