package server

import "testing"

func TestParseResultCachePutAndGet(t *testing.T) {
	cache := &parseResultCache{dir: t.TempDir()}
	sourceURL := "https://v.douyin.com/test/"
	input := parseData{
		Platform: "douyin",
		Type:     "video",
		Title:    "测试视频",
		Cover:    "https://example.com/cover.jpg",
		Downloads: []downloadItem{
			{URL: "https://example.com/video.mp4", Label: "无水印视频"},
		},
	}

	stored, err := cache.put(sourceURL, input)
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if stored.ShareID == "" {
		t.Fatal("stored ShareID should not be empty")
	}
	if stored.SourceURL != sourceURL {
		t.Fatalf("stored SourceURL = %q, want %q", stored.SourceURL, sourceURL)
	}

	got, ok, err := cache.get(stored.ShareID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !ok {
		t.Fatal("expected cached result")
	}
	if got.ShareID != stored.ShareID {
		t.Fatalf("got ShareID = %q, want %q", got.ShareID, stored.ShareID)
	}
	if got.Title != input.Title {
		t.Fatalf("got title = %q, want %q", got.Title, input.Title)
	}
	if len(got.Downloads) != 1 || got.Downloads[0].URL != input.Downloads[0].URL {
		t.Fatalf("cached downloads mismatch: %#v", got.Downloads)
	}
}

func TestParseResultCacheGetBySourceURL(t *testing.T) {
	cache := &parseResultCache{dir: t.TempDir()}
	sourceURL := "https://v.douyin.com/stable/"
	stored, err := cache.put(sourceURL, parseData{Platform: "douyin", Type: "video"})
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}

	got, ok, err := cache.getBySourceURL(sourceURL)
	if err != nil {
		t.Fatalf("getBySourceURL failed: %v", err)
	}
	if !ok {
		t.Fatal("expected cached result by source URL")
	}
	if got.ShareID != stored.ShareID {
		t.Fatalf("got ShareID = %q, want %q", got.ShareID, stored.ShareID)
	}
}

func TestParseResultCacheNormalizesAudioAliases(t *testing.T) {
	cache := &parseResultCache{dir: t.TempDir()}
	sourceURL := "https://music.example.com/song/1"
	audioURL := "https://cdn.example.com/song.mp3"

	stored, err := cache.put(sourceURL, parseData{
		Platform: "music",
		Type:     "audio",
		Downloads: []downloadItem{
			{URL: audioURL, Label: "mp3"},
		},
	})
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if stored.Music != audioURL || stored.MP3 != audioURL || stored.Audio != audioURL || stored.AudioURL != audioURL {
		t.Fatalf("stored audio aliases mismatch: %#v", stored)
	}

	got, ok, err := cache.get(stored.ShareID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !ok {
		t.Fatal("expected cached result")
	}
	if got.Music != audioURL || got.MP3 != audioURL || got.Audio != audioURL || got.AudioURL != audioURL {
		t.Fatalf("got audio aliases mismatch: %#v", got)
	}
}

func TestParseResultCacheRejectsInvalidID(t *testing.T) {
	cache := &parseResultCache{dir: t.TempDir()}
	_, ok, err := cache.get("../bad")
	if err != nil {
		t.Fatalf("get invalid id should not error: %v", err)
	}
	if ok {
		t.Fatal("invalid id should not return cached data")
	}
}

func TestCachedParseDataNeedsRefresh(t *testing.T) {
	valid := parseData{
		Title: "Istio 老中医在线问诊",
		Downloads: []downloadItem{
			{URL: "https://example.com/video.mp4"},
		},
	}
	if cachedParseDataNeedsRefresh(valid) {
		t.Fatal("valid parse data should not need refresh")
	}

	garbled := valid
	garbled.Title = "Istio ????????????????"
	if !cachedParseDataNeedsRefresh(garbled) {
		t.Fatal("question mark garbled title should need refresh")
	}

	emptyMedia := parseData{Title: "只有标题"}
	if !cachedParseDataNeedsRefresh(emptyMedia) {
		t.Fatal("empty media result should need refresh")
	}

	audioAlias := parseData{Title: "Audio", MP3: "https://cdn.example.com/song.mp3"}
	if cachedParseDataNeedsRefresh(audioAlias) {
		t.Fatal("audio alias should count as usable media")
	}
}
