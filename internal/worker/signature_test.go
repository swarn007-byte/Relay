package worker

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	payload := []byte(`{"amount":500}`)
	secret := []byte(testSecret)
	now := time.Now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := SignaturePrefix + SignPayload(payload, secret, now)

	if err := VerifySignature(payload, secret, timestamp, signature, now, time.Minute); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	payload := []byte(`{"amount":500}`)
	secret := []byte(testSecret)
	now := time.Now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := SignaturePrefix + SignPayload(payload, secret, now)

	tampered := []byte(`{"amount":501}`)
	if err := VerifySignature(tampered, secret, timestamp, signature, now, time.Minute); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	payload := []byte(`{"amount":500}`)
	now := time.Now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := SignaturePrefix + SignPayload(payload, []byte("another-secret-0123456789abcdef0123"), now)

	if err := VerifySignature(payload, []byte(testSecret), timestamp, signature, now, time.Minute); err == nil {
		t.Fatal("signature from another secret accepted")
	}
}

func TestVerifyRejectsTamperedSignature(t *testing.T) {
	payload := []byte(`{"amount":500}`)
	secret := []byte(testSecret)
	now := time.Now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := SignPayload(payload, secret, now)

	last := signature[len(signature)-1]
	flipped := "0"
	if last == '0' {
		flipped = "1"
	}
	tampered := SignaturePrefix + signature[:len(signature)-1] + flipped

	if err := VerifySignature(payload, secret, timestamp, tampered, now, time.Minute); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

func TestVerifyEnforcesReplayWindow(t *testing.T) {
	payload := []byte(`{"amount":500}`)
	secret := []byte(testSecret)
	window := 5 * time.Minute
	now := time.Now()

	sentAt := now.Add(-window - time.Second)
	timestamp := strconv.FormatInt(sentAt.Unix(), 10)
	signature := SignaturePrefix + SignPayload(payload, secret, sentAt)
	if err := VerifySignature(payload, secret, timestamp, signature, now, window); err == nil {
		t.Fatal("expired timestamp accepted")
	}

	sentAt = now.Add(window + time.Second)
	timestamp = strconv.FormatInt(sentAt.Unix(), 10)
	signature = SignaturePrefix + SignPayload(payload, secret, sentAt)
	if err := VerifySignature(payload, secret, timestamp, signature, now, window); err == nil {
		t.Fatal("future timestamp beyond window accepted")
	}

	alignedNow := now.Truncate(time.Second)
	sentAt = alignedNow.Add(-window)
	timestamp = strconv.FormatInt(sentAt.Unix(), 10)
	signature = SignaturePrefix + SignPayload(payload, secret, sentAt)
	if err := VerifySignature(payload, secret, timestamp, signature, alignedNow, window); err != nil {
		t.Fatalf("timestamp at window edge rejected: %v", err)
	}
}

func TestVerifyRejectsMalformedHeaders(t *testing.T) {
	payload := []byte(`{"amount":500}`)
	secret := []byte(testSecret)
	now := time.Now()
	validTimestamp := strconv.FormatInt(now.Unix(), 10)
	validSignature := SignaturePrefix + SignPayload(payload, secret, now)

	cases := []struct {
		name      string
		timestamp string
		signature string
	}{
		{"non-numeric timestamp", "not-a-number", validSignature},
		{"missing prefix", validTimestamp, SignPayload(payload, secret, now)},
		{"invalid hex", validTimestamp, SignaturePrefix + "zzzz"},
		{"truncated signature", validTimestamp, SignaturePrefix + "abcd"},
		{"empty signature", validTimestamp, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifySignature(payload, secret, tc.timestamp, tc.signature, now, time.Minute); err == nil {
				t.Fatal("malformed header accepted")
			}
		})
	}
}

func TestSignatureUsesTimestampAndSecret(t *testing.T) {
	payload := []byte(`{"amount":500}`)
	secretA := []byte(testSecret)
	secretB := []byte("another-secret-0123456789abcdef0123")
	early := time.Unix(1700000000, 0)
	late := time.Unix(1700000060, 0)

	base := SignPayload(payload, secretA, early)
	if base == SignPayload(payload, secretA, late) {
		t.Fatal("signature must change with timestamp")
	}
	if base == SignPayload(payload, secretB, early) {
		t.Fatal("signature must change with secret")
	}
	if base == SignPayload([]byte(`{"amount":501}`), secretA, early) {
		t.Fatal("signature must change with payload")
	}
	if strings.ToLower(base) != base || len(base) != 64 {
		t.Fatalf("expected 64 lowercase hex chars, got %q", base)
	}
}
