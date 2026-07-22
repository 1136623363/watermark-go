package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/store"
)

func TestRunRequiresExplicitCommand(t *testing.T) {
	err := run(context.Background(), nil, validDeps())
	if err == nil || !strings.Contains(err.Error(), "explicit command") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunRejectsConfigurationBeforeConstructingApplication(t *testing.T) {
	configErr := errors.New("configuration rejected")
	constructed := false
	deps := validDeps()
	deps.LoadConfig = func() (config.Config, error) { return config.Config{}, configErr }
	deps.NewApplication = func(config.Config) (applicationRunner, error) {
		constructed = true
		return runnerStub{}, nil
	}
	err := run(context.Background(), []string{"serve"}, deps)
	if !errors.Is(err, configErr) {
		t.Fatalf("run() error = %v, want configuration error", err)
	}
	if constructed {
		t.Fatal("run() constructed an application after configuration failure")
	}
}

func TestRunInvokesApplicationLifecycleForServe(t *testing.T) {
	runCalled := false
	deps := validDeps()
	deps.LoadConfig = func() (config.Config, error) {
		return config.Config{Environment: config.EnvironmentTest}, nil
	}
	deps.NewApplication = func(cfg config.Config) (applicationRunner, error) {
		if cfg.Environment != config.EnvironmentTest {
			t.Fatalf("application config environment = %q", cfg.Environment)
		}
		return runnerStub{run: func(context.Context) error {
			runCalled = true
			return nil
		}}, nil
	}
	if err := run(context.Background(), []string{"serve"}, deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !runCalled {
		t.Fatal("run() did not invoke application lifecycle")
	}
}

func TestDataGateNeverConstructsApplicationOrListener(t *testing.T) {
	var dataGateCalls atomic.Int32
	deps := validDeps()
	deps.LoadConfig = func() (config.Config, error) {
		t.Fatal("data-gate loaded full API config")
		return config.Config{}, nil
	}
	deps.LoadDataGateConfig = func() (config.DataGateConfig, error) {
		return config.DataGateConfig{Environment: config.EnvironmentTest, Mode: "revalidate", ReceiptPath: "/tmp/receipt.json"}, nil
	}
	deps.RunDataGate = func(ctx context.Context, cfg config.DataGateConfig) error {
		if ctx == nil || cfg.Mode != "revalidate" {
			t.Fatalf("unexpected data-gate request: %#v", cfg)
		}
		dataGateCalls.Add(1)
		return nil
	}
	deps.NewApplication = func(config.Config) (applicationRunner, error) {
		t.Fatal("data-gate constructed application")
		return runnerStub{}, nil
	}
	if err := run(context.Background(), []string{"data-gate"}, deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if dataGateCalls.Load() != 1 {
		t.Fatalf("data-gate calls = %d", dataGateCalls.Load())
	}
}

func TestDefaultRunDataGateRevalidateWritesBoundReceiptWithoutOpeningDB(t *testing.T) {
	path := t.TempDir() + "/receipt.json"
	cfg := config.DataGateConfig{
		Environment:       config.EnvironmentTest,
		Mode:              store.GateModeRevalidate,
		ReceiptPath:       path,
		Role:              "recovery",
		DataStage:         "shadow",
		ImageDigest:       "ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DeploymentRunID:   "deploy-123",
		GateAttemptID:     "gate-123",
		SchemaState:       "schema-012",
		TargetDBIdentity:  "mysql-shadow",
		RedisIdentity:     "redis-shadow",
		OutboxIdentity:    "outbox-shadow",
		InputSnapshotHash: "snapshot-hash",
		ConfigHash:        "config-hash",
	}

	if err := defaultRunDataGate(context.Background(), cfg); err != nil {
		t.Fatalf("defaultRunDataGate() error = %v", err)
	}
	receipt, err := store.LoadGateReceipt(path)
	if err != nil {
		t.Fatalf("load gate receipt: %v", err)
	}
	expect := store.GateExpectation{
		Role:              cfg.Role,
		DataStage:         cfg.DataStage,
		ImageDigest:       cfg.ImageDigest,
		DeploymentRunID:   cfg.DeploymentRunID,
		SchemaState:       cfg.SchemaState,
		TargetDBIdentity:  cfg.TargetDBIdentity,
		RedisIdentity:     cfg.RedisIdentity,
		OutboxIdentity:    cfg.OutboxIdentity,
		InputSnapshotHash: cfg.InputSnapshotHash,
		ConfigHash:        cfg.ConfigHash,
	}
	if err := receipt.Validate(expect, time.Now()); err != nil {
		t.Fatalf("receipt did not validate: %v", err)
	}
	if receipt.GateAttemptID != cfg.GateAttemptID {
		t.Fatalf("receipt attempt = %q, want %q", receipt.GateAttemptID, cfg.GateAttemptID)
	}
}

func TestServeRejectsStaleOrMismatchedGateReceiptBeforeStartingComponents(t *testing.T) {
	gateErr := errors.New("gate receipt mismatch")
	deps := validDeps()
	deps.VerifyServeGate = func(context.Context, config.Config) error { return gateErr }
	constructed := false
	deps.NewApplication = func(config.Config) (applicationRunner, error) {
		constructed = true
		return runnerStub{}, nil
	}
	err := run(context.Background(), []string{"serve"}, deps)
	if !errors.Is(err, gateErr) {
		t.Fatalf("run() error = %v, want gate error", err)
	}
	if constructed {
		t.Fatal("serve constructed application before gate receipt passed")
	}
}

func TestDefaultVerifyServeGateRejectsMismatchedReceiptIdentity(t *testing.T) {
	path := t.TempDir() + "/receipt.json"
	now := time.Now()
	receipt := store.GateReceipt{
		SchemaVersion:     store.GateReceiptSchemaVersion,
		Role:              "recovery",
		DataStage:         "shadow",
		ImageDigest:       "ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DeploymentRunID:   "deploy-123",
		GateAttemptID:     "gate-123",
		SchemaState:       "schema-012",
		TargetDBIdentity:  "mysql-shadow",
		RedisIdentity:     "redis-shadow",
		OutboxIdentity:    "outbox-shadow",
		InputSnapshotHash: "snapshot-hash",
		ConfigHash:        "config-hash",
		CompletedAt:       now,
		ExpiresAt:         now.Add(time.Hour),
		Passed:            true,
	}
	if err := store.WriteGateReceiptAtomic(context.Background(), path, receipt); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Gate: config.ServeGateConfig{
		ReceiptPath:       path,
		Role:              receipt.Role,
		DataStage:         "final",
		ImageDigest:       receipt.ImageDigest,
		DeploymentRunID:   receipt.DeploymentRunID,
		SchemaState:       receipt.SchemaState,
		TargetDBIdentity:  receipt.TargetDBIdentity,
		RedisIdentity:     receipt.RedisIdentity,
		OutboxIdentity:    receipt.OutboxIdentity,
		InputSnapshotHash: receipt.InputSnapshotHash,
		ConfigHash:        receipt.ConfigHash,
	}}
	err := defaultVerifyServeGate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "data stage") {
		t.Fatalf("defaultVerifyServeGate() error = %v, want data stage mismatch", err)
	}
}

func TestAPIHealthcheckIsLocalSecretFreeAndReceiptBound(t *testing.T) {
	var healthcheckCalls atomic.Int32
	deps := validDeps()
	deps.RunHealthcheck = func(ctx context.Context, cfg config.Config) error {
		if ctx == nil || cfg.Environment != config.EnvironmentTest {
			t.Fatalf("unexpected healthcheck cfg: %#v", cfg)
		}
		healthcheckCalls.Add(1)
		return nil
	}
	if err := run(context.Background(), []string{"healthcheck", "api"}, deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if healthcheckCalls.Load() != 1 {
		t.Fatalf("healthcheck calls = %d", healthcheckCalls.Load())
	}
}

func TestRunRejectsLegacyMigrateCommand(t *testing.T) {
	err := run(context.Background(), []string{"migrate"}, validDeps())
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("run() error = %v", err)
	}
}

func validDeps() processDeps {
	return processDeps{
		LoadConfig: func() (config.Config, error) {
			return config.Config{Environment: config.EnvironmentTest}, nil
		},
		LoadDataGateConfig: func() (config.DataGateConfig, error) {
			return config.DataGateConfig{Environment: config.EnvironmentTest, Mode: "apply", ReceiptPath: "/tmp/receipt.json"}, nil
		},
		RunDataGate:    func(context.Context, config.DataGateConfig) error { return nil },
		RunHealthcheck: func(context.Context, config.Config) error { return nil },
		VerifyServeGate: func(context.Context, config.Config) error {
			return nil
		},
		NewApplication: func(config.Config) (applicationRunner, error) {
			return runnerStub{}, nil
		},
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
