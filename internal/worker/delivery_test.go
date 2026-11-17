package worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swarn007-byte/Relay/internal/security"
	"github.com/swarn007-byte/Relay/internal/storage"
)

const testSecret = "test-secret-0123456789abcdef0123456789"

func newTestClient(t *testing.T, timeout time.Duration) *Client {
	t.Helper()
	return NewClient(timeout, security.NewGuard(true, nil), []byte(testSecret))
}

func testJob(targetURL string) storage.DeliveryJob {
	return storage.DeliveryJob{
		DeliveryID: "del_123",
		EventID:    "evt_123",
		EventType:  "payment.success",
		TargetURL:  targetURL,
		Payload:    `{"amount":500}`,
	}
}

func TestDeliverWebhookSuccess(t *testing.T) {
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing JSON content type")
		}
		if r.Header.Get(EventIDHeader) != "evt_123" {
			t.Errorf("missing event id header")
		}
		if r.Header.Get(DeliveryIDHeader) != "del_123" {
			t.Errorf("missing delivery id header")
		}
		if r.Header.Get(EventTypeHeader) != "payment.success" {
			t.Errorf("missing event type header")
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) != `{"amount":500}` {
			t.Errorf("unexpected payload: %q", body)
		}

		timestamp := r.Header.Get(TimestampHeader)
		signature := r.Header.Get(SignatureHeader)
		if err := VerifySignature(body, []byte(testSecret), timestamp, signature, time.Now(), time.Minute); err != nil {
			t.Errorf("signature verification failed: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		received <- struct{}{}
	}))
	defer server.Close()

	res := newTestClient(t, 5*time.Second).Deliver(context.Background(), testJob(server.URL))

	if !res.Success || res.StatusCode != 200 || res.Retryable || res.Canceled || res.Error != "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	<-received
}

func TestDeliverPreservesExactPayloadBytes(t *testing.T) {
	payload := ` { "amount" : 500 , "list" : [1,2,3] } `
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	job := testJob(server.URL)
	job.Payload = payload

	res := newTestClient(t, 5*time.Second).Deliver(context.Background(), job)
	if !res.Success {
		t.Fatalf("delivery failed: %+v", res)
	}
	if got != payload {
		t.Fatalf("payload bytes changed: got %q want %q", got, payload)
	}
}

func TestDeliverStatusClassification(t *testing.T) {
	var redirectHits atomic.Int64
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()

	cases := []struct {
		code      int
		retryable bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusTooManyRequests, true},
		{http.StatusRequestTimeout, true},
		{http.StatusNotFound, false},
		{http.StatusGone, false},
		{http.StatusBadRequest, false},
		{http.StatusFound, false},
	}

	client := newTestClient(t, 5*time.Second)

	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.code == http.StatusFound {
					w.Header().Set("Location", redirectTarget.URL)
				}
				w.WriteHeader(tc.code)
			}))
			defer server.Close()

			res := client.Deliver(context.Background(), testJob(server.URL))
			if res.Success {
				t.Fatalf("expected failure for status %d", tc.code)
			}
			if res.StatusCode != tc.code {
				t.Fatalf("expected status %d, got %d", tc.code, res.StatusCode)
			}
			if res.Retryable != tc.retryable {
				t.Fatalf("expected retryable=%v for status %d, got %v", tc.retryable, tc.code, res.Retryable)
			}
			if !strings.Contains(res.Error, fmt.Sprintf("HTTP %d", tc.code)) {
				t.Fatalf("unexpected error: %q", res.Error)
			}
		})
	}

	if redirectHits.Load() != 0 {
		t.Fatalf("redirect target was contacted %d times", redirectHits.Load())
	}
}

func TestDeliverConnectionErrorIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	res := newTestClient(t, 2*time.Second).Deliver(context.Background(), testJob(url))
	if res.Success || res.Canceled {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !res.Retryable {
		t.Fatalf("connection error should be retryable: %+v", res)
	}
	if res.StatusCode != 0 || res.Error == "" {
		t.Fatalf("expected error details: %+v", res)
	}
}

func TestDeliverBlockedDestinationIsPermanent(t *testing.T) {
	strict := NewClient(2*time.Second, security.NewGuard(false, []int{80, 443}), []byte(testSecret))
	res := strict.Deliver(context.Background(), testJob("http://127.0.0.1:9/hook"))
	if res.Success || res.Retryable {
		t.Fatalf("blocked destination must be permanent: %+v", res)
	}
	if !strings.Contains(res.Error, "not allowed") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

func TestDeliverCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := newTestClient(t, 5*time.Second).Deliver(ctx, testJob("http://127.0.0.1:9/hook"))
	if !res.Canceled || res.Success {
		t.Fatalf("expected canceled result: %+v", res)
	}
	if res.Error != "" || res.Retryable {
		t.Fatalf("canceled delivery must not be a failure: %+v", res)
	}
}

func TestDeliverRequiresSecret(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	client := NewClient(2*time.Second, security.NewGuard(true, nil), nil)
	res := client.Deliver(context.Background(), testJob(server.URL))
	if res.Success || res.Retryable {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.Error, "signing secret") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	if hits.Load() != 0 {
		t.Fatalf("request was sent without a secret")
	}
}

func TestDeliverBoundsResponseRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 2<<20))
	}))
	defer server.Close()

	res := newTestClient(t, 5*time.Second).Deliver(context.Background(), testJob(server.URL))
	if !res.Success {
		t.Fatalf("expected success despite large body: %+v", res)
	}
}
