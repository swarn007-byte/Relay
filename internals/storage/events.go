package storage

import (
	"context"
	"database/sql"
	"time"

	// "uuid"

	"github.com/google/uuid"
)

func SaveEvent(
	ctx context.Context,
	db *sql.DB,
	eventID string,
	eventType string,
	targetURL string,
	payload []byte,
) error {

	//begin the transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	//next is insert this into event table
	// Insert event
	_, err = tx.ExecContext(
		ctx,
		`insert into events
		(id, event_type, payload, created_at)
		values (?, ?, ?, ?)`,
		eventID,
		eventType,
		string(payload),
		now,
	)
	if err != nil {
		return err
	}

	// insert into events(id,event_type,payload,created_at)
	// values(eventID,eventType,payload,time.Now())

	delivery_ID := uuid.NewString()

	//insert into delivery job table
	_, err = tx.ExecContext(
		ctx,
		`insert into delivery_jobs
			(delivery_id, event_id, status, target_url, attempts, next_attempt_at)
			values (?, ?, ?, ?, ?, ?)`,
		delivery_ID,
		eventID,
		"pending",
		targetURL,
		0,
		now,
	)
	if err != nil {
		return err
	}

	//next is insert into delivery job table

	// insert into dedelivery_jobs(delivery_id,event_id,status,target_url,attempts,next_attempt_at)
	// values()

	return tx.Commit()
}
