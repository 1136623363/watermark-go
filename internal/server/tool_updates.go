package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"watermark-backend/internal/runtimecfg"
)

const (
	toolComponentYTDLP   = "yt-dlp"
	toolComponentVideoDL = "videodl"
	toolComponentMusicDL = "musicdl"
	toolComponentM3U8DL  = "m3u8dl"
)

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
			"items":    globalToolUpdateManager.status(c.Request.Context(), true),
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
	if err != nil {
		writeAdminAudit(c, "tool_update.failed", "tool", component, gin.H{"error": err.Error()})
		c.JSON(http.StatusOK, httpResponse{Code: 1001, Msg: err.Error(), Data: status})
		return
	}
	writeAdminAudit(c, "tool_update.started", "tool", component, gin.H{"status": status.Status})
	c.JSON(http.StatusAccepted, httpResponse{Code: 0, Msg: "update task started", Data: status})
}

func startToolAutoUpdater() func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		timer := time.NewTimer(runtimecfg.ToolUpdatesInterval())
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				settings := runtimecfg.Current()
				if settings.ToolUpdatesAutoCheckEnabled {
					globalToolUpdateManager.runAutoUpdate(ctx, settings)
				}
				timer.Reset(runtimecfg.ToolUpdatesInterval())
			}
		}
	}()
	return cancel
}

func (manager *toolUpdateManager) status(ctx context.Context, checkRemote bool) []toolUpdateStatus {
	settings := runtimecfg.Current()
	items := []toolUpdateStatus{
		manager.ytdlpStatus(ctx, settings, checkRemote),
		manager.gitStatus(ctx, settings, checkRemote, toolComponentVideoDL, "videodl", "video parser source", "https://github.com/CharlesPikachu/videodl.git", settings.UniversalParserVideoDLPath),
		manager.gitStatus(ctx, settings, checkRemote, toolComponentMusicDL, "musicdl", "music parser source", "https://github.com/CharlesPikachu/musicdl.git", settings.UniversalParserMusicDLPath),
		manager.m3u8dlStatus(settings),
	}
	return items
}

func (manager *toolUpdateManager) ytdlpStatus(ctx context.Context, settings runtimecfg.Settings, checkRemote bool) toolUpdateStatus {
	root := toolRoot(settings)
	persistent := venvExecutable(filepath.Join(root, "venv"), "yt-dlp")
	active := runtimecfg.YTDLPBinary()
	status := toolUpdateStatus{
		Key:               toolComponentYTDLP,
		Name:              "yt-dlp",
		Kind:              "python package",
		ActivePath:        active,
		PersistentPath:    persistent,
		CanUpdate:         true,
		Running:           manager.isRunning(toolComponentYTDLP),
		LastLog:           readToolLogTail(settings, toolComponentYTDLP),
		LastLogPath:       lastToolLogPath(settings, toolComponentYTDLP),
		AutoUpdateAllowed: true,
	}
	version, err := commandOutput(ctx, active, "--version")
	if err != nil {
		status.Status = "missing"
		status.Message = err.Error()
	} else {
		status.Status = "ready"
		status.CurrentVersion = compactSingleLine(version)
	}
	if checkRemote {
		if latest, err := latestPipVersion(ctx, settings, "yt-dlp"); err == nil {
			status.LatestVersion = latest
		} else if status.Message == "" {
			status.Message = "remote check failed: " + err.Error()
		}
	}
	return status
}

