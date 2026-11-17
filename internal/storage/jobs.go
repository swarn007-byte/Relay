package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound       = errors.New("delivery not found")
	ErrLeaseLost      = errors.New("delivery job lease lost")
	ErrNotReplayable  = errors.New("delivery is not in dead state")
	ErrActiveDelivery = errors.New("event already has an active delivery")
)

type DeliveryJob struct {
	SigningSecret string
	DeliveryID    string
	EventID       string
	EventType     string
	TargetURL     string
	Payload       string
	Attempts      int
	LeaseToken    string
}

type AttemptOutcome struct {
	AttemptNumber int
	StartedAt     time.Time
	FinishedAt    time.Time
	StatusCode    int
	Success       bool
	Error         string
}

type DeliveryDetail struct {
	DeliveryID     string     `json:"delivery_id"`
	EventID        string     `json:"event_id"`
	EventType      string     `json:"event_type"`
	TargetURL      string     `json:"target_url"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at"`
	LastError      *string    `json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Payload        string     `json:"payload"`
}

type AttemptRecord struct {
	ID            string     `json:"id"`
	DeliveryID    string     `json:"delivery_id"`
	AttemptNumber int        `json:"attempt_number"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    time.Time  `json:"finished_at"`
	DurationMS    int64      `json:"duration_ms"`
	StatusCode    *int       `json:"status_code"`
	Success       bool       `json:"success"`
	Error         string     `json:"error,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
}

func FetchDueJobs(ctx context.Context, db *sql.DB, limit int) ([]DeliveryJob, error) {
	nowMs := timeToMillis(time.Now())

	rows, err := db.QueryContext(
		ctx,
		`select d.delivery_id, d.event_id, e.event_type,
		        d.target_url, d.attempts, e.payload, coalesce((select signing_secret from event_scopes where event_id=e.id), '')
		 from delivery_jobs d
		 join events e on d.event_id = e.id
		 where d.status = 'pending'
		   and d.next_attempt_at is not null
		   and d.next_attempt_at <= ?
		 order by d.next_attempt_at asc, d.delivery_id asc
		 limit ?`,
		nowMs,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []DeliveryJob
	for rows.Next() {
		var job DeliveryJob
		if err := rows.Scan(
			&job.DeliveryID,
			&job.EventID,
			&job.EventType,
			&job.TargetURL,
			&job.Attempts,
			&job.Payload,
			&job.SigningSecret,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func ClaimJob(
	ctx context.Context,
	db *sql.DB,
	deliveryID string,
	leaseToken string,
	leaseTTL time.Duration,
) (bool, error) {
	now := time.Now().UTC()
	result, err := db.ExecContext(
		ctx,
		`update delivery_jobs
		 set status = 'processing',
		     lease_owner = ?,
		     lease_expires_at = ?,
		     updated_at = ?
		 where delivery_id = ?
		   and status = 'pending'
		   and next_attempt_at is not null
		   and next_attempt_at <= ?`,
		leaseToken,
		timeToMillis(now.Add(leaseTTL)),
		timeToMillis(now),
		deliveryID,
		timeToMillis(now),
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func RenewLease(
	ctx context.Context,
	db *sql.DB,
	job DeliveryJob,
	leaseTTL time.Duration,
) (bool, error) {
	now := time.Now().UTC()
	result, err := db.ExecContext(
		ctx,
		`update delivery_jobs
		 set lease_expires_at = ?, updated_at = ?
		 where delivery_id = ?
		   and status = 'processing'
		   and lease_owner = ?`,
		timeToMillis(now.Add(leaseTTL)),
		timeToMillis(now),
		job.DeliveryID,
		job.LeaseToken,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func ReleaseJob(ctx context.Context, db *sql.DB, job DeliveryJob) error {
	now := time.Now().UTC()
	result, err := db.ExecContext(
		ctx,
		`update delivery_jobs
		 set status = 'pending',
		     next_attempt_at = ?,
		     lease_owner = null,
		     lease_expires_at = null,
		     updated_at = ?
		 where delivery_id = ?
		   and status = 'processing'
		   and lease_owner = ?`,
		timeToMillis(now),
		timeToMillis(now),
		job.DeliveryID,
		job.LeaseToken,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", ErrLeaseLost, job.DeliveryID)
	}
	return nil
}

func RecoverExpiredLeases(ctx context.Context, db *sql.DB) (int64, error) {
	now := time.Now().UTC()
	result, err := db.ExecContext(
		ctx,
		`update delivery_jobs
		 set status = 'pending',
		     next_attempt_at = ?,
		     lease_owner = null,
		     lease_expires_at = null,
		     updated_at = ?
		 where status = 'processing'
		   and (lease_expires_at is null or lease_expires_at < ?)`,
		timeToMillis(now),
		timeToMillis(now),
		timeToMillis(now),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func RecordSuccess(
	ctx context.Context,
	db *sql.DB,
	job DeliveryJob,
	out AttemptOutcome,
) error {
	return recordAttempt(ctx, db, job, out, nil, "delivered")
}

func RecordFailure(
	ctx context.Context,
	db *sql.DB,
	job DeliveryJob,
	out AttemptOutcome,
	nextAttemptAt *time.Time,
) error {
	status := "dead"
	if nextAttemptAt != nil {
		status = "pending"
	}
	return recordAttempt(ctx, db, job, out, nextAttemptAt, status)
}

func recordAttempt(
	ctx context.Context,
	db *sql.DB,
	job DeliveryJob,
	out AttemptOutcome,
	nextAttemptAt *time.Time,
	nextStatus string,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var nextMs sql.NullInt64
	if nextAttemptAt != nil {
		nextMs = sql.NullInt64{Int64: timeToMillis(*nextAttemptAt), Valid: true}
	}

	_, err = tx.ExecContext(
		ctx,
		`insert into delivery_attempts
		 (id, delivery_id, attempt_number, started_at, finished_at,
		  duration_ms, status_code, success, error, next_attempt_at)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(),
		job.DeliveryID,
		out.AttemptNumber,
		timeToMillis(out.StartedAt),
		timeToMillis(out.FinishedAt),
		out.FinishedAt.Sub(out.StartedAt).Milliseconds(),
		nullInt(out.StatusCode),
		boolToInt(out.Success),
		nullString(out.Error),
		nextMs,
	)
	if err != nil {
		return err
	}

	nowMs := timeToMillis(time.Now())
	result, err := tx.ExecContext(
		ctx,
		`update delivery_jobs
		 set status = ?,
		     attempts = attempts + 1,
		     next_attempt_at = ?,
		     lease_owner = null,
		     lease_expires_at = null,
		     last_error = ?,
		     updated_at = ?
		 where delivery_id = ?
		   and status = 'processing'
		   and lease_owner = ?`,
		nextStatus,
		nextMs,
		nullString(out.Error),
		nowMs,
		job.DeliveryID,
		job.LeaseToken,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", ErrLeaseLost, job.DeliveryID)
	}

	return tx.Commit()
}

