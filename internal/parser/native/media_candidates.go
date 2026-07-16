package native

import (
	"strings"

	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type candidateMetadata struct {
	Quality int
	Bitrate int
	Width   int
	Height  int
}

func appendUsableMediaCandidate(candidates []coreparser.MediaCandidate, raw string, kind coreparser.MediaKind, metadata candidateMetadata) []coreparser.MediaCandidate {
	normalized, ok := normalizeExternalMediaURL(raw)
	if !ok {
		return candidates
	}
	incoming := coreparser.MediaCandidate{
		URL: normalized, Kind: kind,
		Quality: metadata.Quality, Bitrate: metadata.Bitrate,
		Width: metadata.Width, Height: metadata.Height,
		SourceRank: len(candidates),
	}
	for index := range candidates {
		if candidates[index].URL == normalized && candidates[index].Kind == kind {
			fillMissingCandidateMetadata(&candidates[index], incoming)
			return candidates
		}
	}
	return append(candidates, incoming)
}

func normalizeExternalMediaURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	target, err := netguard.NewFetchURL(raw)
	if err != nil {
		return "", false
	}
	normalized := ""
	if err := target.Use(func(value string) error {
		normalized = value
		return nil
	}); err != nil {
		return "", false
	}
	return normalized, true
}

func applyMediaCandidates(info *VideoParseInfo, candidates []coreparser.MediaCandidate) {
	if info == nil {
		return
	}
	ordered := append([]coreparser.MediaCandidate(nil), candidates...)
	coreparser.SortMediaCandidates(ordered)
	info.Candidates = ordered
	info.VideoUrl = ""
	for _, candidate := range ordered {
		if candidate.Kind == coreparser.MediaKindVideo {
			info.VideoUrl = candidate.URL
			break
		}
	}
}

func normalizeMediaCandidates(input []coreparser.MediaCandidate) []coreparser.MediaCandidate {
	result := make([]coreparser.MediaCandidate, 0, len(input))
	seen := make(map[string]int, len(input))
	for _, candidate := range input {
		switch candidate.Kind {
		case coreparser.MediaKindVideo, coreparser.MediaKindAudio, coreparser.MediaKindM3U8:
		default:
			continue
		}
		normalized, ok := normalizeExternalMediaURL(candidate.URL)
		if !ok {
			continue
		}
		identity := string(candidate.Kind) + "\x00" + normalized
		if existing, exists := seen[identity]; exists {
			fillMissingCandidateMetadata(&result[existing], candidate)
			continue
		}
		seen[identity] = len(result)
		candidate.URL = normalized
		result = append(result, candidate)
	}
	coreparser.SortMediaCandidates(result)
	return result
}

func fillMissingCandidateMetadata(destination *coreparser.MediaCandidate, source coreparser.MediaCandidate) {
	if destination == nil {
		return
	}
	if destination.Quality == 0 {
		destination.Quality = source.Quality
	}
	if destination.Bitrate == 0 {
		destination.Bitrate = source.Bitrate
	}
	if destination.Width == 0 {
		destination.Width = source.Width
	}
	if destination.Height == 0 {
		destination.Height = source.Height
	}
}

func firstCandidateURL(candidates []coreparser.MediaCandidate, kind coreparser.MediaKind) string {
	for _, candidate := range candidates {
		if candidate.Kind == kind {
			return candidate.URL
		}
	}
	return ""
}
