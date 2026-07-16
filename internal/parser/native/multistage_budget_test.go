package native

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type snapshotRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn snapshotRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type snapshotClientFactory struct {
	mu        sync.Mutex
	requests  []string
	deadlines []time.Time
	handle    func(*http.Request) (*http.Response, error)
}

func (factory *snapshotClientFactory) HTTPClient(ctx context.Context, _ int) *http.Client {
	return factory.client(ctx, nil)
}

func (factory *snapshotClientFactory) HTTPClientWithRedirect(
	ctx context.Context,
	_ int,
	redirect func(*http.Request, []*http.Request) error,
) *http.Client {
	return factory.client(ctx, redirect)
}

func (factory *snapshotClientFactory) client(
	ctx context.Context,
	redirect func(*http.Request, []*http.Request) error,
) *http.Client {
	factory.mu.Lock()
	deadline, _ := ctx.Deadline()
	factory.deadlines = append(factory.deadlines, deadline)
	factory.mu.Unlock()
	return &http.Client{
		CheckRedirect: redirect,
		Transport: snapshotRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			request = request.Clone(ctx)
			if err := request.Context().Err(); err != nil {
				return nil, err
			}
			factory.mu.Lock()
			factory.requests = append(factory.requests, request.URL.Hostname()+request.URL.Path)
			factory.mu.Unlock()
			return factory.handle(request)
		}),
	}
}

func (factory *snapshotClientFactory) snapshot() ([]string, []time.Time) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return append([]string(nil), factory.requests...), append([]time.Time(nil), factory.deadlines...)
}

func snapshotResponse(request *http.Request, status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestBilibiliShortLinkSnapshotUsesOneRequestAndRedirectBudget(t *testing.T) {
	view := `{"code":0,"data":{"bvid":"BV1synthetic","title":"fixture","pages":[{"cid":11}]}}`
	play := `{"code":0,"data":{"durl":[{"url":"https://cdn.example/video.mp4"}]}}`
	factory := &snapshotClientFactory{}
	factory.handle = func(request *http.Request) (*http.Response, error) {
		switch request.URL.Hostname() + request.URL.Path {
		case "b23.tv/synthetic":
			return snapshotResponse(request, http.StatusFound, "", http.Header{
				"Location": {"https://www.bilibili.com/video/BV1synthetic"},
			}), nil
		case "api.bilibili.com/x/web-interface/view":
			return snapshotResponse(request, http.StatusOK, view, nil), nil
		case "api.bilibili.com/x/player/playurl":
			return snapshotResponse(request, http.StatusOK, play, nil), nil
		default:
			return nil, errors.New("unexpected synthetic Bilibili request")
		}
	}
	service, err := NewService(coreparser.Dependencies{Fetcher: factory})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ParseVideoShareURL(t.Context(), "https://b23.tv/synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if result.VideoUrl != "https://cdn.example/video.mp4" {
		t.Fatalf("Bilibili video URL = %q", result.VideoUrl)
	}
	requests, deadlines := factory.snapshot()
	want := []string{
		"b23.tv/synthetic",
		"api.bilibili.com/x/web-interface/view",
		"api.bilibili.com/x/player/playurl",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("Bilibili request sequence = %#v, want %#v", requests, want)
	}
	if len(deadlines) != 3 || deadlines[0].IsZero() || !deadlines[0].Equal(deadlines[1]) || !deadlines[1].Equal(deadlines[2]) {
		t.Fatalf("Bilibili clients did not share one Parse deadline: %#v", deadlines)
	}
}

func TestBilibiliStoppedRedirectCannotEscapeLimitsOrDuplicateGate(t *testing.T) {
	tests := []struct {
		name         string
		location     string
		maxRequests  int
		maxRedirects int
	}{
		{name: "redirect-limit", location: "https://www.bilibili.com/video/BV1synthetic", maxRequests: 4, maxRedirects: 0},
		{name: "duplicate-short-link", location: "https://b23.tv/synthetic", maxRequests: 4, maxRedirects: 3},
		{name: "request-limit", location: "https://www.bilibili.com/video/BV1synthetic", maxRequests: 1, maxRedirects: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory := &snapshotClientFactory{}
			factory.handle = func(request *http.Request) (*http.Response, error) {
				return snapshotResponse(request, http.StatusFound, "", http.Header{"Location": {test.location}}), nil
			}
			descriptor := descriptorsFromRegistrations([]nativeRegistration{{
				key: "bilibilibudget", displayName: "Bilibili budget fixture",
				hostRules: []coreparser.HostRule{{Host: "b23.tv"}}, capabilities: coreparser.CapabilityVideo,
				maxRequests: test.maxRequests, maxRedirects: test.maxRedirects,
				bind: func(clients legacyHTTPClients) legacyParserBinding {
					return bindShare(biliBili{legacyHTTPClients: clients})
				},
			}})[0]
			adapter, err := descriptor.New(coreparser.Dependencies{Fetcher: factory})
			if err != nil {
				t.Fatal(err)
			}
			target, err := netguard.NewFetchURL("https://b23.tv/synthetic")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Parse(t.Context(), coreparser.Request{URL: target, Platform: descriptor.Key}); err == nil {
				t.Fatal("Bilibili budget violation unexpectedly succeeded")
			}
			requests, _ := factory.snapshot()
			if len(requests) != 1 {
				t.Fatalf("Bilibili performed %d physical requests after budget/duplicate rejection", len(requests))
			}
		})
	}
}

