package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"

	"billing-api/internal/apperr"
	"billing-api/internal/calc"
	dbpkg "billing-api/internal/db"
)

// LineHandlers owns /api/documents/{id}/lines/*. Every mutation is
// transactional and finishes by recomputing document totals; the
// response is always the full updated document so the frontend can
// replace local state in one shot.
type LineHandlers struct {
	db *sqlx.DB
}

func NewLineHandlers(db *sqlx.DB) *LineHandlers {
	return &LineHandlers{db: db}
}

// Mount registers create/update/delete on r. Caller mounts r at
// /api/documents/{id}/lines — the {id} URL param is read via
// parseDocID (defined in documents.go).
func (h *LineHandlers) Mount(r chi.Router) {
	r.Method(http.MethodPost, "/", appHandler(h.create))
	r.Method(http.MethodPatch, "/{lineId}", appHandler(h.update))
	r.Method(http.MethodDelete, "/{lineId}", appHandler(h.delete))
}

// ------------------------- request shapes -----------------------------

// createLineRequest is a LineInput plus an optional Position. The
// embedded LineInput carries the validate tags calc already relies
// on so we don't restate them here.
type createLineRequest struct {
	calc.LineInput
	Position *int `json:"position" validate:"omitempty,min=1"`
}

// updateLineRequest is the PATCH shape. Every field is a pointer so
// "not provided" is distinguishable from "provided as zero value".
//
// Known limitation: clearing an existing discount (setting both
// discount_type and discount_value back to NULL) is NOT supported
// through this endpoint — you can only overwrite them with new
// values. Sending discount_value=0 with discount_type=fixed gives
// you an effective net-zero discount; a full reset requires
// DELETE + POST. Called out to keep the JSON contract simple.
type updateLineRequest struct {
	Description   *string            `json:"description"    validate:"omitempty,min=1,max=500"`
	Quantity      *int               `json:"quantity"       validate:"omitempty,min=1"`
	UnitPrice     *decimal.Decimal   `json:"unit_price"`
	DiscountType  *calc.DiscountType `json:"discount_type"  validate:"omitempty,oneof=fixed percent"`
	DiscountValue *decimal.Decimal   `json:"discount_value"`
	TaxPercent    *decimal.Decimal   `json:"tax_percent"`
	Position      *int               `json:"position"       validate:"omitempty,min=1"`
}

// ------------------------------ handlers ------------------------------

// create adds a line to a draft document. Position auto-assigns to
// max(position)+1 when omitted from the request. Full flow: compute
// → tx → assertEditable → insert → recompute doc → commit → return
// hydrated document.
func (h *LineHandlers) create(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}
	docID, err := parseDocID(r)
	if err != nil {
		return err
	}

	var req createLineRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	computed, err := calc.ComputeLine(req.LineInput)
	if err != nil {
		return prefixLineField(err)
	}

	ctx := r.Context()
	tx, err := h.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := assertEditable(ctx, tx, docID, user.ID); err != nil {
		return err
	}

	position, err := resolvePosition(ctx, tx, docID, req.Position)
	if err != nil {
		return err
	}

	dt, dv, tp := nullableLineFields(req.LineInput)
	var ins lineInsertResult
	if err := sqlx.GetContext(ctx, tx, &ins, dbpkg.QLineInsert,
		docID, position, req.Description, req.Quantity, req.UnitPrice,
		dt, dv, tp,
		computed.LineSubtotal, computed.DiscountAmount, computed.AfterDiscount,
		computed.TaxAmount, computed.LineTotal,
	); err != nil {
		return err
	}

	if err := recomputeDocInTx(ctx, tx, docID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	doc, err := getDocWithLines(ctx, h.db, docID, user.ID)
	if err != nil {
		return err
	}
	respondJSON(w, http.StatusCreated, documentResponse{Document: doc})
	return nil
}

