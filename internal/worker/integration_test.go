package worker

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/swarn007-byte/Relay/internal/storage"
	_ "modernc.org/sqlite"
)

func openWorkerDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.CreateTables(context.Background(), db); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func seedWorkerJob(t *testing.T, db *sql.DB, target string, status string, attempts int, nextAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	eventID := uuid.NewString()
	deliveryID := uuid.NewString()

	if _, err := db.ExecContext(ctx,
		`insert into events (id, event_type, payload, created_at) values (?, ?, ?, ?)`,
		eventID, "payment.success", `{"amount":500}`, time.Now().UTC().UnixMilli(),
	); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	var nextMs interface{}
	if !nextAt.IsZero() {
		nextMs = nextAt.UTC().UnixMilli()
	}
	if _, err := db.ExecContext(ctx,
		`insert into delivery_jobs
		 (delivery_id, event_id, status, target_url, attempts,
		  next_attempt_at, lease_owner, lease_expires_at, last_error,
		  created_at, updated_at)
		 values (?, ?, ?, ?, ?, ?, null, null, null, ?, ?)`,
		deliveryID, eventID, status, target, attempts, nextMs,
		time.Now().UTC().UnixMilli(), time.Now().UTC().UnixMilli(),
	); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return deliveryID
}

func schedulerTestOptions() SchedulerOptions {
	return SchedulerOptions{
		Interval:  20 * time.Millisecond,
		BatchSize: 20,
		LeaseTTL:  time.Minute,
	}
}

func poolTestOptions() Options {
	return Options{
		MaxAttempts:    5,
		RetryBaseDelay: 5 * time.Second,
		RetryMaxDelay:  time.Minute,
		LeaseTTL:       time.Minute,
	}
}

func claimDueJob(t *testing.T, db *sql.DB, jobs chan storage.DeliveryJob) storage.DeliveryJob {
	t.Helper()
	if err := dispatchDueJobs(context.Background(), db, jobs, schedulerTestOptions()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case job := <-jobs:
		return job
	default:
		t.Fatal("no due job was claimed")
		return storage.DeliveryJob{}
	}
}

func readJob(t *testing.T, db *sql.DB, deliveryID string) (status string, attempts int, next sql.NullInt64, owner sql.NullString) {
	t.Helper()
	err := db.QueryRow(
		`select status, attempts, next_attempt_at, lease_owner
		 from delivery_jobs where delivery_id = ?`,
		deliveryID,
	).Scan(&status, &attempts, &next, &owner)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	return
}

