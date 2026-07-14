package parser

import "testing"

func TestXiGuaRequestHeadersOmitUnconfiguredCookie(t *testing.T) {
	t.Setenv("XIGUA_COOKIE", "")
	if got := xiGuaRequestHeaders()[HttpHeaderCookie]; got != "" {
		t.Fatal("XiGua request headers retained an unconfigured Cookie")
	}
}

func TestXiGuaRequestHeadersUseTrimmedEnvironmentCookie(t *testing.T) {
	expected := "configured-" + "cookie-material"
	t.Setenv("XIGUA_COOKIE", "  "+expected+"  ")
	if got := xiGuaRequestHeaders()[HttpHeaderCookie]; got != expected {
		t.Fatal("XiGua request headers did not use the trimmed environment Cookie")
	}
}
