package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeProxy struct {
	endpoint string
	verified bool
}

func (proxy fakeProxy) VerifiedEndpoint() (string, bool) { return proxy.endpoint, proxy.verified }

type recordingExecutor struct{ command Command }

func (executor *recordingExecutor) Run(_ context.Context, command Command) (Output, error) {
	executor.command = command
	return Output{Stdout: []byte(`{"id":"synthetic"}`)}, nil
}

type testImagePaths map[PathKind]map[string]bool

func (paths testImagePaths) AllowsImagePath(kind PathKind, value string) bool {
	return paths[kind][value]
}

func approvedTestImagePaths() testImagePaths {
	return testImagePaths{
		PathExecutable: {"/usr/local/bin/yt-dlp": true, "/usr/local/bin/python3": true},
		PathScript:     {"/app/bridges/universal/python/bridge.py": true},
		PathReadOnlySource: {
			"/app/third_party/videodl": true,
			"/app/third_party/musicdl": true,
		},
		PathWorkDir: {"/app/cache/universal": true},
	}
}

func TestRunnerRequiresVerifiedLoopbackGuardProxy(t *testing.T) {
	t.Parallel()
	for _, proxy := range []GuardProxy{
		nil,
		fakeProxy{endpoint: "http://127.0.0.1:18080", verified: false},
		fakeProxy{endpoint: "http://192.168.1.8:18080", verified: true},
	} {
		if _, err := New(Config{Binary: "/usr/local/bin/yt-dlp", Timeout: time.Second, Proxy: proxy, Execute: &recordingExecutor{}, Paths: approvedTestImagePaths()}); err == nil {
			t.Fatal("runner accepted an unverified or non-loopback proxy")
		}
	}
}

func TestRunnerCommandHasOneNonOverridableProxyAndMinimalEnvironment(t *testing.T) {
	t.Parallel()
	executor := &recordingExecutor{}
	runner, err := New(Config{
		Binary: "/usr/local/bin/yt-dlp", Timeout: 3 * time.Second,
		Proxy: fakeProxy{endpoint: "http://127.0.0.1:18080", verified: true}, Execute: executor, Paths: approvedTestImagePaths(),
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := runner.Command("https://media.example/watch?id=synthetic")
	if err != nil {
		t.Fatal(err)
	}
	proxyCount := 0
	for _, argument := range command.Args {
		if argument == "--proxy" {
			proxyCount++
		}
	}
	if proxyCount != 1 || !reflect.DeepEqual(command.Env, []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}) {
		t.Fatalf("unsafe command shape: proxy_count=%d arg_count=%d env_count=%d", proxyCount, len(command.Args), len(command.Env))
	}
	joined := strings.Join(command.Args, " ")
	if !strings.Contains(joined, "http://127.0.0.1:18080") {
		t.Fatal("guard proxy absent from command")
	}
	formats := []string{
		"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%o", "%f", "%e", "%c", "%U", "%p", "%T", "%z",
		"% 120.80v", "%#+120.80q",
	}
	input := NewInput([]byte("runner-input-synthetic"))
	output := Output{Stdout: []byte("runner-output-synthetic"), Stderr: []byte("runner-stderr-synthetic")}
	values := map[string]any{
		"command": command, "command-pointer": &command,
		"input": input, "input-pointer": &input,
		"output": output, "output-pointer": &output,
	}
	for _, format := range formats {
		formatValues := values
		if format == "%v" || format == "%+v" || format == "%#v" {
			formatValues = make(map[string]any, len(values)+1)
			for name, value := range values {
				formatValues[name] = value
			}
			formatValues["nested"] = []any{command, input, output}
		}
		if format == "%p" {
			formatValues = map[string]any{
				"command-pointer": &command, "input-pointer": &input, "output-pointer": &output,
			}
		}
		for name, value := range formatValues {
			rendered := fmt.Sprintf(format, value)
			if strings.Contains(rendered, "synthetic") {
				t.Fatalf("format %q (%s) exposed runner material: %s", format, name, rendered)
			}
		}
	}
	if _, err := json.Marshal(command); err == nil {
		t.Fatal("structured runner command was serializable")
	}
}

func TestRunnerRejectsNilContextAndControlCharacterPath(t *testing.T) {
	t.Parallel()
	base := Config{
		Binary: "/usr/local/bin/yt-dlp", Timeout: time.Second,
		Proxy: fakeProxy{endpoint: "http://127.0.0.1:18080", verified: true}, Execute: &recordingExecutor{}, Paths: approvedTestImagePaths(),
	}
	runner, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(nil, "https://media.example/watch"); err == nil {
		t.Fatal("runner accepted a nil context")
	}
	base.Binary = "/usr/local/bin/yt-dlp\n--config-location=/tmp/unsafe"
	if _, err := New(base); err == nil {
		t.Fatal("runner accepted a control character in the binary path")
	}
}

func TestRunnerRejectsMalformedGuardProxyEndpoint(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"http://user:pass@127.0.0.1:18080",
		"http://127.0.0.1:18080/path",
		"http://127.0.0.1:18080?proxy=override",
		"https://127.0.0.1:18080",
		"http://localhost:18080",
	} {
		_, err := New(Config{
			Binary: "/usr/local/bin/yt-dlp", Timeout: time.Second,
			Proxy: fakeProxy{endpoint: endpoint, verified: true}, Execute: &recordingExecutor{}, Paths: approvedTestImagePaths(),
		})
		if err == nil {
			t.Fatalf("runner accepted malformed guard proxy %q", endpoint)
		}
	}
}

func TestRunnerRejectsAbsolutePathOutsideVerifiedImageAllowlist(t *testing.T) {
	t.Parallel()
	config := Config{
		Binary: "/tmp/attacker-controlled-yt-dlp", Timeout: time.Second,
		Proxy: fakeProxy{endpoint: "http://127.0.0.1:18080", verified: true}, Execute: &recordingExecutor{},
		Paths: approvedTestImagePaths(),
	}
	if _, err := New(config); err == nil {
		t.Fatal("runner accepted an absolute executable outside verified image provenance")
	}
	config.Binary = "/usr/local/bin/yt-dlp"
	config.Paths = nil
	if _, err := New(config); err == nil {
		t.Fatal("runner accepted a missing image path policy")
	}
}
