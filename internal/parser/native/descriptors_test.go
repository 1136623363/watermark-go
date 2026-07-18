package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type nativeRoundTripper struct {
	calls atomic.Int32
	body  string
	err   error
}

type countingClientFactory struct{ calls atomic.Int32 }

type multiRequestBudgetParser struct {
	clients legacyHTTPClients
	urls    []string
}

func (parser multiRequestBudgetParser) parseShareUrl(string) (*VideoParseInfo, error) {
	for _, raw := range parser.urls {
		response, err := parser.clients.newHTTPClient().Get(raw)
		if err != nil {
			return nil, err
		}
		_ = response.Body.Close()
	}
	return &VideoParseInfo{VideoUrl: "https://cdn.example/video.mp4"}, nil
}

type deadlineRecordingFactory struct {
	mu        sync.Mutex
	deadlines []time.Time
	transport http.RoundTripper
}

func (factory *deadlineRecordingFactory) client(ctx context.Context, redirect func(*http.Request, []*http.Request) error) *http.Client {
	factory.mu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		factory.deadlines = append(factory.deadlines, deadline)
	} else {
		factory.deadlines = append(factory.deadlines, time.Time{})
	}
	factory.mu.Unlock()
	return &http.Client{Transport: factory.transport, CheckRedirect: redirect}
}

func (factory *deadlineRecordingFactory) HTTPClient(ctx context.Context, _ int) *http.Client {
	return factory.client(ctx, nil)
}

func (factory *deadlineRecordingFactory) HTTPClientWithRedirect(ctx context.Context, _ int, redirect func(*http.Request, []*http.Request) error) *http.Client {
	return factory.client(ctx, redirect)
}

func (factory *countingClientFactory) HTTPClient(context.Context, int) *http.Client {
	factory.calls.Add(1)
	return nil
}

func (factory *countingClientFactory) HTTPClientWithRedirect(context.Context, int, func(*http.Request, []*http.Request) error) *http.Client {
	factory.calls.Add(1)
	return nil
}

func (transport *nativeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	if transport.err != nil {
		return nil, transport.err
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(transport.body)), Request: request,
	}, nil
}

func TestRegistryContainsLegacyNativePlatforms(t *testing.T) {
	t.Parallel()
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		t.Fatal(err)
	}
	catalog := registry.CatalogSnapshot()
	if len(catalog.Platforms) != 26 || catalog.HostRuleCount() != 41 || catalog.SupportsIDCount() != 21 {
		t.Fatalf("catalog counts = platforms:%d hosts:%d id:%d", len(catalog.Platforms), catalog.HostRuleCount(), catalog.SupportsIDCount())
	}
	path := filepath.Join("testdata", "catalog.golden.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var golden coreparser.Catalog
	if err := json.Unmarshal(encoded, &golden); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog, golden) {
		t.Fatal("native descriptor catalog drifted from its exact golden")
	}
	for _, unapproved := range []string{"douyin.wtf", "api.amemv.com", "media-parser"} {
		if strings.Contains(strings.ToLower(string(encoded)), unapproved) {
			t.Fatalf("catalog silently adopted an unapproved research alias %q", unapproved)
		}
	}
}

func TestNativeHostRulesPreserveFixedCommitControlledSubdomains(t *testing.T) {
	t.Parallel()
	// The fixed baseline deliberately matched every one of its 41 domains at
	// a DNS label boundary. Keep that compatibility until a later catalog task
	// can replace a broad rule with a complete, fixture-backed exact alias set.
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range Descriptors() {
		for _, rule := range descriptor.HostRules {
			if !rule.IncludeSubdomains {
				t.Errorf("fixed-commit controlled-subdomain rule %s became exact", rule.Host)
			}
			resolved, resolveErr := registry.ResolveURL("https://" + rule.Host + "/synthetic")
			if resolveErr != nil || resolved.Key != descriptor.Key {
				t.Errorf("exact host %s resolved to %q: %v", rule.Host, resolved.Key, resolveErr)
			}
			child := "synthetic." + rule.Host
			resolved, resolveErr = registry.ResolveURL("https://" + child + "/synthetic")
			if resolveErr != nil || resolved.Key != descriptor.Key {
				t.Errorf("controlled child %s resolved to %q: %v", child, resolved.Key, resolveErr)
			}
			hostileSuffix := rule.Host + ".attacker.invalid"
			if resolved, resolveErr := registry.ResolveURL("https://" + hostileSuffix + "/synthetic"); resolveErr == nil {
				t.Errorf("hostile suffix %s resolved to %q", hostileSuffix, resolved.Key)
			}
		}
	}
}

