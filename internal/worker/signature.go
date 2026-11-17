package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	EventIDHeader    = "X-Relay-Event-ID"
	DeliveryIDHeader = "X-Relay-Delivery-ID"
	EventTypeHeader  = "X-Relay-Event-Type"
	TimestampHeader  = "X-Relay-Timestamp"
	SignatureHeader  = "X-Relay-Signature"
	SignaturePrefix  = "sha256="
	defaultClockSkew = 5 * time.Minute
)

func SignPayload(payload []byte, secret []byte, timestamp time.Time) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(timestamp.Unix(), 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifySignature(
	payload []byte,
	secret []byte,
	timestampHeader string,
	signatureHeader string,
	now time.Time,
	window time.Duration,
) error {
	if window <= 0 {
		window = defaultClockSkew
	}

	seconds, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp header")
	}
	sentAt := time.Unix(seconds, 0)
	skew := now.Sub(sentAt)
	if skew < 0 {
		skew = -skew
	}
	if skew > window {
		return fmt.Errorf("timestamp outside allowed replay window")
	}

	if !strings.HasPrefix(signatureHeader, SignaturePrefix) {
		return fmt.Errorf("signature must use the %s prefix", SignaturePrefix)
	}
	received, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, SignaturePrefix))
	if err != nil {
		return fmt.Errorf("signature is not valid hex")
	}

	expected, err := hex.DecodeString(SignPayload(payload, secret, sentAt))
	if err != nil {
		return fmt.Errorf("signature computation failed")
	}
	if !hmac.Equal(received, expected) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
