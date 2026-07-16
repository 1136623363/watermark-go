package netguard

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type staticResolver map[string][]netip.Addr

func (resolver staticResolver) LookupNetIP(_ context.Context, host string) ([]netip.Addr, error) {
	addresses, ok := resolver[host]
	if !ok {
		return nil, errors.New("host absent from hermetic resolver")
	}
	return append([]netip.Addr(nil), addresses...), nil
}

type lookupResult struct {
	addresses []netip.Addr
	err       error
}

type sequenceResolver struct {
	mu      sync.Mutex
	results map[string][]lookupResult
}

type blockingResolver struct{}

func (blockingResolver) LookupNetIP(ctx context.Context, _ string) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type redirectBlockingResolver struct{}

func (redirectBlockingResolver) LookupNetIP(ctx context.Context, host string) ([]netip.Addr, error) {
	if host == "one.example" {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (resolver *sequenceResolver) LookupNetIP(_ context.Context, host string) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	results := resolver.results[host]
	if len(results) == 0 {
		return nil, errors.New("scripted resolver exhausted")
	}
	result := results[0]
	resolver.results[host] = results[1:]
	return append([]netip.Addr(nil), result.addresses...), result.err
}

type recordingDialer struct {
	addresses []string
}

func (dialer *recordingDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	dialer.addresses = append(dialer.addresses, address)
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(io.Discard, server)
	}()
	return client, nil
}

type failingDialer struct{}

func (failingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("synthetic dial failure")
}

type mappedDialer struct {
	mu           sync.Mutex
	destinations map[string]string
	addresses    []string
}

func (dialer *mappedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer.mu.Lock()
	dialer.addresses = append(dialer.addresses, address)
	destination, ok := dialer.destinations[address]
	dialer.mu.Unlock()
	if !ok {
		return nil, errors.New("validated address absent from hermetic dial map")
	}
	return (&net.Dialer{}).DialContext(ctx, network, destination)
}

func TestFetchURLRejectsUnsafeInputAndCannotRevealQuery(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"file:///etc/passwd",
		"https://user:password@example.com/video",
		"http://127.0.0.1/admin",
		"http://[::ffff:127.0.0.1]/admin",
		"https://example.com:8443/video",
		"https://example.com/%zz",
	} {
		if _, err := NewFetchURL(raw); err == nil {
			t.Fatalf("NewFetchURL(%q) unexpectedly succeeded", raw)
		}
	}

	fetchURL, err := NewFetchURL("https://Example.COM./watch/path?token=do-not-log#fragment")
	if err != nil {
		t.Fatal(err)
	}
	if got := fetchURL.Safe().String(); got != "https://example.com/watch/path" {
		t.Fatalf("safe URL = %q", got)
	}
	if strings.Contains(fetchURL.Safe().String(), "do-not-log") {
		t.Fatal("safe URL disclosed query material")
	}
	publicIPv6, err := NewFetchURL("https://[2606:2800:220:1:248:1893:25c8:1946]/video")
	if err != nil || publicIPv6.Safe().String() != "https://[2606:2800:220:1:248:1893:25c8:1946]/video" {
		t.Fatalf("public IPv6 URL was not canonicalized: safe=%q error=%v", publicIPv6.Safe().String(), err)
	}
}

func TestFetchURLFormattingNeverRevealsQueryMaterial(t *testing.T) {
	t.Parallel()
	const sentinel = "format-query-sentinel"
	target, err := NewFetchURL("https://media.example/watch?session=" + sentinel)
	if err != nil {
		t.Fatal(err)
	}

	formats := []string{
		"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%o", "%f", "%e", "%c", "%U", "%p", "%T", "%w", "%z",
		"% 120.80v", "%#+120.80q",
	}
	for _, format := range formats {
		for name, value := range map[string]any{
			"value":     target,
			"pointer":   &target,
			"interface": any(target),
			"nested":    []any{target, &target},
		} {
			if rendered := fmt.Sprintf(format, value); strings.Contains(rendered, sentinel) {
				t.Fatalf("format %q (%s) revealed query material: %q", format, name, rendered)
			}
		}
	}
}