func (manager *toolUpdateManager) gitStatus(ctx context.Context, settings runtimecfg.Settings, checkRemote bool, key string, name string, kind string, repo string, activePath string) toolUpdateStatus {
	root := toolRoot(settings)
	persistent := filepath.Join(root, "CharlesPikachu", key)
	status := toolUpdateStatus{
		Key:               key,
		Name:              name,
		Kind:              kind,
		ActivePath:        activePath,
		PersistentPath:    persistent,
		CanUpdate:         true,
		Running:           manager.isRunning(key),
		LastLog:           readToolLogTail(settings, key),
		LastLogPath:       lastToolLogPath(settings, key),
		AutoUpdateAllowed: true,
	}
	if gitDirExists(activePath) {
		status.Status = "ready"
		status.CurrentVersion = gitCurrentRevision(ctx, activePath)
	} else if dirExists(activePath) {
		status.Status = "source-only"
		status.Message = "directory exists but is not a git checkout"
	} else {
		status.Status = "missing"
		status.Message = "source directory does not exist"
	}
	if checkRemote {
		latest, err := gitRemoteRevision(ctx, activePath, repo)
		if err == nil {
			status.LatestVersion = latest
		} else if status.Message == "" {
			status.Message = "remote check failed: " + err.Error()
		}
	}
	return status
}

func (manager *toolUpdateManager) m3u8dlStatus(settings runtimecfg.Settings) toolUpdateStatus {
	return toolUpdateStatus{
		Key:         toolComponentM3U8DL,
		Name:        "m3u8dl",
		Kind:        "reserved component",
		Status:      "reserved",
		Message:     "m3u8 merge currently uses ffmpeg; independent m3u8dl is not wired into production yet",
		CanUpdate:   false,
		Running:     manager.isRunning(toolComponentM3U8DL),
		LastLog:     readToolLogTail(settings, toolComponentM3U8DL),
		LastLogPath: lastToolLogPath(settings, toolComponentM3U8DL),
	}
}

func (manager *toolUpdateManager) update(ctx context.Context, component string) (toolUpdateStatus, error) {
	if !manager.tryStart(component) {
		return toolUpdateStatus{Key: component, Status: "running"}, errors.New("tool update is already running")
	}
	defer manager.finish(component)
	return manager.runUpdate(ctx, component)
}

func (manager *toolUpdateManager) updateAsync(component string) (toolUpdateStatus, error) {
	if !manager.tryStart(component) {
		return toolUpdateStatus{Key: component, Status: "running", Running: true}, errors.New("tool update is already running")
	}
	status := toolUpdateStatus{
		Key:     component,
		Status:  "running",
		Message: "update task started",
		Running: true,
	}
	go func() {
		defer manager.finish(component)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		finalStatus, err := manager.runUpdate(ctx, component)
		if err != nil {
			logWarnf("tool update failed component=%s error=%v", component, err)
			return
		}
		logInfof("tool update finished component=%s status=%s version=%s", component, finalStatus.Status, finalStatus.CurrentVersion)
	}()
	return status, nil
}

func (manager *toolUpdateManager) runUpdate(ctx context.Context, component string) (toolUpdateStatus, error) {
	settings := runtimecfg.Current()
	logFile, err := createToolLog(settings, component)
	if err != nil {
		return toolUpdateStatus{Key: component, Status: "error"}, err
	}
	logger := &toolCommandLogger{file: logFile}
	defer logger.close()

	logger.writef("tool update started component=%s at=%s", component, time.Now().Format(time.RFC3339))
	var status toolUpdateStatus
	switch component {
	case toolComponentYTDLP:
		status, err = manager.updateYTDLP(ctx, settings, logger)
	case toolComponentVideoDL:
		status, err = manager.updateGitSource(ctx, settings, logger, toolComponentVideoDL, "https://github.com/CharlesPikachu/videodl.git")
	case toolComponentMusicDL:
		status, err = manager.updateGitSource(ctx, settings, logger, toolComponentMusicDL, "https://github.com/CharlesPikachu/musicdl.git")
	case toolComponentM3U8DL:
		err = errors.New("m3u8dl is reserved; current m3u8 merge uses ffmpeg and follows Docker image releases")
		status = manager.m3u8dlStatus(settings)
	default:
		err = errors.New("invalid tool component")
		status = toolUpdateStatus{Key: component, Status: "error"}
	}
	if err != nil {
		logger.writef("tool update failed component=%s error=%v", component, err)
		status.Status = "error"
		status.Message = err.Error()
		status.LastLog = readFileTail(logFile.Name(), 12000)
		status.LastLogPath = logFile.Name()
		return status, err
	}
	logger.writef("tool update finished component=%s at=%s", component, time.Now().Format(time.RFC3339))
	status.LastLog = readFileTail(logFile.Name(), 12000)
	status.LastLogPath = logFile.Name()
	status.UpdatedAt = time.Now()
	return status, nil
}

