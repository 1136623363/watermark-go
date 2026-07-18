package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func validIdentity() Identity {
	return Identity{
		Role:              "parser-helper",
		RunID:             "run-123",
		ImageDigest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SocketPath:        "/run/watermark/parser-helper.sock",
		ProxyEndpoint:     "http://127.0.0.1:18080",
		PolicyFingerprint: "task4-policy",
	}
}

func TestSandboxIdentityFailsClosedWithoutVerifiedRoleSocketAndProxy(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Identity){
		"wrong-role":          func(identity *Identity) { identity.Role = "api" },
		"missing-run":         func(identity *Identity) { identity.RunID = "" },
		"relative-socket":     func(identity *Identity) { identity.SocketPath = "parser.sock" },
		"remote-proxy":        func(identity *Identity) { identity.ProxyEndpoint = "http://egress-proxy:18080" },
		"proxy-with-userinfo": func(identity *Identity) { identity.ProxyEndpoint = "http://user:pass@127.0.0.1:18080" },
		"secret-fingerprint":  func(identity *Identity) { identity.PolicyFingerprint = "contains-secret" },
	} {
		t.Run(name, func(t *testing.T) {
			identity := validIdentity()
			mutate(&identity)
			if _, err := NewClient(identity, "parser-helper"); !errors.Is(err, ErrSandboxUnverified) {
				t.Fatalf("NewClient error = %v", err)
			}
		})
	}
	client, err := NewClient(validIdentity(), "parser-helper")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint, ok := client.GuardProxy().VerifiedEndpoint(); !ok || endpoint != "http://127.0.0.1:18080" {
		t.Fatalf("guard proxy = %q, %v", endpoint, ok)
	}
}

func TestSandboxJobProtocolRejectsOversizedPayloadAndSecrets(t *testing.T) {
	t.Parallel()
	if _, err := NewJob("video", []byte(`{"url":"https://media.example/watch"}`)); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"empty":  nil,
		"secret": []byte(`{"Authorization":"Bearer sentinel"}`),
		"large":  []byte(strings.Repeat("a", MaxJobPayloadBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewJob("video", payload); !errors.Is(err, ErrUnsafePayload) {
				t.Fatalf("NewJob error = %v", err)
			}
		})
	}
}

func TestSandboxJobFormattingNeverRevealsPayload(t *testing.T) {
	t.Parallel()
	const sentinel = "payload-sentinel"
	job, err := NewJob("video", []byte(`{"url":"https://media.example/watch?x=`+sentinel+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		if rendered := fmt.Sprintf(format, job); strings.Contains(rendered, sentinel) {
			t.Fatalf("format %q exposed payload: %s", format, rendered)
		}
	}
	if _, err := json.Marshal(job); err != nil {
		t.Fatalf("opaque job should still serialize only public fields: %v", err)
	}
}
