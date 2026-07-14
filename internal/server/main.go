package server

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	adminweb "watermark-backend/internal/admin/web"
	"watermark-backend/internal/parsers/native"
	"watermark-backend/internal/runtimecfg"
	"watermark-backend/internal/utils"
)

type httpResponse struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

type parseRequest struct {
	URL          string `json:"url"`
	ForceRefresh bool   `json:"forceRefresh,omitempty"`
	Source       int    `json:"source,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
	Signature    string `json:"signature,omitempty"`
	Version      int    `json:"version,omitempty"`
}

type parseData struct {
	Platform  string         `json:"platform"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Desc      string         `json:"desc"`
	Cover     string         `json:"cover"`
	Author    string         `json:"author"`
	Avatar    string         `json:"avatar"`
	Music     string         `json:"music"`
	MP3       string         `json:"mp3,omitempty"`
	Audio     string         `json:"audio,omitempty"`
	AudioURL  string         `json:"audioUrl,omitempty"`
	Duration  int            `json:"duration"`
	Downloads []downloadItem `json:"downloads"`
	Images    []string       `json:"images"`
	Pics      []string       `json:"pics"`
	M3U8      string         `json:"m3u8"`
	Preview   string         `json:"previewUrl"`
	PlayAddr  string         `json:"playAddr"`
	ShareID   string         `json:"shareId,omitempty"`
	SourceURL string         `json:"sourceUrl,omitempty"`
}

type downloadItem struct {
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

const (
	v1ErrMissingParameter    = "MISSING_PARAMETER"
	v1ErrUnsupportedURL      = "UNSUPPORTED_URL"
	v1ErrUnsupportedSource   = "UNSUPPORTED_SOURCE"
	v1ErrIDParseNotSupported = "ID_PARSE_NOT_SUPPORTED"
	v1ErrParseFailed         = "PARSE_FAILED"
)

type apiV1Response struct {
	Status string      `json:"status"`
	Data   interface{} `json:"data"`
}

type apiV1ErrorResponse struct {
	Status string     `json:"status"`
	Error  apiV1Error `json:"error"`
}

type apiV1Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type platformInfo struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	URLParse bool   `json:"url_parse"`
	IDParse  bool   `json:"id_parse"`
}

var platformNames = map[string]string{
	"acfun":        "AcFun",
	"bilibili":     "哔哩哔哩",
	"cctv":         "央视网",
	"doupai":       "逗拍",
	"douyin":       "抖音",
	"haokan":       "好看视频",
	"huoshan":      "火山",
	"huya":         "虎牙",
	"kuaishou":     "快手",
	"lishipin":     "梨视频",
	"lvzhou":       "绿洲",
	"meipai":       "美拍",
	"pipigaoxiao":  "皮皮搞笑",
	"pipixia":      "皮皮虾",
	"qqvideo":      "腾讯视频",
	"quanmin":      "度小视",
	"quanminkge":   "全民K歌",
	"redbook":      "小红书",
	"sixroom":      "六间房",
	"sohu":         "搜狐视频",
	"twitter":      "X/Twitter",
	"weibo":        "微博",
	"weishi":       "微视",
	"xigua":        "西瓜视频",
	"xinpianchang": "新片场",
	"zuiyou":       "最右",
}

var looseURLPattern = regexp.MustCompile(`https?://[^\s]+`)

func Run() {
	if maybeRunMigrationCommand() {
		return
	}
	startHTTPServer()
}

