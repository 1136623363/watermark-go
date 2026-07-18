package cache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T, version string) Key {
	t.Helper()
	key, err := NewKey(KeyParts{
		Platform:            "bilibili",
		CanonicalResourceID: "BV1xx-canonical-resource",
		ParserVersion:       "parser-v1",
		ResultSchemaVersion: version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCacheKeyBindsPlatformResourceParserAndSchemaVersion(t *testing.T) {
	key, err := NewKey(KeyParts{
		Platform:            "BiliBili",
		CanonicalResourceID: "https://www.bilibili.com/video/BV1?tracking=redacted",
		ParserVersion:       "parser-v1",
		ResultSchemaVersion: "schema-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := key.String()
	for _, forbidden := range []string{"bilibili.com", "tracking", "BV1"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("cache key exposed reversible resource material: %s", rendered)
		}
	}

	changed, err := NewKey(KeyParts{
		Platform:            "bilibili",
		CanonicalResourceID: "https://www.bilibili.com/video/BV1?tracking=redacted",
		ParserVersion:       "parser-v2",
		ResultSchemaVersion: "schema-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.String() == key.String() {
		t.Fatal("parser version change did not change cache key")
	}
}

func TestCacheVersionChangeMisses(t *testing.T) {
	ctx := context.Background()
	cache := NewMemory(8)
	keyV1 := testKey(t, "schema-v1")
	keyV2 := testKey(t, "schema-v2")
	if err := cache.Set(ctx, keyV1, []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.Get(ctx, keyV2); err != nil || ok {
		t.Fatalf("schema version changed cache lookup = %t, %v", ok, err)
	}
}

func TestCacheFallsBackWhenRedisUnavailable(t *testing.T) {
	ctx := context.Background()
	key := testKey(t, "schema-v1")
	cache := NewTiered(failingStore{}, NewMemory(8))
	if err := cache.Set(ctx, key, []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cache.Get(ctx, key)
	if err != nil || !ok || string(got) != "v" {
		t.Fatalf("fallback get = %q/%t/%v", got, ok, err)
	}
}

func TestCacheSingleflightCoalescesSameKey(t *testing.T) {
	ctx := context.Background()
	key := testKey(t, "schema-v1")
	cache := NewTiered(nil, NewMemory(8))
	start := make(chan struct{})
	var loads atomic.Int32
	loader := func(context.Context) ([]byte, error) {
		loads.Add(1)
		<-start
		return []byte("loaded"), nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.Do(ctx, key, time.Minute, false, loader)
			if err != nil || string(got) != "loaded" {
				errs <- errors.New("unexpected singleflight result")
			}
		}()
	}
	for loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", loads.Load())
	}
}

func TestNegativeCachePolicyRejectsNonCacheableErrors(t *testing.T) {
	for _, class := range []ErrorClass{
		ErrorContextCanceled,
		ErrorInternal,
		ErrorCredentialRequired,
		ErrorSchemaChanged,
		ErrorSecurityRejected,
		ErrorSessionExpired,
	} {
		if NegativeCacheable(class) || NegativeTTL(class) != 0 {
			t.Fatalf("negative cache accepted non-cacheable class %s", class)
		}
	}
	if !NegativeCacheable(ErrorStableFailure) || NegativeTTL(ErrorStableFailure) != 180*time.Second {
		t.Fatal("stable failure negative cache policy is wrong")
	}
}

func TestRedisAndMemoryShareCacheSemantics(t *testing.T) {
	key := testKey(t, "schema-v1")
	if RedisNamespacedKey("candidate-final", key) != "wm:candidate-final:"+key.String() {
		t.Fatalf("unexpected redis key namespace")
	}
	memory := NewMemory(1)
	ctx := context.Background()
	if err := memory.Set(ctx, key, []byte("one"), time.Minute); err != nil {
		t.Fatal(err)
	}
	other := testKey(t, "schema-v2")
	if err := memory.Set(ctx, other, []byte("two"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := memory.Get(ctx, key); err != nil || ok {
		t.Fatalf("memory capacity did not evict oldest key: %t/%v", ok, err)
	}
}

type failingStore struct{}

func (failingStore) Get(context.Context, Key) ([]byte, bool, error) {
	return nil, false, errors.New("redis unavailable")
}

func (failingStore) Set(context.Context, Key, []byte, time.Duration) error {
	return errors.New("redis unavailable")
}

func (failingStore) Delete(context.Context, Key) error {
	return errors.New("redis unavailable")
}
