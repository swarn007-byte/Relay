package worker

import (
	"math"
	"math/rand"
	"time"
)

func RetryDelay(attempt int, base time.Duration, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if base <= 0 {
		base = time.Second
	}
	if max < base {
		max = base
	}

	delay := int64(base)
	for i := 1; i < attempt; i++ {
		if delay >= int64(max)/2 {
			delay = int64(max)
			break
		}
		delay *= 2
	}
	if delay > int64(max) {
		delay = int64(max)
	}
	if delay > math.MaxInt64/4 {
		delay = math.MaxInt64 / 4
	}

	minDelay := delay * 8 / 10
	jitterSpan := delay*4/10 + 1
	result := minDelay + rand.Int63n(jitterSpan)

	if result > int64(max) {
		result = int64(max)
	}
	return time.Duration(result)
}