func TestEveryNativeFixedEndpointHasPolicyOwner(t *testing.T) {
	t.Parallel()
	descriptors := Descriptors()
	owners := make([]netguard.AuthorityOwner, 0, len(descriptors))
	for _, descriptor := range descriptors {
		rules := make([]netguard.AuthorityRule, 0, len(descriptor.HostRules)+len(descriptor.AuthorityRules))
		for _, rule := range descriptor.HostRules {
			rules = append(rules, netguard.AuthorityRule{
				Purpose: netguard.PurposeInputShare, Host: rule.Host, IncludeSubdomains: rule.IncludeSubdomains,
			})
		}
		rules = append(rules, descriptor.AuthorityRules...)
		owners = append(owners, netguard.AuthorityOwner{Owner: string(descriptor.Key), Rules: rules})
	}
	registry, err := netguard.NewAuthorityRegistry(owners)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		owner   string
		host    string
		purpose netguard.AuthorityPurpose
	}{
		{owner: SourceBiliBili, host: "api.bilibili.com", purpose: netguard.PurposeMetadataAPI},
		{owner: SourceSohu, host: SohuSessionHost, purpose: netguard.PurposeSessionConsumer},
		{owner: SourceTwitter, host: "cdn.syndication.twimg.com", purpose: netguard.PurposeMetadataAPI},
		{owner: SourceQQVideo, host: "vv.video.qq.com", purpose: netguard.PurposeMetadataAPI},
		{owner: SourceCCTV, host: "vdn.apps.cntv.cn", purpose: netguard.PurposeMetadataAPI},
		{owner: SourcePiPiXia, host: "api.pipix.com", purpose: netguard.PurposeMetadataAPI},
		{owner: SourceHuYa, host: "liveapi.huya.com", purpose: netguard.PurposeMetadataAPI},
		{owner: SourcePiPiGaoXiao, host: "share.ippzone.com", purpose: netguard.PurposeMetadataAPI},
	} {
		t.Run(test.owner+"/"+test.host, func(t *testing.T) {
			target, err := netguard.NewFetchURL("https://" + test.host + "/synthetic")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Authorize(netguard.AuthorityRequest{
				Owner: test.owner, Purpose: test.purpose, URL: target,
			}); err != nil {
				t.Fatalf("fixed endpoint owner missing: %v", err)
			}
			if _, err := registry.Authorize(netguard.AuthorityRequest{
				Owner: SourceDouYin, Purpose: test.purpose, URL: target,
			}); !errors.Is(err, netguard.ErrAuthorityDenied) {
				t.Fatalf("fixed endpoint accepted wrong owner: %v", err)
			}
		})
	}
}

func TestParserAPIAuthorityCannotBeUsedAsInputRoute(t *testing.T) {
	t.Parallel()
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://api.bilibili.com/x/web-interface/view?bvid=BV1synthetic",
		"https://api.tv.sohu.com/v4/video/info/1.json?api_key=query-sentinel",
	} {
		if descriptor, err := registry.ResolveURL(raw); err == nil {
			t.Fatalf("fixed API authority %q resolved as input share for %q", raw, descriptor.Key)
		}
	}
}

func TestEveryNativeParserConstructionPerformsNoIO(t *testing.T) {
	t.Parallel()
	factory := &countingClientFactory{}
	for _, descriptor := range Descriptors() {
		constructed, err := descriptor.New(coreparser.Dependencies{Fetcher: factory})
		if err != nil || constructed == nil {
			t.Fatalf("construct %s: parser=%T error=%v", descriptor.Key, constructed, err)
		}
	}
	if factory.calls.Load() != 0 {
		t.Fatalf("native constructors performed %d HTTP operations", factory.calls.Load())
	}
}

