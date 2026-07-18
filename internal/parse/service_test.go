package parse

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeVideoProvidesAllFrontendAliases(t *testing.T) {
	got := Normalize(Result{
		Platform: "douyin",
		Type:     "video",
		Title:    "video title",
		VideoURL: "https://cdn.example/v.mp4",
		AudioURL: "https://cdn.example/a.mp3",
	})
	if got.Music != got.MP3 || got.Music != got.AudioURL || got.Audio != got.AudioURL {
		t.Fatalf("audio aliases diverged: music=%q mp3=%q audio=%q audioUrl=%q", got.Music, got.MP3, got.Audio, got.AudioURL)
	}
	if len(got.Downloads) == 0 || got.Downloads[0].URL != "https://cdn.example/v.mp4" {
		t.Fatalf("downloads = %#v, want video download alias", got.Downloads)
	}
	if got.PlayAddr != "https://cdn.example/v.mp4" || got.PreviewURL != "https://cdn.example/v.mp4" {
		t.Fatalf("video aliases missing: play=%q preview=%q", got.PlayAddr, got.PreviewURL)
	}
}

func TestNormalizeLivePhotoKeepsLegacyImagesShape(t *testing.T) {
	got := Normalize(Result{
		Platform: "redbook",
		Type:     "gallery",
		Images: []ImageAsset{{
			URL:          "https://cdn.example/a.jpg",
			LivePhotoURL: "https://cdn.example/a.mp4",
		}},
	})
	if len(got.Images) != 1 || got.Images[0] != "https://cdn.example/a.jpg" {
		t.Fatalf("legacy images = %#v, want static image strings", got.Images)
	}
	if len(got.ImageAssets) != 1 || got.ImageAssets[0].LivePhotoURL != "https://cdn.example/a.mp4" {
		t.Fatalf("image assets = %#v, want stable Live Photo pairing", got.ImageAssets)
	}
}

func TestForceRefreshBypassesPositiveAndNegativeCache(t *testing.T) {
	parser := &countingParser{result: Result{Platform: "example", Type: "video", VideoURL: "https://cdn.example/v.mp4"}}
	cache := newFakeCache()
	cache.positive["https://example.com/v"] = Result{Title: "stale", Type: "video", VideoURL: "https://cdn.example/stale.mp4"}
	cache.negative["https://example.com/v"] = NewError(ErrorUpstreamBlocked, StageUpstream, "example", true)
	store := &fakeStore{}
	service := NewService(Dependencies{
		Parser:  parser,
		Cache:   cache,
		Store:   store,
		Entropy: &sequenceReader{},
		Resolver: staticResolver{descriptor: Descriptor{
			Platform:  "example",
			HostRules: []HostRule{{Host: "example.com"}},
		}},
	})

	got, err := service.Parse(context.Background(), Request{URL: "https://example.com/v?ticket=opaque", ForceRefresh: true})
	if err != nil {
		t.Fatalf("Parse(force refresh) error = %v", err)
	}
	if parser.calls.Load() != 1 {
		t.Fatalf("parser calls = %d, want 1", parser.calls.Load())
	}
	if got.Result.VideoURL != "https://cdn.example/v.mp4" {
		t.Fatalf("parse result = %#v, want fresh parser result", got.Result)
	}
	if got.Data.ShareID == "" || len(store.saved) != 1 {
		t.Fatalf("share result not persisted: shareID=%q saves=%d", got.Data.ShareID, len(store.saved))
	}
	if strings.Contains(got.Cache.CanonicalURL, "ticket=opaque") {
		t.Fatalf("capability query entered canonical cache identity: %#v", got.Cache)
	}
}

func TestCacheKeyUsesCanonicalURLAndParserSchemaVersions(t *testing.T) {
	left, err := NewCacheIdentity(CacheIdentityParts{
		Platform:             "douyin",
		CanonicalResourceURL: "https://example.com/v?vid=42",
		ParserVersion:        "parser-v1",
		ResultSchemaVersion:  "result-v1",
	})
	if err != nil {
		t.Fatalf("NewCacheIdentity(left) error = %v", err)
	}
	right, err := NewCacheIdentity(CacheIdentityParts{
		Platform:             "douyin",
		CanonicalResourceURL: "https://example.com/v?vid=42",
		ParserVersion:        "parser-v2",
		ResultSchemaVersion:  "result-v1",
	})
	if err != nil {
		t.Fatalf("NewCacheIdentity(right) error = %v", err)
	}
	if left.Key == right.Key {
		t.Fatalf("cache key did not bind parser version: %q", left.Key)
	}
	if strings.Contains(left.Key, "vid=42") || strings.Contains(left.Key, "example.com") {
		t.Fatalf("cache key exposed canonical resource material: %q", left.Key)
	}
}

