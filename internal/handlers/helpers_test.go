package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"billing-api/internal/apperr"
)

// TestClassifyError locks the central mapping table: known error
// shapes get their AppError; unknown errors return nil so
// respondError knows to log and default to 500.
func TestClassifyError(t *testing.T) {
	t.Run("returns AppError as-is (pointer identity preserved)", func(t *testing.T) {
		src := apperr.NewBadRequest("BAD", "bad", "x")
		got := classifyError(src)
		require.Same(t, src, got)
	})

	t.Run("wrapped AppError still classified", func(t *testing.T) {
		src := apperr.NewNotFound("DOCUMENT_NOT_FOUND")
		wrapped := fmt.Errorf("while fetching: %w", src)
		got := classifyError(wrapped)
		require.NotNil(t, got)
		require.Equal(t, "DOCUMENT_NOT_FOUND", got.Code)
		require.Equal(t, http.StatusNotFound, got.Status)
	})

	t.Run("sql.ErrNoRows becomes 404 NOT_FOUND", func(t *testing.T) {
		got := classifyError(sql.ErrNoRows)
		require.NotNil(t, got)
		require.Equal(t, http.StatusNotFound, got.Status)
		require.Equal(t, "NOT_FOUND", got.Code)
		require.Equal(t, "Resource not found", got.Message)
	})

	t.Run("pq unique violation becomes 409 UNIQUE_VIOLATION", func(t *testing.T) {
		got := classifyError(&pq.Error{Code: pq.ErrorCode("23505"), Message: "duplicate key value violates unique constraint"})
		require.NotNil(t, got)
		require.Equal(t, http.StatusConflict, got.Status)
		require.Equal(t, "UNIQUE_VIOLATION", got.Code)
	})

	t.Run("other pq errors are not classified", func(t *testing.T) {
		got := classifyError(&pq.Error{Code: pq.ErrorCode("23503"), Message: "foreign key violation"})
		require.Nil(t, got, "only 23505 unique violation is auto-mapped; caller must handle others")
	})

	t.Run("unknown error returns nil so caller logs + 500s", func(t *testing.T) {
		got := classifyError(errors.New("connection reset"))
		require.Nil(t, got)
	})
}
