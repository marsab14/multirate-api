// Package apperr defines AppError, the sentinel error type every
// handler (and the calc module) returns, plus a Respond writer that
// turns an AppError into the standard JSON envelope.
//
// Split of responsibility with handlers.respondError:
//   - apperr.Respond is the low-level writer. It maps an AppError
//     (or falls back to a generic 500 INTERNAL_ERROR) to the wire
//     envelope. Kept here so auth middleware can call it without
//     importing package handlers — handlers → auth → handlers would
//     cycle once handlers pull auth.UserFromContext.
//   - handlers.respondError is the full mapper. It classifies raw
//     errors (sql.ErrNoRows, pq unique violations, unknown) into
//     AppErrors first, logs anything that falls through, and then
//     delegates the actual write here.
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
	Status  int
	Code    string
	Message string
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

// NewForbidden constructs a 403-status AppError. Reserved for cases
// where the caller is authenticated but not allowed to act on this
// specific resource; ownership misses still return NewNotFound so
// we don't leak cross-user existence.
func NewForbidden(code, message string) *AppError {
	return &AppError{
		Status:  http.StatusForbidden,
		Code:    code,
		Message: message,
	}
}

// NewNotFound constructs a 404-status AppError with a generic
// "Not found" message — code carries the specific identifier (e.g.
// DOCUMENT_NOT_FOUND). Ownership checks intentionally return this
// rather than 403 so the API doesn't leak whether a resource exists
// for a different user.
func NewNotFound(code string) *AppError {
	return &AppError{
		Status:  http.StatusNotFound,
		Code:    code,
		Message: "Not found",
	}
}

// NewConflict constructs a 409-status AppError. Used when a request
// is well-formed and authorised but conflicts with resource state
// (e.g. writing to a finalized document).
func NewConflict(code, message string) *AppError {
	return &AppError{
		Status:  http.StatusConflict,
		Code:    code,
		Message: message,
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
// INTERNAL_ERROR — callers that want richer classification (e.g.
// sql.ErrNoRows → 404) should do that mapping first and pass an
// AppError in.
//
// r is currently unused but kept in the signature so per-request
// metadata (request_id, method, path) can be logged here later
// without changing every call site.
func Respond(w http.ResponseWriter, _ *http.Request, err error) {
	var appErr *AppError
	if !errors.As(err, &appErr) {
		appErr = &AppError{
			Status:  http.StatusInternalServerError,
			Code:    "INTERNAL_ERROR",
			Message: "Something went wrong",
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
