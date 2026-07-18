package netguard

import (
	"errors"
	"net/http"
	"strings"
)

var (
	ErrAuthorityDenied    = errors.New("network authority denied")
	ErrAuthorityAmbiguous = errors.New("network authority is ambiguous")
)

type AuthorityPurpose string

const (
	PurposeInputShare       AuthorityPurpose = "input_share"
	PurposeMetadataAPI      AuthorityPurpose = "metadata_api"
	PurposeSessionBootstrap AuthorityPurpose = "session_bootstrap"
	PurposeSessionConsumer  AuthorityPurpose = "session_consumer"
	PurposeMediaCandidate   AuthorityPurpose = "media_candidate"
)

type AuthorityOwner struct {
	Owner string
	Rules []AuthorityRule
}

type AuthorityRule struct {
	Purpose               AuthorityPurpose
	Host                  string
	IncludeSubdomains     bool
	DynamicPublic         bool
	AllowSensitiveHeaders bool
}

type AuthorityRequest struct {
	Owner   string
	Purpose AuthorityPurpose
	URL     FetchURL
	Header  http.Header
}

type AuthorityDecision struct {
	owner   string
	purpose AuthorityPurpose
	host    string
	header  http.Header
}

type AuthorityRegistry struct {
	owners map[string][]AuthorityRule
}

func NewAuthorityRegistry(owners []AuthorityOwner) (*AuthorityRegistry, error) {
	registry := &AuthorityRegistry{owners: make(map[string][]AuthorityRule, len(owners))}
	seen := make([]registeredAuthorityRule, 0)
	for _, owner := range owners {
		name := strings.ToLower(strings.TrimSpace(owner.Owner))
		if name == "" {
			return nil, ErrAuthorityDenied
		}
		if _, exists := registry.owners[name]; exists {
			return nil, ErrAuthorityAmbiguous
		}
		rules := make([]AuthorityRule, 0, len(owner.Rules))
		for _, rule := range owner.Rules {
			normalized, err := normalizeAuthorityRule(rule)
			if err != nil {
				return nil, err
			}
			for _, previous := range seen {
				if previous.owner != name && authorityRulesOverlap(previous.rule, normalized) {
					return nil, ErrAuthorityAmbiguous
				}
			}
			seen = append(seen, registeredAuthorityRule{owner: name, rule: normalized})
			rules = append(rules, normalized)
		}
		registry.owners[name] = rules
	}
	return registry, nil
}

func (registry *AuthorityRegistry) Authorize(request AuthorityRequest) (AuthorityDecision, error) {
	if registry == nil || request.URL.parsed == nil {
		return AuthorityDecision{}, ErrAuthorityDenied
	}
	owner := strings.ToLower(strings.TrimSpace(request.Owner))
	if owner == "" || request.Purpose == "" {
		return AuthorityDecision{}, ErrAuthorityDenied
	}
	host, err := canonicalHost(request.URL.parsed.Hostname())
	if err != nil {
		return AuthorityDecision{}, ErrAuthorityDenied
	}
	for _, rule := range registry.owners[owner] {
		if rule.Purpose != request.Purpose {
			continue
		}
		if !authorityRuleMatchesHost(rule, host) {
			continue
		}
		if authorityHostClaimedByOtherPurpose(registry.owners[owner], rule, host) {
			return AuthorityDecision{}, ErrAuthorityDenied
		}
		header := request.Header.Clone()
		if !rule.AllowSensitiveHeaders {
			stripCrossOriginSensitiveHeaders(header)
		}
		return AuthorityDecision{
			owner: owner, purpose: request.Purpose, host: host, header: header,
		}, nil
	}
	return AuthorityDecision{}, ErrAuthorityDenied
}

func (registry *AuthorityRegistry) AuthorizeRedirect(previous AuthorityDecision, next AuthorityRequest) (AuthorityDecision, error) {
	if strings.TrimSpace(previous.owner) == "" || previous.purpose == "" {
		return AuthorityDecision{}, ErrAuthorityDenied
	}
	if strings.ToLower(strings.TrimSpace(next.Owner)) != previous.owner || next.Purpose != previous.purpose {
		return AuthorityDecision{}, ErrAuthorityDenied
	}
	return registry.Authorize(next)
}

func (decision AuthorityDecision) SanitizedHeader() http.Header {
	return decision.header.Clone()
}

func (decision AuthorityDecision) Owner() string { return decision.owner }

func (decision AuthorityDecision) Purpose() AuthorityPurpose { return decision.purpose }

func (decision AuthorityDecision) Host() string { return decision.host }

type registeredAuthorityRule struct {
	owner string
	rule  AuthorityRule
}

func normalizeAuthorityRule(rule AuthorityRule) (AuthorityRule, error) {
	if rule.Purpose == "" {
		return AuthorityRule{}, ErrAuthorityDenied
	}
	if rule.DynamicPublic {
		if rule.Purpose != PurposeMediaCandidate || strings.TrimSpace(rule.Host) != "" || rule.IncludeSubdomains || rule.AllowSensitiveHeaders {
			return AuthorityRule{}, ErrAuthorityDenied
		}
		return rule, nil
	}
	host, err := canonicalHost(rule.Host)
	if err != nil {
		return AuthorityRule{}, ErrAuthorityDenied
	}
	rule.Host = host
	return rule, nil
}

func authorityRulesOverlap(left, right AuthorityRule) bool {
	if left.DynamicPublic || right.DynamicPublic || left.Purpose != right.Purpose {
		return false
	}
	return authorityRuleMatchesHost(left, right.Host) || authorityRuleMatchesHost(right, left.Host)
}

func authorityHostClaimedByOtherPurpose(rules []AuthorityRule, matched AuthorityRule, host string) bool {
	if matched.DynamicPublic {
		for _, rule := range rules {
			if rule.DynamicPublic || rule.Purpose == matched.Purpose {
				continue
			}
			if rule.Host == host {
				return true
			}
		}
		return false
	}
	if matched.Host == host {
		return false
	}
	for _, rule := range rules {
		if rule.DynamicPublic || rule.Purpose == matched.Purpose {
			continue
		}
		if rule.Host == host {
			return true
		}
	}
	return false
}

func authorityRuleMatchesHost(rule AuthorityRule, host string) bool {
	if rule.DynamicPublic {
		return true
	}
	return host == rule.Host || (rule.IncludeSubdomains && strings.HasSuffix(host, "."+rule.Host))
}
