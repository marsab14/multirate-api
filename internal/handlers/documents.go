package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"billing-api/internal/apperr"
	"billing-api/internal/auth"
	"billing-api/internal/calc"
	dbpkg "billing-api/internal/db"
)

// dateFormat is the wire format for all date-typed fields. Chosen
// deliberately narrow — ISO-8601 date, no times, no zones — so the
// wire contract is unambiguous and easy to validate.
const dateFormat = "2006-01-02"

// DocumentHandlers owns /api/documents/*. It carries only the DB
// handle; per-request state (user, ids, deadlines) flows in through
// context.
type DocumentHandlers struct {
	db *sqlx.DB
}

func NewDocumentHandlers(db *sqlx.DB) *DocumentHandlers {
	return &DocumentHandlers{db: db}
}

// Mount registers list/create/get/update/delete on r. Caller mounts
// r at /api/documents. All handlers use appHandler so they can
// `return err` and centralised error mapping happens for free.
func (h *DocumentHandlers) Mount(r chi.Router) {
	r.Method(http.MethodGet, "/", appHandler(h.list))
	r.Method(http.MethodPost, "/", appHandler(h.create))
	r.Method(http.MethodGet, "/{id}", appHandler(h.get))
	r.Method(http.MethodPatch, "/{id}", appHandler(h.update))
	r.Method(http.MethodDelete, "/{id}", appHandler(h.delete))
}

// ------------------------- request/response ----------------------------

type createDocumentRequest struct {
	Title     string           `json:"title"      validate:"required,min=1,max=200"`
	Customer  string           `json:"customer"   validate:"required,min=1,max=200"`
	IssueDate string           `json:"issue_date" validate:"required,datetime=2006-01-02"`
	Lines     []calc.LineInput `json:"lines"      validate:"omitempty,dive"`
}

type updateDocumentRequest struct {
	Title     *string `json:"title"      validate:"omitempty,min=1,max=200"`
	Customer  *string `json:"customer"   validate:"omitempty,min=1,max=200"`
	IssueDate *string `json:"issue_date" validate:"omitempty,datetime=2006-01-02"`
}

type documentResponse struct {
	Document *dbpkg.Document `json:"document"`
}

type documentsResponse struct {
	Documents []dbpkg.Document `json:"documents"`
}

// ------------------------------ handlers -------------------------------

// list returns every document for the caller, optionally filtered
// by [from, to]. The response never carries per-doc lines — clients
// hit GET /{id} for that detail.
func (h *DocumentHandlers) list(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}

	from, to, err := parseDateRange(r)
	if err != nil {
		return err
	}

	docs := []dbpkg.Document{}
	if err := sqlx.SelectContext(r.Context(), h.db, &docs, dbpkg.QDocumentsList, user.ID, from, to); err != nil {
		return err
	}
	respondJSON(w, http.StatusOK, documentsResponse{Documents: docs})
	return nil
}

