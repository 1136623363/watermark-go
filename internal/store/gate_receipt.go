package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const GateReceiptSchemaVersion = "gate-receipt/v1"

type GateReceipt struct {
	SchemaVersion     string    `json:"schemaVersion"`
	Role              string    `json:"role"`
	DataStage         string    `json:"dataStage"`
	ImageDigest       string    `json:"imageDigest"`
	DeploymentRunID   string    `json:"deploymentRunId"`
	GateAttemptID     string    `json:"gateAttemptId"`
	SchemaState       string    `json:"schemaState"`
	TargetDBIdentity  string    `json:"targetDbIdentity"`
	RedisIdentity     string    `json:"redisIdentity"`
	OutboxIdentity    string    `json:"outboxIdentity"`
	InputSnapshotHash string    `json:"inputSnapshotHash"`
	ConfigHash        string    `json:"configHash"`
	CompletedAt       time.Time `json:"completedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
	Passed            bool      `json:"passed"`
}

type GateExpectation struct {
	Role              string
	DataStage         string
	ImageDigest       string
	DeploymentRunID   string
	SchemaState       string
	TargetDBIdentity  string
	RedisIdentity     string
	OutboxIdentity    string
	InputSnapshotHash string
	ConfigHash        string
}

func (receipt GateReceipt) Validate(expect GateExpectation, now time.Time) error {
	if receipt.SchemaVersion != GateReceiptSchemaVersion || !receipt.Passed {
		return errors.New("gate receipt did not pass")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if receipt.ExpiresAt.IsZero() || !now.Before(receipt.ExpiresAt) {
		return errors.New("gate receipt is expired")
	}
	if strings.TrimSpace(receipt.GateAttemptID) == "" || receipt.CompletedAt.IsZero() {
		return errors.New("gate receipt identity is incomplete")
	}
	for _, mismatch := range []struct {
		name string
		got  string
		want string
	}{
		{"role", receipt.Role, expect.Role},
		{"data stage", receipt.DataStage, expect.DataStage},
		{"image digest", receipt.ImageDigest, expect.ImageDigest},
		{"deployment run", receipt.DeploymentRunID, expect.DeploymentRunID},
		{"schema state", receipt.SchemaState, expect.SchemaState},
		{"target db", receipt.TargetDBIdentity, expect.TargetDBIdentity},
		{"redis", receipt.RedisIdentity, expect.RedisIdentity},
		{"outbox", receipt.OutboxIdentity, expect.OutboxIdentity},
		{"snapshot", receipt.InputSnapshotHash, expect.InputSnapshotHash},
		{"config", receipt.ConfigHash, expect.ConfigHash},
	} {
		if strings.TrimSpace(mismatch.want) == "" || mismatch.got != mismatch.want {
			return fmt.Errorf("gate receipt %s mismatch", mismatch.name)
		}
	}
	return nil
}

func (receipt GateReceipt) String() string {
	return fmt.Sprintf("GateReceipt{schemaVersion:%s role:%s dataStage:%s passed:%t}", receipt.SchemaVersion, receipt.Role, receipt.DataStage, receipt.Passed)
}

func WriteGateReceiptAtomic(ctx context.Context, path string, receipt GateReceipt) error {
	if ctx == nil {
		return errors.New("gate receipt context is required")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("gate receipt path is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".gate-receipt-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(receipt); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func LoadGateReceipt(path string) (GateReceipt, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return GateReceipt{}, err
	}
	var receipt GateReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return GateReceipt{}, err
	}
	return receipt, nil
}
