package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

var (
	ErrDASHPairRequired       = errors.New("dash video/audio pair is required")
	ErrFallbackBudgetExceeded = errors.New("dash fallback budget exceeded")
	ErrNoDASHCandidate        = errors.New("dash candidate unavailable")
)

type DASHRequest struct {
	Video []coreparser.MediaCandidate
	Audio []coreparser.MediaCandidate
}

type DASHJob struct {
	Video coreparser.MediaCandidate
	Audio coreparser.MediaCandidate
}

type FallbackBudget struct {
	max      int
	attempts int
}

type CandidateProbe func(context.Context, coreparser.MediaCandidate) error

type DASHFetchSource struct {
	Name     string
	URL      string
	MaxBytes int64
}

type DASHFetchFunc func(context.Context, DASHFetchSource, string) error

type DASHPrefetchOptions struct {
	TempRoot       string
	MaxConcurrency int
	Fetch          DASHFetchFunc
}

type DASHPrefetcher struct {
	tempRoot       string
	maxConcurrency int
	fetch          DASHFetchFunc
}

type Runner interface {
	Run(context.Context, FFmpegCommand) error
}

type RunnerFunc func(context.Context, FFmpegCommand) error

func (function RunnerFunc) Run(ctx context.Context, command FFmpegCommand) error {
	return function(ctx, command)
}

type DASHMergeRequest struct {
	TempRoot   string
	VideoPath  string
	AudioPath  string
	OutputPath string
	Runner     Runner
}

func AllowsSynchronousDASHProjection(_, _ []coreparser.MediaCandidate) bool {
	return false
}

func NewDASHJob(request DASHRequest) (DASHJob, error) {
	video := OrderDASHCandidates(request.Video)
	audio := OrderDASHCandidates(request.Audio)
	if len(video) == 0 || len(audio) == 0 {
		return DASHJob{}, ErrDASHPairRequired
	}
	return DASHJob{Video: video[0], Audio: audio[0]}, nil
}

func OrderDASHCandidates(candidates []coreparser.MediaCandidate) []coreparser.MediaCandidate {
	filtered := make([]coreparser.MediaCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := netguard.NewFetchURL(candidate.URL); err != nil {
			continue
		}
		filtered = append(filtered, candidate)
	}
	coreparser.SortMediaCandidates(filtered)
	return filtered
}

func NewFallbackBudget(max int) *FallbackBudget {
	return &FallbackBudget{max: max}
}

func SelectDASHCandidate(ctx context.Context, candidates []coreparser.MediaCandidate, budget *FallbackBudget, probe CandidateProbe) (coreparser.MediaCandidate, error) {
	if ctx == nil || budget == nil || probe == nil {
		return coreparser.MediaCandidate{}, ErrNoDASHCandidate
	}
	for _, candidate := range OrderDASHCandidates(candidates) {
		if err := ctx.Err(); err != nil {
			return coreparser.MediaCandidate{}, err
		}
		if !budget.allow() {
			return coreparser.MediaCandidate{}, ErrFallbackBudgetExceeded
		}
		if err := probe(ctx, candidate); err == nil {
			return candidate, nil
		}
	}
	if budget.max > 0 && budget.attempts >= budget.max {
		return coreparser.MediaCandidate{}, ErrFallbackBudgetExceeded
	}
	return coreparser.MediaCandidate{}, ErrNoDASHCandidate
}

func (budget *FallbackBudget) allow() bool {
	if budget.max <= 0 {
		budget.attempts++
		return true
	}
	if budget.attempts >= budget.max {
		return false
	}
	budget.attempts++
	return true
}

func NewDASHPrefetcher(options DASHPrefetchOptions) *DASHPrefetcher {
	maxConcurrency := options.MaxConcurrency
	if maxConcurrency <= 0 || maxConcurrency > 2 {
		maxConcurrency = 2
	}
	return &DASHPrefetcher{
		tempRoot:       options.TempRoot,
		maxConcurrency: maxConcurrency,
		fetch:          options.Fetch,
	}
}

