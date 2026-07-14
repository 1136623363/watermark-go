package server

import (
	"errors"
	"testing"
)

func TestParseShareRequestDirectM3U8BypassesFailureCache(t *testing.T) {
	oldCache := globalParseResultCache
	globalParseResultCache = &parseResultCache{dir: t.TempDir()}
	t.Cleanup(func() {
		globalParseResultCache = oldCache
	})

	rawURL := "https://8.8.8.8/live/primary.m3u8?token=test"
	setParseFailure(rawURL, errors.New("yt-dlp parse failed: stale failure"))
	t.Cleanup(func() {
		clearParseFailure(rawURL)
	})

	result, err := parseShareRequestWithOptions("sample "+rawURL, parseRequestOptions{
		BypassCache: true,
	})
	if err != nil {
		t.Fatalf("parse direct m3u8 failed: %v", err)
	}
	if result.source != "m3u8" {
		t.Fatalf("source = %q, want m3u8", result.source)
	}
	if result.data.Platform != "m3u8" {
		t.Fatalf("platform = %q, want m3u8", result.data.Platform)
	}
	if result.data.Type != "m3u8" {
		t.Fatalf("type = %q, want m3u8", result.data.Type)
	}
	if result.data.M3U8 != rawURL {
		t.Fatalf("m3u8 = %q, want %q", result.data.M3U8, rawURL)
	}
	if result.data.Title != "primary.m3u8" {
		t.Fatalf("title = %q, want primary.m3u8", result.data.Title)
	}
}

func TestShouldTryYTDLPRejectsDirectM3U8(t *testing.T) {
	rawURL := "https://8.8.8.8/live/primary.m3u8"
	if shouldTryYTDLP(rawURL, errors.New("share url not have source config")) {
		t.Fatal("direct m3u8 URL should not use yt-dlp fallback")
	}
}

func TestCompatErrorResponseInstagramAccessLimited(t *testing.T) {
	err := errors.New("yt-dlp parse failed: ERROR: [Instagram] DYJXOStGj1W: Requested content is not available, rate-limit reached or login required. Use --cookies")
	res := compatErrorResponse(err)
	if res.Code != 1001 {
		t.Fatalf("code = %d, want 1001", res.Code)
	}
	want := "Instagram 内容暂时无法访问：可能需要登录 Cookie、代理被限流，或帖子权限受限"
	if res.Msg != want {
		t.Fatalf("msg = %q, want %q", res.Msg, want)
	}
}
