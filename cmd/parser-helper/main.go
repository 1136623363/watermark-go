package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/1136623363/watermark-go/internal/parser/sandbox"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(parent context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "healthcheck" {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		if err := sandbox.Healthcheck(ctx, identityFromEnv(getenv), "parser-helper"); err != nil {
			_, _ = fmt.Fprintln(stderr, "parser-helper healthcheck failed")
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(map[string]any{"ok": true, "role": "parser-helper"})
		return 0
	}
	if len(args) == 1 && args[0] == "serve" {
		if parent == nil {
			parent = context.Background()
		}
		server, err := sandbox.NewServer(identityFromEnv(getenv), "parser-helper")
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "parser-helper requires verified sandbox handshake")
			return 1
		}
		if err := server.Serve(parent); err != nil {
			_, _ = fmt.Fprintln(stderr, "parser-helper stopped")
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "parser-helper requires verified sandbox handshake")
	return 1
}

func identityFromEnv(getenv func(string) string) sandbox.Identity {
	if getenv == nil {
		getenv = os.Getenv
	}
	return sandbox.Identity{
		Role:              getenv("PARSER_SANDBOX_ROLE"),
		RunID:             getenv("PARSER_SANDBOX_RUN_ID"),
		ImageDigest:       getenv("PARSER_SANDBOX_IMAGE_DIGEST"),
		SocketPath:        getenv("PARSER_SANDBOX_UDS"),
		ProxyEndpoint:     getenv("NETGUARD_URL"),
		PolicyFingerprint: getenv("NETGUARD_POLICY_FINGERPRINT"),
	}
}
