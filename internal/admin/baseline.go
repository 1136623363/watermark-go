package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrBaselineMediaMissing = errors.New("baseline result missing media")

type BaselineSample struct {
	PlatformKey      string `json:"platformKey"`
	SampleURL        string `json:"sampleURL"`
	Enabled          bool   `json:"enabled"`
	ExpectedPlatform string `json:"expectedPlatform"`
	ExpectedType     string `json:"expectedType"`
}

type BaselineProvenance struct {
	SchemaVersion              int      `json:"schemaVersion"`
	SourceRepository           string   `json:"sourceRepository"`
	SourceCommit               string   `json:"sourceCommit"`
	SourceTree                 string   `json:"sourceTree"`
	BaselineDocument           string   `json:"baselineDocument"`
	BaselineDocumentSourcePath string   `json:"baselineDocumentSourcePathAtCapture"`
	BaselineDocumentTracked    bool     `json:"baselineDocumentTracked"`
	BaselineDocumentBinding    string   `json:"baselineDocumentBinding"`
	BaselineDocumentSHA256     string   `json:"baselineDocumentSha256"`
	CatalogInputs              []string `json:"catalogInputs"`
	CatalogManifestAlgorithm   string   `json:"catalogManifestAlgorithm"`
	CatalogManifestSHA256      string   `json:"catalogManifestSha256"`
	CanonicalFixturePath       string   `json:"canonicalFixturePath,omitempty"`
	CanonicalFixtureSHA256     string   `json:"canonicalFixtureSha256,omitempty"`
	CoverageClueDisposition    string   `json:"coverageClueDisposition,omitempty"`
}

type BaselineProvenanceRequest struct {
	ProvenancePath         string
	SourceRepositoryPath   string
	CanonicalFixturePath   string
	CanonicalFixtureSHA256 string
}

type BaselineParseOptions struct {
	ForceRefresh        bool
	BypassPositiveCache bool
	BypassNegativeCache bool
	BypassHistory       bool
	ParserInvocationID  string
}

type BaselineParseResult struct {
	Platform  string
	Type      string
	Title     string
	MediaURLs []string
}

type BaselineParser interface {
	Parse(context.Context, BaselineSample, BaselineParseOptions) (BaselineParseResult, error)
}

type BaselineRunOptions struct {
	Concurrency int
	RunID       string
	Entropy     io.Reader
	Clock       func() time.Time
}

type BaselineRunReport struct {
	RunID                  string           `json:"runId"`
	Status                 string           `json:"status"`
	Total                  int              `json:"total"`
	Completed              int              `json:"completed"`
	Success                int              `json:"success"`
	Failed                 int              `json:"failed"`
	DurationMS             int64            `json:"durationMs"`
	Concurrency            int              `json:"concurrency"`
	MaxObservedConcurrency int              `json:"maxObservedConcurrency"`
	CacheBypass            bool             `json:"cacheBypass"`
	NativeEnabled          bool             `json:"nativeEnabled"`
	FallbackEnabled        bool             `json:"fallbackEnabled"`
	Records                []BaselineRecord `json:"records"`
}

type BaselineRecord struct {
	SampleKey          string    `json:"sampleKey"`
	Platform           string    `json:"platform"`
	ExpectedPlatform   string    `json:"expectedPlatform"`
	Type               string    `json:"type"`
	ExpectedType       string    `json:"expectedType"`
	OK                 bool      `json:"ok"`
	Status             string    `json:"status"`
	Error              string    `json:"error,omitempty"`
	ErrorKind          error     `json:"-"`
	DurationMS         int64     `json:"durationMs"`
	StartedAt          time.Time `json:"startedAt"`
	EndedAt            time.Time `json:"endedAt"`
	ParserInvocationID string    `json:"parserInvocationId"`
	MediaURLs          []string  `json:"mediaUrls,omitempty"`
}

func FileSHA256(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func LoadBaselineFixture(path string) ([]BaselineSample, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(body), "canonicalFixtureSha256") {
		return nil, errors.New("baseline fixture must not contain its own trust hash")
	}
	var samples []BaselineSample
	if err := json.Unmarshal(body, &samples); err != nil {
		return nil, err
	}
	if !sort.SliceIsSorted(samples, func(i, j int) bool { return samples[i].PlatformKey < samples[j].PlatformKey }) {
		return nil, errors.New("baseline fixture is not sorted by platformKey")
	}
	return samples, nil
}

func EnabledBaselineSamples(samples []BaselineSample) []BaselineSample {
	enabled := make([]BaselineSample, 0, len(samples))
	for _, sample := range samples {
		if sample.Enabled && strings.TrimSpace(sample.SampleURL) != "" {
			enabled = append(enabled, sample)
		}
	}
	return enabled
}