func TestValidatorRejectsPrivateAndMixedDNSAnswers(t *testing.T) {
	t.Parallel()
	validator, err := NewValidator(ValidatorOptions{Resolver: staticResolver{
		"private.example": {netip.MustParseAddr("10.0.0.8")},
		"mixed.example":   {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")},
		"public.example":  {netip.MustParseAddr("93.184.216.34")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://private.example/a", "https://mixed.example/a"} {
		fetchURL, parseErr := NewFetchURL(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if validateErr := validator.Validate(t.Context(), fetchURL); !errors.Is(validateErr, ErrUnsafeDestination) {
			t.Fatalf("Validate(%q) error = %v", raw, validateErr)
		}
	}
	publicURL, err := NewFetchURL("https://public.example/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(t.Context(), publicURL); err != nil {
		t.Fatalf("public destination rejected: %v", err)
	}
}

func TestDialContextPinsAValidatedPublicAddress(t *testing.T) {
	t.Parallel()
	dialer := &recordingDialer{}
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{"public.example": {netip.MustParseAddr("93.184.216.34")}},
		Dialer:   dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := validator.DialContext(t.Context(), "tcp", "public.example:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if len(dialer.addresses) != 1 || dialer.addresses[0] != "93.184.216.34:443" {
		t.Fatalf("dialed addresses = %#v", dialer.addresses)
	}
}

func TestDialContextRejectsRebindingAndMixedAnswersBeforeAnyDial(t *testing.T) {
	t.Parallel()
	public := netip.MustParseAddr("93.184.216.34")
	private := netip.MustParseAddr("127.0.0.1")
	for _, test := range []struct {
		name       string
		dialAnswer []netip.Addr
	}{
		{name: "rebinds-private", dialAnswer: []netip.Addr{private}},
		{name: "mixed-public-first", dialAnswer: []netip.Addr{public, private}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialer := &recordingDialer{}
			resolver := &sequenceResolver{results: map[string][]lookupResult{
				"media.example": {
					{addresses: []netip.Addr{public}},
					{addresses: test.dialAnswer},
				},
			}}
			validator, err := NewValidator(ValidatorOptions{Resolver: resolver, Dialer: dialer})
			if err != nil {
				t.Fatal(err)
			}
			target, err := NewFetchURL("https://media.example/watch")
			if err != nil {
				t.Fatal(err)
			}
			if err := validator.Validate(t.Context(), target); err != nil {
				t.Fatalf("initial public answer rejected: %v", err)
			}
			if _, err := validator.DialContext(t.Context(), "tcp", "media.example:443"); !errors.Is(err, ErrUnsafeDestination) {
				t.Fatalf("unsafe dial answer error = %v", err)
			}
			if len(dialer.addresses) != 0 {
				t.Fatalf("dial attempted before validating the complete answer set: %#v", dialer.addresses)
			}
		})
	}
}

func TestValidatorRejectsEmptyAndFailedDNSAnswers(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		result lookupResult
	}{
		{name: "empty", result: lookupResult{}},
		{name: "error", result: lookupResult{err: errors.New("synthetic resolver failure")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &sequenceResolver{results: map[string][]lookupResult{"media.example": {test.result}}}
			validator, err := NewValidator(ValidatorOptions{Resolver: resolver})
			if err != nil {
				t.Fatal(err)
			}
			target, err := NewFetchURL("https://media.example/watch")
			if err != nil {
				t.Fatal(err)
			}
			if err := validator.Validate(t.Context(), target); err == nil {
				t.Fatal("unusable DNS answer was accepted")
			}
		})
	}
}

func TestFetcherDurationStartsBeforeDNSResolution(t *testing.T) {
	t.Parallel()
	validator, err := NewValidator(ValidatorOptions{Resolver: blockingResolver{}})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{
		Validator: validator,
		Limits: Limits{
			ResponseHeaderBytes: 1024,
			WireBodyBytes:       1024,
			DecodedBodyBytes:    1024,
			Duration:            25 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewFetchURL("https://media.example/watch")
	if err != nil {
		t.Fatal(err)
	}
	outer, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = fetcher.Fetch(outer, FetchRequest{URL: target})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DNS duration error = %v", err)
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("configured duration did not cover DNS resolution: %s", elapsed)
	}
}

func TestFetcherDurationCoversRedirectDNSResolution(t *testing.T) {
	t.Parallel()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", "http://two.example/final")
		writer.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	dialer := &mappedDialer{destinations: map[string]string{
		"93.184.216.34:80": source.Listener.Addr().String(),
	}}
	validator, err := NewValidator(ValidatorOptions{Resolver: redirectBlockingResolver{}, Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{
		Validator: validator,
		Limits: Limits{
			ResponseHeaderBytes: 1024,
			WireBodyBytes:       1024,
			DecodedBodyBytes:    1024,
			Duration:            25 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewFetchURL("http://one.example/start")
	if err != nil {
		t.Fatal(err)
	}
	outer, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = fetcher.Fetch(outer, FetchRequest{URL: target, MaxRedirects: 2})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("redirect DNS duration error = %v", err)
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("configured duration did not cover redirect DNS: %s", elapsed)
	}
}

func TestFetcherDurationCoversResponseBodyRead(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	dialer := &mappedDialer{destinations: map[string]string{
		"93.184.216.34:80": server.Listener.Addr().String(),
	}}
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{"media.example": {netip.MustParseAddr("93.184.216.34")}},
		Dialer:   dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{
		Validator: validator,
		Limits: Limits{
			ResponseHeaderBytes: 1024,
			WireBodyBytes:       1024,
			DecodedBodyBytes:    1024,
			Duration:            25 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewFetchURL("http://media.example/stream")
	if err != nil {
		t.Fatal(err)
	}
	response, err := fetcher.Fetch(t.Context(), FetchRequest{URL: target})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	started := time.Now()
	_, err = io.ReadAll(response.Body)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("body duration error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 200*time.Millisecond {
		t.Fatalf("body read escaped configured duration: %s", elapsed)
	}
}

func TestRedirectPolicyRevalidatesAndStripsSensitiveHeadersAcrossOrigins(t *testing.T) {
	t.Parallel()
	validator, err := NewValidator(ValidatorOptions{Resolver: staticResolver{
		"one.example":      {netip.MustParseAddr("93.184.216.34")},
		"two.example":      {netip.MustParseAddr("93.184.216.35")},
		"internal.example": {netip.MustParseAddr("192.168.1.3")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := http.NewRequest(http.MethodGet, "https://one.example/start", nil)
	next, _ := http.NewRequest(http.MethodGet, "https://two.example/next", nil)
	next.Header.Set("Authorization", "sensitive")
	next.Header.Set("Cookie", "sensitive")
	next.Header.Set("Proxy-Authorization", "sensitive")
	if err := validator.CheckRedirect(2)(next, []*http.Request{previous}); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if next.Header.Get(header) != "" {
			t.Fatalf("redirect retained %s", header)
		}
	}

	unsafe, _ := http.NewRequest(http.MethodGet, "https://internal.example/admin", nil)
	if err := validator.CheckRedirect(2)(unsafe, []*http.Request{previous}); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("unsafe redirect error = %v", err)
	}
	third, _ := http.NewRequest(http.MethodGet, "https://two.example/third", nil)
	if err := validator.CheckRedirect(1)(third, []*http.Request{previous, next}); !errors.Is(err, ErrRedirectLimit) {
		t.Fatalf("redirect limit error = %v", err)
	}
}

func TestRealRedirectFinalSendCannotRestoreCrossOriginCredentials(t *testing.T) {
	t.Parallel()
	received := make(chan http.Header, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received <- request.Header.Clone()
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", "http://two.example/final")
		writer.WriteHeader(http.StatusFound)
	}))
	defer source.Close()

	dialer := &mappedDialer{destinations: map[string]string{
		"93.184.216.34:80": source.Listener.Addr().String(),
		"93.184.216.35:80": destination.Listener.Addr().String(),
	}}
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{
			"one.example": {netip.MustParseAddr("93.184.216.34")},
			"two.example": {netip.MustParseAddr("93.184.216.35")},
		},
		Dialer: dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{Validator: validator, Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewFetchURL("http://one.example/start")
	if err != nil {
		t.Fatal(err)
	}
	raw := ""
	if err := target.Use(func(value string) error { raw = value; return nil }); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := fetcher.HTTPClientWithRedirect(t.Context(), 3, func(next *http.Request, via []*http.Request) error {
		// Treat the hook as hostile: it must not be able to rewrite redirect
		// history so the final mandatory scrub mistakes this for same-origin.
		if len(via) > 0 {
			via[len(via)-1].URL = next.URL
		}
		for key, value := range map[string]string{
			"Authorization":       "redacted",
			"Cookie":              "session=redacted",
			"Proxy-Authorization": "redacted",
			"Referer":             "http://one.example/private?id=redacted",
			"Origin":              "http://one.example",
			"X-Auth":              "redacted",
			"X-Signature":         "redacted",
			"X-Harmless-Trace":    "retained",
		} {
			next.Header.Set(key, value)
		}
		// http.Header is a map, so a hostile hook can bypass Set's canonical
		// spelling and insert keys whose casing Header.Del does not remove.
		next.Header["cOoKiE"] = []string{"mixed-case-session=redacted"}
		next.Header["x-aUtH-context"] = []string{"mixed-case-redacted"}
		return nil
	})
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	headers := <-received
	for _, key := range []string{
		"Authorization", "Cookie", "Proxy-Authorization", "Referer", "Origin",
		"X-Auth", "X-Signature", "X-Auth-Context",
	} {
		if value := headers.Get(key); value != "" {
			t.Fatalf("final redirect send retained %s=%q", key, value)
		}
	}
	if got := headers.Get("X-Harmless-Trace"); got != "retained" {
		t.Fatalf("non-sensitive redirect header = %q", got)
	}
}

func TestRedirectObservationHookCannotChangeValidatedTarget(t *testing.T) {
	t.Parallel()
	validator, err := NewValidator(ValidatorOptions{Resolver: staticResolver{
		"one.example":   {netip.MustParseAddr("93.184.216.34")},
		"two.example":   {netip.MustParseAddr("93.184.216.35")},
		"three.example": {netip.MustParseAddr("93.184.216.36")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{Validator: validator, Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	client := fetcher.HTTPClientWithRedirect(t.Context(), 2, func(request *http.Request, _ []*http.Request) error {
		changed, parseErr := url.Parse("https://three.example/changed")
		if parseErr != nil {
			return parseErr
		}
		request.URL = changed
		return nil
	})
	previous, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://one.example/start", nil)
	next, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://two.example/next", nil)
	if err := client.CheckRedirect(next, []*http.Request{previous}); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("redirect target mutation error = %v", err)
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (body *trackingReadCloser) Close() error {
	body.closed = true
	return nil
}

func responseLimits(headerBytesLimit, wireBytes, decodedBytes int64) Limits {
	return Limits{
		ResponseHeaderBytes: headerBytesLimit,
		WireBodyBytes:       wireBytes,
		DecodedBodyBytes:    decodedBytes,
		Duration:            time.Second,
	}
}

func TestResponseHeaderLimitAcceptsExactAndRejectsMaxPlusOne(t *testing.T) {
	t.Parallel()
	header := http.Header{"X-Test": {"abc"}}
	if got := headerBytes(header); got != int64(len("X-Test: abc\r\n\r\n")) {
		t.Fatalf("test header wire accounting = %d", got)
	}
	if got := headerBytes(http.Header{"X-Test": {"abc", "def"}}); got != int64(len("X-Test: abc\r\nX-Test: def\r\n\r\n")) {
		t.Fatalf("repeated header wire accounting = %d", got)
	}
	exactBody := &trackingReadCloser{Reader: strings.NewReader("")}
	exact, err := limitResponse(&http.Response{Header: header.Clone(), Body: exactBody, ContentLength: -1}, responseLimits(15, 8, 8))
	if err != nil {
		t.Fatal(err)
	}
	if err := exact.Body.Close(); err != nil || !exactBody.closed {
		t.Fatalf("exact-limit body close: closed=%t error=%v", exactBody.closed, err)
	}
	tooLargeBody := &trackingReadCloser{Reader: strings.NewReader("")}
	if _, err := limitResponse(&http.Response{Header: header.Clone(), Body: tooLargeBody, ContentLength: -1}, responseLimits(14, 8, 8)); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("header max+1 error = %v", err)
	}
	if !tooLargeBody.closed {
		t.Fatal("header rejection did not close the upstream body")
	}
}

func TestWireBodyLimitAcceptsExactAndRejectsMaxPlusOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "exact", body: "12345"},
		{name: "max-plus-one", body: "123456", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &trackingReadCloser{Reader: strings.NewReader(test.body)}
			response, err := limitResponse(&http.Response{Header: make(http.Header), Body: upstream, ContentLength: -1}, responseLimits(128, 5, 32))
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(response.Body)
			if test.wantError {
				if !errors.Is(readErr, ErrResponseTooLarge) || string(got) != "12345" {
					t.Fatalf("wire max+1: body=%q error=%v", got, readErr)
				}
			} else if readErr != nil || string(got) != test.body {
				t.Fatalf("wire exact: body=%q error=%v", got, readErr)
			}
			if err := response.Body.Close(); err != nil || !upstream.closed {
				t.Fatalf("wire body close: closed=%t error=%v", upstream.closed, err)
			}
		})
	}
}

func TestWireContentLengthRejectsMaxPlusOneAndClosesBody(t *testing.T) {
	t.Parallel()
	upstream := &trackingReadCloser{Reader: strings.NewReader("123456")}
	_, err := limitResponse(&http.Response{
		Header: make(http.Header), Body: upstream, ContentLength: 6,
	}, responseLimits(128, 5, 32))
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("declared wire max+1 error = %v", err)
	}
	if !upstream.closed {
		t.Fatal("declared wire max+1 did not close upstream body")
	}
}

func TestDecodedBodyLimitAcceptsExactAndRejectsMaxPlusOne(t *testing.T) {
	t.Parallel()
	for _, size := range []int{5, 6} {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write(bytes.Repeat([]byte("x"), size))
		_ = writer.Close()
		upstream := &trackingReadCloser{Reader: bytes.NewReader(compressed.Bytes())}
		response, err := limitResponse(&http.Response{
			Header: http.Header{"Content-Encoding": {"gzip"}}, Body: upstream, ContentLength: -1,
		}, responseLimits(128, 128, 5))
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(response.Body)
		if size == 5 && (readErr != nil || len(got) != 5) {
			t.Fatalf("decoded exact: bytes=%d error=%v", len(got), readErr)
		}
		if size == 6 && (!errors.Is(readErr, ErrResponseTooLarge) || len(got) != 5) {
			t.Fatalf("decoded max+1: bytes=%d error=%v", len(got), readErr)
		}
		if err := response.Body.Close(); err != nil || !upstream.closed {
			t.Fatalf("decoded body close: closed=%t error=%v", upstream.closed, err)
		}
	}
}

func TestMalformedAndUnsupportedContentEncodingCloseBody(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		encoding string
		body     string
	}{
		{name: "malformed-gzip", encoding: "gzip", body: "not-gzip"},
		{name: "unsupported", encoding: "br", body: "opaque"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &trackingReadCloser{Reader: strings.NewReader(test.body)}
			_, err := limitResponse(&http.Response{
				Header: http.Header{"Content-Encoding": {test.encoding}}, Body: upstream, ContentLength: -1,
			}, responseLimits(128, 128, 128))
			if err == nil {
				t.Fatal("invalid content encoding was accepted")
			}
			if !upstream.closed {
				t.Fatal("invalid content encoding did not close upstream body")
			}
		})
	}
}

func TestFetcherErrorDoesNotExposeQueryMaterial(t *testing.T) {
	t.Parallel()
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{"media.example": {netip.MustParseAddr("93.184.216.34")}},
		Dialer:   failingDialer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{
		Validator: validator,
		Limits:    DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := "query-material-sentinel"
	target, err := NewFetchURL("https://media.example/watch?xsec_token=" + sentinel)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(t.Context(), FetchRequest{URL: target})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("unsafe guarded fetch error: %v", err)
	}
}

func TestFetcherOptionsExposeNoRoundTripperBypass(t *testing.T) {
	t.Parallel()
	if field, exists := reflect.TypeOf(FetcherOptions{}).FieldByName("Ba" + "se"); exists {
		t.Fatalf("guarded fetcher exposes arbitrary transport bypass: %s %s", field.Name, field.Type)
	}
}

func TestFetcherTransportPreservesPinnedDialProxyAndTLSInvariants(t *testing.T) {
	t.Parallel()
	validator, err := NewValidator(ValidatorOptions{Resolver: staticResolver{
		"media.example": {netip.MustParseAddr("93.184.216.34")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	fetcher, err := NewFetcher(FetcherOptions{Validator: validator, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	transport := fetcher.transport.base
	if transport.Proxy != nil || transport.DialContext == nil {
		t.Fatalf("transport proxy/dial invariant drifted: hasProxy=%t hasDial=%t", transport.Proxy != nil, transport.DialContext != nil)
	}
	if !transport.DisableCompression || !transport.ForceAttemptHTTP2 || transport.MaxResponseHeaderBytes != limits.ResponseHeaderBytes {
		t.Fatalf("transport limit invariant drifted: %#v", transport)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("transport TLS invariant drifted: %#v", transport.TLSClientConfig)
	}
}

func TestGuardedTransportEnforcesTLS12WithRealHandshake(t *testing.T) {
	for _, test := range []struct {
		name       string
		minVersion uint16
		maxVersion uint16
		wantError  bool
	}{
		{name: "rejects-tls11", minVersion: tls.VersionTLS10, maxVersion: tls.VersionTLS11, wantError: true},
		{name: "accepts-tls12", minVersion: tls.VersionTLS12, maxVersion: tls.VersionTLS12},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{MinVersion: test.minVersion, MaxVersion: test.maxVersion}
			server.StartTLS()
			defer server.Close()

			dialer := &mappedDialer{destinations: map[string]string{
				"93.184.216.34:443": server.Listener.Addr().String(),
			}}
			validator, err := NewValidator(ValidatorOptions{
				Resolver: staticResolver{"example.com": {netip.MustParseAddr("93.184.216.34")}},
				Dialer:   dialer,
			})
			if err != nil {
				t.Fatal(err)
			}
			fetcher, err := NewFetcher(FetcherOptions{Validator: validator, Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			fetcher.transport.base.TLSClientConfig.RootCAs = roots
			target, err := NewFetchURL("https://example.com/media")
			if err != nil {
				t.Fatal(err)
			}
			response, err := fetcher.Fetch(t.Context(), FetchRequest{URL: target})
			if test.wantError {
				if err == nil {
					_ = response.Body.Close()
					t.Fatal("TLS version below 1.2 completed a real handshake")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.TLS == nil || response.TLS.Version < tls.VersionTLS12 {
				t.Fatalf("negotiated TLS state = %#v", response.TLS)
			}
		})
	}
}

func TestGuardedTransportRejectsHostAuthorityOverride(t *testing.T) {
	t.Parallel()
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received <- request.Host
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dialer := &mappedDialer{destinations: map[string]string{
		"93.184.216.34:80": server.Listener.Addr().String(),
	}}
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{"media.example": {netip.MustParseAddr("93.184.216.34")}},
		Dialer:   dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{Validator: validator, Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewFetchURL("http://media.example/watch")
	if err != nil {
		t.Fatal(err)
	}
	raw := ""
	if err := target.Use(func(value string) error { raw = value; return nil }); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "internal.example"
	if _, err := fetcher.HTTPClient(t.Context(), 0).Do(request); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("Host authority override error = %v", err)
	}
	select {
	case host := <-received:
		t.Fatalf("authority override reached upstream as Host %q", host)
	default:
	}
}

func TestGuardedTransportRejectsMixedCaseHostHeaderAuthorityOverride(t *testing.T) {
	t.Parallel()
	received := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received <- request.Header.Clone()
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{"media.example": {netip.MustParseAddr("93.184.216.34")}},
		Dialer: &mappedDialer{destinations: map[string]string{
			"93.184.216.34:80": server.Listener.Addr().String(),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := NewFetcher(FetcherOptions{Validator: validator, Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewFetchURL("http://media.example/watch")
	if err != nil {
		t.Fatal(err)
	}
	raw := ""
	if err := target.Use(func(value string) error { raw = value; return nil }); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header["hOsT"] = []string{"internal.example"}
	if _, err := fetcher.HTTPClient(t.Context(), 0).Do(request); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("mixed-case Host authority override error = %v", err)
	}
	select {
	case headers := <-received:
		t.Fatalf("mixed-case Host authority override reached upstream: %#v", headers)
	default:
	}
}
