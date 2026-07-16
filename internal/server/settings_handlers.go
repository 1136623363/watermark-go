package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

func handleSettingsPage(c *gin.Context) {
	c.HTML(http.StatusOK, "settings.html", gin.H{
		"title": "运行时设置",
	})
}

func handleGetSettings(c *gin.Context) {
	refreshSharedRuntimeSettings()
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: runtimecfg.Current(),
	})
}

func handleSaveSettings(c *gin.Context) {
	var req runtimecfg.Settings
	if err := c.ShouldBindJSON(&req); err != nil {
		logWarnf("runtime settings payload rejected: %v", err)
		c.JSON(http.StatusBadRequest, httpResponse{
			Code: 1004,
			Msg:  "invalid settings payload",
		})
		return
	}

	settings, err := runtimecfg.Update(req)
	if err != nil {
		logWarnf("runtime settings update rejected: %v", err)
		c.JSON(http.StatusBadRequest, httpResponse{
			Code: 1004,
			Msg:  err.Error(),
		})
		return
	}
	if err := persistSharedRuntimeSettings(settings); err != nil {
		logErrorf("runtime settings shared persist failed: %v", err)
		c.JSON(http.StatusInternalServerError, httpResponse{
			Code: 1001,
			Msg:  "save shared settings failed",
		})
		return
	}

	applyRuntimeSettings()
	logInfof(
		"runtime settings updated rate_limit=%t proxy_configured=%t http_timeout_seconds=%d ytdlp_timeout_seconds=%d parser_engine=%s parser_fallback=%t download_fallback_enabled=%t download_fallback_mode=%s ytdlp_binary=%s ffmpeg_binary=%s",
		settings.RateLimitEnabled,
		strings.TrimSpace(settings.OutboundProxy) != "",
		settings.HTTPTimeoutSeconds,
		settings.YTDLPTimeoutSeconds,
		settings.ParserEngine,
		settings.ParserFallbackEnabled,
		settings.DownloadFallbackEnabled,
		settings.DownloadFallbackMode,
		settings.YTDLPBinary,
		settings.FFMPEGBinary,
	)
	writeAdminAudit(c, "runtime_settings.update", "runtime_settings", "current", settings)

	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: settings,
	})
}
