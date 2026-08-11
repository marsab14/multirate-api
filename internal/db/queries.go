package db

// SQL query constants live here so each statement can be reviewed
// and reformatted in one place. Column lists are spelled out (no
// SELECT * / RETURNING *) so a stray migration adding a column
// doesn't silently break scanning.

const (
	// QDocumentsList returns every document owned by $1 (user_id),
	// optionally bounded by $2 (from) and $3 (to). NULL args skip
	// the corresponding filter — see the ::date IS NULL guard.
	// Ordered newest-first with id as the deterministic tie-breaker.
	QDocumentsList = `
		SELECT id, user_id, title, customer, issue_date, status,
		       subtotal, total_discount, total_tax, grand_total,
		       finalized_at, created_at, updated_at
		FROM documents
		WHERE user_id = $1
		  AND ($2::date IS NULL OR issue_date >= $2)
		  AND ($3::date IS NULL OR issue_date <= $3)
		ORDER BY issue_date DESC, id DESC`

	// QDocumentGet fetches a single owned document. Ownership is
	// enforced in the WHERE clause; sql.ErrNoRows on miss.
	QDocumentGet = `
		SELECT id, user_id, title, customer, issue_date, status,
		       subtotal, total_discount, total_tax, grand_total,
		       finalized_at, created_at, updated_at
		FROM documents
		WHERE id = $1 AND user_id = $2`

	// QDocumentGetForUpdate is QDocumentGet plus a row-level lock.
	// Used by finalize to serialize concurrent finalize attempts:
	// the first tx to grab the row lock proceeds; a competing tx
	// blocks on this SELECT and sees status='finalized' once the
	// first commits, then returns 409 DOCUMENT_ALREADY_FINALIZED.
	QDocumentGetForUpdate = `
		SELECT id, user_id, title, customer, issue_date, status,
		       subtotal, total_discount, total_tax, grand_total,
		       finalized_at, created_at, updated_at
		FROM documents
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`

	// QLinesForDoc pulls every line belonging to a document, in
	// display order. Callers should verify document ownership
	// separately before hitting this — this query is not scoped
	// by user_id.
	QLinesForDoc = `
		SELECT id, document_id, position, description, quantity, unit_price,
		       discount_type, discount_value, tax_percent,
		       line_subtotal, discount_amount, after_discount,
		       tax_amount, line_total, created_at, updated_at
		FROM line_items
		WHERE document_id = $1
		ORDER BY position ASC`

	// QDocumentInsert creates a draft document. Money columns and
	// status default to zero / 'draft' at the schema level; the
	// RETURNING clause hands those defaults back so the handler
	// doesn't need a second SELECT to compose the response.
	QDocumentInsert = `
		INSERT INTO documents (user_id, title, customer, issue_date)
		VALUES ($1, $2, $3, $4)
		RETURNING id, subtotal, total_discount, total_tax, grand_total,
		          status, created_at, updated_at`

	// QDocumentUpdateMeta is the partial-update pattern: any of
	// $3/$4/$5 may be NULL to leave the corresponding column
	// unchanged. Ownership is enforced in the WHERE. RETURNING *
	// gives us back the freshly-triggered updated_at.
	QDocumentUpdateMeta = `
		UPDATE documents
		SET title      = COALESCE($3, title),
		    customer   = COALESCE($4, customer),
		    issue_date = COALESCE($5, issue_date)
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, title, customer, issue_date, status,
		          subtotal, total_discount, total_tax, grand_total,
		          finalized_at, created_at, updated_at`

	// QDocumentDelete removes an owned document; the FK on
	// line_items has ON DELETE CASCADE so lines vanish with it.
	QDocumentDelete = `DELETE FROM documents WHERE id = $1 AND user_id = $2`

	// QDocumentUpdateTotals writes the aggregate money columns
	// computed from the current line set. Called at the end of any
	// mutation that touches lines. Does not enforce ownership; the
	// caller is expected to have already asserted editability via
	// assertEditable (which does).
	QDocumentUpdateTotals = `
		UPDATE documents
		SET subtotal = $2, total_discount = $3, total_tax = $4, grand_total = $5
		WHERE id = $1`

	// QDocumentFinalize writes the aggregate money columns and
	// atomically flips status to 'finalized' with a wall-clock
	// finalized_at. Ownership isn't re-checked here — the finalize
	// handler already selected the row FOR UPDATE by (id, user_id).
	QDocumentFinalize = `
		UPDATE documents
		SET subtotal = $2, total_discount = $3, total_tax = $4, grand_total = $5,
		    status = 'finalized', finalized_at = NOW()
		WHERE id = $1`

	// QLineInsert persists a line together with its server-computed
	// derived columns. Callers must never trust computed values
	// from the client — always recompute via calc first.
	QLineInsert = `
		INSERT INTO line_items (
		  document_id, position, description, quantity, unit_price,
		  discount_type, discount_value, tax_percent,
		  line_subtotal, discount_amount, after_discount, tax_amount, line_total
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, created_at, updated_at`

	// QLineGet fetches a single line scoped to (id, document_id).
	// The document_id predicate is the ownership guard — combined
	// with an earlier assertEditable on the parent document, this
	// ensures nobody can PATCH/DELETE another user's line by id.
	QLineGet = `
		SELECT id, document_id, position, description, quantity, unit_price,
		       discount_type, discount_value, tax_percent,
		       line_subtotal, discount_amount, after_discount,
		       tax_amount, line_total, created_at, updated_at
		FROM line_items
		WHERE id = $1 AND document_id = $2`

	// QLineUpdate rewrites both the input columns and the
	// server-computed columns for a line. Full replacement (not
	// COALESCE) because the caller has already merged the incoming
	// patch with the stored row and recomputed derived values.
	QLineUpdate = `
		UPDATE line_items
		SET description     = $3,
		    quantity        = $4,
		    unit_price      = $5,
		    discount_type   = $6,
		    discount_value  = $7,
		    tax_percent     = $8,
		    position        = $9,
		    line_subtotal   = $10,
		    discount_amount = $11,
		    after_discount  = $12,
		    tax_amount      = $13,
		    line_total      = $14
		WHERE id = $1 AND document_id = $2`

	// QLineDelete removes a single line if it belongs to the given
	// document. RowsAffected == 0 → 404 LINE_NOT_FOUND at the
	// handler layer.
	QLineDelete = `DELETE FROM line_items WHERE id = $1 AND document_id = $2`

	// QLineMaxPosition returns the current maximum position value
	// for a document's lines, defaulting to 0 for an empty doc.
	// Used to auto-assign position when a POST /lines request
	// omits it.
	QLineMaxPosition = `
		SELECT COALESCE(MAX(position), 0)
		FROM line_items
		WHERE document_id = $1`

	// QLineUpdateComputed rewrites just the derived money columns
	// for a line. Called during the recompute pass after any line
	// mutation so all lines stay in sync with pure calc output.
	// Input columns are untouched.
	QLineUpdateComputed = `
		UPDATE line_items
		SET line_subtotal   = $2,
		    discount_amount = $3,
		    after_discount  = $4,
		    tax_amount      = $5,
		    line_total      = $6
		WHERE id = $1`

	// QReportSummary aggregates persisted document totals for the
	// caller, optionally bounded by $2 (from) / $3 (to). Empty
	// result is not an error — COUNT returns 0, COALESCE turns the
	// NULL SUMs into 0. The response layer formats via
	// .StringFixed(2) so 0 renders as "0.00" for wire consistency.
	//
	// Intentionally aggregates document.grand_total (etc.) rather
	// than re-summing lines: this keeps the numbers identical to
	// what each individual document's row shows.
	QReportSummary = `
		SELECT
		  COUNT(*)::int                     AS document_count,
		  COALESCE(SUM(grand_total), 0)     AS total_grand_total,
		  COALESCE(SUM(total_tax), 0)       AS total_tax,
		  COALESCE(SUM(total_discount), 0)  AS total_discount
		FROM documents
		WHERE user_id = $1
		  AND ($2::date IS NULL OR issue_date >= $2)
		  AND ($3::date IS NULL OR issue_date <= $3)`
)
