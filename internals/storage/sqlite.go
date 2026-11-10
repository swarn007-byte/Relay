package storage

// import(
// 	"database/sql"
// 	_ "modernc.org/sqlite"
// )
import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

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
    // 2. Check file-reading error
    // 3. Execute SQL
    // 4. Check execution error
    // 5. Return nil on success
}