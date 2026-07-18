package parse

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	sharedcache "github.com/1136623363/watermark-go/internal/cache"
	"github.com/1136623363/watermark-go/internal/netguard"
	"golang.org/x/sync/singleflight"
)

const (
	defaultParserVersion       = "parser-v1"
	defaultResultSchemaVersion = "result-schema-v1"
	defaultPositiveTTL         = 6 * time.Hour
	defaultNegativeTTL         = 180 * time.Second
	shareIDEntropyBytes        = 16
)

var ErrEntropyUnavailable = errors.New("parse share id entropy unavailable")

type ErrorClass string

const (
	ErrorInvalidInput       ErrorClass = "invalid_input"
	ErrorUnsupported        ErrorClass = "unsupported"
	ErrorCredentialRequired ErrorClass = "credential_required"
	ErrorUpstreamTimeout    ErrorClass = "upstream_timeout"
	ErrorUpstreamBlocked    ErrorClass = "upstream_blocked"
	ErrorEmptyMedia         ErrorClass = "empty_media"
	ErrorSchemaChanged      ErrorClass = "schema_changed"
	ErrorInternal           ErrorClass = "internal"
	ErrorCanceled           ErrorClass = "canceled"
	ErrorSecurityRejected   ErrorClass = "security_rejected"
)

type Stage string

const (
	StageInput     Stage = "input"
	StageCache     Stage = "cache"
	StageParser    Stage = "parser"
	StageUpstream  Stage = "upstream"
	StageStore     Stage = "store"
	StageNormalize Stage = "normalize"
)

type Error struct {
	Class     ErrorClass
	Stage     Stage
	Platform  string
	Retryable bool
}

func NewError(class ErrorClass, stage Stage, platform string, retryable bool) *Error {
	if class == "" {
		class = ErrorInternal
	}
	if stage == "" {
		stage = StageParser
	}
	return &Error{Class: class, Stage: stage, Platform: strings.TrimSpace(platform), Retryable: retryable}
}

func (err *Error) Error() string {
	if err == nil {
		return "parse failed"
	}
	return "parse failed: " + string(err.Class)
}

func (err Error) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(err.Error()))
}

func ClassOf(err error) ErrorClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorUpstreamTimeout
	}
	if errors.Is(err, ErrEntropyUnavailable) {
		return ErrorInternal
	}
	var typed *Error
	if errors.As(err, &typed) && typed != nil {
		return typed.Class
	}
	return ErrorInternal
}

func NegativeCacheable(class ErrorClass) bool {
	return class == ErrorUnsupported || class == ErrorEmptyMedia
}

type Parser interface {
	Parse(context.Context, ParserRequest) (Result, error)
}

type IDParser interface {
	ParseID(context.Context, IDParserRequest) (Result, error)
}

type ParserChain struct {
	Parsers     []Parser
	MaxAttempts int
}

func (chain ParserChain) Parse(ctx context.Context, request ParserRequest) (Result, error) {
	limit := chain.MaxAttempts
	if limit <= 0 || limit > len(chain.Parsers) {
		limit = len(chain.Parsers)
	}
	var lastErr error
	for index := 0; index < limit; index++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		parser := chain.Parsers[index]
		if parser == nil {
			continue
		}
		result, err := parser.Parse(ctx, request)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return Result{}, lastErr
	}
	return Result{}, NewError(ErrorInternal, StageParser, request.Descriptor.Platform, true)
}

type Resolver interface {
	ResolveURL(string) (Descriptor, error)
}

type Cache interface {
	GetPositive(context.Context, CacheIdentity) (Result, bool, error)
	SetPositive(context.Context, CacheIdentity, Result, time.Duration) error
	GetNegative(context.Context, CacheIdentity) (error, bool, error)
	SetNegative(context.Context, CacheIdentity, error, time.Duration) error
}

type Store interface {
	SaveResult(context.Context, StoredResult) error
}

type CachedReader interface {
	GetCached(context.Context, string) (CompatData, bool, error)
}

type Dependencies struct {
	Parser              Parser
	IDParser            IDParser
	Resolver            Resolver
	Cache               Cache
	Store               Store
	Entropy             io.Reader
	Clock               func() time.Time
	ParserVersion       string
	ResultSchemaVersion string
	PositiveTTL         time.Duration
	NegativeTTL         time.Duration
}

