package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"billing-api/internal/auth"
)

// mockSB is a fake SupabaseAuthClient built out of function fields
// so each test can shape a single method's behaviour without
// implementing the whole interface anew. Unset fields panic
// deliberately — if a test path calls a method it didn't set, we
// want to know.
type mockSB struct {
	signUp  func(ctx context.Context, email, password string) (*auth.SupabaseSession, error)
	signIn  func(ctx context.Context, email, password string) (*auth.SupabaseSession, error)
	refresh func(ctx context.Context, refreshToken string) (*auth.SupabaseSession, error)
	logout  func(ctx context.Context, accessToken string) error
}

func (m *mockSB) SignUp(ctx context.Context, e, p string) (*auth.SupabaseSession, error) {
	return m.signUp(ctx, e, p)
}
func (m *mockSB) SignIn(ctx context.Context, e, p string) (*auth.SupabaseSession, error) {
	return m.signIn(ctx, e, p)
}
func (m *mockSB) Refresh(ctx context.Context, rt string) (*auth.SupabaseSession, error) {
	return m.refresh(ctx, rt)
}
func (m *mockSB) Logout(ctx context.Context, at string) error {
	return m.logout(ctx, at)
}

// newAuthRouter wires an AuthHandlers built from sb onto a fresh
// chi.Mux at /api/auth so tests can hit real paths.
func newAuthRouter(sb SupabaseAuthClient) *chi.Mux {
	r := chi.NewMux()
	r.Route("/api/auth", NewAuthHandlers(sb).Mount)
	return r
}

// doJSON is a tiny helper that posts a JSON body and returns the
// response recorder. All handler tests fit this shape.
func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sampleSession() *auth.SupabaseSession {
	return &auth.SupabaseSession{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    1_700_000_000,
		User:         auth.SupabaseUser{ID: "abc", Email: "u@example.com"},
	}
}

// TestSignup covers happy path, email-taken mapping, confirmation
// required (nil session), and validation failure on a short password.
func TestSignup(t *testing.T) {
	t.Run("happy path returns session", func(t *testing.T) {
		sb := &mockSB{signUp: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			return sampleSession(), nil
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/signup",
			map[string]string{"email": "u@example.com", "password": "supersecret"})
		require.Equal(t, http.StatusOK, rec.Code)

		var resp sessionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Session)
		require.Equal(t, "at", resp.Session.AccessToken)
		require.False(t, resp.RequiresConfirmation)
	})

	t.Run("email already registered maps to EMAIL_TAKEN", func(t *testing.T) {
		sb := &mockSB{signUp: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			return nil, &auth.SupabaseError{Status: 422, Message: "User already registered"}
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/signup",
			map[string]string{"email": "u@example.com", "password": "supersecret"})
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "EMAIL_TAKEN", errorCode(t, rec))
	})

	t.Run("nil session means requires_confirmation", func(t *testing.T) {
		sb := &mockSB{signUp: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			return nil, nil
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/signup",
			map[string]string{"email": "u@example.com", "password": "supersecret"})
		require.Equal(t, http.StatusOK, rec.Code)

		var resp sessionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Nil(t, resp.Session)
		require.True(t, resp.RequiresConfirmation)
	})

	t.Run("short password fails validation before hitting supabase", func(t *testing.T) {
		called := false
		sb := &mockSB{signUp: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			called = true
			return nil, nil
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/signup",
			map[string]string{"email": "u@example.com", "password": "short"})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "VALIDATION_ERROR", errorCode(t, rec))
		require.False(t, called, "supabase must not be called on validation failure")
	})

	t.Run("other supabase errors surface as SIGNUP_FAILED 400", func(t *testing.T) {
		sb := &mockSB{signUp: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			return nil, &auth.SupabaseError{Status: 400, Message: "Password should be at least 6 characters"}
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/signup",
			map[string]string{"email": "u@example.com", "password": "supersecret"})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "SIGNUP_FAILED", errorCode(t, rec))
	})
}

// TestLogin covers happy path, the generic INVALID_CREDENTIALS
// mapping (never leaking whether email exists) and the
// EMAIL_NOT_CONFIRMED special case.
func TestLogin(t *testing.T) {
	t.Run("happy path returns session", func(t *testing.T) {
		sb := &mockSB{signIn: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			return sampleSession(), nil
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/login",
			map[string]string{"email": "u@example.com", "password": "supersecret"})
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("bad credentials map to generic INVALID_CREDENTIALS", func(t *testing.T) {
		sb := &mockSB{signIn: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			return nil, &auth.SupabaseError{Status: 400, Code: "invalid_grant", Message: "Invalid login credentials"}
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/login",
			map[string]string{"email": "u@example.com", "password": "supersecret"})
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Equal(t, "INVALID_CREDENTIALS", errorCode(t, rec))

		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		require.Equal(t, "Invalid email or password", payload.Error.Message)
	})

	t.Run("unconfirmed email maps to 403 EMAIL_NOT_CONFIRMED", func(t *testing.T) {
		sb := &mockSB{signIn: func(_ context.Context, _, _ string) (*auth.SupabaseSession, error) {
			return nil, &auth.SupabaseError{Status: 400, Message: "Email not confirmed"}
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/login",
			map[string]string{"email": "u@example.com", "password": "supersecret"})
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "EMAIL_NOT_CONFIRMED", errorCode(t, rec))
	})
}

func TestRefresh(t *testing.T) {
	t.Run("happy path returns fresh session", func(t *testing.T) {
		sb := &mockSB{refresh: func(_ context.Context, _ string) (*auth.SupabaseSession, error) {
			return sampleSession(), nil
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/refresh",
			map[string]string{"refresh_token": "rt"})
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("any error becomes 401 REFRESH_FAILED", func(t *testing.T) {
		sb := &mockSB{refresh: func(_ context.Context, _ string) (*auth.SupabaseSession, error) {
			return nil, &auth.SupabaseError{Status: 400, Message: "refresh token expired"}
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/refresh",
			map[string]string{"refresh_token": "rt"})
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Equal(t, "REFRESH_FAILED", errorCode(t, rec))
	})

	t.Run("missing refresh_token fails validation", func(t *testing.T) {
		sb := &mockSB{}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/refresh",
			map[string]string{})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "VALIDATION_ERROR", errorCode(t, rec))
	})
}

func TestLogout(t *testing.T) {
	t.Run("swallows supabase errors and returns 204", func(t *testing.T) {
		called := false
		sb := &mockSB{logout: func(_ context.Context, _ string) error {
			called = true
			return &auth.SupabaseError{Status: 401, Message: "invalid token"}
		}}
		rec := doJSON(t, newAuthRouter(sb), http.MethodPost, "/api/auth/logout",
			map[string]string{"refresh_token": "rt"})
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.True(t, called)
	})

	t.Run("missing body returns 204 without calling supabase", func(t *testing.T) {
		called := false
		sb := &mockSB{logout: func(_ context.Context, _ string) error {
			called = true
			return nil
		}}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		rec := httptest.NewRecorder()
		newAuthRouter(sb).ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.False(t, called)
	})
}

// errorCode pulls .error.code out of an error envelope response so
// each test doesn't repeat the same 5 lines of unmarshalling.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload.Error.Code
}
