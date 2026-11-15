package worker

import (
    "log"
    "sync"

    "github.com/swarn007-byte/Relay/internals/storage"
)

func StartPool(
    jobs <-chan storage.DeliveryJob,
    workerCount int,
) *sync.WaitGroup {

    var wg sync.WaitGroup

    for i := 1; i <= workerCount; i++ {
        wg.Add(1)

        go func(id int) {
            defer wg.Done()

            for job := range jobs {
                log.Printf(
                    "Worker %d received job %s",
                    id,
                    job.DeliveryID,
                )
            }
        }(i)
    }

    return &wg
}