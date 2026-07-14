package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"watermark-backend/internal/parsers/native"
)

const adminTestSamplesFilePath = "cache/platform-test-samples.json"

type adminTestSample struct {
	Platform  string    `json:"platform"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Enabled   bool      `json:"enabled"`
	Note      string    `json:"note,omitempty"`
	SortOrder int       `json:"sortOrder"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type adminTestSamplesFile struct {
	Items     []adminTestSample `json:"items"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

var externalAdminTestSampleNames = map[string]string{
	"abc":         "ABC",
	"arte":        "ArteTV",
	"baidutieba":  "百度贴吧",
	"cctalk":      "CCtalk",
	"dongchedi":   "懂车帝",
	"youtube":     "YouTube",
	"tiktok":      "TikTok",
	"instagram":   "Instagram",
	"facebook":    "Facebook",
	"iqiyi":       "爱奇艺",
	"mgtv":        "芒果TV",
	"open163":     "网易公开课",
	"reddit":      "Reddit",
	"ted":         "TED",
	"vimeo":       "Vimeo",
	"dailymotion": "Dailymotion",
	"m3u8":        "M3U8",
	"youku":       "优酷",
	"zhihu":       "知乎",
}

func handleAdminListTestSamples(c *gin.Context) {
	samples, store, err := currentAdminTestSamples(c.Request.Context())
	if err != nil {
		c.JSON(200, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	c.JSON(200, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"items":     samples,
			"enabled":   len(adminTestLinksFromSamples(samples)),
			"testLinks": adminTestLinksFromSamples(samples),
			"store":     store,
		},
	})
}

func handleAdminSaveTestSamples(c *gin.Context) {
	var req struct {
		Items []adminTestSample `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, httpResponse{Code: 1004, Msg: "invalid samples payload"})
		return
	}

	samples := sanitizeAdminTestSamples(req.Items)
	if len(samples) == 0 {
		c.JSON(400, httpResponse{Code: 1004, Msg: "at least one test sample is required"})
		return
	}

	saved, store, err := saveAdminTestSamples(c.Request.Context(), samples)
	if err != nil {
		c.JSON(200, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	writeAdminAudit(c, "admin.test_samples.update", "platform_test_samples", "", gin.H{"count": len(saved), "enabled": len(adminTestLinksFromSamples(saved)), "store": store})
	c.JSON(200, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"items":     saved,
			"enabled":   len(adminTestLinksFromSamples(saved)),
			"testLinks": adminTestLinksFromSamples(saved),
			"store":     store,
		},
	})
}

func handleAdminResetTestSamples(c *gin.Context) {
	samples := defaultAdminTestSamples()
	saved, store, err := saveAdminTestSamples(c.Request.Context(), samples)
	if err != nil {
		c.JSON(200, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	writeAdminAudit(c, "admin.test_samples.reset", "platform_test_samples", "", gin.H{"count": len(saved), "enabled": len(adminTestLinksFromSamples(saved)), "store": store})
	c.JSON(200, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"items":     saved,
			"enabled":   len(adminTestLinksFromSamples(saved)),
			"testLinks": adminTestLinksFromSamples(saved),
			"store":     store,
		},
	})
}

func currentAdminTestLinks(ctx context.Context) []adminTestLink {
	samples, _, err := currentAdminTestSamples(ctx)
	if err != nil {
		logWarnf("load admin test samples failed, using defaults: %v", err)
		samples = defaultAdminTestSamples()
	}
	return adminTestLinksFromSamples(samples)
}

func currentAdminTestSamples(ctx context.Context) ([]adminTestSample, string, error) {
	if appInfra.mysql != nil && adminTestSampleTableExists(ctx) {
		items, err := loadAdminTestSamplesFromMySQL(ctx)
		if err != nil {
			return nil, "mysql", err
		}
		if len(items) > 0 {
			return mergeAdminTestSamplesWithCatalog(items), "mysql", nil
		}
		return defaultAdminTestSamples(), "mysql", nil
	}

	items, err := loadAdminTestSamplesFromFile()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultAdminTestSamples(), "file", nil
		}
		return nil, "file", err
	}
	if len(items) == 0 {
		return defaultAdminTestSamples(), "file", nil
	}
	return mergeAdminTestSamplesWithCatalog(items), "file", nil
}

func saveAdminTestSamples(ctx context.Context, samples []adminTestSample) ([]adminTestSample, string, error) {
	samples = normalizeStoredAdminTestSamples(samples)
	now := time.Now()
	for index := range samples {
		samples[index].UpdatedAt = now
	}

	if appInfra.mysql != nil && adminTestSampleTableExists(ctx) {
		if err := saveAdminTestSamplesToMySQL(ctx, samples); err != nil {
			return nil, "mysql", err
		}
		return samples, "mysql", nil
	}
	if err := saveAdminTestSamplesToFile(samples); err != nil {
		return nil, "file", err
	}
	return samples, "file", nil
}

