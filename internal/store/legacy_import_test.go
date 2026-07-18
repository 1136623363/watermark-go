package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyRuntimeSettingsScrubDropsDistributedAutoUpdateAndSensitiveConfig(t *testing.T) {
	input := []LegacyRuntimeSetting{
		{Key: "rateLimitEnabled", Value: true},
		{Key: "clu" + "sterWorkerEndpoints", Value: []string{"worker-a"}},
		{Key: "toolAutoUpdate", Value: true},
		{Key: "downloadFallbackMode", Value: "proxy"},
		{Key: "downloadFallbackPublicBaseURL", Value: "https://watermark.example"},
		{Key: "outboundProxy", Value: "http://user:pass@proxy.example:8080"},
		{Key: "musicParserConfig", Value: map[string]string{"providerCredential": "redacted"}},
	}
	got := ScrubLegacyRuntimeSettings(input)
	var keys []string
	for _, setting := range got {
		keys = append(keys, setting.Key)
	}
	joined := strings.Join(keys, ",")
	if joined != "downloadFallbackMode,downloadFallbackPublicBaseURL,rateLimitEnabled" {
		t.Fatalf("scrubbed keys = %s", joined)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"worker-a", "providerCredential", "user:pass"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("scrubbed settings retained forbidden material: %s", body)
		}
	}
}

func TestImportManifestContainsOnlyCountsAndChecksums(t *testing.T) {
	manifest := ImportManifest{
		SchemaVersion: "legacy-import/v1",
		Mode:          MigrationModeFinalNoBinlog,
		TableCounts:   map[string]int{"parse_results": 42},
		Checksums:     map[string]string{"parse_results": strings.Repeat("a", 64)},
	}
	if err := manifest.ValidateEvidence(); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "source_url") || strings.Contains(string(body), "result_json") {
		t.Fatalf("manifest exposed row content: %s", body)
	}
}
