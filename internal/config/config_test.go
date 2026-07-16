package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadProductionRejectsMissingShortAndPlaceholderSecrets(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "ADMIN_PASSWORD", value: ""},
		{key: "ADMIN_PASSWORD", value: "short"},
		{key: "ADMIN_PASSWORD", value: "change-me"},
		{key: "ADMIN_SESSION_SECRET", value: ""},
		{key: "ADMIN_SESSION_SECRET", value: "short"},
		{key: "ADMIN_SESSION_SECRET", value: "example-test"},
		{key: "DOWNLOAD_TOKEN_SECRET", value: ""},
		{key: "DOWNLOAD_TOKEN_SECRET", value: "short"},
		{key: "DOWNLOAD_TOKEN_SECRET", value: "invalid-for-test-only"},
		{key: "WECHAT_MINI_APP_SECRET", value: ""},
		{key: "WECHAT_MINI_APP_SECRET", value: "short"},
		{key: "WECHAT_MINI_APP_SECRET", value: "placeholder"},
	}

	for _, test := range tests {
		t.Run(test.key+"/invalid", func(t *testing.T) {
			environment := validProductionEnvironment()
			environment[test.key] = test.value

			_, err := LoadWith(environmentReader(environment))
			if err == nil || !strings.Contains(err.Error(), "weak production secret") {
				t.Fatalf("LoadWith() error = %v, want weak production secret", err)
			}
			assertErrorOmitsEnvironmentValues(t, err, environment)
		})
	}
}

func TestLoadRejectsUnknownEnvironment(t *testing.T) {
	_, err := LoadWith(environmentReader(map[string]string{"APP_ENV": "prodution"}))
	if err == nil || !strings.Contains(err.Error(), "unknown APP_ENV") {
		t.Fatalf("LoadWith() error = %v, want unknown APP_ENV", err)
	}
}

func TestLoadNormalizesKnownEnvironment(t *testing.T) {
	cfg, err := LoadWith(environmentReader(map[string]string{"APP_ENV": "  TeSt  "}))
	if err != nil {
		t.Fatalf("LoadWith() error = %v", err)
	}
	if cfg.Environment != "test" {
		t.Fatalf("Environment = %q, want test", cfg.Environment)
	}
}

func TestLoadProductionRequiresPersistentMySQLAndWechatIdentity(t *testing.T) {
	for _, key := range []string{"MYSQL_DSN", "WECHAT_MINI_APP_ID"} {
		t.Run(key, func(t *testing.T) {
			environment := validProductionEnvironment()
			environment[key] = ""
			if _, err := LoadWith(environmentReader(environment)); err == nil {
				t.Fatalf("LoadWith() accepted missing %s", key)
			}
		})
	}

	environment := validProductionEnvironment()
	environment["WECHAT_MINI_APP_ID"] = "change-me"
	if _, err := LoadWith(environmentReader(environment)); err == nil {
		t.Fatal("LoadWith() accepted placeholder WECHAT_MINI_APP_ID")
	}
}

func TestLoadProductionRejectsMalformedMySQLDSNWithoutExposingIt(t *testing.T) {
	environment := validProductionEnvironment()
	environment["MYSQL_DSN"] = "configured-password-that-must-not-leak"
	_, err := LoadWith(environmentReader(environment))
	if err == nil {
		t.Fatal("LoadWith() accepted malformed MYSQL_DSN")
	}
	if strings.Contains(err.Error(), environment["MYSQL_DSN"]) {
		t.Fatalf("LoadWith() exposed MYSQL_DSN in error: %v", err)
	}
}

