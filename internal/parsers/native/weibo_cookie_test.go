package parser

import (
	"testing"

	"github.com/go-resty/resty/v2"
)

func TestSetWeiboCookieFromEnvironmentOmitsEmptyValue(t *testing.T) {
	t.Setenv("WEIBO_COOKIE", "")
	request := resty.New().R()

	setWeiboCookieFromEnvironment(request)

	if got := request.Header.Get(HttpHeaderCookie); got != "" {
		t.Fatalf("Cookie header = %q, want empty", got)
	}
}

func TestSetWeiboCookieFromEnvironmentTrimsConfiguredValue(t *testing.T) {
	const configured = "  session=unit-test-value; mode=test  "
	const expected = "session=unit-test-value; mode=test"
	t.Setenv("WEIBO_COOKIE", configured)
	request := resty.New().R()

	setWeiboCookieFromEnvironment(request)

	if got := request.Header.Get(HttpHeaderCookie); got != expected {
		t.Fatalf("Cookie header = %q, want %q", got, expected)
	}
}
