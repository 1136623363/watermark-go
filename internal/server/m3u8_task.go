package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"watermark-backend/internal/runtimecfg"
)

type mergeTask struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	SourceURL string    `json:"sourceUrl,omitempty"`
	FilePath  string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type mergeTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*mergeTask
}

type mergeTaskStats struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Error   int `json:"error"`
}

var globalMergeTaskStore = &mergeTaskStore{
	tasks: make(map[string]*mergeTask),
}

func handleM3U8Merge(c *gin.Context) {
	rawURL := strings.TrimSpace(c.Query("url"))
	if err := validateRemoteTarget(rawURL); err != nil {
		c.JSON(http.StatusOK, httpResponse{
			Code: 1004,
			Msg:  err.Error(),
		})
		return
	}

	task, err := enqueueM3U8Merge(rawURL)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{
			Code: 1001,
			Msg:  err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: gin.H{"id": task.ID},
	})
}

func handleTaskStatus(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("id"))
	task, ok := globalMergeTaskStore.get(taskID)
	if !ok {
		c.JSON(http.StatusOK, httpResponse{
			Code: 1004,
			Msg:  "task not found",
		})
		return
	}

	payload := gin.H{
		"id":      task.ID,
		"status":  task.Status,
		"message": task.Message,
	}
	if task.Status == "done" && task.FilePath != "" {
		payload["url"] = buildPublicBaseURL(c) + "/api/task/file/" + task.ID
	}

	c.JSON(http.StatusOK, payload)
}

func handleTaskFile(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("id"))
	task, ok := globalMergeTaskStore.get(taskID)
	if !ok || task.Status != "done" || task.FilePath == "" {
		c.JSON(http.StatusNotFound, httpResponse{
			Code: 1004,
			Msg:  "task file not found",
		})
		return
	}
	if _, err := os.Stat(task.FilePath); err != nil {
		c.JSON(http.StatusNotFound, httpResponse{
			Code: 1004,
			Msg:  "task file not found",
		})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.mp4", task.ID))
	c.File(task.FilePath)
}

func enqueueM3U8Merge(rawURL string) (*mergeTask, error) {
	taskID, err := newTaskID()
	if err != nil {
		return nil, err
	}
	task := &mergeTask{
		ID:        taskID,
		Status:    "pending",
		SourceURL: rawURL,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if !globalMergeTaskStore.enqueue(task, m3u8QueueLimit()) {
		return nil, errors.New("too many m3u8 merge tasks, please try again later")
	}
	persistM3U8TaskQueued(task, rawURL)
	logInfof("m3u8 task queued id=%s target=%s", taskID, targetForLog(rawURL))

	go runM3U8Merge(taskID, rawURL)

	return task, nil
}

func runM3U8Merge(taskID, rawURL string) {
	logInfof("m3u8 task started id=%s target=%s", taskID, targetForLog(rawURL))
	globalMergeTaskStore.update(taskID, func(task *mergeTask) {
		task.Status = "running"
		task.Message = ""
		task.UpdatedAt = time.Now()
	})
	persistCurrentM3U8Task(taskID)

	outputDir := filepath.Join(os.TempDir(), "watermark-backend", "m3u8")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		logErrorf("m3u8 task failed id=%s stage=mkdir error=%v", taskID, err)
		globalMergeTaskStore.fail(taskID, err.Error())
		persistCurrentM3U8Task(taskID)
		return
	}
	outputFile := filepath.Join(outputDir, taskID+".mp4")

	if err := executeFFmpegMerge(rawURL, outputFile); err != nil {
		logErrorf("m3u8 task failed id=%s stage=merge target=%s error=%v", taskID, targetForLog(rawURL), err)
		globalMergeTaskStore.fail(taskID, err.Error())
		persistCurrentM3U8Task(taskID)
		return
	}

	globalMergeTaskStore.update(taskID, func(task *mergeTask) {
		task.Status = "done"
		task.Message = ""
		task.FilePath = outputFile
		task.UpdatedAt = time.Now()
	})
	persistCurrentM3U8Task(taskID)
	logInfof("m3u8 task completed id=%s file=%s", taskID, outputFile)
}

func executeFFmpegMerge(rawURL, outputFile string) error {
	bin, err := resolveFFMPEGBinary()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", rawURL,
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		outputFile,
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return errors.New("m3u8 merge timeout")
	}
	if err == nil {
		return nil
	}

	fallbackArgs := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", rawURL,
		"-c", "copy",
		outputFile,
	}
	cmd = exec.CommandContext(ctx, bin, fallbackArgs...)
	output, err = cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		text = err.Error()
	}
	if strings.Contains(strings.ToLower(text), "encrypted") || strings.Contains(strings.ToLower(text), "key") {
		return errors.New("encrypted m3u8 is not supported")
	}
	return fmt.Errorf("m3u8 merge failed: %s", text)
}

