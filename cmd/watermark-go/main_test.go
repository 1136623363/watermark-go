package main

import (
	"context"
	"errors"
	"testing"

	"github.com/1136623363/watermark-go/internal/config"
)

func TestRunRejectsConfigurationBeforeConstructingApplication(t *testing.T) {
	configErr := errors.New("configuration rejected")
	constructed := false
	err := run(
		context.Background(),
		nil,
		func() (config.Config, error) { return config.Config{}, configErr },
		func(context.Context, config.Config) error {
			t.Fatal("migration ran after configuration failure")
			return nil
		},
		func(config.Config) (applicationRunner, error) {
			constructed = true
			return runnerStub{}, nil
		},
	)
	if !errors.Is(err, configErr) {
		t.Fatalf("run() error = %v, want configuration error", err)
	}
	if constructed {
		t.Fatal("run() constructed an application after configuration failure")
	}
}

func TestRunInvokesApplicationLifecycle(t *testing.T) {
	runCalled := false
	err := run(
		context.Background(),
		nil,
		func() (config.Config, error) {
			return config.Config{Environment: config.EnvironmentTest}, nil
		},
		func(context.Context, config.Config) error {
			t.Fatal("migration ran for normal server mode")
			return nil
		},
		func(cfg config.Config) (applicationRunner, error) {
			if cfg.Environment != config.EnvironmentTest {
				t.Fatalf("application config environment = %q", cfg.Environment)
			}
			return runnerStub{run: func(context.Context) error {
				runCalled = true
				return nil
			}}, nil
		},
	)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !runCalled {
		t.Fatal("run() did not invoke application lifecycle")
	}
}

func TestRunExecutesMigrationAsExplicitOneShotWithProcessContext(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "process-value"))
	cancel()
	migrationCalled := false
	applicationConstructed := false
	err := run(
		ctx,
		[]string{"migrate"},
		func() (config.Config, error) {
			return config.Config{Environment: config.EnvironmentTest}, nil
		},
		func(migrationCtx context.Context, cfg config.Config) error {
			migrationCalled = true
			if migrationCtx != ctx || migrationCtx.Value(contextKey{}) != "process-value" {
				t.Fatal("migration did not receive the process context")
			}
			return migrationCtx.Err()
		},
		func(config.Config) (applicationRunner, error) {
			applicationConstructed = true
			return runnerStub{}, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want process cancellation", err)
	}
	if !migrationCalled || applicationConstructed {
		t.Fatalf("migrationCalled/applicationConstructed = %t/%t", migrationCalled, applicationConstructed)
	}
}

func TestRunReturnsMigrationErrorWithoutConstructingApplication(t *testing.T) {
	migrationErr := errors.New("migration failed")
	applicationConstructed := false
	err := run(
		context.Background(),
		[]string{"migrate"},
		func() (config.Config, error) {
			return config.Config{Environment: config.EnvironmentTest}, nil
		},
		func(context.Context, config.Config) error { return migrationErr },
		func(config.Config) (applicationRunner, error) {
			applicationConstructed = true
			return runnerStub{}, nil
		},
	)
	if !errors.Is(err, migrationErr) {
		t.Fatalf("run() error = %v, want migration error", err)
	}
	if applicationConstructed {
		t.Fatal("run() constructed the server application after one-shot migration")
	}
}

func TestRunCompletesSuccessfulMigrationDeterministicallyWithoutStartingServer(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		migrationCalls := 0
		applicationConstructed := false
		err := run(
			context.Background(),
			[]string{"migrate"},
			func() (config.Config, error) {
				return config.Config{Environment: config.EnvironmentTest}, nil
			},
			func(context.Context, config.Config) error {
				migrationCalls++
				return nil
			},
			func(config.Config) (applicationRunner, error) {
				applicationConstructed = true
				return runnerStub{}, nil
			},
		)
		if err != nil || migrationCalls != 1 || applicationConstructed {
			t.Fatalf("attempt %d: err/calls/application = %v/%d/%t", attempt, err, migrationCalls, applicationConstructed)
		}
	}
}

type runnerStub struct {
	run func(context.Context) error
}

func (runner runnerStub) Run(ctx context.Context) error {
	if runner.run == nil {
		return nil
	}
	return runner.run(ctx)
}
