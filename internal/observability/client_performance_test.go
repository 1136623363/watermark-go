package observability

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientPerformanceEndpointIsAnonymousBoundedAndNonBlocking(t *testing.T) {
	collector := NewPerformanceCollector(PerformanceOptions{Capacity: 1, MaxBodyBytes: 64})
	first := httptest.NewRecorder()
	collector.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/client/performance", strings.NewReader(`{"name":"first"}`)))
	if first.Code != http.StatusOK {
		t.Fatalf("first performance status = %d body=%s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	start := time.Now()
	collector.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/client/performance", strings.NewReader(`{"name":"second"}`)))
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("full performance channel blocked for %s", elapsed)
	}
	if second.Code != http.StatusOK || collector.Dropped() != 1 {
		t.Fatalf("second status/dropped = %d/%d, want 200/1", second.Code, collector.Dropped())
	}
	oversized := httptest.NewRecorder()
	collector.ServeHTTP(oversized, httptest.NewRequest(http.MethodPost, "/api/client/performance", strings.NewReader(strings.Repeat("x", 65))))
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413", oversized.Code)
	}
}

func TestJSONLoggerKeepsLowCardinalityAndRedactsSentinels(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONLogger(&output)
	logger.Log(Event{
		RequestID: "0123456789abcdef0123456789abcdef",
		TaskID:    "task_safe",
		Platform:  "douyin",
		Parser:    "native",
		Stage:     "parse",
		Attempt:   4,
		Cache:     "miss",
		Fallback:  true,
		ErrorKind: "timeout",
		Duration:  25 * time.Millisecond,
		RawURL:    "https://example.com/path?token=sentinel-url-token",
		Headers: map[string]string{
			"Cookie":        "sentinel-cookie",
			"Authorization": "Bearer sentinel-auth",
		},
		Error: "upstream body contained sentinel-upstream-body",
	})
	line := output.String()
	for _, forbidden := range []string{"sentinel-url-token", "sentinel-cookie", "sentinel-auth", "sentinel-upstream-body", "https://example.com/path"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("log leaked %q: %s", forbidden, line)
		}
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log is not JSON: %v line=%s", err, line)
	}
	for _, required := range []string{"requestId", "taskId", "platform", "parser", "stage", "attempt", "cache", "fallback", "errorKind", "durationMs"} {
		if _, ok := record[required]; !ok {
			t.Fatalf("log omitted low-cardinality field %q: %#v", required, record)
		}
	}
	if _, ok := record["url"]; ok {
		t.Fatalf("log included url field: %#v", record)
	}
	if _, ok := record["error"]; ok {
		t.Fatalf("log included raw error field: %#v", record)
	}
}