func TestLoadMigratesLegacyDownloadSecretWithoutDualRuntimeReads(t *testing.T) {
	legacyValue := strongTestValue("legacy")
	canonicalValue := strongTestValue("canonical")

	legacyOnly := validProductionEnvironment()
	delete(legacyOnly, "DOWNLOAD_TOKEN_SECRET")
	legacyOnly["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = legacyValue
	var warnings []string
	cfg, err := LoadWithOptions(environmentReader(legacyOnly), LoadOptions{Warn: func(message string) {
		warnings = append(warnings, message)
	}})
	if err != nil {
		t.Fatalf("LoadWithOptions() legacy-only error = %v", err)
	}
	if cfg.Download.TokenSecret != legacyValue {
		t.Fatal("legacy download secret was not mapped to the canonical field")
	}
	if _, hasLegacyField := reflect.TypeOf(cfg.Download).FieldByName("FallbackTokenSecret"); hasLegacyField {
		t.Fatal("business config exposes a legacy download secret field")
	}
	joinedWarnings := strings.Join(warnings, "\n")
	if !strings.Contains(joinedWarnings, "DOWNLOAD_FALLBACK_TOKEN_SECRET") || strings.Contains(joinedWarnings, legacyValue) {
		t.Fatalf("deprecation warning missing field name or exposed value: %q", joinedWarnings)
	}

	same := validProductionEnvironment()
	same["DOWNLOAD_TOKEN_SECRET"] = legacyValue
	same["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = legacyValue
	cfg, err = LoadWith(environmentReader(same))
	if err != nil {
		t.Fatalf("LoadWith() equal canonical/legacy error = %v", err)
	}
	if cfg.Download.TokenSecret != legacyValue {
		t.Fatal("equal canonical/legacy values did not produce canonical config")
	}

	conflict := validProductionEnvironment()
	conflict["DOWNLOAD_TOKEN_SECRET"] = canonicalValue
	conflict["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = legacyValue
	_, err = LoadWith(environmentReader(conflict))
	if err == nil {
		t.Fatal("LoadWith() accepted conflicting canonical/legacy download secrets")
	}
	if strings.Contains(err.Error(), canonicalValue) || strings.Contains(err.Error(), legacyValue) {
		t.Fatalf("LoadWith() exposed conflicting download secret: %v", err)
	}
}

func TestLoadErrorsAndDeprecationWarningsNeverExposeConfiguredValues(t *testing.T) {
	configured := strongTestValue("configured")
	environment := validProductionEnvironment()
	environment["MYSQL_DSN"] = "user:" + configured + "@tcp(db:3306)/watermark"
	delete(environment, "DOWNLOAD_TOKEN_SECRET")
	environment["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = configured
	var warnings []string
	_, err := LoadWithOptions(environmentReader(environment), LoadOptions{Warn: func(message string) {
		warnings = append(warnings, message)
	}})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if strings.Contains(strings.Join(warnings, "\n"), configured) {
		t.Fatal("deprecation warning exposed configured value")
	}
}

func TestConfigFormattingAndSummaryNeverExposeConfiguredValues(t *testing.T) {
	environment := validProductionEnvironment()
	environment["WEIBO_COOKIE"] = strongTestValue("weibo-cookie")
	environment["XIGUA_COOKIE"] = strongTestValue("xigua-cookie")
	environment["SOHU_API_KEY"] = strongTestValue("sohu-token")
	musicConfig, err := json.Marshal(map[string]string{"providerCredential": strongTestValue("music-config")})
	if err != nil {
		t.Fatalf("marshal opaque music config: %v", err)
	}
	environment["UNIVERSAL_PARSER_MUSICDL_CONFIG_JSON"] = string(musicConfig)
	cfg, err := LoadWith(environmentReader(environment))
	if err != nil {
		t.Fatalf("LoadWith() error = %v", err)
	}

	formatted := fmt.Sprintf("%v|%+v|%#v", cfg, cfg, cfg)
	for _, key := range []string{
		"MYSQL_DSN", "ADMIN_PASSWORD", "ADMIN_SESSION_SECRET", "DOWNLOAD_TOKEN_SECRET",
		"WECHAT_MINI_APP_ID", "WECHAT_MINI_APP_SECRET", "APP_CLIENT_SIGNATURE_KEY",
		"WEIBO_COOKIE", "XIGUA_COOKIE", "SOHU_API_KEY", "UNIVERSAL_PARSER_MUSICDL_CONFIG_JSON",
	} {
		value := environment[key]
		if value != "" && strings.Contains(formatted, value) {
			t.Fatalf("Config formatting exposed %s: %s", key, formatted)
		}
	}
	summary := cfg.Summary()
	if !summary.MySQLConfigured || !summary.DownloadTokenConfigured || !summary.WeiboCookieConfigured || !summary.XiguaCookieConfigured || !summary.SohuTokenConfigured || !summary.MusicConfigConfigured {
		t.Fatalf("Config summary omitted configured-presence flags: %#v", summary)
	}
	if _, err := json.Marshal(cfg); err == nil {
		t.Fatal("Config allowed direct JSON serialization")
	} else if strings.Contains(err.Error(), environment["DOWNLOAD_TOKEN_SECRET"]) {
		t.Fatalf("Config serialization error exposed a secret: %v", err)
	}
}

func TestLoadProductionValidatesOptionalLegacyAESKey(t *testing.T) {
	for _, value := range []string{"", "short", "placeholder"} {
		environment := validProductionEnvironment()
		environment["APP_CLIENT_SIGNATURE_REQUIRED"] = "true"
		environment["APP_CLIENT_SIGNATURE_KEY"] = value
		if _, err := LoadWith(environmentReader(environment)); err == nil {
			t.Fatalf("LoadWith() accepted invalid APP_CLIENT_SIGNATURE_KEY %q", value)
		}
	}

	environment := validProductionEnvironment()
	environment["APP_CLIENT_SIGNATURE_REQUIRED"] = "true"
	environment["APP_CLIENT_SIGNATURE_KEY"] = strings.Repeat("K9", 8)
	if _, err := LoadWith(environmentReader(environment)); err != nil {
		t.Fatalf("LoadWith() rejected valid AES key: %v", err)
	}
}

func TestLoadSingleNodeDefaultsAndParserCookies(t *testing.T) {
	cfg, err := LoadWith(environmentReader(map[string]string{
		"APP_ENV":      "test",
		"WEIBO_COOKIE": "  weibo-test-cookie  ",
		"XIGUA_COOKIE": "  xigua-test-cookie  ",
	}))
	if err != nil {
		t.Fatalf("LoadWith() error = %v", err)
	}
	if cfg.HTTP.Port != "5001" {
		t.Fatalf("HTTP.Port = %q, want 5001", cfg.HTTP.Port)
	}
	if cfg.Baseline.Concurrency != 3 {
		t.Fatalf("Baseline.Concurrency = %d, want 3", cfg.Baseline.Concurrency)
	}
	if cfg.Parser.WeiboCookie != "weibo-test-cookie" || cfg.Parser.XiguaCookie != "xigua-test-cookie" {
		t.Fatal("parser cookies were not trimmed into typed config")
	}
}

func TestLoadRunnerConfigAtEnvironmentBoundary(t *testing.T) {
	environment := map[string]string{
		"APP_ENV":                                  "test",
		"PARSER_ENGINE":                            "universal",
		"PARSER_FALLBACK_ENABLED":                  "true",
		"YT_DLP_BINARY":                            " /opt/tools/yt-dlp ",
		"YT_DLP_TIMEOUT_SECONDS":                   "31",
		"UNIVERSAL_PARSER_PYTHON_BIN":              " /opt/python/bin/python3 ",
		"UNIVERSAL_PARSER_BRIDGE_SCRIPT":           " /opt/bridge/bridge.py ",
		"UNIVERSAL_PARSER_VIDEODL_PATH":            " /opt/sources/videodl ",
		"UNIVERSAL_PARSER_MUSICDL_PATH":            " /opt/sources/musicdl ",
		"UNIVERSAL_PARSER_WORK_DIR":                " /var/lib/watermark-go/universal ",
		"UNIVERSAL_PARSER_TIMEOUT_SECONDS":         "61",
		"UNIVERSAL_PARSER_MUSICDL_TIMEOUT_SECONDS": "16",
		"UNIVERSAL_PARSER_MUSICDL_ITEM_LIMIT":      "6",
	}
	cfg, err := LoadWith(environmentReader(environment))
	if err != nil {
		t.Fatalf("LoadWith() error = %v", err)
	}

	if cfg.Runner.Engine != "universal" || !cfg.Runner.FallbackEnabled {
		t.Fatalf("Runner engine/fallback = %q/%t", cfg.Runner.Engine, cfg.Runner.FallbackEnabled)
	}
	if cfg.Runner.YTDLP.Binary != "/opt/tools/yt-dlp" || cfg.Runner.YTDLP.Timeout != 31*time.Second {
		t.Fatalf("YTDLP config = %#v", cfg.Runner.YTDLP)
	}
	wantUniversal := UniversalRunnerConfig{
		PythonBinary:   "/opt/python/bin/python3",
		BridgeScript:   "/opt/bridge/bridge.py",
		VideoSource:    "/opt/sources/videodl",
		MusicSource:    "/opt/sources/musicdl",
		WorkDir:        "/var/lib/watermark-go/universal",
		VideoTimeout:   61 * time.Second,
		MusicTimeout:   16 * time.Second,
		MusicItemLimit: 6,
	}
	if !reflect.DeepEqual(cfg.Runner.Universal, wantUniversal) {
		t.Fatalf("Universal config = %#v, want %#v", cfg.Runner.Universal, wantUniversal)
	}
}

func TestLoadRunnerConfigDefaultsArePinnedAndSingleNodeSafe(t *testing.T) {
	cfg, err := LoadWith(environmentReader(map[string]string{"APP_ENV": "test"}))
	if err != nil {
		t.Fatalf("LoadWith() error = %v", err)
	}
	if cfg.Runner.Engine != "native" || cfg.Runner.FallbackEnabled {
		t.Fatalf("Runner defaults engine/fallback = %q/%t", cfg.Runner.Engine, cfg.Runner.FallbackEnabled)
	}
	if cfg.Runner.YTDLP.Binary != "/usr/local/bin/yt-dlp" || cfg.Runner.YTDLP.Timeout != 30*time.Second {
		t.Fatalf("YTDLP defaults = %#v", cfg.Runner.YTDLP)
	}
	wantUniversal := UniversalRunnerConfig{
		PythonBinary:   "/usr/local/bin/python3",
		BridgeScript:   "/app/bridges/universal/python/bridge.py",
		VideoSource:    "/app/third_party/CharlesPikachu/videodl",
		MusicSource:    "/app/third_party/CharlesPikachu/musicdl",
		WorkDir:        "/app/cache/universal-parser",
		VideoTimeout:   60 * time.Second,
		MusicTimeout:   15 * time.Second,
		MusicItemLimit: 5,
	}
	if !reflect.DeepEqual(cfg.Runner.Universal, wantUniversal) {
		t.Fatalf("Universal defaults = %#v, want %#v", cfg.Runner.Universal, wantUniversal)
	}
}

func TestLoadRejectsInvalidRunnerConfigWithoutEchoingValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "PARSER_ENGINE", value: "automatic-secret-marker"},
		{key: "PARSER_FALLBACK_ENABLED", value: "sometimes-secret-marker"},
		{key: "YT_DLP_TIMEOUT_SECONDS", value: "999-secret-marker"},
		{key: "UNIVERSAL_PARSER_TIMEOUT_SECONDS", value: "0-secret-marker"},
		{key: "UNIVERSAL_PARSER_MUSICDL_TIMEOUT_SECONDS", value: "999-secret-marker"},
		{key: "UNIVERSAL_PARSER_MUSICDL_ITEM_LIMIT", value: "99-secret-marker"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			environment := map[string]string{"APP_ENV": "test", test.key: test.value}
			_, err := LoadWith(environmentReader(environment))
			if err == nil {
				t.Fatalf("LoadWith() accepted invalid %s", test.key)
			}
			if strings.Contains(err.Error(), test.value) {
				t.Fatalf("LoadWith() exposed invalid %s value: %v", test.key, err)
			}
		})
	}
}