// create validates and computes the payload, then persists doc +
// lines + totals atomically. On any error inside the transaction we
// roll back and surface the original error.
func (h *DocumentHandlers) create(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}

	var req createDocumentRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	issueDate, err := time.Parse(dateFormat, req.IssueDate)
	if err != nil {
		return apperr.NewBadRequest("INVALID_DATE", "issue_date must be YYYY-MM-DD", "issue_date")
	}

	// Validate + compute up front, before opening a transaction.
	// This keeps the tx window tight and returns richer field-path
	// errors than any post-tx failure could.
	computed, err := calc.ComputeDocument(req.Lines)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := h.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	// If we return before commit, this rollback runs and undoes
	// whatever partial work happened. The post-commit rollback is
	// a no-op per database/sql.
	defer func() { _ = tx.Rollback() }()

	var inserted struct {
		ID uuid.UUID `db:"id"`
		// Additional RETURNING columns are unused here — kept in
		// the query for symmetry with the spec, discarded because
		// we call getDocWithLines afterwards for the response.
		Subtotal      any       `db:"subtotal"`
		TotalDiscount any       `db:"total_discount"`
		TotalTax      any       `db:"total_tax"`
		GrandTotal    any       `db:"grand_total"`
		Status        string    `db:"status"`
		CreatedAt     time.Time `db:"created_at"`
		UpdatedAt     time.Time `db:"updated_at"`
	}
	if err := sqlx.GetContext(ctx, tx, &inserted, dbpkg.QDocumentInsert,
		user.ID, req.Title, req.Customer, issueDate); err != nil {
		return err
	}

	for i, ln := range req.Lines {
		c := computed.Lines[i]
		dt, dv, tp := nullableLineFields(ln)
		if _, err := tx.ExecContext(ctx, dbpkg.QLineInsert,
			inserted.ID, i+1, ln.Description, ln.Quantity, ln.UnitPrice,
			dt, dv, tp,
			c.LineSubtotal, c.DiscountAmount, c.AfterDiscount, c.TaxAmount, c.LineTotal,
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, dbpkg.QDocumentUpdateTotals,
		inserted.ID, computed.Subtotal, computed.TotalDiscount, computed.TotalTax, computed.GrandTotal,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	doc, err := getDocWithLines(ctx, h.db, inserted.ID, user.ID)
	if err != nil {
		return err
	}
	respondJSON(w, http.StatusCreated, documentResponse{Document: doc})
	return nil
}

// get returns a single owned document with lines populated.
func (h *DocumentHandlers) get(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}
	docID, err := parseDocID(r)
	if err != nil {
		return err
	}

	doc, err := getDocWithLines(r.Context(), h.db, docID, user.ID)
	if err != nil {
		return err
	}
	respondJSON(w, http.StatusOK, documentResponse{Document: doc})
	return nil
}

// update applies a partial patch to a draft document. Finalized
// documents are rejected via assertEditable before any UPDATE runs.
func (h *DocumentHandlers) update(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}
	docID, err := parseDocID(r)
	if err != nil {
		return err
	}

	var req updateDocumentRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	if _, err := assertEditable(r.Context(), h.db, docID, user.ID); err != nil {
		return err
	}

	var issueDate *time.Time
	if req.IssueDate != nil {
		d, perr := time.Parse(dateFormat, *req.IssueDate)
		if perr != nil {
			return apperr.NewBadRequest("INVALID_DATE", "issue_date must be YYYY-MM-DD", "issue_date")
		}
		issueDate = &d
	}

	var updated dbpkg.Document
	if err := sqlx.GetContext(r.Context(), h.db, &updated, dbpkg.QDocumentUpdateMeta,
		docID, user.ID, req.Title, req.Customer, issueDate,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NewNotFound("DOCUMENT_NOT_FOUND")
		}
		return err
	}

	lines, err := selectLines(r.Context(), h.db, docID)
	if err != nil {
		return err
	}
	updated.Lines = lines

	respondJSON(w, http.StatusOK, documentResponse{Document: &updated})
	return nil
}

// delete removes a draft document. The DB CASCADE takes care of
// lines. Finalized documents are refused via assertEditable.
func (h *DocumentHandlers) delete(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}
	docID, err := parseDocID(r)
	if err != nil {
		return err
	}

	if _, err := assertEditable(r.Context(), h.db, docID, user.ID); err != nil {
		return err
	}

	res, err := h.db.ExecContext(r.Context(), dbpkg.QDocumentDelete, docID, user.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.NewNotFound("DOCUMENT_NOT_FOUND")
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ------------------------------ helpers --------------------------------

// currentUser pulls the caller off context. Defensive against the
// programmer error of mounting a route outside the RequireAuth
// group — in production this never returns an error.
func currentUser(r *http.Request) (auth.User, error) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		return auth.User{}, apperr.NewUnauthorized("UNAUTHORIZED", "missing authenticated user")
	}
	return u, nil
}

