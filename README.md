> **API service:** The website and dashboard have been removed. See [SAAS.md](SAAS.md) for the account/project API. The single-owner API examples below require `RELAY_LEGACY_MODE=true`.

# Relay

Relay is a self-hosted webhook delivery service written in Go.

Applications submit events to Relay over HTTP. Relay durably stores each event together with a
delivery job in SQLite, schedules due jobs, performs signed HTTP POST requests to the configured
target endpoint, retries transient failures with bounded exponential backoff, records every
delivery attempt, and exposes an authenticated API for inspecting and replaying failed deliveries.

Relay is **not** a tunnel, message broker replacement, or generic distributed task framework. It is
a focused, reliable webhook delivery backend.

## Why Relay

Sending webhooks looks trivial until you ask the questions that matter in production:

- What happens if the receiver is down when the event fires?
- What happens if Relay crashes in the middle of a delivery?
- How do I know which deliveries failed and why?
- How does the receiver know the webhook really came from me?
- What happens when an attacker makes my server send requests to internal infrastructure?

Relay answers all of these: durable jobs with an atomic claim protocol, lease-based crash
recovery, per-attempt history, HMAC-SHA256 request signing with replay protection, and SSRF
protection that validates every actual dial target.

## Architecture

```text
External Application
        |
        | POST /events
        v
    Relay HTTP API (internal/api)
        |
        | validate event + destination (internal/security)
        v
    SQLite (internal/storage)
        |
        | atomic event + delivery job insert (WAL mode)
        v
    Job Scheduler (internal/worker/scheduler.go)
        |
        | fetch due pending jobs, recover expired leases,
        | atomically claim with a lease token
        v
    Bounded Worker Pool (internal/worker/pool.go)
        |
        | renew lease, execute delivery
        v
    HTTP Delivery Client (internal/worker/delivery.go)
        |
        | guarded dialer, signed POST, fixed timeout
        v
    Target Webhook Endpoint
        |
        | response
        v
    Delivery State Manager (internal/storage)
        |
        +--- 2xx -----------> delivered
        |
        +--- retryable ------> pending (future next_attempt_at)
        |
        +--- exhausted -------> dead
        |
        +--- non-retryable ---> dead (immediately)
```

### Project layout

```text
main.go                      wiring: config, DB, API, pool, scheduler, shutdown
internal/
  config/config.go           environment configuration and validation
  security/ssrf.go           URL validation, IP range blocking, guarded dialer
  storage/
    schema.sql               schema (embedded)
    sqlite.go                connection setup, pragmas, schema bootstrap
    events.go                atomic event + job persistence
    jobs.go                  claims, leases, state transitions, queries, replay
    migrate.go               automatic legacy schema migration
  worker/
    scheduler.go             due-job dispatch loop + lease recovery
    pool.go                  bounded workers and state machine execution
    delivery.go              outbound HTTP client and failure classification
    retry.go                 bounded exponential backoff with jitter
    signature.go             HMAC-SHA256 signing and verification helper
  api/server.go              HTTP handlers, routing, admin authentication
```

## Tech stack

- **Go 1.26+**, standard library only for HTTP, crypto, signal handling, and routing
- **modernc.org/sqlite** — pure-Go SQLite driver (no CGO)
- **github.com/google/uuid** — identifier generation
- SQLite **WAL mode** with busy timeout and foreign-key enforcement

## Features

- `POST /events` accepting JSON events, `202 Accepted` after durable persistence
- Atomic event + delivery-job insert in a single transaction
- Continuously running scheduler with atomic job claiming
- Bounded worker pool (no goroutine-per-delivery)
- Signed outgoing webhooks: HMAC-SHA256 over `timestamp.payload` with replay-window verification
- Exponential backoff with jitter, capped at a maximum delay
- Attempt counter semantics: first success is attempt 1, fifth failure is attempt 5
- Dead-letter state after exhausted retries or permanent failures
- Persistent per-attempt history (status code, duration, sanitized error, scheduled retry)
- Lease-based crash recovery for jobs abandoned by crashed workers
- Manual replay of dead deliveries without concurrent duplicates
- Authenticated inspection API (`GET /deliveries`, attempts, replay)
- SSRF protection: scheme/port/IP-range validation at ingest **and** at dial time, no redirects,
  no proxy bypass
