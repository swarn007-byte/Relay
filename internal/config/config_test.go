package config

import (
	"strings"
	"testing"
	"time"
)

const testSecretEnv = "0123456789abcdef0123456789abcdef"

func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"RELAY_PORT",
		"RELAY_DB_PATH",
		"RELAY_WORKER_COUNT",
		"RELAY_SCHEDULER_INTERVAL",
		"RELAY_HTTP_TIMEOUT",
		"RELAY_MAX_ATTEMPTS",
		"RELAY_RETRY_BASE_DELAY",
		"RELAY_RETRY_MAX_DELAY",
		"RELAY_LEASE_TTL",
		"RELAY_WEBHOOK_SECRET",
		"RELAY_ADMIN_TOKEN",
		"RELAY_ALLOW_PRIVATE_DESTINATIONS",
		"RELAY_ALLOWED_PORTS",
		"RELAY_REPLAY_WINDOW",
		"RELAY_SHUTDOWN_TIMEOUT",
		"RELAY_MAX_BODY_BYTES",
		"RELAY_SCHEDULER_BATCH_SIZE",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("RELAY_WEBHOOK_SECRET", testSecretEnv)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != "8081" {
		t.Fatalf("unexpected port: %s", cfg.Port)
	}
	if cfg.DBPath != "data/relay.db" {
		t.Fatalf("unexpected db path: %s", cfg.DBPath)
	}
	if cfg.WorkerCount != 4 || cfg.MaxAttempts != 5 {
		t.Fatalf("unexpected workers/attempts: %d/%d", cfg.WorkerCount, cfg.MaxAttempts)
	}
	if cfg.SchedulerInterval != time.Second || cfg.HTTPTimeout != 10*time.Second {
		t.Fatalf("unexpected interval/timeout: %s/%s", cfg.SchedulerInterval, cfg.HTTPTimeout)
	}
	if cfg.LeaseTTL != time.Minute || cfg.RetryBaseDelay != 5*time.Second || cfg.RetryMaxDelay != time.Minute {
		t.Fatalf("unexpected lease/retry: %s/%s/%s", cfg.LeaseTTL, cfg.RetryBaseDelay, cfg.RetryMaxDelay)
	}
	if cfg.AllowPrivateDestinations {
		t.Fatal("private destinations must be denied by default")
	}
	if len(cfg.AllowedPorts) != 2 {
		t.Fatalf("unexpected allowed ports: %v", cfg.AllowedPorts)
	}
	if string(cfg.WebhookSecret) != testSecretEnv {
		t.Fatal("webhook secret not loaded")
	}
}

func TestLoadRequiresSecret(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RELAY_WEBHOOK_SECRET") {
		t.Fatalf("expected secret validation error, got %v", err)
	}

	t.Setenv("RELAY_WEBHOOK_SECRET", "too-short")
	if _, err := Load(); err == nil {
		t.Fatal("expected short secret to be rejected")
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"bad port", "RELAY_PORT", "not-a-port"},
		{"port out of range", "RELAY_PORT", "70000"},
		{"zero workers", "RELAY_WORKER_COUNT", "0"},
		{"zero attempts", "RELAY_MAX_ATTEMPTS", "0"},
		{"bad interval", "RELAY_SCHEDULER_INTERVAL", "soon"},
		{"short timeout", "RELAY_HTTP_TIMEOUT", "10ms"},
		{"retry max below base", "RELAY_RETRY_MAX_DELAY", "1s"},
		{"lease below timeout", "RELAY_LEASE_TTL", "5s"},
		{"short admin token", "RELAY_ADMIN_TOKEN", "abc"},
		{"bad boolean", "RELAY_ALLOW_PRIVATE_DESTINATIONS", "maybe"},
		{"bad ports", "RELAY_ALLOWED_PORTS", "80,70000"},
		{"tiny body limit", "RELAY_MAX_BODY_BYTES", "10"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("RELAY_WEBHOOK_SECRET", testSecretEnv)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("expected %s=%s to be rejected", tc.key, tc.value)
			}
		})
	}
}

func TestLoadCustomValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("RELAY_WEBHOOK_SECRET", testSecretEnv)
	t.Setenv("RELAY_PORT", "9090")
	t.Setenv("RELAY_WORKER_COUNT", "8")
	t.Setenv("RELAY_SCHEDULER_INTERVAL", "250ms")
	t.Setenv("RELAY_ALLOW_PRIVATE_DESTINATIONS", "true")
	t.Setenv("RELAY_ALLOWED_PORTS", "80,443,8443")
	t.Setenv("RELAY_ADMIN_TOKEN", "admin-token-123456")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != "9090" || cfg.WorkerCount != 8 || cfg.SchedulerInterval != 250*time.Millisecond {
		t.Fatalf("custom values not applied: %+v", cfg)
	}
	if !cfg.AllowPrivateDestinations {
		t.Fatal("allow private not applied")
	}
	if len(cfg.AllowedPorts) != 3 || cfg.AllowedPorts[2] != 8443 {
		t.Fatalf("allowed ports not applied: %v", cfg.AllowedPorts)
	}
	if cfg.AdminToken != "admin-token-123456" {
		t.Fatal("admin token not applied")
	}
}
