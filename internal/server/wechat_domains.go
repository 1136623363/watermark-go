package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	wechatDomainExportLimit = 200
	wechatDomainExportText  = "cache/wechat-download-domains.txt"
	wechatDomainExportJSON  = "cache/wechat-download-domains.json"
)

var wechatDownloadDomainStatuses = map[string]bool{
	"pending":  true,
	"approved": true,
	"ignored":  true,
	"invalid":  true,
	"stale":    true,
}

var wechatDomainExportThrottle struct {
	mu   sync.Mutex
	last time.Time
}

type wechatDomainCandidate struct {
	Origin      string
	Host        string
	Scheme      string
	MediaType   string
	FieldPath   string
	URL         string
	ExamplePath string
}

type wechatDownloadDomainItem struct {
	ID              int64     `json:"id"`
	Origin          string    `json:"origin"`
	Host            string    `json:"host"`
	Scheme          string    `json:"scheme"`
	Platform        string    `json:"platform"`
	MediaTypes      string    `json:"mediaTypes"`
	HitCount        uint64    `json:"hitCount"`
	Status          string    `json:"status"`
	LastSampleURL   string    `json:"lastSampleUrl"`
	LastExamplePath string    `json:"lastExamplePath"`
	Note            string    `json:"note"`
	FirstSeenAt     time.Time `json:"firstSeenAt"`
	LastSeenAt      time.Time `json:"lastSeenAt"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type wechatDomainStats struct {
	Total    int `json:"total"`
	Pending  int `json:"pending"`
	Approved int `json:"approved"`
	Ignored  int `json:"ignored"`
	Invalid  int `json:"invalid"`
	Stale    int `json:"stale"`
}

type wechatDomainExportPayload struct {
	UpdatedAt  time.Time `json:"updatedAt"`
	Limit      int       `json:"limit"`
	Count      int       `json:"count"`
	Domains    []string  `json:"domains"`
	WechatText string    `json:"wechatText"`
	TextPath   string    `json:"textPath"`
	JSONPath   string    `json:"jsonPath"`
}

func recordWechatDownloadDomains(ctx context.Context, sourceURL string, data parseData, trigger string) {
	if appInfra.mysql == nil {
		return
	}
	candidates := extractWechatDomainCandidates(data)
	if len(candidates) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := upsertWechatDownloadDomains(dbCtx, appInfra.mysql, sourceURL, data, candidates); err != nil {
		logWarnf("wechat download domain collect failed trigger=%s target=%s error=%v", trigger, targetForLog(sourceURL), err)
		return
	}
	if _, err := maybeRefreshWechatDownloadDomainExport(dbCtx, appInfra.mysql); err != nil {
		logWarnf("wechat download domain export failed trigger=%s error=%v", trigger, err)
	}
}

func extractWechatDomainCandidates(data parseData) []wechatDomainCandidate {
	type rawCandidate struct {
		raw       string
		mediaType string
		fieldPath string
	}
	rawItems := make([]rawCandidate, 0, 8+len(data.Downloads)+len(data.Images)+len(data.Pics))
	add := func(rawValue, mediaType, fieldPath string) {
		rawValue = strings.TrimSpace(rawValue)
		if rawValue == "" {
			return
		}
		rawItems = append(rawItems, rawCandidate{raw: rawValue, mediaType: mediaType, fieldPath: fieldPath})
	}

	for index, item := range data.Downloads {
		mediaType := inferDownloadMediaType(data.Type, item.Label)
		add(item.URL, mediaType, "downloads."+strconv.Itoa(index)+".url")
	}
	for index, item := range data.Images {
		add(item, "image", "images."+strconv.Itoa(index))
	}
	for index, item := range data.Pics {
		add(item, "image", "pics."+strconv.Itoa(index))
	}
	add(data.Cover, "cover", "cover")
	add(data.Avatar, "image", "avatar")
	add(data.Music, "audio", "music")
	add(data.PlayAddr, "video", "playAddr")
	add(data.Preview, "video", "previewUrl")
	add(data.M3U8, "m3u8", "m3u8")

	seen := make(map[string]bool, len(rawItems))
	items := make([]wechatDomainCandidate, 0, len(rawItems))
	for _, raw := range rawItems {
		candidate, ok := normalizeWechatDomainCandidate(raw.raw, raw.mediaType, raw.fieldPath)
		if !ok {
			continue
		}
		key := candidate.URL + "|" + candidate.MediaType
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, candidate)
	}
	return items
}

func inferDownloadMediaType(resultType string, label string) string {
	text := strings.ToLower(strings.TrimSpace(resultType + " " + label))
	switch {
	case strings.Contains(text, "audio") || strings.Contains(text, "music") || strings.Contains(text, "音频"):
		return "audio"
	case strings.Contains(text, "image") || strings.Contains(text, "pic") || strings.Contains(text, "图片"):
		return "image"
	case strings.Contains(text, "m3u8"):
		return "m3u8"
	case strings.Contains(text, "cover") || strings.Contains(text, "封面"):
		return "cover"
	default:
		return "video"
	}
}

func normalizeWechatDomainCandidate(rawURL, mediaType, fieldPath string) (wechatDomainCandidate, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return wechatDomainCandidate{}, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" {
		return wechatDomainCandidate{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || !isPublicDomainHost(host) {
		return wechatDomainCandidate{}, false
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if len(path) > 512 {
		path = path[:512]
	}
	return wechatDomainCandidate{
		Origin:      scheme + "://" + host,
		Host:        host,
		Scheme:      scheme,
		MediaType:   strings.TrimSpace(mediaType),
		FieldPath:   strings.TrimSpace(fieldPath),
		URL:         parsed.String(),
		ExamplePath: path,
	}, true
}

func isPublicDomainHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
	}
	if !strings.Contains(host, ".") {
		return false
	}
	return host != "localhost"
}

func upsertWechatDownloadDomains(ctx context.Context, db *sql.DB, sourceURL string, data parseData, candidates []wechatDomainCandidate) error {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Origin != candidates[j].Origin {
			return candidates[i].Origin < candidates[j].Origin
		}
		if candidates[i].MediaType != candidates[j].MediaType {
			return candidates[i].MediaType < candidates[j].MediaType
		}
		return candidates[i].URL < candidates[j].URL
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	platform := strings.TrimSpace(data.Platform)
	shareID := strings.TrimSpace(data.ShareID)
	for _, candidate := range candidates {
		domainID, err := upsertWechatDomain(ctx, tx, sourceURL, platform, candidate)
		if err != nil {
			return err
		}
		if err := insertWechatDomainObservation(ctx, tx, domainID, sourceURL, platform, shareID, candidate); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func upsertWechatDomain(ctx context.Context, tx *sql.Tx, sourceURL string, platform string, candidate wechatDomainCandidate) (int64, error) {
	var existing struct {
		ID        int64
		MediaType string
	}
	err := tx.QueryRowContext(ctx, `
SELECT id, media_types
FROM wechat_download_domains
WHERE origin = ?
FOR UPDATE`, candidate.Origin).Scan(&existing.ID, &existing.MediaType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	mediaTypes := mergeCSV(existing.MediaType, candidate.MediaType)
	if errors.Is(err, sql.ErrNoRows) {
		result, err := tx.ExecContext(ctx, `
INSERT INTO wechat_download_domains
  (origin, host, scheme, platform, media_types, hit_count, status, last_sample_url, last_example_path, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, 1, 'pending', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			candidate.Origin,
			candidate.Host,
			candidate.Scheme,
			platform,
			mediaTypes,
			sourceURL,
			candidate.ExamplePath,
		)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}

	_, err = tx.ExecContext(ctx, `
UPDATE wechat_download_domains
SET
  platform = CASE WHEN ? <> '' THEN ? ELSE platform END,
  media_types = ?,
  hit_count = hit_count + 1,
  last_sample_url = ?,
  last_example_path = ?,
  last_seen_at = CURRENT_TIMESTAMP
WHERE id = ?`,
		platform,
		platform,
		mediaTypes,
		sourceURL,
		candidate.ExamplePath,
		existing.ID,
	)
	if err != nil {
		return 0, err
	}
	return existing.ID, nil
}

