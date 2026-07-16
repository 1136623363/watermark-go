package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
	nativeparser "github.com/1136623363/watermark-go/internal/parser/native"
)

const (
	applicationParserSessionTTL      = 15 * time.Minute
	applicationParserSessionCapacity = 64
)

var (
	applicationDownloadConfigMu sync.RWMutex
	applicationDownloadConfig   config.DownloadConfig
	applicationNativeParserMu   sync.RWMutex
	applicationNativeParser     *nativeparser.Service
	applicationRunnerConfigMu   sync.RWMutex
	applicationRunnerConfig     config.RunnerConfig
)

func setApplicationDownloadConfig(cfg config.DownloadConfig) {
	applicationDownloadConfigMu.Lock()
	defer applicationDownloadConfigMu.Unlock()
	applicationDownloadConfig = cfg
}

func newApplicationNativeParser(cfg config.ParserConfig) (*nativeparser.Service, error) {
	fetcher, err := netguard.NewDefaultFetcher()
	if err != nil {
		return nil, err
	}
	dependencies, err := newApplicationNativeDependencies(cfg, fetcher)
	if err != nil {
		return nil, err
	}
	service, err := nativeparser.NewService(dependencies)
	if err != nil {
		return nil, err
	}
	return service, nil
}

func newApplicationNativeDependencies(cfg config.ParserConfig, fetcher coreparser.HTTPClientFactory) (coreparser.Dependencies, error) {
	if fetcher == nil {
		return coreparser.Dependencies{}, errors.New("application parser requires a guarded fetcher")
	}
	provider, err := coreparser.NewSessionMaterialProvider(coreparser.SessionMaterialOptions{
		TTL: applicationParserSessionTTL, Capacity: applicationParserSessionCapacity,
	})
	if err != nil {
		return coreparser.Dependencies{}, err
	}
	loader := coreparser.SessionLoader(func(
		ctx context.Context,
		key coreparser.SessionMaterialKey,
		budget *coreparser.RequestBudget,
	) (coreparser.SensitiveMaterial, error) {
		if ctx == nil || budget == nil {
			return coreparser.SensitiveMaterial{}, coreparser.NewParseError(
				coreparser.ErrorSecurityRejected, errors.New("invalid session material request"),
			)
		}
		if err := ctx.Err(); err != nil {
			return coreparser.SensitiveMaterial{}, err
		}
		if key.Platform != coreparser.PlatformKey(nativeparser.SourceSohu) || !isSohuSessionHost(key.Host) {
			return coreparser.SensitiveMaterial{}, coreparser.NewParseError(
				coreparser.ErrorSecurityRejected, errors.New("session material scope is not allowed"),
			)
		}
		if !cfg.SohuAPIToken.Configured() {
			return coreparser.SensitiveMaterial{}, coreparser.NewParseError(
				coreparser.ErrorCredentialRequired, errors.New("Sohu credential is not configured"),
			)
		}
		material := coreparser.SensitiveMaterial{}
		if err := cfg.SohuAPIToken.Use(func(value string) error {
			material = coreparser.NewSensitiveMaterial(value)
			return nil
		}); err != nil || !material.Configured() {
			return coreparser.SensitiveMaterial{}, coreparser.NewParseError(
				coreparser.ErrorCredentialRequired, errors.New("Sohu credential is unavailable"),
			)
		}
		return material, nil
	})
	return coreparser.Dependencies{
		Fetcher: fetcher, Sessions: provider, SessionLoader: loader,
		WeiboCookie: cfg.WeiboCookie, XiguaCookie: cfg.XiguaCookie,
	}, nil
}

func isSohuSessionHost(raw string) bool {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	return host == nativeparser.SohuSessionHost
}

func setApplicationNativeParser(service *nativeparser.Service) {
	applicationNativeParserMu.Lock()
	defer applicationNativeParserMu.Unlock()
	applicationNativeParser = service
}

func currentNativeParser() (*nativeparser.Service, error) {
	applicationNativeParserMu.RLock()
	defer applicationNativeParserMu.RUnlock()
	if applicationNativeParser == nil {
		return nil, errors.New("native parser service is unavailable")
	}
	return applicationNativeParser, nil
}

func nativeParserSources() []nativeparser.SourceInfo {
	service, err := currentNativeParser()
	if err == nil {
		return service.Sources()
	}
	return nativeparser.CatalogSources()
}

func nativeParserSource(source string) (nativeparser.SourceInfo, bool) {
	service, err := currentNativeParser()
	if err == nil {
		return service.Source(source)
	}
	return nativeparser.CatalogSource(source)
}

func nativeParserSourceForURL(raw string) (string, error) {
	service, err := currentNativeParser()
	if err == nil {
		return service.SourceForURL(raw)
	}
	return nativeparser.CatalogSourceForURL(raw)
}

func currentApplicationDownloadConfig() config.DownloadConfig {
	applicationDownloadConfigMu.RLock()
	defer applicationDownloadConfigMu.RUnlock()
	return applicationDownloadConfig
}

func setApplicationRunnerConfig(cfg config.RunnerConfig) {
	applicationRunnerConfigMu.Lock()
	defer applicationRunnerConfigMu.Unlock()
	applicationRunnerConfig = cfg
}

func currentApplicationRunnerConfig() config.RunnerConfig {
	applicationRunnerConfigMu.RLock()
	defer applicationRunnerConfigMu.RUnlock()
	return applicationRunnerConfig
}
