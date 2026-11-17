package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"time"

	"github.com/tursodatabase/libsql-client-go/libsql"
)

// OpenConfigured retains local SQLite for self-hosting and uses a durable remote
// database when Turso credentials are configured. It never falls back to an
// ephemeral file if remote configuration is incomplete or unavailable.
func OpenConfigured(path string) (*sql.DB, error) {
	databaseURL, authToken := os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN")
	if databaseURL != "" || authToken != "" {
		return OpenRemote(databaseURL, authToken)
	}
	return Open(path)
}
func OpenRemote(databaseURL, authToken string) (*sql.DB, error) {
	u, err := url.Parse(databaseURL)
	if err != nil || (u.Scheme != "libsql" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || authToken == "" {
		return nil, errors.New("configure TURSO_DATABASE_URL as a TLS-enabled libsql/HTTPS origin and TURSO_AUTH_TOKEN separately")
	}
	connector, err := libsql.NewConnector(databaseURL, libsql.WithAuthToken(authToken))
	if err != nil {
		return nil, errors.New("remote database configuration rejected")
	}
	db := sql.OpenDB(foreignKeyConnector{connector})
	// Free demo traffic is deliberately serialized: SQLite has one writer, and
	// a remote transaction must keep its connection for the entire transaction.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("remote database connection failed; check database availability and credentials")
	}
	return db, nil
}

type foreignKeyConnector struct{ driver.Connector }

func (c foreignKeyConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, errors.New("remote database connection failed")
	}
	ex, ok := conn.(driver.ExecerContext)
	if !ok {
		conn.Close()
		return nil, errors.New("remote database driver does not support initialization")
	}
	if _, err = ex.ExecContext(ctx, "PRAGMA foreign_keys=ON", nil); err != nil {
		conn.Close()
		return nil, errors.New("remote database initialization failed")
	}
	return conn, nil
}
