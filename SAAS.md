# Relay account and project API

Relay is an API-only webhook delivery service. The HTML/CSS website and dashboard have been removed. SaaS API mode is enabled by default; `/`, `/app`, and the former asset routes return 404.

## Run

Requires Go 1.26.4 or later:

```sh
export RELAY_WEBHOOK_SECRET="$(openssl rand -hex 32)"
go run .
```

Check `GET /health` on port 8081. Local storage defaults to `data/relay.db`. To use a durable Turso/libSQL database, configure both `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` in your host's secure settings. Incomplete remote configuration fails startup rather than falling back to ephemeral storage. Local records are not uploaded automatically.

## Account and project endpoints

| Endpoint | Purpose |
| --- | --- |
| `POST /api/auth/signup` | Create an account using email and password (12–256 characters) |
| `POST /api/auth/login` | Create a seven-day HttpOnly session cookie |
| `POST /api/auth/logout` | Revoke the session |
| `GET /api/me` | Read the signed-in account |
| `GET /api/projects` | List owned projects |
| `POST /api/projects` | Create a project with a name |
| `GET, POST /api/projects/{project}/endpoints` | List or register endpoints using name and URL |
| `DELETE /api/projects/{project}/endpoints/{id}` | Disable an endpoint for new events |
| `GET, POST /api/projects/{project}/keys` | List or create named project API keys |
| `DELETE /api/projects/{project}/keys/{id}` | Revoke a project key |
| `GET /api/projects/{project}/deliveries` | List latest 100 deliveries; optional status filter |
| `GET /api/projects/{project}/deliveries/{id}` | Read delivery payload and attempt history |
| `POST /api/projects/{project}/deliveries/{id}/replay` | Replay a dead delivery |
| `GET /api/projects/{project}/usage` | Read current UTC month's event allowance and usage |
| `POST /api/projects/{project}/events` | Submit an event with session authentication |

Send JSON with `Content-Type: application/json`. Account/session writes require `X-Relay-CSRF: 1`. If sending an Origin header, it must match the configured `RELAY_PUBLIC_URL` origin, or the request host when that setting is absent. API keys and endpoint signing secrets are returned once at creation.

## Submit events using a project API key

```sh
curl -X POST http://localhost:8081/api/v1/events \
  -H "Authorization: Bearer $RELAY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"event":"payment.success","endpoint_id":"YOUR_ENDPOINT_ID","payload":{"amount":500}}'
```

`POST /events` is an alias. Events, delivery jobs, signing-secret snapshots, and usage are persisted atomically. Each project accepts 10,000 new events per UTC calendar month; retries and replay do not consume additional events. Queued deliveries continue after an endpoint is disabled. Replays retain duplicate protection.

## Security and hosting

Project ownership is enforced before reading or changing tenant resources. Passwords use salted PBKDF2-HMAC-SHA256 (210,000 iterations). Session tokens and API keys are stored as hashes. Each endpoint has its own signing secret. The existing destination validation and signing protections remain active.

Use TLS for public deployment. Sessions are Secure over HTTPS, HttpOnly, and SameSite=Strict. Configure `RELAY_PUBLIC_URL` when a reverse proxy changes the host. Authentication attempts are limited by peer IP; configure trusted edge protections before a public launch. Never expose legacy mode publicly.

The Dockerfile runs as UID 65532. For local SQLite hosting, provide a writable persistent volume at `/data`. The optional `render.yaml` uses a free Render service with a remote Turso database and requires database credentials in Render settings. No website or Vercel proxy is included. Free backend services may sleep and pause delivery. Existing hosted deployments are managed independently of source removal.

To build inside an environment with a custom trusted CA:

```sh
docker build --secret id=ca_bundle,src=/etc/ssl/certs/ca-certificates.crt -t relay .
```

TLS and module checksum verification stay enabled. The CA bundle is only mounted during installation.

## Compatibility and limitations

Startup adds account/project/key/scope/usage tables without deleting historical events. Existing unscoped events remain outside tenant APIs. Set `RELAY_LEGACY_MODE=true` for the original single-owner API documented in README. The process-wide signing secret remains required for legacy worker compatibility.

Email verification, password recovery, paid billing, teams, account deletion, and managed operational monitoring are not implemented.

## Validation

```sh
go test ./...
go test -race ./...
go build ./...
```

The integration tests exercise isolation between two accounts, authentication, CSRF rejection, key revocation, endpoint disabling, usage limits, signed worker dispatch, attempt persistence, and replay constraints. They also verify that the removed website routes return 404.
