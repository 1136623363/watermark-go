package netguard

import (
	"errors"
	"net/http"
	"testing"
)

func mustAuthorityURL(t *testing.T, raw string) FetchURL {
	t.Helper()
	target, err := NewFetchURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestPurposeScopedOutboundAuthority(t *testing.T) {
	t.Parallel()
	registry, err := NewAuthorityRegistry([]AuthorityOwner{{
		Owner: "bilibili",
		Rules: []AuthorityRule{
			{Purpose: PurposeInputShare, Host: "bilibili.com", IncludeSubdomains: true},
			{Purpose: PurposeMetadataAPI, Host: "api.bilibili.com"},
			{Purpose: PurposeMediaCandidate, DynamicPublic: true},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		purpose AuthorityPurpose
		rawURL  string
		wantErr error
	}{
		{name: "input-share-label", purpose: PurposeInputShare, rawURL: "https://www.bilibili.com/video/BV1synthetic"},
		{name: "metadata-exact", purpose: PurposeMetadataAPI, rawURL: "https://api.bilibili.com/x/web-interface/view?bvid=BV1synthetic"},
		{name: "metadata-not-input", purpose: PurposeInputShare, rawURL: "https://api.bilibili.com/x/web-interface/view?bvid=BV1synthetic", wantErr: ErrAuthorityDenied},
		{name: "input-not-metadata", purpose: PurposeMetadataAPI, rawURL: "https://www.bilibili.com/video/BV1synthetic", wantErr: ErrAuthorityDenied},
		{name: "candidate-public-dynamic", purpose: PurposeMediaCandidate, rawURL: "https://upos-sz-mirror.example/video.m4s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := registry.Authorize(AuthorityRequest{
				Owner:   "bilibili",
				Purpose: test.purpose,
				URL:     mustAuthorityURL(t, test.rawURL),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Authorize() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestEveryFixedEndpointHasUniquePolicyOwner(t *testing.T) {
	t.Parallel()
	if _, err := NewAuthorityRegistry([]AuthorityOwner{
		{Owner: "left", Rules: []AuthorityRule{{Purpose: PurposeMetadataAPI, Host: "api.example.com"}}},
		{Owner: "right", Rules: []AuthorityRule{{Purpose: PurposeMetadataAPI, Host: "api.example.com"}}},
	}); !errors.Is(err, ErrAuthorityAmbiguous) {
		t.Fatalf("duplicate fixed metadata host error = %v", err)
	}
	if _, err := NewAuthorityRegistry([]AuthorityOwner{
		{Owner: "left", Rules: []AuthorityRule{{Purpose: PurposeInputShare, Host: "example.com", IncludeSubdomains: true}}},
		{Owner: "right", Rules: []AuthorityRule{{Purpose: PurposeInputShare, Host: "api.example.com"}}},
	}); !errors.Is(err, ErrAuthorityAmbiguous) {
		t.Fatalf("overlapping input host rules error = %v", err)
	}
}

func TestParserAPIAuthorityCannotBeUsedAsInputRoute(t *testing.T) {
	t.Parallel()
	registry, err := NewAuthorityRegistry([]AuthorityOwner{{
		Owner: "bilibili",
		Rules: []AuthorityRule{
			{Purpose: PurposeInputShare, Host: "bilibili.com", IncludeSubdomains: true},
			{Purpose: PurposeMetadataAPI, Host: "api.bilibili.com"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Authorize(AuthorityRequest{
		Owner: "bilibili", Purpose: PurposeInputShare,
		URL: mustAuthorityURL(t, "https://api.bilibili.com/x/player/playurl?bvid=BV1synthetic"),
	})
	if !errors.Is(err, ErrAuthorityDenied) {
		t.Fatalf("API host became an input route: %v", err)
	}
}

func TestSensitiveHeadersNeverReachDynamicMediaCandidateHost(t *testing.T) {
	t.Parallel()
	registry, err := NewAuthorityRegistry([]AuthorityOwner{{
		Owner: "kuaishou",
		Rules: []AuthorityRule{
			{Purpose: PurposeInputShare, Host: "www.kuaishou.com", IncludeSubdomains: true},
			{Purpose: PurposeMetadataAPI, Host: "www.kuaishou.com", IncludeSubdomains: true, AllowSensitiveHeaders: true},
			{Purpose: PurposeMediaCandidate, DynamicPublic: true},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{
		"Authorization": {"Bearer redacted"},
		"Cookie":        {"session=redacted"},
		"Origin":        {"https://www.kuaishou.com"},
		"Referer":       {"https://www.kuaishou.com/private?id=redacted"},
		"X-Auth":        {"redacted"},
		"X-Trace":       {"retain"},
	}
	decision, err := registry.Authorize(AuthorityRequest{
		Owner: "kuaishou", Purpose: PurposeMediaCandidate,
		URL:    mustAuthorityURL(t, "https://mirror.example/video.mp4"),
		Header: header,
	})
	if err != nil {
		t.Fatal(err)
	}
	sanitized := decision.SanitizedHeader()
	for _, key := range []string{"Authorization", "Cookie", "Origin", "Referer", "X-Auth"} {
		if got := sanitized.Get(key); got != "" {
			t.Fatalf("dynamic candidate retained %s=%q", key, got)
		}
	}
	if got := sanitized.Get("X-Trace"); got != "retain" {
		t.Fatalf("non-sensitive header = %q", got)
	}
	if header.Get("Authorization") == "" || header.Get("Cookie") == "" {
		t.Fatal("Authorize mutated the caller's original headers")
	}
}

func TestCrossPurposeRedirectFailsClosed(t *testing.T) {
	t.Parallel()
	registry, err := NewAuthorityRegistry([]AuthorityOwner{{
		Owner: "sohu",
		Rules: []AuthorityRule{
			{Purpose: PurposeInputShare, Host: "tv.sohu.com", IncludeSubdomains: true},
			{Purpose: PurposeSessionBootstrap, Host: "api.tv.sohu.com", AllowSensitiveHeaders: true},
			{Purpose: PurposeSessionConsumer, Host: "api.tv.sohu.com", AllowSensitiveHeaders: true},
			{Purpose: PurposeMediaCandidate, DynamicPublic: true},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := registry.Authorize(AuthorityRequest{
		Owner: "sohu", Purpose: PurposeMediaCandidate,
		URL: mustAuthorityURL(t, "https://cdn.example/video.mp4"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.AuthorizeRedirect(candidate, AuthorityRequest{
		Owner: "sohu", Purpose: PurposeSessionConsumer,
		URL: mustAuthorityURL(t, "https://api.tv.sohu.com/v4/video/info/1.json?api_key=secret"),
	})
	if !errors.Is(err, ErrAuthorityDenied) {
		t.Fatalf("media candidate redirected into credentialed session consumer: %v", err)
	}
	_, err = registry.AuthorizeRedirect(candidate, AuthorityRequest{
		Owner: "sohu", Purpose: PurposeMediaCandidate,
		URL: mustAuthorityURL(t, "https://cdn-backup.example/video.mp4"),
	})
	if err != nil {
		t.Fatalf("same-purpose public candidate redirect failed: %v", err)
	}
}
