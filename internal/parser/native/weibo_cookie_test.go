package native

import (
	"testing"

	"github.com/go-resty/resty/v2"
)

func TestSetWeiboCookieOmitsEmptyValue(t *testing.T) {
	request := resty.New().R()

	setWeiboCookie(request, "")

	if got := request.Header.Get(HttpHeaderCookie); got != "" {
		t.Fatal("empty Weibo Cookie unexpectedly produced a header")
	}
}

func TestSetWeiboCookieTrimsConfiguredValue(t *testing.T) {
	const configured = "  session=unit-test-value; mode=test  "
	const expected = "session=unit-test-value; mode=test"
	request := resty.New().R()

	setWeiboCookie(request, configured)

	if got := request.Header.Get(HttpHeaderCookie); got != expected {
		t.Fatal("configured Weibo Cookie was not trimmed exactly")
	}
}
