package pricing

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"numind-server/internal/pkg/model"
)

// Cache tuning constants. Sized for ~dozens of service_type × provider × model
// combinations in prod; 500 entries leaves headroom without memory pressure.
const (
	cacheSize = 500
	cacheTTL  = 5 * time.Minute
)

// cacheKey builds the canonical LRU key. Exposed for tests / the pubsub
// InvalidateCache path so keys are computed identically on both sides.
func cacheKey(serviceType, provider, model string) string {
	return serviceType + "|" + provider + "|" + model
}

// agnosticCacheKey builds the LRU key for service_type-agnostic fallback hits
// (fix ①). The "agnostic|" prefix never collides with a real service_type, so
// these entries are kept distinct from the cacheKey() entries above. Computed in
// one place so resolveAgnostic and InvalidateCache stay in sync.
func agnosticCacheKey(provider, model string) string {
	return "agnostic|" + provider + "|" + model
}

// cacheEntry wraps a PricingRule with its expiry so that stale entries can be
// dropped lazily on Get. We prefer lazy TTL over a background sweeper to keep
// the cache dependency minimal.
type cacheEntry struct {
	rule      *model.PricingRule
	expiresAt time.Time
}

// ruleCache is the shared LRU cache used by calculator. Package-scoped
// (default) so InvalidateCache can purge keys without a Calculator handle,
// mirroring the pubsub contract from spec §3.0.4.
type ruleCache struct {
	lru *lru.Cache[string, cacheEntry]
	ttl time.Duration
}

// newRuleCache builds an LRU of the given size + TTL. Panics on invalid size
// (LRU constructor error) — this is process-init code and misconfiguration is
// a programming error, not a runtime condition.
func newRuleCache(size int, ttl time.Duration) *ruleCache {
	c, err := lru.New[string, cacheEntry](size)
	if err != nil {
		// Size <= 0 is the only failure mode per hashicorp/golang-lru.
		panic("pricing: invalid LRU cache size: " + err.Error())
	}
	return &ruleCache{lru: c, ttl: ttl}
}

// Get returns the cached rule if present and not expired. Expired entries are
// evicted on access so a subsequent Put refills from the authoritative store.
func (c *ruleCache) Get(key string) (*model.PricingRule, bool) {
	entry, ok := c.lru.Get(key)
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		c.lru.Remove(key)
		return nil, false
	}
	return entry.rule, true
}

// Put stores a rule under the given key with the cache's configured TTL.
func (c *ruleCache) Put(key string, rule *model.PricingRule) {
	c.lru.Add(key, cacheEntry{
		rule:      rule,
		expiresAt: time.Now().Add(c.ttl),
	})
}

// Remove evicts a single cache entry (exposed for InvalidateCache).
func (c *ruleCache) Remove(key string) {
	c.lru.Remove(key)
}

// Purge empties the cache entirely (exposed for tests / admin-wide reload).
func (c *ruleCache) Purge() {
	c.lru.Purge()
}

// ----------------------------------------------------------------------------
// Package-level cache registry for pubsub-driven invalidation.
//
// Each NewCalculator instance registers its cache so that InvalidateCache can
// fan out an eviction across every live Calculator in the process (typically
// one, but tests construct multiple).
// ----------------------------------------------------------------------------

var (
	cacheRegistryMu sync.RWMutex
	cacheRegistry   []*ruleCache
)

func registerCache(c *ruleCache) {
	cacheRegistryMu.Lock()
	defer cacheRegistryMu.Unlock()
	cacheRegistry = append(cacheRegistry, c)
}

func unregisterCache(c *ruleCache) {
	cacheRegistryMu.Lock()
	defer cacheRegistryMu.Unlock()
	for i, existing := range cacheRegistry {
		if existing == c {
			cacheRegistry = append(cacheRegistry[:i], cacheRegistry[i+1:]...)
			return
		}
	}
}

// InvalidateCache evicts the cache entry for (serviceType, provider, model)
// across every registered Calculator. Admin CRUD endpoints call this (usually
// via a pubsub "pricing_rule_changed" subscriber) so that operator edits take
// effect without waiting up to 5 minutes for TTL expiry.
//
// A missing key is a no-op — callers do not need to check membership first.
func InvalidateCache(serviceType, provider, model string) {
	key := cacheKey(serviceType, provider, model)
	// Also evict the service_type-agnostic entry (fix ①): it is keyed only by
	// (provider, model), so any service_type's price change for this model must
	// drop it too — otherwise an admin price edit would leave the agnostic
	// fallback path serving the stale price until TTL expiry.
	aKey := agnosticCacheKey(provider, model)
	cacheRegistryMu.RLock()
	defer cacheRegistryMu.RUnlock()
	for _, c := range cacheRegistry {
		c.Remove(key)
		c.Remove(aKey)
	}
}

// PurgeAllCaches empties every registered cache (admin-wide reload hook).
// Exposed separately from InvalidateCache because the pubsub contract is
// per-key; "reset everything" is a distinct operator action.
func PurgeAllCaches() {
	cacheRegistryMu.RLock()
	defer cacheRegistryMu.RUnlock()
	for _, c := range cacheRegistry {
		c.Purge()
	}
}
