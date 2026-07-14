package utils

import "testing"

func TestRegexpMatchUrlFromStringInstagramPost(t *testing.T) {
	raw := "分享 https://www.instagram.com/p/DYJXOStGj1W/?igsh=ajQ5cno1dDMwZGRk 复制"
	got, err := RegexpMatchUrlFromString(raw)
	if err != nil {
		t.Fatalf("RegexpMatchUrlFromString returned error: %v", err)
	}
	want := "https://www.instagram.com/p/DYJXOStGj1W/?igsh=ajQ5cno1dDMwZGRk"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestRegexpMatchUrlFromStringTrimsChinesePunctuation(t *testing.T) {
	raw := "链接：https://www.instagram.com/reel/C62MdoDOWCr/。"
	got, err := RegexpMatchUrlFromString(raw)
	if err != nil {
		t.Fatalf("RegexpMatchUrlFromString returned error: %v", err)
	}
	want := "https://www.instagram.com/reel/C62MdoDOWCr/"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}
