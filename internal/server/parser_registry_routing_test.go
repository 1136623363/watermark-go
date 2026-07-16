package server

import "testing"

func TestServerRoutingUsesRegistryHostRulesAndRegistryAliases(t *testing.T) {
	setApplicationNativeParser(nil)
	t.Cleanup(func() { setApplicationNativeParser(nil) })

	for _, rawURL := range []string{
		"https://www.douyin.com/video/synthetic",
		"https://sub.weibo.com/status/synthetic",
	} {
		if !matchPlatform(rawURL) {
			t.Fatalf("registry rejected an approved native host: %q", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://weibo.com.evil.example/status/synthetic",
		"https://evil.example/path/weibo.com/status",
		"https://evil.example/watch?next=https://www.douyin.com/video/synthetic",
	} {
		if matchPlatform(rawURL) || detectSource(rawURL) != "" {
			t.Fatalf("server guessed a native platform outside registry host rules: %q", rawURL)
		}
	}

	for alias, want := range map[string]string{
		"xiaohongshu": "redbook",
		"kgqq":        "quanminkge",
		"ixigua":      "xigua",
	} {
		if got := normalizeAdminSamplePlatform(alias); got != want {
			t.Fatalf("registry alias %q normalized to %q, want %q", alias, got, want)
		}
	}
}
