package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

type mysqlParseResultStore struct {
	db *sql.DB
}

func (store *mysqlParseResultStore) get(shareID string) (parseData, bool, error) {
	record, ok, err := store.getRecord(shareID)
	if err != nil || !ok {
		return parseData{}, ok, err
	}
	data := record.Data
	data.ShareID = record.ID
	data.SourceURL = firstNonEmpty(data.SourceURL, record.SourceURL, record.NormalizedURL)
	data = normalizeParseDataMediaAliases(data)
	return data, true, nil
}

func (store *mysqlParseResultStore) getRecord(shareID string) (cachedParseResult, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var record cachedParseResult
	var body []byte
	err := store.db.QueryRowContext(ctx, `
SELECT share_id, source_url, normalized_url, result_json, created_at, updated_at
FROM parse_results
WHERE share_id = ? AND status = 1
LIMIT 1
`, shareID).Scan(&record.ID, &record.SourceURL, &record.NormalizedURL, &body, &record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return cachedParseResult{}, false, nil
		}
		return cachedParseResult{}, false, err
	}
	if err := json.Unmarshal(body, &record.Data); err != nil {
		return cachedParseResult{}, false, err
	}
	record.Data.ShareID = record.ID
	record.Data.SourceURL = firstNonEmpty(record.Data.SourceURL, record.SourceURL, record.NormalizedURL)
	record.Data = normalizeParseDataMediaAliases(record.Data)
	return record, true, nil
}

func (store *mysqlParseResultStore) put(sourceURL string, data parseData) (parseData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	requestURL := strings.TrimSpace(sourceURL)
	shareID := parseCacheID(requestURL)
	urlHash := parseURLHash(requestURL)
	sourceURL = safePersistentSourceURL(requestURL)
	normalized := sourceURL
	data.ShareID = shareID
	data.SourceURL = sourceURL
	data = normalizeParseDataMediaAliases(data)

	body, err := json.Marshal(data)
	if err != nil {
		return data, err
	}

	_, err = store.db.ExecContext(ctx, `
INSERT INTO parse_results (
  share_id, url_hash, source_url, normalized_url, platform, result_type,
  title, cover_url, author_name, result_json, status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON DUPLICATE KEY UPDATE
  share_id = VALUES(share_id),
  source_url = VALUES(source_url),
  normalized_url = VALUES(normalized_url),
  platform = VALUES(platform),
  result_type = VALUES(result_type),
  title = VALUES(title),
  cover_url = VALUES(cover_url),
  author_name = VALUES(author_name),
  result_json = VALUES(result_json),
  status = 1,
  updated_at = CURRENT_TIMESTAMP
`, shareID, urlHash, sourceURL, normalized, data.Platform, data.Type, data.Title, data.Cover, data.Author, body)
	if err != nil {
		return data, err
	}
	return data, nil
}

func (store *mysqlParseResultStore) list(limit int, query string) ([]parseCacheSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query = strings.TrimSpace(query)

	var rows *sql.Rows
	var err error
	if query == "" {
		rows, err = store.db.QueryContext(ctx, `
SELECT share_id, source_url, platform, result_type, title, cover_url, created_at, updated_at
FROM parse_results
WHERE status = 1
ORDER BY updated_at DESC
LIMIT ?
`, limit)
	} else {
		like := "%" + query + "%"
		rows, err = store.db.QueryContext(ctx, `
SELECT share_id, source_url, platform, result_type, title, cover_url, created_at, updated_at
FROM parse_results
WHERE status = 1
  AND (share_id LIKE ? OR source_url LIKE ? OR platform LIKE ? OR result_type LIKE ? OR title LIKE ?)
ORDER BY updated_at DESC
LIMIT ?
`, like, like, like, like, like, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]parseCacheSummary, 0)
	for rows.Next() {
		var item parseCacheSummary
		if err := rows.Scan(
			&item.ID,
			&item.SourceURL,
			&item.Platform,
			&item.Type,
			&item.Title,
			&item.Cover,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *mysqlParseResultStore) stats() (parseCacheStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stats parseCacheStats
	var latest sql.NullTime
	err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*), MAX(updated_at)
FROM parse_results
WHERE status = 1
`).Scan(&stats.Count, &latest)
	if err != nil {
		return parseCacheStats{}, err
	}
	if latest.Valid {
		stats.LatestTime = latest.Time
	}
	return stats, nil
}

func (store *mysqlParseResultStore) delete(shareID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := store.db.ExecContext(ctx, "DELETE FROM parse_results WHERE share_id = ?", shareID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (store *mysqlParseResultStore) clear() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := store.db.ExecContext(ctx, "DELETE FROM parse_results")
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