// parseDocID reads {id} out of the chi URL and rejects malformed
// UUIDs before we hit the database.
func parseDocID(r *http.Request) (uuid.UUID, error) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, apperr.NewBadRequest("INVALID_ID", "invalid document id", "id")
	}
	return id, nil
}

// parseDateRange reads ?from=YYYY-MM-DD&to=YYYY-MM-DD off the
// request. Either or both may be absent; a present-but-malformed
// value is a 400. from > to is rejected as INVALID_DATE_RANGE.
func parseDateRange(r *http.Request) (from, to *time.Time, err error) {
	q := r.URL.Query()
	if s := q.Get("from"); s != "" {
		t, perr := time.Parse(dateFormat, s)
		if perr != nil {
			return nil, nil, apperr.NewBadRequest("INVALID_DATE", "from must be YYYY-MM-DD", "from")
		}
		from = &t
	}
	if s := q.Get("to"); s != "" {
		t, perr := time.Parse(dateFormat, s)
		if perr != nil {
			return nil, nil, apperr.NewBadRequest("INVALID_DATE", "to must be YYYY-MM-DD", "to")
		}
		to = &t
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, apperr.NewBadRequest("INVALID_DATE_RANGE", "from must be on or before to", "from")
	}
	return from, to, nil
}

// nullableLineFields converts the three nullable calc.LineInput
// fields into interface{} values ready for sqlx.Exec — nil for
// absent columns, plain string / decimal.Decimal for present ones.
// Passing typed nil pointers directly to lib/pq is possible but
// depends on subtle database/sql conversion rules; this is safer.
func nullableLineFields(l calc.LineInput) (dt, dv, tp any) {
	if l.DiscountType != nil {
		dt = string(*l.DiscountType)
	}
	if l.DiscountValue != nil {
		dv = *l.DiscountValue
	}
	if l.TaxPercent != nil {
		tp = *l.TaxPercent
	}
	return dt, dv, tp
}

// assertEditable enforces the write-guard for every mutating
// endpoint: the document must exist, be owned by userID, and be in
// draft status. The returned document may be discarded when the
// caller doesn't need it beyond the guard. q accepts either
// *sqlx.DB or *sqlx.Tx so callers already inside a transaction can
// share the row read.
func assertEditable(ctx context.Context, q sqlx.QueryerContext, docID, userID uuid.UUID) (*dbpkg.Document, error) {
	var doc dbpkg.Document
	err := sqlx.GetContext(ctx, q, &doc, dbpkg.QDocumentGet, docID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NewNotFound("DOCUMENT_NOT_FOUND")
	}
	if err != nil {
		return nil, err
	}
	if doc.Status == "finalized" {
		return nil, apperr.NewConflict("DOCUMENT_FINALIZED", "Document is finalized")
	}
	return &doc, nil
}

// getDocWithLines is the single canonical way to load a document
// plus its lines for a response. Ownership is enforced by the
// document query; lines don't need a secondary user_id check
// because the FK to documents already scopes them.
func getDocWithLines(ctx context.Context, db *sqlx.DB, docID, userID uuid.UUID) (*dbpkg.Document, error) {
	var doc dbpkg.Document
	err := sqlx.GetContext(ctx, db, &doc, dbpkg.QDocumentGet, docID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NewNotFound("DOCUMENT_NOT_FOUND")
	}
	if err != nil {
		return nil, err
	}
	lines, err := selectLines(ctx, db, docID)
	if err != nil {
		return nil, err
	}
	doc.Lines = lines
	return &doc, nil
}

// selectLines is the shared "give me the lines for this document"
// query. Split out so both getDocWithLines and update() can call it
// without duplicating the SelectContext ceremony.
func selectLines(ctx context.Context, q sqlx.QueryerContext, docID uuid.UUID) ([]dbpkg.LineItem, error) {
	lines := []dbpkg.LineItem{}
	if err := sqlx.SelectContext(ctx, q, &lines, dbpkg.QLinesForDoc, docID); err != nil {
		return nil, err
	}
	return lines, nil
}