func TestEveryNativeParserBindingPerformsNoIO(t *testing.T) {
	t.Parallel()
	factory := &countingClientFactory{}
	clients := legacyHTTPClients{fetcher: factory}
	for _, registration := range nativeRegistrations {
		binding := registration.bind(clients)
		if binding.share == nil {
			t.Fatalf("bind %s: URL parser is nil", registration.key)
		}
		if got := binding.id != nil; got != registration.supportsID() {
			t.Fatalf("bind %s: id parser=%t supportsId=%t", registration.key, got, registration.supportsID())
		}
	}
	if factory.calls.Load() != 0 {
		t.Fatalf("native parser bindings performed %d HTTP operations", factory.calls.Load())
	}
}

func TestDescriptorMetadataDoesNotAliasRegistrationInput(t *testing.T) {
	t.Parallel()
	input := []nativeRegistration{{
		key: SourceDouYin, displayName: "fixture",
		aliases:   []coreparser.PlatformKey{"fixture-alias"},
		hostRules: []coreparser.HostRule{{Host: "fixture.example", IncludeSubdomains: true}},
		queryKeys: []string{"fixture_id"}, capabilities: coreparser.CapabilityVideo,
		maxRequests: 2, maxRedirects: 1,
		bind: func(legacyHTTPClients) legacyParserBinding {
			return legacyParserBinding{share: douYin{}, id: douYin{}}
		},
	}}

	descriptors := descriptorsFromRegistrations(input)
	descriptors[0].Aliases[0] = "mutated-alias"
	descriptors[0].HostRules[0].Host = "mutated.example"
	descriptors[0].QueryKeys[0] = "mutated_query"

	if input[0].aliases[0] != "fixture-alias" || input[0].hostRules[0].Host != "fixture.example" || input[0].queryKeys[0] != "fixture_id" {
		t.Fatal("descriptor construction mutated or aliased registration metadata")
	}
}

func TestDescriptorsDoNotReadLegacyCompatibilityMap(t *testing.T) {
	original := videoSourceInfoMapping[SourceDouYin]
	videoSourceInfoMapping[SourceDouYin] = videoSourceInfo{
		VideoShareUrlDomain: []string{"attacker.invalid"},
		VideoShareUrlParser: zuiYou{},
	}
	t.Cleanup(func() { videoSourceInfoMapping[SourceDouYin] = original })

	for _, descriptor := range Descriptors() {
		if descriptor.Key != coreparser.PlatformKey(SourceDouYin) {
			continue
		}
		if len(descriptor.HostRules) != 3 || descriptor.HostRules[0].Host != "v.douyin.com" || !descriptor.SupportsID {
			t.Fatalf("descriptor metadata was read from compatibility map: %#v", descriptor)
		}
		return
	}
	t.Fatal("douyin descriptor is absent")
}

func TestSourceInfoPreservesHostRuleSemantics(t *testing.T) {
	t.Parallel()
	source, ok := CatalogSource(SourceDouYin)
	if !ok {
		t.Fatal("douyin catalog source is absent")
	}
	if len(source.HostRules) != 3 || source.HostRules[0] != (coreparser.HostRule{Host: "v.douyin.com", IncludeSubdomains: true}) {
		t.Fatalf("source host rules were flattened: %#v", source.HostRules)
	}
	source.HostRules[0].Host = "mutated.example"
	again, ok := CatalogSource(SourceDouYin)
	if !ok || again.HostRules[0].Host != "v.douyin.com" {
		t.Fatal("source info exposed mutable registry metadata")
	}
}

func TestSohuRequiresInjectedCredentialBeforeNetwork(t *testing.T) {
	t.Parallel()
	factory := &countingClientFactory{}
	service, err := NewService(coreparser.Dependencies{Fetcher: factory})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ParseVideoID(t.Context(), SourceSohu, "synthetic-id")
	var typed *coreparser.ParseError
	if !errors.As(err, &typed) || typed.Code != coreparser.ErrorCredentialRequired {
		t.Fatalf("missing Sohu credential error = %#v", err)
	}
	if factory.calls.Load() != 0 {
		t.Fatalf("Sohu attempted %d requests without a credential", factory.calls.Load())
	}
}

