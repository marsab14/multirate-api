# Billing API (Go)

Backend for the billing / document management take-home. Companion
frontend: `billing-web` (Vite + React).

## Stack

- Go 1.25+
- [chi](https://github.com/go-chi/chi) router with the standard
  middleware stack (RequestID, RealIP, Recoverer, CORS, slog request
  logger, content-type)
- [sqlx](https://github.com/jmoiron/sqlx) + hand-written SQL — no ORM
- [shopspring/decimal](https://github.com/shopspring/decimal) for
  every money value; `NUMERIC(12, 2)` in Postgres
- [golang-jwt/jwt/v5](https://github.com/golang-jwt/jwt) +
  [MicahParks/keyfunc/v3](https://github.com/MicahParks/keyfunc) for
  ES256 access-token verification against Supabase's JWKS
- [go-playground/validator/v10](https://github.com/go-playground/validator)
  for request struct validation
- Supabase Postgres, Supabase Auth (over REST — no official Go SDK)

## Setup

1. Install prerequisites:
   - **Go 1.25+** (`brew install go`)
   - **golang-migrate CLI** — `brew install golang-migrate`, or
     `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest`
2. Copy `.env.example` → `.env` and fill from your Supabase project:
   - `DATABASE_URL` → *Settings → Database → Connection string*
     (**pooler**, port `6543`, append `?sslmode=require`)
   - `SUPABASE_URL` → *Settings → API → Project URL*
   - `SUPABASE_ANON_KEY` → *Settings → API → anon public key*
3. **Enable asymmetric JWT signing on the Supabase project.**
   Dashboard → *Auth → Signing Keys* → migrate to an asymmetric key
   (ES256). Without this step the JWKS endpoint returns an empty
   key set and every authenticated request will fail
   `INVALID_TOKEN`. Rolling back is one click.
4. In *Supabase → Authentication → Providers → Email*, **disable
   "Confirm email"** for the demo so `POST /api/auth/signup` returns
   a session immediately instead of a `requires_confirmation: true`
   stub.
5. `make migrate-up`
6. `make run` — server boots on `:4000`. Verify with
   `curl localhost:4000/health` → `{"ok":true}`.

## Env vars

Every non-optional var is enforced at boot; a missing required var
causes the process to exit before it starts listening.

| Var | Required | Notes |
| --- | --- | --- |
| `PORT` | no | Defaults to `4000`. |
| `ENV` | no | `development` or `production`. Default `development`. |
| `DATABASE_URL` | yes | Postgres DSN (Supabase pooler recommended, port 6543). |
| `SUPABASE_URL` | yes | Project URL, e.g. `https://xxxx.supabase.co`. Used for auth proxy calls and to derive the JWKS URL. |
| `SUPABASE_ANON_KEY` | yes | Sent as `apikey` when proxying to Supabase Auth signup/login/refresh/logout. |
| `CORS_ORIGIN` | yes | Comma-separated allowed browser origins. |

`SUPABASE_JWT_SECRET` is intentionally not read. Verification uses
the project's public JWKS at
`<SUPABASE_URL>/auth/v1/.well-known/jwks.json` — ES256 asymmetric.
The shared HS256 secret is a legacy code path we do not support.

## Make targets

| Target | Purpose |
| --- | --- |
| `make run` | Run the API locally. |
| `make test` | `go test ./... -race`. |
| `make tidy` | `go mod tidy`. |
| `make build` | Compile to `bin/api`. |
| `make migrate-up` | Apply all pending migrations. |
| `make migrate-down` | Roll back the most recent migration. |
| `make migrate-status` | Print the current migration version. |

All `migrate-*` targets read `DATABASE_URL` from `.env` by default;
pass `DB_URL=…` on the command line to override for one-off runs.

## Auth architecture

The frontend never talks to Supabase directly — auth is fully
backend-proxied. Signup/login/refresh/logout are HTTP proxies to
Supabase's gotrue; access-token verification is asymmetric (ES256)
against the project's JWKS.

1. Frontend POSTs `/api/auth/signup` or `/api/auth/login`.
2. Backend calls Supabase Auth REST endpoints with the anon key on
   the `apikey` header (safe server-side; the anon key is a public
   value).
3. Backend returns `{ "session": { access_token, refresh_token,
   expires_at, user } }`. The `access_token` is an ES256 JWT signed
   with the project's private key, which never leaves Supabase.
4. Frontend stores tokens in localStorage and sends `access_token`
   as `Authorization: Bearer …` on every subsequent request.
5. Backend verifies the JWT locally against the JWKS on every
   non-auth request. The JWK Set is fetched once at boot from
   `<SUPABASE_URL>/auth/v1/.well-known/jwks.json` and refreshed by
   a background goroutine. The verifier enforces:
   - `alg` in `{ES256}` (algorithm allowlist — blocks the classic
     `alg: HS256` downgrade attack where an attacker signs an HMAC
     token using the public-key bytes as the HMAC key)
   - `kid` header resolves to a public key currently in the JWKS
   - `iss` equals `<SUPABASE_URL>/auth/v1`
   - `aud` equals `authenticated`
   - `exp` is present and in the future
6. `sub` becomes `user.ID` (UUID), `email` becomes `user.Email` on
   `context.Context`. Handlers pull identity from the context —
   never from the request body.
7. On `401 TOKEN_EXPIRED`, the frontend POSTs `/api/auth/refresh`
   with the stored `refresh_token` and swaps in the returned
   session. Under asymmetric JWTs Supabase defaults access-token
   TTL to ~5 minutes (vs 1h under legacy HS256), so `/refresh`
   is on the hot path.

Auth error codes the frontend switches on:

| Code | Status | Meaning |
| --- | --- | --- |
| `EMAIL_TAKEN` | 409 | Signup — address already registered. |
| `SIGNUP_FAILED` | 400 | Signup — other Supabase error, message forwarded. |
| `INVALID_CREDENTIALS` | 401 | Login — generic, never leaks whether email exists. |
| `EMAIL_NOT_CONFIRMED` | 403 | Login — Supabase requires a confirmation click. |
| `REFRESH_FAILED` | 401 | Refresh — bounce to `/login`. |
| `UNAUTHORIZED` | 401 | Missing Bearer token. |
| `INVALID_TOKEN` | 401 | Bad signature, wrong `iss`/`aud`, wrong `alg`, unknown `kid`, malformed subject. |
| `TOKEN_EXPIRED` | 401 | Access token expired — try `/api/auth/refresh`. |

### Why JWKS/ES256 instead of the legacy HS256 secret?

- **Blast radius**: with HS256 anyone holding `SUPABASE_JWT_SECRET`
  can mint valid tokens. With ES256 the private key lives only in
  Supabase Auth — a compromise of this backend cannot forge tokens.
- **Rotation**: Supabase can rotate signing keys with zero
  redeploys; the JWKS refresh picks up the new `kid` transparently.
- **Config**: one less secret to distribute (`SUPABASE_JWT_SECRET`
  is gone from `.env`, `render.yaml`, and every deploy target).

### Why not the Supabase Go SDK?

There isn't an official one. Community wrappers exist, but the four
endpoints we need (`/signup`, `/token?grant_type=password`,
`/token?grant_type=refresh_token`, `/logout`) are trivial JSON POSTs,
and rolling a small typed client is cheaper than a dependency and its
version-skew risk.

## Calculation policy

- `shopspring/decimal.Decimal` for every money value — never a
  `float64` near money.
- Storage: `NUMERIC(12, 2)` in Postgres. Scans directly into
  `decimal.Decimal` via lib/pq.
- Rounding: `HALF_UP` to 2dp per line. Because our money values are
  always `>= 0`, `decimal.Round(2)` (HALF_AWAY_FROM_ZERO) is
  equivalent to HALF_UP.
- Discount is applied to the line subtotal **before** tax. Tax
  percent applies to the discounted amount, not the raw subtotal.
- `discount_type` is `fixed` | `percent` | `NULL`; exactly one may be
  set. Percents in `[0, 100]`. Fixed discounts must be `<= subtotal`
  — the server **rejects** with `DISCOUNT_EXCEEDS_SUBTOTAL`, never
  clamps.
- Document totals are pure sums of already-rounded line values. No
  re-rounding at the document level.

### Worked example — the spec sample doc must yield `grand_total = 421.50`

| Line | qty | unit_price | discount | tax | subtotal | discount | after | tax | line_total |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Widget A | 2 | 100.00 | 10% | 5% | 200.00 | 20.00 | 180.00 | 9.00 | **189.00** |
| Widget B | 1 | 50.00 | — | 5% | 50.00 | 0.00 | 50.00 | 2.50 | **52.50** |
| Service fee | 1 | 200.00 | fixed 20.00 | — | 200.00 | 20.00 | 180.00 | 0.00 | **180.00** |
| **Totals** | | | | | **450.00** | **40.00** | | **11.50** | **421.50** |

Locked in by `TestSampleDocument` in `internal/calc/calc_test.go` —
any regression here fails CI.

## Finalize / immutability

- **Every write** (document PATCH/DELETE, line POST/PATCH/DELETE)
  funnels through `assertEditable(ctx, tx, docID, userID)`. A
  finalized document rejects the write with `409 DOCUMENT_FINALIZED`.
- Finalize uses `SELECT … FOR UPDATE` inside a transaction. Two
  concurrent finalize requests serialize on the row lock: the first
  commits with `status='finalized'`; the second's SELECT unblocks,
  sees the new status, and returns `409 DOCUMENT_ALREADY_FINALIZED`
  (a distinct code so the frontend can distinguish "someone
  finalized this while you were looking at it" from "you tried to
  edit a locked doc").
- Empty documents refuse finalize with `400 EMPTY_DOCUMENT`.
- Finalize recomputes totals from persisted line inputs before
  writing — defence in depth against any drift between what's stored
  in `line_subtotal`, `line_total` and what `calc` would produce.

## API

Every route below is JSON in / JSON out. Everything under `/api/`
except `/api/auth/*` requires `Authorization: Bearer <access_token>`.

| Method | Path | Auth | Notes |
| --- | --- | --- | --- |
| `GET` | `/health` | — | Liveness probe, no auth. |
| `POST` | `/api/auth/signup` | — | Body `{ email, password }`. |
| `POST` | `/api/auth/login` | — | Body `{ email, password }`. |
| `POST` | `/api/auth/refresh` | — | Body `{ refresh_token }`. |
| `POST` | `/api/auth/logout` | — | Body `{ refresh_token? }`. Always 204. |
| `GET` | `/api/documents` | yes | Query `?from=YYYY-MM-DD&to=YYYY-MM-DD` optional. |
| `POST` | `/api/documents` | yes | Body `{ title, customer, issue_date, lines? }`. |
| `GET` | `/api/documents/{id}` | yes | Returns doc + lines. |
| `PATCH` | `/api/documents/{id}` | yes | Partial `{ title?, customer?, issue_date? }`. Draft only. |
| `DELETE` | `/api/documents/{id}` | yes | Draft only. Cascades lines. 204. |
| `POST` | `/api/documents/{id}/finalize` | yes | Locks doc, freezes totals, `status='finalized'`. |
| `POST` | `/api/documents/{id}/lines` | yes | Body: `LineInput + position?`. Returns full doc. |
| `PATCH` | `/api/documents/{id}/lines/{lineId}` | yes | Partial update; recomputes doc. |
| `DELETE` | `/api/documents/{id}/lines/{lineId}` | yes | Removes; recomputes doc. |
| `GET` | `/api/reports/summary` | yes | Query `?from&to` optional. Counts + totals. |

### Response contract

Success responses carry the resource under a semantic key:

```json
{ "document":  { "id": "…", "grand_total": "421.50", "lines": [ … ] } }
{ "documents": [ { … }, { … } ] }
{ "session":   { "access_token": "…", … } }
```

Errors always use the same envelope; `field` is present when the
failure was attributable to a specific input path:

```json
{ "error": { "code": "SNAKE_CASE_CODE", "message": "human text", "field": "lines.1.discount_value" } }
```

## Deploy

Render is the configured target. The provided `render.yaml` declares
a Docker web service that builds off the repo's `Dockerfile`, health
checks `/health`, and reads five secrets (`DATABASE_URL`,
`SUPABASE_URL`, `SUPABASE_ANON_KEY`, `SUPABASE_JWT_SECRET`,
`CORS_ORIGIN`) that you fill in the Render dashboard.

```
Render dashboard → New → Blueprint → point at this repo → fill the
five sync: false secrets in the service's Environment tab → Deploy.
```

Notes:

- `DATABASE_URL` must use the Supabase **pooler** endpoint (port
  6543). The direct 5432 endpoint runs out of connections quickly
  under any real traffic.
- The container is statically compiled and runs with no shell — cold
  start is ~15s on Render's free tier and ~2s on the starter plan
  (dominated by container spin-up, not the Go binary).
- Pick a Render region close to Supabase; pooler latency dominates
  request time.

## Assumptions

- **Discount policy: reject, don't clamp.** A fixed discount larger
  than the line subtotal is a user error worth surfacing, not a UX
  edge to silently smooth over.
- **Quantity is `INTEGER >= 1`.** No fractional quantities. Anything
  else (kg, litres) can be modelled via unit price for the demo.
- **`discount_type` and `discount_value` are strictly paired.**
  Setting one without the other returns `INVALID_DISCOUNT_TYPE`.
- **Dates are UTC.** `issue_date` is a Postgres `DATE`, not a
  timestamp — no zone conversion at write time.
- **Email confirmation off in Supabase for the demo.** With it on,
  signup returns `{ session: null, requires_confirmation: true }`
  and the frontend has to route through a confirmation flow. Out of
  scope for the take-home.
- **No `finalized → draft` transition.** Once locked, a doc stays
  locked. Restoring is out of scope (would need an audit story).
- **No official Supabase Go SDK.** Using a thin typed REST client
  (`internal/auth/supabase.go`, ~200 lines) is lower risk than
  taking a community-maintained wrapper.
- **PATCH cannot fully clear an optional field back to NULL.** With
  pointer-based partials you can't distinguish "omitted" from
  "explicit null". A DELETE + POST loop covers the rare full-reset
  case; called out in `updateLineRequest`'s doc comment.

## What I'd improve before production

- **Rate limiting on every endpoint** — currently only noted as
  future work for `/api/auth/*` (10 requests / 15 min per IP is
  probably enough signal).
- **Idempotency key on `POST /finalize`** — the FOR UPDATE lock
  already prevents lost updates, but a client-supplied key would let
  a retried request return the original response instead of a 409.
- **Audit log table for finalized docs** — who, when, plus a JSONB
  snapshot of the frozen state. Useful for disputes.
- **Slog wired end-to-end with the chi request ID.** Currently the
  request logger emits it; individual `slog.ErrorContext` calls in
  handlers do not automatically pick it up.
- **OpenTelemetry traces.** Go's OTEL SDK is well-maintained; a
  single `otelhttp.NewHandler` wrap in `internal/app` plus DB tx
  spans would cover 95% of debugging needs.
- **Session revocation list** — Supabase can't invalidate an
  already-issued JWT before its `exp`. A backend-side allowlist keyed
  by `jti` would let a real logout truly revoke.
- **PDF export.** Anything from a Go template + `wkhtmltopdf` to
  a hosted service like Docmosis.
- **Multi-currency support** — a `currency` column on `documents`,
  currency-aware formatting in the response layer.
- **`calc` as a shared internal package** if the frontend belonged
  to the same org — the same rules should never be re-implemented
  in TS.
- **Migrations invoked via a Go entrypoint flag** rather than a
  separate CLI, so production deploys can atomic `apply-then-serve`
  in one container start.
- **Integration test suite** against a real Postgres (testcontainers)
  covering the document + line CRUD paths that currently only have
  compile-time and calc-level coverage.
