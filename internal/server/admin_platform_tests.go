package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
)

var errNoPlatformTestLinks = errors.New("no platform test links")

type adminPlatformTestRunSnapshot struct {
	RunID       string                    `json:"runId"`
	Status      string                    `json:"status"`
	Message     string                    `json:"message,omitempty"`
	Total       int                       `json:"total"`
	Completed   int                       `json:"completed"`
	Success     int                       `json:"success"`
	Failed      int                       `json:"failed"`
	DurationMS  int64                     `json:"durationMs"`
	StartedAt   time.Time                 `json:"startedAt,omitempty"`
	CompletedAt *time.Time                `json:"completedAt,omitempty"`
	UpdatedAt   time.Time                 `json:"updatedAt,omitempty"`
	Items       []adminPlatformTestResult `json:"items"`
	Store       string                    `json:"store"`
}

type adminPlatformTestRunManager struct {
	mu     sync.Mutex
	latest *adminPlatformTestRunSnapshot
}

var adminPlatformTestRuns = &adminPlatformTestRunManager{}

func (m *adminPlatformTestRunManager) start(ctx context.Context, links []adminTestLink, username string) (adminPlatformTestRunSnapshot, bool, error) {
	links = sanitizeAdminTestLinks(links)
	if len(links) == 0 {
		return adminPlatformTestRunSnapshot{}, false, errNoPlatformTestLinks
	}

	m.mu.Lock()
	if m.latest != nil && m.latest.Status == "running" {
		snapshot := cloneAdminPlatformTestRunSnapshot(*m.latest)
		m.mu.Unlock()
		return snapshot, true, nil
	}

	runID, err := newAdminPlatformTestRunID()
	if err != nil {
		m.mu.Unlock()
		return adminPlatformTestRunSnapshot{}, false, err
	}
	now := time.Now()
	snapshot := adminPlatformTestRunSnapshot{
		RunID:     runID,
		Status:    "running",
		Total:     len(links),
		StartedAt: now,
		UpdatedAt: now,
		Items:     make([]adminPlatformTestResult, 0, len(links)),
		Store:     "memory",
	}
	for index, link := range links {
		snapshot.Items = append(snapshot.Items, adminPlatformTestResult{
			Name:     firstNonEmpty(link.Name, sampleDisplayName(link.Platform), "样本"),
			URL:      link.URL,
			Platform: link.Platform,
			Status:   "pending",
		})
		snapshot.Items[index].DurationMS = 0
	}
	m.latest = &snapshot
	m.mu.Unlock()

	if appInfra.mysql != nil {
		if err := insertAdminPlatformTestRun(ctx, snapshot, username); err != nil {
			logErrorf("create platform test run failed run_id=%s error=%v", runID, err)
		} else {
			m.setStore(runID, "mysql")
		}
	}

	go m.execute(runID, links)
	return m.snapshotByID(runID), false, nil
}

func (m *adminPlatformTestRunManager) execute(runID string, links []adminTestLink) {
	runAdminPlatformTestsWithScheduler(context.Background(), links, adminPlatformTestSchedulerHooks{
		OnRunning: func(index int, target adminPlatformTestTarget) {
			m.markItemRunning(runID, index, target.Node)
		},
		OnComplete: func(index int, result adminPlatformTestResult) {
			m.updateItem(runID, index, result)
		},
	})
	m.finish(runID)
}

func (m *adminPlatformTestRunManager) snapshot() (adminPlatformTestRunSnapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latest == nil {
		return adminPlatformTestRunSnapshot{}, false
	}
	return cloneAdminPlatformTestRunSnapshot(*m.latest), true
}

func (m *adminPlatformTestRunManager) snapshotByID(runID string) adminPlatformTestRunSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latest == nil || m.latest.RunID != runID {
		return adminPlatformTestRunSnapshot{}
	}
	return cloneAdminPlatformTestRunSnapshot(*m.latest)
}

func (m *adminPlatformTestRunManager) setStore(runID, store string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latest == nil || m.latest.RunID != runID {
		return
	}
	m.latest.Store = store
}

