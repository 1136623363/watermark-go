package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/netguard"
	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

const (
	downloadFallbackStatusPending   = "pending"
	downloadFallbackStatusRunning   = "running"
	downloadFallbackStatusCompleted = "completed"
	downloadFallbackStatusFailed    = "failed"
)

type downloadFallbackRequest struct {
	SourceURL string `json:"sourceUrl"`
	MediaURL  string `json:"mediaUrl"`
	MediaType string `json:"mediaType"`
	ShareID   string `json:"shareId"`
	Attempt   int    `json:"attempt"`
	UserID    int64  `json:"-"`
	PublicID  string `json:"-"`
	ClientIP  string `json:"-"`
	UserAgent string `json:"-"`
}

type downloadFallbackProxyTicket struct {
	SourceURL string `json:"sourceUrl,omitempty"`
	MediaURL  string `json:"mediaUrl"`
	MediaType string `json:"mediaType"`
	ShareID   string `json:"shareId,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	UserID    int64  `json:"userId,omitempty"`
	PublicID  string `json:"publicId,omitempty"`
	ClientIP  string `json:"clientIp,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`
	Expires   int64  `json:"expires"`
}

type downloadFallbackTask struct {
	TaskID      string    `json:"taskId"`
	Status      string    `json:"status"`
	Progress    int       `json:"progress"`
	SourceURL   string    `json:"sourceUrl,omitempty"`
	MediaURL    string    `json:"mediaUrl,omitempty"`
	MediaType   string    `json:"mediaType"`
	Mode        string    `json:"mode,omitempty"`
	ShareID     string    `json:"shareId,omitempty"`
	UserID      int64     `json:"-"`
	PublicID    string    `json:"-"`
	ClientIP    string    `json:"-"`
	UserAgent   string    `json:"-"`
	FileKey     string    `json:"fileKey,omitempty"`
	FilePath    string    `json:"-"`
	FileSize    int64     `json:"fileSize,omitempty"`
	ContentType string    `json:"contentType,omitempty"`
	Message     string    `json:"message,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
}

type downloadFallbackStore struct {
	mu      sync.RWMutex
	tasks   map[string]*downloadFallbackTask
	byKey   map[string]string
	running int
}

type progressWriter struct {
	total      int64
	written    int64
	lastUpdate time.Time
	update     func(progress int, written int64)
}

var (
	globalDownloadFallbackStore = &downloadFallbackStore{
		tasks: make(map[string]*downloadFallbackTask),
		byKey: make(map[string]string),
	}
	downloadFallbackKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,180}$`)
)

func handleDownloadFallbackCreate(c *gin.Context) {
	if !downloadFallbackEnabled() {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "服务器兜底下载未启用"})
		return
	}
	if downloadFallbackTokenSecret() == "" {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "download fallback signing is unavailable"})
		return
	}

	var req downloadFallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "invalid fallback payload"})
		return
	}

	req.SourceURL = strings.TrimSpace(req.SourceURL)
	req.MediaURL = strings.TrimSpace(req.MediaURL)
	req.ShareID = strings.TrimSpace(req.ShareID)
	req.MediaType = normalizeDownloadFallbackMediaType(req.MediaType)
	attachOptionalClientSession(c)
	meta := downloadFallbackClientMetaFromContext(c)
	req.UserID = meta.UserID
	req.PublicID = meta.PublicID
	req.ClientIP = meta.ClientIP
	req.UserAgent = meta.UserAgent
	if req.MediaType == "" {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "unsupported fallback media type"})
		return
	}
	if err := validateRemoteTarget(req.MediaURL); err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: err.Error()})
		return
	}

	mode := downloadFallbackMode()
	switch mode {
	case runtimecfg.DownloadFallbackModeProxy:
		taskID := buildDownloadFallbackProxyTaskID(req)
		downloadURL, err := buildDownloadFallbackProxyURL(c, req)
		if err != nil {
			recordDownloadFallbackEvent(c, req, mode, downloadFallbackEventStatusFailed, taskID, 0, 0, 0, "download fallback signing is unavailable")
			c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "download fallback signing is unavailable"})
			return
		}
		recordDownloadFallbackEvent(c, req, mode, downloadFallbackEventStatusIssued, taskID, 0, 0, 0, "")
		c.JSON(http.StatusOK, httpResponse{
			Code: 0,
			Msg:  "ok",
			Data: downloadURL,
		})
		return
	case runtimecfg.DownloadFallbackModeCDN:
		if downloadFallbackCDNBaseURL() == "" {
			recordDownloadFallbackEvent(c, req, mode, downloadFallbackEventStatusFailed, "", 0, 0, 0, "download fallback cdn base url is not configured")
			c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "download fallback cdn base url is not configured"})
			return
		}
	}

	fileKey := buildDownloadFallbackFileKey(req)
	filePath := downloadFallbackPublicFilePath(fileKey)
	if task := completedDownloadFallbackTaskFromFile(req, fileKey, filePath); task != nil {
		data, err := downloadFallbackResponseData(c, *task)
		if err != nil {
			recordDownloadFallbackEvent(c, req, mode, downloadFallbackEventStatusFailed, task.TaskID, 0, 0, 0, "download fallback signing is unavailable")
			c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "download fallback signing is unavailable"})
			return
		}
		recordDownloadFallbackEvent(c, req, mode, downloadFallbackEventStatusReused, task.TaskID, task.FileSize, 0, 0, "")
		c.JSON(http.StatusOK, httpResponse{
			Code: 0,
			Msg:  "ok",
			Data: data,
		})
		return
	}

	task, created, err := globalDownloadFallbackStore.enqueue(req, fileKey, filePath)
	if err != nil {
		recordDownloadFallbackEvent(c, req, mode, downloadFallbackEventStatusFailed, "", 0, 0, 0, err.Error())
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error()})
		return
	}
	if created {
		recordDownloadFallbackEvent(c, req, mode, downloadFallbackEventStatusQueued, task.TaskID, 0, 0, 0, "")
		go runDownloadFallbackTask(task.TaskID)
	} else {
		status := downloadFallbackEventStatusReused
		if task.Status == downloadFallbackStatusPending || task.Status == downloadFallbackStatusRunning {
			status = downloadFallbackEventStatusQueued
		}
		recordDownloadFallbackEvent(c, req, mode, status, task.TaskID, task.FileSize, 0, 0, task.Message)
	}

	data, err := downloadFallbackResponseData(c, task)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "download fallback signing is unavailable"})
		return
	}
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: data,
	})
}

func handleDownloadFallbackStatus(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("id"))
	handleLocalDownloadFallbackStatus(c, taskID)
}

func handleLocalDownloadFallbackStatus(c *gin.Context, taskID string) {
	handleLocalDownloadFallbackStatusWithFileKey(c, taskID, "")
}

