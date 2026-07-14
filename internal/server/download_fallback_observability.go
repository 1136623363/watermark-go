package server

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"watermark-backend/internal/runtimecfg"
)

const (
	downloadFallbackEventStatusIssued    = "issued"
	downloadFallbackEventStatusQueued    = "queued"
	downloadFallbackEventStatusReused    = "reused"
	downloadFallbackEventStatusRunning   = "running"
	downloadFallbackEventStatusCompleted = "completed"
	downloadFallbackEventStatusFailed    = "failed"
)

type downloadFallbackClientMeta struct {
	UserID    int64  `json:"userId,omitempty"`
	UID       string `json:"uid,omitempty"`
	PublicID  string `json:"publicId,omitempty"`
	ClientIP  string `json:"clientIp,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`
}

type downloadFallbackEventRecord struct {
	RequestID        string    `json:"requestId"`
	Mode             string    `json:"mode"`
	Status           string    `json:"status"`
	TaskID           string    `json:"taskId,omitempty"`
	ShareID          string    `json:"shareId,omitempty"`
	SourceURL        string    `json:"sourceUrl,omitempty"`
	MediaURL         string    `json:"mediaUrl,omitempty"`
	MediaType        string    `json:"mediaType,omitempty"`
	UserID           int64     `json:"userId,omitempty"`
	UID              string    `json:"uid,omitempty"`
	PublicID         string    `json:"publicId,omitempty"`
	ClientIP         string    `json:"clientIp,omitempty"`
	UserAgent        string    `json:"userAgent,omitempty"`
	NodeID           string    `json:"nodeId,omitempty"`
	NodeName         string    `json:"nodeName,omitempty"`
	NodeRole         string    `json:"nodeRole,omitempty"`
	BytesTransferred int64     `json:"bytesTransferred"`
	DurationMS       int64     `json:"durationMs"`
	OriginStatus     int       `json:"originStatus,omitempty"`
	ErrorMessage     string    `json:"errorMessage,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type downloadFallbackActiveTransfer struct {
	downloadFallbackEventRecord
	StartedAt     time.Time `json:"startedAt"`
	lastPersistAt time.Time `json:"-"`
}

type downloadFallbackActiveRegistry struct {
	mu    sync.RWMutex
	items map[string]*downloadFallbackActiveTransfer
}

type downloadFallbackAdminStats struct {
	WindowHours      int              `json:"windowHours"`
	Total            int64            `json:"total"`
	Running          int64            `json:"running"`
	Completed        int64            `json:"completed"`
	Failed           int64            `json:"failed"`
	BytesTransferred int64            `json:"bytesTransferred"`
	AvgDurationMS    int64            `json:"avgDurationMs"`
	MaxDurationMS    int64            `json:"maxDurationMs"`
	Active           int              `json:"active"`
	ActiveUsers      int              `json:"activeUsers"`
	ActiveBytes      int64            `json:"activeBytes"`
	ByMode           map[string]int64 `json:"byMode"`
	ByStatus         map[string]int64 `json:"byStatus"`
}

type downloadFallbackDBSnapshot struct {
	Store  string                           `json:"store"`
	Cache  string                           `json:"cache,omitempty"`
	Stats  downloadFallbackAdminStats       `json:"stats"`
	Active []downloadFallbackActiveTransfer `json:"active,omitempty"`
	Recent []downloadFallbackEventRecord    `json:"recent"`
}

type downloadFallbackAdminPayload struct {
	Enabled       bool                             `json:"enabled"`
	Mode          string                           `json:"mode"`
	PublicBaseURL string                           `json:"publicBaseUrl,omitempty"`
	CDNBaseURL    string                           `json:"cdnBaseUrl,omitempty"`
	Store         string                           `json:"store"`
	Cache         string                           `json:"cache,omitempty"`
	Stats         downloadFallbackAdminStats       `json:"stats"`
	Active        []downloadFallbackActiveTransfer `json:"active"`
	Recent        []downloadFallbackEventRecord    `json:"recent"`
}

type transferCountingReader struct {
	reader     io.Reader
	transferID string
	written    int64
	lastUpdate time.Time
}

type transferCountingReadSeeker struct {
	reader     io.ReadSeeker
	transferID string
	written    int64
	lastUpdate time.Time
}

var globalDownloadFallbackTransfers = &downloadFallbackActiveRegistry{
	items: make(map[string]*downloadFallbackActiveTransfer),
}

func handleAdminDownloadFallback(c *gin.Context) {
	hours := intFromQuery(c, "hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 168 {
		hours = 168
	}
	limit := intFromQuery(c, "limit", 100)
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	payload := currentDownloadFallbackAdminPayload(c.Request.Context(), hours, limit)
	c.JSON(200, httpResponse{Code: 0, Msg: "ok", Data: payload})
}

func currentDownloadFallbackAdminPayload(ctx context.Context, hours int, limit int) downloadFallbackAdminPayload {
	dbSnapshot := loadDownloadFallbackDBSnapshotCached(ctx, hours, limit)
	active := mergeDownloadFallbackActive(dbSnapshot.Active, globalDownloadFallbackTransfers.list())
	stats := dbSnapshot.Stats
	applyActiveDownloadFallbackStats(&stats, active)
	return downloadFallbackAdminPayload{
		Enabled:       runtimecfg.DownloadFallbackEnabled(),
		Mode:          runtimecfg.DownloadFallbackMode(),
		PublicBaseURL: runtimecfg.DownloadFallbackPublicBaseURL(),
		CDNBaseURL:    runtimecfg.DownloadFallbackCDNBaseURL(),
		Store:         dbSnapshot.Store,
		Cache:         dbSnapshot.Cache,
		Stats:         stats,
		Active:        active,
		Recent:        dbSnapshot.Recent,
	}
}

func loadDownloadFallbackDBSnapshotCached(ctx context.Context, hours int, limit int) downloadFallbackDBSnapshot {
	cacheKey := fmt.Sprintf("download_fallback:%d:%d", hours, limit)
	var cached downloadFallbackDBSnapshot
	if adminRedisCacheGet(cacheKey, &cached) {
		cached.Cache = "redis"
		return cached
	}
	loaded := loadDownloadFallbackDBSnapshot(ctx, hours, limit)
	if loaded.Store == "mysql" {
		adminRedisCacheSet(cacheKey, loaded, 3*time.Second)
	}
	return loaded
}

func loadDownloadFallbackDBSnapshot(ctx context.Context, hours int, limit int) downloadFallbackDBSnapshot {
	payload := downloadFallbackDBSnapshot{
		Store: "memory",
		Stats: downloadFallbackAdminStats{
			WindowHours: hours,
			ByMode:      map[string]int64{},
			ByStatus:    map[string]int64{},
		},
		Active: []downloadFallbackActiveTransfer{},
		Recent: []downloadFallbackEventRecord{},
	}
	if appInfra.mysql == nil {
		return payload
	}
	payload.Store = "mysql"
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	queryCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	var avg sql.NullFloat64
	if err := appInfra.mysql.QueryRowContext(queryCtx, `
SELECT
  COUNT(*),
  COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(bytes_transferred), 0),
  AVG(duration_ms),
  COALESCE(MAX(duration_ms), 0)
