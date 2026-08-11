// Package calc is the pure money-math core: per-line validation +
// computation and per-document aggregation. It has no DB, HTTP, or
// logging dependencies — only shopspring/decimal and apperr — so it
// can be exhaustively covered by table-driven tests.
//
// All returned validation failures are *apperr.AppError values with
// SNAKE_CASE codes matching the API contract.
package calc

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"billing-api/internal/apperr"
)

// DiscountType is the JSON enum sent by clients. Kept as a named
// string so struct-tag validation can enforce oneof=fixed percent.
type DiscountType string

const (
	DiscountFixed   DiscountType = "fixed"
	DiscountPercent DiscountType = "percent"
)

// LineInput is the client-supplied shape of a single line. Struct
// tags are for downstream go-playground/validator use; calc itself
// re-validates everything so it can be trusted in isolation.
type LineInput struct {
	Quantity      int              `json:"quantity"       validate:"required,min=1"`
	UnitPrice     decimal.Decimal  `json:"unit_price"     validate:"required"`
	DiscountType  *DiscountType    `json:"discount_type"  validate:"omitempty,oneof=fixed percent"`
	DiscountValue *decimal.Decimal `json:"discount_value"`
	TaxPercent    *decimal.Decimal `json:"tax_percent"`
	Description   string           `json:"description"    validate:"required,max=500"`
}

// LineComputed is the server-derived money view of a line. Every
// field is >= 0 and pinned to exactly 2 decimal places.
type LineComputed struct {
	LineSubtotal   decimal.Decimal `json:"line_subtotal"`
	DiscountAmount decimal.Decimal `json:"discount_amount"`
	AfterDiscount  decimal.Decimal `json:"after_discount"`
	TaxAmount      decimal.Decimal `json:"tax_amount"`
	LineTotal      decimal.Decimal `json:"line_total"`
}

// DocumentComputed is the aggregated view returned by ComputeDocument.
// Totals are pure sums of already-rounded line values.
type DocumentComputed struct {
	Subtotal      decimal.Decimal `json:"subtotal"`
	TotalDiscount decimal.Decimal `json:"total_discount"`
	TotalTax      decimal.Decimal `json:"total_tax"`
	GrandTotal    decimal.Decimal `json:"grand_total"`
	Lines         []LineComputed  `json:"lines"`
}

var (
	zero    = decimal.Zero
	hundred = decimal.NewFromInt(100)
)

// round2 rounds to 2 decimal places. shopspring/decimal's Round uses
// HALF_AWAY_FROM_ZERO, which for our non-negative money values is
// exactly equivalent to HALF_UP (the policy pinned in the spec).
// Guard: never pass a negative Decimal through here without
// reasoning about that equivalence.
func round2(d decimal.Decimal) decimal.Decimal {
	return d.Round(2)
}

