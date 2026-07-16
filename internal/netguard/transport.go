package netguard

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrResponseTooLarge = errors.New("upstream response exceeds safety limit")

type Limits struct {
	ResponseHeaderBytes int64
	WireBodyBytes       int64
	DecodedBodyBytes    int64
	Duration            time.Duration
}

type FetcherOptions struct {
	Validator *Validator
	Limits    Limits
}

type FetchRequest struct {
	Method       string
	URL          FetchURL
	Header       http.Header
	Body         io.Reader
	MaxRedirects int
}

type Fetcher struct {
	validator *Validator
	transport *limitedTransport
}

type limitedTransport struct {
	validator *Validator
	base      *http.Transport
	limits    Limits
}

func DefaultLimits() Limits {
	return Limits{
		ResponseHeaderBytes: 64 << 10,
		WireBodyBytes:       16 << 20,
		DecodedBodyBytes:    32 << 20,
		Duration:            20 * time.Second,
	}
}

func NewDefaultFetcher() (*Fetcher, error) {
	validator, err := NewValidator(ValidatorOptions{})
	if err != nil {
		return nil, err
	}
	return NewFetcher(FetcherOptions{Validator: validator, Limits: DefaultLimits()})
}

func NewFetcher(options FetcherOptions) (*Fetcher, error) {
	if options.Validator == nil {
		return nil, errors.New("netguard validator is required")
	}
	limits := options.Limits
	if limits.ResponseHeaderBytes <= 0 || limits.WireBodyBytes <= 0 || limits.DecodedBodyBytes <= 0 || limits.Duration <= 0 {
		return nil, errors.New("all netguard limits must be positive")
	}
	base := &http.Transport{
		Proxy:                  nil,
		DialContext:            options.Validator.DialContext,
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: limits.ResponseHeaderBytes,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	return &Fetcher{
		validator: options.Validator,
		transport: &limitedTransport{validator: options.Validator, base: base, limits: limits},
	}, nil
}

func (fetcher *Fetcher) Fetch(ctx context.Context, request FetchRequest) (*http.Response, error) {
	if fetcher == nil || fetcher.transport == nil || request.URL.parsed == nil {
		return nil, errors.New("invalid guarded fetch request")
	}
	if ctx == nil {
		return nil, errors.New("guarded fetch context is required")
	}
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodPost {
		return nil, errors.New("guarded fetch method is not allowed")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, request.URL.requestURL().String(), request.Body)
	if err != nil {
		return nil, fmt.Errorf("create guarded request: %w", err)
	}
	httpRequest.Header = request.Header.Clone()
	response, err := fetcher.HTTPClient(ctx, request.MaxRedirects).Do(httpRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		for _, sentinel := range []error{ErrUnsafeDestination, ErrRedirectLimit, ErrResponseTooLarge, ErrInvalidFetchURL} {
			if errors.Is(err, sentinel) {
				return nil, sentinel
			}
		}
		return nil, errors.New("guarded upstream fetch failed")
	}
	return response, nil
}

// HTTPClient returns a client whose requests are forced onto the supplied
// parse context and whose transport and redirect policy remain guarded.
func (fetcher *Fetcher) HTTPClient(ctx context.Context, maxRedirects int) *http.Client {
	return fetcher.HTTPClientWithRedirect(ctx, maxRedirects, nil)
}

// HTTPClientWithRedirect composes parser-specific stop/observation behavior
// after the mandatory validation policy. Callers cannot replace the safety
// checks when they need to observe a legitimate redirect.
func (fetcher *Fetcher) HTTPClientWithRedirect(ctx context.Context, maxRedirects int, afterValidation func(*http.Request, []*http.Request) error) *http.Client {
	if maxRedirects < 0 {
		maxRedirects = 3
	}
	validate := fetcher.validator.CheckRedirect(maxRedirects)
	deadline := &deadlineState{duration: fetcher.transport.limits.Duration}
	timedTransport := &deadlineTransport{next: fetcher.transport, deadline: deadline}
	return &http.Client{
		Transport: contextualTransport{ctx: ctx, next: timedTransport},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			policyHistory := cloneRedirectHistory(via)
			if request == nil || request.URL == nil {
				return ErrInvalidFetchURL
			}
			redirectParent := request.Context()
			if ctx != nil {
				redirectParent = ctx
			}
			redirectContext, cancelRedirect := deadline.bind(redirectParent)
			*request = *request.Clone(redirectContext)
			if err := validate(request, policyHistory); err != nil {
				cancelRedirect()
				return err
			}
			validatedTarget, err := NewFetchURL(request.URL.String())
			if err != nil {
				cancelRedirect()
				return ErrUnsafeDestination
			}
			validatedFingerprint := validatedTarget.Fingerprint()
			if afterValidation != nil {
				if err := afterValidation(request, cloneRedirectHistory(policyHistory)); err != nil {
					cancelRedirect()
					return err
				}
			}
			finalTarget, err := NewFetchURL(request.URL.String())
			if err != nil || finalTarget.Fingerprint() != validatedFingerprint {
				cancelRedirect()
				return ErrUnsafeDestination
			}
			// The observation hook is intentionally inside the mandatory policy.
			// Revalidate and scrub after it returns so it cannot change the target
			// or restore credentials immediately before net/http sends the hop.
			if err := validate(request, policyHistory); err != nil {
				cancelRedirect()
				return err
			}
			return nil
		},
	}
}