func countAttempts(t *testing.T, db *sql.DB, deliveryID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`select count(*) from delivery_attempts where delivery_id = ?`,
		deliveryID,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func TestDeliveryPipeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	jobs := make(chan storage.DeliveryJob, 4)
	job := claimDueJob(t, db, jobs)

	if job.DeliveryID != deliveryID {
		t.Fatalf("claimed wrong job: %s", job.DeliveryID)
	}
	if job.LeaseToken == "" {
		t.Fatal("claimed job has no lease token")
	}

	processJob(context.Background(), db, newTestClient(t, 5*time.Second), poolTestOptions(), 1, job)

	status, attempts, next, owner := readJob(t, db, deliveryID)
	if status != "delivered" || attempts != 1 || next.Valid || owner.Valid {
		t.Fatalf("unexpected state: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}
	if n := countAttempts(t, db, deliveryID); n != 1 {
		t.Fatalf("expected 1 attempt record, got %d", n)
	}
}

func TestFailedDeliverySchedulesRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	jobs := make(chan storage.DeliveryJob, 4)
	job := claimDueJob(t, db, jobs)
	processJob(context.Background(), db, newTestClient(t, 5*time.Second), poolTestOptions(), 1, job)

	status, attempts, next, owner := readJob(t, db, deliveryID)
	if status != "pending" || attempts != 1 || !next.Valid || owner.Valid {
		t.Fatalf("unexpected state: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}

	retryAt := time.UnixMilli(next.Int64)
	now := time.Now()
	if !retryAt.After(now.Add(3 * time.Second)) {
		t.Fatalf("retry not scheduled in the future: %s", retryAt)
	}
	if !retryAt.Before(now.Add(7 * time.Second)) {
		t.Fatalf("retry exceeds base delay bound: %s", retryAt)
	}

	attemptsList, err := storage.GetAttempts(context.Background(), db, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptsList) != 1 {
		t.Fatalf("expected 1 attempt record, got %d", len(attemptsList))
	}
	first := attemptsList[0]
	if first.Success || first.StatusCode == nil || *first.StatusCode != 500 {
		t.Fatalf("unexpected attempt record: %+v", first)
	}
	if first.NextAttemptAt == nil {
		t.Fatal("attempt record missing retry schedule")
	}
}

func TestDeadAfterMaxAttempts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 4, time.Now().Add(-time.Second))

	jobs := make(chan storage.DeliveryJob, 4)
	job := claimDueJob(t, db, jobs)
	if job.Attempts != 4 {
		t.Fatalf("expected job with 4 prior attempts, got %d", job.Attempts)
	}
	processJob(context.Background(), db, newTestClient(t, 5*time.Second), poolTestOptions(), 1, job)

	status, attempts, next, owner := readJob(t, db, deliveryID)
	if status != "dead" || attempts != 5 || next.Valid || owner.Valid {
		t.Fatalf("unexpected state: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}
	if n := countAttempts(t, db, deliveryID); n != 1 {
		t.Fatalf("expected 1 attempt record, got %d", n)
	}

	due, err := storage.FetchDueJobs(context.Background(), db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("dead job must not be scheduled, got %d due jobs", len(due))
	}
}

func TestPermanentFailureDeadImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	jobs := make(chan storage.DeliveryJob, 4)
	job := claimDueJob(t, db, jobs)
	processJob(context.Background(), db, newTestClient(t, 5*time.Second), poolTestOptions(), 1, job)

	status, attempts, _, _ := readJob(t, db, deliveryID)
	if status != "dead" || attempts != 1 {
		t.Fatalf("unexpected state: status=%s attempts=%d", status, attempts)
	}

	attemptsList, err := storage.GetAttempts(context.Background(), db, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptsList) != 1 || attemptsList[0].Success {
		t.Fatalf("unexpected attempts: %+v", attemptsList)
	}
	if !strings.Contains(attemptsList[0].Error, "404") {
		t.Fatalf("unexpected error: %q", attemptsList[0].Error)
	}
}

func TestCrashRecoveryRedispatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	ctx := context.Background()
	if _, err := storage.ClaimJob(ctx, db, deliveryID, "crashed-worker", -time.Second); err != nil {
		t.Fatal(err)
	}
	status, _, _, _ := readJob(t, db, deliveryID)
	if status != "processing" {
		t.Fatalf("expected processing, got %s", status)
	}

	recoverLeases(ctx, db)
	status, _, _, owner := readJob(t, db, deliveryID)
	if status != "pending" || owner.Valid {
		t.Fatalf("crashed job not recovered: status=%s owner.Valid=%v", status, owner.Valid)
	}

	jobs := make(chan storage.DeliveryJob, 4)
	job := claimDueJob(t, db, jobs)
	if job.DeliveryID != deliveryID {
		t.Fatalf("wrong job reclaimed: %s", job.DeliveryID)
	}
	processJob(ctx, db, newTestClient(t, 5*time.Second), poolTestOptions(), 1, job)

	status, attempts, _, _ := readJob(t, db, deliveryID)
	if status != "delivered" || attempts != 1 {
		t.Fatalf("unexpected state after recovery: status=%s attempts=%d", status, attempts)
	}
}

func TestWorkerSkipsLostLease(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	ctx := context.Background()
	if _, err := storage.ClaimJob(ctx, db, deliveryID, "token-old", -time.Second); err != nil {
		t.Fatal(err)
	}
	recoverLeases(ctx, db)
	if _, err := storage.ClaimJob(ctx, db, deliveryID, "token-new", time.Minute); err != nil {
		t.Fatal(err)
	}

	staleJob := storage.DeliveryJob{
		DeliveryID: deliveryID,
		EventID:    "evt",
		TargetURL:  server.URL,
		Payload:    `{}`,
		LeaseToken: "token-old",
	}
	processJob(ctx, db, newTestClient(t, 5*time.Second), poolTestOptions(), 1, staleJob)

	if hits.Load() != 0 {
		t.Fatalf("stale worker delivered the job %d time(s)", hits.Load())
	}
	status, attempts, _, owner := readJob(t, db, deliveryID)
	if status != "processing" || attempts != 0 || owner.String != "token-new" {
		t.Fatalf("stale worker modified state: status=%s attempts=%d owner=%s", status, attempts, owner.String)
	}
}

func TestShutdownReleasesQueuedJobs(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	db := openWorkerDB(t)
	first := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))
	second := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	jobs := make(chan storage.DeliveryJob, 4)
	if err := dispatchDueJobs(context.Background(), db, jobs, schedulerTestOptions()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	wg := StartPool(ctx, db, jobs, 2, newTestClient(t, 5*time.Second), poolTestOptions())
	close(jobs)
	wg.Wait()

	for _, id := range []string{first, second} {
		status, attempts, next, owner := readJob(t, db, id)
		if status != "pending" || attempts != 0 || !next.Valid || owner.Valid {
			t.Fatalf("job %s not released: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
				id, status, attempts, next.Valid, owner.Valid)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("canceled pool delivered %d job(s)", hits.Load())
	}
}

func TestConcurrentDispatchClaimsOnce(t *testing.T) {
	db := openWorkerDB(t)
	const jobCount = 20
	ids := map[string]bool{}
	for i := 0; i < jobCount; i++ {
		id := seedWorkerJob(t, db, "https://example.com/hook", "pending", 0, time.Now().Add(-time.Second))
		ids[id] = true
	}

	jobs := make(chan storage.DeliveryJob, jobCount*2)
	errCh := make(chan error, 4)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := dispatchDueJobs(context.Background(), db, jobs, schedulerTestOptions()); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(jobs)
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent dispatch failed: %v", err)
	}

	seen := map[string]int{}
	for job := range jobs {
		seen[job.DeliveryID]++
	}
	if len(seen) != jobCount {
		t.Fatalf("expected %d claimed jobs, got %d", jobCount, len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("job %s dispatched %d times", id, count)
		}
		if !ids[id] {
			t.Fatalf("unknown job %s dispatched", id)
		}
	}

	var processing int
	if err := db.QueryRow(`select count(*) from delivery_jobs where status = 'processing'`).Scan(&processing); err != nil {
		t.Fatal(err)
	}
	if processing != jobCount {
		t.Fatalf("expected %d processing jobs, got %d", jobCount, processing)
	}
}