- Graceful shutdown on SIGINT/SIGTERM with job release and bounded drain
- Automatic migration of the legacy TEXT-timestamp schema (no data loss)

## Installation

Requires Go 1.26 or newer.

```bash
git clone https://github.com/swarn007-byte/Relay.git
cd Relay
go build ./...
```

## Configuration

All configuration is read from environment variables. Values are validated at startup; the
process refuses to start with an invalid configuration.

| Variable | Default | Description |
|---|---|---|
| `RELAY_PORT` | `8081` | HTTP listen port |
| `RELAY_DB_PATH` | `data/relay.db` | SQLite database file (directory is created) |
| `RELAY_WORKER_COUNT` | `4` | Number of delivery workers (1–1024) |
| `RELAY_SCHEDULER_INTERVAL` | `1s` | Scheduler tick interval (min `100ms`) |
| `RELAY_SCHEDULER_BATCH_SIZE` | `20` | Max jobs claimed per tick |
| `RELAY_HTTP_TIMEOUT` | `10s` | Per-delivery HTTP timeout (min `1s`) |
| `RELAY_MAX_ATTEMPTS` | `5` | Max delivery attempts before dead-letter (min `1`) |
| `RELAY_RETRY_BASE_DELAY` | `5s` | First retry delay |
| `RELAY_RETRY_MAX_DELAY` | `60s` | Retry delay cap (must be ≥ base delay) |
| `RELAY_LEASE_TTL` | `60s` | Claim lease duration (must be > HTTP timeout) |
| `RELAY_WEBHOOK_SECRET` | — | **Required**, ≥ 32 bytes. HMAC signing secret |
| `RELAY_ADMIN_TOKEN` | — | Bearer token for the management API. If unset, the management API is disabled |
| `RELAY_ALLOW_PRIVATE_DESTINATIONS` | `false` | Dev-only: allow private/loopback destinations |
| `RELAY_ALLOWED_PORTS` | `80,443` | Allowed destination ports (ignored in dev mode) |
| `RELAY_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown deadline |
| `RELAY_MAX_BODY_BYTES` | `1048576` | Max request body size for `POST /events` |

See [`.env.example`](.env.example) for a complete template. Never commit real secrets.

## Running

```bash
export RELAY_WEBHOOK_SECRET="$(openssl rand -hex 32)"
export RELAY_ADMIN_TOKEN="$(openssl rand -hex 16)"
go run .
```

For local development against `127.0.0.1` receivers, also set:

```bash
export RELAY_ALLOW_PRIVATE_DESTINATIONS=true
```

**Never enable `RELAY_ALLOW_PRIVATE_DESTINATIONS` in production.** It disables the SSRF guard.

Expected startup output:

```text
Database initialized
Relay server running on port 8081
```

Stop with `Ctrl-C` or `SIGTERM`; Relay drains gracefully:

```text
Shutdown signal received
Scheduler stopped
Shutdown complete
```

## Submitting an event

```bash
curl -i -X POST http://localhost:8081/events \
  -H 'Content-Type: application/json' \
  -d '{
    "event": "payment.success",
    "target_url": "https://example.com/webhook",
    "payload": {"payment_id": "pay_123", "amount": 500}
  }'
```

Response (`202 Accepted` — the event is durable, the webhook has **not** been sent yet):

```json
{
  "event_id": "6a36fb5f-7e5c-487e-8eaf-f6ff5aec7261",
  "delivery_id": "a0fe45e6-95a0-40f5-b4c0-3127c602c1e2",
  "status": "accepted"
}
```

Validation failures return `400` with `{"error": "..."}`. Wrong or missing `Content-Type` returns
`415`; bodies larger than `RELAY_MAX_BODY_BYTES` return `413`. Destinations that fail SSRF
validation are rejected at ingest time.

## Delivery lifecycle

```text
pending --> processing --> delivered
                |
                +-------> pending   (retryable failure, future next_attempt_at)
                |
                +-------> dead      (attempts exhausted OR non-retryable failure)
