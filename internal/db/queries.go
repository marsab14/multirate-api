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
)
