package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validGateExpectation() GateExpectation {
	return GateExpectation{
		Role:              "recovery",
		DataStage:         "final",
		ImageDigest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DeploymentRunID:   "run-123",
		SchemaState:       "schema-012",
		TargetDBIdentity:  "mysql-final-identity",
		RedisIdentity:     "redis-final-identity",
		OutboxIdentity:    "outbox-not-applicable",
		InputSnapshotHash: "snapshot-hash",
		ConfigHash:        "config-hash",
	}
}

func validGateReceipt(now time.Time) GateReceipt {
	expect := validGateExpectation()
	return GateReceipt{
		SchemaVersion:     GateReceiptSchemaVersion,
		Role:              expect.Role,
		DataStage:         expect.DataStage,
		ImageDigest:       expect.ImageDigest,
		DeploymentRunID:   expect.DeploymentRunID,
		GateAttemptID:     "attempt-1",
		SchemaState:       expect.SchemaState,
		TargetDBIdentity:  expect.TargetDBIdentity,
		RedisIdentity:     expect.RedisIdentity,
		OutboxIdentity:    expect.OutboxIdentity,
		InputSnapshotHash: expect.InputSnapshotHash,
		ConfigHash:        expect.ConfigHash,
		CompletedAt:       now,
		ExpiresAt:         now.Add(time.Hour),
		Passed:            true,
	}
}

func TestGateReceiptValidatesEveryServeBindingField(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	expect := validGateExpectation()
	if err := validGateReceipt(now).Validate(expect, now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*GateReceipt){
		"role": func(receipt *GateReceipt) { receipt.Role = "candidate" },
		"digest": func(receipt *GateReceipt) {
			receipt.ImageDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"run":        func(receipt *GateReceipt) { receipt.DeploymentRunID = "old-run" },
		"db":         func(receipt *GateReceipt) { receipt.TargetDBIdentity = "other-db" },
		"redis":      func(receipt *GateReceipt) { receipt.RedisIdentity = "other-redis" },
		"failed":     func(receipt *GateReceipt) { receipt.Passed = false },
		"expired":    func(receipt *GateReceipt) { receipt.ExpiresAt = now.Add(-time.Second) },
		"incomplete": func(receipt *GateReceipt) { receipt.GateAttemptID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			receipt := validGateReceipt(now)
			mutate(&receipt)
			if err := receipt.Validate(expect, now); err == nil {
				t.Fatal("receipt mismatch was accepted")
			}
		})
	}
}

func TestGateReceiptAtomicWriteUses0600AndRoundTrips(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "receipt.json")
	receipt := validGateReceipt(now)
	if err := WriteGateReceiptAtomic(context.Background(), path, receipt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %v, want 0600", info.Mode().Perm())
	}
	loaded, err := LoadGateReceipt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(validGateExpectation(), now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loaded.String(), validGateExpectation().ConfigHash) {
		t.Fatal("receipt formatting exposed detailed config hash")
	}
}

func TestRevalidateGateIsReadOnly(t *testing.T) {
	store := &recordingGateStore{}
	_, err := RunDataGate(context.Background(), GateRequest{
		Mode:        GateModeRevalidate,
		Receipt:     validGateReceipt(time.Now()),
		Expectation: validGateExpectation(),
		Now:         time.Now(),
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	if store.writeCalls != 0 {
		t.Fatalf("revalidate performed writes: %d", store.writeCalls)
	}
}

type recordingGateStore struct{ writeCalls int }

func (store *recordingGateStore) Apply(context.Context) error {
	store.writeCalls++
	return nil
}

func (store *recordingGateStore) Revalidate(context.Context) error { return nil }
