package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
	"github.com/1136623363/watermark-go/internal/utils"
)

type Service struct {
	registry     *coreparser.Registry
	dependencies coreparser.Dependencies
}

type SourceInfo struct {
	Key         string
	DisplayName string
	HostRules   []coreparser.HostRule
	// Domains is retained as a display-only compatibility projection. Routing
	// callers must use HostRules or SourceForURL so IncludeSubdomains is not lost.
	Domains  []string
	URLParse bool
	IDParse  bool
}

func CatalogSnapshot() (coreparser.Catalog, error) {
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		return coreparser.Catalog{}, err
	}
	return registry.CatalogSnapshot(), nil
}

func CatalogSources() []SourceInfo {
	catalog, err := CatalogSnapshot()
	if err != nil {
		return nil
	}
	return sourcesFromCatalog(catalog)
}

func CatalogSource(source string) (SourceInfo, bool) {
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		return SourceInfo{}, false
	}
	descriptor, ok := registry.Descriptor(coreparser.PlatformKey(strings.ToLower(strings.TrimSpace(source))))
	if !ok {
		return SourceInfo{}, false
	}
	return sourceFromDescriptor(descriptor), true
}

func CatalogSourceForURL(raw string) (string, error) {
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		return "", err
	}
	descriptor, err := registry.ResolveURL(raw)
	if err != nil {
		return "", err
	}
	return string(descriptor.Key), nil
}

func NewService(dependencies coreparser.Dependencies) (*Service, error) {
	if dependencies.Fetcher == nil {
		return nil, errors.New("native parser requires a guarded fetcher")
	}
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		return nil, fmt.Errorf("build native parser registry: %w", err)
	}
	if dependencies.Clock == nil {
		dependencies.Clock = time.Now
	}
	return &Service{registry: registry, dependencies: dependencies}, nil
}

func (service *Service) Catalog() coreparser.Catalog {
	if service == nil || service.registry == nil {
		return coreparser.Catalog{}
	}
	return service.registry.CatalogSnapshot()
}

func (service *Service) Sources() []SourceInfo {
	return sourcesFromCatalog(service.Catalog())
}

func sourcesFromCatalog(catalog coreparser.Catalog) []SourceInfo {
	result := make([]SourceInfo, 0, len(catalog.Platforms))
	for _, platform := range catalog.Platforms {
		domains := make([]string, 0, len(platform.HostRules))
		for _, rule := range platform.HostRules {
			domains = append(domains, rule.Host)
		}
		result = append(result, SourceInfo{
			Key: string(platform.Key), DisplayName: platform.DisplayName,
			HostRules: append([]coreparser.HostRule(nil), platform.HostRules...),
			Domains:   domains, URLParse: true, IDParse: platform.SupportsID,
		})
	}
	return result
}

func (service *Service) Source(source string) (SourceInfo, bool) {
	if service == nil || service.registry == nil {
		return SourceInfo{}, false
	}
	descriptor, ok := service.registry.Descriptor(coreparser.PlatformKey(strings.ToLower(strings.TrimSpace(source))))
	if !ok {
		return SourceInfo{}, false
	}
	return sourceFromDescriptor(descriptor), true
}

func sourceFromDescriptor(descriptor coreparser.Descriptor) SourceInfo {
	domains := make([]string, 0, len(descriptor.HostRules))
	for _, rule := range descriptor.HostRules {
		domains = append(domains, rule.Host)
	}
	return SourceInfo{
		Key: string(descriptor.Key), DisplayName: descriptor.DisplayName,
		HostRules: append([]coreparser.HostRule(nil), descriptor.HostRules...),
		Domains:   domains, URLParse: true, IDParse: descriptor.SupportsID,
	}
}

func (service *Service) SourceForURL(raw string) (string, error) {
	if service == nil || service.registry == nil {
		return "", errors.New("native parser service is unavailable")
	}
	descriptor, err := service.registry.ResolveURL(raw)
	if err != nil {
		return "", err
	}
	return string(descriptor.Key), nil
}

func (service *Service) ParseVideoShareURLByRegexp(ctx context.Context, shareMessage string) (*VideoParseInfo, error) {
	shareURL, err := utils.RegexpMatchUrlFromString(shareMessage)
	if err != nil {
		return nil, err
	}
	return service.ParseVideoShareURL(ctx, shareURL)
}

func (service *Service) ParseVideoShareURL(ctx context.Context, shareURL string) (*VideoParseInfo, error) {
	if service == nil || service.registry == nil {
		return nil, errors.New("native parser service is unavailable")
	}
	descriptor, err := service.registry.ResolveURL(strings.TrimSpace(shareURL))
	if err != nil {
		return nil, err
	}
	adapter, err := descriptor.New(service.dependencies)
	if err != nil {
		return nil, err
	}
	normalizedURL, err := coreparser.NormalizeFetchURL(descriptor, shareURL)
	if err != nil {
		return nil, err
	}
	result, err := adapter.Parse(ctx, coreparser.Request{URL: normalizedURL, Platform: descriptor.Key})
	if err != nil {
		return nil, err
	}
	return resultToLegacy(result), nil
}

func (service *Service) ParseVideoID(ctx context.Context, source, videoID string) (*VideoParseInfo, error) {
	if strings.TrimSpace(videoID) == "" || strings.TrimSpace(source) == "" {
		return nil, errors.New("video id or source is empty")
	}
	descriptor, ok := service.registry.Descriptor(coreparser.PlatformKey(strings.ToLower(strings.TrimSpace(source))))
	if !ok {
		return nil, fmt.Errorf("unknown parser source %q", source)
	}
	if !descriptor.SupportsID {
		return nil, fmt.Errorf("source %s has no video id parser", descriptor.Key)
	}
	adapter, err := descriptor.New(service.dependencies)
	if err != nil {
		return nil, err
	}
	result, err := adapter.Parse(ctx, coreparser.Request{ID: videoID, Platform: descriptor.Key})
	if err != nil {
		return nil, err
	}
	return resultToLegacy(result), nil
}

func (service *Service) BatchParseVideoID(ctx context.Context, source string, videoIDs []string) (map[string]BatchParseItem, error) {
	if len(videoIDs) == 0 || strings.TrimSpace(source) == "" {
		return nil, errors.New("videos id or source is empty")
	}
	if sourceInfo, ok := service.Source(source); !ok || !sourceInfo.IDParse {
		return nil, fmt.Errorf("source %s has no video id parser", source)
	}
	var wait sync.WaitGroup
	var mutex sync.Mutex
	parsed := make(map[string]BatchParseItem, len(videoIDs))
	for _, current := range videoIDs {
		videoID := current
		wait.Add(1)
		go func() {
			defer wait.Done()
			info, parseErr := service.ParseVideoID(ctx, source, videoID)
			mutex.Lock()
			parsed[videoID] = BatchParseItem{ParseInfo: info, Error: parseErr}
			mutex.Unlock()
		}()
	}
	wait.Wait()
	return parsed, nil
}

// sourceForShareURL remains package-local for the fixed-commit extraction
// regression tests. It resolves against the same descriptor registry used by
// production, never against a second host table.
func sourceForShareURL(shareURL string) string {
	registry, err := coreparser.NewRegistry(Descriptors())
	if err != nil {
		return ""
	}
	descriptor, err := registry.ResolveURL(shareURL)
	if err != nil {
		return ""
	}
	return string(descriptor.Key)
}
