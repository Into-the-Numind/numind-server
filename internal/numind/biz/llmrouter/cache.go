package llmrouter

import (
	"sync"
	"time"
)

const cacheTTL = 5 * time.Minute

// routesCache 路由列表缓存条目（按 modelID 索引）
type routesCache struct {
	data      []ResolvedRoute
	expiresAt time.Time
}

// cache LLMRouter 内存缓存，使用 sync.RWMutex 保护并发访问
type cache struct {
	mu     sync.RWMutex
	routes map[uint64]*routesCache
}

// newCache 创建新的缓存实例
func newCache() *cache {
	return &cache{
		routes: make(map[uint64]*routesCache),
	}
}

// getRoutes 返回指定 modelID 的缓存路由列表（未过期时有效）
func (c *cache) getRoutes(modelID uint64) ([]ResolvedRoute, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.routes[modelID]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.data, true
}

// setRoutes 更新指定 modelID 的路由列表缓存
func (c *cache) setRoutes(modelID uint64, routes []ResolvedRoute) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.routes[modelID] = &routesCache{
		data:      routes,
		expiresAt: time.Now().Add(cacheTTL),
	}
}

// Invalidate 清空所有缓存数据
func (c *cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.routes = make(map[uint64]*routesCache)
}