func (prefetcher *DASHPrefetcher) Prefetch(ctx context.Context, sources []DASHFetchSource) ([]string, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	root, err := absoluteRoot(prefetcher.tempRoot)
	if err != nil {
		return nil, err
	}
	if prefetcher.fetch == nil {
		return nil, errors.New("dash fetch function is required")
	}
	result := make([]string, len(sources))
	var created []string
	var mu sync.Mutex
	var wait sync.WaitGroup
	semaphore := make(chan struct{}, prefetcher.maxConcurrency)
	errc := make(chan error, len(sources))
	for index, source := range sources {
		index, source := index, source
		if _, err := netguard.NewFetchURL(source.URL); err != nil {
			return nil, err
		}
		name, err := safeGeneratedName(source.Name, index)
		if err != nil {
			return nil, err
		}
		finalPath := filepath.Join(root, name)
		partPath := finalPath + ".part"
		result[index] = finalPath
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			}
			defer func() { <-semaphore }()
			if err := prefetcher.fetch(ctx, source, partPath); err != nil {
				_ = os.Remove(partPath)
				errc <- err
				return
			}
			info, err := os.Stat(partPath)
			if err != nil {
				errc <- err
				return
			}
			if source.MaxBytes > 0 && info.Size() > source.MaxBytes {
				_ = os.Remove(partPath)
				errc <- ErrM3U8Limit
				return
			}
			if err := os.Chmod(partPath, 0o600); err != nil {
				_ = os.Remove(partPath)
				errc <- err
				return
			}
			if err := os.Rename(partPath, finalPath); err != nil {
				_ = os.Remove(partPath)
				errc <- err
				return
			}
			if err := os.Chmod(finalPath, 0o600); err != nil {
				errc <- err
				return
			}
			mu.Lock()
			created = append(created, finalPath)
			mu.Unlock()
		}()
	}
	wait.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			for _, path := range created {
				_ = os.Remove(path)
			}
			return nil, err
		}
	}
	return result, nil
}

func MergeDASH(ctx context.Context, request DASHMergeRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	videoPath, err := requireLocalPath(request.TempRoot, request.VideoPath)
	if err != nil {
		return err
	}
	audioPath, err := requireLocalPath(request.TempRoot, request.AudioPath)
	if err != nil {
		return err
	}
	outputPath, err := requireLocalPath(request.TempRoot, request.OutputPath)
	if err != nil {
		return err
	}
	partPath := outputPath + ".part"
	defer func() {
		_ = os.Remove(partPath)
	}()
	if request.Runner == nil {
		return ErrFFmpegFailed
	}
	command := FFmpegCommand{
		Executable: "ffmpeg",
		Args: []string{
			"-hide_banner",
			"-nostdin",
			"-protocol_whitelist", "file",
			"-i", videoPath,
			"-i", audioPath,
			"-c", "copy",
			"-y", partPath,
		},
	}
	if err := validateFFmpegCommand(command); err != nil {
		return err
	}
	if err := request.Runner.Run(ctx, command); err != nil {
		_ = os.Remove(partPath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.Join(ErrFFmpegFailed, err)
	}
	if _, err := os.Stat(partPath); err != nil {
		return errors.Join(ErrFFmpegFailed, err)
	}
	if err := os.Rename(partPath, outputPath); err != nil {
		return err
	}
	if err := os.Chmod(outputPath, 0o600); err != nil {
		return err
	}
	return nil
}

func safeGeneratedName(name string, index int) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "asset"
	}
	name = strings.ReplaceAll(name, " ", "_")
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return "", ErrUnsafeLocalPath
	}
	return strings.ToLower(name) + "_" + strings.TrimLeft(filepath.Ext(name), ".") + fallbackIndexSuffix(index), nil
}

func fallbackIndexSuffix(index int) string {
	return "_" + strconv.Itoa(index) + ".bin"
}