func startHTTPServer() {
	if err := validateCurrentLegacyProductionConfig(); err != nil {
		panic(err)
	}
	logResources, err := setupLogging()
	if err != nil {
		panic(err)
	}
	defer logResources.Close()

	if err := runtimecfg.Load(); err != nil {
		logErrorf("load runtime settings failed: %v", err)
		panic(err)
	}
	applyRuntimeSettings()
	if err := initInfrastructure(context.Background()); err != nil {
		logErrorf("initialize infrastructure failed: %v", err)
		panic(err)
	}
	seedSharedRuntimeSettingsIfMissing()
	if loadSharedRuntimeSettings(true) {
		applyRuntimeSettings()
	}
	globalParseResultCache.configure(appInfra.mysql, appInfra.redis)
	markInterruptedPersistentTasks()
	stopToolAutoUpdater := startToolAutoUpdater()
	defer stopToolAutoUpdater()
	stopDownloadFallbackCleaner := startDownloadFallbackCleaner()
	defer stopDownloadFallbackCleaner()
	defer closeInfrastructure()
	logInfof(
		"runtime settings loaded proxy_configured=%t http_timeout=%s ytdlp_timeout=%s parser_engine=%s parser_fallback=%t ytdlp_binary=%s ffmpeg_binary=%s",
		strings.TrimSpace(runtimecfg.ProxyURLString()) != "",
		runtimecfg.HTTPTimeout(),
		runtimecfg.YTDLPTimeout(),
		runtimecfg.ParserEngine(),
		runtimecfg.ParserFallbackEnabled(),
		runtimecfg.YTDLPBinary(),
		runtimecfg.FFMPEGBinary(),
	)

	r := gin.Default()

	sub, err := adminweb.TemplatesFS()
	if err != nil {
		logErrorf("load templates failed: %v", err)
		panic(err)
	}

	tmpl := template.Must(template.ParseFS(sub, "*.html"))
	r.SetHTMLTemplate(tmpl)

	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/admin")
	})
	r.GET("/admin/login", handleAdminLoginPage)
	r.POST("/admin/login", rateLimitMiddleware("admin-login", envInt("ADMIN_LOGIN_RATE_LIMIT_PER_MINUTE", 10), time.Minute), handleAdminLogin)
	r.POST("/admin/logout", handleAdminLogout)
	r.GET("/admin", requireAdminSession(), handleAdminPage)
	adminAPI := r.Group("/admin/api")
	adminAPI.Use(requireAdminSession())
	{
		adminAPI.GET("/summary", handleAdminSummary)
		adminAPI.POST("/parse", handleAdminParse)
		adminAPI.GET("/parse/attempts", handleAdminParseAttemptStats)
		adminAPI.POST("/test/platforms", rateLimitMiddleware("admin-platform-test", envInt("ADMIN_TEST_RATE_LIMIT_PER_MINUTE", 6), time.Minute), handleAdminRunPlatformTests)
		adminAPI.POST("/test/platform-runs", rateLimitMiddleware("admin-platform-test-run", envInt("ADMIN_TEST_RUN_RATE_LIMIT_PER_MINUTE", 3), time.Minute), handleAdminStartPlatformTestRun)
		adminAPI.GET("/test/platform-runs/latest", handleAdminLatestPlatformTestRun)
		adminAPI.GET("/test/platform-runs/:run_id", handleAdminGetPlatformTestRun)
		adminAPI.GET("/test/samples", handleAdminListTestSamples)
		adminAPI.POST("/test/samples", handleAdminSaveTestSamples)
		adminAPI.POST("/test/samples/reset", handleAdminResetTestSamples)
		adminAPI.GET("/wechat-domains", handleAdminListWechatDomains)
		adminAPI.PATCH("/wechat-domains/:id", handleAdminUpdateWechatDomain)
		adminAPI.POST("/wechat-domains/export", handleAdminRefreshWechatDomainExport)
		adminAPI.GET("/cache", handleAdminListParseCache)
		adminAPI.GET("/cache/:id", handleAdminGetParseCache)
		adminAPI.DELETE("/cache/:id", handleAdminDeleteParseCache)
		adminAPI.DELETE("/cache", handleAdminClearParseCache)
		adminAPI.GET("/tasks", handleAdminListTasks)
		adminAPI.GET("/download-fallback", handleAdminDownloadFallback)
		adminAPI.GET("/requests", handleAdminRecentRequests)
		adminAPI.GET("/logs", handleAdminLogs)
		adminAPI.GET("/diagnostics", handleAdminDiagnostics)
		adminAPI.GET("/tools", handleAdminToolStatus)
		adminAPI.POST("/tools/check", handleAdminToolCheck)
		adminAPI.POST("/tools/:component/update", handleAdminToolUpdate)
	}
	r.GET("/preview/player", func(c *gin.Context) {
		c.HTML(http.StatusOK, "preview.html", gin.H{
			"title":    strings.TrimSpace(c.Query("title")),
			"src":      strings.TrimSpace(c.Query("src")),
			"fallback": strings.TrimSpace(c.Query("fallback")),
			"poster":   strings.TrimSpace(c.Query("poster")),
		})
	})
	r.GET("/settings", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/admin#settings")
	})

	r.GET("/api/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, httpResponse{
			Code: 0,
			Msg:  "ok",
			Data: gin.H{
				"node":           currentClusterNodeInfo(),
				"infrastructure": currentInfrastructureStatus(c.Request.Context()),
			},
		})
	})
	r.POST("/api/internal/platform-test", handleInternalPlatformTest)
	r.POST("/api/client/session", rateLimitMiddleware("api-client-session", envInt("CLIENT_SESSION_RATE_LIMIT_PER_MINUTE", 20), time.Minute), handleClientSessionCreate)

	r.GET("/api/settings", requireAdminSession(), handleGetSettings)
	r.POST("/api/settings", requireAdminSession(), handleSaveSettings)

	r.GET("/api/v1/health", apiV1HealthHandler)
	r.GET("/api/v1/platforms", apiV1PlatformsHandler)
	r.GET("/api/v1/parse", apiV1ParseURLHandler)
	r.GET("/api/v1/parse/:source/:video_id", apiV1ParseIDHandler)
	r.GET("/api/parse/cache/:id", handleParseCache)

	r.GET("/video/share/url/parse", func(c *gin.Context) {
		result, _, err := parseShareRequestTracked(c, c.Query("url"), "legacy.share_url_parse", parseRequestOptions{})
		if err != nil {
			c.JSON(http.StatusOK, httpResponse{Code: 201, Msg: err.Error()})
			return
		}

		c.JSON(http.StatusOK, httpResponse{Code: 200, Msg: "ok", Data: result.info})
	})

	r.GET("/video/id/parse", func(c *gin.Context) {
		parseRes, err := parser.ParseVideoId(c.Query("source"), c.Query("video_id"))
		if err != nil {
			c.JSON(http.StatusOK, httpResponse{Code: 201, Msg: err.Error()})
			return
		}

		c.JSON(http.StatusOK, httpResponse{Code: 200, Msg: "ok", Data: parseRes})
	})

	r.POST("/api/parse", rateLimitMiddleware("api-parse", envInt("PARSE_RATE_LIMIT_PER_MINUTE", 30), time.Minute), func(c *gin.Context) {
		var req parseRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, compatErrorResponse(err))
			return
		}
		if !validateClientParseSignature(c, req) {
			return
		}

		options := parseRequestOptions{}
		entrypoint := "api.parse"
		if req.ForceRefresh {
			options.BypassCache = true
			options.BypassFailureCache = true
			entrypoint = "api.parse.refresh"
		}
		result, _, err := parseShareRequestTracked(c, req.URL, entrypoint, options)
		if err != nil {
			c.JSON(http.StatusOK, compatErrorResponse(err))
			return
		}

		c.JSON(http.StatusOK, httpResponse{
			Code: 0,
			Msg:  "ok",
			Data: result.data,
		})
	})

	r.GET("/api/hybrid/video_data", rateLimitMiddleware("api-hybrid", envInt("PARSE_RATE_LIMIT_PER_MINUTE", 30), time.Minute), func(c *gin.Context) {
		result, _, err := parseShareRequestTracked(c, c.Query("url"), "api.hybrid.video_data", parseRequestOptions{})
		if err != nil {
			c.JSON(http.StatusOK, compatErrorResponse(err))
			return
		}

		c.JSON(http.StatusOK, httpResponse{
			Code: 0,
			Msg:  "ok",
			Data: result.data,
		})
	})

	r.POST("/api/profile", unsupportedHandler)
	r.GET("/api/m3u8/merge", rateLimitMiddleware("api-m3u8", envInt("M3U8_RATE_LIMIT_PER_MINUTE", 10), time.Minute), handleM3U8Merge)
	r.GET("/api/task/:id", handleTaskStatus)
	r.GET("/api/task/file/:id", handleTaskFile)
	r.POST("/api/download/fallback", rateLimitMiddleware("api-download-fallback", envInt("DOWNLOAD_FALLBACK_RATE_LIMIT_PER_MINUTE", 10), time.Minute), handleDownloadFallbackCreate)
	r.GET("/api/download/status/:ticket", handleDownloadFallbackPublicStatus)
	r.GET("/api/download/proxy/:ticket", handleDownloadFallbackProxy)
	r.GET("/api/download/cdn/:ticket", handleDownloadFallbackPublicFile)
	r.GET("/api/download/node/:node/fallback/:id", handleDownloadFallbackNodeStatus)
	r.GET("/api/download/node/:node/file/:key", handleDownloadFallbackNodeFile)
	r.GET("/api/download/fallback/:id", handleDownloadFallbackStatus)
	r.GET("/api/download/file/:key", handleDownloadFallbackFile)
	r.GET("/api/internal/download/fallback/:id", handleInternalDownloadFallbackStatus)
	r.GET("/api/internal/download/file/:key", handleInternalDownloadFallbackFile)
	r.GET("/api/internal/download/proxy/:ticket", handleInternalDownloadFallbackProxy)

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "5001"
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logInfof("http server starting addr=%s", srv.Addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logErrorf("http server stopped unexpectedly: %v", err)
			panic(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	logInfof("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logErrorf("http server shutdown failed: %v", err)
		return
	}
	logInfof("http server stopped")
}

func unsupportedHandler(c *gin.Context) {
	c.JSON(http.StatusOK, httpResponse{
		Code: 1002,
		Msg:  "unsupported platform",
	})
}

func sendV1Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, apiV1Response{Status: "success", Data: data})
}

