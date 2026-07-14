package runtimecfg

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

type Settings struct {
	RateLimitEnabled                     bool     `json:"rateLimitEnabled"`
	OutboundProxy                        string   `json:"outboundProxy"`
	HTTPTimeoutSeconds                   int      `json:"httpTimeoutSeconds"`
	YTDLPTimeoutSeconds                  int      `json:"ytDlpTimeoutSeconds"`
	YTDLPBinary                          string   `json:"ytDlpBinary"`
	FFMPEGBinary                         string   `json:"ffmpegBinary"`
	ParserEngine                         string   `json:"parserEngine"`
	ParserFallbackEnabled                bool     `json:"parserFallbackEnabled"`
	UniversalParserTimeoutSeconds        int      `json:"universalParserTimeoutSeconds"`
	UniversalParserPythonBin             string   `json:"universalParserPythonBin"`
	UniversalParserBridgeScript          string   `json:"universalParserBridgeScript"`
	UniversalParserVideoDLPath           string   `json:"universalParserVideoDlPath"`
	UniversalParserMusicDLPath           string   `json:"universalParserMusicDlPath"`
	UniversalParserWorkDir               string   `json:"universalParserWorkDir"`
	UniversalParserMusicDLTimeoutSeconds int      `json:"universalParserMusicDlTimeoutSeconds"`
	UniversalParserMusicDLItemLimit      int      `json:"universalParserMusicDlItemLimit"`
	UniversalParserMusicDLConfigJSON     string   `json:"universalParserMusicDlConfigJson"`
	ToolUpdatesRoot                      string   `json:"toolUpdatesRoot"`
	ToolUpdatesAutoCheckEnabled          bool     `json:"toolUpdatesAutoCheckEnabled"`
	ToolUpdatesAutoUpdateYTDLP           bool     `json:"toolUpdatesAutoUpdateYtDlp"`
	ToolUpdatesAutoUpdateSources         bool     `json:"toolUpdatesAutoUpdateSources"`
	ToolUpdatesIntervalHours             int      `json:"toolUpdatesIntervalHours"`
	ClusterDispatchMode                  string   `json:"clusterDispatchMode"`
	ClusterWorkerEndpoints               []string `json:"clusterWorkerEndpoints"`
	ClusterDisabledNodes                 []string `json:"clusterDisabledNodes"`
	ClusterTestConcurrency               int      `json:"clusterTestConcurrency"`
	ClusterHealthTimeoutSeconds          int      `json:"clusterHealthTimeoutSeconds"`
	ClusterRemoteTestTimeoutSeconds      int      `json:"clusterRemoteTestTimeoutSeconds"`
	DownloadFallbackEnabled              bool     `json:"downloadFallbackEnabled"`
	DownloadFallbackMode                 string   `json:"downloadFallbackMode"`
	DownloadFallbackPublicBaseURL        string   `json:"downloadFallbackPublicBaseUrl"`
	DownloadFallbackCDNBaseURL           string   `json:"downloadFallbackCdnBaseUrl"`
}

type UniversalParserConfig struct {
	PythonBin             string
	BridgeScript          string
	VideoDLPath           string
	MusicDLPath           string
	WorkDir               string
	TimeoutSeconds        int
	MusicDLTimeoutSeconds int
	MusicDLItemLimit      int
	MusicDLConfigJSON     string
}

const (
	ParserEngineNative    = "native"
	ParserEngineUniversal = "universal"

	ClusterDispatchAll     = "all"
	ClusterDispatchLocal   = "local"
	ClusterDispatchWorkers = "workers"

	DownloadFallbackModeCache = "cache"
	DownloadFallbackModeProxy = "proxy"
	DownloadFallbackModeCDN   = "cdn"
)

var (
	mu      sync.RWMutex
	current Settings
)

const settingsFilePath = "cache/runtime-settings.json"