func adminTestSampleTableExists(ctx context.Context) bool {
	if appInfra.mysql == nil {
		return false
	}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var tableName string
	err := appInfra.mysql.QueryRowContext(queryCtx, `
SELECT TABLE_NAME
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_test_samples'
LIMIT 1
`).Scan(&tableName)
	return err == nil
}

func loadAdminTestSamplesFromMySQL(ctx context.Context) ([]adminTestSample, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := appInfra.mysql.QueryContext(queryCtx, `
SELECT platform_key, display_name, sample_url, enabled, note, sort_order, updated_at
FROM platform_test_samples
ORDER BY sort_order ASC, display_name ASC, platform_key ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []adminTestSample
	for rows.Next() {
		var item adminTestSample
		var enabled int
		if err := rows.Scan(&item.Platform, &item.Name, &item.URL, &enabled, &item.Note, &item.SortOrder, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func saveAdminTestSamplesToMySQL(ctx context.Context, samples []adminTestSample) error {
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := appInfra.mysql.BeginTx(queryCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(queryCtx, "DELETE FROM platform_test_samples"); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(queryCtx, `
INSERT INTO platform_test_samples (platform_key, display_name, sample_url, enabled, note, sort_order)
VALUES (?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range samples {
		enabled := 0
		if item.Enabled {
			enabled = 1
		}
		if _, err := stmt.ExecContext(queryCtx, item.Platform, item.Name, item.URL, enabled, item.Note, item.SortOrder); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadAdminTestSamplesFromFile() ([]adminTestSample, error) {
	bytes, err := os.ReadFile(adminTestSamplesFilePath)
	if err != nil {
		return nil, err
	}
	var payload adminTestSamplesFile
	if err := json.Unmarshal(bytes, &payload); err != nil {
		return nil, err
	}
	return payload.Items, nil
}

func saveAdminTestSamplesToFile(samples []adminTestSample) error {
	if err := os.MkdirAll(filepath.Dir(adminTestSamplesFilePath), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(adminTestSamplesFile{
		Items:     samples,
		UpdatedAt: time.Now(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(adminTestSamplesFilePath, body, 0o644)
}

func normalizeStoredAdminTestSamples(input []adminTestSample) []adminTestSample {
	items := sanitizeAdminTestSamples(input)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SortOrder == items[j].SortOrder {
			return items[i].Name < items[j].Name
		}
		return items[i].SortOrder < items[j].SortOrder
	})
	for index := range items {
		items[index].SortOrder = index
	}
	return items
}

func defaultAdminTestSamples() []adminTestSample {
	known := make(map[string]adminTestLink)
	for _, link := range defaultAdminTestLinks {
		platform := normalizeAdminSamplePlatform(firstNonEmpty(link.Platform, detectSource(link.URL), platformForDisplayName(link.Name)))
		if platform == "" {
			platform = normalizeAdminSamplePlatform(link.Name)
		}
		link.Platform = platform
		known[platform] = link
	}

	items := make([]adminTestSample, 0, len(parser.VideoSourceInfoMapping)+len(externalAdminTestSampleNames))
	sources := make([]string, 0, len(parser.VideoSourceInfoMapping))
	for source := range parser.VideoSourceInfoMapping {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	sortOrder := 0
	for _, source := range sources {
		name := source
		if displayName, ok := platformNames[source]; ok {
			name = displayName
		}
		link := known[source]
		enabled := strings.TrimSpace(link.URL) != ""
		if link.Enabled != nil {
			enabled = *link.Enabled
		}
		items = append(items, adminTestSample{
			Platform:  source,
			Name:      firstNonEmpty(link.Name, name),
			URL:       link.URL,
			Enabled:   enabled,
			Note:      link.Note,
			SortOrder: sortOrder,
		})
		sortOrder++
		delete(known, source)
	}

	externalSources := make([]string, 0, len(externalAdminTestSampleNames))
	for source := range externalAdminTestSampleNames {
		externalSources = append(externalSources, source)
	}
	sort.Strings(externalSources)
	for _, source := range externalSources {
		link := known[source]
		enabled := strings.TrimSpace(link.URL) != ""
		if link.Enabled != nil {
			enabled = *link.Enabled
		}
		items = append(items, adminTestSample{
			Platform:  source,
			Name:      firstNonEmpty(link.Name, externalAdminTestSampleNames[source]),
			URL:       link.URL,
			Enabled:   enabled,
			Note:      link.Note,
			SortOrder: sortOrder,
		})
		sortOrder++
		delete(known, source)
	}

	for source, link := range known {
		enabled := strings.TrimSpace(link.URL) != ""
		if link.Enabled != nil {
			enabled = *link.Enabled
		}
		items = append(items, adminTestSample{
			Platform:  source,
			Name:      firstNonEmpty(link.Name, source),
			URL:       link.URL,
			Enabled:   enabled,
			Note:      link.Note,
			SortOrder: sortOrder,
		})
		sortOrder++
	}

	return items
}

func mergeAdminTestSamplesWithCatalog(input []adminTestSample) []adminTestSample {
	catalog := defaultAdminTestSamples()
	result := make([]adminTestSample, 0, len(catalog)+len(input))
	seen := make(map[string]bool, len(catalog)+len(input))
	overrides := make(map[string]adminTestSample, len(input))

	for _, item := range sanitizeAdminTestSamples(input) {
		key := adminTestSampleKey(item)
		if key != "" {
			overrides[key] = item
		}
	}

	for _, base := range catalog {
		key := adminTestSampleKey(base)
		if override, ok := overrides[key]; ok {
			base.URL = override.URL
			base.Enabled = override.Enabled
			base.Note = override.Note
			base.UpdatedAt = override.UpdatedAt
			if strings.TrimSpace(override.Name) != "" {
				base.Name = override.Name
			}
			if strings.TrimSpace(override.Platform) != "" {
				base.Platform = override.Platform
			}
		}
		result = append(result, base)
		seen[key] = true
	}

	for _, item := range sanitizeAdminTestSamples(input) {
		key := adminTestSampleKey(item)
		if key == "" || seen[key] {
			continue
		}
		if item.SortOrder <= 0 {
			item.SortOrder = len(result)
		}
		result = append(result, item)
		seen[key] = true
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].SortOrder == result[j].SortOrder {
			return result[i].Name < result[j].Name
		}
		return result[i].SortOrder < result[j].SortOrder
	})
	for index := range result {
		result[index].SortOrder = index
	}
	return result
}

func sanitizeAdminTestSamples(input []adminTestSample) []adminTestSample {
	items := make([]adminTestSample, 0, len(input))
	seen := make(map[string]bool, len(input))
	for index, item := range input {
		item.Platform = normalizeAdminSamplePlatform(firstNonEmpty(item.Platform, detectSource(item.URL), platformForDisplayName(item.Name)))
		item.Name = strings.TrimSpace(item.Name)
		item.URL = strings.TrimSpace(item.URL)
		item.Note = strings.TrimSpace(item.Note)
		if len([]rune(item.Note)) > 255 {
			item.Note = string([]rune(item.Note)[:255])
		}
		if item.SortOrder < 0 {
			item.SortOrder = index
		}
		if item.Name == "" {
			item.Name = sampleDisplayName(item.Platform)
		}
		if item.Platform == "" && item.Name != "" {
			item.Platform = normalizeAdminSamplePlatform(normalizeAdminSampleKey(item.Name))
		}
		if item.Platform == "" && item.URL != "" {
			item.Platform = fmt.Sprintf("custom-%d", index+1)
		}
		if item.Platform == "" && item.Name == "" && item.URL == "" {
			continue
		}
		basePlatform := item.Platform
		for suffix := 2; seen[item.Platform]; suffix++ {
			item.Platform = fmt.Sprintf("%s-%d", basePlatform, suffix)
		}
		seen[item.Platform] = true
		items = append(items, item)
	}
	return items
}

func adminTestLinksFromSamples(samples []adminTestSample) []adminTestLink {
	items := make([]adminTestLink, 0, len(samples))
	for _, sample := range sanitizeAdminTestSamples(samples) {
		if !sample.Enabled || strings.TrimSpace(sample.URL) == "" {
			continue
		}
		items = append(items, adminTestLink{
			Platform: sample.Platform,
			Name:     firstNonEmpty(sample.Name, sampleDisplayName(sample.Platform)),
			URL:      sample.URL,
		})
	}
	return sanitizeAdminTestLinks(items)
}

func adminTestSampleKey(item adminTestSample) string {
	if item.Platform != "" {
		return "platform:" + normalizeAdminSamplePlatform(item.Platform)
	}
	if item.Name != "" {
		return "name:" + normalizeAdminSampleKey(item.Name)
	}
	if item.URL != "" {
		return "url:" + item.URL
	}
	return ""
}

func platformForDisplayName(name string) string {
	key := normalizeAdminSampleKey(name)
	if key == "" {
		return ""
	}
	for platform, displayName := range platformNames {
		if normalizeAdminSampleKey(displayName) == key {
			return platform
		}
	}
	for platform, displayName := range externalAdminTestSampleNames {
		if normalizeAdminSampleKey(displayName) == key {
			return platform
		}
	}
	switch key {
	case "twitterx", "xtwitter":
		return "twitter"
	default:
		return ""
	}
}

func sampleDisplayName(platform string) string {
	platform = normalizeAdminSamplePlatform(platform)
	if name, ok := platformNames[platform]; ok {
		return name
	}
	if name, ok := externalAdminTestSampleNames[platform]; ok {
		return name
	}
	return platform
}

func normalizeAdminSamplePlatform(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "xiaohongshu":
		return "redbook"
	case "kgqq":
		return "quanminkge"
	case "ixigua":
		return "xigua"
	case "x", "twitterx", "xtwitter":
		return "twitter"
	default:
		return value
	}
}

func normalizeAdminSampleKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "_", "", "-", "", "/", "")
	return replacer.Replace(value)
}
