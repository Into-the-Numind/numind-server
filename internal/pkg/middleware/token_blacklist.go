package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// TokenBlacklist token黑名单管理器
type TokenBlacklist struct {
	blacklist map[string]time.Time
	mu        sync.RWMutex
}

var (
	globalBlacklist *TokenBlacklist
	once            sync.Once
)

// InitTokenBlacklist 初始化token黑名单（单例模式）
func InitTokenBlacklist() *TokenBlacklist {
	once.Do(func() {
		globalBlacklist = &TokenBlacklist{
			blacklist: make(map[string]time.Time),
		}
		// 启动清理goroutine，定期清理过期的token
		go globalBlacklist.cleanup()
	})
	return globalBlacklist
}

// GetTokenBlacklist 获取全局token黑名单实例
func GetTokenBlacklist() *TokenBlacklist {
	if globalBlacklist == nil {
		return InitTokenBlacklist()
	}
	return globalBlacklist
}

// AddToken 将token加入黑名单
// expireTime: token的过期时间，用于自动清理
func (tb *TokenBlacklist) AddToken(token string, expireTime time.Time) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tokenHash := hashToken(token)
	tb.blacklist[tokenHash] = expireTime
}

// IsTokenBlacklisted 检查token是否在黑名单中
func (tb *TokenBlacklist) IsTokenBlacklisted(token string) bool {
	tb.mu.RLock()
	defer tb.mu.RUnlock()

	tokenHash := hashToken(token)
	expireTime, exists := tb.blacklist[tokenHash]
	if !exists {
		return false
	}

	// 如果token已过期，从黑名单中移除
	if time.Now().After(expireTime) {
		tb.mu.RUnlock()
		tb.mu.Lock()
		delete(tb.blacklist, tokenHash)
		tb.mu.Unlock()
		tb.mu.RLock()
		return false
	}

	return true
}

// RemoveToken 从黑名单中移除token
func (tb *TokenBlacklist) RemoveToken(token string) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tokenHash := hashToken(token)
	delete(tb.blacklist, tokenHash)
}

// cleanup 定期清理过期的token
func (tb *TokenBlacklist) cleanup() {
	ticker := time.NewTicker(1 * time.Hour) // 每小时清理一次
	defer ticker.Stop()

	for range ticker.C {
		tb.mu.Lock()
		now := time.Now()
		for tokenHash, expireTime := range tb.blacklist {
			if now.After(expireTime) {
				delete(tb.blacklist, tokenHash)
			}
		}
		tb.mu.Unlock()
	}
}

// hashToken 对token进行hash，用于存储
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
