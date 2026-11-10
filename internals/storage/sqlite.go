package storage

// import(
// 	"database/sql"
// 	_ "modernc.org/sqlite"
// )

import (
	"context"
	"database/sql"
	_ "embed"
	"time"

	_ "modernc.org/sqlite"
)

var schema string

func InitDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", "data/relay.db")
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func CreateTables(db *sql.DB) error {
	// 1. Read schema.sql
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	_, err := db.ExecContext(ctx, schema)
	if err != nil {
		return err
	}
	return nil
}
