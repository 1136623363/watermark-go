package server

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	_ "github.com/go-sql-driver/mysql"
)

type infrastructureState struct {
	mysql *sql.DB
	redis *redis.Client
}

type infrastructureStatus struct {
	MySQLEnabled bool   `json:"mysqlEnabled"`
	MySQLStatus  string `json:"mysqlStatus"`
	MySQLDSN     string `json:"mysqlDsn,omitempty"`
	RedisEnabled bool   `json:"redisEnabled"`
	RedisStatus  string `json:"redisStatus"`
	RedisAddr    string `json:"redisAddr,omitempty"`
}

var appInfra infrastructureState

func initInfrastructure(ctx context.Context) error {
	mysqlDB, err := openOptionalMySQL(ctx)
	if err != nil {
		return err
	}
	appInfra.mysql = mysqlDB

	redisClient, err := openOptionalRedis(ctx)
	if err != nil {
		if appInfra.mysql != nil {
			_ = appInfra.mysql.Close()
			appInfra.mysql = nil
		}
		return err
	}
	appInfra.redis = redisClient
	return nil
}

func closeInfrastructure() {
	if appInfra.redis != nil {
		_ = appInfra.redis.Close()
		appInfra.redis = nil
	}
	if appInfra.mysql != nil {
		_ = appInfra.mysql.Close()
		appInfra.mysql = nil
	}
}

func openOptionalMySQL(ctx context.Context) (*sql.DB, error) {
	return openConfiguredMySQL(ctx, os.Getenv("MYSQL_DSN"))
}

func openConfiguredMySQL(ctx context.Context, rawDSN string) (*sql.DB, error) {
	dsn := strings.TrimSpace(rawDSN)
	if dsn == "" {
		logInfof("mysql disabled: MYSQL_DSN is empty")
		return nil, nil
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(envInt("MYSQL_MAX_OPEN_CONNS", 50))
	db.SetMaxIdleConns(envInt("MYSQL_MAX_IDLE_CONNS", 10))
	db.SetConnMaxLifetime(time.Duration(envInt("MYSQL_CONN_MAX_LIFETIME_SECONDS", 1800)) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(envInt("MYSQL_CONN_MAX_IDLE_SECONDS", 300)) * time.Second)

	pingTimeout := time.Duration(envInt("MYSQL_PING_TIMEOUT_SECONDS", 5)) * time.Second
	if pingTimeout <= 0 {
		pingTimeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	logInfof("mysql enabled dsn=%s", maskMySQLDSN(dsn))
	return db, nil
}

func openOptionalRedis(ctx context.Context) (*redis.Client, error) {
	addr := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if addr == "" {
		logInfof("redis disabled: REDIS_ADDR is empty")
		return nil, nil
	}

	dbIndex := envInt("REDIS_DB", 0)
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Username:     strings.TrimSpace(os.Getenv("REDIS_USERNAME")),
		Password:     strings.TrimSpace(os.Getenv("REDIS_PASSWORD")),
		DB:           dbIndex,
		PoolSize:     envInt("REDIS_POOL_SIZE", 20),
		MinIdleConns: envInt("REDIS_MIN_IDLE_CONNS", 2),
	})

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	logInfof("redis enabled addr=%s db=%d", addr, dbIndex)
	return client, nil
}

func currentInfrastructureStatus(ctx context.Context) infrastructureStatus {
	status := infrastructureStatus{
		MySQLEnabled: appInfra.mysql != nil,
		MySQLStatus:  "disabled",
		MySQLDSN:     maskMySQLDSN(os.Getenv("MYSQL_DSN")),
		RedisEnabled: appInfra.redis != nil,
		RedisStatus:  "disabled",
		RedisAddr:    strings.TrimSpace(os.Getenv("REDIS_ADDR")),
	}

	if appInfra.mysql != nil {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := appInfra.mysql.PingContext(pingCtx)
		cancel()
		if err != nil {
			status.MySQLStatus = "error: " + err.Error()
		} else {
			status.MySQLStatus = "ok"
		}
	}

	if appInfra.redis != nil {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := appInfra.redis.Ping(pingCtx).Err()
		cancel()
		if err != nil {
			status.RedisStatus = "error: " + err.Error()
		} else {
			status.RedisStatus = "ok"
		}
	}

	return status
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func maskMySQLDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return ""
	}

	at := strings.LastIndex(dsn, "@")
	if at <= 0 {
		return dsn
	}
	credentials := dsn[:at]
	hostAndParams := dsn[at:]
	colon := strings.Index(credentials, ":")
	if colon < 0 {
		return credentials + hostAndParams
	}
	return credentials[:colon] + ":***" + hostAndParams
}

func redisKey(parts ...string) string {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, "watermark")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, ":")
}

func normalizeURLForHash(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return strings.TrimRight(parsed.String(), "/")
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
