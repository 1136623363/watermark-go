package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/1136623363/watermark-go/internal/app"
	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/server"
)

type applicationRunner interface {
	Run(context.Context) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], config.Load, server.RunMigrations, func(cfg config.Config) (applicationRunner, error) {
		return app.New(cfg)
	}); err != nil {
		log.Printf("watermark-go stopped: %v", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	loadConfig func() (config.Config, error),
	runMigrations func(context.Context, config.Config) error,
	newApplication func(config.Config) (applicationRunner, error),
) error {
	if ctx == nil {
		return fmt.Errorf("nil process context")
	}
	if loadConfig == nil || runMigrations == nil || newApplication == nil {
		return fmt.Errorf("process dependencies are required")
	}
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if len(args) != 0 {
		if len(args) != 1 || args[0] != "migrate" {
			return fmt.Errorf("unknown command")
		}
		if err := runMigrations(ctx, cfg); err != nil {
			return fmt.Errorf("run migration: %w", err)
		}
		return nil
	}
	application, err := newApplication(cfg)
	if err != nil {
		return fmt.Errorf("construct application: %w", err)
	}
	if application == nil {
		return fmt.Errorf("construct application: nil runner")
	}
	return application.Run(ctx)
}
