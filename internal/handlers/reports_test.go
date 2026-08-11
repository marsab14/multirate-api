package handlers

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestBuildSummaryResponse locks the response formatter's contract:
// money fields always render 2dp (including for zero, which is the
// case the user flagged), and from/to echo through as YYYY-MM-DD
// strings or null.
func TestBuildSummaryResponse(t *testing.T) {
	t.Run("zero row formats every money field as 0.00", func(t *testing.T) {
		row := summaryRow{
			DocumentCount:   0,
			TotalGrandTotal: decimal.Zero,
			TotalTax:        decimal.Zero,
			TotalDiscount:   decimal.Zero,
		}
		resp := buildSummaryResponse(row, nil, nil)
		require.Nil(t, resp.From)
		require.Nil(t, resp.To)
		require.Equal(t, 0, resp.DocumentCount)
		require.Equal(t, "0.00", resp.TotalGrandTotal)
		require.Equal(t, "0.00", resp.TotalTax)
		require.Equal(t, "0.00", resp.TotalDiscount)
	})

	t.Run("populated row preserves trailing zeros", func(t *testing.T) {
		row := summaryRow{
			DocumentCount:   3,
			TotalGrandTotal: decimal.RequireFromString("421.50"),
			TotalTax:        decimal.RequireFromString("11.5"),
			TotalDiscount:   decimal.RequireFromString("40"),
		}
		from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
		resp := buildSummaryResponse(row, &from, &to)
		require.NotNil(t, resp.From)
		require.NotNil(t, resp.To)
		require.Equal(t, "2026-08-01", *resp.From)
		require.Equal(t, "2026-08-31", *resp.To)
		require.Equal(t, 3, resp.DocumentCount)
		require.Equal(t, "421.50", resp.TotalGrandTotal)
		require.Equal(t, "11.50", resp.TotalTax)
		require.Equal(t, "40.00", resp.TotalDiscount)
	})
}