func cloneRedirectHistory(input []*http.Request) []*http.Request {
	cloned := make([]*http.Request, len(input))
	for index, request := range input {
		if request != nil {
			cloned[index] = request.Clone(request.Context())
		}
	}
	return cloned
}

type deadlineState struct {
	duration time.Duration
	once     sync.Once
	deadline time.Time
}

func (state *deadlineState) bind(parent context.Context) (context.Context, context.CancelFunc) {
	state.once.Do(func() {
		state.deadline = time.Now().Add(state.duration)
	})
	return context.WithDeadline(parent, state.deadline)
}

// deadlineTransport is constructed once per guarded HTTP client. Its absolute
// deadline begins at the first RoundTrip and is reused by every redirect hop,
// so DNS validation, dialing, headers, and body reads share one duration.
type deadlineTransport struct {
	next     http.RoundTripper
	deadline *deadlineState
}

func (transport *deadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.next == nil || transport.deadline == nil || request == nil {
		return nil, errors.New("invalid deadline transport")
	}
	ctx, cancel := transport.deadline.bind(request.Context())
	response, err := transport.next.RoundTrip(request.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	if response == nil || response.Body == nil {
		cancel()
		return nil, errors.New("upstream returned an empty HTTP response")
	}
	response.Body = &cancelingBody{body: response.Body, cancel: cancel}
	return response, nil
}

type contextualTransport struct {
	ctx  context.Context
	next http.RoundTripper
}

func (transport contextualTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("nil HTTP request")
	}
	ctx := transport.ctx
	if ctx == nil {
		ctx = request.Context()
	}
	return transport.next.RoundTrip(request.Clone(ctx))
}

func (transport *limitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, ErrInvalidFetchURL
	}
	target, err := NewFetchURL(request.URL.String())
	if err != nil {
		return nil, ErrUnsafeDestination
	}
	if !requestAuthorityMatches(request, target) {
		return nil, ErrUnsafeDestination
	}
	if err := transport.validator.Validate(request.Context(), target); err != nil {
		return nil, err
	}
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	return limitResponse(response, transport.limits)
}

func requestAuthorityMatches(request *http.Request, target FetchURL) bool {
	if request == nil || request.URL == nil || containsHeaderKeyFold(request.Header, "Host") {
		return false
	}
	if request.Host == "" {
		return true
	}
	authority, err := NewFetchURL(request.URL.Scheme + "://" + request.Host + "/")
	if err != nil {
		return false
	}
	left := target.requestURL()
	right := authority.requestURL()
	if left == nil || right == nil || !strings.EqualFold(left.Scheme, right.Scheme) || !strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return effectivePort(left) == effectivePort(right)
}