```

Every transition is enforced by a conditional `UPDATE` guarded by the job's lease token, plus a
`CHECK` constraint on the status column — silent invalid transitions are impossible.

1. A new event creates a `pending` job with `next_attempt_at = now`.
2. The scheduler fetches due pending jobs, recovers expired leases, and atomically claims each job
   (`pending → processing`, assigning `lease_owner` and `lease_expires_at`).
3. Before sending, the worker renews the lease. If the lease was lost (recovered elsewhere), the
   worker skips the job without touching it.
4. The worker performs a signed HTTP POST with a fixed timeout.
5. The outcome is persisted **in one transaction**: an `delivery_attempts` row plus the job
   transition.

### Attempt accounting

`attempts` counts completed delivery attempts:

| Scenario | status | attempts |
|---|---|---|
| First delivery succeeds | `delivered` | `1` |
| Fifth delivery fails (retryable) | `dead` | `5` |
| First delivery hits a permanent failure (e.g. HTTP 404) | `dead` | `1` |
| Delivery canceled by shutdown | `pending` | unchanged (no attempt recorded) |

### Retry policy

| Attempt | Nominal delay | Jittered range |
|---|---|---|
| 1 | 5s | 4–6s |
| 2 | 10s | 8–12s |
| 3 | 20s | 16–24s |
| 4 | 40s | 32–48s |
| 5+ | 60s (cap) | 48–60s |

Jitter is uniform over ±20% of the nominal delay, capped at `RELAY_RETRY_MAX_DELAY`, and the
computation is overflow-safe. The next attempt time is persisted in SQLite, so **restarting Relay
does not lose retry schedules**.

**Failure classification:**

- **Retryable:** connection/DNS errors, timeouts, HTTP `408`, `425`, `429`, and all `5xx`
- **Permanent (dead immediately):** other `3xx`/`4xx` responses, SSRF-blocked destinations,
  malformed requests

Redirects are **never followed** (`CheckRedirect` returns the last response), so a `3xx` is treated
as a permanent misconfiguration rather than an open redirect vector.

## HMAC-SHA256 webhook verification

Every outgoing request carries:

```http
POST /webhook HTTP/1.1
Content-Type: application/json
X-Relay-Event-ID: 6a36fb5f-7e5c-487e-8eaf-f6ff5aec7261
X-Relay-Delivery-ID: a0fe45e6-95a0-40f5-b4c0-3127c602c1e2
X-Relay-Event-Type: payment.success
X-Relay-Timestamp: 1794934774
X-Relay-Signature: sha256=d97dc8ed42fa935d6eb6d440cfa4e0580f647a7154d72a89f9d5cbade6edba7d
```

**Canonical signing format (exact):**

```text
signed_payload = unix_seconds + "." + exact_request_body_bytes
signature      = "sha256=" + hex(HMAC_SHA256(RELAY_WEBHOOK_SECRET, signed_payload))
```

`unix_seconds` is the integer value of `X-Relay-Timestamp`. The signature covers the **exact bytes
transmitted** — the payload is never re-serialized.

### Verifying in Go

```go
func verifyRelay(body []byte, secret []byte, tsHeader, sigHeader string, now time.Time, window time.Duration) error {
	seconds, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return err
	}
	sentAt := time.Unix(seconds, 0)
	if skew := now.Sub(sentAt); skew > window || skew < -window {
		return errors.New("timestamp outside replay window")
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(tsHeader))
	mac.Write([]byte("."))
	mac.Write(body)
	expected, _ := hex.DecodeString(strings.TrimPrefix(sigHeader, "sha256="))

	if !hmac.Equal(expected, mac.Sum(nil)) {
		return errors.New("signature mismatch")
	}
	return nil
}
```

Rules for consumers:

1. Reject requests whose `X-Relay-Timestamp` is outside your replay window (e.g. ±5 minutes).
2. Compare with a **constant-time** comparison (`hmac.Equal`).
3. Compute the signature over the **raw body bytes**, before any JSON parsing.
4. A valid signature proves origin and integrity — it does **not** prevent replay outside the
   window, so still make your handlers idempotent.

**Secret scope:** Relay currently uses a single global `RELAY_WEBHOOK_SECRET`. It does **not**
provide per-destination isolation — every destination sees the same secret. Rotate it by updating
the environment variable and restarting Relay. Per-destination secrets are listed under future
improvements.

## Dead-letter handling

A job becomes `dead` when:

- `attempts` reaches `RELAY_MAX_ATTEMPTS`, or
- the failure is non-retryable (permanent `3xx`/`4xx`, blocked destination).

Dead jobs are excluded from automatic scheduling. The last failure reason stays in
`last_error`, and the full attempt history is preserved in `delivery_attempts`.

```bash
curl -H "Authorization: Bearer $RELAY_ADMIN_TOKEN" \
  "http://localhost:8081/deliveries?status=dead"
