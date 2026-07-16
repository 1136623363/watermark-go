package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

type memoryRateWindow struct {
	Count     int
	ExpiresAt time.Time
}

var (
	memoryRateMu      sync.Mutex
	memoryRateWindows = map[string]memoryRateWindow{}
)

func rateLimitMiddleware(scope string, limit int, window time.Duration) gin.HandlerFunc {
	if !rateLimitEnabled() || limit <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	if window <= 0 {
		window = time.Minute
	}
	return func(c *gin.Context) {
		key := rateLimitKey(scope, clientIPForLimit(c))
		allowed, retryAfter := allowRequest(key, limit, window)
		if !allowed {
			c.Header("Retry-After", retryAfter.String())
			c.AbortWithStatusJSON(http.StatusTooManyRequests, httpResponse{
				Code: 429,
				Msg:  "请求过于频繁，请稍后再试",
			})
			return
		}
		c.Next()
	}
}

func rateLimitEnabled() bool {
	return runtimecfg.RateLimitEnabled()
}

func allowRequest(key string, limit int, window time.Duration) (bool, time.Duration) {
	if appInfra.redis != nil {
		allowed, retryAfter, err := allowRequestRedis(key, limit, window)
		if err == nil {
			return allowed, retryAfter
		}
		logErrorf("redis rate limit failed key=%s error=%v", key, err)
	}
	return allowRequestMemory(key, limit, window)
}

func allowRequestRedis(key string, limit int, window time.Duration) (bool, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	redisKeyName := redisKey("rate", key)
	count, err := appInfra.redis.Incr(ctx, redisKeyName).Result()
	if err != nil {
		return false, 0, err
	}
	if count == 1 {
		if err := appInfra.redis.Expire(ctx, redisKeyName, window).Err(); err != nil {
			return false, 0, err
		}
	}
	if count <= int64(limit) {
		return true, 0, nil
	}
	ttl, err := appInfra.redis.TTL(ctx, redisKeyName).Result()
	if err != nil || ttl < 0 {
		ttl = window
	}
	return false, ttl, nil
}

func allowRequestMemory(key string, limit int, window time.Duration) (bool, time.Duration) {
	now := time.Now()
	memoryRateMu.Lock()
	defer memoryRateMu.Unlock()
	pruneMemoryRateWindowsLocked(now)

	item, ok := memoryRateWindows[key]
	if !ok || now.After(item.ExpiresAt) {
		memoryRateWindows[key] = memoryRateWindow{
			Count:     1,
			ExpiresAt: now.Add(window),
		}
		return true, 0
	}
	if item.Count >= limit {
		return false, time.Until(item.ExpiresAt)
	}
	item.Count++
	memoryRateWindows[key] = item
	return true, 0
}

func rateLimitKey(scope, ip string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	return scope + ":" + ip
}

func clientIPForLimit(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.ClientIP()
}

func resetRateLimitMemoryForTest() {
	memoryRateMu.Lock()
	defer memoryRateMu.Unlock()
	memoryRateWindows = map[string]memoryRateWindow{}
}

func pruneMemoryRateWindowsLocked(now time.Time) {
	maxWindows := envInt("RATE_LIMIT_MEMORY_MAX_WINDOWS", 20000)
	if maxWindows <= 0 {
		maxWindows = 20000
	}
	if len(memoryRateWindows) < maxWindows {
		return
	}
	for key, item := range memoryRateWindows {
		if now.After(item.ExpiresAt) {
			delete(memoryRateWindows, key)
		}
	}
	for key := range memoryRateWindows {
		if len(memoryRateWindows) < maxWindows {
			return
		}
		delete(memoryRateWindows, key)
	}
}

var _ = redis.Nil
