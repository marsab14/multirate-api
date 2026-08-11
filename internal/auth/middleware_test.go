package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-please-do-not-use-in-prod"

// signToken produces an HS256 token with the given claims signed
// with testSecret. Any claim value can be overridden; missing
// standard claims fall back to sensible defaults so most tests
// only need to set what they're testing.
func signToken(t *testing.T, overrides jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":   "11111111-1111-1111-1111-111111111111",
		"email": "user@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	}
	for k, v := range overrides {
		claims[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

// captureUser is a downstream handler that pulls User off ctx and
// writes it back as JSON so tests can assert the middleware forwarded
// the right identity.
func captureUser(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "no user", http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":    u.ID.String(),
		"email": u.Email,
	})
}

func TestRequireAuth(t *testing.T) {
	tests := []struct {
		name       string
		auth       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing header",
			auth:       "",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
		{
			name:       "wrong prefix",
			auth:       "Token abc.def.ghi",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
		{
			name:       "malformed token",
			auth:       "Bearer not-a-jwt",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
		{
			name:       "wrong secret",
			auth:       "Bearer " + signWithSecret(t, "other-secret"),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
		{
			name:       "expired token",
			auth:       "Bearer " + signToken(t, jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "TOKEN_EXPIRED",
		},
		{
			name:       "non-UUID subject",
			auth:       "Bearer " + signToken(t, jwt.MapClaims{"sub": "not-a-uuid"}),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
	}

	mw := RequireAuth(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code)
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			require.Equal(t, tc.wantCode, payload.Error.Code)
		})
	}
}

// TestRequireAuth_ValidToken asserts the happy path forwards the
// request with a hydrated User on context.
func TestRequireAuth_ValidToken(t *testing.T) {
	id := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	tok := signToken(t, jwt.MapClaims{"sub": id.String(), "email": "alice@example.com"})

	handler := RequireAuth(testSecret)(http.HandlerFunc(captureUser))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, id.String(), body["id"])
	require.Equal(t, "alice@example.com", body["email"])
}

// signWithSecret is a small helper for the "wrong secret" case: it
// produces a token signed with an arbitrary secret so the middleware
// (which knows only testSecret) rejects it.
func signWithSecret(t *testing.T, secret string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "11111111-1111-1111-1111-111111111111",
		"email": "user@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}
