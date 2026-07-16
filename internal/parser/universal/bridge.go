package universal

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
	ytdlprunner "github.com/1136623363/watermark-go/internal/parser/ytdlp"
)

type PythonBridge struct {
	cfg           Config
	guardEndpoint string
}

type bridgeResponse struct {
	OK    bool             `json:"ok"`
	Kind  string           `json:"kind"`
	Error string           `json:"error"`
	Items []map[string]any `json:"items"`
	Meta  map[string]any   `json:"meta"`
}

func NewPythonBridge(cfg Config) (*PythonBridge, error) {
	if cfg.Runner == nil {
		return nil, errors.New("universal parser requires an isolated runner")
	}
	guardEndpoint, err := ytdlprunner.ValidateGuardProxy(cfg.GuardProxy)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(cfg.PythonBin) || !filepath.IsAbs(cfg.BridgeScript) ||
		!filepath.IsAbs(cfg.VideoDLPath) || !filepath.IsAbs(cfg.WorkDir) {
		return nil, errors.New("universal parser paths must be fixed absolute paths")
	}
	if cfg.MusicDLPath != "" && !filepath.IsAbs(cfg.MusicDLPath) {
		return nil, errors.New("universal music source path must be absolute")
	}
	for _, value := range []string{cfg.PythonBin, cfg.BridgeScript, cfg.VideoDLPath, cfg.MusicDLPath, cfg.WorkDir} {
		if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("universal parser path is invalid")
		}
	}
	approvedPaths := []struct {
		kind  ytdlprunner.PathKind
		value string
	}{
		{ytdlprunner.PathExecutable, cfg.PythonBin},
		{ytdlprunner.PathScript, cfg.BridgeScript},
		{ytdlprunner.PathReadOnlySource, cfg.VideoDLPath},
		{ytdlprunner.PathWorkDir, cfg.WorkDir},
	}
	if cfg.MusicDLPath != "" {
		approvedPaths = append(approvedPaths, struct {
			kind  ytdlprunner.PathKind
			value string
		}{ytdlprunner.PathReadOnlySource, cfg.MusicDLPath})
	}
	if cfg.Paths == nil {
		return nil, errors.New("universal parser image path policy is required")
	}
	for _, path := range approvedPaths {
		if !cfg.Paths.AllowsImagePath(path.kind, path.value) {
			return nil, errors.New("universal parser path is not approved by image provenance")
		}
	}
	if cfg.BridgeTimeout <= 0 || cfg.MusicDLTimeout <= 0 || cfg.MusicDLItemLimit <= 0 {
		return nil, errors.New("universal parser limits must be positive")
	}
	return &PythonBridge{cfg: cfg, guardEndpoint: guardEndpoint}, nil
}

func (b *PythonBridge) ParseVideo(ctx context.Context, req ParseRequest) (ParseData, error) {
	resp, err := b.call(ctx, "video", req)
	if err != nil {
		return ParseData{}, err
	}
	return buildVideoParseData(req.URL, resp.Items)
}

func (b *PythonBridge) SearchMusic(ctx context.Context, req ParseRequest) (ParseData, error) {
	resp, err := b.call(ctx, "music-search", req)
	if err != nil {
		return ParseData{}, err
	}
	return buildMusicParseData(firstNonEmpty(req.Keyword, req.URL), resp.Items)
}

func (b *PythonBridge) ParseMusicPlaylist(ctx context.Context, req ParseRequest) (ParseData, error) {
	resp, err := b.call(ctx, "music-playlist", req)
	if err != nil {
		return ParseData{}, err
	}
	return buildMusicParseData(req.URL, resp.Items)
}

func (b *PythonBridge) Health(ctx context.Context) map[string]any {
	return map[string]any{
		"enabled":          true,
		"guarded":          true,
		"python":           b.cfg.PythonBin,
		"bridgeScript":     b.cfg.BridgeScript,
		"videodlPath":      b.cfg.VideoDLPath,
		"musicdlPath":      b.cfg.MusicDLPath,
		"workDir":          b.cfg.WorkDir,
		"timeout":          b.timeout().String(),
		"musicdlTimeout":   b.musicDLTimeout().String(),
		"musicdlItemLimit": b.musicDLItemLimit(),
	}
}