func TestNativeParserErrorsNeverExposeAllowedQueryMaterial(t *testing.T) {
	t.Parallel()
	sentinel := "query-material-sentinel"
	transport := &nativeRoundTripper{err: errors.New("synthetic transport failure")}
	factory := &deadlineRecordingFactory{transport: transport}
	service, err := NewService(coreparser.Dependencies{Fetcher: factory})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ParseVideoShareURL(t.Context(), "https://www.xiaohongshu.com/explore/synthetic?xsec_token="+sentinel)
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("native parser returned an unsafe error: %v", err)
	}
}

func TestParserFetchesUpstreamOnceForRichMediaResult(t *testing.T) {
	t.Parallel()
	fixture := `<html><script>window._ROUTER_DATA = {"loaderData":{"video_(id)/page":{"videoInfoRes":{"item_list":[{"desc":"synthetic","images":[{"url_list":["https://cdn.example/image.jpg"],"video":{"play_addr":{"url_list":["https://cdn.example/live.mp4"]}}}],"video":{"play_addr":{"uri":"https://cdn.example/audio.m4a"},"cover":{"url_list":["https://cdn.example/cover.jpg"]}},"author":{"nickname":"fixture"}}]}}}}</script></html>`
	transport := &nativeRoundTripper{body: fixture}
	factory := &deadlineRecordingFactory{transport: transport}
	service, err := NewService(coreparser.Dependencies{Fetcher: factory})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ParseVideoID(t.Context(), SourceDouYin, "synthetic-id")
	if err != nil {
		t.Fatal(err)
	}
	if transport.calls.Load() != 1 {
		t.Fatalf("upstream fetch count = %d", transport.calls.Load())
	}
	if result.MusicUrl == "" || len(result.Images) != 1 || result.Images[0].LivePhotoUrl == "" {
		t.Fatalf("rich media result lost fields: %#v", result)
	}
}