func (manager *toolUpdateManager) updateYTDLP(ctx context.Context, settings runtimecfg.Settings, logger *toolCommandLogger) (toolUpdateStatus, error) {
	venvDir, pythonBin, err := ensureToolVenv(ctx, settings, logger)
	if err != nil {
		return toolUpdateStatus{Key: toolComponentYTDLP}, err
	}
	if err := logger.run(ctx, "", pythonBin, "-m", "pip", "install", "--no-cache-dir", "--timeout", "60", "--retries", "2", "-U", "yt-dlp"); err != nil {
		return toolUpdateStatus{Key: toolComponentYTDLP}, err
	}
	binary := venvExecutable(venvDir, "yt-dlp")
	version, err := commandOutput(ctx, binary, "--version")
	if err != nil {
		return toolUpdateStatus{Key: toolComponentYTDLP}, err
	}
	if err := updateRuntimeSettings(func(next *runtimecfg.Settings) {
		next.YTDLPBinary = binary
		next.UniversalParserPythonBin = pythonBin
	}); err != nil {
		return toolUpdateStatus{Key: toolComponentYTDLP}, err
	}
	return manager.ytdlpStatus(ctx, runtimecfg.Current(), false).withVersion(compactSingleLine(version)), nil
}

func (manager *toolUpdateManager) updateGitSource(ctx context.Context, settings runtimecfg.Settings, logger *toolCommandLogger, component string, repo string) (toolUpdateStatus, error) {
	root := toolRoot(settings)
	target := filepath.Join(root, "CharlesPikachu", component)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return toolUpdateStatus{Key: component}, err
	}

	if !dirExists(target) {
		if err := logger.run(ctx, "", "git", "clone", "--depth", "1", repo, target); err != nil {
			return toolUpdateStatus{Key: component}, err
		}
	} else if gitDirExists(target) {
		if err := logger.run(ctx, target, "git", "fetch", "--all", "--prune"); err != nil {
			return toolUpdateStatus{Key: component}, err
		}
		ref := gitDefaultRemoteRef(ctx, target)
		if err := logger.run(ctx, target, "git", "reset", "--hard", ref); err != nil {
			return toolUpdateStatus{Key: component}, err
		}
	} else {
		return toolUpdateStatus{Key: component}, fmt.Errorf("%s exists but is not a git checkout", target)
	}

	_, pythonBin, err := ensureToolVenv(ctx, settings, logger)
	if err != nil {
		return toolUpdateStatus{Key: component}, err
	}
	requirements := filepath.Join(target, "requirements.txt")
	if fileExists(requirements) {
		if err := logger.run(ctx, "", pythonBin, "-m", "pip", "install", "--no-cache-dir", "--timeout", "60", "--retries", "2", "-r", requirements); err != nil {
			return toolUpdateStatus{Key: component}, err
		}
	}

	if err := updateRuntimeSettings(func(next *runtimecfg.Settings) {
		next.UniversalParserPythonBin = pythonBin
		switch component {
		case toolComponentVideoDL:
			next.UniversalParserVideoDLPath = target
		case toolComponentMusicDL:
			next.UniversalParserMusicDLPath = target
		}
	}); err != nil {
		return toolUpdateStatus{Key: component}, err
	}

	current := runtimecfg.Current()
	status := manager.gitStatus(ctx, current, false, component, component, "source", repo, target)
	status.CurrentVersion = gitCurrentRevision(ctx, target)
	return status, nil
}

