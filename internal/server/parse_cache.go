package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/1136623363/watermark-go/internal/netguard"
)

type cachedParseResult struct {
	ID            string    `json:"id"`
	SourceURL     string    `json:"sourceUrl"`
	NormalizedURL string    `json:"normalizedUrl"`
	Data          parseData `json:"data"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type parseResultCache struct {
	mu           sync.RWMutex
	dir          string
	mysql        *mysqlParseResultStore
	redis        *redisParseResultCache
	statsMu      sync.Mutex
	statsCache   parseCacheStats
	statsCacheAt time.Time
}

type parseCacheSummary struct {
	ID        string    `json:"id"`
	SourceURL string    `json:"sourceUrl"`
	Platform  string    `json:"platform"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Cover     string    `json:"cover"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type parseCacheStats struct {
	Count      int       `json:"count"`
	LatestTime time.Time `json:"latestTime,omitempty"`
}

var (
	globalParseResultCache = &parseResultCache{
		dir: filepath.Join("cache", "parse-results"),
	}
	parseCacheIDPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)
)

func handleParseCache(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	data, ok, err := globalParseResultCache.get(id)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{
			Code: 1001,
			Msg:  err.Error(),
		})
		return
	}
	if !ok {
		c.JSON(http.StatusOK, httpResponse{
			Code: 1004,
			Msg:  "分享内容已失效",
		})
		return
	}

	data = normalizeParseDataMediaAliases(data)
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: data,
	})
}

func cacheParseData(sourceURL string, data parseData) parseData {
	sourceURL = strings.TrimSpace(sourceURL)
	data = normalizeParseDataMediaAliases(data)
	data.SourceURL = safePersistentSourceURL(sourceURL)
	cached, err := globalParseResultCache.put(sourceURL, data)
	if err != nil {
		logWarnf("parse cache write failed target=%s error=%v", targetForLog(sourceURL), err)
		return data
	}
	recordWechatDownloadDomains(context.Background(), sourceURL, cached, "parse_cache")
	return cached
}

func (cache *parseResultCache) getBySourceURL(sourceURL string) (parseData, bool, error) {
	id := parseCacheID(sourceURL)
	if id == "" {
		return parseData{}, false, nil
	}
	return cache.get(id)
}

func (cache *parseResultCache) configure(mysqlDB *sql.DB, redisClient *redis.Client) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if mysqlDB != nil {
		cache.mysql = &mysqlParseResultStore{db: mysqlDB}
		logInfof("parse result store enabled: mysql")
	} else {
		cache.mysql = nil
		logInfof("parse result store using file fallback dir=%s", cache.dir)
	}
	if redisClient != nil {
		cache.redis = &redisParseResultCache{client: redisClient, ttl: parseCacheTTL()}
		logInfof("parse result hot cache enabled: redis ttl=%s", parseCacheTTL())
	} else {
		cache.redis = nil
	}
}

func (cache *parseResultCache) list(limit int, query string) ([]parseCacheSummary, error) {
	if cache.mysql != nil {
		return cache.mysql.list(limit, query)
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	files, err := os.ReadDir(cache.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []parseCacheSummary{}, nil
		}
		return nil, err
	}

	query = strings.ToLower(strings.TrimSpace(query))
	items := make([]parseCacheSummary, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		item, ok := cache.readExistingLocked(strings.TrimSuffix(file.Name(), ".json"))
		if !ok {
			continue
		}
		summary := toParseCacheSummary(item)
		if query != "" && !summary.matches(query) {
			continue
		}
		items = append(items, summary)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	if limit > 0 && len(items) > limit {
		return items[:limit], nil
	}
	return items, nil
}

func (cache *parseResultCache) stats() parseCacheStats {
	if stats, ok := cache.cachedStats(); ok {
		return stats
	}
	if cache.mysql != nil {
		stats, err := cache.mysql.stats()
		if err == nil {
			cache.setStatsCache(stats)
			return stats
		}
		logWarnf("mysql parse cache stats failed: %v", err)
		if stats, ok := cache.cachedStatsAnyAge(); ok {
			return stats
		}
		return parseCacheStats{}
	}
	items, err := cache.list(0, "")
	if err != nil {
		return parseCacheStats{}
	}
	stats := parseCacheStats{Count: len(items)}
	if len(items) > 0 {
		stats.LatestTime = items[0].UpdatedAt
	}
	cache.setStatsCache(stats)
	return stats
}

func (cache *parseResultCache) cachedStats() (parseCacheStats, bool) {
	cache.statsMu.Lock()
	defer cache.statsMu.Unlock()
	if cache.statsCacheAt.IsZero() || time.Since(cache.statsCacheAt) > 30*time.Second {
		return parseCacheStats{}, false
	}
	return cache.statsCache, true
}

func (cache *parseResultCache) cachedStatsAnyAge() (parseCacheStats, bool) {
	cache.statsMu.Lock()
	defer cache.statsMu.Unlock()
	if cache.statsCacheAt.IsZero() {
		return parseCacheStats{}, false
	}
	return cache.statsCache, true
}

func (cache *parseResultCache) setStatsCache(stats parseCacheStats) {
	cache.statsMu.Lock()
	cache.statsCache = stats
	cache.statsCacheAt = time.Now()
	cache.statsMu.Unlock()
}

func (cache *parseResultCache) get(id string) (parseData, bool, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !parseCacheIDPattern.MatchString(id) {
		return parseData{}, false, nil
	}

	if cache.redis != nil {
		if data, ok, err := cache.redis.getByShareID(id); err == nil && ok {
			return data, true, nil
		} else if err != nil {
			logWarnf("redis parse cache read failed share_id=%s error=%v", id, err)
		}
	}
	if cache.mysql != nil {
		data, ok, err := cache.mysql.get(id)
		if err != nil {
			return parseData{}, false, err
		}
		if ok {
			data = normalizeParseDataMediaAliases(data)
			cache.putRedisBestEffort(data)
			return data, true, nil
		}
		return parseData{}, false, nil
	}

	cache.mu.RLock()
	defer cache.mu.RUnlock()

	path := cache.path(id)
	file, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return parseData{}, false, nil
		}
		return parseData{}, false, err
	}

	var item cachedParseResult
	if err := json.Unmarshal(file, &item); err != nil {
		return parseData{}, false, err
	}

	data := item.Data
	data.ShareID = item.ID
	data.SourceURL = firstNonEmpty(data.SourceURL, item.SourceURL, item.NormalizedURL)
	data = normalizeParseDataMediaAliases(data)
	return data, true, nil
}

func (cache *parseResultCache) getRecord(id string) (cachedParseResult, bool, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !parseCacheIDPattern.MatchString(id) {
		return cachedParseResult{}, false, nil
	}

	if cache.mysql != nil {
		return cache.mysql.getRecord(id)
	}

	cache.mu.RLock()
	defer cache.mu.RUnlock()

	path := cache.path(id)
	file, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cachedParseResult{}, false, nil
		}
		return cachedParseResult{}, false, err
	}

	var item cachedParseResult
	if err := json.Unmarshal(file, &item); err != nil {
		return cachedParseResult{}, false, err
	}
	item.Data.ShareID = item.ID
	item.Data.SourceURL = firstNonEmpty(item.Data.SourceURL, item.SourceURL, item.NormalizedURL)
	item.Data = normalizeParseDataMediaAliases(item.Data)
	return item, true, nil
}

func (cache *parseResultCache) put(sourceURL string, data parseData) (parseData, error) {
	requestURL := strings.TrimSpace(sourceURL)
	id := parseCacheID(requestURL)
	if id == "" {
		return data, nil
	}
	sourceURL = safePersistentSourceURL(requestURL)
	data.ShareID = id
	data.SourceURL = sourceURL
	data = normalizeParseDataMediaAliases(data)

	if cache.mysql != nil {
		stored, err := cache.mysql.put(requestURL, data)
		if err != nil {
			return data, err
		}
		cache.putRedisBestEffort(stored)
		return stored, nil
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	now := time.Now()
	item := cachedParseResult{
		ID:            id,
		SourceURL:     sourceURL,
		NormalizedURL: sourceURL,
		Data:          data,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if existing, ok := cache.readExistingLocked(id); ok {
		item.CreatedAt = existing.CreatedAt
	}

	item.Data.ShareID = id
	item.Data.SourceURL = sourceURL

	if err := os.MkdirAll(cache.dir, 0o755); err != nil {
		return data, err
	}

	body, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return data, err
	}

	path := cache.path(id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return data, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return data, err
	}

	cache.putRedisBestEffort(item.Data)
	return item.Data, nil
}

func (cache *parseResultCache) delete(id string) (bool, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !parseCacheIDPattern.MatchString(id) {
		return false, nil
	}

	if cache.mysql != nil {
		deleted, err := cache.mysql.delete(id)
		if err != nil {
			return false, err
		}
		cache.deleteRedisBestEffort(id)
		return deleted, nil
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	err := os.Remove(cache.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	cache.deleteRedisBestEffort(id)
	return true, nil
}

func (cache *parseResultCache) clear() (int, error) {
	if cache.mysql != nil {
		count, err := cache.mysql.clear()
		if err != nil {
			return 0, err
		}
		if cache.redis != nil {
			cache.redis.clearBestEffort()
		}
		return count, nil
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	files, err := os.ReadDir(cache.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	count := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(cache.dir, file.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return count, err
		}
		count++
	}
	return count, nil
}

func (cache *parseResultCache) readExistingLocked(id string) (cachedParseResult, bool) {
	file, err := os.ReadFile(cache.path(id))
	if err != nil {
		return cachedParseResult{}, false
	}

	var item cachedParseResult
	if err := json.Unmarshal(file, &item); err != nil {
		return cachedParseResult{}, false
	}
	return item, true
}

func (cache *parseResultCache) path(id string) string {
	return filepath.Join(cache.dir, id+".json")
}

func parseCacheID(sourceURL string) string {
	sourceURL = normalizeURLForHash(sourceURL)
	if sourceURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceURL))
	return hex.EncodeToString(sum[:12])
}

func parseURLHash(sourceURL string) string {
	sourceURL = normalizeURLForHash(sourceURL)
	if sourceURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceURL))
	return hex.EncodeToString(sum[:])
}

func safePersistentSourceURL(raw string) string {
	target, err := netguard.NewFetchURL(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return target.Safe().String()
}

func (cache *parseResultCache) putRedisBestEffort(data parseData) {
	if cache.redis == nil || strings.TrimSpace(data.ShareID) == "" {
		return
	}
	data = normalizeParseDataMediaAliases(data)
	if err := cache.redis.put(data); err != nil {
		logWarnf("redis parse cache write failed share_id=%s error=%v", data.ShareID, err)
	}
}

func (cache *parseResultCache) deleteRedisBestEffort(id string) {
	if cache.redis == nil {
		return
	}
	if err := cache.redis.delete(id); err != nil {
		logWarnf("redis parse cache delete failed share_id=%s error=%v", id, err)
	}
}

func toParseCacheSummary(item cachedParseResult) parseCacheSummary {
	data := item.Data
	return parseCacheSummary{
		ID:        item.ID,
		SourceURL: firstNonEmpty(data.SourceURL, item.SourceURL, item.NormalizedURL),
		Platform:  data.Platform,
		Type:      data.Type,
		Title:     data.Title,
		Cover:     data.Cover,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}

func (summary parseCacheSummary) matches(query string) bool {
	values := []string{
		summary.ID,
		summary.SourceURL,
		summary.Platform,
		summary.Type,
		summary.Title,
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}
