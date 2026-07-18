package download

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

func TestDownloadTicketValidationMatrix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	key := []byte("dummy-download-signing-material-32b")
	claims := TicketClaims{
		TaskID:    "task_ticket",
		Purpose:   PurposeDownload,
		ExpiresAt: now.Add(time.Minute),
	}

	if _, err := SignTicket(nil, claims, now); !errors.Is(err, ErrSigningKeyRequired) {
		t.Fatalf("SignTicket(nil) error = %v, want ErrSigningKeyRequired", err)
	}
	if _, err := SignTicket(key, TicketClaims{TaskID: "", Purpose: PurposeDownload, ExpiresAt: now.Add(time.Minute)}, now); !errors.Is(err, ErrTicketTaskRequired) {
		t.Fatalf("SignTicket(empty task) error = %v, want ErrTicketTaskRequired", err)
	}
	if _, err := SignTicket(key, TicketClaims{TaskID: "task_ticket", Purpose: "", ExpiresAt: now.Add(time.Minute)}, now); !errors.Is(err, ErrTicketPurposeRequired) {
		t.Fatalf("SignTicket(empty purpose) error = %v, want ErrTicketPurposeRequired", err)
	}

	raw, err := SignTicket(key, claims, now)
	if err != nil {
		t.Fatalf("SignTicket() error = %v", err)
	}
	for name, verify := range map[string]func() error{
		"empty ticket": func() error {
			_, err := VerifyTicket(key, "", PurposeDownload, now)
			return err
		},
		"expired": func() error {
			_, err := VerifyTicket(key, raw, PurposeDownload, now.Add(2*time.Minute))
			return err
		},
		"cross purpose": func() error {
			_, err := VerifyTicket(key, raw, PurposePoll, now)
			return err
		},
		"tampered": func() error {
			_, err := VerifyTicket(key, raw+"tamper", PurposeDownload, now)
			return err
		},
	} {
		if err := verify(); err == nil {
			t.Fatalf("%s ticket verification succeeded, want failure", name)
		}
	}

	verified, err := VerifyTicket(key, raw, PurposeDownload, now)
	if err != nil {
		t.Fatalf("VerifyTicket(valid) error = %v", err)
	}
	if verified.TaskID != claims.TaskID || verified.Purpose != claims.Purpose || !verified.ExpiresAt.Equal(claims.ExpiresAt) {
		t.Fatalf("verified claims = %#v, want %#v", verified, claims)
	}
}

func TestDownloadFallbackRejectsAttemptBelowFourAndSSRF(t *testing.T) {
	service := newDownloadServiceForTest(t, ServiceOptions{})
	_, err := service.CreateFallback(context.Background(), CreateRequest{
		MediaURL:  "https://example.com/video.mp4",
		MediaType: MediaTypeVideo,
		Attempt:   3,
		ClientID:  "client-a",
	})
	if !errors.Is(err, ErrAttemptTooEarly) {
		t.Fatalf("CreateFallback(attempt=3) error = %v, want ErrAttemptTooEarly", err)
	}

	_, err = service.CreateFallback(context.Background(), CreateRequest{
		MediaURL:  "http://127.0.0.1/private.mp4",
		MediaType: MediaTypeVideo,
		Attempt:   4,
		ClientID:  "client-a",
	})
	if !errors.Is(err, netguard.ErrInvalidFetchURL) && !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("CreateFallback(loopback) error = %v, want netguard rejection", err)
	}
}

func TestDownloadMediaSizeLimits(t *testing.T) {
	for mediaType, want := range map[MediaType]int64{
		MediaTypeVideo: 300 << 20,
		MediaTypeAudio: 50 << 20,
		MediaTypeImage: 20 << 20,
	} {
		got, err := MaxBytesForMediaType(mediaType)
		if err != nil {
			t.Fatalf("MaxBytesForMediaType(%s) error = %v", mediaType, err)
		}
		if got != want {
			t.Fatalf("MaxBytesForMediaType(%s) = %d, want %d", mediaType, got, want)
		}
	}
}

