package registry

import (
	"sync"
	"time"

	"numind-server/internal/pkg/model"
)

const defaultTTL = 30 * time.Second

// ----------------------------------------------------------------------------
// cache entry types
// ----------------------------------------------------------------------------

// serviceCacheEntry holds a single AIService with its expiry time.
type serviceCacheEntry struct {
	svc      *model.AIService
	expireAt time.Time
}

// taskCacheEntry holds a ResolvedTask (primary + fallbacks) with its expiry time.
type taskCacheEntry struct {
	primary   *ResolvedRoute
	fallbacks []ResolvedRoute
	expireAt  time.Time
}

// ----------------------------------------------------------------------------
// cache
// ----------------------------------------------------------------------------

// cache is a thread-safe in-memory cache for AI services and resolved task routes.
// All entries carry a TTL-based expiry; expired entries are lazily evicted on access.
//
// Metrics (hit/miss counts) are tracked but not yet exposed — they will be
// consumed by the Task 8 /healthz endpoint.
type cache struct {
	mu       sync.RWMutex
	ttl      time.Duration
	services map[uint64]*serviceCacheEntry
	tasks    map[string]*taskCacheEntry

	// serviceTaskIndex maps service ID → set of task IDs that reference it.
	// Used by InvalidateService to efficiently evict task entries on service writes.
	serviceTaskIndex map[uint64]map[string]struct{}

	// Metrics (unexported — surfaced in Task 8).
	hits   int64
	misses int64
}

// newCache creates a new cache with the specified TTL.
// If ttl is zero, defaultTTL (30 s) is used.
func newCache(ttl time.Duration) *cache {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &cache{
		ttl:              ttl,
		services:         make(map[uint64]*serviceCacheEntry),
		tasks:            make(map[string]*taskCacheEntry),
		serviceTaskIndex: make(map[uint64]map[string]struct{}),
	}
}

// ----------------------------------------------------------------------------
// Service cache
// ----------------------------------------------------------------------------

// GetService returns the cached AIService for id, or (nil, false) on miss / expiry.
func (c *cache) GetService(id uint64) (*model.AIService, bool) {
	c.mu.RLock()
	entry, ok := c.services[id]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expireAt) {
		c.mu.Lock()
		// Re-check under write lock to avoid TOCTOU between RUnlock and Lock.
		if entry2, ok2 := c.services[id]; ok2 && time.Now().After(entry2.expireAt) {
			delete(c.services, id)
		}
		c.misses++
		c.mu.Unlock()
		return nil, false
	}
	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	return entry.svc, true
}

// SetService stores an AIService in the cache.
func (c *cache) SetService(svc *model.AIService) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services[svc.ID] = &serviceCacheEntry{
		svc:      svc,
		expireAt: time.Now().Add(c.ttl),
	}
}

// InvalidateService removes the cached service entry for id and also evicts every
// task entry that references this service (so stale routes are not returned).
func (c *cache) InvalidateService(id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.services, id)

	// Evict all task entries that reference this service.
	if taskIDs, ok := c.serviceTaskIndex[id]; ok {
		for taskID := range taskIDs {
			delete(c.tasks, taskID)
		}
		delete(c.serviceTaskIndex, id)
	}
}

// ----------------------------------------------------------------------------
// Task (ResolvedRoute) cache
// ----------------------------------------------------------------------------

// GetTask returns the cached primary + fallback routes for taskID,
// or (nil, nil, false) on miss / expiry.
func (c *cache) GetTask(taskID string) (*ResolvedRoute, []ResolvedRoute, bool) {
	c.mu.RLock()
	entry, ok := c.tasks[taskID]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expireAt) {
		c.mu.Lock()
		if entry2, ok2 := c.tasks[taskID]; ok2 && time.Now().After(entry2.expireAt) {
			delete(c.tasks, taskID)
		}
		c.misses++
		c.mu.Unlock()
		return nil, nil, false
	}
	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	return entry.primary, entry.fallbacks, true
}

// SetTask stores resolved routes for taskID. serviceIDs lists all service IDs
// referenced by this task (primary + fallbacks), used to build the reverse index.
func (c *cache) SetTask(taskID string, primary *ResolvedRoute, fallbacks []ResolvedRoute, serviceIDs []uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.tasks[taskID] = &taskCacheEntry{
		primary:   primary,
		fallbacks: fallbacks,
		expireAt:  time.Now().Add(c.ttl),
	}

	// Build reverse index: serviceID → {taskID}.
	for _, svcID := range serviceIDs {
		if c.serviceTaskIndex[svcID] == nil {
			c.serviceTaskIndex[svcID] = make(map[string]struct{})
		}
		c.serviceTaskIndex[svcID][taskID] = struct{}{}
	}
}

// InvalidateTask removes the cached entry for a specific task ID.
func (c *cache) InvalidateTask(taskID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tasks, taskID)
}

// ----------------------------------------------------------------------------
// Metrics (unexported accessors — used in tests only for now)
// ----------------------------------------------------------------------------

// stats returns the cumulative hit/miss counts. Not thread-safe on its own —
// callers that need precise counts must hold the lock externally; for test
// assertions approximate values are sufficient.
func (c *cache) stats() (hits, misses int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses
}