func containsHeaderKeyFold(header http.Header, target string) bool {
	for key := range header {
		if strings.EqualFold(key, target) {
			return true
		}
	}
	return false
}

func effectivePort(target *url.URL) string {
	if port := target.Port(); port != "" {
		return port
	}
	if strings.EqualFold(target.Scheme, "https") {
		return "443"
	}
	return "80"
}

func limitResponse(response *http.Response, limits Limits) (*http.Response, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("upstream returned an empty HTTP response")
	}
	if headerBytes(response.Header) > limits.ResponseHeaderBytes {
		_ = response.Body.Close()
		return nil, ErrResponseTooLarge
	}
	if response.ContentLength > limits.WireBodyBytes {
		_ = response.Body.Close()
		return nil, ErrResponseTooLarge
	}

	wire := &limitedReadCloser{reader: response.Body, closer: response.Body, remaining: limits.WireBodyBytes}
	var decoded io.ReadCloser = wire
	contentEncoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	if contentEncoding != "" && contentEncoding != "identity" && contentEncoding != "gzip" {
		_ = wire.Close()
		return nil, errors.New("unsupported upstream content encoding")
	}
	if contentEncoding == "gzip" {
		gzipReader, gzipErr := gzip.NewReader(wire)
		if gzipErr != nil {
			_ = wire.Close()
			return nil, fmt.Errorf("decode upstream gzip response: %w", gzipErr)
		}
		decoded = &compoundReadCloser{Reader: gzipReader, closers: []io.Closer{gzipReader, wire}}
		response.Header.Del("Content-Encoding")
		response.ContentLength = -1
	}
	response.Body = &limitedReadCloser{reader: decoded, closer: decoded, remaining: limits.DecodedBodyBytes}
	return response, nil
}

func headerBytes(header http.Header) int64 {
	// The terminating empty line is part of the response header section.
	total := int64(2)
	for key, values := range header {
		for _, value := range values {
			// Count each logical value as its own conservative wire line:
			// "Key: Value\r\n". This neither undercounts repeated fields nor
			// double-counts the delimiter for a single field.
			total += int64(len(key) + 2 + len(value) + 2)
		}
	}
	return total
}

type limitedReadCloser struct {
	reader    io.Reader
	closer    io.Closer
	remaining int64
	exceeded  bool
}

func (reader *limitedReadCloser) Read(buffer []byte) (int, error) {
	if reader.exceeded {
		return 0, ErrResponseTooLarge
	}
	if reader.remaining < 0 {
		reader.exceeded = true
		return 0, ErrResponseTooLarge
	}
	limit := int64(len(buffer))
	if limit > reader.remaining+1 {
		limit = reader.remaining + 1
	}
	count, err := reader.reader.Read(buffer[:limit])
	if int64(count) > reader.remaining {
		allowed := int(reader.remaining)
		reader.remaining = -1
		reader.exceeded = true
		if allowed > 0 {
			return allowed, ErrResponseTooLarge
		}
		return 0, ErrResponseTooLarge
	}
	reader.remaining -= int64(count)
	return count, err
}

func (reader *limitedReadCloser) Close() error {
	if reader.closer == nil {
		return nil
	}
	return reader.closer.Close()
}

type compoundReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (reader *compoundReadCloser) Close() error {
	var result error
	for _, closer := range reader.closers {
		result = errors.Join(result, closer.Close())
	}
	return result
}

type cancelingBody struct {
	body   io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (body *cancelingBody) Read(buffer []byte) (int, error) {
	count, err := body.body.Read(buffer)
	if err != nil {
		body.once.Do(body.cancel)
	}
	return count, err
}

func (body *cancelingBody) Close() error {
	body.once.Do(body.cancel)
	return body.body.Close()
}
