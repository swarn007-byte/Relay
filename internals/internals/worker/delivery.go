package worker

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/swarn007-byte/Relay/internals/storage"
)

var client = &http.Client{
	Timeout: 10 * time.Second,
}

func DeliverWebhook(
	ctx context.Context,
	job storage.DeliveryJob,
) error {

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		job.TargetURL,
		bytes.NewBufferString(job.Payload),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Event-ID", job.EventID)
	req.Header.Set("X-Relay-Delivery-ID", job.DeliveryID)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}

	return nil
}
