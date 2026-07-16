package server

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/netguard"
)

const defaultLogDir = "logs"

var appLogger = log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

type logResources struct {
	files []*os.File
}

func setupLogging() (*logResources, error) {
	logDir := strings.TrimSpace(os.Getenv("LOG_DIR"))
	if logDir == "" {
		logDir = defaultLogDir
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory failed: %w", err)
	}

	accessFile, err := openLogFile(logDir, "access.log")
	if err != nil {
		return nil, err
	}
	errorFile, err := openLogFile(logDir, "error.log")
	if err != nil {
		_ = accessFile.Close()
		return nil, err
	}
	appFile, err := openLogFile(logDir, "app.log")
	if err != nil {
		_ = accessFile.Close()
		_ = errorFile.Close()
		return nil, err
	}

	gin.DefaultWriter = io.MultiWriter(os.Stdout, accessFile)
	gin.DefaultErrorWriter = io.MultiWriter(os.Stderr, errorFile)
	appLogger.SetOutput(io.MultiWriter(os.Stdout, appFile))
	appLogger.SetFlags(log.LstdFlags | log.Lmicroseconds)

	return &logResources{files: []*os.File{accessFile, errorFile, appFile}}, nil
}

func openLogFile(logDir, fileName string) (*os.File, error) {
	path := filepath.Join(logDir, fileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %s failed: %w", path, err)
	}
	return file, nil
}

func (resources *logResources) Close() {
	if resources == nil {
		return
	}
	for _, file := range resources.files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func logInfof(format string, args ...interface{}) {
	appLogger.Printf("INFO "+format, args...)
}

func logWarnf(format string, args ...interface{}) {
	appLogger.Printf("WARN "+format, args...)
}

func logErrorf(format string, args ...interface{}) {
	appLogger.Printf("ERROR "+format, args...)
}

func targetForLog(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "[empty-target]"
	}
	target, err := netguard.NewFetchURL(rawURL)
	if err != nil {
		return "[invalid-target]"
	}
	safe := target.Safe().String()
	separator := strings.Index(safe, "://")
	if separator < 1 {
		return "[invalid-target]"
	}
	authority := safe[separator+3:]
	if slash := strings.IndexByte(authority, '/'); slash >= 0 {
		authority = authority[:slash]
	}
	if authority == "" {
		return "[invalid-target]"
	}
	return safe[:separator] + "://" + authority
}

func compactLogMessage(message string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if len(message) > 300 {
		return message[:300] + "..."
	}
	return message
}
