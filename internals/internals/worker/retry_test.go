package worker

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/swarn007-byte/Relay/internals/internals/storage"
	_ "modernc.org/sqlite"
)

func TestFailedDeliverySchedulesRetry(t *testing.T) {
	ctx := context.Background()

	// Simulate a failing webhook endpoint.
	receiver := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}),
	)
	defer receiver.Close()

	// Create a fresh database for this test.
	dbPath := filepath.Join(t.TempDir(), "retry.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := storage.CreateTables(db); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Add(-time.Second).
		Format(time.RFC3339Nano)

	// Insert event.
	_, err = db.Exec(
		`INSERT INTO events
         (id, event_type, payload, created_at)
         VALUES (?, ?, ?, ?)`,
		"evt_retry", "payment.failed",
		`{"amount":500}`, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Insert pending delivery job.
	_, err = db.Exec(
		`INSERT INTO delivery_jobs
         (delivery_id, event_id, status,
          target_url, attempts, next_attempt_at)
         VALUES (?, ?, ?, ?, ?, ?)`,
		"del_retry", "evt_retry", "pending",
		receiver.URL, 0, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	jobs := make(chan storage.DeliveryJob, 10)
	wg := StartPool(ctx, db, jobs, 2)

	// Dispatch the pending job.
	err = ScheduleJobs(ctx, db, jobs, 10)

	close(jobs)
	wg.Wait()

	if err != nil {
		t.Fatal(err)
	}

	// Verify the failed job was scheduled again.
	var status string
	var attempts int
	var nextAttempt string

	err = db.QueryRow(
		`SELECT status, attempts, next_attempt_at
         FROM delivery_jobs
         WHERE delivery_id = ?`,
		"del_retry",
	).Scan(&status, &attempts, &nextAttempt)

	if err != nil {
		t.Fatal(err)
	}

	if status != "pending" {
		t.Fatalf("expected pending, got %s", status)
	}

	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}

	retryAt, err := time.Parse(time.RFC3339Nano, nextAttempt)
	if err != nil {
		t.Fatal(err)
	}

	if !retryAt.After(time.Now().Add(3 * time.Second)) {
		t.Fatalf("retry time was not scheduled in the future: %s",
			nextAttempt)
	}
}
