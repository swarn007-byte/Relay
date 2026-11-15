package worker

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/swarn007-byte/Relay/internals/storage"
)

func TestDeliverWebhook(t *testing.T) {
    // Temporary receiving server
    server := httptest.NewServer(http.HandlerFunc(
        func(w http.ResponseWriter, r *http.Request) {
            if r.Method != http.MethodPost {
                t.Errorf("expected POST, got %s", r.Method)
            }

            if r.Header.Get("Content-Type") != "application/json" {
                t.Error("missing JSON content type")
            }

            w.WriteHeader(http.StatusOK)
        },
    ))
    defer server.Close()

    // Sample delivery job
    job := storage.DeliveryJob{
        DeliveryID: "del_123",
        EventID:    "evt_123",
        TargetURL:  server.URL,
        Payload:    `{"amount":500}`,
    }

    // Execute delivery
    err := DeliverWebhook(context.Background(), job)
    if err != nil {
        t.Fatalf("delivery failed: %v", err)
    }
}