func insertWechatDomainObservation(ctx context.Context, tx *sql.Tx, domainID int64, sourceURL string, platform string, shareID string, candidate wechatDomainCandidate) error {
	hash := sha256.Sum256([]byte(candidate.URL))
	urlHash := hex.EncodeToString(hash[:])
	_, err := tx.ExecContext(ctx, `
INSERT INTO wechat_download_domain_observations
  (domain_id, origin, host, platform, source_url, result_share_id, media_type, field_path, url_hash, example_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  domain_id = VALUES(domain_id),
  platform = VALUES(platform),
  source_url = VALUES(source_url),
  result_share_id = VALUES(result_share_id),
  media_type = VALUES(media_type),
  example_path = VALUES(example_path)`,
		domainID,
		candidate.Origin,
		candidate.Host,
		platform,
		sourceURL,
		shareID,
		candidate.MediaType,
		candidate.FieldPath,
		urlHash,
		candidate.ExamplePath,
	)
	return err
}

func mergeCSV(existing string, values ...string) string {
	parts := strings.Split(existing, ",")
	seen := make(map[string]bool, len(parts)+len(values))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			seen[part] = true
		}
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = true
		}
	}
	merged := make([]string, 0, len(seen))
	for value := range seen {
		merged = append(merged, value)
	}
	sort.Strings(merged)
	return strings.Join(merged, ",")
}

