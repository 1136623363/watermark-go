package universal

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	ytdlprunner "github.com/1136623363/watermark-go/internal/parser/ytdlp"
)

type verifiedTestProxy struct{}

func (verifiedTestProxy) VerifiedEndpoint() (string, bool) {
	return "http://127.0.0.1:18080", true
}

type bridgeExecutor struct {
	command ytdlprunner.Command
	output  ytdlprunner.Output
	err     error
	wait    bool
}

func (executor *bridgeExecutor) Run(ctx context.Context, command ytdlprunner.Command) (ytdlprunner.Output, error) {
	executor.command = command
	if executor.wait {
		<-ctx.Done()
		return ytdlprunner.Output{}, ctx.Err()
	}
	return executor.output, executor.err
}

func validBridgeConfig(executor ytdlprunner.Executor) Config {
	return Config{
		PythonBin: "/usr/local/bin/python3", BridgeScript: "/app/bridges/universal/python/bridge.py",
		VideoDLPath: "/app/third_party/videodl", MusicDLPath: "/app/third_party/musicdl",
		WorkDir: "/app/cache/universal", BridgeTimeout: 1, MusicDLTimeout: 1,
		MusicDLItemLimit: 3, Runner: executor, GuardProxy: verifiedTestProxy{}, Paths: ytdlprunner.ImagePathPolicy(approvedUniversalTestImagePaths{}),
	}
}

type approvedUniversalTestImagePaths struct{}

func (approvedUniversalTestImagePaths) AllowsImagePath(kind ytdlprunner.PathKind, value string) bool {
	allowed := map[ytdlprunner.PathKind]map[string]bool{
		ytdlprunner.PathExecutable: {"/usr/local/bin/python3": true},
		ytdlprunner.PathScript:     {"/app/bridges/universal/python/bridge.py": true},
		ytdlprunner.PathReadOnlySource: {
			"/app/third_party/videodl": true,
			"/app/third_party/musicdl": true,
		},
		ytdlprunner.PathWorkDir: {"/app/cache/universal": true},
	}
	return allowed[kind][value]
}

func TestPythonBridgeConstructorFailsClosedWithoutGuardedRunner(t *testing.T) {
	t.Parallel()
	config := validBridgeConfig(nil)
	if _, err := NewPythonBridge(config); err == nil {
		t.Fatal("bridge accepted a missing isolated runner")
	}
	config.Runner = &bridgeExecutor{}
	config.GuardProxy = nil
	if _, err := NewPythonBridge(config); err == nil {
		t.Fatal("bridge accepted a missing verified proxy")
	}
}

func TestPythonBridgeUsesMinimalEnvironmentProxyAndStructuredOutput(t *testing.T) {
	t.Parallel()
	executor := &bridgeExecutor{output: ytdlprunner.Output{Stdout: []byte(`{
  "ok": true,
  "kind": "video",
  "items": [{
    "source":"synthetic", "title":"fixture", "download_url":"https://cdn.example/video.mp4",
    "unknown_raw":{"cookie":"must-not-cross"}
  }]
}`)}}
	bridge, err := NewPythonBridge(validBridgeConfig(executor))
	if err != nil {
		t.Fatal(err)
	}
	result, err := bridge.ParseVideo(t.Context(), ParseRequest{URL: "https://media.example/watch", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.PlayAddr != "https://cdn.example/video.mp4" || len(result.Items) != 1 {
		t.Fatalf("unexpected structured result: %#v", result)
	}
	wantEnv := []string{
		"PYTHONIOENCODING=utf-8",
		"VIDEODL_PATH=/app/third_party/videodl",
		"MUSICDL_PATH=/app/third_party/musicdl",
		"BRIDGE_WORK_DIR=/app/cache/universal",
		"MUSICDL_ITEM_LIMIT=3",
		"HTTP_PROXY=http://127.0.0.1:18080",
		"http_proxy=http://127.0.0.1:18080",
		"HTTPS_PROXY=http://127.0.0.1:18080",
		"https_proxy=http://127.0.0.1:18080",
		"ALL_PROXY=http://127.0.0.1:18080",
		"all_proxy=http://127.0.0.1:18080",
		"NO_PROXY=",
		"no_proxy=",
	}
	if !reflect.DeepEqual(executor.command.Env, wantEnv) {
		t.Fatalf("child environment = %#v", executor.command.Env)
	}
	if !executor.command.TerminateProcessGroup || strings.Count(strings.Join(executor.command.Args, " "), "--guard-proxy") != 1 {
		t.Fatalf("unsafe runner command: %#v", executor.command)
	}
	if strings.Contains(strings.Join(executor.command.Env, "\n"), "MUSICDL_CONFIG") {
		t.Fatal("opaque music configuration crossed the helper boundary")
	}
}

func TestPythonBridgeDoesNotExposeRawOutputOrStderr(t *testing.T) {
	t.Parallel()
	sentinel := "secret-output-sentinel"
	executor := &bridgeExecutor{output: ytdlprunner.Output{Stdout: []byte("not-json-" + sentinel), Stderr: []byte(sentinel)}}
	bridge, err := NewPythonBridge(validBridgeConfig(executor))
	if err != nil {
		t.Fatal(err)
	}
	_, err = bridge.ParseVideo(t.Context(), ParseRequest{URL: "https://media.example/watch"})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("unsafe invalid-output error: %v", err)
	}

	executor.output = ytdlprunner.Output{}
	executor.err = errors.New(sentinel)
	_, err = bridge.ParseVideo(t.Context(), ParseRequest{URL: "https://media.example/watch"})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("unsafe runner error: %v", err)
	}
}