func handleLocalDownloadFallbackStatusWithFileKey(c *gin.Context, taskID, fileKey string) {
	task, ok := globalDownloadFallbackStore.get(taskID)
	if !ok && validDownloadFallbackKey(fileKey) {
		if recovered := completedDownloadFallbackTaskFromFile(downloadFallbackRequest{}, fileKey, downloadFallbackPublicFilePath(fileKey)); recovered != nil {
			recovered.TaskID = taskID
			task = *recovered
			ok = true
		}
	}
	if !ok {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "fallback task not found"})
		return
	}
	data, err := downloadFallbackResponseData(c, task)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: "download fallback signing is unavailable"})
		return
	}
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: data,
	})
}

func handleDownloadFallbackFile(c *gin.Context) {
	key := strings.TrimSpace(c.Param("key"))
	handleLocalDownloadFallbackFile(c, key)
}

func handleLocalDownloadFallbackFile(c *gin.Context, key string) {
	serveDownloadFallbackFile(c, key, strings.TrimSpace(c.Query("expires")), strings.TrimSpace(c.Query("token")))
}

func serveDownloadFallbackFile(c *gin.Context, key, expiresRaw, token string) {
	if !validDownloadFallbackKey(key) {
		c.JSON(http.StatusNotFound, httpResponse{Code: 1004, Msg: "fallback file not found"})
		return
	}
	expires, err := strconv.ParseInt(expiresRaw, 10, 64)
	if err != nil || expires <= time.Now().Unix() || !verifyDownloadFallbackToken(key, expires, token) {
		c.JSON(http.StatusForbidden, httpResponse{Code: 1004, Msg: "fallback download token expired"})
		return
	}

	filePath := downloadFallbackPublicFilePath(key)
	file, err := os.Open(filePath)
	if err != nil {
		c.JSON(http.StatusNotFound, httpResponse{Code: 1004, Msg: "fallback file not found"})
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		c.JSON(http.StatusNotFound, httpResponse{Code: 1004, Msg: "fallback file not found"})
		return
	}

	c.Header("Accept-Ranges", "bytes")
	c.Header("Cache-Control", "private, max-age=1800")
	if contentType := mime.TypeByExtension(filepath.Ext(key)); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	mode := downloadFallbackMode()
	if mode == runtimecfg.DownloadFallbackModeProxy {
		mode = runtimecfg.DownloadFallbackModeCache
	}
	transfer := beginDownloadFallbackTransfer(c, mode, downloadFallbackRequest{
		MediaURL:  key,
		MediaType: downloadFallbackMediaTypeFromKey(key),
	}, "")
	counter := newTransferCountingReadSeeker(file, transfer.RequestID)
	http.ServeContent(c.Writer, c.Request, key, stat.ModTime(), counter)
	status := c.Writer.Status()
	if status == 0 {
		status = http.StatusOK
	}
	eventStatus := downloadFallbackEventStatusCompleted
	message := ""
	if status >= http.StatusBadRequest {
		eventStatus = downloadFallbackEventStatusFailed
		message = http.StatusText(status)
	}
	finishDownloadFallbackTransfer(transfer, eventStatus, counter.written, status, message)
}

func handleDownloadFallbackPublicStatus(c *gin.Context) {
	node, statusValue, _, ok := parseDownloadFallbackTicket(strings.TrimSpace(c.Param("ticket")), "status")
	if !ok {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "fallback task not found"})
		return
	}
	taskID, fileKey := splitDownloadFallbackStatusValue(statusValue)
	if taskID == "" {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "fallback task not found"})
		return
	}
	if isCurrentClusterNodeSelector(node) {
		handleLocalDownloadFallbackStatusWithFileKey(c, taskID, fileKey)
		return
	}
	targetPath := "/api/internal/download/fallback/" + neturl.PathEscape(taskID)
	if fileKey != "" {
		targetPath += "?fileKey=" + neturl.QueryEscape(fileKey)
	}
	proxyDownloadFallbackToNode(c, node, targetPath)
}

func handleDownloadFallbackPublicFile(c *gin.Context) {
	node, key, expires, ok := parseDownloadFallbackTicket(strings.TrimSpace(c.Param("ticket")), "file")
	if !ok {
		c.JSON(http.StatusForbidden, httpResponse{Code: 1004, Msg: "fallback download token expired"})
		return
	}
	expiresRaw := strconv.FormatInt(expires, 10)
	token := signDownloadFallbackToken(key, expires)
	if isCurrentClusterNodeSelector(node) {
		serveDownloadFallbackFile(c, key, expiresRaw, token)
		return
	}
	targetPath := "/api/internal/download/file/" + neturl.PathEscape(key) + "?expires=" + neturl.QueryEscape(expiresRaw) + "&token=" + neturl.QueryEscape(token)
	proxyDownloadFallbackToNode(c, node, targetPath)
}

func handleDownloadFallbackProxy(c *gin.Context) {
	rawTicket := strings.TrimSpace(c.Param("ticket"))
	node, proxyTicket, _, ok := parseDownloadFallbackTicket(rawTicket, "proxy")
	if ok {
		if isCurrentClusterNodeSelector(node) {
			handleLocalDownloadFallbackProxy(c, proxyTicket)
			return
		}
		proxyDownloadFallbackToNode(c, node, "/api/internal/download/proxy/"+neturl.PathEscape(proxyTicket))
		return
	}

	// Backward compatibility for proxy links created before node-aware tickets.
	handleLocalDownloadFallbackProxy(c, rawTicket)
}

func handleLocalDownloadFallbackProxy(c *gin.Context, rawTicket string) {
	ticket, ok := parseDownloadFallbackProxyTicket(strings.TrimSpace(rawTicket))
	if !ok {
		c.JSON(http.StatusForbidden, httpResponse{Code: 1004, Msg: "fallback proxy token expired"})
		return
	}
	proxyRemoteDownload(c, downloadFallbackRequest{
		SourceURL: ticket.SourceURL,
		MediaURL:  ticket.MediaURL,
		MediaType: ticket.MediaType,
		ShareID:   ticket.ShareID,
		Attempt:   ticket.Attempt,
		UserID:    ticket.UserID,
		PublicID:  ticket.PublicID,
		ClientIP:  ticket.ClientIP,
		UserAgent: ticket.UserAgent,
	})
}

func handleDownloadFallbackNodeStatus(c *gin.Context) {
	node := strings.TrimSpace(c.Param("node"))
	taskID := strings.TrimSpace(c.Param("id"))
	fileKey := strings.TrimSpace(c.Query("fileKey"))
	if isCurrentClusterNodeSelector(node) {
		handleLocalDownloadFallbackStatusWithFileKey(c, taskID, fileKey)
		return
	}
	targetPath := "/api/internal/download/fallback/" + neturl.PathEscape(taskID)
	if fileKey != "" {
		targetPath += "?fileKey=" + neturl.QueryEscape(fileKey)
	}
	proxyDownloadFallbackToNode(c, node, targetPath)
}

