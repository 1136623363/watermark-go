package server

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migrationRecord struct {
	Version string
	Name    string
	SQL     string
}

type migrationQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	alterAddIndexPattern        = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?" + `\s+ADD\s+(?:KEY|INDEX)\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?")
	alterAddIndexKeywordPattern = regexp.MustCompile(`(?is)\bADD\s+(?:KEY|INDEX)\b`)
)

func RunMigrations(ctx context.Context, cfg config.Config) error {
	if ctx == nil {
		return errors.New("migration context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	migrationCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	db, err := openConfiguredMySQL(migrationCtx, cfg.MySQL.DSN)
	if err != nil {
		return err
	}
	if db == nil {
		return errors.New("MYSQL_DSN is required for migrate")
	}
	defer db.Close()

	return runMigrations(migrationCtx, db)
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version VARCHAR(64) NOT NULL PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`); err != nil {
		return fmt.Errorf("ensure schema_migrations failed: %w", err)
	}

	records, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, record := range records {
		applied, err := migrationApplied(ctx, db, record.Version)
		if err != nil {
			return fmt.Errorf("check migration %s failed: %w", record.Name, err)
		}
		if applied {
			logInfof("migration skipped version=%s name=%s", record.Version, record.Name)
			continue
		}
		logInfof("migration applying version=%s name=%s", record.Version, record.Name)
		if err := applyMigration(ctx, db, record); err != nil {
			return err
		}
		logInfof("migration applied version=%s name=%s", record.Version, record.Name)
	}
	return nil
}

func loadMigrations() ([]migrationRecord, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	records := make([]migrationRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(entry.Name(), ".sql")
		version := name
		if idx := strings.Index(name, "_"); idx > 0 {
			version = name[:idx]
		}
		records = append(records, migrationRecord{
			Version: version,
			Name:    name,
			SQL:     string(content),
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})
	return records, nil
}

func migrationApplied(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var existing string
	err := db.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE version = ?", version).Scan(&existing)
	if err == nil {
		return true, nil
	}
	if isNoRows(err) {
		return false, nil
	}
	return false, err
}

func applyMigration(ctx context.Context, db *sql.DB, record migrationRecord) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := splitSQLStatements(record.SQL)
	for _, statement := range statements {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if err := execMigrationStatement(ctx, tx, statement); err != nil {
			return fmt.Errorf("apply migration %s failed: %w", record.Name, err)
		}
	}
	if _, err := tx.ExecContext(
		ctx,
		"INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
		record.Version,
		record.Name,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func execMigrationStatement(ctx context.Context, tx *sql.Tx, statement string) error {
	table, index, ok := parseAlterAddIndex(statement)
	if ok {
		exists, err := migrationIndexExists(ctx, tx, table, index)
		if err != nil {
			return err
		}
		if exists {
			logInfof("migration index skipped table=%s index=%s", table, index)
			return nil
		}
	}

	if _, err := tx.ExecContext(ctx, statement); err != nil {
		if ok && isDuplicateIndexError(err) {
			exists, checkErr := migrationIndexExists(ctx, tx, table, index)
			if checkErr == nil && exists {
				logInfof("migration index already exists table=%s index=%s", table, index)
				return nil
			}
		}
		return err
	}
	return nil
}

func parseAlterAddIndex(statement string) (string, string, bool) {
	trimmed := strings.TrimSpace(statement)
	if len(alterAddIndexKeywordPattern.FindAllString(trimmed, -1)) != 1 {
		return "", "", false
	}
	matches := alterAddIndexPattern.FindStringSubmatch(trimmed)
	if len(matches) != 3 {
		return "", "", false
	}
	return matches[1], matches[2], true
}

func migrationIndexExists(ctx context.Context, db migrationQueryer, tableName, indexName string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND index_name = ?
`, tableName, indexName).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check index %s.%s failed: %w", tableName, indexName, err)
	}
	return count > 0, nil
}

func isDuplicateIndexError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key name") || strings.Contains(message, "error 1061")
}

func splitSQLStatements(sqlText string) []string {
	lines := strings.Split(sqlText, "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		clean = append(clean, line)
	}
	return strings.Split(strings.Join(clean, "\n"), ";")
}
