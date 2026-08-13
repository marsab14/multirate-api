// Package handlers contains the HTTP endpoints. Handlers return
// errors; the appHandler adapter converts those errors into HTTP
// responses via respondError — the central classifier that maps
// AppErrors, sql.ErrNoRows, and pq.Error(23505) to the wire
// envelope and logs anything else before defaulting to 500.
//
// Shared helpers live here: a validator singleton, decodeJSON,
// respondJSON, respondError, and the appHandler adapter.
package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/lib/pq"

	"multirate-api/internal/apperr"
)

// validate is a package-level go-playground/validator instance.
// Cached because the library recommends reusing an instance (it
// builds a per-type struct plan on first use).
var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Report field paths using JSON tag names so the wire-facing
	// AppError.Field matches what clients actually sent. Without
	// this we'd surface Go field names like "Email" instead of
	// "email".
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

// decodeJSON parses the request body into v and runs struct-tag
// validation. Unknown JSON fields are rejected up front so typos
// don't silently succeed. On failure it returns an *apperr.AppError
// already populated with a client-safe code and (where possible) a
// field path. Handlers are expected to check the returned error and
// pass it straight to respondError.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.NewBadRequest("INVALID_JSON", err.Error(), "")
	}
	if err := validate.Struct(v); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) && len(ve) > 0 {
			fe := ve[0]
			return apperr.NewBadRequest(
				"VALIDATION_ERROR",
				fmt.Sprintf("%s failed validation on '%s'", fe.Field(), fe.Tag()),
				fe.Field(),
			)
		}
		return apperr.NewBadRequest("VALIDATION_ERROR", err.Error(), "")
	}
	return nil
}

// decodeBodyOptional reads the body into v when non-empty. Missing
// / empty bodies are not an error — the caller inspects the zero
// value of v to detect that case. No struct-tag validation is run
// here; use decodeJSON when the body is required + validated.
func decodeBodyOptional(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return apperr.NewBadRequest("INVALID_JSON", "invalid JSON body", "")
	}
	return nil
}

// respondJSON writes v as JSON with the given status. Encoding
// failures are ignored — nothing sensible can be done after the
// headers are on the wire.
func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// respondError is the central error classifier: it inspects err and
// writes the appropriate JSON envelope + status. Known shapes
// (AppError, sql.ErrNoRows, pq unique violation) get their own
// mapping. Anything else is logged with request context and reduced
// to a generic 500 so driver-level text never reaches the client.
//
// Actual JSON writing is delegated to apperr.Respond so every error
// path shares one wire encoder — auth middleware calls apperr.Respond
// directly for the same reason.
func respondError(w http.ResponseWriter, r *http.Request, err error) {
	if mapped := classifyError(err); mapped != nil {
		apperr.Respond(w, r, mapped)
		return
	}
	slog.ErrorContext(r.Context(), "unhandled error",
		"err", err.Error(),
		"path", r.URL.Path,
		"method", r.Method,
	)
	apperr.Respond(w, r, err)
}

// classifyError turns known error shapes into a matching AppError.
// Returns nil when nothing matches — that's the caller's cue to log
// and let apperr.Respond default to 500 INTERNAL_ERROR.
func classifyError(err error) *apperr.AppError {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return ae
	}
	if errors.Is(err, sql.ErrNoRows) {
		return &apperr.AppError{
			Status:  http.StatusNotFound,
			Code:    "NOT_FOUND",
			Message: "Resource not found",
		}
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return apperr.NewConflict("UNIQUE_VIOLATION", "Unique constraint violated")
	}
	return nil
}

// appHandler adapts a `func(w, r) error` handler into http.Handler
// so we can `return err` from handler bodies and get consistent
// mapping via respondError.
type appHandler func(w http.ResponseWriter, r *http.Request) error

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := fn(w, r); err != nil {
		respondError(w, r, err)
	}
}