func VerifyBaselineProvenance(request BaselineProvenanceRequest) (BaselineProvenance, error) {
	body, err := os.ReadFile(request.ProvenancePath)
	if err != nil {
		return BaselineProvenance{}, err
	}
	var provenance BaselineProvenance
	if err := json.Unmarshal(body, &provenance); err != nil {
		return BaselineProvenance{}, err
	}
	if provenance.SourceCommit != "1d3dc9a6064f3f2e41af9ea92a29566885939175" ||
		provenance.SourceTree != "d1aac032059b5622fc9f0b5cd6ce321a77978a1e" ||
		provenance.BaselineDocumentSHA256 != "a470a87e64242e5e97ee1a03571c43198a6bd7036c0b756e8c69fd9b639df29a" ||
		provenance.CatalogManifestSHA256 != "05d832a7d59897d16cd4bd26a7d02d6f6bdf5ec6829c1a280e974579fa29bf6a" {
		return BaselineProvenance{}, errors.New("baseline provenance does not match approved source anchors")
	}
	if strings.TrimSpace(provenance.BaselineDocumentSourcePath) != "" {
		if hash, err := FileSHA256(provenance.BaselineDocumentSourcePath); err == nil && hash != provenance.BaselineDocumentSHA256 {
			return BaselineProvenance{}, errors.New("baseline document hash mismatch")
		}
	}
	if request.CanonicalFixturePath != "" {
		hash, err := FileSHA256(request.CanonicalFixturePath)
		if err != nil {
			return BaselineProvenance{}, err
		}
		if hash != request.CanonicalFixtureSHA256 {
			return BaselineProvenance{}, errors.New("canonical fixture hash mismatch")
		}
	}
	if provenance.CanonicalFixturePath != "" && provenance.CanonicalFixturePath != "tests/baseline/fixtures/platform-samples.json" {
		return BaselineProvenance{}, errors.New("unexpected canonical fixture path")
	}
	if provenance.CanonicalFixtureSHA256 != "" && provenance.CanonicalFixtureSHA256 != request.CanonicalFixtureSHA256 {
		return BaselineProvenance{}, errors.New("unexpected canonical fixture hash")
	}
	return provenance, nil
}

func RunBaseline(ctx context.Context, samples []BaselineSample, parser BaselineParser, options BaselineRunOptions) (BaselineRunReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if parser == nil {
		return BaselineRunReport{}, errors.New("baseline parser is required")
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	entropy := options.Entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	runID := strings.TrimSpace(options.RunID)
	var err error
	if runID == "" {
		runID, err = randomBaselineID(entropy, "baseline")
		if err != nil {
			return BaselineRunReport{}, err
		}
	}
	concurrency := options.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	if concurrency != 3 {
		concurrency = 3
	}
	enabled := EnabledBaselineSamples(samples)
	report := BaselineRunReport{
		RunID:           runID,
		Status:          "running",
		Total:           len(enabled),
		Concurrency:     concurrency,
		CacheBypass:     true,
		NativeEnabled:   true,
		FallbackEnabled: true,
		Records:         make([]BaselineRecord, len(enabled)),
	}
	started := clock()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	active := 0
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				sample := enabled[index]
				invocationID, idErr := randomBaselineID(entropy, "parse")
				recordStarted := clock()
				record := BaselineRecord{
					SampleKey:          sample.PlatformKey,
					ExpectedPlatform:   sample.ExpectedPlatform,
					ExpectedType:       sample.ExpectedType,
					Status:             "completed",
					StartedAt:          recordStarted,
					ParserInvocationID: invocationID,
				}
				if idErr != nil {
					record.OK = false
					record.Error = idErr.Error()
				} else {
					mu.Lock()
					active++
					if active > report.MaxObservedConcurrency {
						report.MaxObservedConcurrency = active
					}
					mu.Unlock()
					result, parseErr := parser.Parse(ctx, sample, BaselineParseOptions{
						ForceRefresh:        true,
						BypassPositiveCache: true,
						BypassNegativeCache: true,
						BypassHistory:       true,
						ParserInvocationID:  invocationID,
					})
					mu.Lock()
					active--
					mu.Unlock()
					record.Platform = result.Platform
					record.Type = result.Type
					record.MediaURLs = append([]string(nil), result.MediaURLs...)
					if parseErr != nil {
						record.OK = false
						record.Error = parseErr.Error()
					} else if len(result.MediaURLs) == 0 {
						record.OK = false
						record.Error = ErrBaselineMediaMissing.Error()
						record.ErrorKind = ErrBaselineMediaMissing
					} else {
						record.OK = true
					}
				}
				record.EndedAt = clock()
				record.DurationMS = record.EndedAt.Sub(record.StartedAt).Milliseconds()
				report.Records[index] = record
			}
		}()
	}
	for index := range enabled {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return report, ctx.Err()
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()
	for _, record := range report.Records {
		report.Completed++
		if record.OK {
			report.Success++
		} else {
			report.Failed++
		}
	}
	report.Status = "completed"
	report.DurationMS = clock().Sub(started).Milliseconds()
	return report, nil
}

func randomBaselineID(reader io.Reader, prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw), nil
}
