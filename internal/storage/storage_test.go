package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := CreateTables(context.Background(), db); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func seedJob(
	t *testing.T,
	db *sql.DB,
	status string,
	targetURL string,
	attempts int,
	nextAt time.Time,
) (string, string) {
	t.Helper()
	ctx := context.Background()
	eventID := uuid.NewString()
	deliveryID := uuid.NewString()

	_, err := db.ExecContext(ctx,
		`insert into events (id, event_type, payload, created_at) values (?, ?, ?, ?)`,
		eventID, "payment.success", `{"amount":500}`, timeToMillis(time.Now()))
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}

	var nextMs interface{}
	if !nextAt.IsZero() {
		nextMs = timeToMillis(nextAt)
	}
	_, err = db.ExecContext(ctx,
		`insert into delivery_jobs
		 (delivery_id, event_id, status, target_url, attempts,
		  next_attempt_at, lease_owner, lease_expires_at, last_error,
		  created_at, updated_at)
		 values (?, ?, ?, ?, ?, ?, null, null, null, ?, ?)`,
		deliveryID, eventID, status, targetURL, attempts, nextMs,
		timeToMillis(time.Now()), timeToMillis(time.Now()))
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return eventID, deliveryID
}

func jobRow(t *testing.T, db *sql.DB, deliveryID string) (status string, attempts int, next sql.NullInt64, owner sql.NullString) {
	t.Helper()
	err := db.QueryRow(
		`select status, attempts, next_attempt_at, lease_owner
		 from delivery_jobs where delivery_id = ?`,
		deliveryID,
	).Scan(&status, &attempts, &next, &owner)
	if err != nil {
		t.Fatalf("read job %s: %v", deliveryID, err)
	}
	return
}

func TestSaveEventAtomicPersistence(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	eventID := uuid.NewString()
	deliveryID, err := SaveEvent(ctx, db, eventID, "payment.success", "https://example.com/hook", []byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("save event: %v", err)
	}
	if deliveryID == "" {
		t.Fatal("expected a delivery id")
	}

	var eventCount int
	if err := db.QueryRow(`select count(*) from events where id = ?`, eventID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 event, got %d", eventCount)
	}

	status, attempts, next, owner := jobRow(t, db, deliveryID)
	if status != "pending" || attempts != 0 || !next.Valid || owner.Valid {
		t.Fatalf("unexpected job state: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}
	if next.Int64 > timeToMillis(time.Now())+1000 {
		t.Fatalf("next_attempt_at not due: %d", next.Int64)
	}

	if _, err := SaveEvent(ctx, db, eventID, "payment.success", "https://example.com/hook", []byte(`{"a":1}`)); err == nil {
		t.Fatal("expected duplicate event id to fail")
	}

	var jobCount int
	if err := db.QueryRow(`select count(*) from delivery_jobs where event_id = ?`, eventID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("expected duplicate insert to roll back, got %d jobs", jobCount)
	}
}

func TestFetchDueJobs(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dueEvent, dueID := seedJob(t, db, "pending", "https://example.com/due", 0, time.Now().Add(-time.Second))
	seedJob(t, db, "pending", "https://example.com/future", 0, time.Now().Add(time.Hour))
	seedJob(t, db, "dead", "https://example.com/dead", 5, time.Time{})

	jobs, err := FetchDueJobs(ctx, db, 10)
	if err != nil {
		t.Fatalf("fetch due jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(jobs))
	}
	job := jobs[0]
	if job.DeliveryID != dueID || job.EventID != dueEvent {
		t.Fatalf("wrong job fetched: %+v", job)
	}
	if job.Payload != `{"amount":500}` {
		t.Fatalf("wrong payload: %q", job.Payload)
	}
	if job.EventType != "payment.success" {
		t.Fatalf("wrong event type: %q", job.EventType)
	}
}

func TestFetchDueJobsLimit(t *testing.T) {
	db := openTestDB(t)
	for i := 0; i < 5; i++ {
		seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now().Add(-time.Second))
	}
	jobs, err := FetchDueJobs(context.Background(), db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
}

func TestClaimJobAtomic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	_, dueID := seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now().Add(-time.Second))
	_, futureID := seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now().Add(time.Hour))

	claimed, err := ClaimJob(ctx, db, dueID, "token-1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("expected first claim to succeed")
	}

	claimedAgain, err := ClaimJob(ctx, db, dueID, "token-2", time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimedAgain {
		t.Fatal("expected second claim to be rejected")
	}

	status, _, _, owner := jobRow(t, db, dueID)
	if status != "processing" || owner.String != "token-1" {
		t.Fatalf("unexpected state: status=%s owner=%s", status, owner.String)
	}

	claimedFuture, err := ClaimJob(ctx, db, futureID, "token-3", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimedFuture {
		t.Fatal("expected future job claim to be rejected")
	}
}

func TestRecoverExpiredLeases(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	_, expiredID := seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now().Add(-time.Second))
	_, liveID := seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now().Add(-time.Second))

	if _, err := ClaimJob(ctx, db, expiredID, "token-expired", -time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimJob(ctx, db, liveID, "token-live", time.Minute); err != nil {
		t.Fatal(err)
	}

	recovered, err := RecoverExpiredLeases(ctx, db)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered job, got %d", recovered)
	}

	status, _, next, owner := jobRow(t, db, expiredID)
	if status != "pending" || owner.Valid || !next.Valid {
		t.Fatalf("expired job not recovered: status=%s owner.Valid=%v next.Valid=%v", status, owner.Valid, next.Valid)
	}

	status, _, _, owner = jobRow(t, db, liveID)
	if status != "processing" || owner.String != "token-live" {
		t.Fatalf("live lease was reclaimed: status=%s owner=%s", status, owner.String)
	}
}

