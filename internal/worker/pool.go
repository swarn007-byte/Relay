package worker

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"github.com/swarn007-byte/Relay/internal/storage"
)

type Options struct {
	MaxAttempts    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	LeaseTTL       time.Duration
	Now            func() time.Time
}

func StartPool(
	ctx context.Context,
	db *sql.DB,
	jobs <-chan storage.DeliveryJob,
	workerCount int,
	client *Client,
	opts Options,
) *sync.WaitGroup {
	if opts.Now == nil {
		opts.Now = time.Now
	}

	var wg sync.WaitGroup
	for i := 1; i <= workerCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for job := range jobs {
				processJob(ctx, db, client, opts, id, job)
			}
		}(i)
	}
	return &wg
}

func processJob(
	ctx context.Context,
	db *sql.DB,
	client *Client,
	opts Options,
	workerID int,
	job storage.DeliveryJob,
) {
	if opts.Now == nil {
		opts.Now = time.Now
	}

	log.Printf("Worker %d processing job %s", workerID, job.DeliveryID)

	dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if ctx.Err() != nil {
		releaseJob(dbCtx, db, job, "shutdown before dispatch")
		return
	}

	renewed, err := storage.RenewLease(dbCtx, db, job, opts.LeaseTTL)
	if err != nil {
		log.Printf("Worker %d could not renew lease for job %s: %v", workerID, job.DeliveryID, err)
		return
	}
	if !renewed {
		log.Printf("Worker %d skipped job %s: lease no longer held", workerID, job.DeliveryID)
		return
	}

	res := client.Deliver(ctx, job)
	if res.Canceled {
		releaseJob(dbCtx, db, job, "delivery canceled during shutdown")
		return
	}

	attemptNumber := job.Attempts + 1
	out := storage.AttemptOutcome{
		AttemptNumber: attemptNumber,
		StartedAt:     res.StartedAt,
		FinishedAt:    res.FinishedAt,
		StatusCode:    res.StatusCode,
		Success:       res.Success,
		Error:         res.Error,
	}

	if res.Success {
		if err := storage.RecordSuccess(dbCtx, db, job, out); err != nil {
			log.Printf("Worker %d failed to record success for job %s: %v", workerID, job.DeliveryID, err)
			return
		}
		log.Printf("Job %s delivered on attempt %d", job.DeliveryID, attemptNumber)
		return
	}

	if !res.Retryable {
		if err := storage.RecordFailure(dbCtx, db, job, out, nil); err != nil {
			log.Printf("Worker %d failed to dead-letter job %s: %v", workerID, job.DeliveryID, err)
			return
		}
		log.Printf("Job %s failed permanently on attempt %d: %s", job.DeliveryID, attemptNumber, res.Error)
		return
	}

	if attemptNumber >= opts.MaxAttempts {
		if err := storage.RecordFailure(dbCtx, db, job, out, nil); err != nil {
			log.Printf("Worker %d failed to dead-letter job %s: %v", workerID, job.DeliveryID, err)
			return
		}
		log.Printf("Job %s exhausted %d attempts and was dead-lettered", job.DeliveryID, attemptNumber)
		return
	}

	delay := RetryDelay(attemptNumber, opts.RetryBaseDelay, opts.RetryMaxDelay)
	nextAttempt := opts.Now().Add(delay)

	if err := storage.RecordFailure(dbCtx, db, job, out, &nextAttempt); err != nil {
		log.Printf("Worker %d failed to schedule retry for job %s: %v", workerID, job.DeliveryID, err)
		return
	}
	log.Printf("Job %s failed on attempt %d, retry scheduled: %s",
		job.DeliveryID, attemptNumber, nextAttempt.Format(time.RFC3339))
}

func releaseJob(ctx context.Context, db *sql.DB, job storage.DeliveryJob, reason string) {
	if err := storage.ReleaseJob(ctx, db, job); err != nil {
		log.Printf("Could not release job %s (%s): %v", job.DeliveryID, reason, err)
		return
	}
	log.Printf("Released job %s back to pending (%s)", job.DeliveryID, reason)
}
