package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// newTestASMStore 构建测试用 IAgentSessionMemoryStore（SQLite WAL 文件 DB）。
func newTestASMStore(t *testing.T) IAgentSessionMemoryStore {
	t.Helper()
	db := newTestDB(t, &model.AgentSessionMemory{})
	return NewAgentSessionMemoryStore(db)
}

// sampleASM 构建一条最小 L1 记忆（score=1.0 默认）。
func sampleASM(userID uint, agentDefID uint64, kind, content string) *model.AgentSessionMemory {
	return &model.AgentSessionMemory{
		UserID:            userID,
		AgentDefinitionID: agentDefID,
		Kind:              kind,
		Content:           content,
		Score:             1.0,
		SourceType:        "agent",
		RecencyAt:         time.Now(),
	}
}

// ─── Create + ListByUserAgent ──────────────────────────────────────────────────

// TestStore_AgentSessionMemory_Create_persists 验证 Create 后能 List 到。
func TestStore_AgentSessionMemory_Create_persists(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	m := sampleASM(1, 100, "fact", "hello world")
	require.NoError(t, s.Create(ctx, m))
	require.NotZero(t, m.ID)

	items, err := s.ListByUserAgent(ctx, 1, 100, ListOpts{})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "hello world", items[0].Content)
}

// NOTE: score=0 / confidence=0 SQLite boundary tests are in M2 (internal/pkg/model/*_test.go).
// M3 store layer does not re-test this — per spec §8.3 P2-4 decision.

// TestStore_AgentSessionMemory_List_KindFilter 验证 Kind 过滤。
func TestStore_AgentSessionMemory_List_KindFilter(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "fact", "a fact")))
	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "preference", "a pref")))

	items, err := s.ListByUserAgent(ctx, 1, 100, ListOpts{Kind: "fact"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "fact", items[0].Kind)
}

// TestStore_AgentSessionMemory_CrossUserIsolation 验证跨 user 隔离：
// userA 写 → userB List → 0 行。
func TestStore_AgentSessionMemory_CrossUserIsolation(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "fact", "user A memory")))

	items, err := s.ListByUserAgent(ctx, 2, 100, ListOpts{})
	require.NoError(t, err)
	assert.Empty(t, items, "user_B must not see user_A's memories")
}

// ─── AliveOnly TTL 过滤 ────────────────────────────────────────────────────────

// TestStore_AgentSessionMemory_AliveOnly_ExpiresAtFilter 验证 expires_at 过期行被 AliveOnly 过滤。
func TestStore_AgentSessionMemory_AliveOnly_ExpiresAtFilter(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	// alive 行
	mAlive := sampleASM(1, 100, "fact", "alive")
	futureTime := time.Now().Add(24 * time.Hour)
	mAlive.ExpiresAt = &futureTime
	require.NoError(t, s.Create(ctx, mAlive))

	// expired 行
	mExpired := sampleASM(1, 100, "fact", "expired")
	pastTime := time.Now().Add(-1 * time.Hour)
	mExpired.ExpiresAt = &pastTime
	require.NoError(t, s.Create(ctx, mExpired))

	// 无过滤：2 行
	all, err := s.ListByUserAgent(ctx, 1, 100, ListOpts{AliveOnly: false})
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// AliveOnly=true：仅 1 行（alive）
	alive, err := s.ListByUserAgent(ctx, 1, 100, ListOpts{AliveOnly: true})
	require.NoError(t, err)
	require.Len(t, alive, 1)
	assert.Equal(t, "alive", alive[0].Content)
}

// TestStore_AgentSessionMemory_AliveOnly_NullExpiresAt 验证 expires_at=NULL 的行在 AliveOnly=true 时仍返回。
func TestStore_AgentSessionMemory_AliveOnly_NullExpiresAt(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	m := sampleASM(1, 100, "fact", "no expiry")
	// m.ExpiresAt = nil（零值，永久）
	require.NoError(t, s.Create(ctx, m))

	items, err := s.ListByUserAgent(ctx, 1, 100, ListOpts{AliveOnly: true})
	require.NoError(t, err)
	assert.Len(t, items, 1, "NULL expires_at must be treated as alive")
}

