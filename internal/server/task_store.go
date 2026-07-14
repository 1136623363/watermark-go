package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type taskSummary struct {
	TaskID       string    `json:"id"`
	TaskType     string    `json:"type"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"message"`
	SourceURL    string    `json:"sourceUrl,omitempty"`
	RetryCount   int       `json:"retryCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type taskListPayload struct {
	Stats mergeTaskStats `json:"stats"`
	Items []taskSummary  `json:"items"`
}

func persistM3U8TaskQueued(task *mergeTask, rawURL string) {
	if appInfra.mysql == nil || task == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"url": rawURL})
	payloadJSON := string(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := appInfra.mysql.ExecContext(ctx, `
INSERT INTO parse_tasks (task_id, task_type, status, payload_json, available_at, created_at, updated_at)
VALUES (?, 'm3u8_merge', ?, ?, NOW(), ?, ?)
ON DUPLICATE KEY UPDATE
  status = VALUES(status),
  payload_json = VALUES(payload_json),
  updated_at = VALUES(updated_at)
`, task.ID, task.Status, payloadJSON, task.CreatedAt, task.UpdatedAt); err != nil {
		logErrorf("persist m3u8 task queued failed id=%s error=%v", task.ID, err)
	}
}

func persistM3U8TaskStatus(task *mergeTask) {
	if appInfra.mysql == nil || task == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	finishedExpr := "NULL"
	if task.Status == "done" || task.Status == "error" {
		finishedExpr = "NOW()"
	}
	query := `
UPDATE parse_tasks
SET status = ?, error_message = ?, started_at = COALESCE(started_at, ?), finished_at = ` + finishedExpr + `, updated_at = ?
WHERE task_id = ?
`
	if _, err := appInfra.mysql.ExecContext(ctx, query, task.Status, task.Message, task.CreatedAt, task.UpdatedAt, task.ID); err != nil {
		logErrorf("persist m3u8 task status failed id=%s error=%v", task.ID, err)
	}
}

func markInterruptedPersistentTasks() {
	if appInfra.mysql == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := appInfra.mysql.ExecContext(ctx, `
UPDATE parse_tasks
SET status = 'error',
    error_message = '任务因服务重启中断，请重新创建',
    finished_at = COALESCE(finished_at, NOW()),
    updated_at = NOW()
WHERE task_type = 'm3u8_merge'
  AND status IN ('pending', 'running')
`)
	if err != nil {
		logErrorf("mark interrupted m3u8 tasks failed: %v", err)
		return
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		logWarnf("marked interrupted m3u8 tasks count=%d", affected)
	}
}

func listPersistentTasks(limit int) (taskListPayload, bool, error) {
	if appInfra.mysql == nil {
		return taskListPayload{}, false, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := appInfra.mysql.QueryContext(ctx, `
SELECT task_id, task_type, status, payload_json, error_message, retry_count, created_at, updated_at
FROM parse_tasks
ORDER BY updated_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return taskListPayload{}, true, err
	}
	defer rows.Close()

	payload := taskListPayload{
		Items: make([]taskSummary, 0),
	}
	for rows.Next() {
		var item taskSummary
		var payloadJSON []byte
		var errorMessage sql.NullString
		if err := rows.Scan(
			&item.TaskID,
			&item.TaskType,
			&item.Status,
			&payloadJSON,
			&errorMessage,
			&item.RetryCount,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return taskListPayload{}, true, err
		}
		item.SourceURL = sourceURLFromTaskPayload(payloadJSON)
		if errorMessage.Valid {
			item.ErrorMessage = errorMessage.String
		}
		payload.Items = append(payload.Items, item)
	}
	if err := rows.Err(); err != nil {
		return taskListPayload{}, true, err
	}
	payload.Stats = statsFromTaskSummaries(payload.Items)
	return payload, true, nil
}

func sourceURLFromTaskPayload(payload []byte) string {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	return body.URL
}

func currentTaskStats() mergeTaskStats {
	if stats, ok, err := persistentTaskStats(); ok && err == nil {
		return stats
	}
	return globalMergeTaskStore.stats()
}

func persistentTaskStats() (mergeTaskStats, bool, error) {
	if appInfra.mysql == nil {
		return mergeTaskStats{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rows, err := appInfra.mysql.QueryContext(ctx, `
SELECT status, COUNT(*)
FROM parse_tasks
GROUP BY status
`)
	if err != nil {
		return mergeTaskStats{}, true, err
	}
	defer rows.Close()

	var stats mergeTaskStats
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return mergeTaskStats{}, true, err
		}
		stats.Total += count
		switch status {
		case "pending":
			stats.Pending += count
		case "running":
			stats.Running += count
		case "done":
			stats.Done += count
		case "error":
			stats.Error += count
		}
	}
	if err := rows.Err(); err != nil {
		return mergeTaskStats{}, true, err
	}
	return stats, true, nil
}

func statsFromTaskSummaries(items []taskSummary) mergeTaskStats {
	stats := mergeTaskStats{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case "pending":
			stats.Pending++
		case "running":
			stats.Running++
		case "done":
			stats.Done++
		case "error":
			stats.Error++
		}
	}
	return stats
}

func memoryTaskListPayload() taskListPayload {
	items := globalMergeTaskStore.list()
	summaries := make([]taskSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, taskSummary{
			TaskID:       item.ID,
			TaskType:     "m3u8_merge",
			Status:       item.Status,
			ErrorMessage: item.Message,
			SourceURL:    item.SourceURL,
			CreatedAt:    item.CreatedAt,
			UpdatedAt:    item.UpdatedAt,
		})
	}
	return taskListPayload{
		Stats: globalMergeTaskStore.stats(),
		Items: summaries,
	}
}