func (b *PythonBridge) call(ctx context.Context, mode string, req ParseRequest) (bridgeResponse, error) {
	timeout := b.timeoutForMode(mode)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(req)
	if err != nil {
		return bridgeResponse{}, err
	}
	if len(payload) > 256<<10 {
		return bridgeResponse{}, errors.New("universal parser input exceeds limit")
	}

	command := ytdlprunner.Command{
		Path:  b.cfg.PythonBin,
		Args:  []string{b.cfg.BridgeScript, mode, "--guard-proxy", b.guardEndpoint},
		Stdin: ytdlprunner.NewInput(payload),
		Env: []string{
			"PYTHONIOENCODING=utf-8",
			"VIDEODL_PATH=" + b.cfg.VideoDLPath,
			"MUSICDL_PATH=" + b.cfg.MusicDLPath,
			"BRIDGE_WORK_DIR=" + b.cfg.WorkDir,
			"MUSICDL_ITEM_LIMIT=" + strconv.Itoa(b.musicDLItemLimit()),
			"HTTP_PROXY=" + b.guardEndpoint,
			"HTTPS_PROXY=" + b.guardEndpoint,
			"ALL_PROXY=" + b.guardEndpoint,
			"NO_PROXY=",
		},
		Dir:     filepath.Dir(filepath.Dir(filepath.Dir(b.cfg.BridgeScript))),
		Timeout: timeout, TerminateProcessGroup: true,
	}
	output, err := b.cfg.Runner.Run(callCtx, command)
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return bridgeResponse{}, fmt.Errorf("universal parser timeout after %s", timeout)
		}
		return bridgeResponse{}, errors.New("isolated universal parser failed")
	}
	if len(output.Stdout) == 0 || len(output.Stdout) > 8<<20 {
		return bridgeResponse{}, errors.New("universal parser output rejected")
	}
	var resp bridgeResponse
	if err := json.Unmarshal(output.Stdout, &resp); err != nil {
		return bridgeResponse{}, errors.New("universal parser returned invalid json")
	}
	if !resp.OK {
		return bridgeResponse{}, errors.New("universal parser reported failure")
	}
	if len(resp.Items) == 0 {
		return bridgeResponse{}, errors.New("universal parser returned empty result")
	}
	return resp, nil
}

func (b *PythonBridge) timeout() time.Duration {
	if b.cfg.BridgeTimeout <= 0 {
		return 60 * time.Second
	}
	return time.Duration(b.cfg.BridgeTimeout) * time.Second
}

func (b *PythonBridge) timeoutForMode(mode string) time.Duration {
	if strings.HasPrefix(mode, "music-") {
		return b.musicDLTimeout()
	}
	return b.timeout()
}

func (b *PythonBridge) musicDLTimeout() time.Duration {
	if b.cfg.MusicDLTimeout <= 0 {
		return 15 * time.Second
	}
	return time.Duration(b.cfg.MusicDLTimeout) * time.Second
}

func (b *PythonBridge) musicDLItemLimit() int {
	limit := b.cfg.MusicDLItemLimit
	if limit <= 0 {
		return 5
	}
	if limit > 20 {
		return 20
	}
	return limit
}

func buildVideoParseData(sourceURL string, items []map[string]any) (ParseData, error) {
	first := firstUsableItem(items)
	if first == nil {
		return ParseData{}, errors.New("no usable universal video result")
	}

	videoURL := guardedOutputURL(firstNonEmpty(stringValue(first, "download_url"), stringValue(first, "url")))
	audioURL := guardedOutputURL(stringValue(first, "audio_download_url"))
	cover := guardedOutputURL(stringValue(first, "cover_url"))
	if videoURL == "" && audioURL == "" {
		return ParseData{}, errors.New("universal video result has no safe media URL")
	}
	title := firstNonEmpty(stringValue(first, "title"), filenameFromURL(videoURL), safeReferenceURL(sourceURL))
	platform := normalizePlatform(stringValue(first, "source"))
	parseType := "video"
	if isM3U8(videoURL) {
		parseType = "m3u8"
	}

	downloads := make([]DownloadItem, 0, 2)
	if videoURL != "" {
		downloads = append(downloads, DownloadItem{URL: videoURL, Label: firstNonEmpty(strings.ToUpper(stringValue(first, "ext")), "video")})
	}
	if audioURL != "" {
		downloads = append(downloads, DownloadItem{URL: audioURL, Label: "audio"})
	}

	return ParseData{
		Platform:  platform,
		Type:      parseType,
		Title:     title,
		Desc:      title,
		Cover:     cover,
		Author:    stringValue(first, "author"),
		Music:     audioURL,
		Duration:  intValue(first, "duration"),
		Downloads: downloads,
		Images:    []string{},
		Pics:      []string{},
		M3U8:      m3u8OrEmpty(videoURL),
		Preview:   firstNonEmpty(videoURL, cover),
		PlayAddr:  videoURL,
		ShareID:   stableID(sourceURL, videoURL, title),
		SourceURL: safeReferenceURL(sourceURL),
		Items:     toMediaItems("video", items),
	}, nil
}

