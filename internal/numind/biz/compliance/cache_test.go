package compliance

import (
	"sync"
	"testing"
	"time"

	"numind-server/internal/pkg/model"
)

func makeRules(ids ...uint64) []*model.ComplianceRule {
	rules := make([]*model.ComplianceRule, 0, len(ids))
	for _, id := range ids {
		rules = append(rules, &model.ComplianceRule{ID: id, RuleType: "custom", RuleText: "test rule"})
	}
	return rules
}

// TestTTLCache_NewWithDefaults verifies that zero/negative values use defaults.
func TestTTLCache_NewWithDefaults(t *testing.T) {
	c := NewTTLCache(0, 0)
	if c.cap != DefaultCacheCap {
		t.Errorf("expected cap=%d, got %d", DefaultCacheCap, c.cap)
	}
	if c.ttl != DefaultCacheTTL {
		t.Errorf("expected ttl=%v, got %v", DefaultCacheTTL, c.ttl)
	}
}

// TestTTLCache_GetMiss verifies Get on empty cache returns nil, false.
func TestTTLCache_GetMiss(t *testing.T) {
	c := NewTTLCache(10, time.Minute)
	rules, ok := c.Get(1)
	if ok {
		t.Error("expected miss, got hit")
	}
	if rules != nil {
		t.Errorf("expected nil rules, got %v", rules)
	}
}

// TestTTLCache_SetThenGet verifies a Set entry is retrievable.
func TestTTLCache_SetThenGet(t *testing.T) {
	c := NewTTLCache(10, time.Minute)
	want := makeRules(1, 2, 3)
	c.Set(42, want)

	got, ok := c.Get(42)
	if !ok {
		t.Fatal("expected hit, got miss")
	}
	if len(got) != len(want) {
		t.Errorf("expected %d rules, got %d", len(want), len(got))
	}
	for i, r := range got {
		if r.ID != want[i].ID {
			t.Errorf("rule[%d] ID mismatch: want %d got %d", i, want[i].ID, r.ID)
		}
	}
}

// TestTTLCache_TTLExpiry verifies expired entries are treated as misses and EvictionCount increments.
func TestTTLCache_TTLExpiry(t *testing.T) {
	c := NewTTLCache(10, 10*time.Millisecond)
	c.Set(1, makeRules(1))

	// Should hit before expiry.
	if _, ok := c.Get(1); !ok {
		t.Fatal("expected hit before TTL expiry")
	}

	time.Sleep(15 * time.Millisecond)

	// Should miss after expiry.
	rules, ok := c.Get(1)
	if ok {
		t.Error("expected miss after TTL expiry, got hit")
	}
	if rules != nil {
		t.Error("expected nil rules after TTL expiry")
	}
	if c.EvictionCount() < 1 {
		t.Errorf("expected EvictionCount >= 1 after TTL expiry, got %d", c.EvictionCount())
	}
}

// TestTTLCache_Invalidate verifies that Invalidate makes entry a miss.
func TestTTLCache_Invalidate(t *testing.T) {
	c := NewTTLCache(10, time.Minute)
	c.Set(5, makeRules(10))

	c.Invalidate(5)

	_, ok := c.Get(5)
	if ok {
		t.Error("expected miss after Invalidate, got hit")
	}
}

// TestTTLCache_InvalidateNonexistent verifies Invalidate on non-existent key is idempotent.
func TestTTLCache_InvalidateNonexistent(t *testing.T) {
	c := NewTTLCache(10, time.Minute)
	// Should not panic.
	c.Invalidate(999)
	c.Invalidate(999) // double idempotent
}

// TestTTLCache_CapEviction verifies that when cap is reached, the LRU entry is evicted.
func TestTTLCache_CapEviction(t *testing.T) {
	c := NewTTLCache(2, time.Minute)

	// Set entries with distinct lastUsed times.
	c.Set(1, makeRules(1))
	time.Sleep(2 * time.Millisecond)
	c.Set(2, makeRules(2))

	// Access entry 1 to make it more recently used than entry 2 was at initial set.
	// But we want entry 1 to be the oldest — so we access 2 to update its lastUsed.
	time.Sleep(2 * time.Millisecond)
	c.Get(2) // updates lastUsed for entry 2

	evBefore := c.EvictionCount()

	// Adding a 3rd entry should evict the LRU (entry 1, which has oldest lastUsed).
	c.Set(3, makeRules(3))

	if c.Size() != 2 {
		t.Errorf("expected size=2 after cap eviction, got %d", c.Size())
	}
	if c.EvictionCount() <= evBefore {
		t.Errorf("expected EvictionCount to increment, got %d (was %d)", c.EvictionCount(), evBefore)
	}

	// Entry 1 should be evicted (oldest lastUsed).
	_, ok1 := c.Get(1)
	_, ok2 := c.Get(2)
	_, ok3 := c.Get(3)

	if ok1 {
		t.Error("expected entry 1 (LRU) to be evicted")
	}
	if !ok2 {
		t.Error("expected entry 2 to survive eviction")
	}
	if !ok3 {
		t.Error("expected entry 3 (newly inserted) to exist")
	}
}

// TestTTLCache_Size verifies Size reflects the number of non-expired entries.
func TestTTLCache_Size(t *testing.T) {
	c := NewTTLCache(10, time.Minute)

	if c.Size() != 0 {
		t.Errorf("expected size=0, got %d", c.Size())
	}

	c.Set(1, makeRules(1))
	c.Set(2, makeRules(2))

	if c.Size() != 2 {
		t.Errorf("expected size=2, got %d", c.Size())
	}

	c.Invalidate(1)

	if c.Size() != 1 {
		t.Errorf("expected size=1 after invalidation, got %d", c.Size())
	}
}

// TestTTLCache_RaceParallel verifies concurrent Get + Invalidate under race detector.
func TestTTLCache_RaceParallel(t *testing.T) {
	c := NewTTLCache(50, 100*time.Millisecond)

	// Pre-populate with some entries.
	for i := uint(1); i <= 20; i++ {
		c.Set(i, makeRules(uint64(i)))
	}

	var wg sync.WaitGroup

	// 100 concurrent Get goroutines.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := uint(idx%20) + 1
			c.Get(key)
			c.Set(key+100, makeRules(uint64(key)))
		}(i)
	}

	// 10 concurrent Invalidate goroutines.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c.Invalidate(uint(idx + 1))
		}(i)
	}

	wg.Wait()

	// Verify no panic and Size/EvictionCount are accessible without race.
	_ = c.Size()
	_ = c.EvictionCount()
}
