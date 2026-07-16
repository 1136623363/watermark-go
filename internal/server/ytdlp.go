package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/1136623363/watermark-go/internal/netguard"
	parser "github.com/1136623363/watermark-go/internal/parser/native"
	ytdlprunner "github.com/1136623363/watermark-go/internal/parser/ytdlp"
)

const ytDLPFormatSelector = "best[protocol=https][vcodec!=none][acodec!=none]/best[protocol=http][vcodec!=none][acodec!=none]/best[vcodec!=none][acodec!=none]/best"

type ytDLPMetadata struct {
	ID                 string                   `json:"id"`
	Title              string                   `json:"title"`
	Thumbnail          string                   `json:"thumbnail"`
	Uploader           string                   `json:"uploader"`
	Channel            string                   `json:"channel"`
	URL                string                   `json:"url"`
	WebpageURL         string                   `json:"webpage_url"`
	ManifestURL        string                   `json:"manifest_url"`
	Ext                string                   `json:"ext"`
	Protocol           string                   `json:"protocol"`
	Extractor          string                   `json:"extractor"`
	ExtractorKey       string                   `json:"extractor_key"`
	Duration           float64                  `json:"duration"`
	RequestedDownloads []ytDLPRequestedDownload `json:"requested_downloads"`
	Thumbnails         []ytDLPThumbnail         `json:"thumbnails"`
}

type ytDLPRequestedDownload struct {
	URL      string `json:"url"`
	Ext      string `json:"ext"`
	Protocol string `json:"protocol"`
	FormatID string `json:"format_id"`
}

type ytDLPThumbnail struct {
	URL string `json:"url"`
}

type parseResult struct {
	source       string
	sourceURL    string
	parserEngine string
	info         *parser.VideoParseInfo
	data         parseData
}

func tryParseWithYTDLP(ctx context.Context, rawURL string, parseErr error) (*parseResult, error) {
	if !shouldTryYTDLP(rawURL, parseErr) {
		return nil, parseErr
	}
	logInfof("yt-dlp fallback started target=%s original_error=%s", targetForLog(rawURL), compactLogMessage(parseErr.Error()))

	meta, err := runYTDLP(ctx, rawURL)
	if err != nil {
		logErrorf("yt-dlp fallback failed target=%s error=%v", targetForLog(rawURL), err)
		return nil, err
	}

	data, source, err := buildParseDataFromYTDLP(rawURL, meta)
	if err != nil {
		logErrorf("yt-dlp parse data build failed target=%s error=%v", targetForLog(rawURL), err)
		return nil, err
	}
	logInfof(
		"yt-dlp fallback succeeded target=%s platform=%s type=%s extractor=%s",
		targetForLog(rawURL),
		source,
		data.Type,
		firstNonEmpty(meta.ExtractorKey, meta.Extractor),
	)

	return &parseResult{
		source:       source,
		sourceURL:    rawURL,
		parserEngine: "yt-dlp",
		info:         toVideoParseInfo(data),
		data:         data,
	}, nil
}

