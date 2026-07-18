package server

import "testing"

func TestValidateRemoteTargetRejectsPrivateTargetsEvenWhenLegacyOverrideIsSet(t *testing.T) {
	t.Setenv("DOWNLOAD_FALLBACK_ALLOW_PRIVATE_URLS", "")
	if err := validateRemoteTarget("http://127.0.0.1:18080/sample.mp4"); err == nil {
		t.Fatal("expected private target to be rejected by default")
	}

	t.Setenv("DOWNLOAD_FALLBACK_ALLOW_PRIVATE_URLS", "true")
	if err := validateRemoteTarget("http://127.0.0.1:18080/sample.mp4"); err == nil {
		t.Fatal("legacy private-target override should no longer bypass netguard")
	}
}

func TestDownloadFallbackOriginRefererForDouyinCDN(t *testing.T) {
	referer, origin := downloadFallbackOriginReferer("https://v95-bjb-mc-cold.douyinvod.com/path/video.mp4")
	if referer != "https://www.douyin.com/" {
		t.Fatalf("referer = %q", referer)
	}
	if origin != "https://www.douyin.com" {
		t.Fatalf("origin = %q", origin)
	}
}

func TestDownloadFallbackOriginHeaderProfilesUseSourceURLAndRangeFallback(t *testing.T) {
	profiles := downloadFallbackOriginHeaderProfiles(
		"https://www.douyin.com/video/7038495861986397443",
		"https://v95-bjb-mc-cold.douyinvod.com/path/video.mp4",
		"video",
		"bytes=0-",
	)
	var hasSourceReferer bool
	var hasRange bool
	var hasNoRange bool
	for _, profile := range profiles {
		if profile["Referer"] == "https://www.douyin.com/video/7038495861986397443" {
			hasSourceReferer = true
		}
		if profile["Range"] == "bytes=0-" {
			hasRange = true
		}
		if profile["Range"] == "" {
			hasNoRange = true
		}
	}
	if !hasSourceReferer {
		t.Fatal("expected source page referer profile")
	}
	if !hasRange {
		t.Fatal("expected ranged profile")
	}
	if !hasNoRange {
		t.Fatal("expected non-ranged fallback profile")
	}
}