FROM download_fallback_events
WHERE created_at >= ?`, since).Scan(
		&payload.Stats.Total,
		&payload.Stats.Running,
		&payload.Stats.Completed,
		&payload.Stats.Failed,
		&payload.Stats.BytesTransferred,
		&avg,
		&payload.Stats.MaxDurationMS,
	); err != nil {
		logWarnf("download fallback stats query failed: %v", err)
		return payload
	}
	if avg.Valid {
		payload.Stats.AvgDurationMS = int64(avg.Float64)
	}

	loadDownloadFallbackBreakdown(queryCtx, since, "mode", payload.Stats.ByMode)
	loadDownloadFallbackBreakdown(queryCtx, since, "status", payload.Stats.ByStatus)

	activeRows, err := appInfra.mysql.QueryContext(queryCtx, `
SELECT
  e.request_id, e.mode, e.status, e.task_id, e.share_id, e.source_url, e.media_url, e.media_type,
  COALESCE(e.user_id, 0), COALESCE(u.public_id, ''), INET6_NTOA(e.client_ip), e.user_agent,
  e.node_id, e.node_name, e.node_role, e.bytes_transferred, e.duration_ms, e.origin_status,
  e.error_message, e.created_at, e.updated_at
FROM download_fallback_events e
LEFT JOIN app_users u ON u.id = e.user_id
WHERE e.status = 'running' AND e.updated_at >= ?
ORDER BY e.updated_at DESC
LIMIT ?`, time.Now().Add(-30*time.Minute), limit)
	if err == nil {
		defer activeRows.Close()
		for activeRows.Next() {
			item, scanErr := scanDownloadFallbackEventRecord(activeRows)
			if scanErr != nil {
				logWarnf("download fallback active scan failed: %v", scanErr)
				break
			}
			payload.Active = append(payload.Active, downloadFallbackActiveTransfer{
				downloadFallbackEventRecord: item,
				StartedAt:                   item.CreatedAt,
			})
		}
		if err := activeRows.Err(); err != nil {
			logWarnf("download fallback active rows failed: %v", err)
		}
	} else {
		logWarnf("download fallback active query failed: %v", err)
	}

	recentLimit := limit * 4
	if recentLimit < limit {
		recentLimit = limit
	}
	if recentLimit > 500 {
		recentLimit = 500
	}
	rows, err := appInfra.mysql.QueryContext(queryCtx, `
