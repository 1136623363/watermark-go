package admin

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

const approvedCanonicalFixtureSHA256 = "bb0f55ea17ddc613f64282a5786a7ab137a945a847b444fdd2f4bfb212bc5eba"

func TestBaselineFixtureHashCountsAndOrdering(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "baseline", "fixtures", "platform-samples.json")
	hash, err := FileSHA256(path)
	if err != nil {
		t.Fatalf("FileSHA256() error = %v", err)
	}
	if hash != approvedCanonicalFixtureSHA256 {
		t.Fatalf("fixture hash = %s, want %s", hash, approvedCanonicalFixtureSHA256)
	}
	fixture, err := LoadBaselineFixture(path)
	if err != nil {
		t.Fatalf("LoadBaselineFixture() error = %v", err)
	}
	if len(fixture) != 96 {
		t.Fatalf("fixture items = %d, want 96", len(fixture))
	}
	if got := len(EnabledBaselineSamples(fixture)); got != 93 {
		t.Fatalf("enabled samples = %d, want 93", got)
	}
	disabled := []string{}
	keys := []string{}
	for _, sample := range fixture {
		keys = append(keys, sample.PlatformKey)
		if !sample.Enabled {
			disabled = append(disabled, sample.PlatformKey)
		}
		if sample.ExpectedPlatform != sample.PlatformKey {
			t.Fatalf("%s expectedPlatform = %q, want same platform key", sample.PlatformKey, sample.ExpectedPlatform)
		}
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("fixture platform keys are not sorted: %#v", keys)
	}
	if got := stringsJoin(disabled, ","); got != "doupai,huoshan,xinpianchang" {
		t.Fatalf("disabled platforms = %s, want doupai,huoshan,xinpianchang", got)
	}
}

func TestBaselineProvenanceMatchesApprovedSourcesAndFixture(t *testing.T) {
	provenance, err := VerifyBaselineProvenance(BaselineProvenanceRequest{
		ProvenancePath:         filepath.Join("..", "..", "docs", "baseline-provenance.json"),
		SourceRepositoryPath:   "/srv/watermark/watermark-backend",
		CanonicalFixturePath:   filepath.Join("..", "..", "tests", "baseline", "fixtures", "platform-samples.json"),
		CanonicalFixtureSHA256: approvedCanonicalFixtureSHA256,
	})
	if err != nil {
		t.Fatalf("VerifyBaselineProvenance() error = %v", err)
	}
	if provenance.SourceCommit != "1d3dc9a6064f3f2e41af9ea92a29566885939175" ||
		provenance.SourceTree != "d1aac032059b5622fc9f0b5cd6ce321a77978a1e" ||
		provenance.CatalogManifestSHA256 != "05d832a7d59897d16cd4bd26a7d02d6f6bdf5ec6829c1a280e974579fa29bf6a" {
		t.Fatalf("unexpected provenance: %#v", provenance)
	}
}