```

## Manual replay

Replay creates a **new** `pending` delivery job for the same event:

- The original dead job is never modified — its history stays intact.
- Replay is rejected with `409 Conflict` if the event already has a `pending` or `processing`
  job, so a replay can never create two concurrent active deliveries for one event.
- Only `dead` jobs can be replayed (`409` otherwise).
- The replayed job starts with `attempts = 0` and gets its own attempt history.

```bash
curl -X POST -H "Authorization: Bearer $RELAY_ADMIN_TOKEN" \
  http://localhost:8081/deliveries/<delivery-id>/replay
```

```json
{"delivery_id": "<new-id>", "status": "pending", "replayed": "<old-id>"}
```

## API reference

All `/deliveries*` endpoints require `Authorization: Bearer $RELAY_ADMIN_TOKEN`. If
`RELAY_ADMIN_TOKEN` is unset they return `403` (management API disabled). Unknown endpoints
return JSON `404`.

### `GET /health`

```json
{"status": "ok"}
```

### `POST /events`

Body: `{"event": string, "target_url": string, "payload": <any JSON value>}` → `202` with
`{"event_id", "delivery_id", "status"}`.

### `GET /deliveries?status=<pending|processing|delivered|dead>&limit=<1-200>`

```json
{"deliveries": [ { "delivery_id": "...", "status": "delivered", "attempts": 1, ... } ]}
```

### `GET /deliveries/{id}`

Full detail including `event_type`, `target_url`, `next_attempt_at`, `last_error`, and `payload`.
`404` if unknown, `400` if the id is not a UUID.

### `GET /deliveries/{id}/attempts`

```json
{"attempts": [{
  "id": "...",
  "attempt_number": 1,
  "started_at": "2026-11-17T16:59:34.818Z",
  "finished_at": "2026-11-17T16:59:34.82Z",
  "duration_ms": 1,
  "status_code": 200,
  "success": true,
  "next_attempt_at": null
}]}
```

### `POST /deliveries/{id}/replay`

`202` on success, `404` unknown id, `409` if not dead or if an active delivery already exists.

## Crash recovery

If Relay crashes after claiming a job (`processing`) but before recording the outcome, the job
would be stuck forever. Relay prevents this with **leases**:

- Every claim writes `lease_owner` (a unique token) and `lease_expires_at = now + RELAY_LEASE_TTL`.
- Expired `processing` jobs are reset to `pending` **on startup and on every scheduler tick**.
- Every state transition is guarded by `WHERE lease_owner = <token>`; a stale worker whose lease
  was reclaimed gets `ErrLeaseLost` and cannot overwrite the newer state.
- Workers renew the lease immediately before sending, so a job queued longer than the TTL is not
  delivered by a worker that no longer owns it.
- A `processing` row with no lease (e.g. migrated from the legacy schema) is recovered on startup.

**At-least-once semantics.** Relay guarantees at-least-once delivery, never exactly-once. If Relay
crashes after the target accepted a request but before SQLite recorded the success, the job will be
retried and the target may receive a duplicate. **Webhook consumers must be idempotent.**

## Graceful shutdown

On `SIGINT`/`SIGTERM`:

1. The HTTP server stops accepting new requests (`Server.Shutdown`).
2. The scheduler stops claiming new jobs and exits.
3. The jobs channel is closed; queued jobs that were claimed but not dispatched are **released
   back to `pending`** (not counted as attempts).
4. In-flight deliveries are canceled by context and released back to `pending`.
5. Workers are awaited with a `RELAY_SHUTDOWN_TIMEOUT` deadline.
6. The database is closed only after dependent goroutines stop.

Canceled deliveries are never recorded as ordinary failures.

## SQLite reliability

- `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`
- Connection pool capped (`MaxOpenConns=16`)
- All timestamps are **integer Unix milliseconds** — sorting and comparison are exact
- Parameterized SQL everywhere; atomic multi-statement transactions for every lifecycle change
- Indexes: `(status, next_attempt_at)` for dispatch, `(status, lease_expires_at)` for recovery,
  `(event_id)` for replay checks, `(delivery_id, attempt_number)` for history
- Legacy databases that still use TEXT/RFC3339 timestamps are migrated automatically inside a
  transaction on first start; existing rows are preserved

## Testing

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./...
go test -race ./...
```