func (m *adminPlatformTestRunManager) markItemRunning(runID string, index int, node clusterNodeInfo) {
	m.mu.Lock()
	if m.latest == nil || m.latest.RunID != runID || index < 0 || index >= len(m.latest.Items) {
		m.mu.Unlock()
		return
	}
	m.latest.Items[index].Status = "running"
	now := time.Now()
	m.latest.Items[index].StartedAt = &now
	m.latest.Items[index].NodeID = node.ID
	m.latest.Items[index].NodeName = node.Name
	m.latest.Items[index].NodeRole = node.Role
	m.latest.UpdatedAt = now
	snapshot := cloneAdminPlatformTestRunSnapshot(*m.latest)
	m.mu.Unlock()
	storeAdminPlatformTestRunItem(context.Background(), snapshot, index)
}

func (m *adminPlatformTestRunManager) updateItem(runID string, index int, result adminPlatformTestResult) {
	m.mu.Lock()
	if m.latest == nil || m.latest.RunID != runID || index < 0 || index >= len(m.latest.Items) {
		m.mu.Unlock()
		return
	}
	if result.Name == "" {
		result.Name = m.latest.Items[index].Name
	}
	if result.URL == "" {
		result.URL = m.latest.Items[index].URL
	}
	if result.Platform == "" {
		result.Platform = m.latest.Items[index].Platform
	}
	if result.Status == "" {
		result.Status = "completed"
	}
	m.latest.Items[index] = result
	m.recalculateLocked()
	snapshot := cloneAdminPlatformTestRunSnapshot(*m.latest)
	m.mu.Unlock()
	storeAdminPlatformTestRunItem(context.Background(), snapshot, index)
	storeAdminPlatformTestRunProgress(context.Background(), snapshot)
}

func (m *adminPlatformTestRunManager) finish(runID string) {
	m.mu.Lock()
	if m.latest == nil || m.latest.RunID != runID {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	m.latest.Status = "completed"
	m.latest.CompletedAt = &now
	m.latest.UpdatedAt = now
	m.recalculateLocked()
	snapshot := cloneAdminPlatformTestRunSnapshot(*m.latest)
	m.mu.Unlock()
	storeAdminPlatformTestRunProgress(context.Background(), snapshot)
}

func (m *adminPlatformTestRunManager) recalculateLocked() {
	if m.latest == nil {
		return
	}
	completed := 0
	success := 0
	failed := 0
	for _, item := range m.latest.Items {
		if item.Status == "completed" {
			completed++
			if item.OK {
				success++
			} else {
				failed++
			}
		}
	}
	m.latest.Completed = completed
	m.latest.Success = success
	m.latest.Failed = failed
	m.latest.DurationMS = time.Since(m.latest.StartedAt).Milliseconds()
}

func latestAdminPlatformTestRun(ctx context.Context) (adminPlatformTestRunSnapshot, bool) {
	mem, hasMem := adminPlatformTestRuns.snapshot()
	if hasMem && mem.Status == "running" {
		return mem, true
	}
	dbSnapshot, hasDB, err := loadLatestAdminPlatformTestRun(ctx)
	if err != nil {
		logErrorf("load latest platform test run failed: %v", err)
	}
	if hasDB {
		if hasMem && mem.UpdatedAt.After(dbSnapshot.UpdatedAt) {
			return mem, true
		}
		return dbSnapshot, true
	}
	if hasMem {
		return mem, true
	}
	return adminPlatformTestRunSnapshot{}, false
}

func adminPlatformTestRunByID(ctx context.Context, runID string) (adminPlatformTestRunSnapshot, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return adminPlatformTestRunSnapshot{}, false
	}
	mem, hasMem := adminPlatformTestRuns.snapshot()
	if hasMem && mem.RunID == runID && mem.Status == "running" {
		return mem, true
	}
	dbSnapshot, hasDB, err := loadAdminPlatformTestRunByID(ctx, runID)
	if err != nil {
		logErrorf("load platform test run failed run_id=%s error=%v", runID, err)
	}
	if hasDB {
		if hasMem && mem.RunID == runID && mem.UpdatedAt.After(dbSnapshot.UpdatedAt) {
			return mem, true
		}
		return dbSnapshot, true
	}
	if hasMem && mem.RunID == runID {
		return mem, true
	}
	return adminPlatformTestRunSnapshot{}, false
}

