package server

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

type adminTestLink struct {
	Platform string `json:"platform,omitempty"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Enabled  *bool  `json:"enabled,omitempty"`
	Note     string `json:"note,omitempty"`
}

type adminPlatformTestResult struct {
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	OK           bool       `json:"ok"`
	Status       string     `json:"status,omitempty"`
	Platform     string     `json:"platform,omitempty"`
	Type         string     `json:"type,omitempty"`
	ParserEngine string     `json:"parserEngine,omitempty"`
	Title        string     `json:"title,omitempty"`
	ShareID      string     `json:"shareId,omitempty"`
	Error        string     `json:"error,omitempty"`
	DurationMS   int64      `json:"durationMs"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	RespondedAt  *time.Time `json:"respondedAt,omitempty"`
	NodeID       string     `json:"nodeId,omitempty"`
	NodeName     string     `json:"nodeName,omitempty"`
	NodeRole     string     `json:"nodeRole,omitempty"`
}

var adminStartedAt = time.Now()

var defaultAdminTestLinks = []adminTestLink{
	{Name: "抖音", URL: "https://v.douyin.com/i2YArd1J/"},
	{Name: "快手", URL: "https://v.kuaishou.com/3yItmR"},
	{Name: "哔哩哔哩", URL: "https://www.bilibili.com/video/BV1634y1w7Nu/"},
	{Name: "微博", URL: "https://h5.video.weibo.com/show/1034:4915132212641842"},
	{Name: "小红书", URL: "http://xhslink.com/a/ELECa0m0DXccb"},
	{Name: "皮皮虾", URL: "https://h5.pipix.com/item/7111320087503575303"},
	{Name: "皮皮搞笑", URL: "https://h5.pipigx.com/pp/post/808595934847"},
	{Name: "全民K歌", URL: "https://kg.qq.com/node/play?s=d-GYCadMOClC7d_0&shareuid=64959a822728338a32&topsource=a0_pn201001006_z1_u687725416_l1_t1579315797__"},
	{Name: "度小视", URL: "https://xspshare.baidu.com/sv?source=share-h5&pd=qm_share_mvideo&vid=3712722043658570249"},
	{Name: "西瓜视频", URL: "https://www.ixigua.com/7359024563227656714"},
	{Name: "微视", URL: "https://video.weishi.qq.com/vfD4rz6U"},
	{Name: "好看视频", URL: "https://haokan.baidu.com/v?vid=4737540153193310732&pd=pcshare"},
	{Name: "梨视频", URL: "https://www.pearvideo.com/detail_1768812?st=7"},
	{Name: "央视网", URL: "https://tv.cctv.com/2026/05/09/VIDEy4R1ur8n3rdXIUlV8bDu260509.shtml"},
	{Name: "搜狐视频", URL: "http://my.tv.sohu.com/us/335942214/399571612.shtml"},
	{Name: "虎牙", URL: "https://v.huya.com/play/754425177.html"},
	{Name: "AcFun", URL: "https://www.acfun.cn/v/ac32776052"},
	{Name: "六间房", URL: "https://v.6.cn/minivideo/2716610"},
	{Name: "绿洲", URL: "https://m.oasis.weibo.cn/v1/h5/share?sid=4568005764186704"},
	{Name: "美拍", URL: "https://www.meipai.com/video/990/6870563584084037566"},
	{Name: "腾讯视频", URL: "https://v.qq.com/x/page/l3502vppd13.html"},
	{Name: "最右", URL: "https://share.xiaochuankeji.cn/hybrid/share/post?pid=407260908&vid=2492069614&zy_to=applink&share_count=1&m=72986390f8e3651c01fe06c56bccfb56&d=1eed7b49c0a6141fa1913307c450274fa5878ca53265df2c35e2f4fce0bffbfa&app=zuiyou&recommend=r0&name=n0&title_type=t0"},
	{Name: "YouTube", URL: "https://www.youtube.com/watch?v=oGsXa6slchc&t=2s"},
	{Name: "TikTok", URL: "https://www.tiktok.com/@nathanharenice/video/7413042588074200326"},
	{Name: "Instagram", URL: "https://www.instagram.com/reel/C62MdoDOWCr/"},
	{Name: "Twitter/X", URL: "https://x.com/Eminem/status/943590594491772928"},
	{Name: "Facebook", URL: "https://facebook.com/CTSHSTT/videos/374811976811983/"},
	{Name: "Vimeo", URL: "https://vimeo.com/76979871"},
	{Name: "Dailymotion", URL: "https://www.dailymotion.com/video/x7t3la2"},
	{Name: "M3U8", URL: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8"},
	{Platform: "abc", Name: "ABC", URL: "https://www.abc.net.au/btn/classroom/wwi-centenary/10527914"},
	{Platform: "arte", Name: "ArteTV", URL: "https://www.arte.tv/en/videos/127781-000-A/arte-reportage/"},
	{Platform: "baidutieba", Name: "百度贴吧", URL: "https://tieba.baidu.com/p/7280373361"},
	{Platform: "cctalk", Name: "CCtalk", URL: "https://www.cctalk.com/v/17604950351552?sid=1760494906733025"},
	{Platform: "dongchedi", Name: "懂车帝", URL: "https://www.dongchedi.com/video/7595864547580658200"},
	{Platform: "iqiyi", Name: "爱奇艺", URL: "https://www.iqiyi.com/v_1wc7muawbrc.html"},
	{Platform: "mgtv", Name: "芒果TV", URL: "https://www.mgtv.com/l/100026064/19868457.html?fpa=1684&fpos=&lastp=ch_home&cpid=5"},
	{Platform: "open163", Name: "网易公开课", URL: "https://open.163.com/newview/movie/free?pid=WIAL798Q9&mid=BIAL799D2"},
	{Platform: "reddit", Name: "Reddit", URL: "https://www.reddit.com/r/videos/comments/6rrwyj/that_small_heart_attack/"},
	{Platform: "ted", Name: "TED", URL: "https://www.ted.com/talks/alanna_shaikh_why_covid_19_is_hitting_us_now_and_how_to_prepare_for_the_next_outbreak"},
	{Platform: "youku", Name: "优酷", URL: "https://v.youku.com/v_show/id_XNDU1MTg1NjM2OA=="},
	{Platform: "zhihu", Name: "知乎", URL: "https://www.zhihu.com/zvideo/1342930761977176064"},
}

func handleAdminPage(c *gin.Context) {
	c.HTML(http.StatusOK, "index.html", gin.H{
		"title": "去水印后台管理",
	})
}

func handleAdminSummary(c *gin.Context) {
	testSamples, testSampleStore, err := currentAdminTestSamples(c.Request.Context())
	if err != nil {
		logWarnf("load admin test samples failed in summary: %v", err)
		testSamples = defaultAdminTestSamples()
		testSampleStore = "default"
	}
	testLinks := adminTestLinksFromSamples(testSamples)
	latestPlatformTest, hasLatestPlatformTest := latestAdminPlatformTestRun(c.Request.Context())

	sources := nativeParserSources()
	platforms := make([]platformInfo, 0, len(sources))
	for _, info := range sources {
		platforms = append(platforms, platformInfo{
			Source: info.Key, Name: info.DisplayName,
			URLParse: info.URLParse, IDParse: info.IDParse,
		})
	}
	sort.Slice(platforms, func(i, j int) bool {
		return platforms[i].Source < platforms[j].Source
	})

	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"startedAt":       adminStartedAt,
			"uptimeSeconds":   int64(time.Since(adminStartedAt).Seconds()),
			"goVersion":       runtime.Version(),
			"platformCount":   len(sources),
			"testSampleCount": len(testSamples),
			"testLinkCount":   len(testLinks),
			"testLinks":       testLinks,
			"testSamples":     testSamples,
			"testSampleStore": testSampleStore,
			"latestPlatformTest": func() interface{} {
				if hasLatestPlatformTest {
					return latestPlatformTest
				}
				return nil
			}(),
			"cache":          globalParseResultCache.stats(),
			"tasks":          currentTaskStats(),
			"settings":       runtimecfg.Current(),
			"infrastructure": currentInfrastructureStatus(c.Request.Context()),
			"node":           currentClusterNodeInfo(),
			"cluster":        currentClusterStatus(c.Request.Context()),
			"platforms":      platforms,
			"defaultAccount": adminCredentials().IsDefault,
		},
	})
}

