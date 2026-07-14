package server

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestSecuritySensitiveIDCallsitesPropagateEntropyFailure(t *testing.T) {
	restoreReader := replaceSecureRandomReaderForTest(failingRandomReader{})
	t.Cleanup(restoreReader)

	if _, err := newTaskID(); err == nil {
		t.Fatal("newTaskID() ignored entropy failure")
	}
	if _, err := newAdminPlatformTestRunID(); err == nil {
		t.Fatal("newAdminPlatformTestRunID() ignored entropy failure")
	}
	store := &downloadFallbackStore{tasks: make(map[string]*downloadFallbackTask), byKey: make(map[string]string)}
	if _, _, err := store.enqueue(downloadFallbackRequest{MediaURL: "https://8.8.8.8/video.mp4", MediaType: "video"}, "video_example.mp4", filepath.Join(t.TempDir(), "video_example.mp4")); err == nil {
		t.Fatal("download fallback enqueue ignored entropy failure")
	}
	if len(store.tasks) != 0 || len(store.byKey) != 0 {
		t.Fatal("download fallback enqueue mutated the store after entropy failure")
	}

	t.Setenv("DOWNLOAD_FALLBACK_TMP_DIR", t.TempDir())
	task := downloadFallbackTask{FileKey: "video_example.mp4", FilePath: filepath.Join(t.TempDir(), "video_example.mp4")}
	if err := downloadFallbackMedia(context.Background(), "task-example", task, ""); err == nil {
		t.Fatal("downloadFallbackMedia() ignored temporary-name entropy failure")
	}
	if got := newDownloadFallbackObservationRequestID(); got != "" {
		t.Fatal("observation request ID used a predictable fallback")
	}
}

func TestParseLockEntropyFailureUsesProcessLock(t *testing.T) {
	originalRedis := appInfra.redis
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	appInfra.redis = client
	parseInMemoryLocks = sync.Map{}
	t.Cleanup(func() {
		appInfra.redis = originalRedis
		_ = client.Close()
		parseInMemoryLocks = sync.Map{}
	})
	restoreReader := replaceSecureRandomReaderForTest(failingRandomReader{})
	t.Cleanup(restoreReader)

	release, ok := acquireParseLock("https://example.invalid/video")
	if !ok {
		t.Fatal("acquireParseLock() did not fall back to the process lock")
	}
	release()
}
