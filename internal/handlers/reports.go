package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"

	dbpkg "multirate-api/internal/db"
)

// ReportHandlers owns /api/reports/*. Currently that's just the
// account-scoped summary; more report shapes can hang off the same
// struct without churn.
type ReportHandlers struct {
	db *sqlx.DB
}

func NewReportHandlers(db *sqlx.DB) *ReportHandlers {
	return &ReportHandlers{db: db}
}

// Mount registers the report endpoints on r. Caller mounts r at
// /api/reports; the router must sit inside the RequireAuth group so
// summary can pull the caller from context.
func (h *ReportHandlers) Mount(r chi.Router) {
	r.Method(http.MethodGet, "/summary", appHandler(h.summary))
}

// summaryRow is the DB projection of QReportSummary. Kept separate
// from summaryResponse so we can format the money fields with
// StringFixed(2) on the way out — shopspring/decimal drops trailing
// zeros on the "0" you get back from COALESCE(SUM(...), 0) when the
// result set is empty, and we want the wire to always show "0.00".
type summaryRow struct {
	DocumentCount   int             `db:"document_count"`
	TotalGrandTotal decimal.Decimal `db:"total_grand_total"`
	TotalTax        decimal.Decimal `db:"total_tax"`
	TotalDiscount   decimal.Decimal `db:"total_discount"`
}

// summaryResponse is the wire shape. Money fields are strings
// (pre-formatted to 2dp) rather than raw decimal.Decimal so the
// client sees a stable "421.50" / "0.00" format regardless of
// whether the underlying SQL result had trailing zeros or not.
// From/To echo the caller's query params — null when omitted.
type summaryResponse struct {
	From            *string `json:"from"`
	To              *string `json:"to"`
	DocumentCount   int     `json:"document_count"`
	TotalGrandTotal string  `json:"total_grand_total"`
	TotalTax        string  `json:"total_tax"`
	TotalDiscount   string  `json:"total_discount"`
}

// summary aggregates persisted document totals for the caller,
// optionally bounded by ?from and ?to. Single round trip; no
// re-summing of lines — the numbers reflect exactly what each
// document.grand_total column already stores.
func (h *ReportHandlers) summary(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		return err
	}

	var row summaryRow
	if err := sqlx.GetContext(r.Context(), h.db, &row, dbpkg.QReportSummary, user.ID, from, to); err != nil {
		return err
	}

	respondJSON(w, http.StatusOK, buildSummaryResponse(row, from, to))
	return nil
}

// buildSummaryResponse is factored out of the handler so the
// number-formatting behaviour can be reasoned about (and tested)
// without spinning up a DB. Pure.
func buildSummaryResponse(row summaryRow, from, to *time.Time) summaryResponse {
	return summaryResponse{
		From:            formatOptionalDate(from),
		To:              formatOptionalDate(to),
		DocumentCount:   row.DocumentCount,
		TotalGrandTotal: row.TotalGrandTotal.StringFixed(2),
		TotalTax:        row.TotalTax.StringFixed(2),
		TotalDiscount:   row.TotalDiscount.StringFixed(2),
	}
}

// formatOptionalDate turns a *time.Time into a *string in
// YYYY-MM-DD, keeping nil as nil so the JSON response can echo
// "from": null when the caller didn't pass one.
func formatOptionalDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(dateFormat)
	return &s
}