// update PATCHes a line. Merges the incoming partial onto the
// existing row, revalidates via ComputeLine, then rewrites the
// input + derived columns in a single UPDATE. Line 404s if it
// exists under a different document than the URL claims — an
// ownership check by proxy, since the parent document is already
// scoped to the caller by assertEditable.
func (h *LineHandlers) update(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}
	docID, err := parseDocID(r)
	if err != nil {
		return err
	}
	lineID, err := parseLineID(r)
	if err != nil {
		return err
	}

	var req updateLineRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := h.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := assertEditable(ctx, tx, docID, user.ID); err != nil {
		return err
	}

	var existing dbpkg.LineItem
	if err := sqlx.GetContext(ctx, tx, &existing, dbpkg.QLineGet, lineID, docID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NewNotFound("LINE_NOT_FOUND")
		}
		return err
	}

	merged := mergeLineUpdate(existing, req)
	computed, err := calc.ComputeLine(merged)
	if err != nil {
		return prefixLineField(err)
	}

	position := existing.Position
	if req.Position != nil {
		position = *req.Position
	}

	dt, dv, tp := nullableLineFields(merged)
	if _, err := tx.ExecContext(ctx, dbpkg.QLineUpdate,
		lineID, docID,
		merged.Description, merged.Quantity, merged.UnitPrice,
		dt, dv, tp,
		position,
		computed.LineSubtotal, computed.DiscountAmount, computed.AfterDiscount,
		computed.TaxAmount, computed.LineTotal,
	); err != nil {
		return err
	}

	if err := recomputeDocInTx(ctx, tx, docID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	doc, err := getDocWithLines(ctx, h.db, docID, user.ID)
	if err != nil {
		return err
	}
	respondJSON(w, http.StatusOK, documentResponse{Document: doc})
	return nil
}

// delete removes a line and rebases document totals. RowsAffected
// == 0 means the line either never existed or belonged to a
// different document — 404 either way.
func (h *LineHandlers) delete(w http.ResponseWriter, r *http.Request) error {
	user, err := currentUser(r)
	if err != nil {
		return err
	}
	docID, err := parseDocID(r)
	if err != nil {
		return err
	}
	lineID, err := parseLineID(r)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := h.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := assertEditable(ctx, tx, docID, user.ID); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, dbpkg.QLineDelete, lineID, docID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.NewNotFound("LINE_NOT_FOUND")
	}

	if err := recomputeDocInTx(ctx, tx, docID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	doc, err := getDocWithLines(ctx, h.db, docID, user.ID)
	if err != nil {
		return err
	}
	respondJSON(w, http.StatusOK, documentResponse{Document: doc})
	return nil
}

// ------------------------------ helpers -------------------------------

// lineInsertResult consumes the RETURNING columns of QLineInsert.
// sqlx requires a destination for every returned column; we don't
// actually use these values (getDocWithLines refetches after commit)
// but they need somewhere to land.
type lineInsertResult struct {
	ID        uuid.UUID `db:"id"`
	CreatedAt any       `db:"created_at"`
	UpdatedAt any       `db:"updated_at"`
}

// parseLineID extracts and validates the {lineId} URL param.
func parseLineID(r *http.Request) (uuid.UUID, error) {
	idStr := chi.URLParam(r, "lineId")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, apperr.NewBadRequest("INVALID_ID", "invalid line id", "lineId")
	}
	return id, nil
}

// resolvePosition returns the caller-supplied position when
// present, else max(position)+1 for the document. Runs inside the
// transaction so we get a consistent view.
func resolvePosition(ctx context.Context, tx *sqlx.Tx, docID uuid.UUID, requested *int) (int, error) {
	if requested != nil {
		return *requested, nil
	}
	var maxPos int64
	if err := sqlx.GetContext(ctx, tx, &maxPos, dbpkg.QLineMaxPosition, docID); err != nil {
		return 0, err
	}
	return int(maxPos) + 1, nil
}