func TestNegativeCacheRejectsNonCacheableErrors(t *testing.T) {
	for _, class := range []ErrorClass{
		ErrorCredentialRequired,
		ErrorSecurityRejected,
		ErrorSchemaChanged,
		ErrorInternal,
		ErrorCanceled,
	} {
		if NegativeCacheable(class) {
			t.Fatalf("class %s must not be ordinary-negative-cacheable", class)
		}
	}
	if !NegativeCacheable(ErrorUnsupported) {
		t.Fatal("unsupported input should remain stable-negative-cacheable")
	}
}

func TestShareIDEntropyFailureDoesNotPersistResult(t *testing.T) {
	parser := &countingParser{result: Result{Platform: "example", Type: "video", VideoURL: "https://cdn.example/v.mp4"}}
	store := &fakeStore{}
	service := NewService(Dependencies{
		Parser:  parser,
		Cache:   newFakeCache(),
		Store:   store,
		Entropy: failingReader{},
		Resolver: staticResolver{descriptor: Descriptor{
			Platform:  "example",
			HostRules: []HostRule{{Host: "example.com"}},
		}},
	})
	_, err := service.Parse(context.Background(), Request{URL: "https://example.com/v"})
	if !errors.Is(err, ErrEntropyUnavailable) {
		t.Fatalf("Parse entropy error = %v, want ErrEntropyUnavailable", err)
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved results after entropy failure = %d, want 0", len(store.saved))
	}
}

func TestParserChainUsesBoundedFallback(t *testing.T) {
	first := &countingParser{err: NewError(ErrorSchemaChanged, StageParser, "example", true)}
	second := &countingParser{result: Result{Platform: "example", Type: "video", VideoURL: "https://cdn.example/fallback.mp4"}}
	third := &countingParser{result: Result{Platform: "example", Type: "video", VideoURL: "https://cdn.example/unused.mp4"}}
	chain := ParserChain{Parsers: []Parser{first, second, third}, MaxAttempts: 2}
	got, err := chain.Parse(context.Background(), ParserRequest{Descriptor: Descriptor{Platform: "example"}})
	if err != nil {
		t.Fatalf("ParserChain.Parse() error = %v", err)
	}
	if got.VideoURL != "https://cdn.example/fallback.mp4" {
		t.Fatalf("fallback result = %#v", got)
	}
	if first.calls.Load() != 1 || second.calls.Load() != 1 || third.calls.Load() != 0 {
		t.Fatalf("fallback calls = first %d second %d third %d", first.calls.Load(), second.calls.Load(), third.calls.Load())
	}
}

func TestValidateMediaRejectsEmptyAndUnsafeResults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input Result
		class ErrorClass
	}{
		{name: "empty", input: Result{Platform: "example"}, class: ErrorEmptyMedia},
		{name: "loopback", input: Result{Platform: "example", VideoURL: "http://127.0.0.1/video.mp4"}, class: ErrorSecurityRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMedia(tc.input)
			if ClassOf(err) != tc.class {
				t.Fatalf("ValidateMedia() class = %s error=%v, want %s", ClassOf(err), err, tc.class)
			}
		})
	}
}

type countingParser struct {
	calls  atomic.Int32
	result Result
	err    error
}

func (parser *countingParser) Parse(context.Context, ParserRequest) (Result, error) {
	parser.calls.Add(1)
	if parser.err != nil {
		return Result{}, parser.err
	}
	return parser.result, nil
}

type staticResolver struct {
	descriptor Descriptor
	err        error
}

func (resolver staticResolver) ResolveURL(string) (Descriptor, error) {
	if resolver.err != nil {
		return Descriptor{}, resolver.err
	}
	return resolver.descriptor, nil
}

type fakeCache struct {
	positive map[string]Result
	negative map[string]error
}

func newFakeCache() *fakeCache {
	return &fakeCache{
		positive: make(map[string]Result),
		negative: make(map[string]error),
	}
}

func (cache *fakeCache) GetPositive(_ context.Context, identity CacheIdentity) (Result, bool, error) {
	value, ok := cache.positive[identity.CanonicalURL]
	return value, ok, nil
}

func (cache *fakeCache) SetPositive(_ context.Context, identity CacheIdentity, result Result, _ time.Duration) error {
	cache.positive[identity.CanonicalURL] = result
	return nil
}

func (cache *fakeCache) GetNegative(_ context.Context, identity CacheIdentity) (error, bool, error) {
	value, ok := cache.negative[identity.CanonicalURL]
	return value, ok, nil
}

func (cache *fakeCache) SetNegative(_ context.Context, identity CacheIdentity, err error, _ time.Duration) error {
	cache.negative[identity.CanonicalURL] = err
	return nil
}

type fakeStore struct {
	saved []StoredResult
}

func (store *fakeStore) SaveResult(_ context.Context, result StoredResult) error {
	store.saved = append(store.saved, result)
	return nil
}

type sequenceReader struct{ next byte }

func (reader *sequenceReader) Read(p []byte) (int, error) {
	for index := range p {
		reader.next++
		p[index] = reader.next
	}
	return len(p), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
