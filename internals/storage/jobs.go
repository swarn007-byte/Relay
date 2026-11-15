package storage

import (
	"context"
	"database/sql"
	"time"
)

type DeliveryJob struct {
	DeliveryID string
	EventID    string
	TargetURL  string
	Attempts   int
	Payload    string
}

//cuz jb db se row fetch kroge so go need smthing to store those

func FetchPendingJobs(
	ctx context.Context,
	db *sql.DB,
	limit int,
) ([]DeliveryJob, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	rows, err := db.QueryContext(
		ctx,
		`SELECT d.delivery_id, d.event_id,
            d.target_url, d.attempts, e.payload
     FROM delivery_jobs d
     JOIN events e ON d.event_id = e.id
     WHERE d.status = 'pending'
       AND d.next_attempt_at <= ?
     ORDER BY d.next_attempt_at ASC
     LIMIT ?`,
		now,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []DeliveryJob

	for rows.Next() {
		var job DeliveryJob

		err := rows.Scan(
			&job.DeliveryID,
			&job.EventID,
			&job.TargetURL,
			&job.Attempts,
			&job.Payload,
		)
		if err != nil {
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

) (bool, error) {

	result, err := db.ExecContext(
		ctx,
		`update delivery_jobs
		  set status = 'processing'
				where delivery_id=? and status = "pending" and next_attempt_at <=?`,
		deliveryID,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, err
	}

	// `update delivery_jobs
	//   set status = 'processing'
	// 		where delivery_id=?,and status = pending,and next_attempt <=?`,
	// 		deliveryID,
	// 		time.Now().UTC().Format(time.RFC3339Nano),

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected == 1, nil
}