func handleDownloadFallbackNodeFile(c *gin.Context) {
	node := strings.TrimSpace(c.Param("node"))
	key := strings.TrimSpace(c.Param("key"))
	if isCurrentClusterNodeSelector(node) {
		handleLocalDownloadFallbackFile(c, key)
		return
	}
	targetPath := "/api/internal/download/file/" + neturl.PathEscape(key)
	if rawQuery := c.Request.URL.RawQuery; rawQuery != "" {
		targetPath += "?" + rawQuery
	}
	proxyDownloadFallbackToNode(c, node, targetPath)
}

func handleInternalDownloadFallbackStatus(c *gin.Context) {
	if !allowInternalClusterRequest(c) {
		c.JSON(http.StatusForbidden, httpResponse{Code: 403, Msg: "forbidden"})
		return
	}
	handleLocalDownloadFallbackStatusWithFileKey(c, strings.TrimSpace(c.Param("id")), strings.TrimSpace(c.Query("fileKey")))
}

func handleInternalDownloadFallbackFile(c *gin.Context) {
	if !allowInternalClusterRequest(c) {
		c.JSON(http.StatusForbidden, httpResponse{Code: 403, Msg: "forbidden"})
		return
	}
	handleLocalDownloadFallbackFile(c, strings.TrimSpace(c.Param("key")))
}

func handleInternalDownloadFallbackProxy(c *gin.Context) {
	if !allowInternalClusterRequest(c) {
		c.JSON(http.StatusForbidden, httpResponse{Code: 403, Msg: "forbidden"})
		return
	}
	handleLocalDownloadFallbackProxy(c, strings.TrimSpace(c.Param("ticket")))
}

func proxyDownloadFallbackToNode(c *gin.Context, node, targetPath string) {
	c.JSON(http.StatusNotFound, httpResponse{Code: 1004, Msg: "cluster download fallback is disabled"})
}

func proxyRemoteDownload(c *gin.Context, req downloadFallbackRequest) {
	req.MediaURL = strings.TrimSpace(req.MediaURL)
	req.SourceURL = strings.TrimSpace(req.SourceURL)
	req.ShareID = strings.TrimSpace(req.ShareID)
	req.PublicID = strings.TrimSpace(req.PublicID)
	req.MediaType = normalizeDownloadFallbackMediaType(req.MediaType)
	if req.MediaType == "" {
		req.MediaType = "video"
	}
	if err := validateRemoteTarget(req.MediaURL); err != nil {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: err.Error()})
		return
	}

	transfer := beginDownloadFallbackTransfer(c, runtimecfg.DownloadFallbackModeProxy, req, buildDownloadFallbackProxyTaskID(req))
	ctx, cancel := context.WithTimeout(c.Request.Context(), downloadFallbackProxyTimeout())
	defer cancel()
	resp, err := doDownloadFallbackOriginRequest(ctx, req.SourceURL, req.MediaURL, req.MediaType, strings.TrimSpace(c.GetHeader("Range")))
	if err != nil {
		finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusFailed, 0, 0, err.Error())
		c.JSON(http.StatusBadGateway, httpResponse{Code: 1001, Msg: "fallback proxy failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := strings.TrimSpace(resp.Header.Get("X-Error-Reason1"))
		c.Header("X-Fallback-Origin-Status", strconv.Itoa(resp.StatusCode))
		if reason != "" {
			c.Header("X-Fallback-Origin-Reason", reason)
		}
		msg := fmt.Sprintf("fallback proxy origin returned HTTP %d", resp.StatusCode)
		if reason != "" {
			msg += ": " + reason
		}
		finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusFailed, 0, resp.StatusCode, msg)
		c.JSON(http.StatusBadGateway, httpResponse{Code: 1001, Msg: msg})
		return
	}

	maxBytes := downloadFallbackMaxBytes(req.MediaType)
	if resp.ContentLength > maxBytes {
		msg := fmt.Sprintf("file too large: %d bytes exceeds %d bytes", resp.ContentLength, maxBytes)
		finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusFailed, 0, resp.StatusCode, msg)
		c.JSON(http.StatusRequestEntityTooLarge, httpResponse{Code: 1001, Msg: msg})
		return
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if !downloadFallbackContentTypeAllowed(req.MediaType, contentType) {
		msg := "unsupported content type: " + firstNonEmptyString(contentType, "unknown")
		finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusFailed, 0, resp.StatusCode, msg)
		c.JSON(http.StatusBadGateway, httpResponse{Code: 1001, Msg: msg})
		return
	}

	copyResponseHeader(c, resp, "Content-Type")
	copyResponseHeader(c, resp, "Content-Length")
	copyResponseHeader(c, resp, "Content-Range")
	copyResponseHeader(c, resp, "Accept-Ranges")
	copyResponseHeader(c, resp, "ETag")
	copyResponseHeader(c, resp, "Last-Modified")
	c.Header("Cache-Control", "private, max-age=300")
	if resp.Header.Get("Accept-Ranges") == "" {
		c.Header("Accept-Ranges", "bytes")
	}
	c.Status(resp.StatusCode)
	counter := newTransferCountingReader(resp.Body, transfer.RequestID)
	written, copyErr := io.Copy(c.Writer, counter)
	if copyErr != nil {
		finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusFailed, written, resp.StatusCode, copyErr.Error())
		return
	}
	finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusCompleted, written, resp.StatusCode, "")
}

func copyResponseHeader(c *gin.Context, resp *http.Response, key string) {
	value := strings.TrimSpace(resp.Header.Get(key))
	if value != "" {
		c.Header(key, value)
	}
}

func doDownloadFallbackOriginRequest(ctx context.Context, sourceURL, mediaURL, mediaType, rangeHeader string) (*http.Response, error) {
	profiles := downloadFallbackOriginHeaderProfiles(sourceURL, mediaURL, mediaType, rangeHeader)
	var lastErr error
	for index, headers := range profiles {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
		if err != nil {
			return nil, errors.New("invalid fallback media url")
		}
		for key, value := range headers {
			if strings.TrimSpace(value) != "" {
				req.Header.Set(key, value)
			}
		}
		resp, err := doGuardedDownloadFallbackRequest(ctx, req, 4, func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 {
				previous := via[len(via)-1]
				for _, key := range []string{"User-Agent", "Accept", "Accept-Language", "Accept-Encoding", "Cache-Control", "Pragma", "Range", "Referer", "Origin", "Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site"} {
					if value := strings.TrimSpace(previous.Header.Get(key)); value != "" {
						req.Header.Set(key, value)
					}
				}
			}
			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", downloadFallbackUserAgent())
			}
			if req.Header.Get("Accept") == "" {
				req.Header.Set("Accept", "*/*")
			}
			return nil
		})
		if err != nil {
			lastErr = err
			continue
		}
		if !downloadFallbackOriginStatusShouldRetry(resp.StatusCode) || index == len(profiles)-1 {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("fallback origin request failed")
}

