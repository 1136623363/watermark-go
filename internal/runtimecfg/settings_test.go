package runtimecfg

import "testing"

func TestShouldUseProxyForTarget(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   bool
	}{
		{
			name:   "douyin short link should bypass proxy",
			rawURL: "https://v.douyin.com/0Ju1KcKOj3g/",
			want:   false,
		},
		{
			name:   "bilibili should bypass proxy",
			rawURL: "https://www.bilibili.com/video/BV1634y1w7Nu/",
			want:   false,
		},
		{
			name:   "xiaohongshu should bypass proxy",
			rawURL: "https://www.xiaohongshu.com/discovery/item/63fec0ba0000000014026f92",
			want:   false,
		},
		{
			name:   "youtube should use proxy",
			rawURL: "https://www.youtube.com/watch?v=oGsXa6slchc&t=2s",
			want:   true,
		},
		{
			name:   "youtu.be should use proxy",
			rawURL: "https://youtu.be/oGsXa6slchc",
			want:   true,
		},
		{
			name:   "tiktok should use proxy",
			rawURL: "https://www.tiktok.com/@foo/video/7413042588074200326",
			want:   true,
		},
		{
			name:   "instagram should use proxy",
			rawURL: "https://www.instagram.com/reel/C62MdoDOWCr/",
			want:   true,
		},
		{
			name:   "facebook should use proxy",
			rawURL: "https://facebook.com/CTSHSTT/videos/374811976811983/",
			want:   true,
		},
		{
			name:   "x should use proxy",
			rawURL: "https://x.com/Eminem/status/943590594491772928",
			want:   true,
		},
		{
			name:   "dailymotion should use proxy",
			rawURL: "https://www.dailymotion.com/video/x7t3la2",
			want:   true,
		},
		{
			name:   "plain m3u8 should bypass proxy by default",
			rawURL: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldUseProxyForTarget(tc.rawURL)
			if got != tc.want {
				t.Fatalf("ShouldUseProxyForTarget(%q) = %v, want %v", tc.rawURL, got, tc.want)
			}
		})
	}
}

func TestNormalizeRejectsRemoteDNSAndCredentialedProxyConfig(t *testing.T) {
	tests := []string{
		"socks5h://127.0.0.1:1080",
		"http://user:pass@127.0.0.1:8080",
		"https://token@proxy.example:443",
	}
	for _, proxyURL := range tests {
		t.Run(proxyURL, func(t *testing.T) {
			settings := defaults()
			settings.OutboundProxy = proxyURL
			if err := normalizeAndValidate(&settings); err == nil {
				t.Fatalf("normalizeAndValidate accepted unsafe proxy %q", proxyURL)
			}
		})
	}
}

func TestNormalizeRejectsUnsafeMusicDLNetworkOverrideConfig(t *testing.T) {
	tests := []string{
		`{"sources":{"TIDALMusicClient":{"requests_overrides":{"headers":{"Cookie":"x"}}}}}`,
		`{"sources":{"TIDALMusicClient":{"proxies":{"https":"http://evil.example:8080"}}}}`,
		`{"sources":{"TIDALMusicClient":{"verify":false}}}`,
		`{"sources":{"TIDALMusicClient":{"stream":true}}}`,
		`{"sources":{"TIDALMusicClient":{"allow_redirects":true}}}`,
		`{"sources":{"TIDALMusicClient":{"session":{"id":"x"}}}}`,
	}
	for _, configured := range tests {
		t.Run(configured, func(t *testing.T) {
			settings := defaults()
			settings.UniversalParserMusicDLConfigJSON = configured
			if err := normalizeAndValidate(&settings); err == nil {
				t.Fatal("normalizeAndValidate accepted unsafe musicdl network override config")
			}
		})
	}

	settings := defaults()
	settings.UniversalParserMusicDLConfigJSON = `{"sources":{"TIDALMusicClient":{"quality":"lossless"}}}`
	if err := normalizeAndValidate(&settings); err != nil {
		t.Fatalf("normalizeAndValidate rejected safe musicdl config: %v", err)
	}
}

func TestNormalizeClusterDisabledNodes(t *testing.T) {
	settings := defaults()
	settings.ClusterDisabledNodes = []string{" worker-1 ", "", "WORKER-1", "http://192.168.31.222:5001", "worker-2"}

	if err := normalizeAndValidate(&settings); err != nil {
		t.Fatalf("normalizeAndValidate() error = %v", err)
	}

	want := []string{"worker-1", "http://192.168.31.222:5001", "worker-2"}
	if len(settings.ClusterDisabledNodes) != len(want) {
		t.Fatalf("ClusterDisabledNodes length = %d, want %d: %#v", len(settings.ClusterDisabledNodes), len(want), settings.ClusterDisabledNodes)
	}
	for index := range want {
		if settings.ClusterDisabledNodes[index] != want[index] {
			t.Fatalf("ClusterDisabledNodes[%d] = %q, want %q", index, settings.ClusterDisabledNodes[index], want[index])
		}
	}
}

