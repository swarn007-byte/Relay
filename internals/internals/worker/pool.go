package worker

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"github.com/swarn007-byte/Relay/internals/internals/storage"
)

const MaxAttempts = 5

func StartPool(
	ctx context.Context,
	db *sql.DB,
	jobs <-chan storage.DeliveryJob,
	workerCount int,
	secret []byte,
) *sync.WaitGroup

	var wg sync.WaitGroup

	for i := 1; i <= workerCount; i++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			for job := range jobs {
				log.Printf(
					"Worker %d processing job %s",
					id,
					job.DeliveryID,
				)

				if ctx.Err() != nil {
					log.Printf(
						"Worker %d stopping: context canceled",
						id,
					)
					return
				}

				err := DeliverWebhook(ctx, job,secret)

				dbCtx, cancel := context.WithTimeout(
					context.Background(),
					5*time.Second,
				)

				if err == nil {
					err = storage.MarkDelivered(
						dbCtx,
						db,
						job.DeliveryID,
					)

					if err != nil {
						log.Printf(
							"Failed to mark job %s delivered: %v",
							job.DeliveryID,
							err,
						)
					} else {
						log.Printf(
							"Job %s delivered successfully",
							job.DeliveryID,
						)
					}

				} else {
					log.Printf(
						"Job %s failed: %v",
						job.DeliveryID,
						err,
					)

					currentAttempt := job.Attempts + 1

					if currentAttempt >= MaxAttempts {
						err = storage.MarkDead(
							dbCtx,
							db,
							job.DeliveryID,
						)

						if err != nil {
							log.Printf(
								"Failed to mark job %s dead: %v",
								job.DeliveryID,
								err,
							)
						} else {
							log.Printf(
								"Job %s moved to dead-letter state",
								job.DeliveryID,
							)
						}

					} else {
						nextAttempt := time.Now().Add(
							RetryDelay(currentAttempt),
						)

						err = storage.ScheduleRetry(
							dbCtx,
							db,
							job.DeliveryID,
							nextAttempt,
						)

						if err != nil {
							log.Printf(
								"Failed to schedule retry for %s: %v",
								job.DeliveryID,
								err,
							)
						} else {
							log.Printf(
								"Job %s scheduled for retry at %s",
								job.DeliveryID,
								nextAttempt.UTC().Format(time.RFC3339),
							)
						}
					}
				}

				cancel()
			}
		}(i)
	}

	return &wg
}
