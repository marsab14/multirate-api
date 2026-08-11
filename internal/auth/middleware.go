// Package auth verifies Supabase-issued JWTs (HS256) and exposes the
// caller identity on request context via UserFromContext. Handlers
// pull the current user from context rather than trusting anything
// in the request body.
package auth

import (
	"context"
	"errors"
	"fmt"
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

// RequireAuth is a chi-compatible middleware factory. It verifies
// the Bearer token against jwtSecret (HS256) and rejects with a
// specific error code so the frontend can decide between "refresh
// and retry" (TOKEN_EXPIRED) and "bounce to login" (INVALID_TOKEN
// or UNAUTHORIZED).
//
// The middleware calls apperr.Respond directly (not handlers.
// respondError) to avoid importing package handlers, which imports
// this package and would cycle.
func RequireAuth(jwtSecret string) func(http.Handler) http.Handler {
	secret := []byte(jwtSecret)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				apperr.Respond(w, r, apperr.NewUnauthorized("UNAUTHORIZED", "Missing bearer token"))
				return
			}
			tokenStr := strings.TrimPrefix(header, "Bearer ")

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return secret, nil
			})
			if err != nil {
				code := "INVALID_TOKEN"
				if errors.Is(err, jwt.ErrTokenExpired) {
					code = "TOKEN_EXPIRED"
				}
				apperr.Respond(w, r, apperr.NewUnauthorized(code, "Invalid or expired token"))
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok || !token.Valid {
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
