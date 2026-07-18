package store

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestMigrationsAreOrderedAndIdempotent(t *testing.T) {
	migrations, err := LoadMigrationsDir(filepath.Join(repoRoot(t), "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 12 {
		t.Fatalf("migration count = %d, want 12", len(migrations))
	}
	for index, migration := range migrations {
		want := fmt.Sprintf("%03d", index+1)
		if migration.Version != want {
			t.Fatalf("migration[%d] version = %q, want %q", index, migration.Version, want)
		}
		for _, statement := range SplitSQLStatements(migration.SQL) {
			trimmed := strings.TrimSpace(statement)
			if trimmed == "" {
				continue
			}
			upper := strings.ToUpper(trimmed)
			if !strings.HasPrefix(upper, "CREATE TABLE IF NOT EXISTS") &&
				!strings.HasPrefix(upper, "ALTER TABLE") &&
				!strings.HasPrefix(upper, "UPDATE ") {
				t.Fatalf("migration %s contains unsupported non-idempotent statement: %s", migration.Name, trimmed)
			}
		}
	}
}

func TestMigrationsIncludeTaskLeaseFields(t *testing.T) {
	migrations, err := LoadMigrationsDir(filepath.Join(repoRoot(t), "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	last := migrations[len(migrations)-1]
	if last.Version != "012" {
		t.Fatalf("last migration = %s", last.Name)
	}
	for _, fragment := range []string{"locked_by", "locked_until", "next_attempt_at", "idx_status_next_attempt"} {
		if !strings.Contains(last.SQL, fragment) {
			t.Fatalf("task lease migration missing %s", fragment)
		}
	}
}

func TestApplyMigrationsSkipsAlreadyAppliedVersions(t *testing.T) {
	migrations := []Migration{
		{Version: "001", Name: "001_core", SQL: "CREATE TABLE IF NOT EXISTS one (id INT);"},
		{Version: "002", Name: "002_next", SQL: "CREATE TABLE IF NOT EXISTS two (id INT);"},
		{Version: "003", Name: "003_next", SQL: "CREATE TABLE IF NOT EXISTS three (id INT);"},
		{Version: "004", Name: "004_next", SQL: "UPDATE three SET id = id;"},
		{Version: "005", Name: "005_next", SQL: "UPDATE three SET id = id;"},
		{Version: "006", Name: "006_next", SQL: "UPDATE three SET id = id;"},
		{Version: "007", Name: "007_next", SQL: "UPDATE three SET id = id;"},
		{Version: "008", Name: "008_next", SQL: "UPDATE three SET id = id;"},
		{Version: "009", Name: "009_next", SQL: "UPDATE three SET id = id;"},
		{Version: "010", Name: "010_next", SQL: "UPDATE three SET id = id;"},
		{Version: "011", Name: "011_next", SQL: "UPDATE three SET id = id;"},
		{Version: "012", Name: "012_next", SQL: "UPDATE three SET id = id;"},
	}
	executor := &recordingMigrationExecutor{applied: map[string]bool{"001": true, "003": true}}
	if err := ApplyMigrations(context.Background(), executor, migrations); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(executor.appliedOrder, ","); got != "002,004,005,006,007,008,009,010,011,012" {
		t.Fatalf("applied order = %s", got)
	}
	if executor.ensureCalls != 1 {
		t.Fatalf("ensure migration table calls = %d", executor.ensureCalls)
	}
}

type recordingMigrationExecutor struct {
	ensureCalls  int
	applied      map[string]bool
	appliedOrder []string
}

func (executor *recordingMigrationExecutor) EnsureMigrationTable(context.Context) error {
	executor.ensureCalls++
	return nil
}

func (executor *recordingMigrationExecutor) HasMigration(_ context.Context, version string) (bool, error) {
	return executor.applied[version], nil
}

func (executor *recordingMigrationExecutor) ApplyMigration(_ context.Context, migration Migration) error {
	executor.applied[migration.Version] = true
	executor.appliedOrder = append(executor.appliedOrder, migration.Version)
	return nil
}
