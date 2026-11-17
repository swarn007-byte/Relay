package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/swarn007-byte/Relay/internals/internals/storage"
)

var client = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(
		req *http.Request,
		via []*http.Request,
	) error {
		return http.ErrUseLastResponse
	},
}

func DeliverWebhook(
	ctx context.Context,
	job storage.DeliveryJob,
	secret []byte,
) error {
	if len(secret) == 0 {
		return errors.New("webhook signing secret is required")
	}

	body := []byte(job.Payload)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		job.TargetURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Event-ID", job.EventID)
	req.Header.Set("X-Relay-Delivery-ID", job.DeliveryID)

	signature := SignPayload(body, secret)
	req.Header.Set("X-Relay-Signature", "sha256="+signature)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, _ = io.Copy(
		io.Discard,
		io.LimitReader(resp.Body, 64<<10),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf(
			"webhook returned HTTP %d",
			resp.StatusCode,
		)
	}

	return nil
}