func downloadFallbackOriginStatusShouldRetry(status int) bool {
	switch status {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusRequestedRangeNotSatisfiable:
		return true
	default:
		return false
	}
}

func downloadFallbackOriginHeaderProfiles(sourceURL, mediaURL, mediaType, rangeHeader string) []map[string]string {
	base := map[string]string{
		"User-Agent":      downloadFallbackUserAgent(),
		"Accept":          downloadFallbackAcceptHeader(mediaType),
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"Accept-Encoding": "identity",
		"Cache-Control":   "no-cache",
		"Pragma":          "no-cache",
	}
	rangeHeaders := []string{strings.TrimSpace(rangeHeader), ""}
	if rangeHeaders[0] == "" {
		rangeHeaders = []string{""}
	}

	referers := downloadFallbackOriginRefererCandidates(sourceURL, mediaURL)
	profiles := make([]map[string]string, 0, 1+len(rangeHeaders)*maxInt(1, len(referers))*2)
	seen := make(map[string]struct{})
	for _, currentRange := range rangeHeaders {
		baseWithRange := cloneStringMap(base)
		if currentRange != "" {
			baseWithRange["Range"] = currentRange
		}
		appendDownloadFallbackOriginProfile(&profiles, seen, baseWithRange)
		for _, ref := range referers {
			if ref.referer == "" {
				continue
			}
			withReferer := cloneStringMap(baseWithRange)
			withReferer["Referer"] = ref.referer
			withReferer["Sec-Fetch-Dest"] = downloadFallbackSecFetchDest(mediaType)
			withReferer["Sec-Fetch-Mode"] = "no-cors"
			withReferer["Sec-Fetch-Site"] = "cross-site"
			appendDownloadFallbackOriginProfile(&profiles, seen, withReferer)
			if ref.origin != "" {
				withOrigin := cloneStringMap(withReferer)
				withOrigin["Origin"] = ref.origin
				appendDownloadFallbackOriginProfile(&profiles, seen, withOrigin)
			}
		}
	}
	return profiles
}

type downloadFallbackRefererCandidate struct {
	referer string
	origin  string
}

func downloadFallbackOriginRefererCandidates(sourceURL, mediaURL string) []downloadFallbackRefererCandidate {
	candidates := []downloadFallbackRefererCandidate{}
	if referer, origin := downloadFallbackRefererFromURL(sourceURL, true); referer != "" {
		candidates = append(candidates, downloadFallbackRefererCandidate{referer: referer, origin: origin})
	}
	if referer, origin := downloadFallbackPlatformReferer(mediaURL); referer != "" {
		candidates = append(candidates, downloadFallbackRefererCandidate{referer: referer, origin: origin})
	}
	if referer, origin := downloadFallbackRefererFromURL(mediaURL, false); referer != "" {
		candidates = append(candidates, downloadFallbackRefererCandidate{referer: referer, origin: origin})
	}

	result := make([]downloadFallbackRefererCandidate, 0, len(candidates))
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		key := candidate.referer + "\n" + candidate.origin
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func downloadFallbackOriginReferer(rawURL string) (string, string) {
	if referer, origin := downloadFallbackPlatformReferer(rawURL); referer != "" {
		return referer, origin
	}
	return downloadFallbackRefererFromURL(rawURL, false)
}

func downloadFallbackPlatformReferer(rawURL string) (string, string) {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", ""
	}
	for _, item := range []struct {
		keywords []string
		referer  string
		origin   string
	}{
		{
			keywords: []string{"douyinvod.com", "douyin.com", "365yg.com", "amemv.com", "byteimg.com", "snssdk.com"},
			referer:  "https://www.douyin.com/",
			origin:   "https://www.douyin.com",
		},
		{
			keywords: []string{"kuaishou.com", "kwai.net"},
			referer:  "https://www.kuaishou.com/",
			origin:   "https://www.kuaishou.com",
		},
		{
			keywords: []string{"bilibili.com", "bilivideo.com", "hdslb.com"},
			referer:  "https://www.bilibili.com/",
			origin:   "https://www.bilibili.com",
		},
		{
			keywords: []string{"xiaohongshu.com", "xhscdn.com"},
			referer:  "https://www.xiaohongshu.com/",
			origin:   "https://www.xiaohongshu.com",
		},
		{
			keywords: []string{"weibo.com", "sinaimg.cn"},
			referer:  "https://weibo.com/",
			origin:   "https://weibo.com",
		},
		{
			keywords: []string{"youtube.com", "googlevideo.com", "ytimg.com"},
			referer:  "https://www.youtube.com/",
			origin:   "https://www.youtube.com",
		},
	} {
		for _, keyword := range item.keywords {
			if strings.Contains(host, keyword) {
				return item.referer, item.origin
			}
		}
	}
	return "", ""
}

func downloadFallbackRefererFromURL(rawURL string, keepPath bool) (string, string) {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	origin := scheme + "://" + parsed.Host
	if keepPath {
		parsed.Fragment = ""
		parsed.User = nil
		parsed.Scheme = scheme
		if parsed.Path != "" && parsed.Path != "/" {
			return parsed.String(), origin
		}
	}
	return origin + "/", origin
}

