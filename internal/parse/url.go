package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	neturl "net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
	"golang.org/x/net/idna"
)

var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

type CanonicalResource struct {
	URL         string
	Platform    string
	Host        string
	Fingerprint string
	LogFields   map[string]string
}

func ExtractURL(input string) (string, error) {
	value := strings.TrimSpace(urlPattern.FindString(strings.TrimSpace(input)))
	if value == "" {
		return "", NewError(ErrorInvalidInput, StageInput, "", false)
	}
	return strings.TrimRight(value, "\"'，。！？!?）)]}>》」"), nil
}

func CanonicalizeURL(raw string, descriptor Descriptor) (CanonicalResource, error) {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Hostname() == "" {
		return CanonicalResource{}, NewError(ErrorInvalidInput, StageInput, descriptor.Platform, false)
	}
	parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return CanonicalResource{}, NewError(ErrorInvalidInput, StageInput, descriptor.Platform, false)
	}
	host, err := normalizeHost(parsed.Hostname())
	if err != nil {
		return CanonicalResource{}, NewError(ErrorInvalidInput, StageInput, descriptor.Platform, false)
	}
	if len(descriptor.HostRules) > 0 && !descriptorMatchesHost(descriptor, host) {
		return CanonicalResource{}, NewError(ErrorUnsupported, StageInput, descriptor.Platform, false)
	}
	parsed.Host = hostWithPort(parsed, host)
	parsed.Path = cleanPath(parsed.Path)
	parsed.RawPath = ""
	parsed.RawQuery = canonicalQuery(parsed.RawQuery, descriptor.QueryKeys)
	parsed.ForceQuery = false
	parsed.Fragment = ""
	canonical := parsed.String()
	sum := sha256.Sum256([]byte(canonical))
	return CanonicalResource{
		URL:         canonical,
		Platform:    strings.TrimSpace(descriptor.Platform),
		Host:        host,
		Fingerprint: hex.EncodeToString(sum[:]),
		LogFields: map[string]string{
			"platform":  strings.TrimSpace(descriptor.Platform),
			"scheme":    parsed.Scheme,
			"host":      host,
			"queryKeys": strings.Join(normalizedQueryKeys(descriptor.QueryKeys), ","),
		},
	}, nil
}

func normalizeHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if host == "" || strings.ContainsAny(host, "/:@[]") {
		return "", errors.New("invalid host")
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" {
		return "", errors.New("invalid host")
	}
	return strings.ToLower(ascii), nil
}

func hostWithPort(parsed *neturl.URL, host string) string {
	port := strings.TrimSpace(parsed.Port())
	if port == "" || (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		return host
	}
	return net.JoinHostPort(host, port)
}

func cleanPath(raw string) string {
	if raw == "" {
		return ""
	}
	cleaned := path.Clean("/" + strings.TrimLeft(raw, "/"))
	if cleaned == "/" {
		return "/"
	}
	return cleaned
}

func canonicalQuery(rawQuery string, allowedKeys []string) string {
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range normalizedQueryKeys(allowedKeys) {
		allowed[key] = struct{}{}
	}
	if len(allowed) == 0 {
		return ""
	}
	values, err := neturl.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	filtered := make(neturl.Values)
	seenValues := make(map[string]map[string]struct{})
	for key, candidates := range values {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if _, ok := allowed[normalizedKey]; !ok {
			continue
		}
		seen := seenValues[normalizedKey]
		if seen == nil {
			seen = make(map[string]struct{}, len(candidates))
			seenValues[normalizedKey] = seen
		}
		for _, value := range candidates {
			if value == "" {
				continue
			}
			if _, duplicate := seen[value]; duplicate {
				continue
			}
			seen[value] = struct{}{}
			filtered[normalizedKey] = append(filtered[normalizedKey], value)
		}
	}
	for key := range filtered {
		sort.Strings(filtered[key])
	}
	return filtered.Encode()
}

func normalizedQueryKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func descriptorMatchesHost(descriptor Descriptor, host string) bool {
	for _, rule := range descriptor.HostRules {
		ruleHost, err := normalizeHost(rule.Host)
		if err != nil {
			continue
		}
		if host == ruleHost || (rule.IncludeSubdomains && strings.HasSuffix(host, "."+ruleHost)) {
			return true
		}
	}
	return false
}

type RegistryResolver struct {
	registry *coreparser.Registry
}

func NewRegistryResolver(descriptors []coreparser.Descriptor) (*RegistryResolver, error) {
	registry, err := coreparser.NewRegistry(descriptors)
	if err != nil {
		return nil, err
	}
	return &RegistryResolver{registry: registry}, nil
}

func (resolver *RegistryResolver) ResolveURL(raw string) (Descriptor, error) {
	if resolver == nil || resolver.registry == nil {
		return Descriptor{}, NewError(ErrorInternal, StageInput, "", true)
	}
	descriptor, err := resolver.registry.ResolveURL(raw)
	if err != nil {
		return Descriptor{}, err
	}
	return DescriptorFromParser(descriptor), nil
}

func DescriptorFromParser(descriptor coreparser.Descriptor) Descriptor {
	out := Descriptor{
		Platform:  string(descriptor.Key),
		QueryKeys: append([]string(nil), descriptor.QueryKeys...),
		HostRules: make([]HostRule, 0, len(descriptor.HostRules)),
	}
	for _, rule := range descriptor.HostRules {
		out.HostRules = append(out.HostRules, HostRule{
			Host:              rule.Host,
			IncludeSubdomains: rule.IncludeSubdomains,
		})
	}
	return out
}
