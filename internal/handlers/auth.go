package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"billing-api/internal/apperr"
	"billing-api/internal/auth"
)

// SupabaseAuthClient is the narrow interface the auth handlers
// depend on. Defined at the consumer (this package) so tests can
// supply an in-memory fake without pulling in net/http.
// *auth.SupabaseClient satisfies it structurally.
type SupabaseAuthClient interface {
	SignUp(ctx context.Context, email, password string) (*auth.SupabaseSession, error)
	SignIn(ctx context.Context, email, password string) (*auth.SupabaseSession, error)
	Refresh(ctx context.Context, refreshToken string) (*auth.SupabaseSession, error)
	Logout(ctx context.Context, accessToken string) error
}

// AuthHandlers owns the /api/auth/* endpoint set. It carries only
// the Supabase client — no DB, no logger — since these endpoints
// are pure proxies to Supabase Auth.
type AuthHandlers struct {
	sb SupabaseAuthClient
}

func NewAuthHandlers(sb SupabaseAuthClient) *AuthHandlers {
	return &AuthHandlers{sb: sb}
}

// Mount registers the four auth endpoints on r. Caller is
// responsible for mounting r at /api/auth.
func (h *AuthHandlers) Mount(r chi.Router) {
	r.Post("/signup", h.signup)
	r.Post("/login", h.login)
	r.Post("/refresh", h.refresh)
	r.Post("/logout", h.logout)
}

// loginRequest is reused for signup as well as login — same shape.
// min=8 on password applies at both endpoints; that's stricter than
// strictly necessary for login but keeps a single shared type and
// gives us a defensible baseline for the demo.
type loginRequest struct {
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required,min=8"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// sessionResponse is the wire shape returned by signup/login/refresh.
// Session is a pointer so the confirmation-required signup path can
// return {"session": null, "requires_confirmation": true} without a
// zero-value session leaking through.
type sessionResponse struct {
	Session              *auth.SupabaseSession `json:"session"`
	RequiresConfirmation bool                  `json:"requires_confirmation,omitempty"`
}

// signup proxies to Supabase's /signup. See mapping rules inline.
func (h *AuthHandlers) signup(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		respondError(w, r, err)
		return
	}

	session, err := h.sb.SignUp(r.Context(), req.Email, req.Password)
	if err != nil {
		if se, ok := auth.AsSupabaseError(err); ok {
			if strings.Contains(strings.ToLower(se.Message), "user already registered") {
				respondError(w, r, &apperr.AppError{
					Code:    "EMAIL_TAKEN",
					Message: "That email is already registered",
					Status:  http.StatusConflict,
				})
				return
			}
			respondError(w, r, &apperr.AppError{
				Code:    "SIGNUP_FAILED",
				Message: se.Message,
				Status:  http.StatusBadRequest,
			})
			return
		}
		respondError(w, r, err)
		return
	}

	if session == nil {
		respondJSON(w, http.StatusOK, sessionResponse{
			Session:              nil,
			RequiresConfirmation: true,
		})
		return
	}
	respondJSON(w, http.StatusOK, sessionResponse{Session: session})
}

// login proxies to Supabase's /token?grant_type=password.
//
// Security note: the wire error is intentionally generic
// ("Invalid email or password") regardless of whether the failure
// was "no such user" or "wrong password". Leaking the distinction
// would let an attacker enumerate valid email addresses via a
// signup-then-login funnel.
func (h *AuthHandlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		respondError(w, r, err)
		return
	}

	session, err := h.sb.SignIn(r.Context(), req.Email, req.Password)
	if err != nil {
		if se, ok := auth.AsSupabaseError(err); ok {
			msg := strings.ToLower(se.Message)
			if strings.Contains(msg, "email not confirmed") {
				respondError(w, r, &apperr.AppError{
					Code:    "EMAIL_NOT_CONFIRMED",
					Message: "Please confirm your email before logging in",
					Status:  http.StatusForbidden,
				})
				return
			}
			respondError(w, r, &apperr.AppError{
				Code:    "INVALID_CREDENTIALS",
				Message: "Invalid email or password",
				Status:  http.StatusUnauthorized,
			})
			return
		}
		respondError(w, r, err)
		return
	}
	respondJSON(w, http.StatusOK, sessionResponse{Session: session})
}

// refresh exchanges a refresh_token for a fresh session. Any error
// collapses to 401 REFRESH_FAILED — the frontend interprets that as
// "session unrecoverable, bounce to /login".
//
// Under asymmetric JWTs Supabase defaults access-token TTL to
// ~5 minutes (vs 1h under legacy HS256), so this endpoint is on
// the hot path — cache-affinity for the middleware's JWKS matters.
func (h *AuthHandlers) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(r, &req); err != nil {
		respondError(w, r, err)
		return
	}

	session, err := h.sb.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		respondError(w, r, apperr.NewUnauthorized("REFRESH_FAILED", "Could not refresh session"))
		return
	}
	respondJSON(w, http.StatusOK, sessionResponse{Session: session})
}

// logout is best-effort. Supabase's /logout wants the *access* token
// as a bearer; the spec asks us to accept a refresh_token in the
// body for symmetry with the other endpoints. If the token type
// doesn't match Supabase will 401 — which we swallow. Real logout
// happens client-side by dropping tokens from localStorage.
func (h *AuthHandlers) logout(w http.ResponseWriter, r *http.Request) {
	var req logoutRequest
	// Body is optional; ignore decode failures.
	_ = decodeBodyOptional(r, &req)

	if req.RefreshToken != "" {
		_ = h.sb.Logout(r.Context(), req.RefreshToken)
	}
	w.WriteHeader(http.StatusNoContent)
}
