package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

type KeyParts struct {
	Platform            string
	CanonicalResourceID string
	ParserVersion       string
	ResultSchemaVersion string
}

type Key struct {
	value string
}

func NewKey(parts KeyParts) (Key, error) {
	for _, value := range []string{parts.Platform, parts.CanonicalResourceID, parts.ParserVersion, parts.ResultSchemaVersion} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
			return Key{}, errors.New("cache key identity is incomplete")
		}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.ToLower(strings.TrimSpace(parts.Platform)),
		parts.CanonicalResourceID,
		parts.ParserVersion,
		parts.ResultSchemaVersion,
	}, "\x00")))
	return Key{value: "parse:v1:" + hex.EncodeToString(sum[:])}, nil
}

func (key Key) String() string { return key.value }

func (key Key) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(key.value))
}

type Store interface {
	Get(context.Context, Key) ([]byte, bool, error)
	Set(context.Context, Key, []byte, time.Duration) error
	Delete(context.Context, Key) error
}

type Loader func(context.Context) ([]byte, error)

type Tiered struct {
	primary  Store
	fallback Store
	group    singleflight.Group
}

func NewTiered(primary Store, fallback Store) *Tiered {
	return &Tiered{primary: primary, fallback: fallback}
}

func (cache *Tiered) Get(ctx context.Context, key Key) ([]byte, bool, error) {
	if cache == nil {
		return nil, false, errors.New("nil cache")
	}
	if cache.primary != nil {
		value, ok, err := cache.primary.Get(ctx, key)
		if err == nil && ok {
			return value, true, nil
		}
	}
	if cache.fallback == nil {
		return nil, false, nil
	}
	return cache.fallback.Get(ctx, key)
}

func (cache *Tiered) Set(ctx context.Context, key Key, value []byte, ttl time.Duration) error {
	if cache == nil {
		return errors.New("nil cache")
	}
	var result error
	if cache.primary != nil {
		if err := cache.primary.Set(ctx, key, value, ttl); err != nil {
			result = err
		}
	}
	if cache.fallback != nil {
		if err := cache.fallback.Set(ctx, key, value, ttl); err != nil && result == nil {
			result = err
		}
	}
	if cache.fallback != nil {
		return nil
	}
	return result
}

func (cache *Tiered) Delete(ctx context.Context, key Key) error {
	if cache == nil {
		return errors.New("nil cache")
	}
	var result error
	if cache.primary != nil {
		result = cache.primary.Delete(ctx, key)
	}
	if cache.fallback != nil {
		if err := cache.fallback.Delete(ctx, key); err != nil && result == nil {
			result = err
		}
	}
	return result
}

func (cache *Tiered) Do(ctx context.Context, key Key, ttl time.Duration, force bool, loader Loader) ([]byte, error) {
	if cache == nil || loader == nil {
		return nil, errors.New("cache loader is required")
	}
	if !force {
		if value, ok, err := cache.Get(ctx, key); err == nil && ok {
			return value, nil
		}
	}
	result, err, _ := cache.group.Do(key.String(), func() (any, error) {
		if !force {
			if value, ok, err := cache.Get(ctx, key); err == nil && ok {
				return value, nil
			}
		}
		value, err := loader(ctx)
		if err != nil {
			return nil, err
		}
		if err := cache.Set(ctx, key, value, ttl); err != nil {
			return nil, err
		}
		return append([]byte(nil), value...), nil
	})
	if err != nil {
		return nil, err
	}
	value, _ := result.([]byte)
	return append([]byte(nil), value...), nil
}

type ErrorClass string

const (
	ErrorStableFailure      ErrorClass = "stable_failure"
	ErrorContextCanceled    ErrorClass = "context_canceled"
	ErrorInternal           ErrorClass = "internal"
	ErrorCredentialRequired ErrorClass = "credential_required"
	ErrorSchemaChanged      ErrorClass = "schema_changed"
	ErrorSecurityRejected   ErrorClass = "security_rejected"
	ErrorSessionExpired     ErrorClass = "session_expired"
)

func NegativeCacheable(class ErrorClass) bool {
	return class == ErrorStableFailure
}

func NegativeTTL(class ErrorClass) time.Duration {
	if NegativeCacheable(class) {
		return 180 * time.Second
	}
	return 0
}
