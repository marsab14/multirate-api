// Package handlers contains the HTTP endpoints. Each handler returns
// an error; the appHandler adapter (added in B10) turns those errors
// into the standard `{"error":{...}}` response.
//
// Shared helpers (decodeJSON, respondJSON, appHandler) land here.
package handlers