func TestLoadKeepsMusicAndSohuCredentialsOpaque(t *testing.T) {
	musicConfig := `{"provider":{"credential":"opaque-music-material"}}`
	sohuMaterial := strongTestValue("opaque-sohu")
	cfg, err := LoadWith(environmentReader(map[string]string{
		"APP_ENV":                              "test",
		"UNIVERSAL_PARSER_MUSICDL_CONFIG_JSON": musicConfig,
		"SOHU_API_KEY":                         sohuMaterial,
	}))
	if err != nil {
		t.Fatalf("LoadWith() error = %v", err)
	}
	if !cfg.Runner.Universal.MusicConfig.Configured() || !cfg.Parser.SohuAPIToken.Configured() {
		t.Fatal("opaque runner/parser credentials were not marked configured")
	}
	formatted := fmt.Sprintf("%v|%+v|%#v|%v|%+v|%#v|%+v|%#v", cfg.Runner.Universal.MusicConfig, cfg.Runner.Universal.MusicConfig, cfg.Runner.Universal.MusicConfig, cfg.Parser.SohuAPIToken, cfg.Parser.SohuAPIToken, cfg.Parser.SohuAPIToken, cfg, cfg)
	if strings.Contains(formatted, musicConfig) || strings.Contains(formatted, sohuMaterial) || strings.Contains(formatted, "opaque-") {
		t.Fatalf("opaque config formatting exposed a credential: %s", formatted)
	}
	for name, value := range map[string]SensitiveValue{
		"music": cfg.Runner.Universal.MusicConfig,
		"sohu":  cfg.Parser.SohuAPIToken,
	} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatalf("%s sensitive value allowed direct JSON serialization", name)
		} else if strings.Contains(err.Error(), "opaque-") {
			t.Fatalf("%s serialization error exposed a credential: %v", name, err)
		}
	}

	var resolvedMusic, resolvedSohu string
	if err := cfg.Runner.Universal.MusicConfig.Use(func(value string) error {
		resolvedMusic = value
		return nil
	}); err != nil {
		t.Fatalf("use music config: %v", err)
	}
	if err := cfg.Parser.SohuAPIToken.Use(func(value string) error {
		resolvedSohu = value
		return nil
	}); err != nil {
		t.Fatalf("use Sohu token: %v", err)
	}
	if resolvedMusic != musicConfig || resolvedSohu != sohuMaterial {
		t.Fatal("opaque values were not available to an explicit consumer")
	}
}

