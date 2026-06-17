package budget

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisBudgetStore is the cross-instance daily-credit aggregate backed by Redis.
// Key: "agent:budget:daily:{userID}:{YYYY-MM-DD-UTC}". TTL ~48h so the key
// survives the full UTC day then auto-cleans. INCRBY makes the counter correct
// across multiple app instances and across process restarts (within the day).
type redisBudgetStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisStore builds an IBudgetStore over the given go-redis client.
// Returns nil when rdb is nil (caller then runs in-process — see NewTracker).
func NewRedisStore(rdb *redis.Client) IBudgetStore {
	if rdb == nil {
		return nil
	}
	return &redisBudgetStore{rdb: rdb, ttl: 48 * time.Hour}
}

func dailyRedisKey(userID uint, day time.Time) string {
	return "agent:budget:daily:" + uintToString(userID) + ":" + day.UTC().Format("2006-01-02")
}

func (s *redisBudgetStore) AddUserDailyCredits(ctx context.Context, userID uint, day time.Time, delta int64) (int64, error) {
	key := dailyRedisKey(userID, day)
	pipe := s.rdb.TxPipeline()
	incr := pipe.IncrBy(ctx, key, delta)
	pipe.Expire(ctx, key, s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

func (s *redisBudgetStore) GetUserDailyCredits(ctx context.Context, userID uint, day time.Time) (int64, error) {
	v, err := s.rdb.Get(ctx, dailyRedisKey(userID, day)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return v, nil
}
