package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

func SaveEvent(
	ctx context.Context,
	db *sql.DB,
	eventID string,
	eventType string,
	targetURL string,
	payload []byte,
) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	nowMs := timeToMillis(now)

	_, err = tx.ExecContext(
		ctx,
		`insert into events (id, event_type, payload, created_at)
		 values (?, ?, ?, ?)`,
		eventID,
		eventType,
		string(payload),
		nowMs,
	)
	if err != nil {
		return "", err
	}

	deliveryID := uuid.NewString()

	_, err = tx.ExecContext(
		ctx,
		`insert into delivery_jobs
		 (delivery_id, event_id, status, target_url, attempts,
		  next_attempt_at, lease_owner, lease_expires_at, last_error,
		  created_at, updated_at)
		 values (?, ?, 'pending', ?, 0, ?, null, null, null, ?, ?)`,
		deliveryID,
		eventID,
		targetURL,
		nowMs,
		nowMs,
		nowMs,
	)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return deliveryID, nil
}