var overseasProxyHostSuffixes = []string{
	"youtube.com",
	"youtu.be",
	"googlevideo.com",
	"ytimg.com",
	"youtubei.googleapis.com",
	"tiktok.com",
	"tiktokv.com",
	"tiktokcdn.com",
	"byteoversea.com",
	"ibytedtos.com",
	"muscdn.com",
	"instagram.com",
	"cdninstagram.com",
	"facebook.com",
	"fb.watch",
	"fbcdn.net",
	"fbsbx.com",
	"vimeo.com",
	"dailymotion.com",
	"dai.ly",
	"x.com",
	"twitter.com",
	"t.co",
	"twimg.com",
	"syndication.twimg.com",
}

func Load() error {
	defaultSettings := defaults()
	settings := defaultSettings
	bytes, err := os.ReadFile(settingsFilePath)
	if err == nil {
		var stored Settings
		if decodeErr := json.Unmarshal(bytes, &stored); decodeErr == nil {
			settings = merge(settings, stored)
			if !jsonObjectHasField(bytes, "clusterWorkerEndpoints") {
				settings.ClusterWorkerEndpoints = append([]string(nil), defaultSettings.ClusterWorkerEndpoints...)
			}
			if !jsonObjectHasField(bytes, "downloadFallbackEnabled") {
				settings.DownloadFallbackEnabled = defaultSettings.DownloadFallbackEnabled
			}
			if !jsonObjectHasField(bytes, "downloadFallbackMode") {
				settings.DownloadFallbackMode = defaultSettings.DownloadFallbackMode
			}
			if !jsonObjectHasField(bytes, "downloadFallbackPublicBaseUrl") {
				settings.DownloadFallbackPublicBaseURL = defaultSettings.DownloadFallbackPublicBaseURL
			}
			if !jsonObjectHasField(bytes, "downloadFallbackCdnBaseUrl") {
				settings.DownloadFallbackCDNBaseURL = defaultSettings.DownloadFallbackCDNBaseURL
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := normalizeAndValidate(&settings); err != nil {
		return err
	}

	mu.Lock()
	current = settings
	mu.Unlock()
	return nil
}

func Current() Settings {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

func Update(next Settings) (Settings, error) {
	settings := merge(defaults(), next)
	if err := normalizeAndValidate(&settings); err != nil {
		return Settings{}, err
	}

	if err := os.MkdirAll(filepath.Dir(settingsFilePath), 0o755); err != nil {
		return Settings{}, err
	}
	bytes, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return Settings{}, err
	}
	if err := os.WriteFile(settingsFilePath, bytes, 0o644); err != nil {
		return Settings{}, err
	}

	mu.Lock()
	current = settings
	mu.Unlock()
	return settings, nil
}

func ProxyURLString() string {
	return strings.TrimSpace(Current().OutboundProxy)
}

func ProxyURLStringForTarget(rawURL string) string {
	if !ShouldUseProxyForTarget(rawURL) {
		return ""
	}
	return ProxyURLString()
}

func HTTPTimeout() time.Duration {
	seconds := Current().HTTPTimeoutSeconds
	if seconds <= 0 {
		seconds = defaults().HTTPTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func YTDLPTimeout() time.Duration {
	seconds := Current().YTDLPTimeoutSeconds
	if seconds <= 0 {
		seconds = defaults().YTDLPTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func YTDLPBinary() string {
	return firstNonEmpty(Current().YTDLPBinary, os.Getenv("YT_DLP_BINARY"), "yt-dlp")
}

func FFMPEGBinary() string {
	return firstNonEmpty(Current().FFMPEGBinary, os.Getenv("FFMPEG_BINARY"), "ffmpeg")
}

func ToolUpdatesRoot() string {
	return firstNonEmpty(Current().ToolUpdatesRoot, os.Getenv("TOOL_UPDATES_ROOT"), filepath.Join("tools"))
}

func ToolUpdatesInterval() time.Duration {
	hours := Current().ToolUpdatesIntervalHours
	if hours <= 0 {
		hours = defaults().ToolUpdatesIntervalHours
	}
	return time.Duration(hours) * time.Hour
}

func ParserEngine() string {
	engine := strings.ToLower(strings.TrimSpace(Current().ParserEngine))
	switch engine {
	case ParserEngineUniversal:
		return ParserEngineUniversal
	default:
		return ParserEngineNative
	}
}

func ParserFallbackEnabled() bool {
	return Current().ParserFallbackEnabled
}

func RateLimitEnabled() bool {
	return Current().RateLimitEnabled
}

func DownloadFallbackEnabled() bool {
	return Current().DownloadFallbackEnabled
}

func DownloadFallbackMode() string {
	mode := strings.ToLower(strings.TrimSpace(Current().DownloadFallbackMode))
	switch mode {
	case DownloadFallbackModeProxy:
		return DownloadFallbackModeProxy
	case DownloadFallbackModeCDN:
		return DownloadFallbackModeCDN
	default:
		return DownloadFallbackModeCache
	}
}

func DownloadFallbackPublicBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(Current().DownloadFallbackPublicBaseURL), "/")
}

func DownloadFallbackCDNBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(Current().DownloadFallbackCDNBaseURL), "/")
}

func UniversalParser() UniversalParserConfig {
	settings := Current()
	return UniversalParserConfig{
		PythonBin:             firstNonEmpty(settings.UniversalParserPythonBin, "python"),
		BridgeScript:          settings.UniversalParserBridgeScript,
		VideoDLPath:           settings.UniversalParserVideoDLPath,
		MusicDLPath:           settings.UniversalParserMusicDLPath,
		WorkDir:               settings.UniversalParserWorkDir,
		TimeoutSeconds:        settings.UniversalParserTimeoutSeconds,
		MusicDLTimeoutSeconds: settings.UniversalParserMusicDLTimeoutSeconds,
		MusicDLItemLimit:      settings.UniversalParserMusicDLItemLimit,
		MusicDLConfigJSON:     settings.UniversalParserMusicDLConfigJSON,
	}
}

func ProxyFunc(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil || !ShouldUseProxyForTarget(req.URL.String()) {
		return nil, nil
	}
	proxyRaw := strings.TrimSpace(Current().OutboundProxy)
	if proxyRaw == "" {
		return nil, nil
	}
	return url.Parse(proxyRaw)
}

func NewHTTPTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok || base == nil {
		return &http.Transport{Proxy: ProxyFunc}
	}
	clone := base.Clone()
	clone.Proxy = ProxyFunc
	return clone
}

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: NewHTTPTransport(),
		Timeout:   HTTPTimeout(),
	}
}

