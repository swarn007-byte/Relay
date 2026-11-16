package worker

import "time"

func RetryDelay(attempt int) time.Duration {
	delay := 5 * time.Second
	maxDelay := 1 * time.Minute

	for i := 1; i < attempt; i++ {
		delay *= 2

		if delay >= maxDelay {
			return maxDelay
		}
	}

	return delay
}