func downloadFallbackSecFetchDest(mediaType string) string {
	switch normalizeDownloadFallbackMediaType(mediaType) {
	case "audio":
		return "audio"
	case "image":
		return "image"
	default:
		return "video"
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func appendDownloadFallbackOriginProfile(profiles *[]map[string]string, seen map[string]struct{}, profile map[string]string) {
	key := downloadFallbackOriginProfileKey(profile)
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*profiles = append(*profiles, profile)
}

func downloadFallbackOriginProfileKey(profile map[string]string) string {
	keys := make([]string, 0, len(profile))
	for key := range profile {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(strings.ToLower(key))
		builder.WriteByte('=')
		builder.WriteString(profile[key])
		builder.WriteByte('\n')
	}
	return builder.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func runDownloadFallbackTask(taskID string) {
	task, ok := globalDownloadFallbackStore.get(taskID)
	if !ok {
		return
	}

	globalDownloadFallbackStore.update(taskID, func(t *downloadFallbackTask) {
		t.Status = downloadFallbackStatusRunning
		t.Progress = 1
		t.Message = ""
		t.UpdatedAt = time.Now()
	})
	logInfof("download fallback task started id=%s type=%s target=%s", taskID, task.MediaType, targetForLog(task.MediaURL))

	mode := normalizeDownloadFallbackMode(task.Mode)
	transfer := beginDownloadFallbackTransfer(nil, mode, downloadFallbackRequest{
		SourceURL: task.SourceURL,
		MediaURL:  task.MediaURL,
		MediaType: task.MediaType,
		ShareID:   task.ShareID,
		UserID:    task.UserID,
		PublicID:  task.PublicID,
		ClientIP:  task.ClientIP,
		UserAgent: task.UserAgent,
	}, taskID)
	ctx, cancel := context.WithTimeout(context.Background(), downloadFallbackTimeout())
	defer cancel()
	if err := downloadFallbackMedia(ctx, taskID, task, transfer.RequestID); err != nil {
		logWarnf("download fallback task failed id=%s target=%s error=%v", taskID, targetForLog(task.MediaURL), err)
		globalDownloadFallbackStore.fail(taskID, err.Error())
		finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusFailed, transfer.BytesTransferred, 0, err.Error())
		return
	}

	globalDownloadFallbackStore.update(taskID, func(t *downloadFallbackTask) {
		t.Status = downloadFallbackStatusCompleted
		t.Progress = 100
		t.Message = ""
		t.UpdatedAt = time.Now()
		t.ExpiresAt = time.Now().Add(downloadFallbackTTL())
	})
	completedTask, _ := globalDownloadFallbackStore.get(taskID)
	finishDownloadFallbackTransfer(transfer, downloadFallbackEventStatusCompleted, completedTask.FileSize, http.StatusOK, "")
	logInfof("download fallback task completed id=%s key=%s bytes=%d", taskID, task.FileKey, completedTask.FileSize)
}

func downloadFallbackMedia(ctx context.Context, taskID string, task downloadFallbackTask, transferID string) error {
	if err := os.MkdirAll(filepath.Dir(task.FilePath), 0o755); err != nil {
		return err
	}
	tmpDir := downloadFallbackTmpDir()
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}

	tmpSuffix, err := secureRandomHex(6)
	if err != nil {
		return errors.New("secure entropy unavailable for temporary file")
	}
	tmpName := task.FileKey + "." + tmpSuffix + ".tmp"
	tmpPath := filepath.Join(tmpDir, tmpName)
	defer os.Remove(tmpPath)

	resp, err := doDownloadFallbackOriginRequest(ctx, task.SourceURL, task.MediaURL, task.MediaType, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := strings.TrimSpace(resp.Header.Get("X-Error-Reason1"))
		if reason != "" {
			return fmt.Errorf("origin returned HTTP %d: %s", resp.StatusCode, reason)
		}
		return fmt.Errorf("origin returned HTTP %d", resp.StatusCode)
	}

	maxBytes := downloadFallbackMaxBytes(task.MediaType)
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("file too large: %d bytes exceeds %d bytes", resp.ContentLength, maxBytes)
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if !downloadFallbackContentTypeAllowed(task.MediaType, contentType) {
		return fmt.Errorf("unsupported content type: %s", firstNonEmptyString(contentType, "unknown"))
	}

	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := &progressWriter{
		total: resp.ContentLength,
		update: func(progress int, written int64) {
			globalDownloadFallbackTransfers.updateBytes(transferID, written)
			globalDownloadFallbackStore.update(taskID, func(t *downloadFallbackTask) {
				t.Progress = progress
				t.FileSize = written
				t.ContentType = contentType
				t.UpdatedAt = time.Now()
			})
		},
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	written, err := copyWithProgress(file, limited, writer)
	if err != nil {
		return err
	}
	if written > maxBytes {
		return fmt.Errorf("file too large: exceeds %d bytes", maxBytes)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, task.FilePath); err != nil {
		return err
	}

	globalDownloadFallbackStore.update(taskID, func(t *downloadFallbackTask) {
		t.FileSize = written
		t.ContentType = contentType
		t.Progress = 99
		t.UpdatedAt = time.Now()
	})
	return nil
}

func copyWithProgress(dst io.Writer, src io.Reader, progress *progressWriter) (int64, error) {
	buf := make([]byte, 256*1024)
	var written int64
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
				progress.add(int64(nw))
			}
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return written, nil
			}
			return written, er
		}
	}
}

func (w *progressWriter) add(n int64) {
	w.written += n
	now := time.Now()
	if now.Sub(w.lastUpdate) < 500*time.Millisecond && w.total > 0 && w.written < w.total {
		return
	}
	w.lastUpdate = now
	progress := 50
	if w.total > 0 {
		progress = int(float64(w.written) / float64(w.total) * 95)
	}
	if progress < 1 {
		progress = 1
	}
	if progress > 95 {
		progress = 95
	}
	w.update(progress, w.written)
}

func decorateDownloadFallbackTask(c *gin.Context, task downloadFallbackTask) (gin.H, error) {
	node := currentClusterNodeInfo()
	nodeSelector := downloadFallbackNodeSelector(node)
	payload := gin.H{
		"taskId":    task.TaskID,
		"status":    task.Status,
		"progress":  task.Progress,
		"mediaType": task.MediaType,
		"message":   task.Message,
	}
	if task.Status == downloadFallbackStatusCompleted && task.FileKey != "" {
		downloadURL, err := buildDownloadFallbackFileURL(c, nodeSelector, task.FileKey)
		if err != nil {
			return nil, err
		}
		payload["downloadUrl"] = downloadURL
		payload["url"] = downloadURL
	}
	if task.Status == downloadFallbackStatusRunning || task.Status == downloadFallbackStatusPending {
		pollURL, err := buildDownloadFallbackPollPath(nodeSelector, task.TaskID, task.FileKey)
		if err != nil {
			return nil, err
		}
		payload["pollUrl"] = pollURL
	}
	return payload, nil
}

func downloadFallbackResponseData(c *gin.Context, task downloadFallbackTask) (interface{}, error) {
	if task.Status == downloadFallbackStatusCompleted && task.FileKey != "" {
		node := currentClusterNodeInfo()
		if downloadFallbackMode() == runtimecfg.DownloadFallbackModeCDN {
			if url, err := buildDownloadFallbackCDNFileURL(node, task.FileKey); err == nil {
				return url, nil
			}
		}
		return buildDownloadFallbackFileURL(c, downloadFallbackNodeSelector(node), task.FileKey)
	}
	return decorateDownloadFallbackTask(c, task)
}

