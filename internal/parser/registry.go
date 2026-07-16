package parser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
	"golang.org/x/net/idna"
)

var (
	ErrDuplicateFetch = errors.New("duplicate upstream fetch")
	ErrBudgetExceeded = errors.New("parser request budget exceeded")
)

type UnknownHostError struct{ Host string }

func (err *UnknownHostError) Error() string { return "no parser registered for host " + err.Host }

type InvalidRequestURLError struct{}

func (*InvalidRequestURLError) Error() string { return "parser request URL is invalid" }

type Registry struct {
	descriptors []Descriptor
	keys        map[PlatformKey]int
}

type Catalog struct {
	Platforms []CatalogPlatform `json:"platforms"`
}

type CatalogPlatform struct {
	Key          PlatformKey   `json:"key"`
	DisplayName  string        `json:"displayName"`
	Aliases      []PlatformKey `json:"aliases"`
	HostRules    []HostRule    `json:"hostRules"`
	Capabilities Capability    `json:"capabilities"`
	Priority     int           `json:"priority"`
	QueryKeys    []string      `json:"queryKeys"`
	SupportsID   bool          `json:"supportsId"`
	MaxRequests  int           `json:"maxRequests"`
	MaxRedirects int           `json:"maxRedirects"`
}

func NewRegistry(input []Descriptor) (*Registry, error) {
	descriptors := make([]Descriptor, len(input))
	for index := range input {
		descriptors[index] = cloneDescriptor(input[index])
	}
	sort.SliceStable(descriptors, func(left, right int) bool {
		if descriptors[left].Priority != descriptors[right].Priority {
			return descriptors[left].Priority < descriptors[right].Priority
		}
		return descriptors[left].Key < descriptors[right].Key
	})
	registry := &Registry{descriptors: descriptors, keys: make(map[PlatformKey]int)}
	hostOwners := make([]struct {
		rule HostRule
		key  PlatformKey
	}, 0)
	for index := range registry.descriptors {
		descriptor := &registry.descriptors[index]
		validCapabilities := CapabilityVideo | CapabilityGallery | CapabilityAudio | CapabilityLivePhoto | CapabilityM3U8
		if !validPlatformKey(descriptor.Key) || descriptor.New == nil || len(descriptor.HostRules) == 0 ||
			strings.TrimSpace(descriptor.DisplayName) == "" || strings.TrimSpace(descriptor.DisplayName) != descriptor.DisplayName ||
			strings.ContainsAny(descriptor.DisplayName, "\x00\r\n") || descriptor.Capabilities == 0 ||
			descriptor.Capabilities&^validCapabilities != 0 || descriptor.Priority < 0 ||
			descriptor.MaxRequests <= 0 || descriptor.MaxRedirects < 0 {
			return nil, fmt.Errorf("invalid descriptor %q", descriptor.Key)
		}
		allKeys := append([]PlatformKey{descriptor.Key}, descriptor.Aliases...)
		for _, key := range allKeys {
			if !validPlatformKey(key) {
				return nil, fmt.Errorf("invalid platform key or alias %q", key)
			}
			if owner, exists := registry.keys[key]; exists {
				return nil, fmt.Errorf("platform key %q is shared by %q and %q", key, registry.descriptors[owner].Key, descriptor.Key)
			}
			registry.keys[key] = index
		}
		seenQuery := make(map[string]struct{}, len(descriptor.QueryKeys))
		for queryIndex, queryKey := range descriptor.QueryKeys {
			queryKey = strings.ToLower(strings.TrimSpace(queryKey))
			if queryKey == "" || strings.ContainsAny(queryKey, "&=%") {
				return nil, fmt.Errorf("invalid query key for %q", descriptor.Key)
			}
			if _, duplicate := seenQuery[queryKey]; duplicate {
				return nil, fmt.Errorf("duplicate query key %q for %q", queryKey, descriptor.Key)
			}
			seenQuery[queryKey] = struct{}{}
			descriptor.QueryKeys[queryIndex] = queryKey
		}
		sort.Strings(descriptor.QueryKeys)
		seenHosts := make(map[string]struct{}, len(descriptor.HostRules))
		for ruleIndex, rule := range descriptor.HostRules {
			host, err := normalizeRegistryHost(rule.Host)
			if err != nil {
				return nil, fmt.Errorf("invalid host rule for %q: %w", descriptor.Key, err)
			}
			if _, duplicate := seenHosts[host]; duplicate {
				return nil, fmt.Errorf("duplicate host rule %q for %q", host, descriptor.Key)
			}
			seenHosts[host] = struct{}{}
			rule.Host = host
			descriptor.HostRules[ruleIndex] = rule
			for _, owner := range hostOwners {
				if owner.key != descriptor.Key && hostRulesOverlap(owner.rule, rule) {
					return nil, fmt.Errorf("host rule %q is ambiguous between %q and %q", host, owner.key, descriptor.Key)
				}
			}
			hostOwners = append(hostOwners, struct {
				rule HostRule
				key  PlatformKey
			}{rule: rule, key: descriptor.Key})
		}
	}
	return registry, nil
}