func NewRestyClient() *resty.Client {
	client := resty.New()
	client.SetTransport(NewHTTPTransport())
	client.SetTimeout(HTTPTimeout())
	return client
}

func ApplyGlobalHTTPSettings() {
	http.DefaultTransport = NewHTTPTransport()
	http.DefaultClient = &http.Client{
		Transport: http.DefaultTransport,
		Timeout:   HTTPTimeout(),
	}
}

func defaults() Settings {
	wd, _ := os.Getwd()
	toolsRoot := firstNonEmpty(os.Getenv("TOOL_UPDATES_ROOT"), filepath.Join(wd, "tools"))
	thirdPartyRoot := filepath.Join(wd, "third_party", "CharlesPikachu")
	return Settings{
		RateLimitEnabled:              envBool("RATE_LIMIT_ENABLED", false),
		OutboundProxy:                 firstNonEmpty(os.Getenv("OUTBOUND_PROXY"), os.Getenv("HTTPS_PROXY"), os.Getenv("HTTP_PROXY")),
		HTTPTimeoutSeconds:            30,
		YTDLPTimeoutSeconds:           60,
		YTDLPBinary:                   strings.TrimSpace(os.Getenv("YT_DLP_BINARY")),
		FFMPEGBinary:                  strings.TrimSpace(os.Getenv("FFMPEG_BINARY")),
		ParserEngine:                  firstNonEmpty(os.Getenv("PARSER_ENGINE"), ParserEngineNative),
		ParserFallbackEnabled:         envBool("PARSER_FALLBACK_ENABLED", false),
		UniversalParserTimeoutSeconds: envInt("UNIVERSAL_PARSER_TIMEOUT_SECONDS", 60),
		UniversalParserPythonBin:      firstNonEmpty(os.Getenv("UNIVERSAL_PARSER_PYTHON_BIN"), "python"),
		UniversalParserBridgeScript:   firstNonEmpty(os.Getenv("UNIVERSAL_PARSER_BRIDGE_SCRIPT"), filepath.Join(wd, "bridges", "universal", "python", "bridge.py")),
		UniversalParserVideoDLPath: firstExisting(
			os.Getenv("UNIVERSAL_PARSER_VIDEODL_PATH"),
			filepath.Join(thirdPartyRoot, "videodl"),
			filepath.Join(wd, "..", "video-backend", "upstreams", "videodl"),
			filepath.Join(wd, "..", "参考源码", "后端", "videodl"),
		),
		UniversalParserMusicDLPath: firstExisting(
			os.Getenv("UNIVERSAL_PARSER_MUSICDL_PATH"),
			filepath.Join(thirdPartyRoot, "musicdl"),
			filepath.Join(wd, "..", "video-backend", "upstreams", "musicdl"),
		),
		UniversalParserWorkDir:               firstNonEmpty(os.Getenv("UNIVERSAL_PARSER_WORK_DIR"), filepath.Join(wd, "cache", "universal-parser")),
		UniversalParserMusicDLTimeoutSeconds: envInt("UNIVERSAL_PARSER_MUSICDL_TIMEOUT_SECONDS", 15),
		UniversalParserMusicDLItemLimit:      envInt("UNIVERSAL_PARSER_MUSICDL_ITEM_LIMIT", 5),
		UniversalParserMusicDLConfigJSON:     strings.TrimSpace(os.Getenv("UNIVERSAL_PARSER_MUSICDL_CONFIG_JSON")),
		ToolUpdatesRoot:                      toolsRoot,
		ToolUpdatesAutoCheckEnabled:          envBool("TOOL_UPDATES_AUTO_CHECK_ENABLED", false),
		ToolUpdatesAutoUpdateYTDLP:           envBool("TOOL_UPDATES_AUTO_UPDATE_YTDLP", false),
		ToolUpdatesAutoUpdateSources:         envBool("TOOL_UPDATES_AUTO_UPDATE_SOURCES", false),
		ToolUpdatesIntervalHours:             envInt("TOOL_UPDATES_INTERVAL_HOURS", 24),
		ClusterDispatchMode:                  firstNonEmpty(os.Getenv("CLUSTER_DISPATCH_MODE"), ClusterDispatchAll),
		ClusterWorkerEndpoints:               parseStringList(os.Getenv("CLUSTER_WORKER_ENDPOINTS")),
		ClusterTestConcurrency:               envInt("ADMIN_CLUSTER_TEST_CONCURRENCY", 3),
		ClusterHealthTimeoutSeconds:          envInt("CLUSTER_HEALTH_TIMEOUT_SECONDS", 2),
		ClusterRemoteTestTimeoutSeconds:      envInt("ADMIN_CLUSTER_TEST_TIMEOUT_SECONDS", 120),
		DownloadFallbackEnabled:              envBool("DOWNLOAD_FALLBACK_ENABLED", false),
		DownloadFallbackMode:                 firstNonEmpty(os.Getenv("DOWNLOAD_FALLBACK_MODE"), DownloadFallbackModeCache),
		DownloadFallbackPublicBaseURL:        strings.TrimSpace(os.Getenv("DOWNLOAD_FALLBACK_PUBLIC_BASE_URL")),
		DownloadFallbackCDNBaseURL:           strings.TrimSpace(os.Getenv("DOWNLOAD_FALLBACK_CDN_BASE_URL")),
	}
}