SELECT
  e.request_id, e.mode, e.status, e.task_id, e.share_id, e.source_url, e.media_url, e.media_type,
  COALESCE(e.user_id, 0), COALESCE(u.public_id, ''), INET6_NTOA(e.client_ip), e.user_agent,
  e.node_id, e.node_name, e.node_role, e.bytes_transferred, e.duration_ms, e.origin_status,
  e.error_message, e.created_at, e.updated_at
FROM download_fallback_events e
LEFT JOIN app_users u ON u.id = e.user_id
ORDER BY e.updated_at DESC
LIMIT ?`, recentLimit)
	if err != nil {
		logWarnf("download fallback recent query failed: %v", err)
		return payload
	}
	defer rows.Close()

	for rows.Next() {
		item, err := scanDownloadFallbackEventRecord(rows)
		if err != nil {
			logWarnf("download fallback recent scan failed: %v", err)
			return payload
		}
		payload.Recent = append(payload.Recent, item)
	}
	if err := rows.Err(); err != nil {
		logWarnf("download fallback recent rows failed: %v", err)
	}
	payload.Recent = compactDownloadFallbackRecentEvents(payload.Recent, limit)
	return payload
}

func compactDownloadFallbackRecentEvents(items []downloadFallbackEventRecord, limit int) []downloadFallbackEventRecord {
	if len(items) == 0 {
		return items
	}
	seen := make(map[string]int, len(items))
	result := make([]downloadFallbackEventRecord, 0, len(items))
	for _, item := range items {
		key := downloadFallbackEventCompactKey(item)
		if index, ok := seen[key]; ok {
			result[index] = mergeDownloadFallbackEventRecords(result[index], item)
			continue
		}
		seen[key] = len(result)
		result = append(result, item)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

func downloadFallbackEventCompactKey(item downloadFallbackEventRecord) string {
	mode := normalizeDownloadFallbackMode(item.Mode)
	if item.TaskID != "" {
		return strings.Join([]string{mode, "task", item.TaskID}, "|")
	}
	hasStableTarget := item.ShareID != "" || item.SourceURL != "" || item.MediaURL != "" || item.UserID > 0 || item.PublicID != "" || item.ClientIP != ""
	if hasStableTarget {
		return strings.Join([]string{
			mode,
			"target",
			strings.TrimSpace(item.ShareID),
			strings.TrimSpace(item.SourceURL),
			strings.TrimSpace(item.MediaURL),
			strings.TrimSpace(item.MediaType),
			fmt.Sprintf("%d", item.UserID),
			strings.TrimSpace(item.PublicID),
			strings.TrimSpace(item.ClientIP),
		}, "|")
	}
	return strings.Join([]string{
		mode,
		"request",
		strings.TrimSpace(item.RequestID),
	}, "|")
}

func mergeDownloadFallbackEventRecords(newer downloadFallbackEventRecord, older downloadFallbackEventRecord) downloadFallbackEventRecord {
	if newer.TaskID == "" {
		newer.TaskID = older.TaskID
	}
	if newer.ShareID == "" {
		newer.ShareID = older.ShareID
	}
	if newer.SourceURL == "" {
		newer.SourceURL = older.SourceURL
	}
	if newer.MediaURL == "" {
		newer.MediaURL = older.MediaURL
	}
	if newer.MediaType == "" {
		newer.MediaType = older.MediaType
	}
	if newer.UserID == 0 {
		newer.UserID = older.UserID
	}
	if newer.UID == "" {
		newer.UID = older.UID
	}
	if newer.PublicID == "" {
		newer.PublicID = older.PublicID
	}
	if newer.ClientIP == "" {
		newer.ClientIP = older.ClientIP
	}
	if newer.UserAgent == "" {
		newer.UserAgent = older.UserAgent
	}
	if newer.NodeID == "" {
		newer.NodeID = older.NodeID
	}
	if newer.NodeName == "" {
		newer.NodeName = older.NodeName
	}
	if newer.NodeRole == "" {
		newer.NodeRole = older.NodeRole
	}
	if newer.BytesTransferred == 0 && older.BytesTransferred > 0 {
		newer.BytesTransferred = older.BytesTransferred
	}
	if newer.DurationMS == 0 && older.DurationMS > 0 {
		newer.DurationMS = older.DurationMS
	}
	if newer.OriginStatus == 0 {
		newer.OriginStatus = older.OriginStatus
	}
	if newer.ErrorMessage == "" {
		newer.ErrorMessage = older.ErrorMessage
	}
	if newer.CreatedAt.IsZero() || (!older.CreatedAt.IsZero() && older.CreatedAt.Before(newer.CreatedAt)) {
		newer.CreatedAt = older.CreatedAt
	}
	return newer
}

func scanDownloadFallbackEventRecord(rows *sql.Rows) (downloadFallbackEventRecord, error) {
	var item downloadFallbackEventRecord
	var sourceURL sql.NullString
	var mediaURL sql.NullString
	var publicID sql.NullString
	var clientIP sql.NullString
	err := rows.Scan(
		&item.RequestID,
		&item.Mode,
		&item.Status,
		&item.TaskID,
		&item.ShareID,
		&sourceURL,
		&mediaURL,
		&item.MediaType,
		&item.UserID,
		&publicID,
		&clientIP,
		&item.UserAgent,
		&item.NodeID,
		&item.NodeName,
		&item.NodeRole,
		&item.BytesTransferred,
		&item.DurationMS,
		&item.OriginStatus,
		&item.ErrorMessage,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		return item, err
	}
	item.SourceURL = sourceURL.String
	item.MediaURL = mediaURL.String
	item.PublicID = publicID.String
	item.ClientIP = clientIP.String
	item.UID = downloadFallbackUID(item.UserID, item.PublicID)
	return item, nil
}

func loadDownloadFallbackBreakdown(ctx context.Context, since time.Time, column string, target map[string]int64) {
	if appInfra.mysql == nil || target == nil {
		return
	}
	if column != "mode" && column != "status" {
		return
	}
	rows, err := appInfra.mysql.QueryContext(ctx, "SELECT "+column+", COUNT(*) FROM download_fallback_events WHERE created_at >= ? GROUP BY "+column, since)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int64
		if err := rows.Scan(&key, &count); err == nil {
			target[key] = count
		}
	}
}

func applyActiveDownloadFallbackStats(stats *downloadFallbackAdminStats, active []downloadFallbackActiveTransfer) {
	if stats == nil {
		return
	}
	if stats.ByMode == nil {
		stats.ByMode = map[string]int64{}
	}
	if stats.ByStatus == nil {
		stats.ByStatus = map[string]int64{}
	}
	users := map[string]struct{}{}
	stats.Active = len(active)
	for _, item := range active {
		stats.ActiveBytes += item.BytesTransferred
		if uid := firstNonEmptyString(item.UID, downloadFallbackUID(item.UserID, item.PublicID)); uid != "" {
			users["u:"+uid] = struct{}{}
		} else if item.ClientIP != "" {
			users["ip:"+item.ClientIP] = struct{}{}
		}
	}
	stats.ActiveUsers = len(users)
}

func mergeDownloadFallbackActive(left []downloadFallbackActiveTransfer, right []downloadFallbackActiveTransfer) []downloadFallbackActiveTransfer {
	if len(left) == 0 {
		return right
	}
	if len(right) == 0 {
		return left
	}
	merged := make([]downloadFallbackActiveTransfer, 0, len(left)+len(right))
	seen := map[string]struct{}{}
	for _, item := range append(right, left...) {
		if item.RequestID == "" {
			continue
		}
		if _, ok := seen[item.RequestID]; ok {
			continue
		}
		seen[item.RequestID] = struct{}{}
		merged = append(merged, item)
	}
	sortDownloadFallbackActive(merged)
	return merged
}

func beginDownloadFallbackTransfer(c *gin.Context, mode string, req downloadFallbackRequest, taskID string) *downloadFallbackActiveTransfer {
	now := time.Now()
	node := currentClusterNodeInfo()
	meta := downloadFallbackClientMetaFromContext(c)
	if meta.UserID == 0 && req.UserID > 0 {
		meta.UserID = req.UserID
	}
	if meta.PublicID == "" {
		meta.PublicID = strings.TrimSpace(req.PublicID)
	}
	meta.UID = downloadFallbackUID(meta.UserID, meta.PublicID)
	if clientIP := strings.TrimSpace(req.ClientIP); clientIP != "" {
		meta.ClientIP = clientIP
	}
	if userAgent := strings.TrimSpace(req.UserAgent); userAgent != "" {
		meta.UserAgent = userAgent
	}
	record := downloadFallbackEventRecord{
		RequestID: newDownloadFallbackObservationRequestID(),
		Mode:      normalizeDownloadFallbackMode(mode),
		Status:    downloadFallbackEventStatusRunning,
		TaskID:    strings.TrimSpace(taskID),
		ShareID:   strings.TrimSpace(req.ShareID),
		SourceURL: strings.TrimSpace(req.SourceURL),
		MediaURL:  strings.TrimSpace(req.MediaURL),
		MediaType: normalizeDownloadFallbackMediaType(req.MediaType),
		UserID:    meta.UserID,
		UID:       meta.UID,
		PublicID:  meta.PublicID,
		ClientIP:  meta.ClientIP,
		UserAgent: limitRunes(meta.UserAgent, 512),
		NodeID:    node.ID,
		NodeName:  node.Name,
		NodeRole:  node.Role,
		CreatedAt: now,
		UpdatedAt: now,
	}
	item := &downloadFallbackActiveTransfer{
		downloadFallbackEventRecord: record,
		StartedAt:                   now,
	}
	globalDownloadFallbackTransfers.start(item)
	go persistDownloadFallbackEventBestEffort(record)
	return item
}

func finishDownloadFallbackTransfer(transfer *downloadFallbackActiveTransfer, status string, bytes int64, originStatus int, message string) {
	if transfer == nil {
		return
	}
	now := time.Now()
	duration := now.Sub(transfer.StartedAt).Milliseconds()
	if status == "" {
		status = downloadFallbackEventStatusCompleted
	}
	if bytes < 0 {
		bytes = 0
	}
	record := transfer.downloadFallbackEventRecord
	record.Status = status
	record.BytesTransferred = bytes
	record.DurationMS = duration
	record.OriginStatus = originStatus
	record.ErrorMessage = compactTextForColumn(message, 1024)
	record.UpdatedAt = now
	globalDownloadFallbackTransfers.finish(record)
	persistDownloadFallbackEventBestEffort(record)
}

func recordDownloadFallbackEvent(c *gin.Context, req downloadFallbackRequest, mode string, status string, taskID string, bytes int64, durationMS int64, originStatus int, message string) {
	now := time.Now()
	node := currentClusterNodeInfo()
	meta := downloadFallbackClientMetaFromContext(c)
	if meta.UserID == 0 && req.UserID > 0 {
		meta.UserID = req.UserID
	}
	if meta.PublicID == "" {
		meta.PublicID = strings.TrimSpace(req.PublicID)
	}
	meta.UID = downloadFallbackUID(meta.UserID, meta.PublicID)
	if clientIP := strings.TrimSpace(req.ClientIP); clientIP != "" {
		meta.ClientIP = clientIP
	}
	if userAgent := strings.TrimSpace(req.UserAgent); userAgent != "" {
		meta.UserAgent = userAgent
	}
	record := downloadFallbackEventRecord{
		RequestID:        newDownloadFallbackObservationRequestID(),
		Mode:             normalizeDownloadFallbackMode(mode),
		Status:           strings.TrimSpace(status),
		TaskID:           strings.TrimSpace(taskID),
		ShareID:          strings.TrimSpace(req.ShareID),
		SourceURL:        strings.TrimSpace(req.SourceURL),
		MediaURL:         strings.TrimSpace(req.MediaURL),
		MediaType:        normalizeDownloadFallbackMediaType(req.MediaType),
		UserID:           meta.UserID,
		UID:              meta.UID,
		PublicID:         meta.PublicID,
		ClientIP:         meta.ClientIP,
		UserAgent:        limitRunes(meta.UserAgent, 512),
		NodeID:           node.ID,
		NodeName:         node.Name,
		NodeRole:         node.Role,
		BytesTransferred: bytes,
		DurationMS:       durationMS,
		OriginStatus:     originStatus,
		ErrorMessage:     compactTextForColumn(message, 1024),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	go persistDownloadFallbackEventBestEffort(record)
}

func newDownloadFallbackObservationRequestID() string {
	requestID, err := secureRandomHex(16)
	if err != nil {
		logErrorf("secure entropy unavailable for download fallback observation request id")
		return ""
	}
	return requestID
}

func persistDownloadFallbackEventBestEffort(record downloadFallbackEventRecord) {
	if appInfra.mysql == nil || record.RequestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_, err := appInfra.mysql.ExecContext(ctx, `
