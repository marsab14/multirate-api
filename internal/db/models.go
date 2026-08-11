package db

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Document mirrors a row in the `documents` table. Lines is not part
// of the row — handlers hydrate it with a second query when they
// need the full aggregate. Kept as `omitempty` so list responses can
// omit it without shipping a `"lines": null`.
type Document struct {
	ID            uuid.UUID       `db:"id" json:"id"`
	UserID        uuid.UUID       `db:"user_id" json:"user_id"`
	Title         string          `db:"title" json:"title"`
	Customer      string          `db:"customer" json:"customer"`
	IssueDate     time.Time       `db:"issue_date" json:"issue_date"`
	Status        string          `db:"status" json:"status"`
	Subtotal      decimal.Decimal `db:"subtotal" json:"subtotal"`
	TotalDiscount decimal.Decimal `db:"total_discount" json:"total_discount"`
	TotalTax      decimal.Decimal `db:"total_tax" json:"total_tax"`
	GrandTotal    decimal.Decimal `db:"grand_total" json:"grand_total"`
	FinalizedAt   *time.Time      `db:"finalized_at" json:"finalized_at,omitempty"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`

	Lines []LineItem `json:"lines,omitempty"`
}

// LineItem mirrors a row in the `line_items` table. Discount and tax
// fields are pointers because the columns are nullable: a line may
// have no discount (both discount_type and discount_value NULL) and
// no tax (tax_percent NULL). Derived money columns
// (line_subtotal … line_total) are computed and persisted by the
// server on every write, never trusted from the client.
type LineItem struct {
	ID             uuid.UUID        `db:"id" json:"id"`
	DocumentID     uuid.UUID        `db:"document_id" json:"document_id"`
	Position       int              `db:"position" json:"position"`
	Description    string           `db:"description" json:"description"`
	Quantity       int              `db:"quantity" json:"quantity"`
	UnitPrice      decimal.Decimal  `db:"unit_price" json:"unit_price"`
	DiscountType   *string          `db:"discount_type" json:"discount_type"`
	DiscountValue  *decimal.Decimal `db:"discount_value" json:"discount_value"`
	TaxPercent     *decimal.Decimal `db:"tax_percent" json:"tax_percent"`
	LineSubtotal   decimal.Decimal  `db:"line_subtotal" json:"line_subtotal"`
	DiscountAmount decimal.Decimal  `db:"discount_amount" json:"discount_amount"`
	AfterDiscount  decimal.Decimal  `db:"after_discount" json:"after_discount"`
	TaxAmount      decimal.Decimal  `db:"tax_amount" json:"tax_amount"`
	LineTotal      decimal.Decimal  `db:"line_total" json:"line_total"`
	CreatedAt      time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time        `db:"updated_at" json:"updated_at"`
}
