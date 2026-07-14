package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
)

type adminRecentRequestItem struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"userId,omitempty"`
	UID          string    `json:"uid,omitempty"`
	PublicID     string    `json:"publicId,omitempty"`
	ClientIP     string    `json:"clientIp,omitempty"`
	RawInput     string    `json:"rawInput,omitempty"`
	SourceURL    string    `json:"sourceUrl,omitempty"`
	Host         string    `json:"host,omitempty"`
	Platform     string    `json:"platform,omitempty"`
	Parser       string    `json:"parser,omitempty"`
	EntryPoint   string    `json:"entrypoint,omitempty"`
	Success      bool      `json:"success"`
	ErrorCode    string    `json:"errorCode,omitempty"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	DurationMS   int64     `json:"durationMs"`
	CreatedAt    time.Time `json:"createdAt"`
}

func adminRequestUID(userID int64, publicID string) string {
	return clientVisibleUIDOrPublicID(userID, publicID)
}

type adminRecentRequestStats struct {
	WindowHours   int   `json:"windowHours"`
	Total         int64 `json:"total"`
	Success       int64 `json:"success"`
	Failed        int64 `json:"failed"`
	AvgDurationMS int64 `json:"avgDurationMs"`
	MaxDurationMS int64 `json:"maxDurationMs"`
}

type adminRecentRequestsPayload struct {
	Store string                   `json:"store"`
	Cache string                   `json:"cache,omitempty"`
	Stats adminRecentRequestStats  `json:"stats"`
	Items []adminRecentRequestItem `json:"items"`
}

func handleAdminRecentRequests(c *gin.Context) {
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
	payload, err := loadAdminRecentRequestsCached(c.Request.Context(), hours, limit)
	if err != nil {
		c.JSON(200, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	c.JSON(200, httpResponse{Code: 0, Msg: "ok", Data: payload})
}

func loadAdminRecentRequestsCached(ctx context.Context, hours int, limit int) (adminRecentRequestsPayload, error) {
	cacheKey := fmt.Sprintf("recent_requests:%d:%d", hours, limit)
	var cached adminRecentRequestsPayload
	if adminRedisCacheGet(cacheKey, &cached) {
		cached.Cache = "redis"
		return cached, nil
	}
	loaded, err := loadAdminRecentRequests(ctx, hours, limit)
	if err == nil && loaded.Store == "mysql" {
		adminRedisCacheSet(cacheKey, loaded, 3*time.Second)
	}
	return loaded, err
}

func loadAdminRecentRequests(ctx context.Context, hours int, limit int) (adminRecentRequestsPayload, error) {
	payload := adminRecentRequestsPayload{
		Store: "disabled",
		Stats: adminRecentRequestStats{
			WindowHours: hours,
		},
		Items: []adminRecentRequestItem{},
	}
	if appInfra.mysql == nil {
		return payload, nil
	}
	payload.Store = "mysql"
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	queryCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	var avg sql.NullFloat64
	if err := appInfra.mysql.QueryRowContext(queryCtx, `
SELECT
  COUNT(*),
  COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0),
  AVG(duration_ms),
  COALESCE(MAX(duration_ms), 0)
FROM parse_attempts
WHERE created_at >= ?`, since).Scan(
		&payload.Stats.Total,
		&payload.Stats.Success,
		&payload.Stats.Failed,
		&avg,
		&payload.Stats.MaxDurationMS,
	); err != nil {
		return payload, err
	}
	if avg.Valid {
		payload.Stats.AvgDurationMS = int64(avg.Float64)
	}

	rows, err := appInfra.mysql.QueryContext(queryCtx, `
SELECT
  p.id,
  COALESCE(p.user_id, 0),
  COALESCE(u.public_id, ''),
  INET6_NTOA(p.client_ip),
  p.raw_input,
  p.source_url,
  p.host,
  p.platform,
  p.parser,
  p.entrypoint,
  p.success,
  p.error_code,
  p.error_message,
  p.duration_ms,
  p.created_at
FROM parse_attempts p
LEFT JOIN app_users u ON u.id = p.user_id
WHERE p.created_at >= ?
ORDER BY p.created_at DESC
LIMIT ?`, since, limit)
	if err != nil {
		return payload, err
	}
	defer rows.Close()

	for rows.Next() {
		var item adminRecentRequestItem
		var publicID sql.NullString
		var clientIP sql.NullString
		var rawInput sql.NullString
		var sourceURL sql.NullString
		var success int
		if err := rows.Scan(
			&item.ID,
			&item.UserID,
			&publicID,
			&clientIP,
			&rawInput,
			&sourceURL,
			&item.Host,
			&item.Platform,
			&item.Parser,
			&item.EntryPoint,
			&success,
			&item.ErrorCode,
			&item.ErrorMessage,
			&item.DurationMS,
			&item.CreatedAt,
		); err != nil {
			return payload, err
		}
		item.PublicID = publicID.String
		item.UID = adminRequestUID(item.UserID, item.PublicID)
		item.ClientIP = clientIP.String
		item.RawInput = rawInput.String
		item.SourceURL = sourceURL.String
		item.Success = success == 1
		payload.Items = append(payload.Items, item)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return payload, err
	}
	return payload, nil
}