func TestPythonBridgeTimeoutCarriesProcessGroupRequirement(t *testing.T) {
	t.Parallel()
	executor := &bridgeExecutor{wait: true}
	config := validBridgeConfig(executor)
	config.BridgeTimeout = 1
	bridge, err := NewPythonBridge(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	_, err = bridge.ParseVideo(ctx, ParseRequest{URL: "https://media.example/watch"})
	if err == nil || !executor.command.TerminateProcessGroup {
		t.Fatalf("timeout/process group behavior missing: %v", err)
	}
}

func TestPythonBridgeRejectsUnsafeHelperMediaURLs(t *testing.T) {
	t.Parallel()
	executor := &bridgeExecutor{output: ytdlprunner.Output{Stdout: []byte(`{
  "ok": true,
  "kind": "video",
  "items": [{
    "download_url":"http://127.0.0.1/admin?token=must-not-cross",
    "audio_download_url":"file:///etc/passwd",
    "cover_url":"javascript:alert(1)"
  }]
}`)}}
	bridge, err := NewPythonBridge(validBridgeConfig(executor))
	if err != nil {
		t.Fatal(err)
	}
	_, err = bridge.ParseVideo(t.Context(), ParseRequest{URL: "https://media.example/watch?token=input-secret"})
	if err == nil {
		t.Fatal("bridge accepted helper media URLs outside the guarded URL boundary")
	}
	if strings.Contains(err.Error(), "must-not-cross") || strings.Contains(err.Error(), "input-secret") {
		t.Fatalf("bridge error exposed query material: %v", err)
	}
}

func TestPythonBridgeStripsInputQueryFromReturnedMetadata(t *testing.T) {
	t.Parallel()
	executor := &bridgeExecutor{output: ytdlprunner.Output{Stdout: []byte(`{
  "ok": true,
  "kind": "video",
  "items": [{"source":"synthetic", "download_url":"https://cdn.example/"}]
}`)}}
	bridge, err := NewPythonBridge(validBridgeConfig(executor))
	if err != nil {
		t.Fatal(err)
	}
	result, err := bridge.ParseVideo(t.Context(), ParseRequest{URL: "https://media.example/watch?token=input-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Title, "input-secret") || strings.Contains(result.SourceURL, "input-secret") {
		t.Fatalf("returned metadata exposed input query: %#v", result)
	}
	if result.SourceURL != "https://media.example/watch" {
		t.Fatalf("safe source URL = %q", result.SourceURL)
	}
}

func TestPythonBridgeRejectsControlCharactersInFixedPaths(t *testing.T) {
	t.Parallel()
	config := validBridgeConfig(&bridgeExecutor{})
	config.BridgeScript = "/app/bridge.py\n--unexpected-argument"
	if _, err := NewPythonBridge(config); err == nil {
		t.Fatal("bridge accepted a control character in a configured path")
	}
}

func TestPythonBridgeRejectsAbsolutePathOutsideVerifiedImageAllowlist(t *testing.T) {
	t.Parallel()
	config := validBridgeConfig(&bridgeExecutor{})
	config.BridgeScript = "/tmp/attacker-controlled-bridge.py"
	if _, err := NewPythonBridge(config); err == nil {
		t.Fatal("bridge accepted an absolute path outside verified image provenance")
	}
	config = validBridgeConfig(&bridgeExecutor{})
	config.Paths = nil
	if _, err := NewPythonBridge(config); err == nil {
		t.Fatal("bridge accepted a missing image path policy")
	}
}

func TestPythonBridgeScriptRejectsUnsafeGuardProxyEndpoints(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is unavailable")
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate bridge test file")
	}
	bridgeScript := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..", "bridges", "universal", "python", "bridge.py"))
	python := `
import importlib.util
import sys

spec = importlib.util.spec_from_file_location("watermark_python_bridge_policy", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

accepted = module.parse_cli_args(["bridge.py", "video", "--guard-proxy", "http://127.0.0.1:18080/"])
if accepted != ("video", "http://127.0.0.1:18080"):
    raise AssertionError(accepted)

for endpoint in (
    "https://127.0.0.1:18080",
    "http://localhost:18080",
    "http://192.168.1.8:18080",
    "http://user:pass@127.0.0.1:18080",
    "http://127.0.0.1",
    "http://127.0.0.1:18080/path",
    "http://127.0.0.1:18080?target=http://evil.example",
    "http://127.0.0.1:18080#fragment",
):
    try:
        module.parse_cli_args(["bridge.py", "video", "--guard-proxy", endpoint])
    except ValueError:
        continue
    raise AssertionError(f"accepted unsafe guard proxy endpoint {endpoint!r}")
`
	command := exec.Command("python3", "-c", python, bridgeScript)
	command.Env = []string{"PYTHONDONTWRITEBYTECODE=1"}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("python bridge accepted an unsafe guard proxy endpoint: %v\n%s", err, output)
	}
}
