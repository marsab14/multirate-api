# billing-api

Go 1.25 HTTP API for a small billing / document management service.
Backend-proxied Supabase auth, hand-written SQL, `shopspring/decimal`
for all money math.

## Setup

```bash
cp .env.example .env      # fill in real values
make migrate-up           # apply schema (requires golang-migrate CLI)
make run                  # boots on :4000 by default
curl localhost:4000/health
# => {"ok":true}
```

Requires Go 1.25+ and reachable Postgres (Supabase pooler URI works).

### Install the `migrate` CLI

The Makefile's `migrate-*` targets shell out to the
[`golang-migrate`](https://github.com/golang-migrate/migrate) CLI.
Install once with either:

```bash
brew install golang-migrate
# or
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

### Make targets

| Target | Purpose |
| --- | --- |
| `make run` | Run the API locally. |
| `make test` | `go test ./... -race`. |
| `make tidy` | `go mod tidy`. |
| `make build` | Compile to `bin/api`. |
| `make migrate-up` | Apply all pending migrations. |
| `make migrate-down` | Roll back the most recent migration. |
| `make migrate-status` | Print the current migration version. |

All `migrate-*` targets read `DATABASE_URL` from `.env` by default; pass
`DB_URL=…` on the command line to override for one-off runs.

## Env vars

See `.env.example` for the full list. All non-optional vars are
enforced at boot; a missing required var causes the process to exit
before it starts listening.

| Var | Required | Notes |
| --- | --- | --- |
| `PORT` | no | Defaults to `4000`. |
| `ENV` | no | `development` or `production`. Default `development`. |
| `DATABASE_URL` | yes | Postgres DSN (Supabase pooler recommended). |
| `SUPABASE_URL` | yes | Project URL. |
| `SUPABASE_ANON_KEY` | yes | Sent as `apikey` when proxying auth. |
| `SUPABASE_JWT_SECRET` | yes | HS256 secret for verifying user tokens. |
| `CORS_ORIGIN` | yes | Comma-separated allowed browser origins. |

## Auth architecture

The frontend never talks to Supabase Auth directly. This backend
exposes `/api/auth/{signup,login,logout,refresh}` and forwards to
Supabase over plain HTTP using `SUPABASE_ANON_KEY` as the `apikey`
header. For every other request the frontend sends the Supabase
access token as `Authorization: Bearer <jwt>`; middleware verifies
it against `SUPABASE_JWT_SECRET` (HS256), extracts `sub` and `email`,
and stashes them in `context.Context` for handlers. User identity
never comes from the request body.

Error codes returned by the auth endpoints (the frontend switches on
these):

| Code | Status | Meaning |
| --- | --- | --- |
| `EMAIL_TAKEN` | 409 | Signup — address already registered. |
| `SIGNUP_FAILED` | 400 | Signup — other Supabase error (message forwarded). |
| `INVALID_CREDENTIALS` | 401 | Login — generic; never leaks whether email exists. |
| `EMAIL_NOT_CONFIRMED` | 403 | Login — Supabase requires a confirmation click. |
| `REFRESH_FAILED` | 401 | Refresh — refresh token gone; bounce to `/login`. |
| `UNAUTHORIZED` / `INVALID_TOKEN` / `TOKEN_EXPIRED` | 401 | Bearer token issues on protected routes. |

### Supabase project setup for the demo

Signup returns `{"session": null, "requires_confirmation": true}`
whenever Supabase requires an email confirmation click. For the
demo, disable that in the Supabase dashboard:

> Authentication → Providers → Email → **turn off "Confirm email"**

With confirmation off, `/api/auth/signup` returns a full session
immediately and the frontend can log the user in without leaving
the app.

## Calculation policy

- All money values are `shopspring/decimal.Decimal`. No `float64`
  ever touches money.
- Storage: `NUMERIC(12,2)` in Postgres.
- Rounding: `HALF_UP` to 2dp per line. Document totals sum already
  rounded line totals — no re-rounding at the document level.
- Discount is applied to the line subtotal *before* tax. Tax percent
  applies to the discounted amount.
- `discount_type` is `fixed` | `percent` | `NULL`; exactly one may
  be set. Percents are in `[0, 100]`. Fixed discounts must be
  `<= subtotal` (server rejects with `DISCOUNT_EXCEEDS_SUBTOTAL`,
  never clamps).
- The sample document from the spec must compute to
  `grand_total = 421.50`.

## API

Implemented over batches B5–B9. Every route below is JSON in / JSON
out, versioned informally under `/api`.

- `POST   /api/auth/signup`
- `POST   /api/auth/login`
- `POST   /api/auth/logout`
- `POST   /api/auth/refresh`
- `GET    /api/documents`
- `POST   /api/documents`
- `GET    /api/documents/{id}`
- `PATCH  /api/documents/{id}`
- `POST   /api/documents/{id}/finalize`
- `DELETE /api/documents/{id}`
- `GET    /api/documents/{id}/lines`
- `POST   /api/documents/{id}/lines`
- `PATCH  /api/documents/{id}/lines/{lineId}`
- `DELETE /api/documents/{id}/lines/{lineId}`
- `GET    /api/reports/summary`
- `GET    /health` — unauthenticated liveness probe.

Errors are always shaped as:

```json
{ "error": { "code": "SNAKE_CASE_CODE", "message": "human text", "field": "optional.path" } }
```

## Deploy

Container build arrives in B11 (`Dockerfile`). Any host that can run
a Linux binary and reach Postgres will do; the process is stateless.

## Assumptions

- Supabase Auth is the identity source of truth; no local user
  table beyond what user rows we mirror for foreign keys.
- Single-tenant per user: ownership is `documents.user_id = auth.uid`.
  No teams, no sharing.
- All amounts are in a single currency (not modelled per-row yet).

## What I'd improve

- Structured audit log for finalize / delete.
- Idempotency keys on document mutations.
- OpenAPI spec generated from handler types.
- Per-user rate limiting on auth endpoints.