func merge(base, override Settings) Settings {
	base.RateLimitEnabled = override.RateLimitEnabled
	if strings.TrimSpace(override.OutboundProxy) != "" || override.OutboundProxy == "" {
		base.OutboundProxy = strings.TrimSpace(override.OutboundProxy)
	}
	if override.HTTPTimeoutSeconds > 0 {
		base.HTTPTimeoutSeconds = override.HTTPTimeoutSeconds
	}
	if override.YTDLPTimeoutSeconds > 0 {
		base.YTDLPTimeoutSeconds = override.YTDLPTimeoutSeconds
	}
	if strings.TrimSpace(override.YTDLPBinary) != "" {
		base.YTDLPBinary = strings.TrimSpace(override.YTDLPBinary)
	}
	if strings.TrimSpace(override.FFMPEGBinary) != "" {
		base.FFMPEGBinary = strings.TrimSpace(override.FFMPEGBinary)
	}
	if strings.TrimSpace(override.ParserEngine) != "" {
		base.ParserEngine = strings.TrimSpace(override.ParserEngine)
	}
	base.ParserFallbackEnabled = override.ParserFallbackEnabled
	if override.UniversalParserTimeoutSeconds > 0 {
		base.UniversalParserTimeoutSeconds = override.UniversalParserTimeoutSeconds
	}
	if strings.TrimSpace(override.UniversalParserPythonBin) != "" {
		base.UniversalParserPythonBin = strings.TrimSpace(override.UniversalParserPythonBin)
	}
	if strings.TrimSpace(override.UniversalParserBridgeScript) != "" {
		base.UniversalParserBridgeScript = strings.TrimSpace(override.UniversalParserBridgeScript)
	}
	if strings.TrimSpace(override.UniversalParserVideoDLPath) != "" {
		base.UniversalParserVideoDLPath = strings.TrimSpace(override.UniversalParserVideoDLPath)
	}
	if strings.TrimSpace(override.UniversalParserMusicDLPath) != "" {
		base.UniversalParserMusicDLPath = strings.TrimSpace(override.UniversalParserMusicDLPath)
	}
	if strings.TrimSpace(override.UniversalParserWorkDir) != "" {
		base.UniversalParserWorkDir = strings.TrimSpace(override.UniversalParserWorkDir)
	}
	if override.UniversalParserMusicDLTimeoutSeconds > 0 {
		base.UniversalParserMusicDLTimeoutSeconds = override.UniversalParserMusicDLTimeoutSeconds
	}
	if override.UniversalParserMusicDLItemLimit > 0 {
		base.UniversalParserMusicDLItemLimit = override.UniversalParserMusicDLItemLimit
	}
	if strings.TrimSpace(override.UniversalParserMusicDLConfigJSON) != "" {
		base.UniversalParserMusicDLConfigJSON = strings.TrimSpace(override.UniversalParserMusicDLConfigJSON)
	}
	if strings.TrimSpace(override.ToolUpdatesRoot) != "" {
		base.ToolUpdatesRoot = strings.TrimSpace(override.ToolUpdatesRoot)
	}
	base.ToolUpdatesAutoCheckEnabled = override.ToolUpdatesAutoCheckEnabled
	base.ToolUpdatesAutoUpdateYTDLP = override.ToolUpdatesAutoUpdateYTDLP
	base.ToolUpdatesAutoUpdateSources = override.ToolUpdatesAutoUpdateSources
	if override.ToolUpdatesIntervalHours > 0 {
		base.ToolUpdatesIntervalHours = override.ToolUpdatesIntervalHours
	}
	if strings.TrimSpace(override.ClusterDispatchMode) != "" {
		base.ClusterDispatchMode = strings.TrimSpace(override.ClusterDispatchMode)
	}
	base.ClusterWorkerEndpoints = append([]string(nil), override.ClusterWorkerEndpoints...)
	base.ClusterDisabledNodes = append([]string(nil), override.ClusterDisabledNodes...)
	if override.ClusterTestConcurrency > 0 {
		base.ClusterTestConcurrency = override.ClusterTestConcurrency
	}
	if override.ClusterHealthTimeoutSeconds > 0 {
		base.ClusterHealthTimeoutSeconds = override.ClusterHealthTimeoutSeconds
	}
	if override.ClusterRemoteTestTimeoutSeconds > 0 {
		base.ClusterRemoteTestTimeoutSeconds = override.ClusterRemoteTestTimeoutSeconds
	}
	base.DownloadFallbackEnabled = override.DownloadFallbackEnabled
	if strings.TrimSpace(override.DownloadFallbackMode) != "" {
		base.DownloadFallbackMode = strings.TrimSpace(override.DownloadFallbackMode)
	}
	base.DownloadFallbackPublicBaseURL = strings.TrimSpace(override.DownloadFallbackPublicBaseURL)
	base.DownloadFallbackCDNBaseURL = strings.TrimSpace(override.DownloadFallbackCDNBaseURL)
	return base
}

