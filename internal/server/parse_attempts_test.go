package server

import (
	"strings"
	"testing"
)

func TestClassifyParseInputKnownPlatform(t *testing.T) {
	item := classifyParseInput("看看这个 https://v.douyin.com/i2YArd1J/")
	if item.Classification != parseLinkClassKnown {
		t.Fatalf("classification = %q, want %q", item.Classification, parseLinkClassKnown)
	}
	if item.Platform != "douyin" {
		t.Fatalf("platform = %q, want douyin", item.Platform)
	}
	if item.Host != "v.douyin.com" {
		t.Fatalf("host = %q, want v.douyin.com", item.Host)
	}
}

func TestClassifyParseInputExternalPlatform(t *testing.T) {
	item := classifyParseInput("https://www.instagram.com/p/DYJXOStGj1W/?igsh=ajQ5cno1dDMwZGRk")
	if item.Classification != parseLinkClassExternal {
		t.Fatalf("classification = %q, want %q", item.Classification, parseLinkClassExternal)
	}
	if item.Platform != "instagram" {
		t.Fatalf("platform = %q, want instagram", item.Platform)
	}
	if strings.Contains(item.RawInput, "igsh") || strings.Contains(item.SourceURL, "igsh") || strings.Contains(item.NormalizedURL, "igsh") {
		t.Fatal("parse-attempt classification retained input query material")
	}
}

func TestClassifyParseInputM3U8(t *testing.T) {
	item := classifyParseInput("https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8")
	if item.Classification != parseLinkClassM3U8 {
		t.Fatalf("classification = %q, want %q", item.Classification, parseLinkClassM3U8)
	}
	if item.Platform != "m3u8" {
		t.Fatalf("platform = %q, want m3u8", item.Platform)
	}
}

func TestClassifyParseInputUnknownAndInvalid(t *testing.T) {
	unknown := classifyParseInput("https://example.com/not-supported")
	if unknown.Classification != parseLinkClassUnknown {
		t.Fatalf("classification = %q, want %q", unknown.Classification, parseLinkClassUnknown)
	}
	if unknown.Host != "example.com" {
		t.Fatalf("host = %q, want example.com", unknown.Host)
	}

	invalid := classifyParseInput("没有链接")
	if invalid.Classification != parseLinkClassInvalid {
		t.Fatalf("classification = %q, want %q", invalid.Classification, parseLinkClassInvalid)
	}
}