func refreshWechatDownloadDomainExport(ctx context.Context, db *sql.DB) (wechatDomainExportPayload, error) {
	domains, err := pendingWechatDownloadDomains(ctx, db)
	if err != nil {
		return wechatDomainExportPayload{}, err
	}
	payload := wechatDomainExportPayload{
		UpdatedAt:  time.Now(),
		Limit:      wechatDomainExportLimit,
		Count:      len(domains),
		Domains:    domains,
		WechatText: strings.Join(domains, ";"),
		TextPath:   wechatDomainExportText,
		JSONPath:   wechatDomainExportJSON,
	}
	if err := os.MkdirAll(filepath.Dir(wechatDomainExportText), 0755); err != nil {
		return payload, err
	}
	if err := os.WriteFile(wechatDomainExportText, []byte(payload.WechatText), 0644); err != nil {
		return payload, err
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return payload, err
	}
	if err := os.WriteFile(wechatDomainExportJSON, body, 0644); err != nil {
		return payload, err
	}
	return payload, nil
}

func maybeRefreshWechatDownloadDomainExport(ctx context.Context, db *sql.DB) (wechatDomainExportPayload, error) {
	wechatDomainExportThrottle.mu.Lock()
	defer wechatDomainExportThrottle.mu.Unlock()
	if time.Since(wechatDomainExportThrottle.last) < 30*time.Second {
		return wechatDomainExportPayload{}, nil
	}
	payload, err := refreshWechatDownloadDomainExport(ctx, db)
	if err == nil {
		wechatDomainExportThrottle.last = time.Now()
	}
	return payload, err
}

