package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(parent context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "healthcheck" {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		if _, err := netguard.VerifyLoopbackProxyEndpoint(getenv("NETGUARD_URL")); err != nil {
			_, _ = fmt.Fprintln(stderr, "netguard-proxy healthcheck failed")
			return 1
		}
		proxy, err := netguard.NewProxy(netguard.ProxyOptions{PolicyFingerprint: getenv("NETGUARD_POLICY_FINGERPRINT")})
		if err != nil || proxy.Healthcheck(ctx) != nil {
			_, _ = fmt.Fprintln(stderr, "netguard-proxy healthcheck failed")
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(map[string]any{"ok": true, "role": "netguard-proxy", "policy": proxy.PolicyFingerprint()})
		return 0
	}
	if len(args) == 1 && args[0] == "serve" {
		endpoint, err := netguard.VerifyLoopbackProxyEndpoint(getenv("NETGUARD_URL"))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "netguard-proxy requires verified loopback endpoint")
			return 1
		}
		proxy, err := netguard.NewProxy(netguard.ProxyOptions{PolicyFingerprint: getenv("NETGUARD_POLICY_FINGERPRINT")})
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "netguard-proxy requires verified policy")
			return 1
		}
		server := &http.Server{
			Addr:              endpointAddress(endpoint),
			Handler:           proxy,
			ReadHeaderTimeout: 5 * time.Second,
		}
		if err := server.ListenAndServe(); err != nil {
			_, _ = fmt.Fprintln(stderr, "netguard-proxy stopped")
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "netguard-proxy requires explicit serve or healthcheck")
	return 1
}

func endpointAddress(endpoint string) string {
	trimmed, err := netguard.VerifyLoopbackProxyEndpoint(endpoint)
	if err != nil {
		return ""
	}
	const prefix = "http://"
	if len(trimmed) > len(prefix) && trimmed[:len(prefix)] == prefix {
		return trimmed[len(prefix):]
	}
	return ""
}