func TestDownloadFallbackConcurrencyLimits(t *testing.T) {
	service := newDownloadServiceForTest(t, ServiceOptions{
		MaxGlobalConcurrency:    2,
		MaxPerClientConcurrency: 1,
	})
	releaseA, err := service.AcquireTransfer(context.Background(), "client-a")
	if err != nil {
		t.Fatalf("AcquireTransfer(client-a) error = %v", err)
	}
	defer releaseA()
	if _, err := service.AcquireTransfer(context.Background(), "client-a"); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("second same-client AcquireTransfer error = %v, want ErrConcurrencyLimit", err)
	}
	releaseB, err := service.AcquireTransfer(context.Background(), "client-b")
	if err != nil {
		t.Fatalf("AcquireTransfer(client-b) error = %v", err)
	}
	defer releaseB()
	if _, err := service.AcquireTransfer(context.Background(), "client-c"); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("third global AcquireTransfer error = %v, want ErrConcurrencyLimit", err)
	}
	releaseA()
	releaseC, err := service.AcquireTransfer(context.Background(), "client-c")
	if err != nil {
		t.Fatalf("AcquireTransfer after release error = %v", err)
	}
	releaseC()
}

func TestDownloadRangeServesPartialContent(t *testing.T) {
	service := newDownloadServiceForTest(t, ServiceOptions{})
	if err := service.WriteCompletedFile(context.Background(), "task_range", []byte("0123456789"), "video/mp4"); err != nil {
		t.Fatalf("WriteCompletedFile() error = %v", err)
	}

	rangeReq := httptest.NewRequest(http.MethodGet, "/api/download/file/task_range", nil)
	rangeReq.Header.Set("Range", "bytes=2-5")
	rangeResponse := httptest.NewRecorder()
	if err := service.ServeTaskFile(rangeResponse, rangeReq, "task_range"); err != nil {
		t.Fatalf("ServeTaskFile(range) error = %v", err)
	}
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "2345" {
		t.Fatalf("range response status/body = %d %q, want 206 %q", rangeResponse.Code, rangeResponse.Body.String(), "2345")
	}
	if got := rangeResponse.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q, want bytes 2-5/10", got)
	}

	fullReq := httptest.NewRequest(http.MethodGet, "/api/download/file/task_range", nil)
	fullResponse := httptest.NewRecorder()
	if err := service.ServeTaskFile(fullResponse, fullReq, "task_range"); err != nil {
		t.Fatalf("ServeTaskFile(full) error = %v", err)
	}
	if fullResponse.Code != http.StatusOK || fullResponse.Body.String() != "0123456789" {
		t.Fatalf("full response status/body = %d %q", fullResponse.Code, fullResponse.Body.String())
	}
}

func TestDownloadStreamingIdleDeadlineAllowsProgressButStopsStalls(t *testing.T) {
	var progressing bytes.Buffer
	progressReader := &scriptedReader{
		steps: []readStep{
			{delay: 10 * time.Millisecond, data: "a"},
			{delay: 10 * time.Millisecond, data: "b"},
			{delay: 10 * time.Millisecond, data: "c"},
		},
	}
	written, err := CopyWithIdleDeadline(context.Background(), &progressing, progressReader, StreamOptions{
		IdleTimeout: 25 * time.Millisecond,
		BufferSize:  1,
	})
	if err != nil {
		t.Fatalf("CopyWithIdleDeadline(progress) error = %v", err)
	}
	if written != 3 || progressing.String() != "abc" {
		t.Fatalf("progressing copy wrote %d %q, want 3 abc", written, progressing.String())
	}

	stallingReader := &scriptedReader{
		steps: []readStep{
			{delay: 50 * time.Millisecond, data: "late"},
		},
	}
	var stalled bytes.Buffer
	if _, err := CopyWithIdleDeadline(context.Background(), &stalled, stallingReader, StreamOptions{
		IdleTimeout: 10 * time.Millisecond,
		BufferSize:  4,
	}); !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("CopyWithIdleDeadline(stall) error = %v, want ErrStreamIdleTimeout", err)
	}
}

