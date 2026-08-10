This is the backend for a billing/document management take-home,
written in Go 1.25.

Stack:
- chi (router) — small, standard-library-flavored
- sqlx (DB) — thin layer over database/sql, hand-written SQL for money math
- shopspring/decimal — decimal arithmetic for money, no floats
- golang-jwt/jwt/v5 — verify Supabase-issued JWTs (HS256)
- go-playground/validator/v10 — struct-tag validation on request bodies
- golang-migrate — SQL file migrations

Auth is backend-proxied: the frontend never talks to Supabase directly.
This backend exposes /api/auth/signup, /login, /logout, /refresh. It
proxies to Supabase Auth REST endpoints (no Go SDK exists — plain
net/http calls with the anon key as the `apikey` header).

For every non-auth request, the frontend sends the Supabase-issued
access token as Bearer. Backend verifies with SUPABASE_JWT_SECRET,
extracts sub → user.ID and email → user.Email into request context.

Money handling:
- shopspring/decimal.Decimal everywhere. Never a float64 near a money value.
- Store NUMERIC(12, 2) in Postgres; scan directly into decimal.Decimal.
- JSON: decimal.Decimal marshals as a string by default — keep it that way.
- Round HALF_UP to 2dp per line. Document totals sum already-rounded lines.
- Sample doc from spec MUST compute to grand_total = 421.50.

Discount rules:
- discount_type is 'fixed' | 'percent' | NULL. Exactly one, never both.
- Percents live in [0, 100]. Fixed discounts must be <= line subtotal
  (reject with DISCOUNT_EXCEEDS_SUBTOTAL — no clamping).
- Discount applies before tax. Tax percent applies to the DISCOUNTED
  line amount, not the raw subtotal.

Document lifecycle:
- Status is 'draft' or 'finalized'. Only drafts are editable.
- Every write endpoint calls assertEditable(ctx, docID, userID) first.
  Returns 409 DOCUMENT_FINALIZED if the doc is locked.
- Finalize runs inside a transaction with SELECT ... FOR UPDATE.

Handler pattern: handlers return error, an appHandler adapter converts
returned errors to HTTP responses via a central mapper. Errors are
sentinel types (AppError struct with Code, Message, Status, Field).

Error response shape (always):
{ "error": { "code": "SNAKE_CASE_CODE", "message": "human text", "field": "optional.field.path" } }

Ownership: every read and write filters by user_id from the verified JWT.
Never trust an id from the request body for ownership.

Idiom notes:
- Prefer context.Context everywhere. Handlers use r.Context(); pass to
  DB calls (ExecContext, QueryContext, sqlx equivalents).
- Prefer slog (log/slog) for structured logging over log.Printf.
- Graceful shutdown in main via signal.NotifyContext + http.Server.Shutdown.
- Table-driven tests for the calc module.