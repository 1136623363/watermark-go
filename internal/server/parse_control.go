package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	parseInMemoryLocks sync.Map
	parseFailureMu     sync.Mutex
	parseFailureMemory = map[string]parseFailureItem{}
)

type parseFailureItem struct {
	Message   string
	ExpiresAt time.Time
}

func getParseFailure(sourceURL string) (string, bool) {
	hash := parseURLHash(sourceURL)
	if hash == "" {
		return "", false
	}
	if appInfra.redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		message, err := appInfra.redis.Get(ctx, redisKey("parse", "fail", hash)).Result()
		if err == nil && strings.TrimSpace(message) != "" {
			return message, true
		}
		if err != nil && err != redis.Nil {
			logWarnf("redis parse failure read failed hash=%s error=%v", hash, err)
		}
	}

	parseFailureMu.Lock()
	defer parseFailureMu.Unlock()
	item, ok := parseFailureMemory[hash]
	if !ok || time.Now().After(item.ExpiresAt) {
		delete(parseFailureMemory, hash)
		return "", false
	}
	return item.Message, true
}

func setParseFailure(sourceURL string, err error) {
	if err == nil {
		return
	}
	hash := parseURLHash(sourceURL)
	if hash == "" {
		return
	}
	message := compactLogMessage(err.Error())
	ttl := parseFailureTTL()
	if appInfra.redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		redisErr := appInfra.redis.Set(ctx, redisKey("parse", "fail", hash), message, ttl).Err()
		cancel()
		if redisErr != nil {
			logWarnf("redis parse failure write failed hash=%s error=%v", hash, redisErr)
		}
	}

	parseFailureMu.Lock()
	pruneParseFailureMemoryLocked(time.Now())
	parseFailureMemory[hash] = parseFailureItem{
		Message:   message,
		ExpiresAt: time.Now().Add(ttl),
	}
	parseFailureMu.Unlock()
}

func clearParseFailure(sourceURL string) {
	hash := parseURLHash(sourceURL)
	if hash == "" {
		return
	}
	if appInfra.redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_ = appInfra.redis.Del(ctx, redisKey("parse", "fail", hash)).Err()
		cancel()
	}
	parseFailureMu.Lock()
	delete(parseFailureMemory, hash)
	parseFailureMu.Unlock()
}

func acquireParseLock(sourceURL string) (func(), bool) {
	hash := parseURLHash(sourceURL)
	if hash == "" {
		return func() {}, true
	}
	if appInfra.redis != nil {
		token, randomErr := secureRandomHex(16)
		if randomErr != nil {
			logErrorf("secure entropy unavailable for redis parse lock; using process lock")
		} else {
			key := redisKey("parse", "lock", hash)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			ok, err := appInfra.redis.SetNX(ctx, key, token, parseLockTTL()).Result()
			cancel()
			if err != nil {
				logWarnf("redis parse lock failed hash=%s error=%v", hash, err)
			} else if ok {
				return func() {
					releaseRedisLock(key, token)
				}, true
			} else {
				return func() {}, false
			}
		}
	}

	lock := &sync.Mutex{}
	actual, loaded := parseInMemoryLocks.LoadOrStore(hash, lock)
	if loaded {
		return func() {}, false
	}
	lock = actual.(*sync.Mutex)
	lock.Lock()
	return func() {
		lock.Unlock()
		parseInMemoryLocks.Delete(hash)
	}, true
}

func waitForParseResult(sourceURL string) (parseData, bool) {
	deadline := time.Now().Add(parseWaitTimeout())
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if data, ok, err := globalParseResultCache.getBySourceURL(sourceURL); err == nil && ok {
			return data, true
		}
	}
	return parseData{}, false
}

func releaseRedisLock(key, token string) {
	if appInfra.redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	script := redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)
	if err := script.Run(ctx, appInfra.redis, []string{key}, token).Err(); err != nil {
		logWarnf("redis parse lock release failed key=%s error=%v", key, err)
	}
}

func parseFailureTTL() time.Duration {
	seconds := envInt("PARSE_FAILURE_CACHE_TTL_SECONDS", 180)
	if seconds <= 0 {
		seconds = 180
	}
	return time.Duration(seconds) * time.Second
}

func pruneParseFailureMemoryLocked(now time.Time) {
	for key, item := range parseFailureMemory {
		if now.After(item.ExpiresAt) {
			delete(parseFailureMemory, key)
		}
	}
	maxItems := envInt("PARSE_FAILURE_MEMORY_MAX_ITEMS", 10000)
	if maxItems <= 0 {
		maxItems = 10000
	}
	for key := range parseFailureMemory {
		if len(parseFailureMemory) <= maxItems {
			return
		}
		delete(parseFailureMemory, key)
	}
}

func parseLockTTL() time.Duration {
	seconds := envInt("PARSE_LOCK_TTL_SECONDS", 60)
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func parseWaitTimeout() time.Duration {
	millis := envInt("PARSE_LOCK_WAIT_MILLISECONDS", 3000)
	if millis <= 0 {
		millis = 3000
	}
	return time.Duration(millis) * time.Millisecond
}

func errParseInProgress() error {
	return errors.New("同一链接正在解析中，请稍后重试")
}
