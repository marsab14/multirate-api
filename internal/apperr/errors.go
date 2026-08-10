// Package apperr defines AppError, the sentinel error type every
// handler returns. A single mapper (in package handlers) turns these
// into HTTP responses so status codes and payload shape stay
// consistent across the API.
//
// Concrete error definitions land in B10.
package apperr