func TestSchedulerStopsOnCancel(t *testing.T) {
	db := openWorkerDB(t)
	jobs := make(chan storage.DeliveryJob, 10)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunScheduler(ctx, db, jobs, schedulerTestOptions())
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not stop after cancel")
	}
}

func TestSchedulerEndToEnd(t *testing.T) {
	delivered := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		select {
		case delivered <- r.Header.Get(DeliveryIDHeader):
		default:
		}
	}))
	defer server.Close()

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan storage.DeliveryJob, 10)
	schedulerDone := make(chan struct{})
	go func() {
		RunScheduler(ctx, db, jobs, schedulerTestOptions())
		close(schedulerDone)
	}()

	wg := StartPool(ctx, db, jobs, 2, newTestClient(t, 5*time.Second), poolTestOptions())

	select {
	case got := <-delivered:
		if got != deliveryID {
			t.Fatalf("delivered wrong id: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for end-to-end delivery")
	}

	waitFor(t, "job to be marked delivered", func() bool {
		status, _, _, _ := readJob(t, db, deliveryID)
		return status == "delivered"
	})

	cancel()
	<-schedulerDone
	close(jobs)
	wg.Wait()

	status, attempts, _, _ := readJob(t, db, deliveryID)
	if status != "delivered" || attempts != 1 {
		t.Fatalf("unexpected final state: status=%s attempts=%d", status, attempts)
	}
}

func TestCanceledDeliveryIsNotCountedAsAttempt(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	db := openWorkerDB(t)
	deliveryID := seedWorkerJob(t, db, server.URL, "pending", 0, time.Now().Add(-time.Second))

	jobs := make(chan storage.DeliveryJob, 4)
	job := claimDueJob(t, db, jobs)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	processJob(ctx, db, newTestClient(t, 5*time.Second), poolTestOptions(), 1, job)

	status, attempts, next, owner := readJob(t, db, deliveryID)
	if status != "pending" || attempts != 0 || !next.Valid || owner.Valid {
		t.Fatalf("canceled delivery must be released unchanged: status=%s attempts=%d next.Valid=%v owner.Valid=%v",
			status, attempts, next.Valid, owner.Valid)
	}
	if n := countAttempts(t, db, deliveryID); n != 0 {
		t.Fatalf("canceled delivery must not record an attempt, got %d", n)
	}
}
