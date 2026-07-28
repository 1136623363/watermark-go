package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Migration struct {
	Version string
	Name    string
	SQL     string
}

type MigrationExecutor interface {
	EnsureMigrationTable(context.Context) error
	HasMigration(context.Context, string) (bool, error)
	ApplyMigration(context.Context, Migration) error
}

func LoadMigrationsDir(dir string) ([]Migration, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("migration directory is required")
	}
	return LoadMigrationsFS(os.DirFS(filepath.Dir(dir)), filepath.Base(dir))
}

func LoadMigrationsFS(fsys fs.FS, dir string) ([]Migration, error) {
	if fsys == nil || strings.TrimSpace(dir) == "" {
		return nil, errors.New("migration filesystem is required")
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(fsys, filepath.ToSlash(filepath.Join(dir, entry.Name())))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(entry.Name(), ".sql")
		version := name
		if index := strings.Index(name, "_"); index > 0 {
			version = name[:index]
		}
		migrations = append(migrations, Migration{
			Version: version,
			Name:    name,
			SQL:     string(body),
		})
	}
	sort.Slice(migrations, func(left, right int) bool {
		return migrations[left].Name < migrations[right].Name
	})
	if err := ValidateMigrations(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

func ValidateMigrations(migrations []Migration) error {
	if len(migrations) != 13 {
		return fmt.Errorf("expected 13 migrations, got %d", len(migrations))
	}
	seen := make(map[string]bool, len(migrations))
	for index, migration := range migrations {
		wantVersion := fmt.Sprintf("%03d", index+1)
		if migration.Version != wantVersion || !strings.HasPrefix(migration.Name, wantVersion+"_") {
			return fmt.Errorf("migration %d is out of order: %s", index, migration.Name)
		}
		if seen[migration.Version] {
			return fmt.Errorf("duplicate migration version %s", migration.Version)
		}
		seen[migration.Version] = true
		if strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("migration %s is empty", migration.Name)
		}
	}
	return nil
}

func ApplyMigrations(ctx context.Context, executor MigrationExecutor, migrations []Migration) error {
	if ctx == nil {
		return errors.New("migration context is required")
	}
	if executor == nil {
		return errors.New("migration executor is required")
	}
	if err := ValidateMigrations(migrations); err != nil {
		return err
	}
	if err := executor.EnsureMigrationTable(ctx); err != nil {
		return err
	}
	for _, migration := range migrations {
		applied, err := executor.HasMigration(ctx, migration.Version)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", migration.Name, err)
		}
		if applied {
			continue
		}
		if err := executor.ApplyMigration(ctx, migration); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.Name, err)
		}
	}
	return nil
}

func ExpectedMigrationSchemaState(migrations []Migration) string {
	hash := sha256.New()
	for _, migration := range migrations {
		statementHash := sha256.Sum256([]byte(migration.SQL))
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", migration.Version, migration.Name, hex.EncodeToString(statementHash[:]))
	}
	return "migrations:" + hex.EncodeToString(hash.Sum(nil))
}

func ValidateAppliedMigrationSchemaState(ctx context.Context, executor MigrationExecutor, migrations []Migration, expectedState string) error {
	if ctx == nil {
		return errors.New("migration context is required")
	}
	if executor == nil {
		return errors.New("migration executor is required")
	}
	if err := ValidateMigrations(migrations); err != nil {
		return err
	}
	actualState := ExpectedMigrationSchemaState(migrations)
	if strings.TrimSpace(expectedState) == "" || expectedState != actualState {
		return fmt.Errorf("migration schema state mismatch")
	}
	for _, migration := range migrations {
		applied, err := executor.HasMigration(ctx, migration.Version)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", migration.Name, err)
		}
		if !applied {
			return fmt.Errorf("migration %s is not applied", migration.Name)
		}
	}
	return nil
}

type SQLExecutor struct {
	DB *sql.DB
}

func (executor SQLExecutor) EnsureMigrationTable(ctx context.Context) error {
	if executor.DB == nil {
		return errors.New("mysql connection is required")
	}
	_, err := executor.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version VARCHAR(64) NOT NULL PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
	return err
}

func (executor SQLExecutor) HasMigration(ctx context.Context, version string) (bool, error) {
	if executor.DB == nil {
		return false, errors.New("mysql connection is required")
	}
	var existing string
	err := executor.DB.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE version = ?", version).Scan(&existing)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (executor SQLExecutor) ApplyMigration(ctx context.Context, migration Migration) error {
	if executor.DB == nil {
		return errors.New("mysql connection is required")
	}
	tx, err := executor.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range SplitSQLStatements(migration.SQL) {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, name) VALUES (?, ?)", migration.Version, migration.Name); err != nil {
		return err
	}
	return tx.Commit()
}

func SplitSQLStatements(sqlText string) []string {
	lines := strings.Split(sqlText, "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		clean = append(clean, line)
	}
	return strings.Split(strings.Join(clean, "\n"), ";")
}