func buildMusicParseData(source string, items []map[string]any) (ParseData, error) {
	first := firstUsableItem(items)
	if first == nil {
		return ParseData{}, errors.New("no usable universal music result")
	}

	audioURL := guardedOutputURL(firstNonEmpty(stringValue(first, "download_url"), stringValue(first, "url")))
	if audioURL == "" {
		return ParseData{}, errors.New("universal music result has no safe media URL")
	}
	title := firstNonEmpty(stringValue(first, "song_name"), stringValue(first, "title"), safeReferenceURL(source))
	author := firstNonEmpty(stringValue(first, "singers"), stringValue(first, "author"))
	cover := guardedOutputURL(stringValue(first, "cover_url"))
	platform := normalizePlatform(firstNonEmpty(stringValue(first, "root_source"), stringValue(first, "source")))

	downloads := []DownloadItem{}
	if audioURL != "" {
		downloads = append(downloads, DownloadItem{URL: audioURL, Label: firstNonEmpty(strings.ToUpper(stringValue(first, "ext")), "audio")})
	}

	return ParseData{
		Platform:  platform,
		Type:      "audio",
		Title:     title,
		Desc:      firstNonEmpty(stringValue(first, "album"), title),
		Cover:     cover,
		Author:    author,
		Music:     audioURL,
		Duration:  intValue(first, "duration_s"),
		Downloads: downloads,
		Images:    []string{},
		Pics:      []string{},
		Preview:   cover,
		ShareID:   stableID(source, audioURL, title),
		SourceURL: safeReferenceURL(source),
		Items:     toMediaItems("audio", items),
	}, nil
}

func firstUsableItem(items []map[string]any) map[string]any {
	for _, item := range items {
		if firstNonEmpty(stringValue(item, "download_url"), stringValue(item, "url"), stringValue(item, "audio_download_url")) != "" {
			return item
		}
	}
	if len(items) > 0 {
		return items[0]
	}
	return nil
}

func toMediaItems(kind string, items []map[string]any) []MediaItem {
	out := make([]MediaItem, 0, len(items))
	for _, item := range items {
		urlValue := guardedOutputURL(firstNonEmpty(stringValue(item, "download_url"), stringValue(item, "url")))
		mediaType := kind
		if kind == "video" && isM3U8(urlValue) {
			mediaType = "m3u8"
		}
		out = append(out, MediaItem{
			Platform: normalizePlatform(firstNonEmpty(stringValue(item, "root_source"), stringValue(item, "source"))),
			Type:     mediaType,
			Title:    firstNonEmpty(stringValue(item, "title"), stringValue(item, "song_name")),
			Cover:    guardedOutputURL(stringValue(item, "cover_url")),
			Author:   firstNonEmpty(stringValue(item, "author"), stringValue(item, "singers")),
			Duration: firstNonZero(intValue(item, "duration"), intValue(item, "duration_s")),
			URL:      urlValue,
			Music:    firstNonEmpty(guardedOutputURL(stringValue(item, "audio_download_url")), urlValue),
		})
	}
	return out
}

func guardedOutputURL(raw string) string {
	target, err := netguard.NewFetchURL(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	result := ""
	_ = target.Use(func(value string) error {
		result = value
		return nil
	})
	return result
}

func safeReferenceURL(raw string) string {
	target, err := netguard.NewFetchURL(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return target.Safe().String()
}

func normalizePlatform(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "VideoClient")
	value = strings.TrimSuffix(value, "MusicClient")
	value = strings.TrimSuffix(value, "Client")
	value = strings.TrimSuffix(value, "Grabber")
	if value == "" {
		return "universal"
	}
	re := regexp.MustCompile(`[^a-zA-Z0-9]+`)
	return strings.ToLower(strings.Trim(re.ReplaceAllString(value, "_"), "_"))
}

func stringValue(item map[string]any, key string) string {
	value, ok := item[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func intValue(item map[string]any, key string) int {
	value, ok := item[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" && value != "NULL" && value != "None" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func isM3U8(raw string) bool {
	return strings.Contains(strings.ToLower(raw), ".m3u8")
}

func m3u8OrEmpty(raw string) string {
	if isM3U8(raw) {
		return raw
	}
	return ""
}

func stableID(parts ...string) string {
	h := sha1.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func filenameFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := filepath.Base(parsed.Path)
	if base == "." || base == "/" {
		return ""
	}
	return base
}
