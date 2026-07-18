package netguard

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type proxyMapDialer struct {
	mu        sync.Mutex
	addresses []string
	targets   map[string]string
}

func (dialer *proxyMapDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer.mu.Lock()
	dialer.addresses = append(dialer.addresses, address)
	target := dialer.targets[address]
	dialer.mu.Unlock()
	if target == "" {
		return nil, errors.New("proxy dial target absent from fixture")
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}

func (dialer *proxyMapDialer) snapshot() []string {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return append([]string(nil), dialer.addresses...)
}

func TestVerifyLoopbackProxyEndpointRejectsRemoteAndSecretEndpoints(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"",
		"https://127.0.0.1:18080",
		"http://localhost:18080",
		"http://192.168.1.8:18080",
		"http://user:pass@127.0.0.1:18080",
		"http://127.0.0.1:18080/path",
		"http://127.0.0.1:18080?target=example",
	} {
		if _, err := VerifyLoopbackProxyEndpoint(endpoint); err == nil {
			t.Fatalf("accepted unsafe proxy endpoint %q", endpoint)
		}
	}
	if got, err := VerifyLoopbackProxyEndpoint("http://127.0.0.1:18080/"); err != nil || got != "http://127.0.0.1:18080" {
		t.Fatalf("loopback proxy endpoint = %q, %v", got, err)
	}
}

func TestProxyRejectsPrivateConnectBeforeDial(t *testing.T) {
	t.Parallel()
	dialer := &proxyMapDialer{targets: map[string]string{}}
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{"metadata.example": {netip.MustParseAddr("169.254.169.254")}},
		Dialer:   dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := NewProxy(ProxyOptions{Validator: validator, PolicyFingerprint: "task4-policy"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodConnect, "http://metadata.example:443", nil)
	request.Host = "metadata.example:443"
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("CONNECT status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if got := dialer.snapshot(); len(got) != 0 {
		t.Fatalf("proxy dialed before rejecting private target: %#v", got)
	}
}

func TestProxyPlainHTTPUsesPinnedGuardedFetcherAndScrubsHeaders(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatal("proxy forwarded sensitive helper header")
		}
		writer.Header().Set("X-Upstream", "ok")
		_, _ = writer.Write([]byte("guarded"))
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	dialer := &proxyMapDialer{targets: map[string]string{"93.184.216.34:80": upstreamHost}}
	validator, err := NewValidator(ValidatorOptions{
		Resolver: staticResolver{"media.example": {netip.MustParseAddr("93.184.216.34")}},
		Dialer:   dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := NewProxy(ProxyOptions{Validator: validator, PolicyFingerprint: "task4-policy"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://media.example/video", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Cookie", "session=secret")
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "guarded" || recorder.Header().Get("X-Upstream") != "ok" {
		t.Fatalf("proxy response status=%d body=%q headers=%v", recorder.Code, recorder.Body.String(), recorder.Header())
	}
	if got := dialer.snapshot(); len(got) != 1 || got[0] != "93.184.216.34:80" {
		t.Fatalf("proxy dialed addresses = %#v", got)
	}
}

func TestProxyHealthcheckDoesNotAccessPublicNetworkOrExposeSecrets(t *testing.T) {
	t.Parallel()
	sentinel := "secret-token"
	if _, err := NewProxy(ProxyOptions{PolicyFingerprint: sentinel}); err == nil {
		t.Fatal("proxy accepted sensitive-looking policy fingerprint")
	}
	proxy, err := NewProxy(ProxyOptions{PolicyFingerprint: "task4-policy"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := proxy.Healthcheck(ctx); err != nil {
		t.Fatalf("healthcheck failed: %v", err)
	}
}

func TestProxyTunnelCopyEnforcesByteLimit(t *testing.T) {
	t.Parallel()
	err := copyProxyTunnel(io.Discard, strings.NewReader("abcdef"), 3)
	if !errors.Is(err, ErrProxyRejected) {
		t.Fatalf("copyProxyTunnel error = %v", err)
	}
}