type Service struct {
	parser              Parser
	idParser            IDParser
	resolver            Resolver
	cache               Cache
	store               Store
	entropy             io.Reader
	clock               func() time.Time
	parserVersion       string
	resultSchemaVersion string
	positiveTTL         time.Duration
	negativeTTL         time.Duration
	group               singleflight.Group
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = time.Now
	}
	parserVersion := strings.TrimSpace(dependencies.ParserVersion)
	if parserVersion == "" {
		parserVersion = defaultParserVersion
	}
	resultSchemaVersion := strings.TrimSpace(dependencies.ResultSchemaVersion)
	if resultSchemaVersion == "" {
		resultSchemaVersion = defaultResultSchemaVersion
	}
	positiveTTL := dependencies.PositiveTTL
	if positiveTTL <= 0 {
		positiveTTL = defaultPositiveTTL
	}
	negativeTTL := dependencies.NegativeTTL
	if negativeTTL <= 0 {
		negativeTTL = defaultNegativeTTL
	}
	return &Service{
		parser:              dependencies.Parser,
		idParser:            dependencies.IDParser,
		resolver:            dependencies.Resolver,
		cache:               dependencies.Cache,
		store:               dependencies.Store,
		entropy:             dependencies.Entropy,
		clock:               clock,
		parserVersion:       parserVersion,
		resultSchemaVersion: resultSchemaVersion,
		positiveTTL:         positiveTTL,
		negativeTTL:         negativeTTL,
	}
}

