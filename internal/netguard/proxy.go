package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrProxyRejected = errors.New("netguard proxy rejected request")

type StaticProxyEndpoint struct {
	Endpoint string
	Verified bool
}

func (endpoint StaticProxyEndpoint) VerifiedEndpoint() (string, bool) {
	if !endpoint.Verified {
		return "", false
	}
	verified, err := VerifyLoopbackProxyEndpoint(endpoint.Endpoint)
	return verified, err == nil
}

func VerifyLoopbackProxyEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed == nil || parsed.Scheme != "http" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid netguard proxy endpoint")
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return "", errors.New("netguard proxy must use a loopback endpoint")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("netguard proxy endpoint cannot contain a path")
	}
	if parsed.Port() == "" {
		return "", errors.New("netguard proxy port is required")
	}
	if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
		return "", errors.New("invalid netguard proxy address")
	}
	parsed.Path = ""
	return parsed.String(), nil
}

type ProxyOptions struct {
	Validator         *Validator
	Limits            Limits
	PolicyFingerprint string
	TunnelBytes       int64
	TunnelDuration    time.Duration
}

type Proxy struct {
	validator         *Validator
	fetcher           *Fetcher
	policyFingerprint string
	tunnelBytes       int64
	tunnelDuration    time.Duration
}

func NewProxy(options ProxyOptions) (*Proxy, error) {
	if strings.TrimSpace(options.PolicyFingerprint) == "" || containsSensitiveMarker(options.PolicyFingerprint) {
		return nil, errors.New("netguard proxy policy fingerprint is required")
	}
	validator := options.Validator
	if validator == nil {
		created, err := NewValidator(ValidatorOptions{})
		if err != nil {
			return nil, err
		}
		validator = created
	}
	limits := options.Limits
	if limits.ResponseHeaderBytes == 0 && limits.WireBodyBytes == 0 && limits.DecodedBodyBytes == 0 && limits.Duration == 0 {
		limits = DefaultLimits()
	}
	fetcher, err := NewFetcher(FetcherOptions{Validator: validator, Limits: limits})
	if err != nil {
		return nil, err
	}
	tunnelBytes := options.TunnelBytes
	if tunnelBytes <= 0 {
		tunnelBytes = 64 << 20
	}
	tunnelDuration := options.TunnelDuration
	if tunnelDuration <= 0 {
		tunnelDuration = limits.Duration
	}
	return &Proxy{
		validator:         validator,
		fetcher:           fetcher,
		policyFingerprint: strings.TrimSpace(options.PolicyFingerprint),
		tunnelBytes:       tunnelBytes,
		tunnelDuration:    tunnelDuration,
	}, nil
}

func (proxy *Proxy) PolicyFingerprint() string {
	if proxy == nil {
		return ""
	}
	return proxy.policyFingerprint
}

func (proxy *Proxy) Healthcheck(ctx context.Context) error {
	if ctx == nil {
		return errors.New("netguard proxy healthcheck context is required")
	}
	if proxy == nil || proxy.validator == nil || proxy.fetcher == nil || strings.TrimSpace(proxy.policyFingerprint) == "" {
		return errors.New("netguard proxy is not initialized")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (proxy *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if proxy == nil || request == nil {
		http.Error(writer, "netguard proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	if request.Method == http.MethodConnect {
		proxy.serveConnect(writer, request)
		return
	}
	proxy.servePlainHTTP(writer, request)
}

func (proxy *Proxy) servePlainHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL == nil || !request.URL.IsAbs() {
		http.Error(writer, "netguard proxy requires absolute-form request", http.StatusBadRequest)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.Error(writer, "netguard proxy method rejected", http.StatusMethodNotAllowed)
		return
	}
	target, err := NewFetchURL(request.URL.String())
	if err != nil {
		http.Error(writer, "netguard proxy target rejected", http.StatusForbidden)
		return
	}
	response, err := proxy.fetcher.Fetch(request.Context(), FetchRequest{
		Method:       request.Method,
		URL:          target,
		Header:       proxyRequestHeader(request.Header),
		MaxRedirects: 3,
	})
	if err != nil {
		http.Error(writer, "netguard proxy fetch rejected", http.StatusForbidden)
		return
	}
	defer response.Body.Close()
	copyProxyResponseHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.StatusCode)
	if request.Method != http.MethodHead {
		_, _ = io.Copy(writer, response.Body)
	}
}

func (proxy *Proxy) serveConnect(writer http.ResponseWriter, request *http.Request) {
	hostPort := strings.TrimSpace(request.Host)
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil || host == "" || port == "" {
		http.Error(writer, "netguard proxy CONNECT target rejected", http.StatusForbidden)
		return
	}
	target, err := NewFetchURL("https://" + hostPort + "/")
	if err != nil {
		http.Error(writer, "netguard proxy CONNECT target rejected", http.StatusForbidden)
		return
	}
	if err := proxy.validator.Validate(request.Context(), target); err != nil {
		http.Error(writer, "netguard proxy CONNECT target rejected", http.StatusForbidden)
		return
	}
	upstream, err := proxy.validator.DialContext(request.Context(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		http.Error(writer, "netguard proxy CONNECT target rejected", http.StatusForbidden)
		return
	}
	defer upstream.Close()
	if deadline := time.Now().Add(proxy.tunnelDuration); proxy.tunnelDuration > 0 {
		_ = upstream.SetDeadline(deadline)
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		http.Error(writer, "netguard proxy hijack unavailable", http.StatusServiceUnavailable)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if deadline := time.Now().Add(proxy.tunnelDuration); proxy.tunnelDuration > 0 {
		_ = client.SetDeadline(deadline)
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	clientReader := io.Reader(client)
	if buffered.Reader.Buffered() > 0 {
		clientReader = io.MultiReader(buffered, client)
	}
	done := make(chan struct{}, 2)
	go func() {
		_ = copyProxyTunnel(upstream, clientReader, proxy.tunnelBytes)
		done <- struct{}{}
	}()
	go func() {
		_ = copyProxyTunnel(client, upstream, proxy.tunnelBytes)
		done <- struct{}{}
	}()
	<-done
}

func proxyRequestHeader(input http.Header) http.Header {
	output := make(http.Header)
	for key, values := range input {
		if isHopByHopHeader(key) || isSensitiveRedirectHeader(key) {
			continue
		}
		for _, value := range values {
			output.Add(key, value)
		}
	}
	return output
}

func copyProxyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func copyProxyTunnel(dst io.Writer, src io.Reader, limit int64) error {
	if limit <= 0 {
		return ErrProxyRejected
	}
	count, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if count > limit {
		return fmt.Errorf("%w: tunnel byte limit exceeded", ErrProxyRejected)
	}
	return err
}

func containsSensitiveMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"password", "passwd", "secret", "token", "cookie", "credential", "mysql", "redis", "dsn"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
