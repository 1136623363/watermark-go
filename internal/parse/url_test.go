package parse

import (
	"fmt"
	"strings"
	"testing"
)

func TestCanonicalURLKeepsOnlyDescriptorQueryKeys(t *testing.T) {
	got, err := CanonicalizeURL("https://www.example/video/1?vid=42&utm_source=x&ticket=opaque#frag",
		Descriptor{Platform: "example", QueryKeys: []string{"vid"}})
	if err != nil {
		t.Fatalf("CanonicalizeURL() error = %v", err)
	}
	if got.URL != "https://www.example/video/1?vid=42" {
		t.Fatalf("canonical URL = %q, want descriptor-only query", got.URL)
	}
	if strings.Contains(fmt.Sprint(got.LogFields), "42") || strings.Contains(fmt.Sprint(got.LogFields), "opaque") {
		t.Fatalf("log fields exposed query material: %#v", got.LogFields)
	}
}

func TestCanonicalURLQueryPolicyMatrix(t *testing.T) {
	descriptor := Descriptor{
		Platform:  "matrix",
		QueryKeys: []string{"vid", "id", "xsec_token", "modal_id", "v", "s", "pid"},
	}
	got, err := CanonicalizeURL("HTTPS://WWW.EXAMPLE.COM./video/1?UTM_source=tracking&VID=42&vid=42&id=&xsec_token=a%2Fb&xsec_token=a%2Fb&modal_id=9&v=1&s=20&pid=7&ticket=opaque#fragment", descriptor)
	if err != nil {
		t.Fatalf("CanonicalizeURL() error = %v", err)
	}
	want := "https://www.example.com/video/1?modal_id=9&pid=7&s=20&v=1&vid=42&xsec_token=a%2Fb"
	if got.URL != want {
		t.Fatalf("canonical URL = %q, want %q", got.URL, want)
	}
	for _, forbidden := range []string{"UTM_source", "ticket", "opaque", "#fragment", "?id=", "&id="} {
		if strings.Contains(got.URL, forbidden) {
			t.Fatalf("canonical URL retained forbidden query/fragment %q: %s", forbidden, got.URL)
		}
	}
	second, err := CanonicalizeURL(got.URL, descriptor)
	if err != nil {
		t.Fatalf("CanonicalizeURL(second) error = %v", err)
	}
	if second.URL != got.URL || second.Fingerprint != got.Fingerprint {
		t.Fatalf("canonicalization is not stable: first=%#v second=%#v", got, second)
	}
}

func TestCanonicalURLRejectsNonHTTPInput(t *testing.T) {
	for _, raw := range []string{
		"ftp://www.example/video/1?vid=42",
		"file:///etc/passwd",
		"not a url",
	} {
		if _, err := CanonicalizeURL(raw, Descriptor{Platform: "example", QueryKeys: []string{"vid"}}); err == nil {
			t.Fatalf("CanonicalizeURL(%q) accepted invalid input", raw)
		}
	}
}
