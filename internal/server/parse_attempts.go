package server

import (
	"context"
	"database/sql"
	"errors"
	"net"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/parsers/native"
)

const (
	parseLinkClassKnown    = "known"
	parseLinkClassExternal = "external"
	parseLinkClassM3U8     = "m3u8"
	parseLinkClassUnknown  = "unknown"
	parseLinkClassInvalid  = "invalid"
)

type parseLinkClassification struct {
	RawInput       string
	SourceURL      string
	NormalizedURL  string
	Host           string
	Platform       string
	Classification string
}

type parseAttemptStatsPayload struct {
	Store         string                   `json:"store"`
	Days          int                      `json:"days"`
	Total         int64                    `json:"total"`
	Success       int64                    `json:"success"`
	Failed        int64                    `json:"failed"`
	Known         int64                    `json:"known"`
	External      int64                    `json:"external"`
	M3U8          int64                    `json:"m3u8"`
	Unknown       int64                    `json:"unknown"`
	Invalid       int64                    `json:"invalid"`
	Items         []parseAttemptStatItem   `json:"items"`
	RecentUnknown []parseAttemptRecentItem `json:"recentUnknown"`
}

type parseAttemptStatItem struct {
	Host           string    `json:"host"`
	Platform       string    `json:"platform"`
	Classification string    `json:"classification"`
	Total          int64     `json:"total"`
	Success        int64     `json:"success"`
	Failed         int64     `json:"failed"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
	SampleURL      string    `json:"sampleUrl,omitempty"`
	LastError      string    `json:"lastError,omitempty"`
}

type parseAttemptRecentItem struct {
	ID             int64     `json:"id"`
	RawInput       string    `json:"rawInput,omitempty"`
	SourceURL      string    `json:"sourceUrl,omitempty"`
	Host           string    `json:"host,omitempty"`
	Platform       string    `json:"platform,omitempty"`
	Classification string    `json:"classification"`
	Success        bool      `json:"success"`
	ErrorCode      string    `json:"errorCode,omitempty"`
	ErrorMessage   string    `json:"errorMessage,omitempty"`
	EntryPoint     string    `json:"entrypoint,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

func parseShareRequestTracked(c *gin.Context, input string, entrypoint string, options parseRequestOptions) (*parseResult, int64, error) {
	started := time.Now()
	result, err := parseShareRequestWithOptions(input, options)
	duration := time.Since(started)
	recordParseAttempt(c, input, entrypoint, duration, result, err)
	return result, duration.Milliseconds(), err
}

func classifyParseInput(rawInput string) parseLinkClassification {
	rawInput = strings.TrimSpace(rawInput)
	classified := parseLinkClassification{
		RawInput:       rawInput,
		Classification: parseLinkClassInvalid,
	}
	if rawInput == "" {
		return classified
	}

	sourceURL := strings.TrimSpace(extractURL(rawInput))
	classified.SourceURL = sourceURL
	parsed, err := neturl.Parse(sourceURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return classified
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	normalizedURL := normalizeURLForHash(sourceURL)
	platform := detectSource(normalizedURL)

	classified.Host = host
	classified.NormalizedURL = normalizedURL
	classified.Platform = platform
	classified.Classification = parseLinkClassUnknown
	switch {
	case isDirectM3U8URL(normalizedURL) || platform == "m3u8":
		classified.Classification = parseLinkClassM3U8
		classified.Platform = "m3u8"
	case isNativeParserSource(platform):
		classified.Classification = parseLinkClassKnown
	case platform != "":
		classified.Classification = parseLinkClassExternal
	}
	return classified
}

func isNativeParserSource(source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	_, ok := parser.VideoSourceInfoMapping[source]
	return ok
}

func recordParseAttempt(c *gin.Context, rawInput string, entrypoint string, duration time.Duration, result *parseResult, parseErr error) {
	db := appInfra.mysql
	if db == nil {
		return
	}

	classified := classifyParseInput(rawInput)
	sourceURL := firstNonEmpty(classified.SourceURL)
	normalizedURL := classified.NormalizedURL
	platform := classified.Platform
	if result != nil {
		sourceURL = firstNonEmpty(result.sourceURL, result.data.SourceURL, sourceURL)
		normalizedURL = firstNonEmpty(normalizeURLForHash(sourceURL), normalizedURL)
		platform = firstNonEmpty(result.source, result.data.Platform, platform)
		if classified.Classification == parseLinkClassUnknown && strings.TrimSpace(result.source) != "" {
			if result.source == "m3u8" {
				classified.Classification = parseLinkClassM3U8
			} else if isNativeParserSource(result.source) {
				classified.Classification = parseLinkClassKnown
			}
		}
	}

	if sourceURL != "" {
		if parsed, err := neturl.Parse(sourceURL); err == nil && strings.TrimSpace(parsed.Hostname()) != "" {
			classified.Host = strings.ToLower(strings.TrimSpace(parsed.Hostname()))
		}
	}

	success := parseErr == nil
	errorCode, errorMessage := parseAttemptError(parseErr)
	urlHash := parseURLHash(firstNonEmpty(normalizedURL, sourceURL, rawInput))
	parserName := parseAttemptParserName(classified.Classification, result)

	clientIP := ""
	userAgent := ""
	var userID interface{}
	if c != nil {
		clientIP = c.ClientIP()
		if c.Request != nil {
			userAgent = c.Request.UserAgent()
		}
		if value, ok := c.Get(clientUserIDContextKey); ok {
			switch typed := value.(type) {
			case int64:
				if typed > 0 {
					userID = typed
				}
			case int:
				if typed > 0 {
					userID = int64(typed)
				}
			}
		}
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := db.ExecContext(ctx, `
INSERT INTO parse_attempts (
  raw_input, source_url, normalized_url, host, classification, url_hash, platform,
  user_id, client_ip, user_agent, parser, entrypoint, success, error_code, error_message, duration_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			limitRunes(classified.RawInput, 4000),
			limitRunes(sourceURL, 4000),
			limitRunes(normalizedURL, 1024),
			limitRunes(classified.Host, 255),
			limitRunes(classified.Classification, 32),
			limitRunes(urlHash, 64),
			limitRunes(platform, 64),
			userID,
			clientIPBytes(clientIP),
			limitRunes(userAgent, 512),
			limitRunes(parserName, 64),
			limitRunes(entrypoint, 64),
			boolInt(success),
			limitRunes(errorCode, 64),
			limitRunes(errorMessage, 1024),
			int(duration.Milliseconds()),
		)
		if err != nil {
			logWarnf("record parse attempt failed entrypoint=%s target=%s error=%v", entrypoint, targetForLog(sourceURL), err)
		}
	}()
}

func parseAttemptError(parseErr error) (string, string) {
	if parseErr == nil {
		return "", ""
	}
	response := compatErrorResponse(parseErr)
	return strconv.Itoa(response.Code), compactTextForColumn(parseErr.Error(), 1024)
}

func parseAttemptParserName(classification string, result *parseResult) string {
	switch classification {
	case parseLinkClassM3U8:
		return "m3u8"
	case parseLinkClassKnown:
		return "native"
	case parseLinkClassExternal:
		return "yt-dlp"
	}
	if result != nil {
		return firstNonEmpty(result.source)
	}
	return ""
}

func handleAdminParseAttemptStats(c *gin.Context) {
	if appInfra.mysql == nil {
		c.JSON(200, httpResponse{
			Code: 0,
			Msg:  "ok",
			Data: parseAttemptStatsPayload{
				Store:         "disabled",
				Days:          intFromQuery(c, "days", 7),
				Items:         []parseAttemptStatItem{},
				RecentUnknown: []parseAttemptRecentItem{},
			},
		})
		return
	}

	days := intFromQuery(c, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}
	limit := intFromQuery(c, "limit", 100)
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	payload, err := loadParseAttemptStats(c.Request.Context(), days, limit)
	if err != nil {
		c.JSON(200, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	c.JSON(200, httpResponse{Code: 0, Msg: "ok", Data: payload})
}

func loadParseAttemptStats(ctx context.Context, days int, limit int) (parseAttemptStatsPayload, error) {
	if appInfra.mysql == nil {
		return parseAttemptStatsPayload{}, errors.New("mysql disabled")
	}
	since := time.Now().AddDate(0, 0, -days)
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	payload := parseAttemptStatsPayload{
		Store:         "mysql",
		Days:          days,
		Items:         []parseAttemptStatItem{},
		RecentUnknown: []parseAttemptRecentItem{},
	}

	err := appInfra.mysql.QueryRowContext(queryCtx, `
SELECT
  COUNT(*),
  COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN classification = 'known' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN classification = 'external' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN classification = 'm3u8' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN classification = 'unknown' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN classification = 'invalid' THEN 1 ELSE 0 END), 0)
FROM parse_attempts
WHERE created_at >= ?`, since).Scan(
		&payload.Total,
		&payload.Success,
		&payload.Failed,
		&payload.Known,
		&payload.External,
		&payload.M3U8,
		&payload.Unknown,
		&payload.Invalid,
	)
	if err != nil {
		return payload, err
	}

	rows, err := appInfra.mysql.QueryContext(queryCtx, `
SELECT
  host,
  platform,
  classification,
  COUNT(*) AS total_count,
  COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0) AS success_count,
  COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failed_count,
  MAX(created_at) AS last_seen_at,
  SUBSTRING_INDEX(GROUP_CONCAT(source_url ORDER BY created_at DESC SEPARATOR '\n'), '\n', 1) AS sample_url,
  SUBSTRING_INDEX(GROUP_CONCAT(error_message ORDER BY created_at DESC SEPARATOR '\n'), '\n', 1) AS last_error
FROM parse_attempts
WHERE created_at >= ?
GROUP BY host, platform, classification
ORDER BY total_count DESC, last_seen_at DESC
LIMIT ?`, since, limit)
	if err != nil {
		return payload, err
	}
	defer rows.Close()

	for rows.Next() {
		var item parseAttemptStatItem
		var sampleURL sql.NullString
		var lastError sql.NullString
		if err := rows.Scan(
			&item.Host,
			&item.Platform,
			&item.Classification,
			&item.Total,
			&item.Success,
			&item.Failed,
			&item.LastSeenAt,
			&sampleURL,
			&lastError,
		); err != nil {
			return payload, err
		}
		item.SampleURL = sampleURL.String
		item.LastError = lastError.String
		payload.Items = append(payload.Items, item)
	}
	if err := rows.Err(); err != nil {
		return payload, err
	}

	recentRows, err := appInfra.mysql.QueryContext(queryCtx, `
SELECT id, raw_input, source_url, host, platform, classification, success, error_code, error_message, entrypoint, created_at
FROM parse_attempts
WHERE created_at >= ? AND classification IN ('unknown', 'invalid')
ORDER BY created_at DESC
LIMIT 50`, since)
	if err != nil {
		return payload, err
	}
	defer recentRows.Close()

	for recentRows.Next() {
		var item parseAttemptRecentItem
		var rawInput sql.NullString
		var sourceURL sql.NullString
		var success int
		if err := recentRows.Scan(
			&item.ID,
			&rawInput,
			&sourceURL,
			&item.Host,
			&item.Platform,
			&item.Classification,
			&success,
			&item.ErrorCode,
			&item.ErrorMessage,
			&item.EntryPoint,
			&item.CreatedAt,
		); err != nil {
			return payload, err
		}
		item.RawInput = rawInput.String
		item.SourceURL = sourceURL.String
		item.Success = success == 1
		payload.RecentUnknown = append(payload.RecentUnknown, item)
	}
	if err := recentRows.Err(); err != nil {
		return payload, err
	}

	return payload, nil
}

func clientIPBytes(value string) []byte {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip.To16()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func compactTextForColumn(value string, limit int) string {
	return limitRunes(strings.Join(strings.Fields(strings.TrimSpace(value)), " "), limit)
}

func limitRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
