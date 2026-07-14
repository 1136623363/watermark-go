package server

import (
	"reflect"
	"sort"
	"testing"
)

func TestExtractWechatDomainCandidates(t *testing.T) {
	data := parseData{
		Platform: "douyin",
		Type:     "video",
		Cover:    "https://p3-sign.douyinpic.com/tos-cn-i-0813/example.jpeg?x=1",
		Avatar:   "http://insecure.example.com/avatar.jpeg",
		Music:    "https://music.example.com/audio.m4a",
		Downloads: []downloadItem{
			{URL: "https://v26-web.douyinvod.com/abc/video.mp4?token=secret", Label: "video"},
			{URL: "https://v26-web.douyinvod.com/abc/video.mp4?token=secret", Label: "video"},
			{URL: "https://127.0.0.1/video.mp4", Label: "video"},
		},
		Images: []string{
			"https://sns-img.example.cn/a/b.png",
			"file:///tmp/a.png",
		},
		M3U8: "https://stream.example.net/live/index.m3u8",
	}

	items := extractWechatDomainCandidates(data)
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Origin+"|"+item.MediaType)
	}
	sort.Strings(got)

	want := []string{
		"https://music.example.com|audio",
		"https://p3-sign.douyinpic.com|cover",
		"https://sns-img.example.cn|image",
		"https://stream.example.net|m3u8",
		"https://v26-web.douyinvod.com|video",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("domains = %#v, want %#v", got, want)
	}
}

func TestMergeCSV(t *testing.T) {
	if got := mergeCSV("video,image", "audio", "video", ""); got != "audio,image,video" {
		t.Fatalf("mergeCSV = %q, want audio,image,video", got)
	}
}