func (manager *toolUpdateManager) runAutoUpdate(ctx context.Context, settings runtimecfg.Settings) {
	logInfof("tool auto update tick enabled interval=%s ytdlp=%t sources=%t", runtimecfg.ToolUpdatesInterval(), settings.ToolUpdatesAutoUpdateYTDLP, settings.ToolUpdatesAutoUpdateSources)
	if settings.ToolUpdatesAutoUpdateYTDLP {
		if _, err := manager.update(ctx, toolComponentYTDLP); err != nil {
			logWarnf("tool auto update failed component=%s error=%v", toolComponentYTDLP, err)
		}
	}
	if settings.ToolUpdatesAutoUpdateSources {
		for _, component := range []string{toolComponentVideoDL, toolComponentMusicDL} {
			if ctx.Err() != nil {
				return
			}
			if _, err := manager.update(ctx, component); err != nil {
				logWarnf("tool auto update failed component=%s error=%v", component, err)
			}
		}
	}
}

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

func updateRuntimeSettings(mutator func(*runtimecfg.Settings)) error {
	next := runtimecfg.Current()
	mutator(&next)
	if _, err := runtimecfg.Update(next); err != nil {
		return err
	}
	applyRuntimeSettings()
	return nil
}

func ensureToolVenv(ctx context.Context, settings runtimecfg.Settings, logger *toolCommandLogger) (string, string, error) {
	root := toolRoot(settings)
	venvDir := filepath.Join(root, "venv")
	pythonBin := venvPython(venvDir)
	basePython := firstAvailableCommand(settings.UniversalParserPythonBin, os.Getenv("PYTHON_BIN"), "python3", "python")
	if basePython == "" {
		return "", "", errors.New("python binary not found")
	}
	if fileExists(pythonBin) {
		if !venvUsesSystemSitePackages(venvDir) {
			logger.writef("upgrading python virtualenv with system site packages path=%s", venvDir)
			if err := logger.run(ctx, "", basePython, "-m", "venv", "--system-site-packages", "--upgrade", venvDir); err != nil {
				return "", "", err
			}
		}
		return venvDir, pythonBin, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	if err := logger.run(ctx, "", basePython, "-m", "venv", "--system-site-packages", venvDir); err != nil {
		return "", "", err
	}
	logger.writef("python virtualenv ready path=%s", pythonBin)
	return venvDir, pythonBin, nil
}

func venvUsesSystemSitePackages(venvDir string) bool {
	bytes, err := os.ReadFile(filepath.Join(venvDir, "pyvenv.cfg"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(bytes), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[0]), "include-system-site-packages") {
			return strings.EqualFold(strings.TrimSpace(parts[1]), "true")
		}
	}
	return false
}

type toolCommandLogger struct {
	mu   sync.Mutex
	file *os.File
}

func (logger *toolCommandLogger) run(ctx context.Context, dir string, name string, args ...string) error {
	logger.writef("$ %s %s", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"PIP_DEFAULT_TIMEOUT=60",
		"PIP_DISABLE_PIP_VERSION_CHECK=1",
		"PIP_PROGRESS_BAR=off",
	)
	cmd.Stdout = logger
	cmd.Stderr = logger
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func (logger *toolCommandLogger) Write(p []byte) (int, error) {
	text := strings.ReplaceAll(string(p), "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			logger.write(line)
		}
	}
	return len(p), nil
}

func (logger *toolCommandLogger) writef(format string, args ...interface{}) {
	logger.write(fmt.Sprintf(format, args...))
}