func pendingWechatDownloadDomains(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT origin
FROM wechat_download_domains
WHERE status = 'pending'
ORDER BY hit_count DESC, last_seen_at DESC, origin ASC
LIMIT ?`, wechatDomainExportLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	domains := make([]string, 0, wechatDomainExportLimit)
	for rows.Next() {
		var origin string
		if err := rows.Scan(&origin); err != nil {
			return nil, err
		}
		domains = append(domains, origin)
	}
	return domains, rows.Err()
}

func listWechatDownloadDomains(ctx context.Context, db *sql.DB, status string, limit int) ([]wechatDownloadDomainItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	status = strings.TrimSpace(status)
	args := []any{}
	where := ""
	if status != "" && status != "all" {
		where = "WHERE status = ?"
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT id, origin, host, scheme, platform, media_types, hit_count, status, COALESCE(last_sample_url, ''), last_example_path, note, first_seen_at, last_seen_at, created_at, updated_at
FROM wechat_download_domains
`+where+`
ORDER BY FIELD(status, 'pending', 'approved', 'ignored', 'invalid', 'stale'), hit_count DESC, last_seen_at DESC, origin ASC
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []wechatDownloadDomainItem{}
	for rows.Next() {
		var item wechatDownloadDomainItem
		if err := rows.Scan(
			&item.ID,
			&item.Origin,
			&item.Host,
			&item.Scheme,
			&item.Platform,
			&item.MediaTypes,
			&item.HitCount,
			&item.Status,
			&item.LastSampleURL,
			&item.LastExamplePath,
			&item.Note,
			&item.FirstSeenAt,
			&item.LastSeenAt,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func wechatDownloadDomainStats(ctx context.Context, db *sql.DB) (wechatDomainStats, error) {
	rows, err := db.QueryContext(ctx, `
SELECT status, COUNT(*)
FROM wechat_download_domains
GROUP BY status`)
	if err != nil {
		return wechatDomainStats{}, err
	}
	defer rows.Close()
	var stats wechatDomainStats
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return wechatDomainStats{}, err
		}
		stats.Total += count
		switch status {
		case "pending":
			stats.Pending = count
		case "approved":
			stats.Approved = count
		case "ignored":
			stats.Ignored = count
		case "invalid":
			stats.Invalid = count
		case "stale":
			stats.Stale = count
		}
	}
	return stats, rows.Err()
}

func handleAdminListWechatDomains(c *gin.Context) {
	if appInfra.mysql == nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "MySQL 未启用，无法统计 downloadFile 合法域名"})
		return
	}
	status := strings.TrimSpace(c.DefaultQuery("status", "pending"))
	if status != "all" && !wechatDownloadDomainStatuses[status] {
		status = "pending"
	}
	limit := intFromQuery(c, "limit", 200)
	items, err := listWechatDownloadDomains(c.Request.Context(), appInfra.mysql, status, limit)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	stats, err := wechatDownloadDomainStats(c.Request.Context(), appInfra.mysql)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	exportPayload, err := refreshWechatDownloadDomainExport(c.Request.Context(), appInfra.mysql)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"items":  items,
			"stats":  stats,
			"export": exportPayload,
			"store":  "mysql",
		},
	})
}

func handleAdminUpdateWechatDomain(c *gin.Context) {
	if appInfra.mysql == nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "MySQL 未启用，无法更新 downloadFile 合法域名"})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "invalid domain id"})
		return
	}
	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "invalid domain payload"})
		return
	}
	req.Status = strings.TrimSpace(req.Status)
	if !wechatDownloadDomainStatuses[req.Status] {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "invalid domain status"})
		return
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > 255 {
		note = string([]rune(note)[:255])
	}
	result, err := appInfra.mysql.ExecContext(c.Request.Context(), `
UPDATE wechat_download_domains
SET status = ?, note = ?
WHERE id = ?`, req.Status, note, id)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		c.JSON(http.StatusNotFound, httpResponse{Code: 1004, Msg: "domain not found"})
		return
	}
	exportPayload, err := refreshWechatDownloadDomainExport(c.Request.Context(), appInfra.mysql)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	writeAdminAudit(c, "wechat_download_domain.update", "wechat_download_domain", strconv.FormatInt(id, 10), gin.H{"status": req.Status})
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: gin.H{"updated": true, "export": exportPayload}})
}

func handleAdminRefreshWechatDomainExport(c *gin.Context) {
	if appInfra.mysql == nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "MySQL 未启用，无法导出 downloadFile 合法域名"})
		return
	}
	exportPayload, err := refreshWechatDownloadDomainExport(c.Request.Context(), appInfra.mysql)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: exportPayload})
}
