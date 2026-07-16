package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopedSessionMaterialInvalidatesOnlyOnTypedExpiry(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	provider, err := NewSessionMaterialProvider(SessionMaterialOptions{
		TTL:      time.Minute,
		Capacity: 4,
		Clock:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionMaterialKey{Platform: "weibo", Host: "weibo.com"}
	var loads atomic.Int32
	load := func(context.Context) (SensitiveMaterial, error) {
		loads.Add(1)
		return NewSensitiveMaterial("opaque-session"), nil
	}
	material, err := provider.Get(t.Context(), key, load)
	if err != nil {
		t.Fatal(err)
	}

	for _, parseErr := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		NewParseError(ErrorSchemaChanged, errors.New("shape changed")),
		NewParseError(ErrorCredentialRequired, errors.New("credential absent")),
		NewParseError(ErrorSecurityRejected, errors.New("destination denied")),
		NewParseError(ErrorUpstreamFailed, errors.New("upstream failed")),
		errors.New("internal"),
	} {
		if provider.InvalidateFor(key, material, parseErr) {
			t.Fatalf("invalidated for non-expiry error %v", parseErr)
		}
		material, err = provider.Get(t.Context(), key, load)
		if err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("non-expiry errors caused %d loads", loads.Load())
	}

	if !provider.InvalidateFor(key, material, NewParseError(ErrorSessionExpired, errors.New("expired"))) {
		t.Fatal("typed session expiry did not invalidate")
	}
	material, err = provider.Get(t.Context(), key, load)
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 2 {
		t.Fatalf("expiry load count = %d", loads.Load())
	}

	guard := &SessionRefreshGuard{}
	expired := NewParseError(ErrorSessionExpired, errors.New("expired again"))
	if _, refreshed, err := provider.RefreshOnce(t.Context(), key, material, expired, guard, load); err != nil || !refreshed {
		t.Fatalf("first guarded refresh = refreshed:%t error:%v", refreshed, err)
	}
	if _, refreshed, err := provider.RefreshOnce(t.Context(), key, material, expired, guard, load); !errors.Is(err, expired) || refreshed {
		t.Fatalf("second guarded refresh escaped one-refresh limit: refreshed:%t error:%v", refreshed, err)
	}
	if loads.Load() != 3 {
		t.Fatalf("guarded expiry load count = %d", loads.Load())
	}
}

func TestStaleSessionExpiryReusesCompletedConcurrentRefreshWithoutReloading(t *testing.T) {
	t.Parallel()
	provider, err := NewSessionMaterialProvider(SessionMaterialOptions{TTL: time.Minute, Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionMaterialKey{Platform: "weibo", Host: "weibo.com"}
	var loads atomic.Int32
	load := func(context.Context) (SensitiveMaterial, error) {
		switch call := loads.Add(1); call {
		case 1:
			return NewSensitiveMaterial("stale-session"), nil
		case 2:
			return NewSensitiveMaterial("refreshed-session"), nil
		default:
			return NewSensitiveMaterial("unexpected-second-refresh"), nil
		}
	}

	// These two materials model requests A and B having already parsed with
	// the same cached session before either reports that it expired.
	staleA, err := provider.Get(t.Context(), key, load)
	if err != nil {
		t.Fatal(err)
	}
	staleB, err := provider.Get(t.Context(), key, load)
	if err != nil {
		t.Fatal(err)
	}
	type refreshResult struct {
		material  SensitiveMaterial
		refreshed bool
		err       error
	}
	expired := NewParseError(ErrorSessionExpired, errors.New("synthetic expiry"))
	start := make(chan struct{})
	refreshAComplete := make(chan struct{})
	resultA := make(chan refreshResult, 1)
	resultB := make(chan refreshResult, 1)

	go func() {
		<-start
		material, refreshed, refreshErr := provider.RefreshOnce(
			context.Background(), key, staleA, expired, &SessionRefreshGuard{}, load,
		)
		resultA <- refreshResult{material: material, refreshed: refreshed, err: refreshErr}
		close(refreshAComplete)
	}()
	go func() {
		<-start
		<-refreshAComplete
		material, refreshed, refreshErr := provider.RefreshOnce(
			context.Background(), key, staleB, expired, &SessionRefreshGuard{}, load,
		)
		resultB <- refreshResult{material: material, refreshed: refreshed, err: refreshErr}
	}()
	close(start)

	a := <-resultA
	b := <-resultB
	for name, result := range map[string]refreshResult{"A": a, "B": b} {
		if result.err != nil || !result.refreshed {
			t.Fatalf("refresh %s = refreshed:%t error:%v", name, result.refreshed, result.err)
		}
		var value string
		if err := result.material.Use(func(materialValue string) error {
			value = materialValue
			return nil
		}); err != nil {
			t.Fatalf("use refreshed material %s: %v", name, err)
		}
		if value != "refreshed-session" {
			t.Fatalf("refresh %s used %q, want the first completed refresh", name, value)
		}
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("session loads = %d, want initial load plus exactly one refresh", got)
	}
}

func TestSensitiveMaterialAndParseErrorCannotLeakViaFormattingOrSerialization(t *testing.T) {
	t.Parallel()
	sentinel := "session-material-must-not-cross"
	material := NewSensitiveMaterial(sentinel)
	formats := []string{
		"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%o", "%f", "%e", "%c", "%U", "%p", "%T", "%z",
		"% 120.80v", "%#+120.80q",
	}
	for _, format := range formats {
		values := map[string]any{"value": material, "pointer": &material}
		if format == "%v" || format == "%+v" || format == "%#v" {
			values["nested"] = []any{material, &material}
		}
		if format == "%p" {
			values = map[string]any{"pointer": &material}
		}
		for name, value := range values {
			if formatted := fmt.Sprintf(format, value); strings.Contains(formatted, sentinel) {
				t.Fatalf("format %q (%s) exposed material: %s", format, name, formatted)
			}
		}
	}
	if _, err := json.Marshal(material); err == nil {
		t.Fatal("sensitive material was JSON serializable")
	}
	if _, err := material.MarshalText(); err == nil {
		t.Fatal("sensitive material was text serializable")
	}

	parseErr := NewParseError(ErrorSessionExpired, errors.New(sentinel))
	for _, format := range formats {
		values := map[string]any{"value": *parseErr, "pointer": parseErr}
		if format == "%v" || format == "%+v" || format == "%#v" {
			values["nested"] = []any{parseErr, *parseErr}
		}
		if format == "%p" {
			values = map[string]any{"pointer": parseErr}
		}
		for name, value := range values {
			if formatted := fmt.Sprintf(format, value); strings.Contains(formatted, sentinel) {
				t.Fatalf("format %q (%s) exposed parser cause: %s", format, name, formatted)
			}
		}
	}
}

func TestSessionMaterialSingleflightTTLCapacityAndExactHostIsolation(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	provider, err := NewSessionMaterialProvider(SessionMaterialOptions{
		TTL:      time.Minute,
		Capacity: 2,
		Clock:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionMaterialKey{Platform: "redbook", Host: "www.xiaohongshu.com"}
	var loads atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context) (SensitiveMaterial, error) {
		if loads.Add(1) == 1 {
			close(entered)
		}
		<-release
		return NewSensitiveMaterial("session"), nil
	}

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, getErr := provider.Get(context.Background(), key, load); getErr != nil {
				t.Errorf("Get error: %v", getErr)
			}
		}()
	}
	<-entered
	close(release)
	wait.Wait()
	if loads.Load() != 1 {
		t.Fatalf("singleflight loads = %d", loads.Load())
	}

	otherHost := SessionMaterialKey{Platform: "redbook", Host: "xiaohongshu.com"}
	if _, err := provider.Get(t.Context(), otherHost, func(context.Context) (SensitiveMaterial, error) {
		loads.Add(1)
		return NewSensitiveMaterial("other"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 2 {
		t.Fatal("exact host keys were not isolated")
	}

	now = now.Add(2 * time.Minute)
	if _, err := provider.Get(t.Context(), key, func(context.Context) (SensitiveMaterial, error) {
		loads.Add(1)
		return NewSensitiveMaterial("renewed"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 3 {
		t.Fatal("expired material was not reloaded")
	}
}

func TestSessionMaterialLRUEvictionAndPlatformIsolation(t *testing.T) {
	t.Parallel()
	provider, err := NewSessionMaterialProvider(SessionMaterialOptions{TTL: time.Minute, Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	loads := map[SessionMaterialKey]int{}
	load := func(key SessionMaterialKey) func(context.Context) (SensitiveMaterial, error) {
		return func(context.Context) (SensitiveMaterial, error) {
			loads[key]++
			return NewSensitiveMaterial(string(key.Platform) + "@" + key.Host), nil
		}
	}
	first := SessionMaterialKey{Platform: "weibo", Host: "weibo.com"}
	second := SessionMaterialKey{Platform: "redbook", Host: "www.xiaohongshu.com"}
	third := SessionMaterialKey{Platform: "douyin", Host: "www.douyin.com"}
	for _, key := range []SessionMaterialKey{first, second, first, third} {
		if _, err := provider.Get(t.Context(), key, load(key)); err != nil {
			t.Fatal(err)
		}
	}
	if loads[first] != 1 || loads[second] != 1 || loads[third] != 1 {
		t.Fatalf("unexpected initial loads: %#v", loads)
	}
	if _, err := provider.Get(t.Context(), second, load(second)); err != nil {
		t.Fatal(err)
	}
	if loads[second] != 2 {
		t.Fatalf("least-recently-used entry was not evicted: %#v", loads)
	}

	otherPlatform := SessionMaterialKey{Platform: "twitter", Host: first.Host}
	if _, err := provider.Get(t.Context(), otherPlatform, load(otherPlatform)); err != nil {
		t.Fatal(err)
	}
	if loads[otherPlatform] != 1 {
		t.Fatalf("same host on another platform was not isolated: %#v", loads)
	}
}

func TestSessionMaterialWaiterCancellationAndFailedLoadIsNotCached(t *testing.T) {
	t.Parallel()
	provider, err := NewSessionMaterialProvider(SessionMaterialOptions{TTL: time.Minute, Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionMaterialKey{Platform: "weibo", Host: "weibo.com"}
	entered := make(chan struct{})
	release := make(chan struct{})
	primaryDone := make(chan error, 1)
	go func() {
		_, getErr := provider.Get(context.Background(), key, func(context.Context) (SensitiveMaterial, error) {
			close(entered)
			<-release
			return SensitiveMaterial{}, errors.New("synthetic load failure")
		})
		primaryDone <- getErr
	}()
	<-entered

	waiterCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Get(waiterCtx, key, func(context.Context) (SensitiveMaterial, error) {
		t.Fatal("singleflight waiter unexpectedly became a loader")
		return SensitiveMaterial{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	close(release)
	if err := <-primaryDone; err == nil {
		t.Fatal("primary failed load unexpectedly succeeded")
	}

	var reloads atomic.Int32
	if _, err := provider.Get(t.Context(), key, func(context.Context) (SensitiveMaterial, error) {
		reloads.Add(1)
		return NewSensitiveMaterial("replacement"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if reloads.Load() != 1 {
		t.Fatalf("failed material load was cached; reloads=%d", reloads.Load())
	}
}