func sendV1Error(c *gin.Context, httpStatus int, code string, message string) {
	c.JSON(httpStatus, apiV1ErrorResponse{
		Status: "error",
		Error:  apiV1Error{Code: code, Message: message},
	})
}

func apiV1HealthHandler(c *gin.Context) {
	sendV1Success(c, gin.H{
		"status":    "ok",
		"version":   "watermark-backend",
		"platforms": len(parser.VideoSourceInfoMapping),
	})
}

func apiV1PlatformsHandler(c *gin.Context) {
	platforms := make([]platformInfo, 0, len(parser.VideoSourceInfoMapping))
	for source, info := range parser.VideoSourceInfoMapping {
		name := source
		if displayName, ok := platformNames[source]; ok {
			name = displayName
		}
		platforms = append(platforms, platformInfo{
			Source:   source,
			Name:     name,
			URLParse: info.VideoShareUrlParser != nil,
			IDParse:  info.VideoIdParser != nil,
		})
	}
	sort.Slice(platforms, func(i, j int) bool {
		return platforms[i].Source < platforms[j].Source
	})
	sendV1Success(c, platforms)
}

func apiV1ParseURLHandler(c *gin.Context) {
	rawURL := c.Query("url")
	started := time.Now()
	var attemptResult *parseResult
	var attemptErr error
	defer func() {
		recordParseAttempt(c, rawURL, "api.v1.parse_url", time.Since(started), attemptResult, attemptErr)
	}()

	if rawURL == "" {
		attemptErr = errors.New("url parameter is required")
		sendV1Error(c, http.StatusBadRequest, v1ErrMissingParameter, "url parameter is required")
		return
	}

	extractedURL, err := utils.RegexpMatchUrlFromString(rawURL)
	if err != nil {
		attemptErr = err
		sendV1Error(c, http.StatusBadRequest, v1ErrUnsupportedURL, "cannot extract a valid url from input")
		return
	}

	if !matchPlatform(extractedURL) {
		attemptErr = errors.New("unsupported platform")
		sendV1Error(c, http.StatusBadRequest, v1ErrUnsupportedURL, "unsupported platform")
		return
	}

	info, err := parser.ParseVideoShareUrlByRegexp(rawURL)
	if err != nil {
		attemptErr = err
		sendV1Error(c, http.StatusUnprocessableEntity, v1ErrParseFailed, err.Error())
		return
	}
	source := detectSource(extractedURL)
	attemptResult = &parseResult{
		source:       source,
		sourceURL:    extractedURL,
		parserEngine: runtimecfg.ParserEngineNative,
		info:         info,
		data:         toParseData(source, info),
	}
	sendV1Success(c, info)
}

