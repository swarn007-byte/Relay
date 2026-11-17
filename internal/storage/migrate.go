package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func isLegacySchema(ctx context.Context, db *sql.DB) (bool, error) {
	var name string
	err := db.QueryRowContext(
		ctx,
		`select name from sqlite_master where type = 'table' and name = 'delivery_jobs'`,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	rows, err := db.QueryContext(ctx, `pragma table_info(delivery_jobs)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if colName == "lease_owner" {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func migrateLegacySchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `alter table delivery_jobs rename to delivery_jobs_legacy`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `alter table events rename to events_legacy`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}

	if err := copyLegacyEvents(ctx, tx); err != nil {
		return err
	}
	if err := copyLegacyJobs(ctx, tx); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `drop table delivery_jobs_legacy`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `drop table events_legacy`); err != nil {
		return err
	}

	return tx.Commit()
}

func copyLegacyEvents(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `select id, event_type, payload, created_at from events_legacy`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type legacyEvent struct {
		id        string
		eventType string
		payload   string
		createdAt int64
	}
	var events []legacyEvent
	for rows.Next() {
		var e legacyEvent
		var createdAt string
		if err := rows.Scan(&e.id, &e.eventType, &e.payload, &createdAt); err != nil {
			return err
		}
		e.createdAt = parseLegacyTimestamp(createdAt)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, e := range events {
		if _, err := tx.ExecContext(
			ctx,
			`insert into events (id, event_type, payload, created_at) values (?, ?, ?, ?)`,
			e.id, e.eventType, e.payload, e.createdAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func copyLegacyJobs(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(
		ctx,
		`select delivery_id, event_id, status, target_url, attempts, next_attempt_at
		 from delivery_jobs_legacy`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type legacyJob struct {
		deliveryID string
		eventID    string
		status     string
		targetURL  string
		attempts   int
		nextAt     sql.NullString
	}
	var jobs []legacyJob
	for rows.Next() {
		var j legacyJob
		if err := rows.Scan(
			&j.deliveryID, &j.eventID, &j.status, &j.targetURL,
			&j.attempts, &j.nextAt,
		); err != nil {
			return err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	nowMs := timeToMillis(time.Now())
	for _, j := range jobs {
		var exists int
		if err := tx.QueryRowContext(
			ctx, `select count(*) from events where id = ?`, j.eventID,
		).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}

		var nextMs sql.NullInt64
		if j.nextAt.Valid && j.nextAt.String != "" {
			if ms, err := time.Parse(time.RFC3339Nano, j.nextAt.String); err == nil {
				nextMs = sql.NullInt64{Int64: timeToMillis(ms), Valid: true}
			} else if j.status == "pending" || j.status == "processing" {
				nextMs = sql.NullInt64{Int64: nowMs, Valid: true}
			}
		} else if j.status == "pending" || j.status == "processing" {
			nextMs = sql.NullInt64{Int64: nowMs, Valid: true}
		}

		if _, err := tx.ExecContext(
			ctx,
			`insert into delivery_jobs
			 (delivery_id, event_id, status, target_url, attempts,
			  next_attempt_at, lease_owner, lease_expires_at, last_error,
			  created_at, updated_at)
			 values (?, ?, ?, ?, ?, ?, null, null, null, ?, ?)`,
			j.deliveryID, j.eventID, j.status, j.targetURL, j.attempts,
			nextMs, nowMs, nowMs,
		); err != nil {
			return fmt.Errorf("migrate job %s: %w", j.deliveryID, err)
		}
	}
	return nil
}

func parseLegacyTimestamp(v string) int64 {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return timeToMillis(t)
	}
	return timeToMillis(time.Now())
}