func normalizeAndValidate(settings *Settings) error {
	settings.OutboundProxy = strings.TrimSpace(settings.OutboundProxy)
	settings.YTDLPBinary = strings.TrimSpace(settings.YTDLPBinary)
	settings.FFMPEGBinary = strings.TrimSpace(settings.FFMPEGBinary)
	settings.ParserEngine = strings.ToLower(strings.TrimSpace(settings.ParserEngine))
	settings.UniversalParserPythonBin = strings.TrimSpace(settings.UniversalParserPythonBin)
	settings.UniversalParserBridgeScript = strings.TrimSpace(settings.UniversalParserBridgeScript)
	settings.UniversalParserVideoDLPath = strings.TrimSpace(settings.UniversalParserVideoDLPath)
	settings.UniversalParserMusicDLPath = strings.TrimSpace(settings.UniversalParserMusicDLPath)
	settings.UniversalParserWorkDir = strings.TrimSpace(settings.UniversalParserWorkDir)
	settings.UniversalParserMusicDLConfigJSON = strings.TrimSpace(settings.UniversalParserMusicDLConfigJSON)
	settings.ToolUpdatesRoot = strings.TrimSpace(settings.ToolUpdatesRoot)
	settings.ClusterDispatchMode = strings.ToLower(strings.TrimSpace(settings.ClusterDispatchMode))
	settings.ClusterWorkerEndpoints = normalizeStringList(settings.ClusterWorkerEndpoints)
	settings.ClusterDisabledNodes = normalizeStringList(settings.ClusterDisabledNodes)
	settings.DownloadFallbackMode = strings.ToLower(strings.TrimSpace(settings.DownloadFallbackMode))
	settings.DownloadFallbackPublicBaseURL = strings.TrimRight(strings.TrimSpace(settings.DownloadFallbackPublicBaseURL), "/")
	settings.DownloadFallbackCDNBaseURL = strings.TrimRight(strings.TrimSpace(settings.DownloadFallbackCDNBaseURL), "/")

	if settings.OutboundProxy != "" {
		proxyURL, err := url.Parse(settings.OutboundProxy)
		if err != nil {
			return errors.New("proxy url is invalid")
		}
		switch strings.ToLower(proxyURL.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return errors.New("proxy scheme must be http/https/socks5/socks5h")
		}
		if proxyURL.Host == "" {
			return errors.New("proxy host is empty")
		}
	}

	if settings.HTTPTimeoutSeconds <= 0 {
		settings.HTTPTimeoutSeconds = defaults().HTTPTimeoutSeconds
	}
	if settings.YTDLPTimeoutSeconds <= 0 {
		settings.YTDLPTimeoutSeconds = defaults().YTDLPTimeoutSeconds
	}
	if settings.ParserEngine == "" {
		settings.ParserEngine = ParserEngineNative
	}
	switch settings.ParserEngine {
	case ParserEngineNative, ParserEngineUniversal:
	default:
		return errors.New("parser engine must be native or universal")
	}
	if settings.UniversalParserTimeoutSeconds <= 0 {
		settings.UniversalParserTimeoutSeconds = defaults().UniversalParserTimeoutSeconds
	}
	if settings.UniversalParserPythonBin == "" {
		settings.UniversalParserPythonBin = defaults().UniversalParserPythonBin
	}
	if settings.UniversalParserBridgeScript == "" {
		settings.UniversalParserBridgeScript = defaults().UniversalParserBridgeScript
	}
	if settings.UniversalParserWorkDir == "" {
		settings.UniversalParserWorkDir = defaults().UniversalParserWorkDir
	}
	if settings.UniversalParserMusicDLTimeoutSeconds <= 0 {
		settings.UniversalParserMusicDLTimeoutSeconds = defaults().UniversalParserMusicDLTimeoutSeconds
	}
	if settings.UniversalParserMusicDLTimeoutSeconds > 120 {
		settings.UniversalParserMusicDLTimeoutSeconds = 120
	}
	if settings.UniversalParserMusicDLItemLimit <= 0 {
		settings.UniversalParserMusicDLItemLimit = defaults().UniversalParserMusicDLItemLimit
	}
	if settings.UniversalParserMusicDLItemLimit > 20 {
		settings.UniversalParserMusicDLItemLimit = 20
	}
	if settings.UniversalParserMusicDLConfigJSON != "" && !json.Valid([]byte(settings.UniversalParserMusicDLConfigJSON)) {
		return errors.New("musicdl config json is invalid")
	}
	if settings.ToolUpdatesRoot == "" {
		settings.ToolUpdatesRoot = defaults().ToolUpdatesRoot
	}
	if settings.ToolUpdatesIntervalHours <= 0 {
		settings.ToolUpdatesIntervalHours = defaults().ToolUpdatesIntervalHours
	}
	if settings.ToolUpdatesIntervalHours > 24*7 {
		settings.ToolUpdatesIntervalHours = 24 * 7
	}
	if settings.ClusterDispatchMode == "" {
		settings.ClusterDispatchMode = ClusterDispatchAll
	}
	switch settings.ClusterDispatchMode {
	case ClusterDispatchAll, ClusterDispatchLocal, ClusterDispatchWorkers:
	default:
		return errors.New("cluster dispatch mode must be all/local/workers")
	}
	if settings.ClusterTestConcurrency <= 0 {
		settings.ClusterTestConcurrency = defaults().ClusterTestConcurrency
	}
	if settings.ClusterTestConcurrency > 16 {
		settings.ClusterTestConcurrency = 16
	}
	if settings.ClusterHealthTimeoutSeconds <= 0 {
		settings.ClusterHealthTimeoutSeconds = defaults().ClusterHealthTimeoutSeconds
	}
	if settings.ClusterHealthTimeoutSeconds > 30 {
		settings.ClusterHealthTimeoutSeconds = 30
	}
	if settings.ClusterRemoteTestTimeoutSeconds <= 0 {
		settings.ClusterRemoteTestTimeoutSeconds = defaults().ClusterRemoteTestTimeoutSeconds
	}
	if settings.ClusterRemoteTestTimeoutSeconds > 600 {
		settings.ClusterRemoteTestTimeoutSeconds = 600
	}
	if settings.DownloadFallbackMode == "" {
		settings.DownloadFallbackMode = DownloadFallbackModeCache
	}
	switch settings.DownloadFallbackMode {
	case DownloadFallbackModeCache, DownloadFallbackModeProxy, DownloadFallbackModeCDN:
	default:
		return errors.New("download fallback mode must be cache/proxy/cdn")
	}
	if err := validateOptionalPublicBaseURL(settings.DownloadFallbackPublicBaseURL, "download fallback public base url"); err != nil {
		return err
	}
	if err := validateOptionalPublicBaseURL(settings.DownloadFallbackCDNBaseURL, "download fallback cdn base url"); err != nil {
		return err
	}
	return nil
}

func validateOptionalPublicBaseURL(rawURL, label string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return errors.New(label + " is invalid")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return errors.New(label + " scheme must be http or https")
	}
	if parsed.Host == "" {
		return errors.New(label + " host is empty")
	}
	return nil
}

func parseStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return normalizeStringList(strings.Split(raw, ","))
}

func jsonObjectHasField(bytes []byte, field string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(bytes, &raw); err != nil {
		return false
	}
	_, ok := raw[field]
	return ok
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func ShouldUseProxyForTarget(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return shouldUseProxyForHost(parsed.Hostname())
}

func shouldUseProxyForHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, suffix := range overseasProxyHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
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

func firstExisting(paths ...string) string {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