func apiV1ParseIDHandler(c *gin.Context) {
	source := c.Param("source")
	videoID := c.Param("video_id")

	info, exists := parser.VideoSourceInfoMapping[source]
	if !exists {
		sendV1Error(c, http.StatusBadRequest, v1ErrUnsupportedSource, "鏈煡鐨勫钩鍙?"+source)
		return
	}
	if info.VideoIdParser == nil {
		sendV1Error(c, http.StatusBadRequest, v1ErrIDParseNotSupported, "this platform does not support video ID parsing")
		return
	}

	parseInfo, err := parser.ParseVideoId(source, videoID)
	if err != nil {
		sendV1Error(c, http.StatusUnprocessableEntity, v1ErrParseFailed, err.Error())
		return
	}
	sendV1Success(c, parseInfo)
}

func matchPlatform(rawURL string) bool {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, sourceInfo := range parser.VideoSourceInfoMapping {
		for _, domain := range sourceInfo.VideoShareUrlDomain {
			domain = strings.ToLower(domain)
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return true
			}
		}
	}
	return false
}

func compatErrorResponse(err error) httpResponse {
	code := 1001
	msg := "parse failed, please retry later"
	text := strings.ToLower(strings.TrimSpace(err.Error()))

	switch {
	case isInstagramAccessLimitedError(text):
		code = 1001
		msg = "Instagram 内容暂时无法访问：可能需要登录 Cookie、代理被限流，或帖子权限受限"
	case strings.Contains(text, "str not have url"), strings.Contains(text, "url is empty"):
		code = 1004
		msg = "invalid url"
	case strings.Contains(text, "not have source config"):
		code = 1002
		msg = "unsupported platform"
	case strings.Contains(text, "source") && strings.Contains(text, "has no"):
		code = 1002
		msg = "unsupported platform"
	default:
		msg = err.Error()
	}

	return httpResponse{Code: code, Msg: msg}
}

