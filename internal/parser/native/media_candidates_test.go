package native

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type candidateRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn candidateRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type candidateClientFactory struct {
	transport http.RoundTripper
	calls     atomic.Int32
}

type nativeCandidateResolver struct{}

func (nativeCandidateResolver) LookupNetIP(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type nativeCandidateDialer struct{ destination string }

func (dialer nativeCandidateDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, dialer.destination)
}

func (factory *candidateClientFactory) client() *http.Client {
	return &http.Client{Transport: candidateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		factory.calls.Add(1)
		return factory.transport.RoundTrip(request)
	})}
}

func (factory *candidateClientFactory) HTTPClient(context.Context, int) *http.Client {
	return factory.client()
}

func (factory *candidateClientFactory) HTTPClientWithRedirect(_ context.Context, _ int, redirect func(*http.Request, []*http.Request) error) *http.Client {
	client := factory.client()
	client.CheckRedirect = redirect
	return client
}

func candidateResponse(request *http.Request, status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestBilibiliKeepsPlayableDurlMirrorsWithoutPromotingUnpairedDASH(t *testing.T) {
	t.Parallel()
	view := `{"code":0,"data":{"bvid":"BV1synthetic","title":"fixture","pic":"https://img.example/cover.jpg","owner":{"mid":7,"name":"author","face":"https://img.example/avatar.jpg"},"pages":[{"cid":11}]}}`
	play := `{"code":0,"data":{"durl":[{"url":"https://progressive.example/video.mp4","backup_url":["https://progressive-backup.example/video.mp4"]}],"dash":{"video":[{"id":80,"baseUrl":"https://dash.example/video-only.m4s","backupUrl":["https://dash-backup.example/video-only.m4s"],"bandwidth":2800000,"width":1920,"height":1080}],"audio":[{"baseUrl":"https://dash.example/audio-only.m4s","bandwidth":192000}]}}}`
	factory := &candidateClientFactory{transport: candidateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/x/web-interface/view":
			return candidateResponse(request, http.StatusOK, view, nil), nil
		case "/x/player/playurl":
			return candidateResponse(request, http.StatusOK, play, nil), nil
		default:
			t.Fatalf("unexpected Bilibili request: %s", request.URL.Path)
			return nil, nil
		}
	})}

	info, err := (biliBili{legacyHTTPClients: legacyHTTPClients{fetcher: factory}}).parseShareUrl("https://www.bilibili.com/video/BV1synthetic")
	if err != nil {
		t.Fatal(err)
	}
	wantURLs := []string{
		"https://progressive.example/video.mp4",
		"https://progressive-backup.example/video.mp4",
	}
	if got := candidateURLs(info.Candidates); !reflect.DeepEqual(got, wantURLs) {
		t.Fatalf("Bilibili candidates = %#v, want %#v", got, wantURLs)
	}
	if info.VideoUrl != wantURLs[0] {
		t.Fatalf("Bilibili compatibility video = %q, want %q", info.VideoUrl, wantURLs[0])
	}
	for _, candidate := range info.Candidates {
		if candidate.Quality != 0 || candidate.Bitrate != 0 || candidate.Width != 0 || candidate.Height != 0 {
			t.Fatalf("Bilibili progressive mirror was mislabeled as a quality tier: %#v", candidate)
		}
	}
	if factory.calls.Load() != 2 {
		t.Fatalf("Bilibili snapshot request count = %d, want 2", factory.calls.Load())
	}
}

func TestBilibiliMultiDurlSegmentsAreNotFallbackAlternatives(t *testing.T) {
	t.Parallel()
	var view biliViewResponse
	view.Data.Title = "fixture"
	var play biliPlayURLResponse
	if err := json.Unmarshal([]byte(`{"code":0,"data":{"durl":[{"url":"https://segments.example/part-1.mp4","backup_url":["https://segments-backup.example/part-1.mp4"]},{"url":"https://segments.example/part-2.mp4"}],"dash":{"video":[{"id":80,"baseUrl":"https://dash.example/video-only.m4s","bandwidth":2800000,"width":1920,"height":1080}]}}}`), &play); err != nil {
		t.Fatal(err)
	}

	info := videoInfoFromBiliSnapshots(view, play)
	if info.VideoUrl != "https://segments.example/part-1.mp4" {
		t.Fatalf("legacy Bilibili projection changed: %q", info.VideoUrl)
	}
	if len(info.Candidates) != 0 {
		t.Fatalf("ordered Bilibili segments were exposed as alternatives: %#v", info.Candidates)
	}
}