func TestCCTVPageAndAPISnapshotSharesBudgetAndCancellation(t *testing.T) {
	t.Run("two-stage success", func(t *testing.T) {
		factory := newCCTVSnapshotFactory(nil)
		service, err := NewService(coreparser.Dependencies{Fetcher: factory})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.ParseVideoShareURL(t.Context(), "https://tv.cctv.com/2026/synthetic.shtml")
		if err != nil {
			t.Fatal(err)
		}
		if result.VideoUrl != "https://cdn.example/cctv.m3u8" {
			t.Fatalf("CCTV video URL = %q", result.VideoUrl)
		}
		requests, deadlines := factory.snapshot()
		want := []string{"tv.cctv.com/2026/synthetic.shtml", "vdn.apps.cntv.cn/api/getHttpVideoInfo.do"}
		if !reflect.DeepEqual(requests, want) {
			t.Fatalf("CCTV request sequence = %#v, want %#v", requests, want)
		}
		if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[0].Equal(deadlines[1]) {
			t.Fatalf("CCTV stages did not share one deadline: %#v", deadlines)
		}
	})

	t.Run("request limit stops API stage", func(t *testing.T) {
		factory := newCCTVSnapshotFactory(nil)
		descriptor := descriptorsFromRegistrations([]nativeRegistration{{
			key: "cctvbudget", displayName: "CCTV budget fixture",
			hostRules: []coreparser.HostRule{{Host: "tv.cctv.com"}}, capabilities: coreparser.CapabilityVideo,
			maxRequests: 1, maxRedirects: 1,
			bind: func(clients legacyHTTPClients) legacyParserBinding {
				return bindShare(cctvVideo{legacyHTTPClients: clients})
			},
		}})[0]
		adapter, err := descriptor.New(coreparser.Dependencies{Fetcher: factory})
		if err != nil {
			t.Fatal(err)
		}
		target, err := netguard.NewFetchURL("https://tv.cctv.com/2026/synthetic.shtml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Parse(t.Context(), coreparser.Request{URL: target, Platform: descriptor.Key}); err == nil {
			t.Fatal("CCTV API stage escaped the physical request limit")
		}
		requests, _ := factory.snapshot()
		if want := []string{"tv.cctv.com/2026/synthetic.shtml"}; !reflect.DeepEqual(requests, want) {
			t.Fatalf("CCTV requests after exhaustion = %#v, want %#v", requests, want)
		}
	})

	t.Run("cancellation stops API stage", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		factory := newCCTVSnapshotFactory(cancel)
		service, err := NewService(coreparser.Dependencies{Fetcher: factory})
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.ParseVideoShareURL(ctx, "https://tv.cctv.com/2026/synthetic.shtml")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CCTV cancellation error = %v", err)
		}
		requests, _ := factory.snapshot()
		if want := []string{"tv.cctv.com/2026/synthetic.shtml"}; !reflect.DeepEqual(requests, want) {
			t.Fatalf("CCTV cancellation allowed another physical request: %#v", requests)
		}
	})
}

