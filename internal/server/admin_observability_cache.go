package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

func adminRedisCacheGet(key string, target any) bool {
	if appInfra.redis == nil || target == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	raw, err := appInfra.redis.Get(ctx, redisKey("admin", "hot", key)).Bytes()
	if err != nil {
		if err != redis.Nil {
			logWarnf("admin redis cache read failed key=%s error=%v", key, err)
		}
		return false
	}
	if err := json.Unmarshal(raw, target); err != nil {
		logWarnf("admin redis cache decode failed key=%s error=%v", key, err)
		return false
	}
	return true
}

func adminRedisCacheSet(key string, value any, ttl time.Duration) {
	if appInfra.redis == nil || value == nil || ttl <= 0 {
		return
	}
	body, err := json.Marshal(value)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := appInfra.redis.Set(ctx, redisKey("admin", "hot", key), body, ttl).Err(); err != nil {
		logWarnf("admin redis cache write failed key=%s error=%v", key, err)
	}
}