func TestDescriptorCapabilitiesMatchResult(t *testing.T) {
	t.Parallel()
	var descriptor coreparser.Descriptor
	for _, candidate := range Descriptors() {
		if candidate.Key == coreparser.PlatformKey(SourceDouYin) {
			descriptor = candidate
			break
		}
	}
	if descriptor.Key == "" {
		t.Fatal("production Douyin descriptor is absent")
	}
	want := coreparser.CapabilityVideo | coreparser.CapabilityGallery | coreparser.CapabilityAudio | coreparser.CapabilityLivePhoto
	if descriptor.Capabilities != want {
		t.Fatalf("Douyin descriptor capabilities = %v, want %v", descriptor.Capabilities, want)
	}

	fixtures := []string{
		`<html><script>window._ROUTER_DATA = {"loaderData":{"video_(id)/page":{"videoInfoRes":{"item_list":[{"desc":"synthetic-video","video":{"play_addr":{"url_list":["https://cdn.example/video.mp4"],"uri":"synthetic-video-id"},"cover":{"url_list":["https://cdn.example/cover.jpg"]}}}]}}}}</script></html>`,
		`<html><script>window._ROUTER_DATA = {"loaderData":{"video_(id)/page":{"videoInfoRes":{"item_list":[{"desc":"synthetic-gallery","images":[{"url_list":["https://cdn.example/image.jpg"],"video":{"play_addr":{"url_list":["https://cdn.example/live.mp4"]}}}],"video":{"play_addr":{"uri":"https://cdn.example/audio.m4a"},"cover":{"url_list":["https://cdn.example/cover.jpg"]}}}]}}}}</script></html>`,
	}
	var observed coreparser.Capability
	for _, fixture := range fixtures {
		transport := &nativeRoundTripper{body: fixture}
		adapter, err := descriptor.New(coreparser.Dependencies{
			Fetcher: &deadlineRecordingFactory{transport: transport},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := adapter.Parse(t.Context(), coreparser.Request{ID: "synthetic-id", Platform: descriptor.Key})
		if err != nil {
			t.Fatal(err)
		}
		if err := result.ValidateAgainst(descriptor); err != nil {
			t.Fatalf("production descriptor rejected its adapter result: %v", err)
		}
		if result.VideoURL != "" {
			observed |= coreparser.CapabilityVideo
		}
		if result.AudioURL != "" {
			observed |= coreparser.CapabilityAudio
		}
		if len(result.Images) > 0 {
			observed |= coreparser.CapabilityGallery
		}
		for _, image := range result.Images {
			if image.LivePhotoURL != "" {
				observed |= coreparser.CapabilityLivePhoto
			}
		}
	}
	if observed != descriptor.Capabilities {
		t.Fatalf("actual production fixtures exercised capabilities %v, descriptor declares %v", observed, descriptor.Capabilities)
	}
}

func TestDescriptorAdapterBindsGuardedClientsToRequestBudget(t *testing.T) {
	t.Parallel()
	fixture := `<html><script>window._ROUTER_DATA = {"loaderData":{"video_(id)/page":{"videoInfoRes":{"item_list":[{"desc":"synthetic","images":[{"url_list":["https://cdn.example/image.jpg"],"video":{"play_addr":{"url_list":["https://cdn.example/live.mp4"]}}}],"video":{"play_addr":{"uri":"https://cdn.example/audio.m4a"},"cover":{"url_list":["https://cdn.example/cover.jpg"]}},"author":{"nickname":"fixture"}}]}}}}</script></html>`
	transport := &nativeRoundTripper{body: fixture}
	factory := &deadlineRecordingFactory{transport: transport}
	service, err := NewService(coreparser.Dependencies{Fetcher: factory})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := service.ParseVideoID(t.Context(), SourceDouYin, "synthetic-id"); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	deadlines := append([]time.Time(nil), factory.deadlines...)
	factory.mu.Unlock()
	if len(deadlines) != 1 || deadlines[0].IsZero() {
		t.Fatalf("descriptor adapter did not bind its HTTP client to the request budget: %#v", deadlines)
	}
	remaining := deadlines[0].Sub(started)
	if remaining <= 0 || remaining > 20*time.Second+100*time.Millisecond {
		t.Fatalf("descriptor request budget deadline = %s", remaining)
	}
}

func TestDescriptorAdapterEnforcesOneBudgetAcrossFreshClients(t *testing.T) {
	t.Parallel()
	transport := &nativeRoundTripper{body: `{}`}
	factory := &deadlineRecordingFactory{transport: transport}
	registrations := []nativeRegistration{{
		key: "budgetfixture", displayName: "budget fixture",
		hostRules:    []coreparser.HostRule{{Host: "media.example"}},
		capabilities: coreparser.CapabilityVideo,
		maxRequests:  2, maxRedirects: 1,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShare(multiRequestBudgetParser{clients: clients, urls: []string{
				"https://media.example/one",
				"https://media.example/two",
				"https://media.example/three",
			}})
		},
	}}
	descriptor := descriptorsFromRegistrations(registrations)[0]
	adapter, err := descriptor.New(coreparser.Dependencies{Fetcher: factory, Clock: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	target, err := netguard.NewFetchURL("https://media.example/share")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Parse(t.Context(), coreparser.Request{URL: target, Platform: descriptor.Key}); err == nil {
		t.Fatal("descriptor adapter allowed a third physical request")
	}
	if got := transport.calls.Load(); got != 2 {
		t.Fatalf("physical requests = %d, want 2", got)
	}
	factory.mu.Lock()
	deadlines := append([]time.Time(nil), factory.deadlines...)
	factory.mu.Unlock()
	if len(deadlines) != 3 || deadlines[0].IsZero() || !deadlines[0].Equal(deadlines[1]) || !deadlines[1].Equal(deadlines[2]) {
		t.Fatalf("fresh clients did not share one Parse deadline: %#v", deadlines)
	}
}

func TestStoppedRedirectStillConsumesSharedRedirectBudget(t *testing.T) {
	t.Parallel()
	budget, err := coreparser.NewRequestBudget(coreparser.BudgetOptions{
		MaxRequests: 2, MaxRedirects: 1, Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	clients := legacyHTTPClients{budget: budget}
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	guarded := clients.withBudget(base)
	next, err := http.NewRequest(http.MethodGet, "https://redirect.example/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := http.NewRequest(http.MethodGet, "https://redirect.example/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := guarded.CheckRedirect(next, []*http.Request{previous}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("first stopped redirect error = %v", err)
	}
	if err := guarded.CheckRedirect(next, []*http.Request{previous}); !errors.Is(err, coreparser.ErrBudgetExceeded) {
		t.Fatalf("stopped redirect did not consume the shared budget: %v", err)
	}
}
