package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

const (
	toolComponentYTDLP   = "yt-dlp"
	toolComponentVideoDL = "videodl"
	toolComponentMusicDL = "musicdl"
	toolComponentM3U8DL  = "m3u8dl"
)

var errToolUpdatesDisabled = errors.New("runtime tool updates are disabled; update the Docker image through GitHub Actions")

type toolUpdateStatus struct {
	Key               string    `json:"key"`
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	Status            string    `json:"status"`
	Message           string    `json:"message,omitempty"`
	ActivePath        string    `json:"activePath,omitempty"`
	PersistentPath    string    `json:"persistentPath,omitempty"`
	CurrentVersion    string    `json:"currentVersion,omitempty"`
	LatestVersion     string    `json:"latestVersion,omitempty"`
	CanUpdate         bool      `json:"canUpdate"`
	Running           bool      `json:"running"`
	LastLog           string    `json:"lastLog,omitempty"`
	LastLogPath       string    `json:"lastLogPath,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
	AutoUpdateAllowed bool      `json:"autoUpdateAllowed"`
}

type toolUpdateManager struct {
	mu      sync.Mutex
	running map[string]bool
}

var globalToolUpdateManager = &toolUpdateManager{running: map[string]bool{}}

func handleAdminToolStatus(c *gin.Context) {
	checkRemote := strings.TrimSpace(c.Query("check")) == "1"
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"settings": runtimecfg.Current(),
			"items":    globalToolUpdateManager.status(c.Request.Context(), checkRemote),
		},
	})
}

func handleAdminToolCheck(c *gin.Context) {
	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{
			"settings": runtimecfg.Current(),
			"items":    globalToolUpdateManager.status(c.Request.Context(), false),
		},
	})
}

func handleAdminToolUpdate(c *gin.Context) {
	component := strings.ToLower(strings.TrimSpace(c.Param("component")))
	if !validToolComponent(component) {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "invalid tool component"})
		return
	}
	status, err := globalToolUpdateManager.updateAsync(component)
	writeAdminAudit(c, "tool_update.disabled", "tool", component, gin.H{"error": err.Error()})
	c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error(), Data: status})
}

func startToolAutoUpdater() func() { return func() {} }

func (manager *toolUpdateManager) status(_ context.Context, _ bool) []toolUpdateStatus {
	settings := runtimecfg.Current()
	root := toolRoot(settings)
	return []toolUpdateStatus{
		disabledToolStatus(toolComponentYTDLP, "yt-dlp", "image-pinned python package", runtimecfg.YTDLPBinary(), filepath.Join(root, "venv", "bin", "yt-dlp")),
		disabledToolStatus(toolComponentVideoDL, "videodl", "image-pinned parser source", settings.UniversalParserVideoDLPath, filepath.Join(root, "CharlesPikachu", toolComponentVideoDL)),
		disabledToolStatus(toolComponentMusicDL, "musicdl", "image-pinned parser source", settings.UniversalParserMusicDLPath, filepath.Join(root, "CharlesPikachu", toolComponentMusicDL)),
		disabledToolStatus(toolComponentM3U8DL, "m3u8dl", "reserved component", "", filepath.Join(root, toolComponentM3U8DL)),
	}
}

func disabledToolStatus(key, name, kind, activePath, persistentPath string) toolUpdateStatus {
	return toolUpdateStatus{
		Key: key, Name: name, Kind: kind, Status: "disabled",
		Message: errToolUpdatesDisabled.Error(), ActivePath: activePath, PersistentPath: persistentPath,
		CanUpdate: false, Running: false, AutoUpdateAllowed: false,
	}
}

func (manager *toolUpdateManager) update(context.Context, string) (toolUpdateStatus, error) {
	return toolUpdateStatus{Status: "disabled", Message: errToolUpdatesDisabled.Error()}, errToolUpdatesDisabled
}

func (manager *toolUpdateManager) updateAsync(component string) (toolUpdateStatus, error) {
	if !validToolComponent(component) {
		return toolUpdateStatus{Key: component, Status: "error"}, errors.New("invalid tool component")
	}
	status := disabledToolStatus(component, component, "image-pinned component", "", "")
	return status, errToolUpdatesDisabled
}

func (manager *toolUpdateManager) runAutoUpdate(context.Context, runtimecfg.Settings) {}

func (manager *toolUpdateManager) tryStart(component string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.running[component] {
		return false
	}
	manager.running[component] = true
	return true
}

func (manager *toolUpdateManager) finish(component string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	delete(manager.running, component)
}

func (manager *toolUpdateManager) isRunning(component string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.running[component]
}

func validToolComponent(component string) bool {
	switch component {
	case toolComponentYTDLP, toolComponentVideoDL, toolComponentMusicDL, toolComponentM3U8DL:
		return true
	default:
		return false
	}
}

func toolRoot(settings runtimecfg.Settings) string {
	return strings.TrimSpace(firstNonEmpty(settings.ToolUpdatesRoot, runtimecfg.ToolUpdatesRoot()))
}