func isInstagramAccessLimitedError(text string) bool {
	if !strings.Contains(text, "instagram") {
		return false
	}
	return strings.Contains(text, "login required") ||
		strings.Contains(text, "cookies") ||
		strings.Contains(text, "rate-limit") ||
		strings.Contains(text, "requested content is not available")
}

type parseRequestOptions struct {
	BypassCache        bool
	BypassFailureCache bool
}

func parseShareRequest(input string) (*parseResult, error) {
	return parseShareRequestWithOptions(input, parseRequestOptions{})
}

func parseShareRequestWithOptions(input string, options parseRequestOptions) (*parseResult, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("url is empty")
	}

	normalized := extractURL(input)
	directM3U8 := isDirectM3U8URL(normalized)
	if !directM3U8 && !options.BypassFailureCache {
		if message, ok := getParseFailure(normalized); ok {
			return nil, errors.New(message)
		}
	}
	if !options.BypassCache {
		if cachedData, ok, err := globalParseResultCache.getBySourceURL(normalized); err == nil && ok {
			if cachedParseDataNeedsRefresh(cachedData) {
				logInfof("parse cache skipped stale result target=%s share_id=%s", targetForLog(normalized), cachedData.ShareID)
			} else {
				return &parseResult{
					source:       detectSource(firstNonEmpty(cachedData.SourceURL, normalized)),
					sourceURL:    firstNonEmpty(cachedData.SourceURL, normalized),
					parserEngine: "cache",
					info:         toVideoParseInfo(cachedData),
					data:         cachedData,
				}, nil
			}
		} else if err != nil {
			logErrorf("parse cache read failed target=%s error=%v", targetForLog(normalized), err)
		}
	}

	unlock, locked := acquireParseLock(normalized)
	if !locked {
		if !options.BypassCache {
			if cachedData, ok := waitForParseResult(normalized); ok {
				return &parseResult{
					source:       detectSource(firstNonEmpty(cachedData.SourceURL, normalized)),
					sourceURL:    firstNonEmpty(cachedData.SourceURL, normalized),
					parserEngine: "cache",
					info:         toVideoParseInfo(cachedData),
					data:         cachedData,
				}, nil
			}
		}
		return nil, errParseInProgress()
	}
	defer unlock()

	if directM3U8 {
		result, err := parseDirectM3U8Request(normalized)
		if err != nil {
			return nil, err
		}
		clearParseFailure(normalized)
		return result, nil
	}

	result, err := parseWithConfiguredEngine(input, normalized)
	if err != nil {
		setParseFailure(normalized, err)
		return nil, err
	}
	result.sourceURL = normalized
	result.data = cacheParseData(normalized, result.data)
	result.info = toVideoParseInfo(result.data)
	clearParseFailure(normalized)
	return result, nil
}

