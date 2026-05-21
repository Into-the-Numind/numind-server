package compliance

import (
	"sync"
	"sync/atomic"
	"time"

	"numind-server/internal/pkg/model"
)

// TTLCache — per-parent_user_id 规则缓存（TTL + cap，lazy LRU 兜底淘汰）
type TTLCache struct {
	mu          sync.Mutex // S2 P2-1 修复：全程 Lock 避免 RUnlock-then-Lock race
	data        map[uint]*cacheEntry
	cap         int
	ttl         time.Duration
	evictionCnt atomic.Uint64 // S2 P3-1：可观测性
}

type cacheEntry struct {
	rules    []*model.ComplianceRule
	expiry   time.Time
	lastUsed time.Time
}

const (
	DefaultCacheCap = 500
	DefaultCacheTTL = 5 * time.Minute
)

func NewTTLCache(cap int, ttl time.Duration) *TTLCache {
	if cap <= 0 {
		cap = DefaultCacheCap
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &TTLCache{
		data: make(map[uint]*cacheEntry, cap),
		cap:  cap,
		ttl:  ttl,
	}
}

func (c *TTLCache) Get(parentUserID uint) ([]*model.ComplianceRule, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[parentUserID]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiry) {
		delete(c.data, parentUserID)
		c.evictionCnt.Add(1)
		return nil, false
	}
	e.lastUsed = time.Now()
	return e.rules, true
}

func (c *TTLCache) Set(parentUserID uint, rules []*model.ComplianceRule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.data) >= c.cap {
		c.evictLRU()
	}
	c.data[parentUserID] = &cacheEntry{
		rules:    rules,
		expiry:   time.Now().Add(c.ttl),
		lastUsed: time.Now(),
	}
}

func (c *TTLCache) Invalidate(parentUserID uint) {
	c.mu.Lock()
	delete(c.data, parentUserID)
	c.mu.Unlock()
}

// evictLRU — caller 必须持 c.mu.Lock()
func (c *TTLCache) evictLRU() {
	var oldestKey uint
	var oldestTime time.Time
	first := true
	for k, v := range c.data {
		if first || v.lastUsed.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.lastUsed
			first = false
		}
	}
	if !first {
		delete(c.data, oldestKey)
		c.evictionCnt.Add(1)
	}
}

// Size — 可观测性
func (c *TTLCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

// EvictionCount — 可观测性（TTL 过期 + LRU 兜底淘汰合计）
func (c *TTLCache) EvictionCount() uint64 {
	return c.evictionCnt.Load()
}
