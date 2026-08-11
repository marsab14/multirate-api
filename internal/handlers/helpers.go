// Package handlers contains the HTTP endpoints. Each handler either
// writes a response directly or delegates to respondError; the
// appHandler-style return-error adapter is deferred to B10.
//
// Shared helpers live here: a validator singleton, decodeJSON,
// respondJSON, respondError.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"billing-api/internal/apperr"
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
// validation. On failure it returns an *apperr.AppError already
// populated with a client-safe code and (where possible) a field
// path. Handlers are expected to check the returned error and pass
// it straight to respondError.
func decodeJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return apperr.NewBadRequest("INVALID_JSON", "invalid JSON body", "")
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

// respondError writes err as the standard error envelope. Thin
// wrapper over apperr.Respond so handler code reads with the
// handlers-package idiom, while auth middleware can call
// apperr.Respond directly without importing this package (which
// would cycle once handlers pull auth.UserFromContext).
func respondError(w http.ResponseWriter, r *http.Request, err error) {
	apperr.Respond(w, r, err)
}
