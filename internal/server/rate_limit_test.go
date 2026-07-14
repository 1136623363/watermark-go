package server

import (
	"testing"
	"time"

	"watermark-backend/internal/runtimecfg"
)

func TestRateLimitDisabledByDefault(t *testing.T) {
	t.Setenv("RATE_LIMIT_ENABLED", "")
	t.Chdir(t.TempDir())
	if err := runtimecfg.Load(); err != nil {
		t.Fatalf("load runtime settings: %v", err)
	}
	if rateLimitEnabled() {
		t.Fatal("rate limit should be disabled unless RATE_LIMIT_ENABLED is explicitly enabled")
	}
}

func TestRateLimitCanBeEnabled(t *testing.T) {
	t.Setenv("RATE_LIMIT_ENABLED", "true")
	t.Chdir(t.TempDir())
	if err := runtimecfg.Load(); err != nil {
		t.Fatalf("load runtime settings: %v", err)
	}
	if !rateLimitEnabled() {
		t.Fatal("rate limit should be enabled when RATE_LIMIT_ENABLED=true")
	}
}

func TestAllowRequestMemoryWindow(t *testing.T) {
	resetRateLimitMemoryForTest()
	key := rateLimitKey("parse", "127.0.0.1")

	for i := 0; i < 2; i++ {
		allowed, retryAfter := allowRequestMemory(key, 2, time.Minute)
		if !allowed {
			t.Fatalf("request %d should be allowed, retryAfter=%s", i+1, retryAfter)
		}
	}

	allowed, retryAfter := allowRequestMemory(key, 2, time.Minute)
	if allowed {
		t.Fatal("third request should be denied")
	}
	if retryAfter <= 0 {
		t.Fatalf("retryAfter should be positive, got %s", retryAfter)
	}
}

func TestAllowRequestMemoryWindowExpires(t *testing.T) {
	resetRateLimitMemoryForTest()
	key := rateLimitKey("parse", "127.0.0.1")

	if allowed, _ := allowRequestMemory(key, 1, 10*time.Millisecond); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _ := allowRequestMemory(key, 1, 10*time.Millisecond); allowed {
		t.Fatal("second request inside window should be denied")
	}

	time.Sleep(20 * time.Millisecond)
	if allowed, retryAfter := allowRequestMemory(key, 1, 10*time.Millisecond); !allowed {
		t.Fatalf("request after window should be allowed, retryAfter=%s", retryAfter)
	}
}