func parseWithConfiguredEngine(input, normalized string) (*parseResult, error) {
	if runtimecfg.ParserEngine() == runtimecfg.ParserEngineUniversal {
		result, err := tryParseWithUniversalParser(normalized)
		if err == nil {
			return result, nil
		}
		if runtimecfg.ParserFallbackEnabled() {
			logWarnf("universal parser failed, trying native parser target=%s error=%v", targetForLog(normalized), err)
			return parseWithNativeParser(input, normalized)
		}
		return nil, err
	}

	result, err := parseWithNativeParser(input, normalized)
	if err == nil {
		return result, nil
	}
	if runtimecfg.ParserFallbackEnabled() {
		logWarnf("native parser failed, trying universal parser target=%s error=%v", targetForLog(normalized), err)
		fallback, fallbackErr := tryParseWithUniversalParser(normalized)
		if fallbackErr == nil {
			return fallback, nil
		}
		return nil, fallbackErr
	}
	return nil, err
}

func parseWithNativeParser(input, normalized string) (*parseResult, error) {
	source := detectSource(normalized)
	info, err := parser.ParseVideoShareUrlByRegexp(input)
	if err != nil {
		fallback, fallbackErr := tryParseWithYTDLP(normalized, err)
		if fallbackErr == nil {
			fallback.sourceURL = normalized
			return fallback, nil
		}
		return nil, fallbackErr
	}

	if source == "" {
		source = detectSource(normalized)
	}

	result := &parseResult{
		source:       source,
		sourceURL:    normalized,
		parserEngine: runtimecfg.ParserEngineNative,
		info:         info,
		data:         toParseData(source, info),
	}
	return result, nil
}

func parseDirectM3U8Request(rawURL string) (*parseResult, error) {
	if err := validateRemoteTarget(rawURL); err != nil {
		return nil, err
	}

	title := titleFromM3U8URL(rawURL)
	data := parseData{
		Platform: "m3u8",
		Type:     "m3u8",
		Title:    title,
		Desc:     title,
		M3U8:     rawURL,
		Preview:  rawURL,
		PlayAddr: rawURL,
	}
	data = cacheParseData(rawURL, data)

	return &parseResult{
		source:       "m3u8",
		sourceURL:    rawURL,
		parserEngine: "m3u8",
		info:         toVideoParseInfo(data),
		data:         data,
	}, nil
}

