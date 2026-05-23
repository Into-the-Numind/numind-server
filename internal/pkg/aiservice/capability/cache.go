package capability

import (
	"sync"
	"time"
)

const cacheTTL = 5 * time.Minute

// cacheEntry holds cached Capabilities with a TTL expiry.
type cacheEntry struct {
	caps      *Capabilities
	expiresAt time.Time
}

// capabilityCache is the package-level in-memory cache.
// Key: model_key (string) → *cacheEntry
//
// sync.Map is used for thread safety with minimal contention on reads.
// Multiple concurrent misses for the same key are accepted (each triggers
// its own DB lookup); the cost is negligible for admin-frequency updates.
//
// NOTE: InvalidateCache only affects this process's cache. In multi-instance
// deployments, other instances will serve stale data until TTL expires (max 5 min).
// Accepted per V1.5 spec; V2 may add Redis pub/sub invalidation.
var capabilityCache sync.Map

// cacheGet returns the cached capabilities for modelKey if present and not expired.
// Returns (nil, false) on miss or expiry.
func cacheGet(modelKey string) (*Capabilities, bool) {
	v, ok := capabilityCache.Load(modelKey)
	if !ok {
		return nil, false
	}
	entry, ok := v.(*cacheEntry)
	if !ok || time.Now().After(entry.expiresAt) {
		// Lazy evict expired entry.
		capabilityCache.Delete(modelKey)
		return nil, false
	}
	return entry.caps, true
}

// cacheSet stores capabilities for modelKey with the package-level TTL.
func cacheSet(modelKey string, caps *Capabilities) {
	capabilityCache.Store(modelKey, &cacheEntry{
		caps:      caps,
		expiresAt: time.Now().Add(cacheTTL),
	})
}

// InvalidateCache removes the cached capabilities for modelKey.
// Must be called after an admin PATCH that updates capability_json, so that
// subsequent GetCapabilities calls see the new values without waiting for TTL
// expiry.
func InvalidateCache(modelKey string) {
	capabilityCache.Delete(modelKey)
}
