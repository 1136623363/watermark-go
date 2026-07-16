package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type ErrorCode string

const (
	ErrorSessionExpired     ErrorCode = "session_expired"
	ErrorSchemaChanged      ErrorCode = "schema_changed"
	ErrorCredentialRequired ErrorCode = "credential_required"
	ErrorSecurityRejected   ErrorCode = "security_rejected"
	ErrorUpstreamFailed     ErrorCode = "upstream_failed"
)

type ParseError struct {
	Code  ErrorCode
	cause error
}

func NewParseError(code ErrorCode, cause error) *ParseError {
	return &ParseError{Code: code, cause: cause}
}

func (err *ParseError) Error() string {
	if err == nil {
		return "parser error"
	}
	return "parser error: " + string(err.Code)
}

// Format prevents non-string fmt verbs from recursively inspecting the
// private cause. Flags, width and precision are intentionally ignored.
func (err ParseError) Format(state fmt.State, _ rune) {
	label := "parser error"
	if err.Code != "" {
		label = "parser error: " + string(err.Code)
	}
	_, _ = state.Write([]byte(label))
}

func (err *ParseError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

type SensitiveMaterial struct {
	value      string
	generation uint64
}

func NewSensitiveMaterial(value string) SensitiveMaterial { return SensitiveMaterial{value: value} }

func (material SensitiveMaterial) Configured() bool { return material.value != "" }

func (material SensitiveMaterial) Use(consumer func(string) error) error {
	if consumer == nil {
		return errors.New("sensitive material consumer is required")
	}
	return consumer(material.value)
}

func (material SensitiveMaterial) String() string {
	if material.Configured() {
		return "[configured]"
	}
	return "[not-configured]"
}

func (material SensitiveMaterial) GoString() string {
	return "parser.SensitiveMaterial(" + material.String() + ")"
}

// Format keeps every fmt verb opaque; String and GoString alone do not cover
// numeric, pointer or invalid verbs, which otherwise reveal the private value.
func (material SensitiveMaterial) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(material.String()))
}

func (material SensitiveMaterial) MarshalJSON() ([]byte, error) {
	return nil, errors.New("sensitive material cannot be serialized")
}

func (material SensitiveMaterial) MarshalText() ([]byte, error) {
	return nil, errors.New("sensitive material cannot be serialized")
}

type SessionMaterialKey struct {
	Platform PlatformKey
	Host     string
}

type SessionMaterialOptions struct {
	TTL      time.Duration
	Capacity int
	Clock    func() time.Time
}

type sessionEntry struct {
	material   SensitiveMaterial
	generation uint64
	expires    time.Time
	used       uint64
	loading    bool
	ready      chan struct{}
	err        error
}

type SessionMaterialProvider struct {
	mu         sync.Mutex
	ttl        time.Duration
	capacity   int
	clock      func() time.Time
	sequence   uint64
	generation uint64
	entries    map[SessionMaterialKey]*sessionEntry
}

type SessionRefreshGuard struct{ used atomic.Bool }

var ErrSessionCapacity = errors.New("session material capacity reached")

func NewSessionMaterialProvider(options SessionMaterialOptions) (*SessionMaterialProvider, error) {
	if options.TTL <= 0 || options.Capacity <= 0 {
		return nil, errors.New("invalid session material options")
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &SessionMaterialProvider{
		ttl: options.TTL, capacity: options.Capacity, clock: clock,
		entries: make(map[SessionMaterialKey]*sessionEntry),
	}, nil
}

func (provider *SessionMaterialProvider) Get(ctx context.Context, key SessionMaterialKey, load func(context.Context) (SensitiveMaterial, error)) (SensitiveMaterial, error) {
	if provider == nil || load == nil || ctx == nil {
		return SensitiveMaterial{}, errors.New("invalid session material request")
	}
	normalizedHost, normalizeErr := normalizeRegistryHost(key.Host)
	if normalizeErr != nil || !validPlatformKey(key.Platform) {
		return SensitiveMaterial{}, errors.New("invalid session material scope")
	}
	key.Host = normalizedHost
	now := provider.clock()
	provider.mu.Lock()
	if entry, exists := provider.entries[key]; exists {
		if entry.loading {
			ready := entry.ready
			provider.mu.Unlock()
			select {
			case <-ctx.Done():
				return SensitiveMaterial{}, ctx.Err()
			case <-ready:
				return entry.material, entry.err
			}
		}
		if now.Before(entry.expires) {
			provider.sequence++
			entry.used = provider.sequence
			material := entry.material
			provider.mu.Unlock()
			return material, nil
		}
		delete(provider.entries, key)
	}
	if len(provider.entries) >= provider.capacity && !provider.evictOldestLocked() {
		provider.mu.Unlock()
		return SensitiveMaterial{}, ErrSessionCapacity
	}
	provider.sequence++
	entry := &sessionEntry{loading: true, ready: make(chan struct{}), used: provider.sequence}
	provider.entries[key] = entry
	provider.mu.Unlock()

	material, err := load(ctx)
	provider.mu.Lock()
	entry.loading = false
	entry.err = err
	if err == nil {
		provider.generation++
		if provider.generation == 0 {
			provider.generation++
		}
		material.generation = provider.generation
		entry.generation = material.generation
		entry.expires = provider.clock().Add(provider.ttl)
	} else {
		delete(provider.entries, key)
	}
	entry.material = material
	close(entry.ready)
	provider.mu.Unlock()
	return material, err
}

func (provider *SessionMaterialProvider) InvalidateFor(key SessionMaterialKey, material SensitiveMaterial, parseErr error) bool {
	if provider == nil {
		return false
	}
	var typed *ParseError
	if !errors.As(parseErr, &typed) || typed == nil || typed.Code != ErrorSessionExpired || material.generation == 0 {
		return false
	}
	normalizedHost, err := normalizeRegistryHost(key.Host)
	if err != nil || !validPlatformKey(key.Platform) {
		return false
	}
	key.Host = normalizedHost
	provider.mu.Lock()
	defer provider.mu.Unlock()
	entry, exists := provider.entries[key]
	if !exists || entry.loading || entry.generation != material.generation {
		return false
	}
	delete(provider.entries, key)
	return true
}

func (provider *SessionMaterialProvider) RefreshOnce(
	ctx context.Context,
	key SessionMaterialKey,
	material SensitiveMaterial,
	parseErr error,
	guard *SessionRefreshGuard,
	load func(context.Context) (SensitiveMaterial, error),
) (SensitiveMaterial, bool, error) {
	var typed *ParseError
	if provider == nil || guard == nil || !errors.As(parseErr, &typed) || typed == nil || typed.Code != ErrorSessionExpired {
		return SensitiveMaterial{}, false, parseErr
	}
	if !guard.used.CompareAndSwap(false, true) {
		return SensitiveMaterial{}, false, parseErr
	}
	provider.InvalidateFor(key, material, parseErr)
	refreshedMaterial, err := provider.Get(ctx, key, load)
	return refreshedMaterial, true, err
}

func (provider *SessionMaterialProvider) evictOldestLocked() bool {
	var oldestKey SessionMaterialKey
	var oldest *sessionEntry
	for key, entry := range provider.entries {
		if entry.loading {
			continue
		}
		if oldest == nil || entry.used < oldest.used {
			oldestKey, oldest = key, entry
		}
	}
	if oldest == nil {
		return false
	}
	delete(provider.entries, oldestKey)
	return true
}

var _ json.Marshaler = SensitiveMaterial{}
