package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	EnvironmentDevelopment = "development"
	EnvironmentTest        = "test"
	EnvironmentProduction  = "production"
)

type Config struct {
	Environment string
	HTTP        HTTPConfig
	MySQL       MySQLConfig
	Redis       RedisConfig
	Parser      ParserConfig
	Runner      RunnerConfig
	Tasks       TaskConfig
	Download    DownloadConfig
	Security    SecurityConfig
	Baseline    BaselineConfig
}

type Summary struct {
	Environment                   string
	HTTPPort                      string
	MySQLConfigured               bool
	RedisConfigured               bool
	WeiboCookieConfigured         bool
	XiguaCookieConfigured         bool
	SohuTokenConfigured           bool
	MusicConfigConfigured         bool
	DownloadTokenConfigured       bool
	AdminPasswordConfigured       bool
	AdminSessionConfigured        bool
	WechatMiniAppIDConfigured     bool
	WechatMiniAppSecretConfigured bool
	ClientSignatureRequired       bool
	ClientSignatureKeyConfigured  bool
	RunnerEngine                  string
	RunnerFallbackEnabled         bool
}

type HTTPConfig struct {
	Port string
}

type MySQLConfig struct {
	DSN string
}

type RedisConfig struct {
	Addr     string
	Username string
	Password string
	DB       int
}

type ParserConfig struct {
	WeiboCookie  string
	XiguaCookie  string
	SohuAPIToken SensitiveValue
}

type RunnerConfig struct {
	Engine          string
	FallbackEnabled bool
	YTDLP           YTDLPRunnerConfig
	Universal       UniversalRunnerConfig
}

type YTDLPRunnerConfig struct {
	Binary  string
	Timeout time.Duration
}

type UniversalRunnerConfig struct {
	PythonBinary   string
	BridgeScript   string
	VideoSource    string
	MusicSource    string
	WorkDir        string
	VideoTimeout   time.Duration
	MusicTimeout   time.Duration
	MusicItemLimit int
	MusicConfig    SensitiveValue
}

type SensitiveValue struct {
	value string
}

type TaskConfig struct {
	WorkerConcurrency int
}

type DownloadConfig struct {
	TokenSecret string
}

type SecurityConfig struct {
	AdminPassword              string
	AdminSessionSecret         string
	WechatMiniAppID            string
	WechatMiniAppSecret        string
	AppClientSignatureRequired bool
	AppClientSignatureKey      string
}

type BaselineConfig struct {
	Concurrency int
}

type LoadOptions struct {
	Warn func(message string)
}

func (value SensitiveValue) Configured() bool {
	return value.value != ""
}

func (value SensitiveValue) Use(consumer func(string) error) error {
	if consumer == nil {
		return errors.New("sensitive value consumer is required")
	}
	return consumer(value.value)
}

func (value SensitiveValue) String() string {
	if value.Configured() {
		return "[configured]"
	}
	return "[not-configured]"
}

func (value SensitiveValue) GoString() string {
	return "config.SensitiveValue(" + value.String() + ")"
}

func (value SensitiveValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("sensitive value cannot be serialized")
}

func (value SensitiveValue) MarshalText() ([]byte, error) {
	return nil, errors.New("sensitive value cannot be serialized")
}

type getenvReader func(key string) string

func (cfg Config) Summary() Summary {
	return Summary{
		Environment:                   cfg.Environment,
		HTTPPort:                      cfg.HTTP.Port,
		MySQLConfigured:               cfg.MySQL.DSN != "",
		RedisConfigured:               cfg.Redis.Addr != "",
		WeiboCookieConfigured:         cfg.Parser.WeiboCookie != "",
		XiguaCookieConfigured:         cfg.Parser.XiguaCookie != "",
		SohuTokenConfigured:           cfg.Parser.SohuAPIToken.Configured(),
		MusicConfigConfigured:         cfg.Runner.Universal.MusicConfig.Configured(),
		DownloadTokenConfigured:       cfg.Download.TokenSecret != "",
		AdminPasswordConfigured:       cfg.Security.AdminPassword != "",
		AdminSessionConfigured:        cfg.Security.AdminSessionSecret != "",
		WechatMiniAppIDConfigured:     cfg.Security.WechatMiniAppID != "",
		WechatMiniAppSecretConfigured: cfg.Security.WechatMiniAppSecret != "",
		ClientSignatureRequired:       cfg.Security.AppClientSignatureRequired,
		ClientSignatureKeyConfigured:  cfg.Security.AppClientSignatureKey != "",
		RunnerEngine:                  cfg.Runner.Engine,
		RunnerFallbackEnabled:         cfg.Runner.FallbackEnabled,
	}
}

