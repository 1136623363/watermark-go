package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sharedcache "github.com/1136623363/watermark-go/internal/cache"
	parseusecase "github.com/1136623363/watermark-go/internal/parse"
)

const runtimeParseCacheSchema = "runtime-parse-cache/v1"

type runtimeParseCache struct {
	store *sharedcache.Tiered
}

type runtimeParseCacheRecord struct {
	SchemaVersion string                  `json:"schemaVersion"`
	Result        parseusecase.Result     `json:"result,omitempty"`
	Error         *runtimeParseCacheError `json:"error,omitempty"`
}

type runtimeParseCacheError struct {
	Class     parseusecase.ErrorClass `json:"class"`
	Stage     parseusecase.Stage      `json:"stage"`
	Platform  string                  `json:"platform"`
	Retryable bool                    `json:"retryable"`
}

func (cache *runtimeParseCache) GetPositive(ctx context.Context, identity parseusecase.CacheIdentity) (parseusecase.Result, bool, error) {
	if cache == nil || cache.store == nil {
		return parseusecase.Result{}, false, nil
	}
	key, err := runtimeCacheKey(identity, "positive")
	if err != nil {
		return parseusecase.Result{}, false, err
	}
	raw, ok, err := cache.store.Get(ctx, key)
	if err != nil || !ok {
		return parseusecase.Result{}, ok, err
	}
	var record runtimeParseCacheRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		_ = cache.store.Delete(ctx, key)
		return parseusecase.Result{}, false, nil
	}
	if record.SchemaVersion != runtimeParseCacheSchema || record.Error != nil {
		return parseusecase.Result{}, false, nil
	}
	return record.Result, true, nil
}

func (cache *runtimeParseCache) SetPositive(ctx context.Context, identity parseusecase.CacheIdentity, result parseusecase.Result, ttl time.Duration) error {
	if cache == nil || cache.store == nil {
		return nil
	}
	key, err := runtimeCacheKey(identity, "positive")
	if err != nil {
		return err
	}
	body, err := json.Marshal(runtimeParseCacheRecord{
		SchemaVersion: runtimeParseCacheSchema,
		Result:        result,
	})
	if err != nil {
		return err
	}
	return cache.store.Set(ctx, key, body, ttl)
}

func (cache *runtimeParseCache) GetNegative(ctx context.Context, identity parseusecase.CacheIdentity) (error, bool, error) {
	if cache == nil || cache.store == nil {
		return nil, false, nil
	}
	key, err := runtimeCacheKey(identity, "negative")
	if err != nil {
		return nil, false, err
	}
	raw, ok, err := cache.store.Get(ctx, key)
	if err != nil || !ok {
		return nil, ok, err
	}
	var record runtimeParseCacheRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		_ = cache.store.Delete(ctx, key)
		return nil, false, nil
	}
	if record.SchemaVersion != runtimeParseCacheSchema || record.Error == nil || record.Error.Class == "" {
		return nil, false, nil
	}
	return parseusecase.NewError(record.Error.Class, record.Error.Stage, record.Error.Platform, record.Error.Retryable), true, nil
}

func (cache *runtimeParseCache) SetNegative(ctx context.Context, identity parseusecase.CacheIdentity, err error, ttl time.Duration) error {
	if cache == nil || cache.store == nil || err == nil {
		return nil
	}
	key, keyErr := runtimeCacheKey(identity, "negative")
	if keyErr != nil {
		return keyErr
	}
	record := runtimeParseCacheRecord{
		SchemaVersion: runtimeParseCacheSchema,
		Error: &runtimeParseCacheError{
			Class: parseusecase.ClassOf(err),
		},
	}
	var typed *parseusecase.Error
	if errors.As(err, &typed) && typed != nil {
		record.Error.Stage = typed.Stage
		record.Error.Platform = typed.Platform
		record.Error.Retryable = typed.Retryable
	}
	body, marshalErr := json.Marshal(record)
	if marshalErr != nil {
		return marshalErr
	}
	return cache.store.Set(ctx, key, body, ttl)
}

func runtimeCacheKey(identity parseusecase.CacheIdentity, polarity string) (sharedcache.Key, error) {
	return sharedcache.NewKey(sharedcache.KeyParts{
		Platform:            identity.Platform,
		CanonicalResourceID: identity.CanonicalURL,
		ParserVersion:       identity.ParserVersion,
		ResultSchemaVersion: strings.TrimSpace(identity.ResultSchemaVersion) + ":" + polarity,
	})
}

func firstNonEmptyRuntime(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