func TestLoadRejectsInvalidSensitiveMusicConfigWithoutEchoingIt(t *testing.T) {
	configured := `{"token":"opaque-music-secret"`
	_, err := LoadWith(environmentReader(map[string]string{
		"APP_ENV":                              "test",
		"UNIVERSAL_PARSER_MUSICDL_CONFIG_JSON": configured,
	}))
	if err == nil {
		t.Fatal("LoadWith() accepted invalid sensitive music config JSON")
	}
	if strings.Contains(err.Error(), configured) || strings.Contains(err.Error(), "opaque-music-secret") {
		t.Fatalf("LoadWith() exposed invalid sensitive music config: %v", err)
	}
}

func TestLoadRejectsInvalidTypedValues(t *testing.T) {
	tests := []map[string]string{
		{"APP_ENV": "test", "PORT": "not-a-port"},
		{"APP_ENV": "test", "REDIS_DB": "-1"},
		{"APP_ENV": "test", "TASK_WORKER_CONCURRENCY": "0"},
		{"APP_ENV": "test", "APP_CLIENT_SIGNATURE_REQUIRED": "sometimes"},
	}
	for _, environment := range tests {
		if _, err := LoadWith(environmentReader(environment)); err == nil {
			t.Fatalf("LoadWith() accepted invalid environment: %#v", environment)
		}
	}
}

