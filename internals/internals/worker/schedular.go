package worker

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/swarn007-byte/Relay/internals/storage"
)

func ScheduleJobs(
	ctx context.Context,
	db *sql.DB,
	jobs chan<- storage.DeliveryJob,
	limit int,

) error {
	pendingJobs, err := storage.FetchPendingJobs(ctx, db, limit)
	if err != nil {
		return err
	}
	for _, job := range pendingJobs {
		claimed, err := storage.ClaimJob(ctx, db, job.DeliveryID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		select {
		case jobs <- job:
			log.Println("Scheduled job:", job.DeliveryID)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func RunSchedular(
	ctx context.Context,
	db *sql.DB,
	jobs chan<- storage.DeliveryJob,
) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			err := ScheduleJobs(ctx, db, jobs, 10)
			if err != nil && ctx.Err() == nil {
				log.Println("Scheduler error:", err)
			}

		case <-ctx.Done():
			log.Println("Scheduler stopped")
			return
		}
	}
	//hum time.sleep bhi use kr skte the but after cancellation
	// it does not receive interrupt it wait for leftover time for new
	// cycle.
}