func (registry *Registry) Descriptor(key PlatformKey) (Descriptor, bool) {
	if registry == nil {
		return Descriptor{}, false
	}
	index, ok := registry.keys[key]
	if !ok {
		return Descriptor{}, false
	}
	return cloneDescriptor(registry.descriptors[index]), true
}

func (registry *Registry) ResolveURL(raw string) (Descriptor, error) {
	target, err := netguard.NewFetchURL(strings.TrimSpace(raw))
	if err != nil {
		return Descriptor{}, &InvalidRequestURLError{}
	}
	parsed, err := url.Parse(target.Safe().String())
	if err != nil || parsed == nil || parsed.Hostname() == "" {
		return Descriptor{}, &InvalidRequestURLError{}
	}
	host, err := normalizeRegistryHost(parsed.Hostname())
	if err != nil {
		return Descriptor{}, &UnknownHostError{}
	}
	for _, descriptor := range registry.descriptors {
		for _, rule := range descriptor.HostRules {
			if ruleMatchesHost(rule, host) {
				return cloneDescriptor(descriptor), nil
			}
		}
	}
	return Descriptor{}, &UnknownHostError{Host: host}
}

// NormalizeFetchURL applies the descriptor's explicit query allowlist and
// returns an opaque request target. Tracking and unknown query fields are
// removed; duplicate values are normalized deterministically.
func NormalizeFetchURL(descriptor Descriptor, raw string) (netguard.FetchURL, error) {
	normalized, err := normalizeAllowedQuery(descriptor, raw)
	if err != nil {
		return netguard.FetchURL{}, err
	}
	return netguard.NewFetchURL(normalized)
}