func TestWeiboPreservesEveryBitrateAndRejectsUnsafeCandidate(t *testing.T) {
	t.Parallel()
	body := `{"data":{"Component_Play_Playinfo":{"title":"fixture","cover_image":"//img.example/cover.jpg","author":"author","avatar":"//img.example/avatar.jpg","urls":{"source":"//media.example/source.mp4","720p":"//media.example/720.mp4","1080p 3500kbps 1920x1080":"//media.example/1080.mp4","unsafe":"http://127.0.0.1/private.mp4"}}}}`
	factory := &candidateClientFactory{transport: candidateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return candidateResponse(request, http.StatusOK, body, nil), nil
	})}

	info, err := (weiBo{legacyHTTPClients: legacyHTTPClients{fetcher: factory}}).parseVideoID("synthetic")
	if err != nil {
		t.Fatal(err)
	}
	wantURLs := []string{"https://media.example/1080.mp4", "https://media.example/720.mp4", "https://media.example/source.mp4"}
	if got := candidateURLs(info.Candidates); !reflect.DeepEqual(got, wantURLs) {
		t.Fatalf("Weibo candidates = %#v, want %#v", got, wantURLs)
	}
	if info.VideoUrl != wantURLs[0] {
		t.Fatalf("Weibo compatibility video = %q, want %q", info.VideoUrl, wantURLs[0])
	}
	best := info.Candidates[0]
	if best.Quality != 1080 || best.Bitrate != 3_500_000 || best.Width != 1920 || best.Height != 1080 || best.SourceRank != 2 {
		t.Fatalf("Weibo bitrate metadata = %#v", best)
	}
}