func handleAdminParse(c *gin.Context) {
	var req parseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "invalid parse payload"})
		return
	}

	result, durationMS, err := parseShareRequestTracked(c, req.URL, "admin.quick_parse", parseRequestOptions{})
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}

	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"durationMs": durationMS,
			"result":     result.data,
		},
	})
}

func handleAdminRunPlatformTests(c *gin.Context) {
	var req struct {
		Links []adminTestLink `json:"links"`
	}
	_ = c.ShouldBindJSON(&req)

	links := sanitizeAdminTestLinks(req.Links)
	if len(links) == 0 {
		links = currentAdminTestLinks(c.Request.Context())
	}

	started := time.Now()
	results := runAdminPlatformTestsWithScheduler(c.Request.Context(), links, adminPlatformTestSchedulerHooks{})

	success := 0
	for _, result := range results {
		if result.OK {
			success++
		}
	}
	durationMS := time.Since(started).Milliseconds()

	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"total":      len(results),
			"success":    success,
			"failed":     len(results) - success,
			"durationMs": durationMS,
			"items":      results,
		},
	})
	writeAdminAudit(c, "admin.platform_test.run", "platform_test", "", gin.H{"total": len(results), "success": success, "durationMs": durationMS})
}

func handleAdminStartPlatformTestRun(c *gin.Context) {
	var req struct {
		Links []adminTestLink `json:"links"`
	}
	_ = c.ShouldBindJSON(&req)

	links := sanitizeAdminTestLinks(req.Links)
	if len(links) == 0 {
		links = currentAdminTestLinks(c.Request.Context())
	}
	snapshot, reused, err := adminPlatformTestRuns.start(c.Request.Context(), links, currentAdminUsername(c))
	if err != nil {
		if errors.Is(err, errNoPlatformTestLinks) {
			c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "no enabled test samples"})
			return
		}
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	if !reused {
		writeAdminAudit(c, "admin.platform_test.start", "platform_test", snapshot.RunID, gin.H{"total": snapshot.Total, "temporary": len(req.Links) > 0})
	}
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"reused": reused,
			"run":    snapshot,
		},
	})
}