// ─── UpdateRecency ────────────────────────────────────────────────────────────

// TestStore_AgentSessionMemory_UpdateRecency_Batch 验证批量 UpdateRecency。
func TestStore_AgentSessionMemory_UpdateRecency_Batch(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	m1 := sampleASM(1, 100, "fact", "m1")
	m2 := sampleASM(1, 100, "fact", "m2")
	require.NoError(t, s.Create(ctx, m1))
	require.NoError(t, s.Create(ctx, m2))

	newTime := time.Now().Add(1 * time.Hour).Truncate(time.Second)
	require.NoError(t, s.UpdateRecency(ctx, []uint64{m1.ID, m2.ID}, newTime))

	items, err := s.ListByUserAgent(ctx, 1, 100, ListOpts{})
	require.NoError(t, err)
	for _, item := range items {
		// SQLite datetime precision: compare truncated
		assert.WithinDuration(t, newTime, item.RecencyAt, 2*time.Second)
	}
}

// TestStore_AgentSessionMemory_UpdateRecency_EmptyIDs 验证空 ID 列表不报错。
func TestStore_AgentSessionMemory_UpdateRecency_EmptyIDs(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()
	require.NoError(t, s.UpdateRecency(ctx, []uint64{}, time.Now()))
}

// ─── DeleteByUser ─────────────────────────────────────────────────────────────

// TestStore_AgentSessionMemory_DeleteByUser 验证 DeleteByUser 只删指定 user 的行。
func TestStore_AgentSessionMemory_DeleteByUser(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "fact", "u1")))
	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "fact", "u1 again")))
	require.NoError(t, s.Create(ctx, sampleASM(2, 100, "fact", "u2")))

	require.NoError(t, s.DeleteByUser(ctx, 1))

	u1Items, err := s.ListByUserAgent(ctx, 1, 100, ListOpts{})
	require.NoError(t, err)
	assert.Empty(t, u1Items, "user 1 memories must be deleted")

	u2Items, err := s.ListByUserAgent(ctx, 2, 100, ListOpts{})
	require.NoError(t, err)
	assert.Len(t, u2Items, 1, "user 2 memories must be untouched")
}

// ─── Count ────────────────────────────────────────────────────────────────────

// TestStore_AgentSessionMemory_Count_AliveVsAll 验证 Count aliveOnly=true/false 差异。
func TestStore_AgentSessionMemory_Count_AliveVsAll(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	// 2 alive（no expiry）
	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "fact", "alive1")))
	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "fact", "alive2")))
	// 1 expired
	mExp := sampleASM(1, 100, "fact", "expired")
	past := time.Now().Add(-1 * time.Hour)
	mExp.ExpiresAt = &past
	require.NoError(t, s.Create(ctx, mExp))

	all, err := s.Count(ctx, 1, 100, false)
	require.NoError(t, err)
	assert.Equal(t, int64(3), all)

	alive, err := s.Count(ctx, 1, 100, true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), alive)
}

// TestStore_AgentSessionMemory_Count_CrossUser 验证 Count 按 (user, agent) 精确过滤。
func TestStore_AgentSessionMemory_Count_CrossUser(t *testing.T) {
	s := newTestASMStore(t)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, sampleASM(1, 100, "fact", "u1")))
	require.NoError(t, s.Create(ctx, sampleASM(2, 100, "fact", "u2")))

	c, err := s.Count(ctx, 1, 100, false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c)

	c2, err := s.Count(ctx, 2, 100, false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c2)

	// 不同 agentDefID → 0
	c3, err := s.Count(ctx, 1, 999, false)
	require.NoError(t, err)
	assert.Equal(t, int64(0), c3)
}