func validProductionEnvironment() map[string]string {
	return map[string]string{
		"APP_ENV":                "production",
		"MYSQL_DSN":              "user:password@tcp(db:3306)/watermark",
		"ADMIN_PASSWORD":         strongTestValue("admin"),
		"ADMIN_SESSION_SECRET":   strongTestValue("session"),
		"DOWNLOAD_TOKEN_SECRET":  strongTestValue("download"),
		"WECHAT_MINI_APP_ID":     "wx-production-id",
		"WECHAT_MINI_APP_SECRET": strongTestValue("wechat"),
	}
}

func strongTestValue(label string) string {
	return label + "-A7B8C9D0E1F2G3H4I5J6K7L8M9N0P1Q2"
}

func environmentReader(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func assertErrorOmitsEnvironmentValues(t *testing.T, err error, environment map[string]string) {
	t.Helper()
	if err == nil {
		return
	}
	for _, key := range []string{
		"MYSQL_DSN",
		"ADMIN_PASSWORD",
		"ADMIN_SESSION_SECRET",
		"DOWNLOAD_TOKEN_SECRET",
		"DOWNLOAD_FALLBACK_TOKEN_SECRET",
		"WECHAT_MINI_APP_SECRET",
		"APP_CLIENT_SIGNATURE_KEY",
		"WEIBO_COOKIE",
		"XIGUA_COOKIE",
	} {
		value := environment[key]
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("error exposed %s value: %v", key, err)
		}
	}
}
