package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"watermark-backend/internal/runtimecfg"
)

const runtimeSettingsCurrentKey = "current"

var sharedRuntimeSettingsState struct {
	sync.Mutex
	lastCheck   time.Time
	lastApplied time.Time
}

func loadSharedRuntimeSettings(force bool) bool {
	if appInfra.mysql == nil {
		return false
	}

	sharedRuntimeSettingsState.Lock()
	defer sharedRuntimeSettingsState.Unlock()
	if !force && time.Since(sharedRuntimeSettingsState.lastCheck) < 5*time.Second {
		return false
	}
	sharedRuntimeSettingsState.lastCheck = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var payload string
	var updatedAt time.Time
	err := appInfra.mysql.QueryRowContext(ctx, `
SELECT setting_json, updated_at
FROM runtime_settings
WHERE setting_key = ?
LIMIT 1
`, runtimeSettingsCurrentKey).Scan(&payload, &updatedAt)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && !isNoRows(err) {
			logWarnf("load shared runtime settings failed: %v", err)
		}
		return false
	}
	if !force && !updatedAt.After(sharedRuntimeSettingsState.lastApplied) {
		return false
	}

	var settings runtimecfg.Settings
	if err := json.Unmarshal([]byte(payload), &settings); err != nil {
		logWarnf("decode shared runtime settings failed: %v", err)
		return false
	}
	if _, err := runtimecfg.Update(settings); err != nil {
		logWarnf("apply shared runtime settings failed: %v", err)
		return false
	}
	sharedRuntimeSettingsState.lastApplied = updatedAt
	logInfof("shared runtime settings applied download_fallback_mode=%s updated_at=%s", runtimecfg.DownloadFallbackMode(), updatedAt.Format(time.RFC3339))
	return true
}

func seedSharedRuntimeSettingsIfMissing() {
	if appInfra.mysql == nil {
		return
	}
	role := strings.ToLower(strings.TrimSpace(os.Getenv("CLUSTER_NODE_ROLE")))
	if role == "worker" {
		return
	}
	settings := runtimecfg.Current()
	payload, err := json.Marshal(settings)
	if err != nil {
		logWarnf("encode initial shared runtime settings failed: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := appInfra.mysql.ExecContext(ctx, `
INSERT IGNORE INTO runtime_settings (setting_key, setting_json)
VALUES (?, CAST(? AS JSON))
`, runtimeSettingsCurrentKey, string(payload))
	if err != nil {
		logWarnf("seed shared runtime settings failed: %v", err)
		return
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		logInfof("shared runtime settings initialized download_fallback_mode=%s", runtimecfg.DownloadFallbackMode())
	}
}

func persistSharedRuntimeSettings(settings runtimecfg.Settings) error {
	if appInfra.mysql == nil {
		return nil
	}
	payload, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = appInfra.mysql.ExecContext(ctx, `
INSERT INTO runtime_settings (setting_key, setting_json)
VALUES (?, CAST(? AS JSON))
ON DUPLICATE KEY UPDATE setting_json = VALUES(setting_json), updated_at = CURRENT_TIMESTAMP
`, runtimeSettingsCurrentKey, string(payload))
	if err != nil {
		return err
	}

	sharedRuntimeSettingsState.Lock()
	sharedRuntimeSettingsState.lastCheck = time.Time{}
	sharedRuntimeSettingsState.lastApplied = time.Time{}
	sharedRuntimeSettingsState.Unlock()
	return nil
}

func refreshSharedRuntimeSettings() {
	if loadSharedRuntimeSettings(false) {
		applyRuntimeSettings()
	}
}
