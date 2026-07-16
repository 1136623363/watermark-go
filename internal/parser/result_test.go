package parser

import (
	"errors"
	"reflect"
	"testing"
)

func TestDescriptorCapabilitiesMatchResult(t *testing.T) {
	t.Parallel()
	descriptor := Descriptor{Key: "rich", Capabilities: CapabilityVideo | CapabilityGallery | CapabilityAudio | CapabilityLivePhoto}
	result := Result{
		VideoURL: "https://cdn.example/video.mp4",
		AudioURL: "https://cdn.example/audio.m4a",
		Images: []ImageAsset{{
			URL:          "https://cdn.example/image.jpg",
			LivePhotoURL: "https://cdn.example/live.mp4",
		}},
	}
	if err := result.ValidateAgainst(descriptor); err != nil {
		t.Fatalf("rich result rejected: %v", err)
	}
	descriptor.Capabilities &^= CapabilityAudio
	if err := result.ValidateAgainst(descriptor); !errors.Is(err, ErrCapabilityMismatch) {
		t.Fatalf("capability mismatch error = %v", err)
	}
}

func TestMediaCandidateOrderIsStable(t *testing.T) {
	t.Parallel()
	candidates := []MediaCandidate{
		{URL: "fallback-first", SourceRank: 0},
		{URL: "fallback-second", SourceRank: 1},
		{URL: "720p", Quality: 720, Bitrate: 1_000, SourceRank: 3},
		{URL: "1080p-low", Quality: 1080, Bitrate: 2_000, SourceRank: 2},
		{URL: "1080p-high", Quality: 1080, Bitrate: 3_000, SourceRank: 4},
	}
	SortMediaCandidates(candidates)
	want := []string{"1080p-high", "1080p-low", "720p", "fallback-first", "fallback-second"}
	got := make([]string, len(candidates))
	for index := range candidates {
		got[index] = candidates[index].URL
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate order = %#v, want %#v", got, want)
	}
}

func TestMediaCandidateStableTieAndMissingMetadataKeepParserOrder(t *testing.T) {
	t.Parallel()
	candidates := []MediaCandidate{
		{URL: "missing-first", SourceRank: 8},
		{URL: "metadata-first", Quality: 1080, Bitrate: 2_000, Width: 1920, Height: 1080, SourceRank: 4},
		{URL: "metadata-second", Quality: 1080, Bitrate: 2_000, Width: 1920, Height: 1080, SourceRank: 4},
		{URL: "missing-second", SourceRank: 1},
	}
	SortMediaCandidates(candidates)
	want := []string{"metadata-first", "metadata-second", "missing-first", "missing-second"}
	got := make([]string, len(candidates))
	for index := range candidates {
		got[index] = candidates[index].URL
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stable candidate order = %#v, want %#v", got, want)
	}
}