func TestStaleWorkerCannotOverwriteNewState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	_, deliveryID := seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now().Add(-time.Second))

	if _, err := ClaimJob(ctx, db, deliveryID, "token-old", -time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverExpiredLeases(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimJob(ctx, db, deliveryID, "token-new", time.Minute); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	staleJob := DeliveryJob{DeliveryID: deliveryID, LeaseToken: "token-old"}
	err := RecordSuccess(ctx, db, staleJob, AttemptOutcome{
		AttemptNumber: 1,
		StartedAt:     now.Add(-time.Second),
		FinishedAt:    now,
		StatusCode:    200,
		Success:       true,
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}

	status, attempts, _, owner := jobRow(t, db, deliveryID)
	if status != "processing" || attempts != 0 || owner.String != "token-new" {
		t.Fatalf("stale worker overwrote state: status=%s attempts=%d owner=%s", status, attempts, owner.String)
	}

	activeJob := DeliveryJob{DeliveryID: deliveryID, LeaseToken: "token-new"}
	if err := RecordSuccess(ctx, db, activeJob, AttemptOutcome{
		AttemptNumber: 1,
		StartedAt:     now.Add(-time.Second),
		FinishedAt:    now,
		StatusCode:    200,
		Success:       true,
	}); err != nil {
		t.Fatalf("active worker record failed: %v", err)
	}

	status, attempts, next, _ := jobRow(t, db, deliveryID)
	if status != "delivered" || attempts != 1 || next.Valid {
		t.Fatalf("unexpected final state: status=%s attempts=%d next.Valid=%v", status, attempts, next.Valid)
	}
}

func TestRecordFailureRetryThenDead(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	_, deliveryID := seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now().Add(-time.Second))

	if _, err := ClaimJob(ctx, db, deliveryID, "token-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	retryAt := now.Add(30 * time.Second)
	err := RecordFailure(ctx, db, DeliveryJob{DeliveryID: deliveryID, LeaseToken: "token-1"}, AttemptOutcome{
		AttemptNumber: 1,
		StartedAt:     now.Add(-time.Second),
		FinishedAt:    now,
		StatusCode:    500,
		Success:       false,
		Error:         "webhook returned HTTP 500",
	}, &retryAt)
	if err != nil {
		t.Fatalf("record failure: %v", err)
	}

	status, attempts, next, owner := jobRow(t, db, deliveryID)
	if status != "pending" || attempts != 1 || !next.Valid || owner.Valid {
		t.Fatalf("unexpected retry state: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}
	if next.Int64 <= timeToMillis(now) {
		t.Fatalf("retry time not in the future: %d", next.Int64)
	}

	if _, err := db.Exec(`update delivery_jobs set next_attempt_at = ? where delivery_id = ?`,
		timeToMillis(now.Add(-time.Second)), deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimJob(ctx, db, deliveryID, "token-2", time.Minute); err != nil {
		t.Fatal(err)
	}
	err = RecordFailure(ctx, db, DeliveryJob{DeliveryID: deliveryID, LeaseToken: "token-2"}, AttemptOutcome{
		AttemptNumber: 2,
		StartedAt:     now.Add(-time.Second),
		FinishedAt:    now,
		Success:       false,
		Error:         "connection refused",
	}, nil)
	if err != nil {
		t.Fatalf("record dead: %v", err)
	}

	status, attempts, next, owner = jobRow(t, db, deliveryID)
	if status != "dead" || attempts != 2 || next.Valid || owner.Valid {
		t.Fatalf("unexpected dead state: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}

	var lastError sql.NullString
	if err := db.QueryRow(`select last_error from delivery_jobs where delivery_id = ?`, deliveryID).
		Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if !lastError.Valid || lastError.String != "connection refused" {
		t.Fatalf("last error not persisted: %v", lastError)
	}

	attemptsList, err := GetAttempts(ctx, db, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptsList) != 2 {
		t.Fatalf("expected 2 attempt records, got %d", len(attemptsList))
	}
	first := attemptsList[0]
	if first.AttemptNumber != 1 || first.Success || first.StatusCode == nil || *first.StatusCode != 500 {
		t.Fatalf("unexpected first attempt: %+v", first)
	}
	if first.NextAttemptAt == nil {
		t.Fatal("expected first attempt to record retry schedule")
	}
	second := attemptsList[1]
	if second.AttemptNumber != 2 || second.Success || second.NextAttemptAt != nil {
		t.Fatalf("unexpected second attempt: %+v", second)
	}
	if second.Error != "connection refused" {
		t.Fatalf("unexpected second attempt error: %q", second.Error)
	}
}

func TestReplayDeliverySemantics(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	eventID, deadID := seedJob(t, db, "dead", "https://example.com/hook", 5, time.Time{})

	newID, err := ReplayDelivery(ctx, db, deadID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if newID == "" || newID == deadID {
		t.Fatalf("expected a new delivery id, got %q", newID)
	}

	status, attempts, next, _ := jobRow(t, db, newID)
	if status != "pending" || attempts != 0 || !next.Valid {
		t.Fatalf("unexpected replay state: status=%s attempts=%d next.Valid=%v", status, attempts, next.Valid)
	}

	if _, err := ReplayDelivery(ctx, db, newID); !errors.Is(err, ErrNotReplayable) {
		t.Fatalf("expected ErrNotReplayable, got %v", err)
	}

	if _, err := ReplayDelivery(ctx, db, deadID); !errors.Is(err, ErrActiveDelivery) {
		t.Fatalf("expected ErrActiveDelivery, got %v", err)
	}

	if _, err := ReplayDelivery(ctx, db, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	var eventCount int
	if err := db.QueryRow(`select count(*) from events where id = ?`, eventID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("replay duplicated the event: %d", eventCount)
	}

	deadStatus, deadAttempts, _, _ := jobRow(t, db, deadID)
	if deadStatus != "dead" || deadAttempts != 5 {
		t.Fatalf("original dead job changed: status=%s attempts=%d", deadStatus, deadAttempts)
	}
}

func TestGetDeliveryNotFound(t *testing.T) {
	db := openTestDB(t)
	_, err := GetDelivery(context.Background(), db, uuid.NewString())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListDeliveriesByStatus(t *testing.T) {
	db := openTestDB(t)
	_, deadID := seedJob(t, db, "dead", "https://example.com/hook", 5, time.Time{})
	seedJob(t, db, "pending", "https://example.com/hook", 0, time.Now())

	dead, err := ListDeliveries(context.Background(), db, "dead", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 || dead[0].DeliveryID != deadID {
		t.Fatalf("expected only the dead delivery, got %+v", dead)
	}

	all, err := ListDeliveries(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(all))
	}
}

func TestMigrationFromLegacySchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	legacySchema := `
create table events(
 id text primary key,
 event_type text not null,
 payload text not null,
 created_at text not null
);
create table delivery_jobs(
 delivery_id text primary key,
 event_id text not null references events(id),
 status text not null,
 target_url text not null,
 attempts integer not null default 0,
 next_attempt_at text
);`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	createdAt := "2025-01-02T03:04:05.123456789Z"
	nextAt := "2025-06-07T08:09:10.987654321Z"
	if _, err := db.Exec(
		`insert into events (id, event_type, payload, created_at) values (?, ?, ?, ?)`,
		"evt-legacy", "payment.success", `{"legacy": true}`, createdAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`insert into delivery_jobs (delivery_id, event_id, status, target_url, attempts, next_attempt_at)
		 values (?, ?, ?, ?, ?, ?)`,
		"del-legacy-pending", "evt-legacy", "pending", "https://example.com/hook", 0, nextAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`insert into delivery_jobs (delivery_id, event_id, status, target_url, attempts, next_attempt_at)
		 values (?, ?, ?, ?, ?, ?)`,
		"del-legacy-dead", "evt-legacy", "dead", "https://example.com/hook", 5, nil,
	); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := CreateTables(ctx, db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	if err := CreateTables(ctx, db); err != nil {
		t.Fatalf("idempotent migration failed: %v", err)
	}

	var nextType string
	if err := db.QueryRow(
		`select typeof(next_attempt_at) from delivery_jobs where delivery_id = ?`,
		"del-legacy-pending",
	).Scan(&nextType); err != nil {
		t.Fatal(err)
	}
	if nextType != "integer" {
		t.Fatalf("expected integer timestamps after migration, got %s", nextType)
	}

	status, attempts, next, owner := jobRow(t, db, "del-legacy-pending")
	if status != "pending" || attempts != 0 || !next.Valid || owner.Valid {
		t.Fatalf("legacy pending job corrupted: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}
	wantNext := timeToMillis(time.Date(2025, 6, 7, 8, 9, 10, 987654321, time.UTC))
	if next.Int64 != wantNext {
		t.Fatalf("next_attempt_at not converted correctly: got %d want %d", next.Int64, wantNext)
	}

	status, attempts, next, _ = jobRow(t, db, "del-legacy-dead")
	if status != "dead" || attempts != 5 || next.Valid {
		t.Fatalf("legacy dead job corrupted: status=%s attempts=%d next.Valid=%v", status, attempts, next.Valid)
	}

	var rawCreated int64
	if err := db.QueryRow(`select created_at from events where id = 'evt-legacy'`).Scan(&rawCreated); err != nil {
		t.Fatal(err)
	}
	wantCreated := timeToMillis(time.Date(2025, 1, 2, 3, 4, 5, 123456789, time.UTC))
	if rawCreated != wantCreated {
		t.Fatalf("event created_at not converted: got %d want %d", rawCreated, wantCreated)
	}

	detail, err := GetDelivery(ctx, db, "del-legacy-pending")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Payload != `{"legacy": true}` {
		t.Fatalf("payload not preserved: %q", detail.Payload)
	}
	if detail.CreatedAt.IsZero() {
		t.Fatal("delivery created_at not set")
	}

	var attemptTableCount int
	if err := db.QueryRow(
		`select count(*) from sqlite_master where type = 'table' and name = 'delivery_attempts'`,
	).Scan(&attemptTableCount); err != nil {
		t.Fatal(err)
	}
	if attemptTableCount != 1 {
		t.Fatal("delivery_attempts table was not created during migration")
	}

	jobs, err := FetchDueJobs(ctx, db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].DeliveryID != "del-legacy-pending" {
		t.Fatalf("migrated job not fetchable: %+v", jobs)
	}
}