func resolveFFMPEGBinary() (string, error) {
	candidates := []string{
		runtimecfg.FFMPEGBinary(),
		"ffmpeg",
		"ffmpeg.exe",
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("ffmpeg binary not found")
}

func buildPublicBaseURL(c *gin.Context) string {
	scheme := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + c.Request.Host
}

func newTaskID() (string, error) {
	return secureRandomHex(8)
}

func (store *mergeTaskStore) get(taskID string) (*mergeTask, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	task, ok := store.tasks[taskID]
	if !ok {
		return nil, false
	}
	copyTask := *task
	return &copyTask, true
}

func (store *mergeTaskStore) list() []mergeTask {
	store.mu.RLock()
	defer store.mu.RUnlock()

	items := make([]mergeTask, 0, len(store.tasks))
	for _, task := range store.tasks {
		if task == nil {
			continue
		}
		items = append(items, *task)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items
}

func (store *mergeTaskStore) stats() mergeTaskStats {
	items := store.list()
	stats := mergeTaskStats{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case "pending":
			stats.Pending++
		case "running":
			stats.Running++
		case "done":
			stats.Done++
		case "error":
			stats.Error++
		}
	}
	return stats
}

func (store *mergeTaskStore) set(task *mergeTask) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked(time.Now())
	store.tasks[task.ID] = task
}

func (store *mergeTaskStore) enqueue(task *mergeTask, limit int) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked(time.Now())
	if limit > 0 && store.activeLocked() >= limit {
		return false
	}
	store.tasks[task.ID] = task
	return true
}

func (store *mergeTaskStore) update(taskID string, fn func(task *mergeTask)) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked(time.Now())
	task, ok := store.tasks[taskID]
	if !ok {
		return
	}
	fn(task)
}

func (store *mergeTaskStore) fail(taskID, message string) {
	store.update(taskID, func(task *mergeTask) {
		task.Status = "error"
		task.Message = message
		task.UpdatedAt = time.Now()
	})
}

func persistCurrentM3U8Task(taskID string) {
	task, ok := globalMergeTaskStore.get(taskID)
	if !ok {
		return
	}
	persistM3U8TaskStatus(task)
}

func (store *mergeTaskStore) activeLocked() int {
	count := 0
	for _, task := range store.tasks {
		if task == nil {
			continue
		}
		if task.Status == "pending" || task.Status == "running" {
			count++
		}
	}
	return count
}

func (store *mergeTaskStore) pruneLocked(now time.Time) {
	retention := m3u8TaskRetention()
	for id, task := range store.tasks {
		if task == nil {
			delete(store.tasks, id)
			continue
		}
		if task.Status != "done" && task.Status != "error" {
			continue
		}
		if retention > 0 && now.Sub(task.UpdatedAt) > retention {
			removeM3U8TaskFile(task)
			delete(store.tasks, id)
		}
	}
	maxRecords := m3u8TaskMaxRecords()
	if maxRecords <= 0 || len(store.tasks) <= maxRecords {
		return
	}
	items := make([]*mergeTask, 0, len(store.tasks))
	for _, task := range store.tasks {
		if task != nil && (task.Status == "done" || task.Status == "error") {
			items = append(items, task)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.Before(items[j].UpdatedAt)
	})
	for _, task := range items {
		if len(store.tasks) <= maxRecords {
			return
		}
		removeM3U8TaskFile(task)
		delete(store.tasks, task.ID)
	}
}

func removeM3U8TaskFile(task *mergeTask) {
	if task == nil || strings.TrimSpace(task.FilePath) == "" {
		return
	}
	_ = os.Remove(task.FilePath)
}

func m3u8QueueLimit() int {
	limit := envInt("M3U8_MAX_CONCURRENT_TASKS", 2)
	if limit <= 0 {
		return 2
	}
	return limit
}

func m3u8TaskRetention() time.Duration {
	hours := envInt("M3U8_TASK_RETENTION_HOURS", 24)
	if hours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(hours) * time.Hour
}

func m3u8TaskMaxRecords() int {
	maxRecords := envInt("M3U8_TASK_MAX_RECORDS", 1000)
	if maxRecords <= 0 {
		return 1000
	}
	return maxRecords
}
