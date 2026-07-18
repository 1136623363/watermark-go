package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type MySQLConfig struct {
	DSN         string
	PingTimeout time.Duration
}

func OpenMySQL(ctx context.Context, cfg MySQLConfig) (*sql.DB, error) {
	if ctx == nil {
		return nil, errors.New("mysql context is required")
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("mysql dsn is required")
	}
	parsed, err := mysql.ParseDSN(cfg.DSN)
	if err != nil {
		return nil, errors.New("mysql dsn is invalid")
	}
	if parsed.DBName == "" {
		return nil, errors.New("mysql database name is required")
	}
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, err
	}
	timeout := cfg.PingTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
