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
) *sync.WaitGroup {

	var wg sync.WaitGroup

	for i := 1; i <= workerCount; i++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			for job := range jobs {
				log.Printf("Worker %d processing job %s",
					id, job.DeliveryID)

				err := DeliverWebhook(ctx, job)

				// Use a separate bounded context for DB updates.
				// This lets us attempt to save the result even
				// when the request context was canceled.
				dbCtx, cancel := context.WithTimeout(
					context.Background(),
					5*time.Second,
				)

				if err == nil {
					err = storage.MarkDelivered(
						dbCtx, db, job.DeliveryID,
					)

					if err != nil {
						log.Printf(
							"Failed to mark job %s delivered: %v",
							job.DeliveryID, err,
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
						job.DeliveryID, err,
					)

					nextAttempt := time.Now().Add(
						RetryDelay(job.Attempts + 1),
					)

					retryErr := storage.ScheduleRetry(
						dbCtx,
						db,
						job.DeliveryID,
						nextAttempt,
					)

					if retryErr != nil {
						log.Printf(
							"Failed to schedule retry for %s: %v",
							job.DeliveryID, retryErr,
						)
					}
				}

				cancel()
			}
		}(i)
	}

	return &wg
}
