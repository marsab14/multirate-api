// Package auth verifies Supabase-issued JWTs and exposes the caller
// identity on request context via UserFromContext. Handlers pull
// the current user from context rather than trusting anything in
// the request body.
//
// Token verification is asymmetric (ES256) against Supabase's
// public JWKS — the private key never leaves the Supabase Auth
// service, so a compromise of this backend cannot forge tokens.
// The JWKS is fetched at boot (see jwks.go) and refreshed by a
// background goroutine that ends with the server's context.
//
// Split of concerns:
//   - supabase.go   — REST client for signup / login / refresh / logout.
//   - jwks.go       — bootstrap the keyfunc + expected iss.
//   - middleware.go — this file; verify a Bearer token per request.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"billing-api/internal/apperr"
)

// contextKey is a private named type so that only this package can
// place or fetch its values on a context.Context — prevents key
// collisions with other packages using string literals.
type contextKey string

const userCtxKey contextKey = "user"

// SupabaseAudience is the aud claim Supabase Auth sets on tokens
// for logged-in users. Anonymous / service tokens carry different
// audiences we don't accept here.
const SupabaseAudience = "authenticated"

// User is the small identity subset extracted from a verified JWT.
// Anything richer (roles, tenant, org) belongs on a separate lookup;
// keeping this narrow means the middleware never needs a DB round
// trip on the hot path.
type User struct {
	ID    uuid.UUID
	Email string
}

// UserFromContext returns the authenticated user attached by
// RequireAuth. The bool is false only if a handler is reached
// without RequireAuth in its chain — a programmer error.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userCtxKey).(User)
	return u, ok
}

// RequireAuth is a chi-compatible middleware factory. Given a
// Keyfunc that resolves Supabase's public keys and the expected
// `iss` claim (both produced by NewSupabaseJWKS), it verifies the
// Bearer token on each request and puts the caller's identity on
// context.
//
// Rejection codes distinguish "refresh and retry" (TOKEN_EXPIRED)
// from "bounce to login" (INVALID_TOKEN or UNAUTHORIZED). The
// middleware calls apperr.Respond directly (not handlers.
// respondError) to avoid importing package handlers, which imports
// this package and would cycle.
//
// Algorithm allowlist is ES256 only — Supabase asymmetric keys
// today. Add RS256 here if a project rotates to RSA keys instead.
func RequireAuth(kf jwt.Keyfunc, expectedIssuer string) func(http.Handler) http.Handler {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithIssuer(expectedIssuer),
		jwt.WithAudience(SupabaseAudience),
		jwt.WithExpirationRequired(),
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				apperr.Respond(w, r, apperr.NewUnauthorized("UNAUTHORIZED", "Missing bearer token"))
				return
			}
			tokenStr := strings.TrimPrefix(header, "Bearer ")

			var claims jwt.MapClaims
			token, err := parser.ParseWithClaims(tokenStr, &claims, kf)
			if err != nil {
				code := classifyJWTError(err)
				apperr.Respond(w, r, apperr.NewUnauthorized(code, "Invalid or expired token"))
				return
			}
			if !token.Valid {
				apperr.Respond(w, r, apperr.NewUnauthorized("INVALID_TOKEN", "Invalid token"))
				return
			}

			sub, _ := claims["sub"].(string)
			email, _ := claims["email"].(string)
			userID, parseErr := uuid.Parse(sub)
			if parseErr != nil {
				apperr.Respond(w, r, apperr.NewUnauthorized("INVALID_TOKEN", "Malformed subject"))
				return
			}

			ctx := context.WithValue(r.Context(), userCtxKey, User{ID: userID, Email: email})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// classifyJWTError picks the wire code the frontend switches on.
// Only TOKEN_EXPIRED buys a refresh attempt — everything else
// (signature mismatch, wrong issuer, wrong audience, unknown kid,
// algorithm rejection) means "bounce to login".
func classifyJWTError(err error) string {
	if errors.Is(err, jwt.ErrTokenExpired) {
		return "TOKEN_EXPIRED"
	}
	return "INVALID_TOKEN"
}