func TestNormalizeClusterSettings(t *testing.T) {
	settings := defaults()
	settings.ClusterDispatchMode = " Workers "
	settings.ClusterWorkerEndpoints = []string{
		" worker-1=http://192.168.31.222:5001 ",
		"",
		"WORKER-1=http://192.168.31.222:5001",
		"http://192.168.31.223:5001/",
	}
	settings.ClusterTestConcurrency = 99
	settings.ClusterHealthTimeoutSeconds = 99
	settings.ClusterRemoteTestTimeoutSeconds = 999

	if err := normalizeAndValidate(&settings); err != nil {
		t.Fatalf("normalizeAndValidate() error = %v", err)
	}

	if settings.ClusterDispatchMode != ClusterDispatchWorkers {
		t.Fatalf("ClusterDispatchMode = %q, want %q", settings.ClusterDispatchMode, ClusterDispatchWorkers)
	}

	wantEndpoints := []string{"worker-1=http://192.168.31.222:5001", "http://192.168.31.223:5001/"}
	if len(settings.ClusterWorkerEndpoints) != len(wantEndpoints) {
		t.Fatalf("ClusterWorkerEndpoints length = %d, want %d: %#v", len(settings.ClusterWorkerEndpoints), len(wantEndpoints), settings.ClusterWorkerEndpoints)
	}
	for index := range wantEndpoints {
		if settings.ClusterWorkerEndpoints[index] != wantEndpoints[index] {
			t.Fatalf("ClusterWorkerEndpoints[%d] = %q, want %q", index, settings.ClusterWorkerEndpoints[index], wantEndpoints[index])
		}
	}

	if settings.ClusterTestConcurrency != 16 {
		t.Fatalf("ClusterTestConcurrency = %d, want 16", settings.ClusterTestConcurrency)
	}
	if settings.ClusterHealthTimeoutSeconds != 30 {
		t.Fatalf("ClusterHealthTimeoutSeconds = %d, want 30", settings.ClusterHealthTimeoutSeconds)
	}
	if settings.ClusterRemoteTestTimeoutSeconds != 600 {
		t.Fatalf("ClusterRemoteTestTimeoutSeconds = %d, want 600", settings.ClusterRemoteTestTimeoutSeconds)
	}
}

func TestInvalidClusterDispatchMode(t *testing.T) {
	settings := defaults()
	settings.ClusterDispatchMode = "round-robin"

	if err := normalizeAndValidate(&settings); err == nil {
		t.Fatal("normalizeAndValidate() error = nil, want invalid dispatch mode error")
	}
}

func TestNormalizeDownloadFallbackSettings(t *testing.T) {
	settings := defaults()
	settings.DownloadFallbackMode = " Proxy "
	settings.DownloadFallbackPublicBaseURL = " https://watermark.bxsn.cn/ "
	settings.DownloadFallbackCDNBaseURL = " https://cdn.example.com/cache/ "

	if err := normalizeAndValidate(&settings); err != nil {
		t.Fatalf("normalizeAndValidate() error = %v", err)
	}

	if settings.DownloadFallbackMode != DownloadFallbackModeProxy {
		t.Fatalf("DownloadFallbackMode = %q, want %q", settings.DownloadFallbackMode, DownloadFallbackModeProxy)
	}
	if settings.DownloadFallbackPublicBaseURL != "https://watermark.bxsn.cn" {
		t.Fatalf("DownloadFallbackPublicBaseURL = %q", settings.DownloadFallbackPublicBaseURL)
	}
	if settings.DownloadFallbackCDNBaseURL != "https://cdn.example.com/cache" {
		t.Fatalf("DownloadFallbackCDNBaseURL = %q", settings.DownloadFallbackCDNBaseURL)
	}
}

func TestInvalidDownloadFallbackMode(t *testing.T) {
	settings := defaults()
	settings.DownloadFallbackMode = "tunnel"

	if err := normalizeAndValidate(&settings); err == nil {
		t.Fatal("normalizeAndValidate() error = nil, want invalid download fallback mode error")
	}
}

func TestJSONObjHasField(t *testing.T) {
	if !jsonObjectHasField([]byte(`{"clusterWorkerEndpoints":[]}`), "clusterWorkerEndpoints") {
		t.Fatal("jsonObjectHasField() = false, want true")
	}
	if jsonObjectHasField([]byte(`{"clusterDisabledNodes":[]}`), "clusterWorkerEndpoints") {
		t.Fatal("jsonObjectHasField() = true, want false")
	}
}
