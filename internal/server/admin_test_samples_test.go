package server

import (
	"testing"

	"watermark-backend/internal/parsers/native"
)

func TestDefaultNativeAdminTestLinksDetectSource(t *testing.T) {
	for _, link := range defaultAdminTestLinks {
		want := platformForDisplayName(link.Name)
		if _, native := parser.VideoSourceInfoMapping[want]; !native {
			continue
		}
		if got := detectSource(link.URL); got != want {
			t.Fatalf("%s default sample detected as %q, want %q for %s", link.Name, got, want, link.URL)
		}
	}
}