func TestBaselineRunnerBypassesCachesUsesConcurrencyThreeAndRequiresMedia(t *testing.T) {
	samples := []BaselineSample{
		{PlatformKey: "a", SampleURL: "https://example.com/a", Enabled: true, ExpectedPlatform: "a", ExpectedType: "video"},
		{PlatformKey: "b", SampleURL: "https://example.com/b", Enabled: true, ExpectedPlatform: "b", ExpectedType: "video"},
		{PlatformKey: "c", SampleURL: "https://example.com/c", Enabled: true, ExpectedPlatform: "c", ExpectedType: "video"},
		{PlatformKey: "d", SampleURL: "https://example.com/d", Enabled: true, ExpectedPlatform: "d", ExpectedType: "video"},
		{PlatformKey: "disabled", SampleURL: "", Enabled: false, ExpectedPlatform: "disabled", ExpectedType: "video"},
	}
	parser := &fakeBaselineParser{delay: 10 * time.Millisecond, emptyMediaFor: map[string]bool{"d": true}}
	report, err := RunBaseline(context.Background(), samples, parser, BaselineRunOptions{Concurrency: 3, RunID: "run-a"})
	if err != nil {
		t.Fatalf("RunBaseline() error = %v", err)
	}
	if report.RunID != "run-a" || report.Total != 4 || report.Completed != 4 || report.Success != 3 || report.Failed != 1 {
		t.Fatalf("report counts = %#v", report)
	}
	if report.Concurrency != 3 || report.MaxObservedConcurrency != 3 {
		t.Fatalf("concurrency = configured %d observed %d, want 3/3", report.Concurrency, report.MaxObservedConcurrency)
	}
	if !report.CacheBypass || !report.NativeEnabled || !report.FallbackEnabled {
		t.Fatalf("runner flags = cacheBypass=%t native=%t fallback=%t", report.CacheBypass, report.NativeEnabled, report.FallbackEnabled)
	}
	for _, sample := range samples {
		if !sample.Enabled {
			continue
		}
		if parser.calls[sample.PlatformKey] != 1 {
			t.Fatalf("sample %s calls = %d, want exactly one parser invocation", sample.PlatformKey, parser.calls[sample.PlatformKey])
		}
	}
	for _, invocation := range parser.invocations {
		if !invocation.BypassPositiveCache || !invocation.BypassNegativeCache || !invocation.BypassHistory || !invocation.ForceRefresh {
			t.Fatalf("parser invocation did not bypass caches/history: %#v", invocation)
		}
		if invocation.ParserInvocationID == "" {
			t.Fatalf("parser invocation omitted parserInvocationId: %#v", invocation)
		}
	}
	if report.Records[3].OK || !errors.Is(report.Records[3].ErrorKind, ErrBaselineMediaMissing) {
		t.Fatalf("empty media record = %#v, want failed ErrBaselineMediaMissing", report.Records[3])
	}
}

func TestBaselineRunIDsAreIndependent(t *testing.T) {
	samples := []BaselineSample{{PlatformKey: "a", SampleURL: "https://example.com/a", Enabled: true, ExpectedPlatform: "a", ExpectedType: "video"}}
	parser := &fakeBaselineParser{}
	entropy := &adminSequenceReader{}
	ids := map[string]bool{}
	for index := 0; index < 3; index++ {
		report, err := RunBaseline(context.Background(), samples, parser, BaselineRunOptions{Entropy: entropy})
		if err != nil {
			t.Fatalf("RunBaseline(%d) error = %v", index, err)
		}
		if report.Completed != 1 {
			t.Fatalf("RunBaseline(%d) completed = %d, want 1", index, report.Completed)
		}
		if ids[report.RunID] {
			t.Fatalf("RunBaseline reused run ID %s", report.RunID)
		}
		ids[report.RunID] = true
	}
}

type fakeBaselineParser struct {
	mu            sync.Mutex
	active        int
	maxActive     int
	calls         map[string]int
	invocations   []BaselineParseOptions
	delay         time.Duration
	emptyMediaFor map[string]bool
}

func (parser *fakeBaselineParser) Parse(ctx context.Context, sample BaselineSample, options BaselineParseOptions) (BaselineParseResult, error) {
	parser.mu.Lock()
	if parser.calls == nil {
		parser.calls = make(map[string]int)
	}
	parser.calls[sample.PlatformKey]++
	parser.invocations = append(parser.invocations, options)
	parser.active++
	if parser.active > parser.maxActive {
		parser.maxActive = parser.active
	}
	parser.mu.Unlock()
	defer func() {
		parser.mu.Lock()
		parser.active--
		parser.mu.Unlock()
	}()
	if parser.delay > 0 {
		time.Sleep(parser.delay)
	}
	if err := ctx.Err(); err != nil {
		return BaselineParseResult{}, err
	}
	result := BaselineParseResult{Platform: sample.ExpectedPlatform, Type: sample.ExpectedType, MediaURLs: []string{"https://cdn.example/" + sample.PlatformKey + ".mp4"}}
	if parser.emptyMediaFor[sample.PlatformKey] {
		result.MediaURLs = nil
	}
	return result, nil
}

type adminSequenceReader struct{ next byte }

func (reader *adminSequenceReader) Read(p []byte) (int, error) {
	for index := range p {
		reader.next++
		p[index] = reader.next
	}
	return len(p), nil
}

func stringsJoin(values []string, separator string) string {
	if len(values) == 0 {
		return ""
	}
	result := values[0]
	for _, value := range values[1:] {
		result += separator + value
	}
	return result
}