func normalizeAllowedQuery(descriptor Descriptor, raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Hostname() == "" {
		return "", errors.New("invalid parser request URL")
	}
	host, err := normalizeRegistryHost(parsed.Hostname())
	if err != nil {
		return "", errors.New("invalid parser request host")
	}
	matched := false
	for _, rule := range descriptor.HostRules {
		if ruleMatchesHost(rule, host) {
			matched = true
			break
		}
	}
	if len(descriptor.HostRules) > 0 && !matched {
		return "", errors.New("parser request host does not match descriptor")
	}
	allowed := make(map[string]struct{}, len(descriptor.QueryKeys))
	for _, key := range descriptor.QueryKeys {
		allowed[strings.ToLower(key)] = struct{}{}
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", errors.New("invalid parser request query")
	}
	filtered := make(url.Values)
	seenValues := make(map[string]map[string]struct{}, len(values))
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
	parsed.RawQuery = filtered.Encode()
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (registry *Registry) CatalogSnapshot() Catalog {
	if registry == nil {
		return Catalog{}
	}
	catalog := Catalog{Platforms: make([]CatalogPlatform, 0, len(registry.descriptors))}
	for _, descriptor := range registry.descriptors {
		catalog.Platforms = append(catalog.Platforms, CatalogPlatform{
			Key: descriptor.Key, DisplayName: descriptor.DisplayName,
			Aliases:      append([]PlatformKey(nil), descriptor.Aliases...),
			HostRules:    append([]HostRule(nil), descriptor.HostRules...),
			Capabilities: descriptor.Capabilities, Priority: descriptor.Priority,
			QueryKeys:  append([]string(nil), descriptor.QueryKeys...),
			SupportsID: descriptor.SupportsID, MaxRequests: descriptor.MaxRequests,
			MaxRedirects: descriptor.MaxRedirects,
		})
	}
	return catalog
}

func (catalog Catalog) HostRuleCount() int {
	count := 0
	for _, platform := range catalog.Platforms {
		count += len(platform.HostRules)
	}
	return count
}

func (catalog Catalog) SupportsIDCount() int {
	count := 0
	for _, platform := range catalog.Platforms {
		if platform.SupportsID {
			count++
		}
	}
	return count
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Aliases = append([]PlatformKey(nil), descriptor.Aliases...)
	descriptor.HostRules = append([]HostRule(nil), descriptor.HostRules...)
	descriptor.QueryKeys = append([]string(nil), descriptor.QueryKeys...)
	return descriptor
}

func normalizeRegistryHost(raw string) (string, error) {
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

func ruleMatchesHost(rule HostRule, host string) bool {
	return host == rule.Host || (rule.IncludeSubdomains && strings.HasSuffix(host, "."+rule.Host))
}

func hostRulesOverlap(left, right HostRule) bool {
	return ruleMatchesHost(left, right.Host) || ruleMatchesHost(right, left.Host)
}

type BudgetOptions struct {
	MaxRequests  int
	MaxRedirects int
	Duration     time.Duration
	Clock        func() time.Time
}

type RequestBudget struct {
	mu           sync.Mutex
	maxRequests  int
	maxRedirects int
	requests     int
	redirects    int
	seen         map[[32]byte]struct{}
	deadline     time.Time
	ctxDeadline  time.Time
	clock        func() time.Time
}

func NewRequestBudget(options BudgetOptions) (*RequestBudget, error) {
	if options.MaxRequests <= 0 || options.MaxRedirects < 0 || options.Duration <= 0 {
		return nil, errors.New("invalid parser request budget")
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	wallStart := time.Now()
	logicalStart := clock()
	return &RequestBudget{
		maxRequests: options.MaxRequests, maxRedirects: options.MaxRedirects,
		seen: make(map[[32]byte]struct{}), deadline: logicalStart.Add(options.Duration),
		ctxDeadline: wallStart.Add(options.Duration), clock: clock,
	}, nil
}

// BindContext derives a context capped by the budget's original absolute
// deadline. Repeated calls consume the remaining duration instead of starting
// a fresh timeout, so every adapter request and redirect shares one wall-clock
// envelope.
func (budget *RequestBudget) BindContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if budget == nil || parent == nil {
		return nil, nil, ErrBudgetExceeded
	}
	budget.mu.Lock()
	remaining := budget.deadline.Sub(budget.clock())
	contextDeadline := budget.ctxDeadline
	budget.mu.Unlock()
	if remaining <= 0 {
		return nil, nil, ErrBudgetExceeded
	}
	if logicalDeadline := time.Now().Add(remaining); logicalDeadline.Before(contextDeadline) {
		contextDeadline = logicalDeadline
	}
	ctx, cancel := context.WithDeadline(parent, contextDeadline)
	return ctx, cancel, nil
}

func (budget *RequestBudget) AllowFetch(target netguard.FetchURL) error {
	if budget == nil {
		return ErrBudgetExceeded
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if !budget.clock().Before(budget.deadline) || budget.requests >= budget.maxRequests {
		return ErrBudgetExceeded
	}
	fingerprint := target.Fingerprint()
	if _, exists := budget.seen[fingerprint]; exists {
		return ErrDuplicateFetch
	}
	budget.seen[fingerprint] = struct{}{}
	budget.requests++
	return nil
}

func (budget *RequestBudget) AllowRedirect() error {
	if budget == nil {
		return ErrBudgetExceeded
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if !budget.clock().Before(budget.deadline) || budget.redirects >= budget.maxRedirects {
		return ErrBudgetExceeded
	}
	budget.redirects++
	return nil
}