func shouldTryYTDLP(rawURL string, parseErr error) bool {
	if parseErr == nil || isDirectM3U8URL(rawURL) {
		return false
	}
	target, err := netguard.NewFetchURL(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	parsed, err := url.Parse(target.Safe().String())
	if err != nil || parsed == nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())

	for _, domain := range []string{
		"youtube.com",
		"youtu.be",
		"tiktok.com",
		"instagram.com",
		"facebook.com",
		"fb.watch",
		"vimeo.com",
		"dailymotion.com",
		"dai.ly",
	} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func runYTDLP(ctx context.Context, rawURL string) (*ytDLPMetadata, error) {
	cfg := currentApplicationRunnerConfig().YTDLP
	runner, err := ytdlprunner.New(ytdlprunner.Config{
		Binary: cfg.Binary, Timeout: cfg.Timeout,
		// Task 4 wires the verified proxy handshake and isolated executor.
		// Until both exist, the constructor intentionally fails closed.
	})
	if err != nil {
		return nil, err
	}
	output, err := runner.Run(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	var meta ytDLPMetadata
	if err := json.Unmarshal(output, &meta); err != nil {
		return nil, fmt.Errorf("yt-dlp json decode failed: %w", err)
	}

	return &meta, nil
}

func buildParseDataFromYTDLP(rawURL string, meta *ytDLPMetadata) (parseData, string, error) {
	source := normalizeYTDLPPlatform(rawURL, meta)
	cover := firstNonEmpty(meta.Thumbnail, lastThumbnail(meta.Thumbnails))
	author := firstNonEmpty(meta.Uploader, meta.Channel)
	title := strings.TrimSpace(meta.Title)
	duration := int(meta.Duration + 0.5)
	mediaURL, protocol := pickYTDLPMediaURL(meta)

	if isM3U8Result(meta, mediaURL, protocol) {
		m3u8URL := firstNonEmpty(meta.ManifestURL, mediaURL)
		if strings.TrimSpace(m3u8URL) == "" {
			return parseData{}, source, errors.New("m3u8 url is empty")
		}
		return parseData{
			Platform: source,
			Type:     "m3u8",
			Title:    title,
			Cover:    cover,
			Author:   author,
			Duration: duration,
			M3U8:     m3u8URL,
		}, source, nil
	}

	if strings.TrimSpace(mediaURL) == "" {
		return parseData{}, source, errors.New("yt-dlp media url is empty")
	}

	return parseData{
		Platform: source,
		Type:     "video",
		Title:    title,
		Cover:    cover,
		Author:   author,
		Duration: duration,
		Downloads: []downloadItem{
			{
				URL:   mediaURL,
				Label: "Original",
			},
		},
	}, source, nil
}

func pickYTDLPMediaURL(meta *ytDLPMetadata) (string, string) {
	if meta == nil {
		return "", ""
	}
	if len(meta.RequestedDownloads) > 0 {
		item := meta.RequestedDownloads[0]
		return strings.TrimSpace(item.URL), strings.TrimSpace(item.Protocol)
	}
	return strings.TrimSpace(meta.URL), strings.TrimSpace(meta.Protocol)
}

func isM3U8Result(meta *ytDLPMetadata, mediaURL, protocol string) bool {
	candidates := []string{
		strings.ToLower(strings.TrimSpace(protocol)),
		strings.ToLower(strings.TrimSpace(mediaURL)),
	}
	if meta != nil {
		candidates = append(candidates,
			strings.ToLower(strings.TrimSpace(meta.Protocol)),
			strings.ToLower(strings.TrimSpace(meta.ManifestURL)),
		)
	}
	for _, item := range candidates {
		if strings.Contains(item, "m3u8") || strings.HasSuffix(item, ".m3u8") {
			return true
		}
	}
	return false
}

func normalizeYTDLPPlatform(rawURL string, meta *ytDLPMetadata) string {
	key := ""
	if meta != nil {
		key = strings.ToLower(firstNonEmpty(meta.ExtractorKey, meta.Extractor))
	}
	switch {
	case strings.Contains(key, "youtube"):
		return "youtube"
	case strings.Contains(key, "tiktok"):
		return "tiktok"
	case strings.Contains(key, "instagram"):
		return "instagram"
	case strings.Contains(key, "facebook"):
		return "facebook"
	case strings.Contains(key, "vimeo"):
		return "vimeo"
	case strings.Contains(key, "dailymotion"):
		return "dailymotion"
	case strings.Contains(key, "twitter"):
		return "twitter"
	}

	parsed, err := url.Parse(rawURL)
	if err == nil {
		host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
		switch {
		case strings.Contains(host, "mux.dev"):
			return "m3u8"
		case strings.Contains(host, "youtube.com"), strings.Contains(host, "youtu.be"):
			return "youtube"
		case strings.Contains(host, "tiktok.com"):
			return "tiktok"
		case strings.Contains(host, "instagram.com"):
			return "instagram"
		case strings.Contains(host, "facebook.com"), strings.Contains(host, "fb.watch"):
			return "facebook"
		case strings.Contains(host, "vimeo.com"):
			return "vimeo"
		case strings.Contains(host, "dailymotion.com"), strings.Contains(host, "dai.ly"):
			return "dailymotion"
		}
	}

	if strings.Contains(strings.ToLower(strings.TrimSpace(rawURL)), ".m3u8") {
		return "m3u8"
	}

	if key != "" {
		return key
	}
	return "generic"
}

func toVideoParseInfo(data parseData) *parser.VideoParseInfo {
	data = normalizeParseDataMediaAliases(data)
	info := &parser.VideoParseInfo{
		Title:      data.Title,
		PreviewUrl: firstNonEmpty(data.Preview, data.PlayAddr),
		MusicUrl:   data.Music,
		CoverUrl:   data.Cover,
	}
	info.Author.Name = data.Author
	info.Author.Avatar = data.Avatar

	if strings.TrimSpace(data.M3U8) != "" {
		info.VideoUrl = strings.TrimSpace(data.M3U8)
	} else if len(data.Downloads) > 0 {
		info.VideoUrl = strings.TrimSpace(data.Downloads[0].URL)
	}

	if len(data.Images) > 0 {
		info.Images = make([]parser.ImgInfo, 0, len(data.Images))
		for _, item := range data.Images {
			if strings.TrimSpace(item) == "" {
				continue
			}
			info.Images = append(info.Images, parser.ImgInfo{Url: item})
		}
	}

	return info
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func lastThumbnail(items []ytDLPThumbnail) string {
	for index := len(items) - 1; index >= 0; index-- {
		if value := strings.TrimSpace(items[index].URL); value != "" {
			return value
		}
	}
	return ""
}