func handleAdminLatestPlatformTestRun(c *gin.Context) {
	snapshot, ok := latestAdminPlatformTestRun(c.Request.Context())
	if !ok {
		c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: gin.H{"run": nil}})
		return
	}
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: gin.H{"run": snapshot}})
}

func handleAdminGetPlatformTestRun(c *gin.Context) {
	snapshot, ok := adminPlatformTestRunByID(c.Request.Context(), c.Param("run_id"))
	if !ok {
		c.JSON(http.StatusNotFound, httpResponse{Code: 1004, Msg: "platform test run not found"})
		return
	}
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: gin.H{"run": snapshot}})
}

func handleAdminListParseCache(c *gin.Context) {
	limit := intFromQuery(c, "limit", 50)
	query := strings.TrimSpace(c.Query("q"))
	items, err := globalParseResultCache.list(limit, query)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: items})
}

func handleAdminGetParseCache(c *gin.Context) {
	record, ok, err := globalParseResultCache.getRecord(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, httpResponse{Code: 1004, Msg: "record not found"})
		return
	}
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: record})
}

func handleAdminDeleteParseCache(c *gin.Context) {
	deleted, err := globalParseResultCache.delete(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	writeAdminAudit(c, "parse_result.delete", "parse_result", c.Param("id"), gin.H{"deleted": deleted})
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: gin.H{"deleted": deleted}})
}

func handleAdminClearParseCache(c *gin.Context) {
	count, err := globalParseResultCache.clear()
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	writeAdminAudit(c, "parse_result.clear", "parse_result", "", gin.H{"deleted": count})
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: gin.H{"deleted": count}})
}

func handleAdminListTasks(c *gin.Context) {
	payload, persistent, err := listPersistentTasks(intFromQuery(c, "limit", 100))
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	if persistent {
		c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: payload})
		return
	}
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: memoryTaskListPayload(),
	})
}