func TestWeiboProductionCandidatesFeedGuardedHEADFallback(t *testing.T) {
	producerBody := `{"data":{"Component_Play_Playinfo":{"title":"fixture","urls":{"1080p":"http://media.example/1080","720p":"http://media.example/720"}}}}`
	producerFactory := &candidateClientFactory{transport: candidateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return candidateResponse(request, http.StatusOK, producerBody, nil), nil
	})}
	info, err := (weiBo{legacyHTTPClients: legacyHTTPClients{fetcher: producerFactory}}).parseVideoID("synthetic")
	if err != nil {
		t.Fatal(err)
	}

	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested = append(requested, request.URL.Path)
		if request.Method != http.MethodHead {
			t.Errorf("candidate consumer method = %s, want HEAD", request.Method)
		}
		if request.URL.Path == "/1080" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	validator, err := netguard.NewValidator(netguard.ValidatorOptions{
		Resolver: nativeCandidateResolver{},
		Dialer:   nativeCandidateDialer{destination: server.Listener.Addr().String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := netguard.NewFetcher(netguard.FetcherOptions{Validator: validator, Limits: netguard.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	budget, err := coreparser.NewRequestBudget(coreparser.BudgetOptions{MaxRequests: 2, MaxRedirects: 0, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := coreparser.AttemptMediaCandidatesWithHEAD(t.Context(), info.Candidates, budget, fetcher, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.URL != "http://media.example/720" {
		t.Fatalf("guarded candidate selected = %#v", selected)
	}
	if want := []string{"/1080", "/720"}; !reflect.DeepEqual(requested, want) {
		t.Fatalf("guarded candidate request order = %#v, want %#v", requested, want)
	}
}

func TestKuaishouMirrorCandidatesKeepSourceOrderWithoutInventedQuality(t *testing.T) {
	t.Parallel()
	fixture := `<html><script>window.INIT_STATE = {"synthetic":true,"page":{"result":1,"photo":{"caption":"fixture","mainMvUrls":[{"url":"https://mirror-one.example/video.mp4"},{"url":"http://127.0.0.1/private.mp4"},{"url":"https://mirror-two.example/video.mp4"}],"coverUrls":[{"url":"https://img.example/cover.jpg"}]}}};</script></html>`
	factory := &candidateClientFactory{transport: candidateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/share":
			return candidateResponse(request, http.StatusFound, "", http.Header{"Location": {"https://www.kuaishou.com/short-video/synthetic"}}), nil
		case "/short-video/synthetic":
			return candidateResponse(request, http.StatusFound, "", http.Header{"Location": {"https://www.kuaishou.com/fw/photo/synthetic"}}), nil
		case "/fw/photo/synthetic":
			return candidateResponse(request, http.StatusOK, fixture, nil), nil
		default:
			t.Fatalf("unexpected Kuaishou request: %s", request.URL.Path)
			return nil, nil
		}
	})}

	info, err := (kuaiShou{legacyHTTPClients: legacyHTTPClients{fetcher: factory}}).parseShareUrl("https://v.kuaishou.com/share")
	if err != nil {
		t.Fatal(err)
	}
	wantURLs := []string{"https://mirror-one.example/video.mp4", "https://mirror-two.example/video.mp4"}
	if got := candidateURLs(info.Candidates); !reflect.DeepEqual(got, wantURLs) {
		t.Fatalf("Kuaishou candidates = %#v, want %#v", got, wantURLs)
	}
	if info.VideoUrl != wantURLs[0] {
		t.Fatalf("Kuaishou compatibility video = %q, want %q", info.VideoUrl, wantURLs[0])
	}
	for index, candidate := range info.Candidates {
		if candidate.Quality != 0 || candidate.Bitrate != 0 || candidate.Width != 0 || candidate.Height != 0 || candidate.SourceRank != index {
			t.Fatalf("Kuaishou mirror %d metadata = %#v", index, candidate)
		}
	}
}

func TestMediaCandidatesSurviveLegacyProjectionAndStayInternal(t *testing.T) {
	t.Parallel()
	input := &VideoParseInfo{
		VideoUrl: "https://media.example/compatible-primary.mp4",
		Candidates: []coreparser.MediaCandidate{
			{URL: "https://media.example/video-only-1080.mp4?signature=candidate-sentinel", Kind: coreparser.MediaKindVideo, Quality: 1080, SourceRank: 1},
			{URL: "https://media.example/compatible-primary.mp4", Kind: coreparser.MediaKindVideo, SourceRank: 0},
		},
	}
	result := legacyToResult(coreparser.PlatformKey(SourceWeiBo), input)
	output := resultToLegacy(result)
	if result.VideoURL != input.VideoUrl || output.VideoUrl != input.VideoUrl {
		t.Fatalf("candidate sorting overwrote explicit compatibility primary: result=%q output=%q", result.VideoURL, output.VideoUrl)
	}
	if !reflect.DeepEqual(output.Candidates, input.Candidates) {
		t.Fatalf("candidate projection changed: got %#v want %#v", output.Candidates, input.Candidates)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Candidates") || strings.Contains(string(encoded), "candidates") || strings.Contains(string(encoded), "candidate-sentinel") {
		t.Fatalf("internal candidates leaked into compatibility JSON: %s", encoded)
	}
	encoded, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "candidates") || strings.Contains(string(encoded), "candidate-sentinel") {
		t.Fatalf("internal candidates leaked from core Result JSON: %s", encoded)
	}
}

func TestBilibiliHostMatchingRejectsSuffixSpoofingWithoutNetwork(t *testing.T) {
	t.Parallel()
	factory := &candidateClientFactory{transport: candidateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("spoofed Bilibili host triggered network: %s", request.URL.Host)
		return nil, nil
	})}
	parser := biliBili{legacyHTTPClients: legacyHTTPClients{fetcher: factory}}
	for _, raw := range []string{
		"https://evilb23.tv/video/BV1bad",
		"https://bilibili.com.evil/video/BV1bad",
	} {
		if _, err := parser.getBvidFromURL(raw); err == nil {
			t.Fatalf("spoofed host accepted: %s", raw)
		}
	}
	if got, err := parser.getBvidFromURL("https://WWW.BILIBILI.COM./video/BV1good"); err != nil || got != "BV1good" {
		t.Fatalf("canonical controlled subdomain rejected: bvid=%q error=%v", got, err)
	}
	if factory.calls.Load() != 0 {
		t.Fatalf("spoofed host caused %d network calls", factory.calls.Load())
	}
}

func TestDuplicateCandidateKeepsEarliestRankAndFillsMissingMetadata(t *testing.T) {
	t.Parallel()
	candidates := appendUsableMediaCandidate(nil, "https://media.example/video.mp4", coreparser.MediaKindVideo, candidateMetadata{})
	candidates = appendUsableMediaCandidate(candidates, "https://MEDIA.EXAMPLE./video.mp4", coreparser.MediaKindVideo, candidateMetadata{
		Quality: 1080, Bitrate: 3_000_000, Width: 1920, Height: 1080,
	})
	if len(candidates) != 1 {
		t.Fatalf("duplicate candidate count = %d", len(candidates))
	}
	got := candidates[0]
	if got.SourceRank != 0 || got.Quality != 1080 || got.Bitrate != 3_000_000 || got.Width != 1920 || got.Height != 1080 {
		t.Fatalf("merged duplicate candidate = %#v", got)
	}
}

func candidateURLs(candidates []coreparser.MediaCandidate) []string {
	urls := make([]string, len(candidates))
	for index := range candidates {
		urls[index] = candidates[index].URL
	}
	return urls
}
