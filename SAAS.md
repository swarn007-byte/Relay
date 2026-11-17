# Relay SaaS application

Relay now serves the marketing page at `/` and the working dashboard at `/app` from the same Go process. SaaS mode is enabled by default. The public Vercel marketing deployment is separate and is not upgraded by changing local files.

## Run locally

Use Go 1.26.4 or later:

```sh
export RELAY_WEBHOOK_SECRET="$(openssl rand -hex 32)"
go run .
```

Open `/app` on port 8081, create an account (password at least 12 characters), create a project, add a public endpoint, and generate a project API key. API keys and endpoint signing secrets are shown once. Save the signing secret in your receiver's secure configuration. The dashboard can submit test events without exposing an API key to the browser. Use `RELAY_ALLOW_PRIVATE_DESTINATIONS=true` only for local receivers during development.

```sh
curl -X POST http://localhost:8081/api/v1/events \
  -H "Authorization: Bearer $RELAY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"event":"payment.success","endpoint_id":"YOUR_ENDPOINT_ID","payload":{"amount":500}}'
```

Accepted events return HTTP 202 and are persisted atomically with a delivery job and project usage. Each project gets 10,000 new events per UTC calendar month. Retry attempts and manual replay do not consume additional events. Limit enforcement is transactional. A failed persistence transaction does not increment usage.

## Authentication and isolation

- Passwords use salted PBKDF2-HMAC-SHA256 with 210,000 iterations. Passwords are never returned.
- Sessions are random 256-bit tokens; only hashes are stored. Cookies are HttpOnly, SameSite=Strict, expire after seven days, and use Secure over HTTPS. Session writes require the `X-Relay-CSRF: 1` header and reject a foreign Origin.
- Authentication attempts are limited to 30 per peer IP per 15 minutes. A trusted reverse proxy may cause multiple users to share this bucket; configure additional edge protection and trusted client IP handling before wider launch.
- Project ownership is checked before endpoints, keys, usage, delivery payloads, attempt logs, and replay are accessed.
- API keys are stored as SHA-256 hashes, project-scoped, and revocable. Endpoint secrets remain server-side after their initial creation response; event records snapshot the secret used by retries and replays.
- SaaS mode removes the old global management routes and requires an API key for `/events`. `POST /api/v1/events` is the recommended ingestion route. Do not enable legacy mode in a public deployment.
- Endpoint disabling stops new submissions; already queued jobs continue. Replay is available only for dead deliveries and retains existing duplicate protection.

## Existing installations

Startup creates additive account/project/key/scope/usage tables without changing or deleting historical event tables. Existing unscoped events stay private to the legacy service and do not become visible to SaaS users. There is no automatic ownership assignment. For the original single-owner API, set `RELAY_LEGACY_MODE=true` and follow README's legacy API instructions. The process-wide `RELAY_WEBHOOK_SECRET` remains required for legacy worker compatibility; SaaS deliveries use endpoint-specific secrets instead.

## Hosting

Run the Go process on a VM or a container host with a durable writable volume at `/data`, for example using the included Dockerfile. Run one instance against the SQLite database. Put HTTPS in front of it; forward `X-Forwarded-Proto: https` from your trusted TLS proxy and prevent direct public access to its internal port. Route both the dashboard and API to the same origin. The application does not enable cross-origin browser access.

Set `RELAY_WEBHOOK_SECRET` in the host's secret settings, mount a durable volume, and set `RELAY_DB_PATH=/data/relay.db`. The container runs as non-root (UID 65532); provision the volume with appropriate ownership. Validate `/health` returns `{"status":"ok"}`, then run the signup-to-delivery flow. Back up the SQLite database and protect it: it contains payloads and signing secrets. Vercel's static hosting alone cannot run this background worker or retain SQLite storage.

## Current scope

Implemented: email/password signup/login/logout, private projects, endpoint registration/disable, API key generation/revocation, test event submission, delivery filtering/detail/attempt history/replay, and monthly usage enforcement. This is a functional first release, not a completed public launch. Email verification, password recovery, paid subscriptions/payment integration, team invitations, managed infrastructure/backups, account deletion, and operational monitoring are not implemented. Do not advertise paid plans or accept subscription payments yet.

## Validation

```sh
go test ./...
go test -race ./...
go build ./...
```

The SaaS integration test exercises two accounts, tenant isolation, CSRF rejection, key authentication/revocation, endpoint disabling, limits, endpoint-specific HMAC verification through real worker dispatch, attempt persistence, replay constraints, logout, and subsequent login. Existing legacy tests continue to run in legacy mode.