func TestDouyinRedirectToXiguaKeepsGuardedClientsBudgetAndDeadline(t *testing.T) {
	const videoID = "7144194760184594977"
	xiguaSnapshot := `<script>window._ROUTER_DATA = {"loaderData":{"video_(id)/page":{"videoInfoRes":{"item_list":[{"author":{"user_id":"author-id","nickname":"author","avatar_thumb":{"url_list":["https://cdn.example/avatar.jpg"]}},"desc":"fixture","video":{"play_addr":{"url_list":["https://cdn.example/xigua.mp4"]},"cover":{"url_list":["https://cdn.example/cover.jpg"]}}}]}}}}</script>`
	factory := &snapshotClientFactory{}
	factory.handle = func(request *http.Request) (*http.Response, error) {
		switch request.URL.Hostname() + request.URL.Path {
		case "v.douyin.com/synthetic":
			return snapshotResponse(request, http.StatusFound, "", http.Header{
				"Location": {"https://www.ixigua.com/douyin/share/video/" + videoID},
			}), nil
		case "m.ixigua.com/douyin/share/video/" + videoID:
			return snapshotResponse(request, http.StatusOK, xiguaSnapshot, nil), nil
		default:
			return nil, errors.New("unexpected synthetic Douyin/Xigua request")
		}
	}
	service, err := NewService(coreparser.Dependencies{Fetcher: factory})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ParseVideoShareURL(t.Context(), "https://v.douyin.com/synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if result.VideoUrl != "https://cdn.example/xigua.mp4" {
		t.Fatalf("Xigua compatibility video URL = %q", result.VideoUrl)
	}
	requests, deadlines := factory.snapshot()
	want := []string{
		"v.douyin.com/synthetic",
		"m.ixigua.com/douyin/share/video/" + videoID,
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("Douyin/Xigua request sequence = %#v, want %#v", requests, want)
	}
	if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("Douyin/Xigua stages did not share one Parse deadline: %#v", deadlines)
	}
}

func TestDouyinRedirectHostCannotSmuggleXiguaAdapterSelection(t *testing.T) {
	factory := &snapshotClientFactory{}
	factory.handle = func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname()+request.URL.Path == "v.douyin.com/synthetic" {
			return snapshotResponse(request, http.StatusFound, "", http.Header{
				"Location": {"https://ixigua.com.evil/douyin/share/video/7144194760184594977"},
			}), nil
		}
		return nil, errors.New("stop after adapter selection observation")
	}
	service, err := NewService(coreparser.Dependencies{Fetcher: factory})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.ParseVideoShareURL(t.Context(), "https://v.douyin.com/synthetic")
	requests, _ := factory.snapshot()
	for _, request := range requests {
		if strings.HasPrefix(request, "m.ixigua.com/") {
			t.Fatalf("malicious suffix selected Xigua adapter: %#v", requests)
		}
	}
	if want := []string{"v.douyin.com/synthetic", "www.iesdouyin.com/share/video/7144194760184594977"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("malicious suffix request sequence = %#v, want %#v", requests, want)
	}
}

func newCCTVSnapshotFactory(cancel context.CancelFunc) *snapshotClientFactory {
	factory := &snapshotClientFactory{}
	factory.handle = func(request *http.Request) (*http.Response, error) {
		switch request.URL.Hostname() + request.URL.Path {
		case "tv.cctv.com/2026/synthetic.shtml":
			if cancel != nil {
				cancel()
			}
			return snapshotResponse(request, http.StatusOK, `<script>var guid = "synthetic-guid";</script>`, nil), nil
		case "vdn.apps.cntv.cn/api/getHttpVideoInfo.do":
			return snapshotResponse(request, http.StatusOK, `{"status":"001","title":"fixture","hls_url":"https://cdn.example/cctv.m3u8"}`, nil), nil
		default:
			return nil, errors.New("unexpected synthetic CCTV request")
		}
	}
	return factory
}