func isDirectM3U8URL(raw string) bool {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return false
	}
	path := strings.ToLower(strings.TrimSpace(parsed.EscapedPath()))
	return strings.Contains(path, ".m3u8")
}

func titleFromM3U8URL(raw string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "m3u8"
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path == "" {
		return "m3u8"
	}
	index := strings.LastIndex(path, "/")
	if index >= 0 {
		path = path[index+1:]
	}
	if value, err := neturl.PathUnescape(path); err == nil {
		path = value
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "m3u8"
	}
	return path
}

func cachedParseDataNeedsRefresh(data parseData) bool {
	if !parseDataHasMedia(data) {
		return true
	}
	return textLooksQuestionMarkGarbled(data.Title) ||
		textLooksQuestionMarkGarbled(data.Desc) ||
		textLooksQuestionMarkGarbled(data.Author)
}

func parseDataHasMedia(data parseData) bool {
	if strings.TrimSpace(data.PlayAddr) != "" ||
		strings.TrimSpace(data.Preview) != "" ||
		strings.TrimSpace(data.M3U8) != "" ||
		strings.TrimSpace(data.Music) != "" ||
		strings.TrimSpace(data.MP3) != "" ||
		strings.TrimSpace(data.Audio) != "" ||
		strings.TrimSpace(data.AudioURL) != "" {
		return true
	}
	for _, item := range data.Downloads {
		if strings.TrimSpace(item.URL) != "" {
			return true
		}
	}
	for _, item := range data.Images {
		if strings.TrimSpace(item) != "" {
			return true
		}
	}
	for _, item := range data.Pics {
		if strings.TrimSpace(item) != "" {
			return true
		}
	}
	return false
}

func normalizeParseDataMediaAliases(data parseData) parseData {
	audioURL := firstNonEmpty(data.Music, data.MP3, data.AudioURL, data.Audio, audioURLFromDownloads(data))
	if audioURL != "" {
		data.Music = audioURL
		data.MP3 = audioURL
		data.Audio = audioURL
		data.AudioURL = audioURL
	}
	return data
}

func audioURLFromDownloads(data parseData) string {
	if strings.EqualFold(strings.TrimSpace(data.Type), "audio") {
		for _, item := range data.Downloads {
			if url := strings.TrimSpace(item.URL); url != "" {
				return url
			}
		}
	}

	for _, item := range data.Downloads {
		if !downloadItemLooksAudio(item) {
			continue
		}
		if url := strings.TrimSpace(item.URL); url != "" {
			return url
		}
	}
	return ""
}

func downloadItemLooksAudio(item downloadItem) bool {
	label := strings.ToLower(strings.TrimSpace(item.Label))
	if label != "" {
		for _, token := range []string{"audio", "music", "mp3", "m4a", "flac", "wav", "aac", "ogg"} {
			if strings.Contains(label, token) {
				return true
			}
		}
	}

	url := strings.ToLower(strings.TrimSpace(item.URL))
	if url == "" {
		return false
	}
	for _, suffix := range []string{".mp3", ".m4a", ".flac", ".wav", ".aac", ".ogg"} {
		if strings.Contains(url, suffix) {
			return true
		}
	}
	return false
}

func textLooksQuestionMarkGarbled(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	runes := []rune(text)
	questionMarks := 0
	longestRun := 0
	currentRun := 0
	for _, r := range runes {
		if r == '?' {
			questionMarks++
			currentRun++
			if currentRun > longestRun {
				longestRun = currentRun
			}
			continue
		}
		currentRun = 0
	}
	if longestRun < 2 {
		return false
	}
	return questionMarks >= 3 || float64(questionMarks)/float64(len(runes)) >= 0.15
}

