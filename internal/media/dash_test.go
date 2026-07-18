package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

func TestDASHAsyncOnlyAndRequiresPairedTracks(t *testing.T) {
	videoOnly := []coreparser.MediaCandidate{{
		URL:        "https://example.com/video-low.m4s",
		Kind:       coreparser.MediaKindVideo,
		Quality:    480,
		SourceRank: 0,
	}}
	if AllowsSynchronousDASHProjection(videoOnly, nil) {
		t.Fatal("unpaired DASH tracks were projected into synchronous VideoURL")
	}
	if _, err := NewDASHJob(DASHRequest{Video: videoOnly}); !errors.Is(err, ErrDASHPairRequired) {
		t.Fatalf("NewDASHJob(video only) error = %v, want ErrDASHPairRequired", err)
	}
}

func TestDASHCandidateOrderAndFallbackBudget(t *testing.T) {
	candidates := []coreparser.MediaCandidate{
		{URL: "https://example.com/video-low.m4s", Kind: coreparser.MediaKindVideo, Quality: 480, Width: 854, Height: 480, SourceRank: 0},
		{URL: "http://127.0.0.1/private.m4s", Kind: coreparser.MediaKindVideo, Quality: 2160, Width: 3840, Height: 2160, SourceRank: 1},
		{URL: "https://example.com/video-high.m4s", Kind: coreparser.MediaKindVideo, Quality: 1080, Width: 1920, Height: 1080, SourceRank: 2},
	}
	ordered := OrderDASHCandidates(candidates)
	if ordered[0].URL != "https://example.com/video-high.m4s" {
		t.Fatalf("ordered[0] = %#v, want metadata-ranked high quality candidate", ordered[0])
	}

	var attempted []string
	selected, err := SelectDASHCandidate(context.Background(), candidates, NewFallbackBudget(2), func(_ context.Context, candidate coreparser.MediaCandidate) error {
		attempted = append(attempted, candidate.URL)
		if strings.Contains(candidate.URL, "high") {
			return errors.New("candidate failed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SelectDASHCandidate() error = %v", err)
	}
	if selected.URL != "https://example.com/video-low.m4s" {
		t.Fatalf("selected candidate = %#v, want fallback to low quality", selected)
	}
	if strings.Join(attempted, ",") != "https://example.com/video-high.m4s,https://example.com/video-low.m4s" {
		t.Fatalf("attempted candidates = %#v, want unsafe skipped and budgeted fallback", attempted)
	}

	_, err = SelectDASHCandidate(context.Background(), candidates, NewFallbackBudget(1), func(context.Context, coreparser.MediaCandidate) error {
		return errors.New("candidate failed")
	})
	if !errors.Is(err, ErrFallbackBudgetExceeded) {
		t.Fatalf("SelectDASHCandidate(budget=1) error = %v, want ErrFallbackBudgetExceeded", err)
	}
}

func TestDASHPrefetchConcurrencyMaxTwoAndCleanupOnFailure(t *testing.T) {
	root := t.TempDir()
	var active int32
	var maxActive int32
	prefetcher := NewDASHPrefetcher(DASHPrefetchOptions{
		TempRoot:       root,
		MaxConcurrency: 2,
		Fetch: func(_ context.Context, source DASHFetchSource, path string) error {
			current := atomic.AddInt32(&active, 1)
			for {
				previous := atomic.LoadInt32(&maxActive)
				if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			if err := os.WriteFile(path, []byte(source.Name), 0o600); err != nil {
				return err
			}
			atomic.AddInt32(&active, -1)
			return nil
		},
	})
	files, err := prefetcher.Prefetch(context.Background(), []DASHFetchSource{
		{Name: "video", URL: "https://example.com/video.m4s", MaxBytes: 10},
		{Name: "audio", URL: "https://example.com/audio.m4s", MaxBytes: 10},
		{Name: "subtitle", URL: "https://example.com/subtitle.m4s", MaxBytes: 10},
		{Name: "cover", URL: "https://example.com/cover.m4s", MaxBytes: 10},
	})
	if err != nil {
		t.Fatalf("Prefetch() error = %v", err)
	}
	if maxActive > 2 {
		t.Fatalf("max active prefetch = %d, want <= 2", maxActive)
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat prefetched file: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("prefetched permissions = %o, want 0600", got)
		}
	}

	err = MergeDASH(context.Background(), DASHMergeRequest{
		TempRoot:   root,
		VideoPath:  files[0],
		AudioPath:  files[1],
		OutputPath: filepath.Join(root, "merged.mp4"),
		Runner: RunnerFunc(func(context.Context, FFmpegCommand) error {
			return errors.New("ffmpeg failed")
		}),
	})
	if !errors.Is(err, ErrFFmpegFailed) {
		t.Fatalf("MergeDASH() error = %v, want ErrFFmpegFailed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "merged.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed merge left final output, stat error = %v", err)
	}
	assertNoPartFiles(t, root)
}

func TestDASHLeaseLossCancelsRunnerAndCleansPartials(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var runnerStarted sync.WaitGroup
	runnerStarted.Add(1)
	errc := make(chan error, 1)
	go func() {
		errc <- MergeDASH(ctx, DASHMergeRequest{
			TempRoot:   root,
			VideoPath:  writeLocalMediaFile(t, root, "video.m4s"),
			AudioPath:  writeLocalMediaFile(t, root, "audio.m4s"),
			OutputPath: filepath.Join(root, "merged.mp4"),
			Runner: RunnerFunc(func(ctx context.Context, _ FFmpegCommand) error {
				runnerStarted.Done()
				<-ctx.Done()
				return ctx.Err()
			}),
		})
	}()
	runnerStarted.Wait()
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("MergeDASH(cancel) error = %v, want context.Canceled", err)
	}
	assertNoPartFiles(t, root)
}

func TestDASHLocalRunnerRejectsDynamicExecutableBeforeStart(t *testing.T) {
	err := LocalFFmpegRunner{}.Run(context.Background(), FFmpegCommand{
		Executable: filepath.Join(t.TempDir(), "ffmpeg"),
		Args:       []string{"-protocol_whitelist", "file"},
	})
	if !errors.Is(err, ErrFFmpegExecutable) {
		t.Fatalf("LocalFFmpegRunner.Run(dynamic executable) error = %v, want ErrFFmpegExecutable", err)
	}
}

func writeLocalMediaFile(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func assertNoPartFiles(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read temp root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			t.Fatalf("left partial file after cleanup: %s", entry.Name())
		}
	}
}