// ComputeLine validates a single LineInput and returns the derived
// money view. On validation failure it returns a *apperr.AppError
// whose Field is relative to the line (e.g. "quantity",
// "discount_value") — ComputeDocument prefixes the "lines.%d." path.
func ComputeLine(in LineInput) (LineComputed, error) {
	if in.Quantity < 1 {
		return LineComputed{}, apperr.NewBadRequest(
			"INVALID_QUANTITY", "quantity must be >= 1", "quantity")
	}
	if in.UnitPrice.IsNegative() {
		return LineComputed{}, apperr.NewBadRequest(
			"INVALID_UNIT_PRICE", "unit_price must be >= 0", "unit_price")
	}

	lineSubtotal := round2(in.UnitPrice.Mul(decimal.NewFromInt(int64(in.Quantity))))

	typeSet := in.DiscountType != nil
	valSet := in.DiscountValue != nil
	if typeSet != valSet {
		field := "discount_value"
		msg := "discount_value is required when discount_type is set"
		if valSet && !typeSet {
			field = "discount_type"
			msg = "discount_type is required when discount_value is set"
		}
		return LineComputed{}, apperr.NewBadRequest("INVALID_DISCOUNT_TYPE", msg, field)
	}

	discountAmount := zero
	if typeSet {
		v := *in.DiscountValue
		switch *in.DiscountType {
		case DiscountPercent:
			if v.LessThan(zero) || v.GreaterThan(hundred) {
				return LineComputed{}, apperr.NewBadRequest(
					"INVALID_PERCENT", "discount percent must be in [0, 100]", "discount_value")
			}
			discountAmount = round2(lineSubtotal.Mul(v).Div(hundred))
		case DiscountFixed:
			if v.LessThan(zero) {
				return LineComputed{}, apperr.NewBadRequest(
					"INVALID_DISCOUNT_VALUE", "fixed discount must be >= 0", "discount_value")
			}
			if v.GreaterThan(lineSubtotal) {
				return LineComputed{}, apperr.NewBadRequest(
					"DISCOUNT_EXCEEDS_SUBTOTAL", "fixed discount exceeds line subtotal", "discount_value")
			}
			discountAmount = round2(v)
		default:
			return LineComputed{}, apperr.NewBadRequest(
				"INVALID_DISCOUNT_TYPE", "discount_type must be 'fixed' or 'percent'", "discount_type")
		}
	}

	afterDiscount := round2(lineSubtotal.Sub(discountAmount))

	taxAmount := zero
	if in.TaxPercent != nil {
		tp := *in.TaxPercent
		if tp.LessThan(zero) || tp.GreaterThan(hundred) {
			return LineComputed{}, apperr.NewBadRequest(
				"INVALID_PERCENT", "tax_percent must be in [0, 100]", "tax_percent")
		}
		taxAmount = round2(afterDiscount.Mul(tp).Div(hundred))
	}

	lineTotal := round2(afterDiscount.Add(taxAmount))

	return LineComputed{
		LineSubtotal:   lineSubtotal,
		DiscountAmount: discountAmount,
		AfterDiscount:  afterDiscount,
		TaxAmount:      taxAmount,
		LineTotal:      lineTotal,
	}, nil
}

// ComputeDocument runs ComputeLine over each input line and sums the
// results. Document totals are sums of already-rounded line values,
// so no additional rounding is arithmetically necessary; the explicit
// round2 calls at the end are defensive against any internal
// precision drift and guarantee 2dp output.
//
// On the first line-level error, ComputeDocument returns immediately
// with the offending field path rewritten as "lines.<i>.<subpath>"
// so callers can point clients at the exact input that failed.
func ComputeDocument(lines []LineInput) (DocumentComputed, error) {
	out := DocumentComputed{
		Subtotal:      zero,
		TotalDiscount: zero,
		TotalTax:      zero,
		GrandTotal:    zero,
		Lines:         make([]LineComputed, 0, len(lines)),
	}
	for i, ln := range lines {
		c, err := ComputeLine(ln)
		if err != nil {
			var appErr *apperr.AppError
			if errors.As(err, &appErr) {
				return DocumentComputed{}, apperr.NewBadRequest(
					appErr.Code,
					appErr.Message,
					fmt.Sprintf("lines.%d.%s", i, appErr.Field),
				)
			}
			return DocumentComputed{}, err
		}
		out.Lines = append(out.Lines, c)
		out.Subtotal = out.Subtotal.Add(c.LineSubtotal)
		out.TotalDiscount = out.TotalDiscount.Add(c.DiscountAmount)
		out.TotalTax = out.TotalTax.Add(c.TaxAmount)
		out.GrandTotal = out.GrandTotal.Add(c.LineTotal)
	}
	out.Subtotal = round2(out.Subtotal)
	out.TotalDiscount = round2(out.TotalDiscount)
	out.TotalTax = round2(out.TotalTax)
	out.GrandTotal = round2(out.GrandTotal)
	return out, nil
}