func completedDownloadFallbackTaskFromFile(req downloadFallbackRequest, fileKey, filePath string) *downloadFallbackTask {
	stat, err := os.Stat(filePath)
	if err != nil || stat.IsDir() {
		return nil
	}
	expiresAt := stat.ModTime().Add(downloadFallbackTTL())
	if time.Now().After(expiresAt) {
		_ = os.Remove(filePath)
		return nil
	}
	return &downloadFallbackTask{
		TaskID:    "cached-" + strings.TrimSuffix(fileKey, filepath.Ext(fileKey)),
		Status:    downloadFallbackStatusCompleted,
		Progress:  100,
		SourceURL: req.SourceURL,
		MediaURL:  req.MediaURL,
		MediaType: req.MediaType,
		Mode:      downloadFallbackMode(),
		ShareID:   req.ShareID,
		UserID:    req.UserID,
		PublicID:  req.PublicID,
		ClientIP:  req.ClientIP,
		UserAgent: req.UserAgent,
		FileKey:   fileKey,
		FilePath:  filePath,
		FileSize:  stat.Size(),
		CreatedAt: stat.ModTime(),
		UpdatedAt: stat.ModTime(),
		ExpiresAt: expiresAt,
	}
}

func (store *downloadFallbackStore) enqueue(req downloadFallbackRequest, fileKey, filePath string) (downloadFallbackTask, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked(time.Now())
	if existingID := store.byKey[fileKey]; existingID != "" {
		if existing := store.tasks[existingID]; existing != nil {
			switch existing.Status {
			case downloadFallbackStatusPending, downloadFallbackStatusRunning:
				return *existing, false, nil
			case downloadFallbackStatusCompleted:
				if _, err := os.Stat(existing.FilePath); err == nil {
					return *existing, false, nil
				}
			}
			delete(store.tasks, existingID)
		}
		delete(store.byKey, fileKey)
	}
	if limit := downloadFallbackConcurrency(); limit > 0 && store.activeLocked() >= limit {
		return downloadFallbackTask{}, false, errors.New("兜底下载任务较多，请稍后重试")
	}
	taskSuffix, err := secureRandomHex(16)
	if err != nil {
		return downloadFallbackTask{}, false, errors.New("secure entropy unavailable for fallback task")
	}
	taskID := "fb_" + time.Now().Format("20060102150405") + "_" + taskSuffix
	now := time.Now()
	task := &downloadFallbackTask{
		TaskID:    taskID,
		Status:    downloadFallbackStatusPending,
		Progress:  0,
		SourceURL: req.SourceURL,
		MediaURL:  req.MediaURL,
		MediaType: req.MediaType,
		Mode:      downloadFallbackMode(),
		ShareID:   req.ShareID,
		UserID:    req.UserID,
		PublicID:  req.PublicID,
		ClientIP:  req.ClientIP,
		UserAgent: req.UserAgent,
		FileKey:   fileKey,
		FilePath:  filePath,
		CreatedAt: now,
		UpdatedAt: now,
	}
	store.tasks[taskID] = task
	store.byKey[fileKey] = taskID
	return *task, true, nil
}

func (store *downloadFallbackStore) get(taskID string) (downloadFallbackTask, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	task, ok := store.tasks[taskID]
	if !ok || task == nil {
		return downloadFallbackTask{}, false
	}
	return *task, true
}

func (store *downloadFallbackStore) update(taskID string, fn func(task *downloadFallbackTask)) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.tasks[taskID]
	if !ok || task == nil {
		return
	}
	fn(task)
}

func (store *downloadFallbackStore) fail(taskID, message string) {
	store.update(taskID, func(task *downloadFallbackTask) {
		task.Status = downloadFallbackStatusFailed
		task.Message = message
		task.Progress = 0
		task.UpdatedAt = time.Now()
	})
}

func (store *downloadFallbackStore) activeLocked() int {
	count := 0
	for _, task := range store.tasks {
		if task == nil {
			continue
		}
		if task.Status == downloadFallbackStatusPending || task.Status == downloadFallbackStatusRunning {
			count++
		}
	}
	return count
}

func (store *downloadFallbackStore) pruneLocked(now time.Time) {
	retention := downloadFallbackTTL()
	for id, task := range store.tasks {
		if task == nil {
			delete(store.tasks, id)
			continue
		}
		if task.Status == downloadFallbackStatusPending || task.Status == downloadFallbackStatusRunning {
			continue
		}
		if retention > 0 && now.Sub(task.UpdatedAt) > retention {
			delete(store.byKey, task.FileKey)
			delete(store.tasks, id)
		}
	}
}

func startDownloadFallbackCleaner() func() {
	interval := time.Duration(envInt("DOWNLOAD_FALLBACK_CLEAN_INTERVAL_SECONDS", 300)) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanupDownloadFallbackFiles()
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

func cleanupDownloadFallbackFiles() {
	root := downloadFallbackPublicDir()
	ttl := downloadFallbackTTL()
	if ttl <= 0 {
		return
	}
	now := time.Now()
	_ = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		if now.Sub(info.ModTime()) > ttl {
			_ = os.Remove(filePath)
		}
		return nil
	})
}

func buildDownloadFallbackFileKey(req downloadFallbackRequest) string {
	ext := downloadFallbackExt(req.MediaType, req.MediaURL)
	sum := sha256.Sum256([]byte(req.SourceURL + "\n" + req.MediaURL + "\n" + req.MediaType))
	return req.MediaType + "_" + hex.EncodeToString(sum[:])[:32] + ext
}

func buildDownloadFallbackProxyTaskID(req downloadFallbackRequest) string {
	mediaType := normalizeDownloadFallbackMediaType(req.MediaType)
	if mediaType == "" {
		mediaType = "video"
	}
	userKey := strings.TrimSpace(req.PublicID)
	if userKey == "" && req.UserID > 0 {
		userKey = strconv.FormatInt(req.UserID, 10)
	}
	if userKey == "" {
		userKey = strings.TrimSpace(req.ClientIP)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(req.SourceURL),
		strings.TrimSpace(req.MediaURL),
		mediaType,
		strings.TrimSpace(req.ShareID),
		userKey,
	}, "\n")))
	return "proxy_" + hex.EncodeToString(sum[:])[:24]
}

func buildDownloadFallbackFileURL(c *gin.Context, nodeID, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("download fallback file key is required")
	}
	expires := time.Now().Add(downloadFallbackTTL()).Unix()
	base := downloadFallbackPublicBaseURL(c)
	ticket := signDownloadFallbackTicket("file", nodeID, key, expires)
	if ticket == "" {
		return "", errors.New("download fallback signing is unavailable")
	}
	return base + "/api/download/cdn/" + ticket, nil
}

