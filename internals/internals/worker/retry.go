package worker

import "time"



package worker

import (
	"math/rand"
	"time"
)

func RetryDelay(attempt int) time.Duration {
	delay := 5 * time.Second
	maxDelay := 1 * time.Minute

	for i := 1; i < attempt; i++ {
		delay *= 2

		if delay >= maxDelay {
			delay = maxDelay
			break
		}
	}

	minDelay := delay * 8 / 10
	maxJitter := delay * 4 / 10

	jitter := time.Duration(rand.Int63n(int64(maxJitter) + 1))

	result := minDelay + jitter

	if result > maxDelay {
		return maxDelay
	}

	return result
}
