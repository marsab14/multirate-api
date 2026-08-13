package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// testIssuer is the value the middleware expects on the `iss` claim.
// Must match what tokens set via signES256's default claims.
const testIssuer = "https://test.supabase.co/auth/v1"

// testKey is a package-level P-256 keypair used to sign test tokens
// and to seed the keyfunc that the middleware verifies against.
// Generated once in init so every test uses the same "trusted" key.
var testKey = mustGenerateKey()

// wrongKey is a distinct P-256 keypair. Tokens signed with this
// simulate "correct algorithm, wrong project" — the middleware
// must reject them.
var wrongKey = mustGenerateKey()

func mustGenerateKey() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("ecdsa keygen: " + err.Error())
	}
	return k
}

// testKeyfunc resolves ANY kid to testKey's public key. Real
// Supabase JWKS routes by kid, but for verifying the middleware
// logic (not the keyfunc library) a static resolver is sufficient
// and avoids spinning up an httptest JWKS server.
func testKeyfunc(_ *jwt.Token) (interface{}, error) {
	return &testKey.PublicKey, nil
}

// signES256 mints an ES256 JWT with sensible defaults; overrides
// let each test poke a specific claim (exp, iss, aud, sub, …).
// signWith lets a test sign with wrongKey to simulate a foreign
// issuer.
func signES256(t *testing.T, signWith *ecdsa.PrivateKey, overrides jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":   "11111111-1111-1111-1111-111111111111",
		"email": "user@example.com",
		"iss":   testIssuer,
		"aud":   SupabaseAudience,
		"role":  "authenticated",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range overrides {
		claims[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = "test-key"
	signed, err := tok.SignedString(signWith)
	require.NoError(t, err)
	return signed
}

// captureUser writes User off ctx as JSON so tests can assert the
// middleware forwarded the right identity.
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
			name:       "signed by different key",
			auth:       "Bearer " + signES256(t, wrongKey, nil),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
		{
			name:       "expired token",
			auth:       "Bearer " + signES256(t, testKey, jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "TOKEN_EXPIRED",
		},
		{
			name:       "wrong issuer",
			auth:       "Bearer " + signES256(t, testKey, jwt.MapClaims{"iss": "https://evil.example.com/auth/v1"}),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
		{
			name:       "wrong audience",
			auth:       "Bearer " + signES256(t, testKey, jwt.MapClaims{"aud": "anon"}),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
		{
			name:       "non-UUID subject",
			auth:       "Bearer " + signES256(t, testKey, jwt.MapClaims{"sub": "not-a-uuid"}),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
		{
			name:       "HS256 downgrade attempt is rejected",
			auth:       "Bearer " + signHS256Downgrade(t),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
	}

	mw := RequireAuth(testKeyfunc, testIssuer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	tok := signES256(t, testKey, jwt.MapClaims{"sub": id.String(), "email": "alice@example.com"})

	handler := RequireAuth(testKeyfunc, testIssuer)(http.HandlerFunc(captureUser))
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

// signHS256Downgrade produces a properly-formed HS256 token. The
// middleware's algorithm allowlist ("ES256") must reject it — this
// is the classic alg-substitution attack: an attacker with the JWKS
// public bytes tries to sign an HS token using those bytes as the
// HMAC key. jwt.WithValidMethods short-circuits that before the
// keyfunc is even consulted.
func signHS256Downgrade(t *testing.T) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "11111111-1111-1111-1111-111111111111",
		"email": "user@example.com",
		"iss":   testIssuer,
		"aud":   SupabaseAudience,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := tok.SignedString([]byte("any-hmac-key"))
	require.NoError(t, err)
	return signed
}
