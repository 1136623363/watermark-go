package native

import "testing"

func TestXiGuaRequestHeadersOmitUnconfiguredCookie(t *testing.T) {
	if got := xiGuaRequestHeaders("")[HttpHeaderCookie]; got != "" {
		t.Fatal("XiGua request headers retained an unconfigured Cookie")
	}
}

func TestXiGuaRequestHeadersUseTrimmedEnvironmentCookie(t *testing.T) {
	expected := "configured-" + "cookie-material"
	if got := xiGuaRequestHeaders("  " + expected + "  ")[HttpHeaderCookie]; got != expected {
		t.Fatal("XiGua request headers did not use the trimmed environment Cookie")
	}
}
