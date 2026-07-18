package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func proxyEnv(overrides map[string]string) func(string) string {
	values := map[string]string{
		"NETGUARD_URL":                "http://127.0.0.1:18080",
		"NETGUARD_POLICY_FINGERPRINT": "task4-policy",
		"UNRELATED_RUNTIME_LABEL":     "unrelated-sentinel",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) string { return values[key] }
}

func TestNetguardProxyHealthcheckUsesOnlyProxyIdentity(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"healthcheck"}, proxyEnv(nil), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"ok":true`) {
		t.Fatalf("healthcheck code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "unrelated-sentinel") {
		t.Fatal("healthcheck leaked unrelated runtime value")
	}
}

func TestNetguardProxyHealthcheckFailsClosedOnRemoteEndpoint(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"healthcheck"}, proxyEnv(map[string]string{"NETGUARD_URL": "http://egress-proxy:18080"}), &stdout, &stderr)
	if code == 0 || strings.Contains(stdout.String()+stderr.String(), "unrelated-sentinel") {
		t.Fatalf("unsafe healthcheck result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestNetguardProxyDefaultCommandDoesNotStartImplicitly(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, proxyEnv(nil), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "explicit serve") {
		t.Fatalf("default command code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
