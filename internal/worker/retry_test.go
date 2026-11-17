package worker

import (
	"testing"
	"time"
)

func TestRetryDelayBounds(t *testing.T) {
	base := 5 * time.Second
	max := 60 * time.Second

	cases := []struct {
		attempt int
		nominal time.Duration
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{5, 60 * time.Second},
		{6, 60 * time.Second},
		{100, 60 * time.Second},
	}

	for _, tc := range cases {
		for i := 0; i < 200; i++ {
			delay := RetryDelay(tc.attempt, base, max)
			lower := tc.nominal * 8 / 10
			upper := tc.nominal * 12 / 10
			if upper > max {
				upper = max
			}
			if delay < lower || delay > upper {
				t.Fatalf("attempt %d: delay %s outside [%s, %s]", tc.attempt, delay, lower, upper)
			}
		}
	}
}

func TestRetryDelayNeverExceedsMax(t *testing.T) {
	base := 5 * time.Second
	max := 60 * time.Second
	for attempt := 0; attempt <= 1000; attempt++ {
		delay := RetryDelay(attempt, base, max)
		if delay <= 0 {
			t.Fatalf("attempt %d: non-positive delay %s", attempt, delay)
		}
		if delay > max {
			t.Fatalf("attempt %d: delay %s exceeds max %s", attempt, delay, max)
		}
	}
}

func TestRetryDelayEdges(t *testing.T) {
	for i := 0; i < 100; i++ {
		d := RetryDelay(0, 5*time.Second, time.Minute)
		if d < 4*time.Second || d > 6*time.Second {
			t.Fatalf("attempt 0 should behave like attempt 1, got %s", d)
		}
		d = RetryDelay(-5, 5*time.Second, time.Minute)
		if d < 4*time.Second || d > 6*time.Second {
			t.Fatalf("negative attempt should behave like attempt 1, got %s", d)
		}
	}

	d := RetryDelay(50, 0, 0)
	if d <= 0 {
		t.Fatalf("zero configuration must fall back to defaults, got %s", d)
	}

	d = RetryDelay(1, 10*time.Second, time.Second)
	if d > 10*time.Second {
		t.Fatalf("max below base must be raised to base, got %s", d)
	}
}