func cloneAdminPlatformTestRunSnapshot(input adminPlatformTestRunSnapshot) adminPlatformTestRunSnapshot {
	output := input
	if output.Status == "running" && !output.StartedAt.IsZero() {
		now := time.Now()
		output.DurationMS = now.Sub(output.StartedAt).Milliseconds()
		output.UpdatedAt = now
	}
	if input.CompletedAt != nil {
		completedAt := *input.CompletedAt
		output.CompletedAt = &completedAt
	}
	output.Items = append([]adminPlatformTestResult(nil), input.Items...)
	for index := range output.Items {
		if output.Items[index].StartedAt != nil {
			startedAt := *output.Items[index].StartedAt
			output.Items[index].StartedAt = &startedAt
		}
		if output.Items[index].RespondedAt != nil {
			respondedAt := *output.Items[index].RespondedAt
			output.Items[index].RespondedAt = &respondedAt
		}
	}
	return output
}

func newAdminPlatformTestRunID() (string, error) {
	return secureRandomHex(16)
}

func insertAdminPlatformTestRun(ctx context.Context, snapshot adminPlatformTestRunSnapshot, username string) error {
	if appInfra.mysql == nil {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	adminID := adminIDByUsername(username)
	var adminIDValue interface{}
	if adminID.Valid {
		adminIDValue = adminID.Int64
	}

	tx, err := appInfra.mysql.BeginTx(queryCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO platform_test_runs
  (run_id, status, message, admin_id, total_count, success_count, failed_count, duration_ms, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, snapshot.RunID, snapshot.Status, snapshot.Message, adminIDValue, snapshot.Total, snapshot.Success, snapshot.Failed, snapshot.DurationMS, snapshot.StartedAt); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(queryCtx, `
INSERT INTO platform_test_items
  (run_id, platform_key, display_name, sample_url, status, success, error_message, duration_ms, sort_order, started_at, responded_at, node_id, node_name, node_role)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for index, item := range snapshot.Items {
		if _, err := stmt.ExecContext(queryCtx, snapshot.RunID, item.Platform, item.Name, item.URL, firstNonEmpty(item.Status, "pending"), boolToTinyInt(item.OK), item.Error, item.DurationMS, index, timePtrValue(item.StartedAt), timePtrValue(item.RespondedAt), item.NodeID, item.NodeName, item.NodeRole); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func storeAdminPlatformTestRunItem(ctx context.Context, snapshot adminPlatformTestRunSnapshot, index int) {
	if appInfra.mysql == nil || index < 0 || index >= len(snapshot.Items) {
		return
	}
	item := snapshot.Items[index]
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := appInfra.mysql.ExecContext(queryCtx, `
UPDATE platform_test_items
SET display_name = ?,
    status = ?,
    success = ?,
    result_platform = ?,
    result_type = ?,
    parser_engine = ?,
    result_title = ?,
    share_id = ?,
    error_message = ?,
    duration_ms = ?,
    started_at = ?,
    responded_at = ?,
    node_id = ?,
    node_name = ?,
    node_role = ?,
    updated_at = NOW()
WHERE run_id = ? AND sort_order = ?
`, item.Name, firstNonEmpty(item.Status, "completed"), boolToTinyInt(item.OK), item.Platform, item.Type, item.ParserEngine, item.Title, item.ShareID, item.Error, item.DurationMS, timePtrValue(item.StartedAt), timePtrValue(item.RespondedAt), item.NodeID, item.NodeName, item.NodeRole, snapshot.RunID, index); err != nil {
		logErrorf("update platform test item failed run_id=%s index=%d error=%v", snapshot.RunID, index, err)
	}
}

func storeAdminPlatformTestRunProgress(ctx context.Context, snapshot adminPlatformTestRunSnapshot) {
	if appInfra.mysql == nil || snapshot.RunID == "" {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var completedAt interface{}
	if snapshot.CompletedAt != nil {
		completedAt = *snapshot.CompletedAt
	}
	if _, err := appInfra.mysql.ExecContext(queryCtx, `
UPDATE platform_test_runs
SET status = ?,
    message = ?,
    total_count = ?,
    success_count = ?,
    failed_count = ?,
    duration_ms = ?,
    completed_at = ?,
    updated_at = NOW()
WHERE run_id = ?
`, snapshot.Status, snapshot.Message, snapshot.Total, snapshot.Success, snapshot.Failed, snapshot.DurationMS, completedAt, snapshot.RunID); err != nil {
		logErrorf("update platform test run failed run_id=%s error=%v", snapshot.RunID, err)
	}
}

func loadLatestAdminPlatformTestRun(ctx context.Context) (adminPlatformTestRunSnapshot, bool, error) {
	return loadAdminPlatformTestRun(ctx, "")
}

func loadAdminPlatformTestRunByID(ctx context.Context, runID string) (adminPlatformTestRunSnapshot, bool, error) {
	return loadAdminPlatformTestRun(ctx, strings.TrimSpace(runID))
}

func loadAdminPlatformTestRun(ctx context.Context, runID string) (adminPlatformTestRunSnapshot, bool, error) {
	if appInfra.mysql == nil {
		return adminPlatformTestRunSnapshot{}, false, nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var snapshot adminPlatformTestRunSnapshot
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	var updatedAt sql.NullTime
	query := `
SELECT run_id, status, message, total_count, success_count, failed_count, duration_ms, started_at, completed_at, updated_at
FROM platform_test_runs
`
	var row *sql.Row
	if runID != "" {
		query += `WHERE run_id = ?`
		row = appInfra.mysql.QueryRowContext(queryCtx, query, runID)
	} else {
		query += `
ORDER BY created_at DESC, id DESC
LIMIT 1
`
		row = appInfra.mysql.QueryRowContext(queryCtx, query)
	}
	err := row.Scan(
		&snapshot.RunID,
		&snapshot.Status,
		&snapshot.Message,
		&snapshot.Total,
		&snapshot.Success,
		&snapshot.Failed,
		&snapshot.DurationMS,
		&startedAt,
		&completedAt,
		&updatedAt,
	)
	if err != nil {
		if isNoRows(err) {
			return adminPlatformTestRunSnapshot{}, false, nil
		}
		return adminPlatformTestRunSnapshot{}, false, err
	}
	now := time.Now()
	snapshot.StartedAt = now
	if startedAt.Valid {
		snapshot.StartedAt = startedAt.Time
	}
	snapshot.UpdatedAt = snapshot.StartedAt
	if updatedAt.Valid {
		snapshot.UpdatedAt = updatedAt.Time
	}
	if completedAt.Valid {
		snapshot.CompletedAt = &completedAt.Time
	}
	snapshot.Store = "mysql"

	rows, err := appInfra.mysql.QueryContext(queryCtx, `
SELECT platform_key, display_name, sample_url, status, success, result_platform, result_type, parser_engine, result_title, share_id, error_message, duration_ms, started_at, responded_at, node_id, node_name, node_role
FROM platform_test_items
WHERE run_id = ?
ORDER BY sort_order ASC, id ASC
`, snapshot.RunID)
	if err != nil {
		return adminPlatformTestRunSnapshot{}, false, err
	}
	defer rows.Close()

	for rows.Next() {
		var item adminPlatformTestResult
		var platformKey string
		var success int
		var startedAt sql.NullTime
		var respondedAt sql.NullTime
		if err := rows.Scan(
			&platformKey,
			&item.Name,
			&item.URL,
			&item.Status,
			&success,
			&item.Platform,
			&item.Type,
			&item.ParserEngine,
			&item.Title,
			&item.ShareID,
			&item.Error,
			&item.DurationMS,
			&startedAt,
			&respondedAt,
			&item.NodeID,
			&item.NodeName,
			&item.NodeRole,
		); err != nil {
			return adminPlatformTestRunSnapshot{}, false, err
		}
		item.OK = success == 1
		if startedAt.Valid {
			item.StartedAt = &startedAt.Time
		}
		if respondedAt.Valid {
			item.RespondedAt = &respondedAt.Time
		}
		item.Platform = firstNonEmpty(item.Platform, platformKey)
		item.Name = firstNonEmpty(item.Name, sampleDisplayName(item.Platform), item.Platform)
		snapshot.Items = append(snapshot.Items, item)
	}
	if err := rows.Err(); err != nil {
		return adminPlatformTestRunSnapshot{}, false, err
	}
	snapshot.Completed = 0
	for _, item := range snapshot.Items {
		if item.Status == "completed" {
			snapshot.Completed++
		}
	}
	if snapshot.Completed == 0 && (snapshot.Success > 0 || snapshot.Failed > 0) {
		snapshot.Completed = snapshot.Success + snapshot.Failed
	}
	return snapshot, true, nil
}

func boolToTinyInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timePtrValue(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return *value
}
