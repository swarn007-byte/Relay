package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		path,
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)

	return db, nil
}

func CreateTables(ctx context.Context, db *sql.DB) error {
	legacy, err := isLegacySchema(ctx, db)
	if err != nil {
		return err
	}
	if legacy {
		if err := migrateLegacySchema(ctx, db); err != nil {
			return fmt.Errorf("legacy schema migration failed: %w", err)
		}
	}

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return err
	}
	return nil
}

func timeToMillis(t time.Time) int64 {
	return t.UTC().UnixMilli()
}

func millisToTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}
