package parser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

type candidateResolver struct{}

func (candidateResolver) LookupNetIP(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type candidateDialer struct{ destination string }

func (dialer candidateDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, dialer.destination)
}

type candidateErrorTransport struct{ err error }

func (transport candidateErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, transport.err
}

type candidateErrorFactory struct{ err error }

func (factory candidateErrorFactory) HTTPClient(context.Context, int) *http.Client {
	return &http.Client{Transport: candidateErrorTransport{err: factory.err}}
}

func (factory candidateErrorFactory) HTTPClientWithRedirect(_ context.Context, _ int, redirect func(*http.Request, []*http.Request) error) *http.Client {
	return &http.Client{Transport: candidateErrorTransport{err: factory.err}, CheckRedirect: redirect}
}

func TestGuardedHEADCandidateFallbackUsesOneSharedBudget(t *testing.T) {
	var mu sync.Mutex
	requested := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requested = append(requested, request.URL.Path)
		mu.Unlock()
		if request.Method != http.MethodHead {
			t.Errorf("candidate probe method = %s, want HEAD", request.Method)
		}
		if request.URL.Path == "/1080" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	validator, err := netguard.NewValidator(netguard.ValidatorOptions{
		Resolver: candidateResolver{},
		Dialer:   candidateDialer{destination: server.Listener.Addr().String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := netguard.NewFetcher(netguard.FetcherOptions{Validator: validator, Limits: netguard.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 2, MaxRedirects: 1, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	candidates := []MediaCandidate{
		{URL: "http://127.0.0.1/private", Quality: 2160, SourceRank: 0},
		{URL: "http://media.example/1080", Quality: 1080, SourceRank: 1},
		{URL: "http://media.example/720", Quality: 720, SourceRank: 2},
	}
	selected, err := AttemptMediaCandidatesWithHEAD(t.Context(), candidates, budget, fetcher, 1)
	if err != nil {
		t.Fatal(err)
	}
	if selected.URL != "http://media.example/720" {
		t.Fatalf("selected candidate = %#v", selected)
	}
	mu.Lock()
	gotRequests := append([]string(nil), requested...)
	mu.Unlock()
	if want := []string{"/1080", "/720"}; !reflect.DeepEqual(gotRequests, want) {
		t.Fatalf("guarded HEAD requests = %#v, want %#v", gotRequests, want)
	}
	if err := budget.AllowFetch(mustCandidateFetchURL(t, "http://media.example/third")); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("candidate fallback did not consume the shared request budget: %v", err)
	}
}

func TestGuardedHEADCandidateFallbackStopsAtTotalRequestBudget(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	validator, err := netguard.NewValidator(netguard.ValidatorOptions{
		Resolver: candidateResolver{}, Dialer: candidateDialer{destination: server.Listener.Addr().String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := netguard.NewFetcher(netguard.FetcherOptions{Validator: validator, Limits: netguard.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 2, MaxRedirects: 0, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = AttemptMediaCandidatesWithHEAD(t.Context(), []MediaCandidate{
		{URL: "http://media.example/one", SourceRank: 0},
		{URL: "http://media.example/two", SourceRank: 1},
		{URL: "http://media.example/three", SourceRank: 2},
	}, budget, fetcher, 0)
	if !errors.Is(err, ErrBudgetExceeded) || calls != 2 {
		t.Fatalf("fallback escaped total budget: calls=%d error=%v", calls, err)
	}
}

func TestGuardedHEADRedirectCountsEveryPhysicalRequest(t *testing.T) {
	t.Run("second physical request exceeds request budget", func(t *testing.T) {
		var mu sync.Mutex
		requested := make([]string, 0, 2)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			mu.Lock()
			requested = append(requested, request.URL.Path)
			mu.Unlock()
			if request.URL.Path == "/start" {
				writer.Header().Set("Location", "http://redirect.example/final")
				writer.WriteHeader(http.StatusFound)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		fetcher := newCandidateTestFetcher(t, server.Listener.Addr().String())
		budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 1, MaxRedirects: 1, Duration: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		const sentinel = "request-budget-query-sentinel"
		_, err = AttemptMediaCandidatesWithHEAD(t.Context(), []MediaCandidate{{
			URL: "http://media.example/start?token=" + sentinel, SourceRank: 0,
		}}, budget, fetcher, 1)
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("redirect request budget error = %v", err)
		}
		assertCandidateErrorOpaque(t, err, sentinel)
		mu.Lock()
		got := append([]string(nil), requested...)
		mu.Unlock()
		if want := []string{"/start"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("requests made after request budget exhausted = %#v, want %#v", got, want)
		}
	})

	t.Run("two requests and one redirect succeed", func(t *testing.T) {
		var mu sync.Mutex
		requested := make([]string, 0, 2)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			mu.Lock()
			requested = append(requested, request.URL.Path)
			mu.Unlock()
			if request.URL.Path == "/start" {
				writer.Header().Set("Location", "http://redirect.example/final")
				writer.WriteHeader(http.StatusFound)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		fetcher := newCandidateTestFetcher(t, server.Listener.Addr().String())
		budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 2, MaxRedirects: 1, Duration: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		selected, err := AttemptMediaCandidatesWithHEAD(t.Context(), []MediaCandidate{{
			URL: "http://media.example/start", SourceRank: 0,
		}}, budget, fetcher, 1)
		if err != nil || selected.URL != "http://media.example/start" {
			t.Fatalf("redirect candidate selection = %#v, error=%v", selected, err)
		}
		mu.Lock()
		got := append([]string(nil), requested...)
		mu.Unlock()
		if want := []string{"/start", "/final"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("redirect request sequence = %#v, want %#v", got, want)
		}
	})
}

func TestGuardedHEADRedirectRejectsDuplicateTargetBeforeSecondRequest(t *testing.T) {
	const sentinel = "duplicate-query-sentinel"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.Header().Set("Location", "http://media.example/loop?token="+sentinel)
		writer.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	fetcher := newCandidateTestFetcher(t, server.Listener.Addr().String())
	budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 2, MaxRedirects: 1, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = AttemptMediaCandidatesWithHEAD(t.Context(), []MediaCandidate{{
		URL: "http://media.example/loop?token=" + sentinel, SourceRank: 0,
	}}, budget, fetcher, 1)
	if !errors.Is(err, ErrDuplicateFetch) || calls != 1 {
		t.Fatalf("duplicate redirect gate: calls=%d error=%v", calls, err)
	}
	assertCandidateErrorOpaque(t, err, sentinel)
}

func TestGuardedHEADCandidateCancellationErrorIsOpaque(t *testing.T) {
	const sentinel = "cancellation-query-sentinel"
	budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 1, MaxRedirects: 0, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = AttemptMediaCandidatesWithHEAD(t.Context(), []MediaCandidate{{
		URL: "http://media.example/video?token=" + sentinel,
	}}, budget, candidateErrorFactory{err: context.Canceled}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("candidate cancellation error = %v", err)
	}
	assertCandidateErrorOpaque(t, err, sentinel)
}

func newCandidateTestFetcher(t *testing.T, destination string) *netguard.Fetcher {
	t.Helper()
	validator, err := netguard.NewValidator(netguard.ValidatorOptions{
		Resolver: candidateResolver{}, Dialer: candidateDialer{destination: destination},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := netguard.NewFetcher(netguard.FetcherOptions{Validator: validator, Limits: netguard.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	return fetcher
}

func mustCandidateFetchURL(t *testing.T, raw string) netguard.FetchURL {
	t.Helper()
	target, err := netguard.NewFetchURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func assertCandidateErrorOpaque(t *testing.T, err error, sentinel string) {
	t.Helper()
	formatted := fmt.Sprintf("%v|%+v|%#v", err, err, err)
	if strings.Contains(formatted, sentinel) {
		t.Fatalf("candidate error exposed query material: %s", formatted)
	}
}
