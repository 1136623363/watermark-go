package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrUnsafeDestination = errors.New("unsafe network destination")
	ErrRedirectLimit     = errors.New("redirect limit exceeded")
)

type Resolver interface {
	LookupNetIP(context.Context, string) ([]netip.Addr, error)
}

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type ValidatorOptions struct {
	Resolver Resolver
	Dialer   Dialer
}

type Validator struct {
	resolver Resolver
	dialer   Dialer
}

type systemResolver struct{ resolver *net.Resolver }

func (resolver systemResolver) LookupNetIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return resolver.resolver.LookupNetIP(ctx, "ip", host)
}

func NewValidator(options ValidatorOptions) (*Validator, error) {
	resolver := options.Resolver
	if resolver == nil {
		resolver = systemResolver{resolver: net.DefaultResolver}
	}
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return &Validator{resolver: resolver, dialer: dialer}, nil
}

func (validator *Validator) Validate(ctx context.Context, target FetchURL) error {
	if validator == nil || validator.resolver == nil || target.parsed == nil {
		return ErrInvalidFetchURL
	}
	if ctx == nil {
		return errors.New("nil validation context")
	}
	return validator.validateHost(ctx, target.parsed.Hostname())
}

func (validator *Validator) validateHost(ctx context.Context, rawHost string) error {
	host, err := canonicalHost(rawHost)
	if err != nil {
		return ErrUnsafeDestination
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		if !isPublicAddress(address) {
			return ErrUnsafeDestination
		}
		return nil
	}
	addresses, err := validator.resolver.LookupNetIP(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("resolve destination: empty address set")
	}
	for _, address := range addresses {
		if !isPublicAddress(address) {
			return ErrUnsafeDestination
		}
	}
	return nil
}

func (validator *Validator) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if validator == nil || validator.resolver == nil || validator.dialer == nil {
		return nil, errors.New("invalid validator")
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, ErrUnsafeDestination
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrUnsafeDestination
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, ErrUnsafeDestination
	}
	host, err = canonicalHost(host)
	if err != nil {
		return nil, ErrUnsafeDestination
	}
	var addresses []netip.Addr
	if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = []netip.Addr{parsed}
	} else {
		addresses, err = validator.resolver.LookupNetIP(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve dial destination: %w", err)
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("resolve dial destination: empty address set")
	}
	validated := make([]netip.Addr, 0, len(addresses))
	for _, candidate := range addresses {
		candidate = candidate.Unmap()
		if !isPublicAddress(candidate) {
			return nil, ErrUnsafeDestination
		}
		validated = append(validated, candidate)
	}
	var joined error
	for _, candidate := range validated {
		pinned := net.JoinHostPort(candidate.String(), port)
		connection, dialErr := validator.dialer.DialContext(ctx, network, pinned)
		if dialErr == nil {
			return connection, nil
		}
		joined = errors.Join(joined, dialErr)
	}
	return nil, fmt.Errorf("dial validated destination: %w", joined)
}

func (validator *Validator) CheckRedirect(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if maxRedirects < 0 || len(via) > maxRedirects {
			return ErrRedirectLimit
		}
		if request == nil || request.URL == nil {
			return ErrInvalidFetchURL
		}
		target, err := NewFetchURL(request.URL.String())
		if err != nil {
			return ErrUnsafeDestination
		}
		if err := validator.Validate(request.Context(), target); err != nil {
			return err
		}
		if len(via) > 0 && !sameOrigin(via[len(via)-1].URL, request.URL) {
			stripCrossOriginSensitiveHeaders(request.Header)
		}
		return nil
	}
}

func stripCrossOriginSensitiveHeaders(header http.Header) {
	for key := range header {
		if isSensitiveRedirectHeader(key) {
			// Header.Del canonicalizes its argument before deleting. Callers can
			// assign directly to the map, so delete the exact observed key to
			// prevent mixed-case spellings from surviving the scrub.
			delete(header, key)
		}
	}
}

func isSensitiveRedirectHeader(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "cookie2", "referer", "origin", "x-api-key", "api-key":
		return true
	}
	for _, marker := range []string{"token", "session", "credential", "csrf", "xsrf", "secret", "apikey", "api-key", "auth", "signature"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

var deniedAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func isPublicAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range deniedAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