func (cfg Config) String() string {
	return fmt.Sprintf("config.Config%+v", cfg.Summary())
}

func (cfg Config) GoString() string {
	return cfg.String()
}

func (cfg Config) MarshalJSON() ([]byte, error) {
	return nil, errors.New("config cannot be serialized; use Summary")
}

func Load() (Config, error) {
	return LoadWithOptions(os.Getenv, LoadOptions{Warn: func(message string) {
		log.Printf("configuration warning: %s", message)
	}})
}

func LoadWith(getenv func(string) string) (Config, error) {
	return LoadWithOptions(getenv, LoadOptions{})
}

func LoadWithOptions(getenv func(string) string, options LoadOptions) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("environment reader is required")
	}
	read := getenvReader(getenv)

	environment, err := loadEnvironment(read)
	if err != nil {
		return Config{}, err
	}
	port, err := loadInteger(read, "PORT", 5001, 1, 65535)
	if err != nil {
		return Config{}, err
	}
	redisDB, err := loadInteger(read, "REDIS_DB", 0, 0, 15)
	if err != nil {
		return Config{}, err
	}
	workerConcurrency, err := loadInteger(read, "TASK_WORKER_CONCURRENCY", 3, 1, 64)
	if err != nil {
		return Config{}, err
	}
	baselineConcurrency, err := loadInteger(read, "BASELINE_CONCURRENCY", 3, 1, 64)
	if err != nil {
		return Config{}, err
	}
	signatureRequired, err := loadBoolean(read, "APP_CLIENT_SIGNATURE_REQUIRED", false)
	if err != nil {
		return Config{}, err
	}
	runner, err := loadRunnerConfig(read)
	if err != nil {
		return Config{}, err
	}
	musicConfig := read.trimmed("UNIVERSAL_PARSER_MUSICDL_CONFIG_JSON")
	if musicConfig != "" && !json.Valid([]byte(musicConfig)) {
		return Config{}, errors.New("invalid UNIVERSAL_PARSER_MUSICDL_CONFIG_JSON")
	}
	runner.Universal.MusicConfig = SensitiveValue{value: musicConfig}

	downloadSecret, err := loadDownloadSecret(read, options)
	if err != nil {
		return Config{}, err
	}
	sohuAPIToken := SensitiveValue{value: read.trimmed("SOHU_API_KEY")}
	cfg := Config{
		Environment: environment,
		HTTP: HTTPConfig{
			Port: strconv.Itoa(port),
		},
		MySQL: MySQLConfig{
			DSN: read.trimmed("MYSQL_DSN"),
		},
		Redis: RedisConfig{
			Addr:     read.trimmed("REDIS_ADDR"),
			Username: read.trimmed("REDIS_USERNAME"),
			Password: read.trimmed("REDIS_PASSWORD"),
			DB:       redisDB,
		},
		Parser: ParserConfig{
			WeiboCookie:  read.trimmed("WEIBO_COOKIE"),
			XiguaCookie:  read.trimmed("XIGUA_COOKIE"),
			SohuAPIToken: sohuAPIToken,
		},
		Runner: runner,
		Tasks: TaskConfig{
			WorkerConcurrency: workerConcurrency,
		},
		Download: DownloadConfig{
			TokenSecret: downloadSecret,
		},
		Security: SecurityConfig{
			AdminPassword:              read.trimmed("ADMIN_PASSWORD"),
			AdminSessionSecret:         read.trimmed("ADMIN_SESSION_SECRET"),
			WechatMiniAppID:            read.trimmed("WECHAT_MINI_APP_ID"),
			WechatMiniAppSecret:        read.trimmed("WECHAT_MINI_APP_SECRET"),
			AppClientSignatureRequired: signatureRequired,
			AppClientSignatureKey:      read.trimmed("APP_CLIENT_SIGNATURE_KEY"),
		},
		Baseline: BaselineConfig{
			Concurrency: baselineConcurrency,
		},
	}

	if cfg.Environment == EnvironmentProduction {
		if err := validateProduction(cfg); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func loadRunnerConfig(read getenvReader) (RunnerConfig, error) {
	engine := strings.ToLower(read.trimmed("PARSER_ENGINE"))
	if engine == "" {
		engine = "native"
	}
	switch engine {
	case "native", "universal":
	default:
		return RunnerConfig{}, errors.New("invalid PARSER_ENGINE")
	}
	fallbackEnabled, err := loadBoolean(read, "PARSER_FALLBACK_ENABLED", false)
	if err != nil {
		return RunnerConfig{}, err
	}
	ytdlpTimeout, err := loadInteger(read, "YT_DLP_TIMEOUT_SECONDS", 30, 1, 120)
	if err != nil {
		return RunnerConfig{}, err
	}
	videoTimeout, err := loadInteger(read, "UNIVERSAL_PARSER_TIMEOUT_SECONDS", 60, 1, 120)
	if err != nil {
		return RunnerConfig{}, err
	}
	musicTimeout, err := loadInteger(read, "UNIVERSAL_PARSER_MUSICDL_TIMEOUT_SECONDS", 15, 1, 120)
	if err != nil {
		return RunnerConfig{}, err
	}
	musicItemLimit, err := loadInteger(read, "UNIVERSAL_PARSER_MUSICDL_ITEM_LIMIT", 5, 1, 20)
	if err != nil {
		return RunnerConfig{}, err
	}

	locations := []struct {
		key      string
		fallback string
	}{
		{key: "YT_DLP_BINARY", fallback: "/usr/local/bin/yt-dlp"},
		{key: "UNIVERSAL_PARSER_PYTHON_BIN", fallback: "/usr/local/bin/python3"},
		{key: "UNIVERSAL_PARSER_BRIDGE_SCRIPT", fallback: "/app/bridges/universal/python/bridge.py"},
		{key: "UNIVERSAL_PARSER_VIDEODL_PATH", fallback: "/app/third_party/CharlesPikachu/videodl"},
		{key: "UNIVERSAL_PARSER_MUSICDL_PATH", fallback: "/app/third_party/CharlesPikachu/musicdl"},
		{key: "UNIVERSAL_PARSER_WORK_DIR", fallback: "/app/cache/universal-parser"},
	}
	values := make([]string, len(locations))
	for index, location := range locations {
		value := read.trimmed(location.key)
		if value == "" {
			value = location.fallback
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return RunnerConfig{}, fmt.Errorf("invalid %s", location.key)
		}
		values[index] = value
	}

	return RunnerConfig{
		Engine:          engine,
		FallbackEnabled: fallbackEnabled,
		YTDLP: YTDLPRunnerConfig{
			Binary:  values[0],
			Timeout: time.Duration(ytdlpTimeout) * time.Second,
		},
		Universal: UniversalRunnerConfig{
			PythonBinary:   values[1],
			BridgeScript:   values[2],
			VideoSource:    values[3],
			MusicSource:    values[4],
			WorkDir:        values[5],
			VideoTimeout:   time.Duration(videoTimeout) * time.Second,
			MusicTimeout:   time.Duration(musicTimeout) * time.Second,
			MusicItemLimit: musicItemLimit,
		},
	}, nil
}

func loadEnvironment(read getenvReader) (string, error) {
	environment := strings.ToLower(read.trimmed("APP_ENV"))
	switch environment {
	case EnvironmentDevelopment, EnvironmentTest, EnvironmentProduction:
		return environment, nil
	default:
		return "", errors.New("unknown APP_ENV")
	}
}

func loadDownloadSecret(read getenvReader, options LoadOptions) (string, error) {
	canonical := read.trimmed("DOWNLOAD_TOKEN_SECRET")
	legacy := read.trimmed("DOWNLOAD_FALLBACK_TOKEN_SECRET")
	if canonical != "" && legacy != "" && canonical != legacy {
		return "", errors.New("conflicting download token secret variables")
	}
	if legacy != "" && options.Warn != nil {
		options.Warn("DOWNLOAD_FALLBACK_TOKEN_SECRET is deprecated; use DOWNLOAD_TOKEN_SECRET")
	}
	if canonical != "" {
		return canonical, nil
	}
	return legacy, nil
}

func loadInteger(read getenvReader, key string, fallback, minimum, maximum int) (int, error) {
	raw := read.trimmed(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return value, nil
}

func loadBoolean(read getenvReader, key string, fallback bool) (bool, error) {
	raw := strings.ToLower(read.trimmed(key))
	if raw == "" {
		return fallback, nil
	}
	switch raw {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s", key)
	}
}

func validateProduction(cfg Config) error {
	if err := validateMySQLDSN(cfg.MySQL.DSN); err != nil {
		return err
	}
	checks := []struct {
		name    string
		value   string
		minimum int
		aes     bool
	}{
		{name: "ADMIN_PASSWORD", value: cfg.Security.AdminPassword, minimum: 12},
		{name: "ADMIN_SESSION_SECRET", value: cfg.Security.AdminSessionSecret, minimum: 32},
		{name: "DOWNLOAD_TOKEN_SECRET", value: cfg.Download.TokenSecret, minimum: 32},
		{name: "WECHAT_MINI_APP_SECRET", value: cfg.Security.WechatMiniAppSecret, minimum: 16},
	}
	if cfg.Security.AppClientSignatureRequired {
		checks = append(checks, struct {
			name    string
			value   string
			minimum int
			aes     bool
		}{name: "APP_CLIENT_SIGNATURE_KEY", value: cfg.Security.AppClientSignatureKey, aes: true})
	}
	for _, check := range checks {
		if err := validateProductionSecret(check.name, check.value, check.minimum, check.aes); err != nil {
			return err
		}
	}
	if cfg.Security.WechatMiniAppID == "" || isObviousPlaceholder(cfg.Security.WechatMiniAppID) {
		return errors.New("invalid production identity: WECHAT_MINI_APP_ID")
	}
	return nil
}

func validateMySQLDSN(raw string) error {
	if raw == "" {
		return errors.New("invalid production storage: MYSQL_DSN is required")
	}
	parsed, err := mysql.ParseDSN(raw)
	if err != nil || strings.TrimSpace(parsed.DBName) == "" {
		return errors.New("invalid production storage: MYSQL_DSN")
	}
	return nil
}

func validateProductionSecret(name, value string, minimum int, aes bool) error {
	value = strings.TrimSpace(value)
	if value == "" || isObviousPlaceholder(value) {
		return fmt.Errorf("weak production secret: %s", name)
	}
	if aes {
		switch len(value) {
		case 16, 24, 32:
			return nil
		default:
			return fmt.Errorf("weak production secret: %s", name)
		}
	}
	if len(value) < minimum {
		return fmt.Errorf("weak production secret: %s", name)
	}
	return nil
}

func isObviousPlaceholder(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	markers := []string{
		"change-me", "change_me", "changeme", "example", "placeholder", "dummy",
		"invalid-for-test-only", "redacted", "sample", "admin123456", "password", "secret",
	}
	for _, marker := range markers {
		if value == marker {
			return true
		}
		for _, separator := range []string{"-", "_", ".", ":", "/"} {
			if strings.HasPrefix(value, marker+separator) {
				return true
			}
		}
	}
	return false
}

func (read getenvReader) trimmed(key string) string {
	return strings.TrimSpace(read(key))
}
