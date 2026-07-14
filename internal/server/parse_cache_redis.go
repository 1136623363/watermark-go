package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type redisParseResultCache struct {
	client *redis.Client
	ttl    time.Duration
}

func parseCacheTTL() time.Duration {
	seconds := envInt("PARSE_RESULT_CACHE_TTL_SECONDS", 7*24*3600)
	if seconds <= 0 {
		seconds = 7 * 24 * 3600
	}
	return time.Duration(seconds) * time.Second
}

func (cache *redisParseResultCache) getByShareID(shareID string) (parseData, bool, error) {
	if cache == nil || cache.client == nil {
		return parseData{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	raw, err := cache.client.Get(ctx, redisKey("parse", "share", shareID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return parseData{}, false, nil
		}
		return parseData{}, false, err
	}

	var data parseData
	if err := json.Unmarshal(raw, &data); err != nil {
		return parseData{}, false, err
	}
	data = normalizeParseDataMediaAliases(data)
	return data, true, nil
}

func (cache *redisParseResultCache) put(data parseData) error {
	if cache == nil || cache.client == nil || strings.TrimSpace(data.ShareID) == "" {
		return nil
	}
	data = normalizeParseDataMediaAliases(data)
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	pipe := cache.client.Pipeline()
	pipe.Set(ctx, redisKey("parse", "share", data.ShareID), body, cache.ttl)
	if hash := parseURLHash(data.SourceURL); hash != "" {
		pipe.Set(ctx, redisKey("parse", "result", hash), body, cache.ttl)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (cache *redisParseResultCache) delete(shareID string) error {
	if cache == nil || cache.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	return cache.client.Del(ctx, redisKey("parse", "share", shareID)).Err()
}

func (cache *redisParseResultCache) clearBestEffort() {
	if cache == nil || cache.client == nil || !allowRedisScanDelete() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	iter := cache.client.Scan(ctx, 0, redisKey("parse", "*"), 200).Iterator()
	for iter.Next(ctx) {
		_ = cache.client.Del(ctx, iter.Val()).Err()
	}
	if err := iter.Err(); err != nil {
		logErrorf("redis parse cache clear failed: %v", err)
	}
}

func allowRedisScanDelete() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_REDIS_SCAN_DELETE")), "true")
}
