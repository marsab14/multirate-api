package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SupabaseUser is the subset of gotrue's user object we surface —
// id + email is all handlers need. Anything richer (metadata,
// provider, roles) is intentionally out of scope for this proxy.
type SupabaseUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// SupabaseSession mirrors the token bundle gotrue returns from
// /signup (when email confirmation is disabled) and /token.
// ExpiresAt is absolute unix seconds; ExpiresIn is available on the
// wire but the frontend already has ExpiresAt to schedule refreshes.
//
// Under asymmetric JWTs the AccessToken is ES256-signed and
// verified locally against the JWKS — see NewSupabaseJWKS and the
// middleware.
type SupabaseSession struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresAt    int64        `json:"expires_at"`
	User         SupabaseUser `json:"user"`
}

// SupabaseError is the error returned by all SupabaseClient methods
// on a non-2xx response. Handlers use errors.As to inspect
// Status/Message and map to the appropriate AppError. The upstream
// error shape stays inside this package — never leaked to clients.
type SupabaseError struct {
	Status  int
	Code    string
	Message string
}

func (e *SupabaseError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("supabase %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("supabase %d: %s", e.Status, e.Message)
}

// SupabaseClient is a thin HTTP wrapper around gotrue's /auth/v1/*
// REST surface for signup / password grant / refresh grant / logout.
// Zero business logic lives here; the handler layer interprets
// errors and picks HTTP status codes.
//
// Verification of Supabase-issued access tokens does NOT live here.
// See middleware.go (RequireAuth) and jwks.go (NewSupabaseJWKS) —
// tokens are verified locally against the project's JWKS.
type SupabaseClient struct {
	baseURL string
	anonKey string
	http    *http.Client
}

// NewSupabaseClient builds a client with a 10-second per-request
// timeout. Callers should treat the returned *SupabaseClient as
// safe for concurrent use — http.Client is goroutine-safe and the
// other fields are read-only after construction.
func NewSupabaseClient(baseURL, anonKey string) *SupabaseClient {
	return &SupabaseClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		anonKey: anonKey,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// SignUp creates a user with email + password. When email
// confirmation is disabled on the Supabase project the response
// carries a full session; when it's enabled the response is a bare
// user object with no tokens, and this method returns (nil, nil).
// Handlers translate (nil, nil) into requires_confirmation=true.
func (c *SupabaseClient) SignUp(ctx context.Context, email, password string) (*SupabaseSession, error) {
	var out SupabaseSession
	if err := c.do(ctx, http.MethodPost, "/signup", map[string]string{
		"email":    email,
		"password": password,
	}, "", &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, nil
	}
	return &out, nil
}

// SignIn exchanges email+password for a session via the password
// grant on /token.
func (c *SupabaseClient) SignIn(ctx context.Context, email, password string) (*SupabaseSession, error) {
	var out SupabaseSession
	if err := c.do(ctx, http.MethodPost, "/token?grant_type=password", map[string]string{
		"email":    email,
		"password": password,
	}, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Refresh trades a refresh token for a new session via /token.
// Note that under asymmetric JWTs the default access-token TTL
// drops to 5 minutes, so a working /refresh flow becomes materially
// more important than under legacy 1-hour HS256 tokens.
func (c *SupabaseClient) Refresh(ctx context.Context, refreshToken string) (*SupabaseSession, error) {
	var out SupabaseSession
	if err := c.do(ctx, http.MethodPost, "/token?grant_type=refresh_token", map[string]string{
		"refresh_token": refreshToken,
	}, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Logout best-effort invalidates the session server-side. Supabase's
// /logout expects the access token in the Authorization header;
// callers may pass whatever token they still have client-side.
// Errors are the caller's to swallow — see handlers.logout.
func (c *SupabaseClient) Logout(ctx context.Context, accessToken string) error {
	return c.do(ctx, http.MethodPost, "/logout", nil, accessToken, nil)
}

// do performs the actual HTTP round trip. body is JSON-encoded (nil
// sends an empty body). bearer, if non-empty, is set on the
// Authorization header. out is JSON-decoded from the response body
// on 2xx; pass nil to discard.
func (c *SupabaseClient) do(
	ctx context.Context,
	method, path string,
	body any,
	bearer string,
	out any,
) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal supabase request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/auth/v1"+path, reqBody)
	if err != nil {
		return fmt.Errorf("build supabase request: %w", err)
	}
	req.Header.Set("apikey", c.anonKey)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("supabase request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read supabase response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseSupabaseError(resp.StatusCode, respBody)
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode supabase response: %w", err)
	}
	return nil
}

// parseSupabaseError normalises gotrue's error shapes into a single
// SupabaseError. Three known shapes are handled:
//
//   - {"error","error_description"}    — OAuth-style (used by /token)
//   - {"code","msg"}                   — v1-native (used by /signup, /logout)
//   - {"code","message","details",...} — PostgREST-style (accidental
//     leakage when SUPABASE_URL is misconfigured; we surface the
//     message so operators can see PGRST125 etc. in logs)
//
// Anything unrecognised falls through to the raw body so the log
// line still contains something useful.
func parseSupabaseError(status int, body []byte) error {
	var s struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		ErrorCode        string `json:"error_code"`
		Msg              string `json:"msg"`
		Message          string `json:"message"`
		Code             string `json:"code"`
	}
	// Ignore decode errors: gotrue occasionally returns text/plain
	// on infra failures and we still want to surface *something*.
	_ = json.Unmarshal(body, &s)

	msg := firstNonEmpty(s.ErrorDescription, s.Msg, s.Message, strings.TrimSpace(string(body)), "supabase request failed")
	code := firstNonEmpty(s.Error, s.ErrorCode, s.Code)

	return &SupabaseError{Status: status, Code: code, Message: msg}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// AsSupabaseError is a small convenience so handlers can inspect
// SupabaseError without importing errors themselves.
func AsSupabaseError(err error) (*SupabaseError, bool) {
	var se *SupabaseError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
