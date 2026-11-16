package worker

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/swarn007-byte/Relay/internals/internals/storage"
	_ "modernc.org/sqlite"
)

func TestDeliveryPipeline(t *testing.T) {
	ctx := context.Background()

	// 1. Create a local webhook receiver
	receiver := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed reading body: %v", err)
			}

			if string(body) != `{"amount":500}` {
				t.Errorf("unexpected payload: %s", body)
			}

			w.WriteHeader(http.StatusOK)
		},
	))
	defer receiver.Close()

	// 2. Create an isolated SQLite database
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := storage.CreateTables(db); err != nil {
		t.Fatal(err)
	}

	// 3. Insert a test event and delivery job
	now := time.Now().UTC().Add(-time.Second).
		Format(time.RFC3339Nano)

	_, err = db.Exec(
		`INSERT INTO events
         (id, event_type, payload, created_at)
         VALUES (?, ?, ?, ?)`,
		"evt_test", "payment.success",
		`{"amount":500}`, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(
		`INSERT INTO delivery_jobs
         (delivery_id, event_id, status,
          target_url, attempts, next_attempt_at)
         VALUES (?, ?, ?, ?, ?, ?)`,
		"del_test", "evt_test", "pending",
		receiver.URL, 0, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	// 4. Start workers and schedule the job
	jobs := make(chan storage.DeliveryJob, 10)
	wg := StartPool(ctx, db, jobs, 2)

	err = ScheduleJobs(ctx, db, jobs, 10)

	close(jobs)
	wg.Wait()

	if err != nil {
		t.Fatal(err)
	}

	// 5. Verify the final database status
	var status string
	var attempts int

	err = db.QueryRow(
		`SELECT status, attempts
         FROM delivery_jobs
         WHERE delivery_id = ?`,
		"del_test",
	).Scan(&status, &attempts)

	if err != nil {
		t.Fatal(err)
	}

	if status != "delivered" {
		t.Fatalf("expected delivered, got %s", status)
	}

	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}