The suite covers, without any external network access:

- event validation and atomic event+job persistence
- due-job fetching, atomic claiming, concurrent dispatch without duplicate claims
- successful HTTP delivery, exact payload bytes, response classification (2xx/3xx/4xx/5xx)
- retry scheduling, backoff and jitter bounds, maximum attempts, dead-letter transitions
- HMAC generation/verification, tampered payload/secret rejection, replay-window enforcement
- crash recovery, stale-worker protection, lease expiry
- graceful shutdown: queued-job release, canceled delivery not counted as attempt
- manual replay semantics and conflicts
- SSRF defense: private/loopback/link-local/multicast/metadata ranges (IPv4 + IPv6), credential
  URLs, port restrictions, redirect non-following, DNS rebinding at dial time, proxy bypass
- configuration validation and legacy schema migration

## Security notes and limitations

Implemented:

- SSRF validation at ingest **and** at dial time; the dialer resolves the host, checks every
  address, then dials the validated IP directly (no TOCTOU/rebinding window)
- Only `http`/`https`; no credentials in URLs; default port allowlist (`80,443`)
- Blocked ranges include loopback, RFC1918, CGNAT, link-local/`169.254.169.254` metadata, ULA,
  multicast, unspecified, documentation, NAT64/6to4/Teredo prefixes
- `Proxy: nil` on the delivery transport — `HTTP_PROXY` cannot bypass the guard
- Redirects are never followed
- Request body limits, strict `Content-Type`, single-JSON-value enforcement
- HTTP server read/write/idle timeouts
- Admin API with constant-time token comparison; disabled when unconfigured
- Secrets are never logged; stored errors are truncated to 512 characters and never contain
  credentials

Limitations (be aware before production use):

- **Single global signing secret** — no per-destination isolation or rotation without restart
- **No rate limiting or TLS** on the management API — put Relay behind a reverse proxy
- **At-least-once** — duplicates are possible; consumers must be idempotent
- **Ingest-time DNS resolution** — an event whose host fails to resolve right now is rejected with
  `400` (the dial-time check still applies on every attempt)
- **Single node** — SQLite and one process; no clustering or HA
- The management API has no pagination cursors (bounded `limit` only)

## Future improvements

- Per-destination signing secrets and rotation
- Rate limiting and TLS termination for the management API
- Metrics endpoint (Prometheus) and structured logging
- Delivery dashboard
- Pagination cursors for listing endpoints
- Multi-node scheduling on top of the existing lease protocol
