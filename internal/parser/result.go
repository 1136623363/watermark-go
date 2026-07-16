package parser

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"

	"github.com/1136623363/watermark-go/internal/netguard"
)

var ErrCapabilityMismatch = errors.New("parser result does not match descriptor capabilities")
var ErrMediaCandidatesExhausted = errors.New("media candidates exhausted")

type Author struct {
	UID    string `json:"uid,omitempty"`
	Name   string `json:"name,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

type ImageAsset struct {
	URL          string `json:"url"`
	LivePhotoURL string `json:"livePhotoUrl,omitempty"`
}

type MediaKind string

const (
	MediaKindVideo MediaKind = "video"
	MediaKindAudio MediaKind = "audio"
	MediaKindM3U8  MediaKind = "m3u8"
)

type MediaCandidate struct {
	URL        string    `json:"url"`
	Kind       MediaKind `json:"kind"`
	Quality    int       `json:"quality,omitempty"`
	Bitrate    int       `json:"bitrate,omitempty"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	SourceRank int       `json:"sourceRank"`
}

type Result struct {
	Platform   PlatformKey  `json:"platform"`
	Author     Author       `json:"author"`
	Title      string       `json:"title"`
	VideoURL   string       `json:"videoUrl,omitempty"`
	PreviewURL string       `json:"previewUrl,omitempty"`
	AudioURL   string       `json:"audioUrl,omitempty"`
	CoverURL   string       `json:"coverUrl,omitempty"`
	Images     []ImageAsset `json:"images"`
	// Candidates are an internal selection seam. CDN signatures and capability
	// query material must never cross a generic JSON response/evidence edge.
	Candidates []MediaCandidate `json:"-"`
}

func (result Result) ValidateAgainst(descriptor Descriptor) error {
	hasVideo := result.VideoURL != ""
	hasGallery := len(result.Images) > 0
	hasAudio := result.AudioURL != ""
	hasLivePhoto := false
	for _, image := range result.Images {
		if image.LivePhotoURL != "" {
			hasLivePhoto = true
			break
		}
	}
	checks := []struct {
		capability Capability
		present    bool
	}{
		{CapabilityVideo, hasVideo},
		{CapabilityGallery, hasGallery},
		{CapabilityAudio, hasAudio},
		{CapabilityLivePhoto, hasLivePhoto},
	}
	for _, check := range checks {
		if check.present && !descriptor.hasCapability(check.capability) {
			return ErrCapabilityMismatch
		}
	}
	if !hasVideo && !hasGallery && !hasAudio {
		return ErrCapabilityMismatch
	}
	return nil
}

func SortMediaCandidates(candidates []MediaCandidate) {
	sort.SliceStable(candidates, func(left, right int) bool {
		leftMetadata := candidateHasMetadata(candidates[left])
		rightMetadata := candidateHasMetadata(candidates[right])
		if leftMetadata != rightMetadata {
			return leftMetadata
		}
		if !leftMetadata {
			return false
		}
		if candidates[left].Quality != candidates[right].Quality {
			return candidates[left].Quality > candidates[right].Quality
		}
		leftPixels := candidates[left].Width * candidates[left].Height
		rightPixels := candidates[right].Width * candidates[right].Height
		if leftPixels != rightPixels {
			return leftPixels > rightPixels
		}
		if candidates[left].Bitrate != candidates[right].Bitrate {
			return candidates[left].Bitrate > candidates[right].Bitrate
		}
		return candidates[left].SourceRank < candidates[right].SourceRank
	})
}

func candidateHasMetadata(candidate MediaCandidate) bool {
	return candidate.Quality > 0 || candidate.Bitrate > 0 || candidate.Width > 0 || candidate.Height > 0
}

type CandidateProbe func(context.Context, MediaCandidate, netguard.FetchURL) error

func AttemptMediaCandidates(ctx context.Context, candidates []MediaCandidate, budget *RequestBudget, probe CandidateProbe) (MediaCandidate, error) {
	return attemptMediaCandidates(ctx, candidates, budget, probe, true)
}

