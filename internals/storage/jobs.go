package storage

import (
	"context"
	"database/sql"
	"time"
)

type DeliveryJob struct {
	eventsID   string
	deliveryID string
	attempts   int
	targetUrl  string
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
		`select delivery_id,event_id,target_url,attempts
       from delivery_jobs
       WHERE status = 'pending'
                  AND next_attempt_at <= ?
                ORDER BY next_attempt_at ASC
                LIMIT ?

     `,
		now, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []DeliveryJob

	for rows.Next() {
		var job DeliveryJob

		err := rows.Scan(
			&job.deliveryID,
			&job.eventsID,
			&job.targetUrl,
			&job.attempts,
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
