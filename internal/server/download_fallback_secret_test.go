package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

func TestDownloadFallbackURLBuildersRejectMissingSecret(t *testing.T) {
	setApplicationDownloadConfig(config.DownloadConfig{})
	t.Cleanup(func() { setApplicationDownloadConfig(config.DownloadConfig{}) })
	t.Setenv("DOWNLOAD_TOKEN_SECRET", strings.Repeat("C9", 20))
	t.Setenv("DOWNLOAD_FALLBACK_TOKEN_SECRET", strings.Repeat("D8", 20))
	t.Setenv("ADMIN_SESSION_SECRET", strings.Repeat("A7", 20))
	t.Setenv("DOWNLOAD_FALLBACK_CDN_BASE_URL", "https://cdn.example.invalid")
	context := downloadFallbackTestContext()
	request := downloadFallbackRequest{MediaURL: "https://8.8.8.8/video.mp4", MediaType: "video"}

	if _, err := buildDownloadFallbackFileURL(context, "local", "video_example.mp4"); err == nil {
		t.Fatal("buildDownloadFallbackFileURL() accepted a missing independent secret")
	}
	if _, err := buildDownloadFallbackProxyURL(context, request); err == nil {
		t.Fatal("buildDownloadFallbackProxyURL() accepted a missing independent secret")
	}
	if _, err := buildDownloadFallbackPollPath("local", "task-example", "video_example.mp4"); err == nil {
		t.Fatal("buildDownloadFallbackPollPath() accepted a missing independent secret")
	}
	if _, err := buildDownloadFallbackCDNFileURL(clusterNodeInfo{ID: "local"}, "video_example.mp4"); err == nil {
		t.Fatal("buildDownloadFallbackCDNFileURL() accepted a missing independent secret")
	}
}

func TestDownloadFallbackURLBuildersRejectEmptyTicketInputs(t *testing.T) {
	setApplicationDownloadConfig(config.DownloadConfig{TokenSecret: strings.Repeat("A7", 20)})
	t.Cleanup(func() { setApplicationDownloadConfig(config.DownloadConfig{}) })
	t.Setenv("DOWNLOAD_FALLBACK_CDN_BASE_URL", "https://cdn.example.invalid")
	context := downloadFallbackTestContext()

	if _, err := buildDownloadFallbackFileURL(context, "local", ""); err == nil {
		t.Fatal("buildDownloadFallbackFileURL() accepted an empty file key")
	}
	if _, err := buildDownloadFallbackPollPath("local", "", "video_example.mp4"); err == nil {
		t.Fatal("buildDownloadFallbackPollPath() accepted an empty task ID")
	}
	if _, err := buildDownloadFallbackProxyURL(context, downloadFallbackRequest{}); err == nil {
		t.Fatal("buildDownloadFallbackProxyURL() accepted an empty media URL")
	}
	if got := signDownloadFallbackTicket("file", "local", "", 4102444800); got != "" {
		t.Fatal("signDownloadFallbackTicket() signed an empty value")
	}
}

func TestDownloadFallbackEnabledHandlerRejectsMissingSecret(t *testing.T) {
	if err := runtimecfg.Load(); err != nil {
		t.Fatalf("load original runtime settings: %v", err)
	}
	original := runtimecfg.Current()
	t.Run("request", testDownloadFallbackEnabledHandlerRejectsMissingSecret)
	if restored := runtimecfg.Current(); !reflect.DeepEqual(restored, original) {
		t.Fatal("runtime settings were not restored after handler test")
	}
}

func testDownloadFallbackEnabledHandlerRejectsMissingSecret(t *testing.T) {
	t.Cleanup(func() {
		if err := runtimecfg.Load(); err != nil {
			t.Error("restore runtime settings after handler test")
		}
	})
	t.Setenv("DOWNLOAD_FALLBACK_ENABLED", "true")
	t.Setenv("DOWNLOAD_FALLBACK_MODE", runtimecfg.DownloadFallbackModeProxy)
	setApplicationDownloadConfig(config.DownloadConfig{})
	t.Cleanup(func() { setApplicationDownloadConfig(config.DownloadConfig{}) })
	t.Setenv("DOWNLOAD_TOKEN_SECRET", strings.Repeat("C9", 20))
	t.Setenv("DOWNLOAD_FALLBACK_TOKEN_SECRET", strings.Repeat("D8", 20))
	t.Setenv("ADMIN_SESSION_SECRET", strings.Repeat("A7", 20))
	if err := runtimecfg.Load(); err != nil {
		t.Fatalf("load test runtime settings: %v", err)
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	body := `{"sourceUrl":"https://8.8.8.8/source","mediaUrl":"https://8.8.8.8/video.mp4","mediaType":"video","attempt":4}`
	context.Request = httptest.NewRequest(http.MethodPost, "/api/download/fallback", bytes.NewBufferString(body))
	context.Request.Header.Set("Content-Type", "application/json")
	handleDownloadFallbackCreate(context)

	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode handler response: %v", err)
	}
	if recorder.Code != http.StatusOK || envelope.Code == 0 || len(envelope.Data) != 0 {
		t.Fatalf("missing-secret response = HTTP %d code %d data-present=%t", recorder.Code, envelope.Code, len(envelope.Data) != 0)
	}
}

func TestDownloadFallbackSecretUsesOnlyCanonicalTypedConfig(t *testing.T) {
	typedValue := strings.Repeat("T6", 20)
	setApplicationDownloadConfig(config.DownloadConfig{TokenSecret: typedValue})
	t.Cleanup(func() { setApplicationDownloadConfig(config.DownloadConfig{}) })
	t.Setenv("DOWNLOAD_TOKEN_SECRET", strings.Repeat("C9", 20))
	t.Setenv("DOWNLOAD_FALLBACK_TOKEN_SECRET", strings.Repeat("L8", 20))

	if got := downloadFallbackTokenSecret(); got != typedValue {
		t.Fatal("download fallback business logic did not use only canonical typed config")
	}
}

func downloadFallbackTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "http://localhost/test", nil)
	return context
}