func attemptMediaCandidates(ctx context.Context, candidates []MediaCandidate, budget *RequestBudget, probe CandidateProbe, accountBeforeProbe bool) (MediaCandidate, error) {
	if ctx == nil || budget == nil || probe == nil || len(candidates) == 0 {
		return MediaCandidate{}, ErrMediaCandidatesExhausted
	}
	ordered := append([]MediaCandidate(nil), candidates...)
	SortMediaCandidates(ordered)
	for _, candidate := range ordered {
		if err := ctx.Err(); err != nil {
			return MediaCandidate{}, err
		}
		target, err := netguard.NewFetchURL(candidate.URL)
		if err != nil {
			continue
		}
		if accountBeforeProbe {
			if err := budget.AllowFetch(target); err != nil {
				return MediaCandidate{}, err
			}
		}
		if err := probe(ctx, candidate, target); err == nil {
			return candidate, nil
		} else if errors.Is(err, ErrBudgetExceeded) {
			return MediaCandidate{}, ErrBudgetExceeded
		} else if errors.Is(err, ErrDuplicateFetch) {
			return MediaCandidate{}, ErrDuplicateFetch
		} else if errors.Is(err, context.Canceled) {
			return MediaCandidate{}, context.Canceled
		} else if errors.Is(err, context.DeadlineExceeded) {
			return MediaCandidate{}, context.DeadlineExceeded
		}
	}
	return MediaCandidate{}, ErrMediaCandidatesExhausted
}

// AttemptMediaCandidatesWithHEAD is the guarded consumer seam for media
// candidates. It deliberately performs only bounded HEAD probes: callers that
// need to download a selected candidate do so through their own netguard
// fetcher after this function returns. Every candidate and redirect shares the
// same RequestBudget and absolute deadline.
func AttemptMediaCandidatesWithHEAD(ctx context.Context, candidates []MediaCandidate, budget *RequestBudget, fetcher HTTPClientFactory, maxRedirects int) (MediaCandidate, error) {
	if ctx == nil || budget == nil || fetcher == nil || maxRedirects < 0 {
		return MediaCandidate{}, ErrMediaCandidatesExhausted
	}
	budgetContext, cancel, err := budget.BindContext(ctx)
	if err != nil {
		return MediaCandidate{}, err
	}
	defer cancel()
	client := fetcher.HTTPClientWithRedirect(budgetContext, maxRedirects, func(*http.Request, []*http.Request) error {
		return budget.AllowRedirect()
	})
	if client == nil {
		return MediaCandidate{}, ErrMediaCandidatesExhausted
	}
	if client.Transport == nil {
		return MediaCandidate{}, ErrMediaCandidatesExhausted
	}
	budgetedClient := *client
	budgetedClient.Transport = candidateBudgetRoundTripper{next: client.Transport, budget: budget}
	return attemptMediaCandidates(budgetContext, candidates, budget, func(probeContext context.Context, _ MediaCandidate, target netguard.FetchURL) error {
		var raw string
		if err := target.Use(func(value string) error {
			raw = value
			return nil
		}); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(probeContext, http.MethodHead, raw, nil)
		if err != nil {
			return errors.New("candidate HEAD request rejected")
		}
		response, err := budgetedClient.Do(request)
		if err != nil {
			return err
		}
		defer func() {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return errors.New("candidate HEAD response rejected")
		}
		return nil
	}, false)
}

type candidateBudgetRoundTripper struct {
	next   http.RoundTripper
	budget *RequestBudget
}

func (transport candidateBudgetRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || transport.next == nil || transport.budget == nil {
		return nil, ErrMediaCandidatesExhausted
	}
	target, err := netguard.NewFetchURL(request.URL.String())
	if err != nil {
		return nil, netguard.ErrInvalidFetchURL
	}
	if err := transport.budget.AllowFetch(target); err != nil {
		return nil, err
	}
	return transport.next.RoundTrip(request)
}