INSERT INTO download_fallback_events (
  request_id, mode, status, task_id, share_id, source_url, media_url, media_type,
  user_id, client_ip, user_agent, node_id, node_name, node_role,
  bytes_transferred, duration_ms, origin_status, error_message, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  mode = VALUES(mode),
  status = CASE
    WHEN status IN ('completed', 'failed') AND VALUES(status) = 'running' THEN status
    ELSE VALUES(status)
  END,
  task_id = VALUES(task_id),
  share_id = VALUES(share_id),
  source_url = VALUES(source_url),
  media_url = VALUES(media_url),
  media_type = VALUES(media_type),
  user_id = VALUES(user_id),
  client_ip = VALUES(client_ip),
  user_agent = VALUES(user_agent),
  node_id = VALUES(node_id),
  node_name = VALUES(node_name),
  node_role = VALUES(node_role),
  bytes_transferred = GREATEST(bytes_transferred, VALUES(bytes_transferred)),
  duration_ms = GREATEST(duration_ms, VALUES(duration_ms)),
  origin_status = IF(VALUES(origin_status) > 0, VALUES(origin_status), origin_status),
  error_message = IF(VALUES(error_message) <> '', VALUES(error_message), error_message),
  updated_at = GREATEST(updated_at, VALUES(updated_at))
`,
		record.RequestID,
		record.Mode,
		record.Status,
		record.TaskID,
		record.ShareID,
		nullIfEmpty(record.SourceURL),
		nullIfEmpty(record.MediaURL),
		record.MediaType,
		nullInt64(record.UserID),
		clientIPBytes(record.ClientIP),
		limitRunes(record.UserAgent, 512),
		record.NodeID,
		record.NodeName,
		record.NodeRole,
		uint64FromInt64(record.BytesTransferred),
		uint64FromInt64(record.DurationMS),
		record.OriginStatus,
		limitRunes(record.ErrorMessage, 1024),
		record.CreatedAt,
		record.UpdatedAt,
	)
	if err != nil {
		logWarnf("download fallback event write failed mode=%s status=%s task=%s error=%v", record.Mode, record.Status, record.TaskID, err)
	}
}

func (registry *downloadFallbackActiveRegistry) start(item *downloadFallbackActiveTransfer) {
	if registry == nil || item == nil || item.RequestID == "" {
		return
	}
	registry.mu.Lock()
	registry.items[item.RequestID] = item
	registry.mu.Unlock()
}

func (registry *downloadFallbackActiveRegistry) updateBytes(requestID string, bytes int64) {
	if registry == nil || requestID == "" {
		return
	}
	var persist *downloadFallbackEventRecord
	registry.mu.Lock()
	if item := registry.items[requestID]; item != nil {
		item.BytesTransferred = bytes
		now := time.Now()
		item.UpdatedAt = now
		item.DurationMS = now.Sub(item.StartedAt).Milliseconds()
		if now.Sub(item.lastPersistAt) >= 5*time.Second {
			item.lastPersistAt = now
			record := item.downloadFallbackEventRecord
			persist = &record
		}
	}
	registry.mu.Unlock()
	if persist != nil {
		go persistDownloadFallbackEventBestEffort(*persist)
	}
}

func (registry *downloadFallbackActiveRegistry) finish(record downloadFallbackEventRecord) {
	if registry == nil || record.RequestID == "" {
		return
	}
	registry.mu.Lock()
	delete(registry.items, record.RequestID)
	registry.mu.Unlock()
}

func (registry *downloadFallbackActiveRegistry) list() []downloadFallbackActiveTransfer {
	if registry == nil {
		return []downloadFallbackActiveTransfer{}
	}
	registry.mu.RLock()
	items := make([]downloadFallbackActiveTransfer, 0, len(registry.items))
	for _, item := range registry.items {
		if item != nil {
			items = append(items, *item)
		}
	}
	registry.mu.RUnlock()
	sortDownloadFallbackActive(items)
	return items
}

func sortDownloadFallbackActive(items []downloadFallbackActiveTransfer) {
	for i := 1; i < len(items); i++ {
		current := items[i]
		j := i - 1
		for j >= 0 && items[j].StartedAt.Before(current.StartedAt) {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = current
	}
}

func newTransferCountingReader(reader io.Reader, transferID string) *transferCountingReader {
	return &transferCountingReader{reader: reader, transferID: transferID}
}

func (reader *transferCountingReader) Read(p []byte) (int, error) {
	n, err := reader.reader.Read(p)
	if n > 0 {
		reader.written += int64(n)
		reader.maybeUpdate()
	}
	return n, err
}

func (reader *transferCountingReader) maybeUpdate() {
	now := time.Now()
	if now.Sub(reader.lastUpdate) < 500*time.Millisecond {
		return
	}
	reader.lastUpdate = now
	globalDownloadFallbackTransfers.updateBytes(reader.transferID, reader.written)
}

func newTransferCountingReadSeeker(reader io.ReadSeeker, transferID string) *transferCountingReadSeeker {
	return &transferCountingReadSeeker{reader: reader, transferID: transferID}
}

func (reader *transferCountingReadSeeker) Read(p []byte) (int, error) {
	n, err := reader.reader.Read(p)
	if n > 0 {
		reader.written += int64(n)
		reader.maybeUpdate()
	}
	return n, err
}

func (reader *transferCountingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return reader.reader.Seek(offset, whence)
}

func (reader *transferCountingReadSeeker) maybeUpdate() {
	now := time.Now()
	if now.Sub(reader.lastUpdate) < 500*time.Millisecond {
		return
	}
	reader.lastUpdate = now
	globalDownloadFallbackTransfers.updateBytes(reader.transferID, reader.written)
}

func downloadFallbackClientMetaFromContext(c *gin.Context) downloadFallbackClientMeta {
	if c == nil {
		return downloadFallbackClientMeta{}
	}
	meta := downloadFallbackClientMeta{
		ClientIP:  c.ClientIP(),
		UserAgent: "",
	}
	if c.Request != nil {
		meta.UserAgent = c.Request.UserAgent()
	}
	if value, ok := c.Get(clientUserIDContextKey); ok {
		switch typed := value.(type) {
		case int64:
			meta.UserID = typed
		case int:
			meta.UserID = int64(typed)
		}
	}
	if value, ok := c.Get(clientPublicIDContextKey); ok {
		if typed, ok := value.(string); ok {
			meta.PublicID = strings.TrimSpace(typed)
		}
	}
	meta.UID = downloadFallbackUID(meta.UserID, meta.PublicID)
	return meta
}

func downloadFallbackUID(userID int64, publicID string) string {
	return clientVisibleUIDOrPublicID(userID, publicID)
}

func normalizeDownloadFallbackMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case runtimecfg.DownloadFallbackModeProxy:
		return runtimecfg.DownloadFallbackModeProxy
	case runtimecfg.DownloadFallbackModeCDN:
		return runtimecfg.DownloadFallbackModeCDN
	default:
		return runtimecfg.DownloadFallbackModeCache
	}
}

func downloadFallbackMediaTypeFromKey(key string) string {
	ext := strings.ToLower(strings.TrimSpace(pathExt(key)))
	switch ext {
	case ".mp3", ".m4a", ".aac", ".wav", ".flac", ".ogg":
		return "audio"
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return "image"
	default:
		return "video"
	}
}

func pathExt(value string) string {
	dot := strings.LastIndex(value, ".")
	if dot < 0 {
		return ""
	}
	return value[dot:]
}

func nullIfEmpty(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func uint64FromInt64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func ipStringFromDB(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	ip := net.IP(value)
	return ip.String()
}
