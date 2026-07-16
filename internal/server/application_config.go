package server

import (
	"sync"

	"github.com/1136623363/watermark-go/internal/config"
)

var (
	applicationDownloadConfigMu sync.RWMutex
	applicationDownloadConfig   config.DownloadConfig
)

func setApplicationDownloadConfig(cfg config.DownloadConfig) {
	applicationDownloadConfigMu.Lock()
	defer applicationDownloadConfigMu.Unlock()
	applicationDownloadConfig = cfg
}

func currentApplicationDownloadConfig() config.DownloadConfig {
	applicationDownloadConfigMu.RLock()
	defer applicationDownloadConfigMu.RUnlock()
	return applicationDownloadConfig
}