func buildDownloadFallbackCDNFileURL(node clusterNodeInfo, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("download fallback file key is required")
	}
	base := downloadFallbackCDNBaseURL()
	if base == "" {
		return "", errors.New("download fallback cdn base url is not configured")
	}
	expires := time.Now().Add(downloadFallbackTTL()).Unix()
	ticket := signDownloadFallbackTicket("file", downloadFallbackNodeSelector(node), key, expires)
	if ticket == "" {
		return "", errors.New("download fallback signing is unavailable")
	}
	return base + "/api/download/cdn/" + ticket, nil
}

func buildDownloadFallbackProxyURL(c *gin.Context, req downloadFallbackRequest) (string, error) {
	if strings.TrimSpace(req.MediaURL) == "" {
		return "", errors.New("download fallback media URL is required")
	}
	expires := time.Now().Add(downloadFallbackTTL()).Unix()
	nodeID := downloadFallbackNodeSelector(currentClusterNodeInfo())
	proxyTicket := signDownloadFallbackProxyTicket(req, expires)
	if proxyTicket == "" {
		return "", errors.New("download fallback signing is unavailable")
	}
	publicTicket := signDownloadFallbackTicket("proxy", nodeID, proxyTicket, expires)
	if publicTicket == "" {
		return "", errors.New("download fallback signing is unavailable")
	}
	return downloadFallbackPublicBaseURL(c) + "/api/download/proxy/" + publicTicket, nil
}

func downloadFallbackPublicBaseURL(c *gin.Context) string {
	base := runtimecfg.DownloadFallbackPublicBaseURL()
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("DOWNLOAD_FALLBACK_PUBLIC_BASE_URL")), "/")
	}
	if base == "" {
		base = buildPublicBaseURL(c)
	}
	return strings.TrimRight(base, "/")
}

func downloadFallbackCDNBaseURL() string {
	base := runtimecfg.DownloadFallbackCDNBaseURL()
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("DOWNLOAD_FALLBACK_CDN_BASE_URL")), "/")
	}
	return strings.TrimRight(base, "/")
}

func buildDownloadFallbackPollPath(nodeID, taskID, fileKey string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return "", errors.New("download fallback task ID is required")
	}
	expires := time.Now().Add(downloadFallbackTTL()).Unix()
	ticket := signDownloadFallbackTicket("status", nodeID, joinDownloadFallbackStatusValue(taskID, fileKey), expires)
	if ticket == "" {
		return "", errors.New("download fallback signing is unavailable")
	}
	return "/api/download/status/" + ticket, nil
}

func joinDownloadFallbackStatusValue(taskID, fileKey string) string {
	taskID = strings.TrimSpace(taskID)
	fileKey = strings.TrimSpace(fileKey)
	if fileKey == "" {
		return taskID
	}
	return taskID + "~" + fileKey
}

func splitDownloadFallbackStatusValue(value string) (taskID, fileKey string) {
	parts := strings.SplitN(strings.TrimSpace(value), "~", 2)
	taskID = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		fileKey = strings.TrimSpace(parts[1])
	}
	return taskID, fileKey
}

func buildDownloadFallbackFilePath(nodeID, key string) string {
	return "/api/download/node/" + neturl.PathEscape(nodeID) + "/file/" + neturl.PathEscape(key)
}

func signDownloadFallbackToken(key string, expires int64) string {
	secret := downloadFallbackTokenSecret()
	if secret == "" || strings.TrimSpace(key) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(key))
	_, _ = mac.Write([]byte("|"))
	_, _ = mac.Write([]byte(strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyDownloadFallbackToken(key string, expires int64, token string) bool {
	expected := signDownloadFallbackToken(key, expires)
	if expected == "" {
		return false
	}
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(token)))
}

func signDownloadFallbackTicket(kind, node, value string, expires int64) string {
	secret := downloadFallbackTokenSecret()
	if secret == "" {
		return ""
	}
	kind = strings.TrimSpace(kind)
	node = strings.TrimSpace(node)
	value = strings.TrimSpace(value)
	if kind == "" || node == "" || value == "" || expires <= time.Now().Unix() {
		return ""
	}
	expiresRaw := strconv.FormatInt(expires, 10)
	body := strings.Join([]string{kind, node, value, expiresRaw}, "|")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(body + "|" + sig))
}

func parseDownloadFallbackTicket(raw, expectedKind string) (node string, value string, expires int64, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", 0, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", "", 0, false
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 5 {
		return "", "", 0, false
	}
	kind := strings.TrimSpace(parts[0])
	node = strings.TrimSpace(parts[1])
	value = strings.TrimSpace(parts[2])
	expiresRaw := strings.TrimSpace(parts[3])
	sig := strings.TrimSpace(parts[4])
	if kind != expectedKind || node == "" || value == "" || sig == "" {
		return "", "", 0, false
	}
	expires, err = strconv.ParseInt(expiresRaw, 10, 64)
	if err != nil || expires <= time.Now().Unix() {
		return "", "", 0, false
	}
	body := strings.Join([]string{kind, node, value, expiresRaw}, "|")
	secret := downloadFallbackTokenSecret()
	if secret == "" {
		return "", "", 0, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return "", "", 0, false
	}
	return node, value, expires, true
}

func signDownloadFallbackProxyTicket(req downloadFallbackRequest, expires int64) string {
	secret := downloadFallbackTokenSecret()
	if secret == "" || strings.TrimSpace(req.MediaURL) == "" || expires <= time.Now().Unix() {
		return ""
	}
	payload := downloadFallbackProxyTicket{
		SourceURL: strings.TrimSpace(req.SourceURL),
		MediaURL:  strings.TrimSpace(req.MediaURL),
		MediaType: normalizeDownloadFallbackMediaType(req.MediaType),
		ShareID:   strings.TrimSpace(req.ShareID),
		Attempt:   req.Attempt,
		UserID:    req.UserID,
		PublicID:  strings.TrimSpace(req.PublicID),
		ClientIP:  strings.TrimSpace(req.ClientIP),
		UserAgent: limitRunes(strings.TrimSpace(req.UserAgent), 512),
		Expires:   expires,
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	body := base64.RawURLEncoding.EncodeToString(bytes)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))
	return body + "." + sig
}

func parseDownloadFallbackProxyTicket(raw string) (downloadFallbackProxyTicket, bool) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return downloadFallbackProxyTicket{}, false
	}
	body := parts[0]
	sig := parts[1]
	secret := downloadFallbackTokenSecret()
	if secret == "" {
		return downloadFallbackProxyTicket{}, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return downloadFallbackProxyTicket{}, false
	}
	bytes, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return downloadFallbackProxyTicket{}, false
	}
	var payload downloadFallbackProxyTicket
	if err := json.Unmarshal(bytes, &payload); err != nil {
		return downloadFallbackProxyTicket{}, false
	}
	payload.SourceURL = strings.TrimSpace(payload.SourceURL)
	payload.MediaURL = strings.TrimSpace(payload.MediaURL)
	payload.MediaType = normalizeDownloadFallbackMediaType(payload.MediaType)
	payload.ShareID = strings.TrimSpace(payload.ShareID)
	if payload.MediaType == "" || payload.MediaURL == "" || payload.Expires <= time.Now().Unix() {
		return downloadFallbackProxyTicket{}, false
	}
	if err := validateRemoteTarget(payload.MediaURL); err != nil {
		return downloadFallbackProxyTicket{}, false
	}
	return payload, true
}

