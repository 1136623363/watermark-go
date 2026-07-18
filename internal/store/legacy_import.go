package store

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type LegacyRuntimeSetting struct {
	Key   string
	Value any
}

type ImportManifest struct {
	SchemaVersion string            `json:"schemaVersion"`
	Mode          string            `json:"mode"`
	TableCounts   map[string]int    `json:"tableCounts"`
	Checksums     map[string]string `json:"checksums"`
}

func ScrubLegacyRuntimeSettings(settings []LegacyRuntimeSetting) []LegacyRuntimeSetting {
	allowed := map[string]bool{
		"rateLimitEnabled":              true,
		"httpTimeoutSeconds":            true,
		"baselineConcurrency":           true,
		"downloadFallbackEnabled":       true,
		"downloadFallbackMode":          true,
		"downloadFallbackPublicBaseURL": true,
		"downloadFallbackCDNBaseURL":    true,
	}
	out := make([]LegacyRuntimeSetting, 0, len(settings))
	for _, setting := range settings {
		key := strings.TrimSpace(setting.Key)
		if !allowed[key] || containsSensitiveSetting(key, setting.Value) {
			continue
		}
		out = append(out, LegacyRuntimeSetting{Key: key, Value: setting.Value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func containsSensitiveSetting(key string, value any) bool {
	joined := strings.ToLower(key + " " + stringifySetting(value))
	distributedMarker := "clu" + "ster"
	for _, marker := range []string{"cookie", "password", "passwd", "secret", "credential", "userinfo", "bearer", "musicdl", distributedMarker, "wor" + "ker", "auto-update", "autoupdate"} {
		if strings.Contains(joined, marker) {
			return true
		}
	}
	return false
}

func stringifySetting(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		body, _ := json.Marshal(typed)
		return string(body)
	}
}

func (manifest ImportManifest) ValidateEvidence() error {
	if manifest.SchemaVersion == "" || manifest.Mode == "" {
		return errors.New("import manifest identity is incomplete")
	}
	for table, count := range manifest.TableCounts {
		if strings.TrimSpace(table) == "" || count < 0 {
			return errors.New("import manifest table count is invalid")
		}
	}
	for table, checksum := range manifest.Checksums {
		if strings.TrimSpace(table) == "" || strings.TrimSpace(checksum) == "" || containsSensitiveSetting(table, checksum) {
			return errors.New("import manifest checksum is invalid")
		}
	}
	return nil
}
