package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func helperEnv(overrides map[string]string) func(string) string {
	values := map[string]string{
		"PARSER_SANDBOX_ROLE":         "parser-helper",
		"PARSER_SANDBOX_RUN_ID":       "run-123",
		"PARSER_SANDBOX_IMAGE_DIGEST": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"PARSER_SANDBOX_UDS":          "/run/watermark/parser-helper.sock",
		"NETGUARD_URL":                "http://127.0.0.1:18080",
		"NETGUARD_POLICY_FINGERPRINT": "task4-policy",
		"UNRELATED_RUNTIME_LABEL":     "unrelated-sentinel",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) string { return values[key] }
}

func TestParserHelperHealthcheckUsesOnlySandboxIdentity(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"healthcheck"}, helperEnv(nil), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"ok":true`) {
		t.Fatalf("healthcheck code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "unrelated-sentinel") {
		t.Fatal("healthcheck leaked unrelated runtime value")
	}
}

func TestParserHelperHealthcheckFailsClosedOnWrongRole(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"healthcheck"}, helperEnv(map[string]string{"PARSER_SANDBOX_ROLE": "api"}), &stdout, &stderr)
	if code == 0 || strings.Contains(stdout.String()+stderr.String(), "unrelated-sentinel") {
		t.Fatalf("unsafe healthcheck result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestParserHelperServeBlocksWithVerifiedHandshakeUntilContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve"}, helperEnv(nil), &stdout, &stderr)
	}()
	select {
	case code := <-done:
		t.Fatalf("serve exited before cancellation code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve cancellation code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not stop after context cancellation")
	}
}

func TestParserHelperServeContextIsSignalCancelable(t *testing.T) {
	t.Parallel()
	ctx, stop := serveContext(context.Background())
	defer stop()
	if ctx.Done() == nil {
		t.Fatal("serve context must expose a cancellation channel")
	}
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("serve context did not cancel")
	}
}

func TestParserHelperDefaultCommandDoesNotStartWithoutHandshake(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, helperEnv(nil), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "verified sandbox") {
		t.Fatalf("default command code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
