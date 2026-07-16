package server

import (
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
)

func TestAuthenticateAdminRejectsMissingEnvironmentPassword(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("PARSE_VIDEO_PASSWORD", "")

	originalMySQL := appInfra.mysql
	appInfra.mysql = nil
	t.Cleanup(func() { appInfra.mysql = originalMySQL })

	_, ok, err := authenticateAdmin("admin", "")
	if err != nil {
		t.Fatalf("authenticateAdmin() error = %v", err)
	}
	if ok {
		t.Fatal("authenticateAdmin() accepted an empty environment password")
	}
}

func TestLoadAdminSessionSecretFailsClosedWhenEntropyUnavailable(t *testing.T) {
	t.Setenv("ADMIN_SESSION_SECRET", "")
	restoreReader := replaceSecureRandomReaderForTest(failingRandomReader{})
	t.Cleanup(restoreReader)

	defer func() {
		if recover() == nil {
			t.Error("loadAdminSessionSecret() did not fail closed")
		}
	}()
	_ = loadAdminSessionSecret()
}

func TestDownloadFallbackSigningRejectsMissingSecret(t *testing.T) {
	setApplicationDownloadConfig(config.DownloadConfig{})
	t.Cleanup(func() { setApplicationDownloadConfig(config.DownloadConfig{}) })
	t.Setenv("ADMIN_SESSION_SECRET", "")

	if got := signDownloadFallbackToken("example", 1700000000); got != "" {
		t.Fatal("signDownloadFallbackToken() signed with an empty secret")
	}
	if verifyDownloadFallbackToken("example", 1700000000, "") {
		t.Fatal("verifyDownloadFallbackToken() accepted an empty secret")
	}
	expires := time.Now().Add(time.Hour).Unix()
	if got := signDownloadFallbackTicket("cache", "local", "example", expires); got != "" {
		t.Fatal("signDownloadFallbackTicket() signed with an empty secret")
	}
	if got := signDownloadFallbackProxyTicket(downloadFallbackRequest{MediaURL: "https://example.invalid/video.mp4", MediaType: "video"}, expires); got != "" {
		t.Fatal("signDownloadFallbackProxyTicket() signed with an empty secret")
	}
}