func handleAdminDiagnostics(c *gin.Context) {
	ytdlp := binaryStatus(runtimecfg.YTDLPBinary())
	ffmpeg := binaryStatus(runtimecfg.FFMPEGBinary())
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"ytDlp":           ytdlp,
			"ffmpeg":          ffmpeg,
			"settings":        runtimecfg.Current(),
			"universalParser": universalParserDiagnostics(),
			"infra":           currentInfrastructureStatus(c.Request.Context()),
			"go":              runtime.Version(),
			"os":              runtime.GOOS,
			"arch":            runtime.GOARCH,
		},
	})
}

func handleAdminLogs(c *gin.Context) {
	name := strings.TrimSpace(c.DefaultQuery("file", "app"))
	logFile, ok := map[string]string{
		"app":    "app.log",
		"access": "access.log",
		"error":  "error.log",
	}[name]
	if !ok {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "鏈煡鏃ュ織绫诲瀷"})
		return
	}

	lines := intFromQuery(c, "lines", 120)
	if lines < 1 {
		lines = 120
	}
	if lines > 500 {
		lines = 500
	}

	content, err := readLogTail(logFile, lines)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: gin.H{"file": name, "content": content}})
}

func runAdminPlatformTest(item adminTestLink) (result adminPlatformTestResult) {
	started := time.Now()
	node := currentClusterNodeInfo()
	result = adminPlatformTestResult{
		Name:      item.Name,
		URL:       item.URL,
		Platform:  item.Platform,
		Status:    "running",
		StartedAt: &started,
		NodeID:    node.ID,
		NodeName:  node.Name,
		NodeRole:  node.Role,
	}
	missingMedia := false
	defer func() {
		respondedAt := time.Now()
		result.RespondedAt = &respondedAt
		result.DurationMS = respondedAt.Sub(started).Milliseconds()
		if result.Status == "running" {
			result.Status = "completed"
		}
		if missingMedia {
			result.Error = "解析结果缺少可用媒体地址"
			return
		}
		result.Error = normalizeDisplayText(result.Error)
	}()

	parsed, err := parseShareRequestWithOptions(item.URL, parseRequestOptions{
		BypassCache:        true,
		BypassFailureCache: true,
	})
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		return result
	}
	result.ParserEngine = firstNonEmpty(parsed.parserEngine, result.ParserEngine)
	if !parseDataHasMedia(parsed.data) {
		result.OK = false
		missingMedia = true
		result.Platform = firstNonEmpty(item.Platform, parsed.data.Platform)
		result.Type = parsed.data.Type
		result.Title = strings.TrimSpace(parsed.data.Title)
		result.ShareID = parsed.data.ShareID
		result.Error = "瑙ｆ瀽缁撴灉缂哄皯鍙敤濯掍綋鍦板潃"
		return result
	}

	result.OK = true
	result.Platform = firstNonEmpty(item.Platform, parsed.data.Platform)
	result.Type = parsed.data.Type
	result.Title = strings.TrimSpace(parsed.data.Title)
	result.ShareID = parsed.data.ShareID
	return result
}

func sanitizeAdminTestLinks(input []adminTestLink) []adminTestLink {
	items := make([]adminTestLink, 0, len(input))
	for _, item := range input {
		platform := normalizeAdminSamplePlatform(firstNonEmpty(item.Platform, detectSource(item.URL), platformForDisplayName(item.Name)))
		name := strings.TrimSpace(item.Name)
		rawURL := strings.TrimSpace(item.URL)
		if rawURL == "" {
			continue
		}
		if name == "" {
			name = sampleDisplayName(platform)
		}
		items = append(items, adminTestLink{Platform: platform, Name: name, URL: rawURL})
	}
	return items
}

func binaryStatus(candidate string) gin.H {
	path, err := exec.LookPath(strings.TrimSpace(candidate))
	if err != nil {
		return gin.H{
			"configured": candidate,
			"available":  false,
			"error":      err.Error(),
		}
	}
	return gin.H{
		"configured": candidate,
		"available":  true,
		"path":       path,
	}
}

func intFromQuery(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(c.Query(key)))
	if err != nil {
		return fallback
	}
	return value
}

func readLogTail(fileName string, lines int) (string, error) {
	logDir := strings.TrimSpace(os.Getenv("LOG_DIR"))
	if logDir == "" {
		logDir = defaultLogDir
	}
	path := filepath.Join(logDir, fileName)
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := strings.ReplaceAll(string(bytes), "\r\n", "\n")
	parts := strings.Split(content, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n"), nil
}
