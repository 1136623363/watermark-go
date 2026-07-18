package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/1136623363/watermark-go/internal/app"
	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/store"
)

type applicationRunner interface {
	Run(context.Context) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], processDeps{
		LoadConfig:         config.Load,
		LoadDataGateConfig: config.LoadDataGate,
		RunDataGate:        defaultRunDataGate,
		RunHealthcheck:     defaultAPIHealthcheck,
		VerifyServeGate:    defaultVerifyServeGate,
		NewApplication: func(cfg config.Config) (applicationRunner, error) {
			return app.New(cfg)
		},
	}); err != nil {
		log.Printf("watermark-go stopped: %v", err)
		os.Exit(1)
	}
}

type processDeps struct {
	LoadConfig         func() (config.Config, error)
	LoadDataGateConfig func() (config.DataGateConfig, error)
	RunDataGate        func(context.Context, config.DataGateConfig) error
	RunHealthcheck     func(context.Context, config.Config) error
	VerifyServeGate    func(context.Context, config.Config) error
	NewApplication     func(config.Config) (applicationRunner, error)
}

func run(ctx context.Context, args []string, deps processDeps) error {
	if ctx == nil {
		return fmt.Errorf("nil process context")
	}
	if deps.LoadConfig == nil || deps.LoadDataGateConfig == nil || deps.RunDataGate == nil ||
		deps.RunHealthcheck == nil || deps.VerifyServeGate == nil || deps.NewApplication == nil {
		return fmt.Errorf("process dependencies are required")
	}
	if len(args) == 0 {
		return fmt.Errorf("explicit command is required")
	}
	switch args[0] {
	case "serve":
		if len(args) != 1 {
			return fmt.Errorf("serve does not accept extra arguments")
		}
		cfg, err := deps.LoadConfig()
		if err != nil {
			return fmt.Errorf("load configuration: %w", err)
		}
		if err := deps.VerifyServeGate(ctx, cfg); err != nil {
			return fmt.Errorf("verify gate receipt: %w", err)
		}
		application, err := deps.NewApplication(cfg)
		if err != nil {
			return fmt.Errorf("construct application: %w", err)
		}
		if application == nil {
			return fmt.Errorf("construct application: nil runner")
		}
		return application.Run(ctx)
	case "data-gate":
		if len(args) != 1 {
			return fmt.Errorf("data-gate does not accept extra arguments")
		}
		cfg, err := deps.LoadDataGateConfig()
		if err != nil {
			return fmt.Errorf("load data-gate configuration: %w", err)
		}
		return deps.RunDataGate(ctx, cfg)
	case "healthcheck":
		if len(args) != 2 || args[1] != "api" {
			return fmt.Errorf("unknown healthcheck target")
		}
		cfg, err := deps.LoadConfig()
		if err != nil {
			return fmt.Errorf("load configuration: %w", err)
		}
		return deps.RunHealthcheck(ctx, cfg)
	default:
		return fmt.Errorf("unknown command")
	}
}

func defaultRunDataGate(ctx context.Context, cfg config.DataGateConfig) error {
	if cfg.Mode == store.GateModeRevalidate {
		return nil
	}
	db, err := store.OpenMySQL(ctx, store.MySQLConfig{DSN: cfg.MySQLDSN})
	if err != nil {
		return err
	}
	defer db.Close()
	migrations, err := store.LoadMigrationsDir(defaultMigrationDir())
	if err != nil {
		return err
	}
	return store.ApplyMigrations(ctx, store.SQLExecutor{DB: db}, migrations)
}

func defaultAPIHealthcheck(context.Context, config.Config) error {
	return nil
}

func defaultVerifyServeGate(_ context.Context, cfg config.Config) error {
	if cfg.Gate.ReceiptPath == "" {
		return nil
	}
	_, err := store.LoadGateReceipt(cfg.Gate.ReceiptPath)
	return err
}

func defaultMigrationDir() string {
	if configured := os.Getenv("WATERMARK_MIGRATIONS_DIR"); configured != "" {
		return configured
	}
	wd, err := os.Getwd()
	if err != nil {
		return "migrations"
	}
	for current := wd; ; current = filepath.Dir(current) {
		candidate := filepath.Join(current, "migrations")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Join(wd, "migrations")
		}
	}
}
