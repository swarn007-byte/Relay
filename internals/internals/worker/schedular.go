package worker
import (
    "context"
    "database/sql"
    "log"

    "github.com/swarn007-byte/Relay/internals/storage"
)

func ScheduleJobs(
	ctx context.Context,
	db *sql.DB,
	jobs chan<-storage.DeliveryJob,
	limit int

)error{
	pendingJobs,err:=storage.FetchPendingJobs(ctx,db,limit)
	if err!=nil{
		return err
	}
	for _,job:= range(pendingJobs){
		claimed,err:=storage.ClaimJob(ctx,db,job.DeliveryID)
		if err!=nil{
			return err
		}
		if !claimed{
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
