package sandbox

import (
	"errors"
	"fmt"
	"strings"

	"github.com/1136623363/watermark-go/internal/netguard"
)

var (
	ErrSandboxUnverified = errors.New("parser sandbox is not verified")
	ErrUnsafePayload     = errors.New("parser sandbox payload rejected")
)

const MaxJobPayloadBytes = 256 << 10

type Identity struct {
	Role              string
	RunID             string
	ImageDigest       string
	SocketPath        string
	ProxyEndpoint     string
	PolicyFingerprint string
}

type VerifiedProxy struct {
	endpoint string
}

func NewVerifiedProxy(endpoint string) (VerifiedProxy, error) {
	verified, err := netguard.VerifyLoopbackProxyEndpoint(endpoint)
	if err != nil {
		return VerifiedProxy{}, err
	}
	return VerifiedProxy{endpoint: verified}, nil
}

func (proxy VerifiedProxy) VerifiedEndpoint() (string, bool) {
	if proxy.endpoint == "" {
		return "", false
	}
	return proxy.endpoint, true
}

type Client struct {
	identity Identity
	proxy    VerifiedProxy
}

func NewClient(identity Identity, expectedRole string) (*Client, error) {
	proxy, err := identity.Validate(expectedRole)
	if err != nil {
		return nil, err
	}
	return &Client{identity: identity, proxy: proxy}, nil
}

func (client *Client) GuardProxy() VerifiedProxy {
	if client == nil {
		return VerifiedProxy{}
	}
	return client.proxy
}

func (identity Identity) Validate(expectedRole string) (VerifiedProxy, error) {
	expectedRole = strings.TrimSpace(expectedRole)
	if expectedRole == "" || strings.TrimSpace(identity.Role) != expectedRole {
		return VerifiedProxy{}, ErrSandboxUnverified
	}
	for _, value := range []string{
		identity.Role, identity.RunID, identity.ImageDigest, identity.SocketPath,
		identity.ProxyEndpoint, identity.PolicyFingerprint,
	} {
		if strings.TrimSpace(value) == "" || containsSensitiveMarker(value) {
			return VerifiedProxy{}, ErrSandboxUnverified
		}
	}
	if !strings.HasPrefix(identity.SocketPath, "/") || strings.ContainsAny(identity.SocketPath, "\x00\r\n") {
		return VerifiedProxy{}, ErrSandboxUnverified
	}
	proxy, err := NewVerifiedProxy(identity.ProxyEndpoint)
	if err != nil {
		return VerifiedProxy{}, fmt.Errorf("%w: %v", ErrSandboxUnverified, err)
	}
	return proxy, nil
}

type Job struct {
	Kind    string
	payload []byte
}

func NewJob(kind string, payload []byte) (Job, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" || strings.ContainsAny(kind, "\x00\r\n/\\") {
		return Job{}, ErrUnsafePayload
	}
	if len(payload) == 0 || len(payload) > MaxJobPayloadBytes || containsSensitiveMarker(string(payload)) {
		return Job{}, ErrUnsafePayload
	}
	return Job{Kind: kind, payload: append([]byte(nil), payload...)}, nil
}

func (job Job) UsePayload(consumer func([]byte) error) error {
	if consumer == nil {
		return errors.New("parser sandbox payload consumer is required")
	}
	return consumer(append([]byte(nil), job.payload...))
}

func (Job) String() string   { return "[opaque-parser-sandbox-job]" }
func (Job) GoString() string { return "sandbox.Job([opaque])" }
func (Job) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[opaque-parser-sandbox-job]"))
}

func containsSensitiveMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization", "cookie", "password", "passwd", "secret", "token", "credential", "mysql", "redis", "dsn"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