func GetDelivery(ctx context.Context, db *sql.DB, deliveryID string) (DeliveryDetail, error) {
	var d DeliveryDetail
	var nextMs, leaseMs sql.NullInt64
	var createdMs, updatedMs int64
	var lastError sql.NullString

	err := db.QueryRowContext(
		ctx,
		`select d.delivery_id, d.event_id, e.event_type, d.target_url,
		        d.status, d.attempts, d.next_attempt_at, d.lease_expires_at,
		        d.last_error, d.created_at, d.updated_at, e.payload
		 from delivery_jobs d
		 join events e on d.event_id = e.id
		 where d.delivery_id = ?`,
		deliveryID,
	).Scan(
		&d.DeliveryID,
		&d.EventID,
		&d.EventType,
		&d.TargetURL,
		&d.Status,
		&d.Attempts,
		&nextMs,
		&leaseMs,
		&lastError,
		&createdMs,
		&updatedMs,
		&d.Payload,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryDetail{}, fmt.Errorf("%w: %s", ErrNotFound, deliveryID)
	}
	if err != nil {
		return DeliveryDetail{}, err
	}

	if nextMs.Valid {
		t := millisToTime(nextMs.Int64)
		d.NextAttemptAt = &t
	}
	if leaseMs.Valid {
		t := millisToTime(leaseMs.Int64)
		d.LeaseExpiresAt = &t
	}
	if lastError.Valid {
		msg := lastError.String
		d.LastError = &msg
	}
	d.CreatedAt = millisToTime(createdMs)
	d.UpdatedAt = millisToTime(updatedMs)

	return d, nil
}
func ListDeliveries(
	ctx context.Context,
	db *sql.DB,
	status string,
	limit int,
) ([]DeliveryDetail, error) {
	query := `select d.delivery_id, d.event_id, e.event_type, d.target_url,
	                 d.status, d.attempts, d.next_attempt_at, d.lease_expires_at,
	                 d.last_error, d.created_at, d.updated_at, e.payload
	          from delivery_jobs d
	          join events e on d.event_id = e.id`
	args := []interface{}{}
	if status != "" {
		query += " where d.status = ?"
		args = append(args, status)
	}
	query += " order by d.created_at desc, d.delivery_id desc limit ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DeliveryDetail
	for rows.Next() {
		var d DeliveryDetail
		var nextMs, leaseMs, createdMs, updatedMs sql.NullInt64
		var lastError sql.NullString
		if err := rows.Scan(
			&d.DeliveryID,
			&d.EventID,
			&d.EventType,
			&d.TargetURL,
			&d.Status,
			&d.Attempts,
			&nextMs,
			&leaseMs,
			&lastError,
			&createdMs,
			&updatedMs,
			&d.Payload,
		); err != nil {
			return nil, err
		}
		if nextMs.Valid {
			t := millisToTime(nextMs.Int64)
			d.NextAttemptAt = &t
		}
		if leaseMs.Valid {
			t := millisToTime(leaseMs.Int64)
			d.LeaseExpiresAt = &t
		}
		if lastError.Valid {
			msg := lastError.String
			d.LastError = &msg
		}
		d.CreatedAt = millisToTime(createdMs.Int64)
		d.UpdatedAt = millisToTime(updatedMs.Int64)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func GetAttempts(ctx context.Context, db *sql.DB, deliveryID string) ([]AttemptRecord, error) {
	rows, err := db.QueryContext(
		ctx,
		`select id, delivery_id, attempt_number, started_at, finished_at,
		        duration_ms, status_code, success, error, next_attempt_at
		 from delivery_attempts
		 where delivery_id = ?
		 order by attempt_number asc, started_at asc`,
		deliveryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AttemptRecord
	for rows.Next() {
		var a AttemptRecord
		var startedMs, finishedMs int64
		var statusCode sql.NullInt64
		var success int
		var errMsg sql.NullString
		var nextMs sql.NullInt64
		if err := rows.Scan(
			&a.ID,
			&a.DeliveryID,
			&a.AttemptNumber,
			&startedMs,
			&finishedMs,
			&a.DurationMS,
			&statusCode,
			&success,
			&errMsg,
			&nextMs,
		); err != nil {
			return nil, err
		}
		a.StartedAt = millisToTime(startedMs)
		a.FinishedAt = millisToTime(finishedMs)
		if statusCode.Valid {
			code := int(statusCode.Int64)
			a.StatusCode = &code
		}
		a.Success = success == 1
		if errMsg.Valid {
			a.Error = errMsg.String
		}
		if nextMs.Valid {
			t := millisToTime(nextMs.Int64)
			a.NextAttemptAt = &t
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ReplayDelivery(ctx context.Context, db *sql.DB, deliveryID string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var status, eventID, targetURL string
	err = tx.QueryRowContext(
		ctx,
		`select status, event_id, target_url
		 from delivery_jobs where delivery_id = ?`,
		deliveryID,
	).Scan(&status, &eventID, &targetURL)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, deliveryID)
	}
	if err != nil {
		return "", err
	}
	if status != "dead" {
		return "", fmt.Errorf("%w: %s", ErrNotReplayable, deliveryID)
	}

	var active int
	err = tx.QueryRowContext(
		ctx,
		`select count(*)
		 from delivery_jobs
		 where event_id = ? and status in ('pending', 'processing')`,
		eventID,
	).Scan(&active)
	if err != nil {
		return "", err
	}
	if active > 0 {
		return "", fmt.Errorf("%w: %s", ErrActiveDelivery, eventID)
	}

	now := time.Now().UTC()
	newDeliveryID := uuid.NewString()
	_, err = tx.ExecContext(
		ctx,
		`insert into delivery_jobs
		 (delivery_id, event_id, status, target_url, attempts,
		  next_attempt_at, lease_owner, lease_expires_at, last_error,
		  created_at, updated_at)
		 values (?, ?, 'pending', ?, 0, ?, null, null, null, ?, ?)`,
		newDeliveryID,
		eventID,
		targetURL,
		timeToMillis(now),
		timeToMillis(now),
		timeToMillis(now),
	)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newDeliveryID, nil
}

func nullInt(v int) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nullString(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
