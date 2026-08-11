// Package apperr defines AppError, the sentinel error type every
// handler (and the calc module) returns, plus a single Respond
// helper that turns AppError values into HTTP responses.
//
// Respond intentionally lives here rather than in package handlers
// so that auth middleware can call it without importing handlers.
// (handlers → auth → handlers would cycle once B6+ handlers start
// pulling UserFromContext.) handlers.respondError is a thin wrapper
// that delegates here.
//
// Additional constructors — NotFound, Conflict, Internal — arrive
// in B10 alongside handler-side polish.
package apperr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// AppError is the sentinel error type used throughout the codebase.
// Code is a stable machine-readable identifier (SNAKE_CASE); Message
// is safe to surface to end users; Field is the JSON path of the
// offending input, empty when not applicable.
type AppError struct {
	Code    string
	Message string
	Status  int
	Field   string
}

// Error implements the error interface for logs and %w chains. The
// wire response is built separately by the HTTP mapper so the two
// representations can diverge without coupling.
func (e *AppError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (field=%s)", e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewBadRequest constructs a 400-status AppError. Field may be empty
// when the failure isn't attributable to a specific request field.
func NewBadRequest(code, message, field string) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Status:  http.StatusBadRequest,
		Field:   field,
	}
}

// NewUnauthorized constructs a 401-status AppError. Used by the auth
// middleware for missing/invalid/expired tokens; the Code the client
// receives (UNAUTHORIZED / INVALID_TOKEN / TOKEN_EXPIRED) drives
// whether the frontend tries a refresh or bounces the user to /login.
func NewUnauthorized(code, message string) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Status:  http.StatusUnauthorized,
	}
}

// wire shape for error responses; kept unexported since the wire
// contract is expressed by the struct tags.
type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type errorEnvelope struct {
	Error errorPayload `json:"error"`
}

// Respond writes a JSON error envelope to w with the status/code
// carried by err. Any non-AppError becomes a generic 500
// INTERNAL_ERROR so we never leak driver-level messages to clients.
// The r parameter is currently unused but kept in the signature so
// per-request context (request_id, method, path) can be logged here
// later without changing every call site.
func Respond(w http.ResponseWriter, _ *http.Request, err error) {
	var appErr *AppError
	if !errors.As(err, &appErr) {
		appErr = &AppError{
			Code:    "INTERNAL_ERROR",
			Message: "internal server error",
			Status:  http.StatusInternalServerError,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(appErr.Status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: errorPayload{
			Code:    appErr.Code,
			Message: appErr.Message,
			Field:   appErr.Field,
		},
	})
}
