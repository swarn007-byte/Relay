package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	SaaS                     bool
	PublicURL                string
	Port                     string
	DBPath                   string
	WorkerCount              int
	SchedulerInterval        time.Duration
	HTTPTimeout              time.Duration
	MaxAttempts              int
	RetryBaseDelay           time.Duration
	RetryMaxDelay            time.Duration
	LeaseTTL                 time.Duration
	WebhookSecret            []byte
	AdminToken               string
	AllowPrivateDestinations bool
	AllowedPorts             []int
	ShutdownTimeout          time.Duration
	MaxBodyBytes             int64
	BatchSize                int
}

func Load() (Config, error) {
	cfg := Config{
		PublicURL:                strings.TrimRight(os.Getenv("RELAY_PUBLIC_URL"), "/"),
		SaaS:                     os.Getenv("RELAY_LEGACY_MODE") != "true",
		Port:                     env("RELAY_PORT", "8081"),
		DBPath:                   env("RELAY_DB_PATH", "data/relay.db"),
		WebhookSecret:            []byte(os.Getenv("RELAY_WEBHOOK_SECRET")),
		AdminToken:               os.Getenv("RELAY_ADMIN_TOKEN"),
		AllowPrivateDestinations: false,
	}

	var err error

	if cfg.WorkerCount, err = envInt("RELAY_WORKER_COUNT", 4); err != nil {
		return Config{}, err
	}
	if cfg.MaxAttempts, err = envInt("RELAY_MAX_ATTEMPTS", 5); err != nil {
		return Config{}, err
	}
	if cfg.BatchSize, err = envInt("RELAY_SCHEDULER_BATCH_SIZE", 20); err != nil {
		return Config{}, err
	}
	if cfg.SchedulerInterval, err = envDuration("RELAY_SCHEDULER_INTERVAL", time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HTTPTimeout, err = envDuration("RELAY_HTTP_TIMEOUT", 10*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.RetryBaseDelay, err = envDuration("RELAY_RETRY_BASE_DELAY", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.RetryMaxDelay, err = envDuration("RELAY_RETRY_MAX_DELAY", time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.LeaseTTL, err = envDuration("RELAY_LEASE_TTL", time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = envDuration("RELAY_SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.MaxBodyBytes, err = envInt64("RELAY_MAX_BODY_BYTES", 1<<20); err != nil {
		return Config{}, err
	}
	if cfg.AllowPrivateDestinations, err = envBool("RELAY_ALLOW_PRIVATE_DESTINATIONS", false); err != nil {
		return Config{}, err
	}

	if cfg.AllowedPorts, err = envPorts("RELAY_ALLOWED_PORTS", []int{80, 443}); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("RELAY_PUBLIC_URL must be an HTTP(S) origin without a path, query, or credentials")
		}
	}

	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("RELAY_PORT must be a valid port number, got %q", c.Port)
	}
	if c.DBPath == "" {
		return fmt.Errorf("RELAY_DB_PATH must not be empty")
	}
	if c.WorkerCount < 1 || c.WorkerCount > 1024 {
		return fmt.Errorf("RELAY_WORKER_COUNT must be between 1 and 1024")
	}
	if c.MaxAttempts < 1 {
		return fmt.Errorf("RELAY_MAX_ATTEMPTS must be at least 1")
	}
	if c.BatchSize < 1 {
		return fmt.Errorf("RELAY_SCHEDULER_BATCH_SIZE must be at least 1")
	}
	if c.SchedulerInterval < 100*time.Millisecond {
		return fmt.Errorf("RELAY_SCHEDULER_INTERVAL must be at least 100ms")
	}
	if c.HTTPTimeout < time.Second {
		return fmt.Errorf("RELAY_HTTP_TIMEOUT must be at least 1s")
	}
	if c.RetryBaseDelay < time.Millisecond {
		return fmt.Errorf("RELAY_RETRY_BASE_DELAY must be at least 1ms")
	}
	if c.RetryMaxDelay < c.RetryBaseDelay {
		return fmt.Errorf("RELAY_RETRY_MAX_DELAY must be >= RELAY_RETRY_BASE_DELAY")
	}
	if c.LeaseTTL <= c.HTTPTimeout {
		return fmt.Errorf("RELAY_LEASE_TTL must be greater than RELAY_HTTP_TIMEOUT")
	}
	if c.ShutdownTimeout < time.Second {
		return fmt.Errorf("RELAY_SHUTDOWN_TIMEOUT must be at least 1s")
	}
	if c.MaxBodyBytes < 1024 {
		return fmt.Errorf("RELAY_MAX_BODY_BYTES must be at least 1024")
	}
	if len(c.WebhookSecret) < 32 {
		return fmt.Errorf("RELAY_WEBHOOK_SECRET must contain at least 32 bytes")
	}
	if len(c.AdminToken) > 0 && len(c.AdminToken) < 16 {
		return fmt.Errorf("RELAY_ADMIN_TOKEN must contain at least 16 characters when set")
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}

func envInt64(key string, fallback int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}

func envBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return b, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration (e.g. 10s, 500ms): %w", key, err)
	}
	return d, nil
}

func envPorts(key string, fallback []int) ([]int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	var ports []int
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("%s contains invalid port %q", key, part)
		}
		ports = append(ports, p)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("%s must list at least one port", key)
	}
	return ports, nil
}
