package netguard

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

var ErrInvalidFetchURL = errors.New("invalid fetch URL")

// FetchURL is an intentionally opaque, validated request target. It does not
// implement fmt.Stringer and refuses serialization so query material cannot be
// included in logs or evidence by accident.
type FetchURL struct {
	parsed *url.URL
}

type SafeURL struct {
	value string
}

// Format deliberately ignores every verb, flag, width, and precision. Without
// an explicit Formatter, fmt recursively inspects FetchURL's private url.URL
// and can disclose RawQuery even though FetchURL has no String method.
func (target FetchURL) Format(state fmt.State, _ rune) {
	label := "<invalid-fetch-url>"
	if target.parsed != nil {
		label = "<opaque-fetch-url>"
	}
	_, _ = state.Write([]byte(label))
}

func NewFetchURL(raw string) (FetchURL, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "\r\n\x00") {
		return FetchURL{}, ErrInvalidFetchURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.Host == "" {
		return FetchURL{}, ErrInvalidFetchURL
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return FetchURL{}, ErrInvalidFetchURL
	}
	if parsed.User != nil {
		return FetchURL{}, ErrInvalidFetchURL
	}
	host, err := canonicalHost(parsed.Hostname())
	if err != nil {
		return FetchURL{}, ErrInvalidFetchURL
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port != "" && port != "80") ||
		(parsed.Scheme == "https" && port != "" && port != "443") {
		return FetchURL{}, ErrInvalidFetchURL
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil && !isPublicAddress(address) {
		return FetchURL{}, ErrInvalidFetchURL
	}
	parsed.Host = host
	if address, parseErr := netip.ParseAddr(host); parseErr == nil && address.Is6() {
		parsed.Host = "[" + host + "]"
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	}
	parsed.Fragment = ""
	return FetchURL{parsed: parsed}, nil
}

func (target FetchURL) Safe() SafeURL {
	if target.parsed == nil {
		return SafeURL{}
	}
	copyURL := *target.parsed
	copyURL.RawQuery = ""
	copyURL.ForceQuery = false
	copyURL.Fragment = ""
	return SafeURL{value: copyURL.String()}
}

func (safe SafeURL) String() string { return safe.value }

func (safe SafeURL) MarshalJSON() ([]byte, error) { return json.Marshal(safe.value) }

func (target FetchURL) MarshalJSON() ([]byte, error) {
	return nil, errors.New("fetch URL cannot be serialized; use SafeURL")
}

func (target FetchURL) MarshalText() ([]byte, error) {
	return nil, errors.New("fetch URL cannot be serialized; use SafeURL")
}

// Use exposes the request form only to an explicit consumer. FetchURL remains
// non-formatable and non-serializable at API, log, cache, and evidence edges.
func (target FetchURL) Use(consumer func(string) error) error {
	if target.parsed == nil {
		return ErrInvalidFetchURL
	}
	if consumer == nil {
		return errors.New("fetch URL consumer is required")
	}
	return consumer(target.parsed.String())
}

func (target FetchURL) Valid() bool { return target.parsed != nil }

func (target FetchURL) fingerprint() [32]byte {
	if target.parsed == nil {
		return [32]byte{}
	}
	return sha256.Sum256([]byte(target.parsed.String()))
}

// Fingerprint provides a non-reversible identity for duplicate-request
// accounting. It deliberately does not expose the request URL.
func (target FetchURL) Fingerprint() [32]byte { return target.fingerprint() }

func (target FetchURL) requestURL() *url.URL {
	if target.parsed == nil {
		return nil
	}
	copyURL := *target.parsed
	return &copyURL
}

func canonicalHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if host == "" || strings.ContainsAny(host, "\x00/\\") {
		return "", ErrInvalidFetchURL
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap().String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", ErrInvalidFetchURL
	}
	return strings.ToLower(ascii), nil
}
