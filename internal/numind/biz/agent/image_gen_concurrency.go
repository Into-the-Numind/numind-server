package agent

import "sync"

// imageGenMaxConcurrentPerUser caps how many image_gen requests a single user may
// have in flight at once (product rule 2026-06-17: 文生图是常用功能、不设开关，但同一
// 用户同时最多 6 个请求). A request over the cap gets a soft error and is not generated.
const imageGenMaxConcurrentPerUser = 6

// userConcurrencyLimiter is a per-user in-flight counter with a hard cap. Safe for
// concurrent use. acquire/release are paired; the map stays bounded (entries are
// deleted when a user's count returns to zero).
type userConcurrencyLimiter struct {
	mu    sync.Mutex
	limit int
	inUse map[uint]int
}

func newUserConcurrencyLimiter(limit int) *userConcurrencyLimiter {
	return &userConcurrencyLimiter{limit: limit, inUse: make(map[uint]int)}
}

// acquire reserves a slot for userID; returns false when the user is already at the
// cap. A successful acquire (true) MUST be paired with exactly one release.
func (l *userConcurrencyLimiter) acquire(userID uint) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inUse[userID] >= l.limit {
		return false
	}
	l.inUse[userID]++
	return true
}

func (l *userConcurrencyLimiter) release(userID uint) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inUse[userID] <= 1 {
		delete(l.inUse, userID)
		return
	}
	l.inUse[userID]--
}

// imageGenConcurrency is the process-wide per-user limiter for image_gen.
var imageGenConcurrency = newUserConcurrencyLimiter(imageGenMaxConcurrentPerUser)
