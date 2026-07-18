package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
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

func TestParserHelperDefaultCommandDoesNotStartWithoutHandshake(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, helperEnv(nil), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "verified sandbox") {
		t.Fatalf("default command code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