func (service *Service) Parse(ctx context.Context, request Request) (ParseOutput, error) {
	if service == nil || service.parser == nil || service.resolver == nil {
		return ParseOutput{}, NewError(ErrorInternal, StageParser, "", true)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rawURL, err := ExtractURL(request.URL)
	if err != nil {
		return ParseOutput{}, err
	}
	descriptor, err := service.resolver.ResolveURL(rawURL)
	if err != nil {
		return ParseOutput{}, NewError(ErrorUnsupported, StageInput, "", false)
	}
	canonical, err := CanonicalizeURL(rawURL, descriptor)
	if err != nil {
		return ParseOutput{}, err
	}
	identity, err := NewCacheIdentity(CacheIdentityParts{
		Platform:             firstNonEmpty(descriptor.Platform, canonical.Platform, "unknown"),
		CanonicalResourceURL: canonical.URL,
		ParserVersion:        service.parserVersion,
		ResultSchemaVersion:  service.resultSchemaVersion,
	})
	if err != nil {
		return ParseOutput{}, NewError(ErrorInternal, StageCache, descriptor.Platform, true)
	}
	if !request.ForceRefresh && service.cache != nil {
		if result, ok, err := service.cache.GetPositive(ctx, identity); err == nil && ok {
			return service.outputFromResult(ctx, identity, result, false)
		}
		if cachedErr, ok, err := service.cache.GetNegative(ctx, identity); err == nil && ok {
			return ParseOutput{}, cachedErr
		}
	}
	value, err, _ := service.group.Do(identity.Key, func() (any, error) {
		if !request.ForceRefresh && service.cache != nil {
			if result, ok, err := service.cache.GetPositive(ctx, identity); err == nil && ok {
				return service.outputFromResult(ctx, identity, result, false)
			}
		}
		result, err := service.parser.Parse(ctx, ParserRequest{
			RawURL:     rawURL,
			Canonical:  canonical,
			Descriptor: descriptor,
		})
		if err != nil {
			parseErr := classifyParseError(err, descriptor.Platform)
			if !request.ForceRefresh && service.cache != nil && NegativeCacheable(ClassOf(parseErr)) {
				_ = service.cache.SetNegative(ctx, identity, parseErr, service.negativeTTL)
			}
			return nil, parseErr
		}
		if strings.TrimSpace(result.Platform) == "" {
			result.Platform = descriptor.Platform
		}
		if err := ValidateMedia(result); err != nil {
			return nil, err
		}
		return service.outputFromResult(ctx, identity, result, true)
	})
	if err != nil {
		return ParseOutput{}, err
	}
	output, _ := value.(ParseOutput)
	return output, nil
}

func (service *Service) ParseID(ctx context.Context, request IDRequest) (ParseOutput, error) {
	if service == nil || service.idParser == nil {
		return ParseOutput{}, NewError(ErrorUnsupported, StageInput, "", false)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.Source = strings.ToLower(strings.TrimSpace(request.Source))
	request.VideoID = strings.TrimSpace(request.VideoID)
	if request.Source == "" || request.VideoID == "" {
		return ParseOutput{}, NewError(ErrorInvalidInput, StageInput, request.Source, false)
	}
	descriptor := Descriptor{Platform: request.Source}
	result, err := service.idParser.ParseID(ctx, IDParserRequest{
		Source:     request.Source,
		VideoID:    request.VideoID,
		Descriptor: descriptor,
	})
	if err != nil {
		return ParseOutput{}, classifyParseError(err, request.Source)
	}
	if strings.TrimSpace(result.Platform) == "" {
		result.Platform = request.Source
	}
	if err := ValidateMedia(result); err != nil {
		return ParseOutput{}, err
	}
	identity, err := NewCacheIdentity(CacheIdentityParts{
		Platform:             request.Source,
		CanonicalResourceURL: "id/" + request.Source + "/" + request.VideoID,
		ParserVersion:        service.parserVersion,
		ResultSchemaVersion:  service.resultSchemaVersion,
	})
	if err != nil {
		return ParseOutput{}, NewError(ErrorInternal, StageCache, request.Source, true)
	}
	return service.outputFromResult(ctx, identity, result, true)
}

func (service *Service) outputFromResult(ctx context.Context, identity CacheIdentity, result Result, persist bool) (ParseOutput, error) {
	data := Normalize(result)
	data.SourceURL = identity.CanonicalURL
	output := ParseOutput{Result: result, Data: data, Cache: identity}
	if !persist {
		return output, nil
	}
	shareID, err := GenerateShareID(service.entropy)
	if err != nil {
		return ParseOutput{}, err
	}
	output.Data.ShareID = shareID
	if service.store != nil {
		if err := service.store.SaveResult(ctx, StoredResult{
			ShareID:   shareID,
			Cache:     identity,
			Result:    result,
			Data:      output.Data,
			CreatedAt: service.clock(),
		}); err != nil {
			return ParseOutput{}, NewError(ErrorInternal, StageStore, result.Platform, true)
		}
	}
	if service.cache != nil {
		_ = service.cache.SetPositive(ctx, identity, result, service.positiveTTL)
	}
	return output, nil
}

func classifyParseError(err error, platform string) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	if errors.Is(err, context.Canceled) {
		return NewError(ErrorCanceled, StageParser, platform, true)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewError(ErrorUpstreamTimeout, StageUpstream, platform, true)
	}
	return NewError(ErrorUpstreamBlocked, StageUpstream, platform, true)
}

func GenerateShareID(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	raw := make([]byte, shareIDEntropyBytes)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", ErrEntropyUnavailable
	}
	return "share_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func ValidateMedia(result Result) error {
	platform := strings.TrimSpace(result.Platform)
	urls := make([]string, 0, 3+len(result.Images)*2)
	for _, raw := range []string{result.VideoURL, result.AudioURL, result.M3U8URL, result.PreviewURL, result.CoverURL} {
		if strings.TrimSpace(raw) != "" {
			urls = append(urls, raw)
		}
	}
	if len(result.Images) > 100 {
		return NewError(ErrorSecurityRejected, StageNormalize, platform, false)
	}
	for _, image := range result.Images {
		if strings.TrimSpace(image.URL) != "" {
			urls = append(urls, image.URL)
		}
		if strings.TrimSpace(image.LivePhotoURL) != "" {
			urls = append(urls, image.LivePhotoURL)
		}
	}
	if len(urls) == 0 {
		return NewError(ErrorEmptyMedia, StageNormalize, platform, false)
	}
	for _, raw := range urls {
		if _, err := netguard.NewFetchURL(strings.TrimSpace(raw)); err != nil {
			return NewError(ErrorSecurityRejected, StageNormalize, platform, false)
		}
	}
	return nil
}

type CacheIdentityParts struct {
	Platform             string
	CanonicalResourceURL string
	ParserVersion        string
	ResultSchemaVersion  string
}

type CacheIdentity struct {
	Platform            string
	CanonicalURL        string
	ParserVersion       string
	ResultSchemaVersion string
	Key                 string
}

func NewCacheIdentity(parts CacheIdentityParts) (CacheIdentity, error) {
	key, err := sharedcache.NewKey(sharedcache.KeyParts{
		Platform:            parts.Platform,
		CanonicalResourceID: parts.CanonicalResourceURL,
		ParserVersion:       parts.ParserVersion,
		ResultSchemaVersion: parts.ResultSchemaVersion,
	})
	if err != nil {
		return CacheIdentity{}, err
	}
	return CacheIdentity{
		Platform:            strings.TrimSpace(parts.Platform),
		CanonicalURL:        strings.TrimSpace(parts.CanonicalResourceURL),
		ParserVersion:       strings.TrimSpace(parts.ParserVersion),
		ResultSchemaVersion: strings.TrimSpace(parts.ResultSchemaVersion),
		Key:                 key.String(),
	}, nil
}