func (logger *toolCommandLogger) write(message string) {
	if logger == nil || logger.file == nil {
		return
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	_, _ = logger.file.WriteString(time.Now().Format("15:04:05") + " " + message + "\n")
}

func (logger *toolCommandLogger) close() {
	if logger != nil && logger.file != nil {
		_ = logger.file.Close()
	}
}

func createToolLog(settings runtimecfg.Settings, component string) (*os.File, error) {
	dir := filepath.Join(toolRoot(settings), "update-logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s-%s.log", safeFilePart(component), time.Now().Format("20060102-150405"))
	return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func lastToolLogPath(settings runtimecfg.Settings, component string) string {
	dir := filepath.Join(toolRoot(settings), "update-logs")
	pattern := filepath.Join(dir, safeFilePart(component)+"-*.log")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func readToolLogTail(settings runtimecfg.Settings, component string) string {
	path := lastToolLogPath(settings, component)
	if path == "" {
		return ""
	}
	return readFileTail(path, 12000)
}

func readFileTail(path string, maxBytes int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size <= 0 {
		return ""
	}
	if maxBytes <= 0 || int64(maxBytes) > size {
		maxBytes = int(size)
	}
	if _, err := file.Seek(-int64(maxBytes), io.SeekEnd); err != nil {
		return ""
	}
	bytes, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return string(bytes)
}

func toolRoot(settings runtimecfg.Settings) string {
	return strings.TrimSpace(firstNonEmpty(settings.ToolUpdatesRoot, runtimecfg.ToolUpdatesRoot()))
}

func venvExecutable(venvDir string, name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", name+".exe")
	}
	return filepath.Join(venvDir, "bin", name)
}

func venvPython(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

func latestPipVersion(ctx context.Context, settings runtimecfg.Settings, pkg string) (string, error) {
	python := firstAvailableCommand(settings.UniversalParserPythonBin, os.Getenv("PYTHON_BIN"), "python3", "python")
	if python == "" {
		return "", errors.New("python binary not found")
	}
	output, err := commandOutput(ctx, python, "-m", "pip", "index", "versions", pkg)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "available versions:") {
			versions := strings.TrimSpace(strings.TrimPrefix(line, "Available versions:"))
			first := strings.Split(versions, ",")[0]
			return strings.TrimSpace(first), nil
		}
	}
	return "", errors.New("latest version not found")
}

func gitCurrentRevision(ctx context.Context, dir string) string {
	output, err := commandOutput(ctx, "git", "-C", dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return compactSingleLine(output)
}

func gitRemoteRevision(ctx context.Context, dir string, repo string) (string, error) {
	target := repo
	if gitDirExists(dir) {
		if origin, err := commandOutput(ctx, "git", "-C", dir, "config", "--get", "remote.origin.url"); err == nil && strings.TrimSpace(origin) != "" {
			target = strings.TrimSpace(origin)
		}
	}
	output, err := commandOutput(ctx, "git", "ls-remote", target, "HEAD")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", errors.New("remote HEAD not found")
	}
	if len(fields[0]) > 12 {
		return fields[0][:12], nil
	}
	return fields[0], nil
}

func gitDefaultRemoteRef(ctx context.Context, dir string) string {
	output, err := commandOutput(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(output)
		if ref != "" && ref != "origin/HEAD" {
			return ref
		}
	}
	for _, ref := range []string{"origin/main", "origin/master"} {
		if err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", ref).Run(); err == nil {
			return ref
		}
	}
	return "origin/main"
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("command is empty")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "PIP_DISABLE_PIP_VERSION_CHECK=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := compactSingleLine(string(output))
		if text == "" {
			text = err.Error()
		}
		return "", errors.New(text)
	}
	return string(output), nil
}

func firstAvailableCommand(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if fileExists(candidate) {
			return candidate
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func compactSingleLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func safeFilePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "tool"
	}
	return builder.String()
}

func dirExists(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && !info.IsDir()
}

func gitDirExists(path string) bool {
	return dirExists(filepath.Join(strings.TrimSpace(path), ".git"))
}

func (status toolUpdateStatus) withVersion(version string) toolUpdateStatus {
	status.CurrentVersion = version
	status.Status = "ready"
	return status
}
