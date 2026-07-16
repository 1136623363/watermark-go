package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

const sohuSuccessFixture = `{"status":200,"data":{"url_high_mp4":"https://cdn.example/video.mp4","video_name":"synthetic"}}`

type sohuResponseStep struct {
	status int
	body   string
	err    error
}

type sohuSequenceTransport struct {
	mu     sync.Mutex
	steps  []sohuResponseStep
	calls  int
	tokens []string
}

func (transport *sohuSequenceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls++
	transport.tokens = append(transport.tokens, request.URL.Query().Get("api_key"))
	index := transport.calls - 1
	if index >= len(transport.steps) {
		return nil, errors.New("unexpected Sohu request")
	}
	step := transport.steps[index]
	if step.err != nil {
		return nil, step.err
	}
	return &http.Response{
		StatusCode: step.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(step.body)),
		Request:    request,
	}, nil
}

func (transport *sohuSequenceTransport) snapshot() (int, []string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.calls, append([]string(nil), transport.tokens...)
}

func newSessionTestProvider(t *testing.T) *coreparser.SessionMaterialProvider {
	t.Helper()
	provider, err := coreparser.NewSessionMaterialProvider(coreparser.SessionMaterialOptions{
		TTL: time.Minute, Capacity: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestSohuSessionRefreshUsesTwoTokensTwoRequestsAndOneBudget(t *testing.T) {
	t.Parallel()
	transport := &sohuSequenceTransport{steps: []sohuResponseStep{
		{status: http.StatusUnauthorized, body: `{"status":401}`},
		{status: http.StatusOK, body: sohuSuccessFixture},
	}}
	factory := &deadlineRecordingFactory{transport: transport}
	var loadCalls atomic.Int32
	var firstBudget *coreparser.RequestBudget
	loader := coreparser.SessionLoader(func(_ context.Context, key coreparser.SessionMaterialKey, budget *coreparser.RequestBudget) (coreparser.SensitiveMaterial, error) {
		call := loadCalls.Add(1)
		if key != (coreparser.SessionMaterialKey{Platform: SourceSohu, Host: "api.tv.sohu.com"}) {
			t.Fatalf("session scope = %#v", key)
		}
		if budget == nil {
			t.Fatal("session loader received a nil request budget")
		}
		if call == 1 {
			firstBudget = budget
		} else if budget != firstBudget {
			t.Fatal("session refresh received a different request budget")
		}
		return coreparser.NewSensitiveMaterial([]string{"first-token", "second-token"}[call-1]), nil
	})
	service, err := NewService(coreparser.Dependencies{
		Fetcher: factory, Sessions: newSessionTestProvider(t), SessionLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ParseVideoID(t.Context(), SourceSohu, "synthetic-id")
	if err != nil {
		t.Fatal(err)
	}
	if result.VideoUrl != "https://cdn.example/video.mp4" {
		t.Fatalf("video URL = %q", result.VideoUrl)
	}
	if loadCalls.Load() != 2 {
		t.Fatalf("session loads = %d, want 2", loadCalls.Load())
	}
	calls, tokens := transport.snapshot()
	if calls != 2 {
		t.Fatalf("Sohu requests = %d, want 2", calls)
	}
	if len(tokens) != 2 || tokens[0] != "first-token" || tokens[1] != "second-token" {
		t.Fatalf("request tokens did not rotate once: %#v", tokens)
	}
}

func TestSohuSessionScopeUsesExactCredentialAuthorityForIDAndURL(t *testing.T) {
	t.Parallel()
	transport := &sohuSequenceTransport{steps: []sohuResponseStep{
		{status: http.StatusOK, body: sohuSuccessFixture},
		{status: http.StatusOK, body: sohuSuccessFixture},
		{status: http.StatusOK, body: sohuSuccessFixture},
	}}
	factory := &deadlineRecordingFactory{transport: transport}
	var mu sync.Mutex
	var loaded []coreparser.SessionMaterialKey
	loader := coreparser.SessionLoader(func(_ context.Context, key coreparser.SessionMaterialKey, budget *coreparser.RequestBudget) (coreparser.SensitiveMaterial, error) {
		if budget == nil {
			t.Fatal("session loader received a nil request budget")
		}
		mu.Lock()
		loaded = append(loaded, key)
		mu.Unlock()
		return coreparser.NewSensitiveMaterial("scope-token"), nil
	})
	service, err := NewService(coreparser.Dependencies{
		Fetcher: factory, Sessions: newSessionTestProvider(t), SessionLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.ParseVideoID(t.Context(), SourceSohu, "first-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ParseVideoShareURL(t.Context(), "https://my.tv.sohu.com/us/1/2.shtml"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ParseVideoID(t.Context(), SourceSohu, "third-id"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	keys := append([]coreparser.SessionMaterialKey(nil), loaded...)
	mu.Unlock()
	want := []coreparser.SessionMaterialKey{
		{Platform: SourceSohu, Host: "api.tv.sohu.com"},
	}
	if len(keys) != len(want) || keys[0] != want[0] {
		t.Fatalf("loaded scopes = %#v, want %#v", keys, want)
	}
}

func TestSohuSessionExpiryRefreshesAtMostOnce(t *testing.T) {
	t.Parallel()
	transport := &sohuSequenceTransport{steps: []sohuResponseStep{
		{status: http.StatusUnauthorized, body: `{"status":401}`},
		{status: http.StatusForbidden, body: `{"status":403}`},
	}}
	factory := &deadlineRecordingFactory{transport: transport}
	var loads atomic.Int32
	service, err := NewService(coreparser.Dependencies{
		Fetcher: factory, Sessions: newSessionTestProvider(t),
		SessionLoader: func(context.Context, coreparser.SessionMaterialKey, *coreparser.RequestBudget) (coreparser.SensitiveMaterial, error) {
			call := loads.Add(1)
			return coreparser.NewSensitiveMaterial([]string{"first-token", "second-token"}[call-1]), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.ParseVideoID(t.Context(), SourceSohu, "synthetic-id")
	var typed *coreparser.ParseError
	if !errors.As(err, &typed) || typed.Code != coreparser.ErrorSessionExpired {
		t.Fatalf("second expiry error = %#v", err)
	}
	formatted := fmt.Sprintf("%v|%+v|%#v", err, err, err)
	if strings.Contains(formatted, "first-token") || strings.Contains(formatted, "second-token") {
		t.Fatalf("Sohu session material leaked through error formatting: %s", formatted)
	}
	if loads.Load() != 2 {
		t.Fatalf("session loads = %d, want 2", loads.Load())
	}
	calls, _ := transport.snapshot()
	if calls != 2 {
		t.Fatalf("Sohu requests = %d, want 2", calls)
	}
}

type sessionErrorParser struct {
	err   error
	calls *atomic.Int32
}

func (parser sessionErrorParser) parseShareUrl(string) (*VideoParseInfo, error) {
	parser.calls.Add(1)
	return nil, parser.err
}

func (parser sessionErrorParser) parseVideoID(string) (*VideoParseInfo, error) {
	parser.calls.Add(1)
	return nil, parser.err
}

func TestSessionAdapterDoesNotRefreshNonExpiryErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "security", err: coreparser.NewParseError(coreparser.ErrorSecurityRejected, errors.New("synthetic"))},
		{name: "schema", err: coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("synthetic"))},
		{name: "credential", err: coreparser.NewParseError(coreparser.ErrorCredentialRequired, errors.New("synthetic"))},
		{name: "upstream", err: coreparser.NewParseError(coreparser.ErrorUpstreamFailed, errors.New("synthetic"))},
		{name: "internal", err: errors.New("synthetic")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var parserCalls atomic.Int32
			registration := nativeRegistration{
				key: "sessionfixture", displayName: "session fixture",
				hostRules:    []coreparser.HostRule{{Host: "session.example"}},
				capabilities: coreparser.CapabilityVideo, sessionHost: "session.example",
				maxRequests: 2, maxRedirects: 1,
				bind: func(legacyHTTPClients) legacyParserBinding {
					return bindShareAndID(sessionErrorParser{err: test.err, calls: &parserCalls})
				},
			}
			descriptor := descriptorsFromRegistrations([]nativeRegistration{registration})[0]
			var loads atomic.Int32
			adapter, err := descriptor.New(coreparser.Dependencies{
				Fetcher:  &deadlineRecordingFactory{transport: &nativeRoundTripper{body: `{}`}},
				Sessions: newSessionTestProvider(t),
				SessionLoader: func(context.Context, coreparser.SessionMaterialKey, *coreparser.RequestBudget) (coreparser.SensitiveMaterial, error) {
					loads.Add(1)
					return coreparser.NewSensitiveMaterial("session"), nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = adapter.Parse(t.Context(), coreparser.Request{ID: "synthetic", Platform: descriptor.Key})
			if loads.Load() != 1 {
				t.Fatalf("non-expiry error caused %d session loads", loads.Load())
			}
			if parserCalls.Load() != 1 {
				t.Fatalf("non-expiry error caused %d parser attempts", parserCalls.Load())
			}
		})
	}
}

func TestSohuDirectSecretInjectionRemainsAvailableForHermeticTests(t *testing.T) {
	t.Parallel()
	transport := &sohuSequenceTransport{steps: []sohuResponseStep{{status: http.StatusOK, body: sohuSuccessFixture}}}
	service, err := NewService(coreparser.Dependencies{
		Fetcher:   &deadlineRecordingFactory{transport: transport},
		SohuToken: coreparser.NewSensitiveMaterial("direct-test-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ParseVideoID(t.Context(), SourceSohu, "synthetic-id"); err != nil {
		t.Fatal(err)
	}
	calls, tokens := transport.snapshot()
	if calls != 1 || len(tokens) != 1 || tokens[0] != "direct-test-token" {
		t.Fatalf("direct secret compatibility requests=%d tokens=%#v", calls, tokens)
	}
}

func TestSohuPartialSessionDependenciesCannotBypassProviderWithDirectSecret(t *testing.T) {
	t.Parallel()
	transport := &sohuSequenceTransport{steps: []sohuResponseStep{{status: http.StatusOK, body: sohuSuccessFixture}}}
	service, err := NewService(coreparser.Dependencies{
		Fetcher:   &deadlineRecordingFactory{transport: transport},
		Sessions:  newSessionTestProvider(t),
		SohuToken: coreparser.NewSensitiveMaterial("must-not-bypass-provider"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ParseVideoID(t.Context(), SourceSohu, "synthetic-id")
	var typed *coreparser.ParseError
	if !errors.As(err, &typed) || typed.Code != coreparser.ErrorCredentialRequired {
		t.Fatalf("partial session dependency error = %#v", err)
	}
	calls, _ := transport.snapshot()
	if calls != 0 {
		t.Fatalf("partial session dependency used the direct secret for %d requests", calls)
	}
}

func TestSohuCancellationDoesNotLeakCredentialInRequestURL(t *testing.T) {
	t.Parallel()
	const sentinel = "cancel-session-sentinel"
	transport := &sohuSequenceTransport{steps: []sohuResponseStep{{err: context.Canceled}}}
	service, err := NewService(coreparser.Dependencies{
		Fetcher:   &deadlineRecordingFactory{transport: transport},
		SohuToken: coreparser.NewSensitiveMaterial(sentinel),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ParseVideoID(t.Context(), SourceSohu, "synthetic-id")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity was lost: %v", err)
	}
	formatted := fmt.Sprintf("%v|%+v|%#v", err, err, err)
	if strings.Contains(formatted, sentinel) {
		t.Fatalf("canceled Sohu request leaked its credential URL: %s", formatted)
	}
}