func TestDownloadTempRootAndFilesArePrivateAndSymlinkRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "download-root")
	service := newDownloadServiceForTest(t, ServiceOptions{TempRoot: root})
	if err := service.EnsureRoot(); err != nil {
		t.Fatalf("EnsureRoot() error = %v", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("root permissions = %o, want 0700", got)
	}
	if _, err := service.TaskFilePath("../escape"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("TaskFilePath traversal error = %v, want ErrUnsafePath", err)
	}

	if err := service.WriteCompletedFile(context.Background(), "task_private", []byte("media"), "video/mp4"); err != nil {
		t.Fatalf("WriteCompletedFile(private) error = %v", err)
	}
	path, err := service.TaskFilePath("task_private")
	if err != nil {
		t.Fatalf("TaskFilePath(private) error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat media file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %o, want 0600", got)
	}

	symlinkPath, err := service.TaskFilePath("task_symlink")
	if err != nil {
		t.Fatalf("TaskFilePath(symlink) error = %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "escape"), symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	err = service.WriteCompletedFile(context.Background(), "task_symlink", []byte("media"), "video/mp4")
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("WriteCompletedFile(symlink) error = %v, want ErrUnsafePath", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			t.Fatalf("left partial file after symlink rejection: %s", entry.Name())
		}
	}
}

func TestDownloadCleanupExpiredKeepsRunningTasks(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	service := newDownloadServiceForTest(t, ServiceOptions{
		Clock: func() time.Time { return now },
	})
	if err := service.WriteCompletedFile(context.Background(), "task_done", []byte("media"), "video/mp4"); err != nil {
		t.Fatalf("WriteCompletedFile(done) error = %v", err)
	}
	donePath, err := service.TaskFilePath("task_done")
	if err != nil {
		t.Fatalf("TaskFilePath(done) error = %v", err)
	}
	service.mu.Lock()
	service.tasks["task_done"] = TaskView{TaskID: "task_done", Status: StatusCompleted, ExpiresAt: now.Add(-time.Minute)}
	service.tasks["task_running"] = TaskView{TaskID: "task_running", Status: StatusRunning, ExpiresAt: now.Add(-time.Minute)}
	service.mu.Unlock()

	removed, err := service.CleanupExpired(context.Background(), now)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("CleanupExpired() removed = %d, want 1", removed)
	}
	service.mu.Lock()
	_, doneExists := service.tasks["task_done"]
	_, runningExists := service.tasks["task_running"]
	service.mu.Unlock()
	if doneExists || !runningExists {
		t.Fatalf("cleanup state doneExists=%t runningExists=%t, want false/true", doneExists, runningExists)
	}
	if _, err := os.Stat(donePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired file stat error = %v, want os.ErrNotExist", err)
	}
}

func newDownloadServiceForTest(t *testing.T, options ServiceOptions) *Service {
	t.Helper()
	if len(options.SigningKey) == 0 {
		options.SigningKey = []byte("dummy-download-signing-material-32b")
	}
	if options.TempRoot == "" {
		options.TempRoot = t.TempDir()
	}
	if options.Entropy == nil {
		options.Entropy = &downloadSequenceReader{}
	}
	service, err := NewService(options)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

type downloadSequenceReader struct{ next byte }

func (reader *downloadSequenceReader) Read(p []byte) (int, error) {
	for index := range p {
		reader.next++
		p[index] = reader.next
	}
	return len(p), nil
}

type readStep struct {
	delay time.Duration
	data  string
	err   error
}

type scriptedReader struct {
	steps []readStep
	index int
}

func (reader *scriptedReader) Read(p []byte) (int, error) {
	if reader.index >= len(reader.steps) {
		return 0, io.EOF
	}
	step := reader.steps[reader.index]
	reader.index++
	time.Sleep(step.delay)
	if step.data != "" {
		return copy(p, step.data), step.err
	}
	if step.err != nil {
		return 0, step.err
	}
	return 0, io.EOF
}
