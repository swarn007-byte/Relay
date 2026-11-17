package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/swarn007-byte/Relay/internal/security"
	"github.com/swarn007-byte/Relay/internal/storage"
)

type Client struct {
	httpClient *http.Client
	secret     []byte
	timeout    time.Duration
	now        func() time.Time
}

type AttemptResult struct {
	StartedAt  time.Time
	FinishedAt time.Time
	StatusCode int
	Success    bool
	Retryable  bool
	Canceled   bool
	Error      string
}

func NewClient(timeout time.Duration, guard *security.Guard, secret []byte) *Client {
	transport := &http.Transport{
		DialContext:           guard.DialContext,
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: timeout,
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		secret:  secret,
		timeout: timeout,
		now:     time.Now,
	}
}

func (c *Client) Deliver(ctx context.Context, job storage.DeliveryJob) AttemptResult {
	started := c.now()
	res := AttemptResult{StartedAt: started}

	if len(c.secret) == 0 {
		res.FinishedAt = c.now()
		res.Error = "webhook signing secret is not configured"
		return res
	}

	body := []byte(job.Payload)

	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		attemptCtx,
		http.MethodPost,
		job.TargetURL,
		bytes.NewReader(body),
	)
	if err != nil {
		res.FinishedAt = c.now()
		res.Error = sanitizeError(err)
		return res
	}

	timestamp := started.Truncate(time.Second)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(EventIDHeader, job.EventID)
	req.Header.Set(DeliveryIDHeader, job.DeliveryID)
	req.Header.Set(EventTypeHeader, job.EventType)
	req.Header.Set(TimestampHeader, strconv.FormatInt(timestamp.Unix(), 10))
	req.Header.Set(SignatureHeader, SignaturePrefix+SignPayload(body, c.secret, timestamp))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		res.FinishedAt = c.now()
		if ctx.Err() != nil {
			res.Canceled = true
			return res
		}
		res.Error = sanitizeError(err)
		res.Retryable = !errors.Is(err, security.ErrBlocked)
		return res
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	res.StatusCode = resp.StatusCode
	res.FinishedAt = c.now()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.Success = true
		return res
	}

	res.Error = fmt.Sprintf("webhook returned HTTP %d", resp.StatusCode)
	res.Retryable = resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooEarly ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500
	return res
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}

	msg := err.Error()

	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.URL != "" {
		if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil {
			parsed.User = nil
			msg = strings.Replace(msg, urlErr.URL, parsed.String(), 1)
		}
	}

	runes := []rune(msg)
	if len(runes) > 512 {
		msg = string(runes[:512])
	}
	return msg
}