// mergeLineUpdate overlays the request's non-nil fields onto the
// existing row, producing a calc.LineInput ready for ComputeLine.
// Nil fields are treated as "leave existing value alone".
func mergeLineUpdate(existing dbpkg.LineItem, req updateLineRequest) calc.LineInput {
	out := calc.LineInput{
		Description:   existing.Description,
		Quantity:      existing.Quantity,
		UnitPrice:     existing.UnitPrice,
		DiscountValue: existing.DiscountValue,
		TaxPercent:    existing.TaxPercent,
	}
	if existing.DiscountType != nil {
		dt := calc.DiscountType(*existing.DiscountType)
		out.DiscountType = &dt
	}

	if req.Description != nil {
		out.Description = *req.Description
	}
	if req.Quantity != nil {
		out.Quantity = *req.Quantity
	}
	if req.UnitPrice != nil {
		out.UnitPrice = *req.UnitPrice
	}
	if req.DiscountType != nil {
		dt := *req.DiscountType
		out.DiscountType = &dt
	}
	if req.DiscountValue != nil {
		v := *req.DiscountValue
		out.DiscountValue = &v
	}
	if req.TaxPercent != nil {
		v := *req.TaxPercent
		out.TaxPercent = &v
	}
	return out
}

// recomputeDocInTx reloads every line for docID, re-runs
// ComputeDocument, and persists the results back — per-line
// derived columns via persistComputedLines and the four aggregates
// via QDocumentUpdateTotals. Must run inside the same transaction
// as the mutation that triggered it so the recomputed state and
// the mutation commit atomically.
func recomputeDocInTx(ctx context.Context, tx *sqlx.Tx, docID uuid.UUID) error {
	lines := []dbpkg.LineItem{}
	if err := sqlx.SelectContext(ctx, tx, &lines, dbpkg.QLinesForDoc, docID); err != nil {
		return err
	}

	inputs := make([]calc.LineInput, len(lines))
	for i, l := range lines {
		inputs[i] = lineItemToInput(l)
	}

	computed, err := calc.ComputeDocument(inputs)
	if err != nil {
		// Reaching this branch means stored line inputs failed
		// validation — a data-consistency issue that a client
		// can't fix. Surface as-is (becomes a 500 via appHandler).
		return err
	}

	if err := persistComputedLines(ctx, tx, lines, computed); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, dbpkg.QDocumentUpdateTotals,
		docID, computed.Subtotal, computed.TotalDiscount, computed.TotalTax, computed.GrandTotal,
	); err != nil {
		return err
	}
	return nil
}

// persistComputedLines writes the derived money columns for each
// element of lines using the matching entry in computed.Lines
// (position-correlated). Callers guarantee len(lines) ==
// len(computed.Lines) — it is always true after
// ComputeDocument(inputs) where inputs was built from lines.
func persistComputedLines(ctx context.Context, tx *sqlx.Tx, lines []dbpkg.LineItem, computed calc.DocumentComputed) error {
	for i, l := range lines {
		c := computed.Lines[i]
		if _, err := tx.ExecContext(ctx, dbpkg.QLineUpdateComputed,
			l.ID, c.LineSubtotal, c.DiscountAmount, c.AfterDiscount, c.TaxAmount, c.LineTotal,
		); err != nil {
			return err
		}
	}
	return nil
}

// lineItemToInput converts a DB row into the LineInput shape calc
// wants. The two structs differ only in a couple of pointer types
// (discount_type in DB is *string, in calc it's *DiscountType).
func lineItemToInput(li dbpkg.LineItem) calc.LineInput {
	in := calc.LineInput{
		Description:   li.Description,
		Quantity:      li.Quantity,
		UnitPrice:     li.UnitPrice,
		DiscountValue: li.DiscountValue,
		TaxPercent:    li.TaxPercent,
	}
	if li.DiscountType != nil {
		dt := calc.DiscountType(*li.DiscountType)
		in.DiscountType = &dt
	}
	return in
}

// prefixLineField rewrites the field path on a calc-originated
// AppError from "quantity" → "line.quantity" (etc.) so clients
// PATCHing a single line get a path scoped to the endpoint they
// hit. The multi-line "lines.N.field" prefix is applied inside
// ComputeDocument and lives on POST /documents instead.
func prefixLineField(err error) error {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return apperr.NewBadRequest(ae.Code, ae.Message, "line."+ae.Field)
	}
	return err
}
