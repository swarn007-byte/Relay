package worker

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/swarn007-byte/Relay/internal/storage"
)

type SchedulerOptions struct {
	Interval  time.Duration
	BatchSize int
	LeaseTTL  time.Duration
}

func RunScheduler(
	ctx context.Context,
	db *sql.DB,
	jobs chan<- storage.DeliveryJob,
	opts SchedulerOptions,
) {
	recoverLeases(ctx, db)

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			recoverLeases(ctx, db)
			if err := dispatchDueJobs(ctx, db, jobs, opts); err != nil && ctx.Err() == nil {
				log.Println("Scheduler error:", err)
			}
		case <-ctx.Done():
			log.Println("Scheduler stopped")
			return
		}
	}
}

func recoverLeases(ctx context.Context, db *sql.DB) {
	recovered, err := storage.RecoverExpiredLeases(ctx, db)
	if err != nil {
		if ctx.Err() == nil {
			log.Println("Lease recovery error:", err)
		}
		return
	}
	if recovered > 0 {
		log.Printf("Recovered %d abandoned delivery job(s)", recovered)
	}
}

func dispatchDueJobs(
	ctx context.Context,
	db *sql.DB,
	jobs chan<- storage.DeliveryJob,
	opts SchedulerOptions,
) error {
	pending, err := storage.FetchDueJobs(ctx, db, opts.BatchSize)
	if err != nil {
		return err
	}

	for _, job := range pending {
		leaseToken := uuid.NewString()
		claimed, err := storage.ClaimJob(ctx, db, job.DeliveryID, leaseToken, opts.LeaseTTL)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		job.LeaseToken = leaseToken

		select {
		case jobs <- job:
			log.Println("Scheduled job:", job.DeliveryID)
		case <-ctx.Done():
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := storage.ReleaseJob(releaseCtx, db, job); err != nil {
				log.Println("Could not release claimed job:", err)
			}
			cancel()
			return ctx.Err()
		}
	}
	return nil
}