func doGuardedDownloadFallbackRequest(ctx context.Context, req *http.Request, maxRedirects int, afterValidation func(*http.Request, []*http.Request) error) (*http.Response, error) {
	if ctx == nil || req == nil || req.URL == nil {
		return nil, errors.New("invalid fallback request")
	}
	fetcher, err := netguard.NewDefaultFetcher()
	if err != nil {
		return nil, err
	}
	return fetcher.HTTPClientWithRedirect(ctx, maxRedirects, afterValidation).Do(req)
}

func downloadFallbackEnabled() bool {
	return runtimecfg.DownloadFallbackEnabled()
}

func downloadFallbackMode() string {
	refreshSharedRuntimeSettings()
	return runtimecfg.DownloadFallbackMode()
}

func downloadFallbackProxyTimeout() time.Duration {
	seconds := envInt("DOWNLOAD_FALLBACK_PROXY_TIMEOUT_SECONDS", 900)
	if seconds <= 0 {
		seconds = 900
	}
	return time.Duration(seconds) * time.Second
}

func downloadFallbackBaseDir() string {
	return strings.TrimSpace(firstNonEmptyString(os.Getenv("DOWNLOAD_FALLBACK_TMP_DIR"), filepath.Join("cache", "download-fallback")))
}

func downloadFallbackPublicDir() string {
	return filepath.Join(downloadFallbackBaseDir(), "public")
}

func downloadFallbackTmpDir() string {
	return filepath.Join(downloadFallbackBaseDir(), "tmp")
}

func downloadFallbackPublicFilePath(key string) string {
	return filepath.Join(downloadFallbackPublicDir(), key)
}

func downloadFallbackTTL() time.Duration {
	seconds := envInt("DOWNLOAD_FALLBACK_TTL_SECONDS", 3600)
	if seconds <= 0 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func downloadFallbackTimeout() time.Duration {
	seconds := envInt("DOWNLOAD_FALLBACK_TIMEOUT_SECONDS", 300)
	if seconds <= 0 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func downloadFallbackConcurrency() int {
	limit := envInt("DOWNLOAD_FALLBACK_CONCURRENCY", 2)
	if limit <= 0 {
		return 2
	}
	if limit > 8 {
		return 8
	}
	return limit
}

func downloadFallbackMaxBytes(mediaType string) int64 {
	switch normalizeDownloadFallbackMediaType(mediaType) {
	case "audio":
		return int64(envInt("DOWNLOAD_FALLBACK_AUDIO_MAX_BYTES", 50*1024*1024))
	case "image":
		return int64(envInt("DOWNLOAD_FALLBACK_IMAGE_MAX_BYTES", 20*1024*1024))
	default:
		return int64(envInt("DOWNLOAD_FALLBACK_MAX_FILE_BYTES", 300*1024*1024))
	}
}

func downloadFallbackTokenSecret() string {
	return strings.TrimSpace(currentApplicationDownloadConfig().TokenSecret)
}

func downloadFallbackUserAgent() string {
	return firstNonEmptyString(
		os.Getenv("DOWNLOAD_FALLBACK_USER_AGENT"),
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36",
	)
}

func downloadFallbackAcceptHeader(mediaType string) string {
	switch mediaType {
	case "video":
		return "video/webm,video/mp4,video/*;q=0.9,*/*;q=0.8"
	case "audio":
		return "audio/*,*/*;q=0.8"
	case "image":
		return "image/avif,image/webp,image/apng,image/*,*/*;q=0.8"
	default:
		return "*/*"
	}
}

func downloadFallbackContentTypeAllowed(mediaType, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" || contentType == "application/octet-stream" {
		return true
	}
	switch mediaType {
	case "video":
		return strings.HasPrefix(contentType, "video/")
	case "audio":
		return strings.HasPrefix(contentType, "audio/")
	case "image":
		return strings.HasPrefix(contentType, "image/")
	default:
		return false
	}
}

func normalizeDownloadFallbackMediaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "video", "audio", "image":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func downloadFallbackExt(mediaType, rawURL string) string {
	parsed, err := neturl.Parse(rawURL)
	if err == nil {
		ext := strings.ToLower(path.Ext(parsed.Path))
		switch ext {
		case ".mp4", ".mov", ".m4v", ".webm", ".mp3", ".m4a", ".aac", ".wav", ".jpg", ".jpeg", ".png", ".webp":
			return ext
		}
	}
	switch mediaType {
	case "audio":
		return ".mp3"
	case "image":
		return ".jpg"
	default:
		return ".mp4"
	}
}

func validDownloadFallbackKey(key string) bool {
	if !downloadFallbackKeyPattern.MatchString(key) {
		return false
	}
	clean := filepath.Clean(key)
	return clean == key && !strings.Contains(key, string(filepath.Separator))
}

func isCurrentClusterNodeSelector(node string) bool {
	node = strings.TrimSpace(node)
	if node == "" {
		return false
	}
	current := currentClusterNodeInfo()
	for _, candidate := range []string{current.ID, current.Name, current.Hostname, current.AdvertiseAddr} {
		if strings.EqualFold(strings.TrimSpace(candidate), node) {
			return true
		}
	}
	if current.Role == "gateway" {
		for _, candidate := range []string{"gateway", "gateway-main", "main-gateway"} {
			if strings.EqualFold(candidate, node) {
				return true
			}
		}
	}
	return false
}

func downloadFallbackNodeEndpoint(node string) (string, bool) {
	node = strings.TrimSpace(node)
	if node == "" {
		return "", false
	}
	current := currentClusterNodeInfo()
	if isCurrentClusterNodeSelector(node) {
		return firstNonEmptyString(current.AdvertiseAddr, "http://127.0.0.1:"+firstNonEmptyString(os.Getenv("PORT"), "5001")), true
	}
	for _, worker := range configuredClusterWorkers() {
		for _, candidate := range []string{worker.Name, worker.URL, strings.TrimRight(worker.URL, "/")} {
			if strings.EqualFold(strings.TrimSpace(candidate), node) {
				return worker.URL, true
			}
		}
	}
	return "", false
}

func downloadFallbackNodeSelector(node clusterNodeInfo) string {
	for _, candidate := range []string{node.Name, node.ID, node.Hostname} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return "local"
}

func envBoolLocal(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
