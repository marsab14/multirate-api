// Package handlers contains the HTTP endpoints. Each handler returns
// an error; the appHandler adapter (added in B10) turns those errors
// into the standard `{"error":{...}}` response.
//
// Shared helpers (decodeJSON, respondJSON, appHandler) land here.
package handlers

import (
	"net/http"

	"billing-api/internal/apperr"
)

// respondError writes err as the standard error envelope. It is a
// thin wrapper over apperr.Respond so that handler code reads with
// the handlers-package idiom, while auth middleware can call
// apperr.Respond directly without importing this package (which
// would form a cycle once handlers pull auth.UserFromContext).
func respondError(w http.ResponseWriter, r *http.Request, err error) {
	apperr.Respond(w, r, err)
}
