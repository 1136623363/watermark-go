package server

import (
	"errors"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

func TestNativeParserDoesNotAttemptYTDLPWhenFallbackDisabled(t *testing.T) {
	service, err := newApplicationNativeParser(config.ParserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	setApplicationNativeParser(service)
	setApplicationRunnerConfig(config.RunnerConfig{
		Engine:          config.ParserEngineNative,
		FallbackEnabled: false,
		YTDLP: config.YTDLPRunnerConfig{
			Binary: "/usr/local/bin/yt-dlp", Timeout: time.Second,
		},
	})
	t.Cleanup(func() {
		setApplicationNativeParser(nil)
		setApplicationRunnerConfig(config.RunnerConfig{})
	})

	_, err = parseWithNativeParser(t.Context(), "https://youtube.com/watch?v=synthetic", "https://youtube.com/watch?v=synthetic")
	var unknownHost *coreparser.UnknownHostError
	if !errors.As(err, &unknownHost) {
		t.Fatalf("disabled fallback replaced the native typed error: %v", err)
	}
}

func TestConfiguredParserEngineUsesTypedStartupConfig(t *testing.T) {
	service, err := newApplicationNativeParser(config.ParserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	setApplicationNativeParser(service)
	setApplicationRunnerConfig(config.RunnerConfig{
		Engine: config.ParserEngineUniversal,
		Universal: config.UniversalRunnerConfig{
			PythonBinary: "/usr/local/bin/python3",
			BridgeScript: "/app/bridges/universal/python/bridge.py",
			VideoSource:  "/app/third_party/CharlesPikachu/videodl",
			MusicSource:  "/app/third_party/CharlesPikachu/musicdl",
			WorkDir:      "/app/cache/universal-parser",
			VideoTimeout: time.Second, MusicTimeout: time.Second, MusicItemLimit: 1,
		},
	})
	t.Cleanup(func() {
		setApplicationNativeParser(nil)
		setApplicationRunnerConfig(config.RunnerConfig{})
	})

	_, err = parseWithConfiguredEngine(t.Context(), "https://youtube.com/watch?v=synthetic", "https://youtube.com/watch?v=synthetic")
	if err == nil || err.Error() != "universal parser requires an isolated runner" {
		t.Fatalf("typed universal engine was not selected: %v", err)
	}
}

func TestYTDLPHostAdmissionNeverGuessesFromPathOrQuery(t *testing.T) {
	t.Parallel()
	parseErr := errors.New("synthetic native failure")
	for _, rawURL := range []string{
		"https://evil.example/path/youtube.com/watch",
		"https://evil.example/watch?next=https://youtube.com/watch",
		"https://youtube.com.evil.example/watch",
		"not-a-url-youtube.com",
	} {
		if shouldTryYTDLP(rawURL, parseErr) {
			t.Fatalf("yt-dlp guessed an unapproved host for %q", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://youtube.com/watch?v=synthetic",
		"https://www.youtube.com/watch?v=synthetic",
		"https://youtu.be/synthetic",
	} {
		if !shouldTryYTDLP(rawURL, parseErr) {
			t.Fatalf("yt-dlp rejected an approved exact/subdomain host: %q", rawURL)
		}
	}
}

func TestUniversalMusicRemainsTypedCredentialRequiredUntilGuardedWiring(t *testing.T) {
	t.Parallel()
	_, err := tryParseWithUniversalParser(t.Context(), "https://music.163.com/song?id=synthetic")
	var typed *coreparser.ParseError
	if !errors.As(err, &typed) || typed.Code != coreparser.ErrorCredentialRequired {
		t.Fatalf("universal music was not production-disabled with a typed credential error: %v", err)
	}
}