func extractURL(input string) string {
	if match := strings.TrimSpace(looseURLPattern.FindString(input)); match != "" {
		return strings.TrimRight(match, "\"',.;:!?，。；：！？)]}>》")
	}

	url, err := utils.RegexpMatchUrlFromString(input)
	if err != nil {
		return input
	}

	return url
}

func detectSource(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	if parsed, err := neturl.Parse(input); err == nil {
		host := strings.ToLower(parsed.Hostname())
		if host != "" {
			for source, info := range parser.VideoSourceInfoMapping {
				for _, domain := range info.VideoShareUrlDomain {
					if hostMatchesSourceDomain(host, domain) {
						return source
					}
				}
			}
		}
	}

	lowerInput := strings.ToLower(input)
	switch {
	case strings.Contains(lowerInput, ".m3u8"):
		return "m3u8"
	case strings.Contains(lowerInput, "youtube.com"), strings.Contains(lowerInput, "youtu.be"):
		return "youtube"
	case strings.Contains(lowerInput, "tiktok.com"):
		return "tiktok"
	case strings.Contains(lowerInput, "instagram.com"):
		return "instagram"
	case strings.Contains(lowerInput, "facebook.com"), strings.Contains(lowerInput, "fb.watch"):
		return "facebook"
	case strings.Contains(lowerInput, "vimeo.com"):
		return "vimeo"
	case strings.Contains(lowerInput, "dailymotion.com"), strings.Contains(lowerInput, "dai.ly"):
		return "dailymotion"
	default:
		return ""
	}
}

func hostMatchesSourceDomain(host, domain string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	domain = strings.ToLower(strings.TrimSpace(domain))
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func toParseData(source string, info *parser.VideoParseInfo) parseData {
	images := make([]string, 0, len(info.Images))
	for _, item := range info.Images {
		if strings.TrimSpace(item.Url) != "" {
			images = append(images, item.Url)
		}
	}

	downloads := make([]downloadItem, 0, 1)
	if strings.TrimSpace(info.VideoUrl) != "" {
		downloads = append(downloads, downloadItem{
			URL:   info.VideoUrl,
			Label: "video",
		})
	}

	parseType := "video"
	if len(images) > 0 && len(downloads) == 0 {
		parseType = "image"
	}

	previewURL := firstNonEmpty(info.PreviewUrl, info.VideoUrl)
	videoURL := strings.TrimSpace(info.VideoUrl)

	return normalizeParseDataMediaAliases(parseData{
		Platform:  normalizePlatform(source),
		Type:      parseType,
		Title:     info.Title,
		Desc:      info.Title,
		Cover:     info.CoverUrl,
		Author:    info.Author.Name,
		Avatar:    info.Author.Avatar,
		Music:     info.MusicUrl,
		Duration:  0,
		Downloads: downloads,
		Images:    images,
		Pics:      images,
		M3U8:      "",
		Preview:   previewURL,
		PlayAddr:  videoURL,
	})
}

func normalizePlatform(source string) string {
	switch source {
	case parser.SourceRedBook:
		return "xiaohongshu"
	case parser.SourceQuanMinKGe:
		return "kgqq"
	case parser.SourceXiGua:
		return "ixigua"
	default:
		return source
	}
}

func applyRuntimeSettings() {
	runtimecfg.ApplyGlobalHTTPSettings()
}

func validateRemoteTarget(raw string) error {
	if raw == "" {
		return errors.New("url is empty")
	}
	parsed, err := neturl.Parse(raw)
	if err != nil {
		return errors.New("invalid download url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("invalid download url")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("invalid download url")
	}
	allowPrivateTargets := envBoolLocal("DOWNLOAD_FALLBACK_ALLOW_PRIVATE_URLS", false)
	if strings.EqualFold(host, "localhost") {
		if allowPrivateTargets {
			return nil
		}
		return errors.New("unsafe download url")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !allowPrivateTargets && !isPublicIP(ip) {
			return errors.New("unsafe download url")
		}
		return nil
	}
	if allowPrivateTargets {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return errors.New("unsafe download url")
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}
