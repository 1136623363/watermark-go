package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

const FormatSelector = "best[protocol=https][vcodec!=none][acodec!=none]/best[protocol=http][vcodec!=none][acodec!=none]/best[vcodec!=none][acodec!=none]/best"

type GuardProxy interface {
	VerifiedEndpoint() (string, bool)
}

type PathKind string

const (
	PathExecutable     PathKind = "executable"
	PathScript         PathKind = "script"
	PathReadOnlySource PathKind = "read-only-source"
	PathWorkDir        PathKind = "work-dir"
)

// ImagePathPolicy is a capability supplied by the isolated runtime after it
// has verified immutable image provenance. Absolute paths alone are not
// sufficient because an attacker-controlled absolute path is still mutable.
type ImagePathPolicy interface {
	AllowsImagePath(PathKind, string) bool
}

type Executor interface {
	Run(context.Context, Command) (Output, error)
}

type Command struct {
	Path                  string
	Args                  []string
	Env                   []string
	Dir                   string
	Stdin                 Input
	Timeout               time.Duration
	TerminateProcessGroup bool
}

type Input struct{ value []byte }

func NewInput(value []byte) Input { return Input{value: append([]byte(nil), value...)} }

func (input Input) Use(consumer func([]byte) error) error {
	if consumer == nil {
		return errors.New("runner input consumer is required")
	}
	return consumer(append([]byte(nil), input.value...))
}

func (Input) String() string   { return "[opaque-runner-input]" }
func (Input) GoString() string { return "ytdlp.Input([opaque])" }
func (Input) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[opaque-runner-input]"))
}
func (Input) MarshalJSON() ([]byte, error) {
	return nil, errors.New("runner input cannot be serialized")
}

func (Command) String() string   { return "[structured-runner-command]" }
func (Command) GoString() string { return "ytdlp.Command([structured])" }
func (Command) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[structured-runner-command]"))
}
func (Command) MarshalJSON() ([]byte, error) {
	return nil, errors.New("runner command cannot be serialized")
}

type Output struct {
	Stdout []byte
	Stderr []byte
}

func (Output) String() string   { return "[opaque-runner-output]" }
func (Output) GoString() string { return "ytdlp.Output([opaque])" }
func (Output) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[opaque-runner-output]"))
}
func (Output) MarshalJSON() ([]byte, error) {
	return nil, errors.New("runner output cannot be serialized")
}

type Config struct {
	Binary  string
	Timeout time.Duration
	Proxy   GuardProxy
	Execute Executor
	Paths   ImagePathPolicy
}

type Runner struct {
	config   Config
	endpoint string
}

func New(config Config) (*Runner, error) {
	if config.Execute == nil {
		return nil, errors.New("yt-dlp runner requires an isolated executor")
	}
	endpoint, err := ValidateGuardProxy(config.Proxy)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(config.Binary) || len(config.Binary) > 4096 || strings.ContainsAny(config.Binary, "\x00\r\n") {
		return nil, errors.New("yt-dlp binary must be a fixed absolute path")
	}
	if config.Paths == nil || !config.Paths.AllowsImagePath(PathExecutable, config.Binary) {
		return nil, errors.New("yt-dlp binary is not approved by image provenance")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("yt-dlp timeout must be positive")
	}
	return &Runner{config: config, endpoint: endpoint}, nil
}

func ValidateGuardProxy(proxy GuardProxy) (string, error) {
	if proxy == nil {
		return "", errors.New("verified netguard proxy is required")
	}
	endpoint, verified := proxy.VerifiedEndpoint()
	if !verified {
		return "", errors.New("verified netguard proxy is required")
	}
	return netguard.VerifyLoopbackProxyEndpoint(endpoint)
}

func (runner *Runner) Command(rawURL string) (Command, error) {
	if runner == nil {
		return Command{}, errors.New("nil yt-dlp runner")
	}
	if _, err := netguard.NewFetchURL(strings.TrimSpace(rawURL)); err != nil {
		return Command{}, errors.New("yt-dlp target URL rejected")
	}
	return Command{
		Path: runner.config.Binary,
		Args: []string{
			"--ignore-config", "--dump-single-json", "--no-warnings", "--no-playlist", "--skip-download",
			"--socket-timeout", "20", "--extractor-retries", "1", "--fragment-retries", "1",
			"--proxy", runner.endpoint, "-f", FormatSelector, rawURL,
		},
		Env: []string{
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
			"HOME=/tmp/netguard-empty-home",
			"XDG_CONFIG_HOME=/tmp/netguard-empty-xdg",
			"HTTP_PROXY=",
			"http_proxy=",
			"HTTPS_PROXY=",
			"https_proxy=",
			"ALL_PROXY=",
			"all_proxy=",
			"NO_PROXY=",
			"no_proxy=",
		}, Timeout: runner.config.Timeout,
		TerminateProcessGroup: true,
	}, nil
}

func (runner *Runner) Run(ctx context.Context, rawURL string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("yt-dlp context is required")
	}
	command, err := runner.Command(rawURL)
	if err != nil {
		return nil, err
	}
	output, err := runner.config.Execute.Run(ctx, command)
	if err != nil {
		return nil, errors.New("isolated yt-dlp runner failed")
	}
	if len(output.Stdout) == 0 || len(output.Stdout) > 8<<20 {
		return nil, errors.New("isolated yt-dlp output rejected")
	}
	return append([]byte(nil), output.Stdout...), nil
}
