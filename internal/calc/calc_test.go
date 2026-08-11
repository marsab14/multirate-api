package calc

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"billing-api/internal/apperr"
)

func dec(s string) decimal.Decimal            { return decimal.RequireFromString(s) }
func ptrDec(s string) *decimal.Decimal        { d := dec(s); return &d }
func ptrType(t DiscountType) *DiscountType    { return &t }

// assertLine compares computed values via StringFixed(2) so that two
// Decimals representing the same money value but with different
// internal Exp always compare equal.
func assertLine(t *testing.T, want, got LineComputed) {
	t.Helper()
	require.Equal(t, want.LineSubtotal.StringFixed(2), got.LineSubtotal.StringFixed(2), "line_subtotal")
	require.Equal(t, want.DiscountAmount.StringFixed(2), got.DiscountAmount.StringFixed(2), "discount_amount")
	require.Equal(t, want.AfterDiscount.StringFixed(2), got.AfterDiscount.StringFixed(2), "after_discount")
	require.Equal(t, want.TaxAmount.StringFixed(2), got.TaxAmount.StringFixed(2), "tax_amount")
	require.Equal(t, want.LineTotal.StringFixed(2), got.LineTotal.StringFixed(2), "line_total")
}

func TestComputeLine(t *testing.T) {
	tests := []struct {
		name     string
		input    LineInput
		expected LineComputed
		wantErr  string // AppError.Code; empty means success
	}{
		{
			name: "percent discount only, no tax",
			input: LineInput{
				Description: "widget", Quantity: 2, UnitPrice: dec("100.00"),
				DiscountType: ptrType(DiscountPercent), DiscountValue: ptrDec("10"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("200.00"),
				DiscountAmount: dec("20.00"),
				AfterDiscount:  dec("180.00"),
				TaxAmount:      dec("0.00"),
				LineTotal:      dec("180.00"),
			},
		},
		{
			name: "fixed discount only, no tax",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("200.00"),
				DiscountType: ptrType(DiscountFixed), DiscountValue: ptrDec("20"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("200.00"),
				DiscountAmount: dec("20.00"),
				AfterDiscount:  dec("180.00"),
				TaxAmount:      dec("0.00"),
				LineTotal:      dec("180.00"),
			},
		},
		{
			name: "tax only, no discount",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				TaxPercent: ptrDec("5"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("50.00"),
				DiscountAmount: dec("0.00"),
				AfterDiscount:  dec("50.00"),
				TaxAmount:      dec("2.50"),
				LineTotal:      dec("52.50"),
			},
		},
		{
			name: "no discount, no tax",
			input: LineInput{
				Description: "widget", Quantity: 3, UnitPrice: dec("15.00"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("45.00"),
				DiscountAmount: dec("0.00"),
				AfterDiscount:  dec("45.00"),
				TaxAmount:      dec("0.00"),
				LineTotal:      dec("45.00"),
			},
		},
		{
			name: "fixed discount equal to subtotal, then tax on zero",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("100.00"),
				DiscountType: ptrType(DiscountFixed), DiscountValue: ptrDec("100.00"),
				TaxPercent: ptrDec("5"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("100.00"),
				DiscountAmount: dec("100.00"),
				AfterDiscount:  dec("0.00"),
				TaxAmount:      dec("0.00"),
				LineTotal:      dec("0.00"),
			},
		},
		{
			name: "percent discount 33.33 rounds correctly",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("100.00"),
				DiscountType: ptrType(DiscountPercent), DiscountValue: ptrDec("33.33"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("100.00"),
				DiscountAmount: dec("33.33"),
				AfterDiscount:  dec("66.67"),
				TaxAmount:      dec("0.00"),
				LineTotal:      dec("66.67"),
			},
		},
		{
			name: "5% tax of 189.99 rounds up half",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("189.99"),
				TaxPercent: ptrDec("5"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("189.99"),
				DiscountAmount: dec("0.00"),
				AfterDiscount:  dec("189.99"),
				TaxAmount:      dec("9.50"),
				LineTotal:      dec("199.49"),
			},
		},
		{
			name: "7% tax on 33.33 rounds down",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("33.33"),
				TaxPercent: ptrDec("7"),
			},
			expected: LineComputed{
				LineSubtotal:   dec("33.33"),
				DiscountAmount: dec("0.00"),
				AfterDiscount:  dec("33.33"),
				TaxAmount:      dec("2.33"),
				LineTotal:      dec("35.66"),
			},
		},

		// --- Error cases ---
		{
			name: "quantity zero rejected",
			input: LineInput{
				Description: "widget", Quantity: 0, UnitPrice: dec("10.00"),
			},
			wantErr: "INVALID_QUANTITY",
		},
		{
			name: "quantity negative rejected",
			input: LineInput{
				Description: "widget", Quantity: -1, UnitPrice: dec("10.00"),
			},
			wantErr: "INVALID_QUANTITY",
		},
		{
			name: "negative unit price rejected",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("-0.01"),
			},
			wantErr: "INVALID_UNIT_PRICE",
		},
		{
			name: "fixed discount greater than subtotal rejected",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				DiscountType: ptrType(DiscountFixed), DiscountValue: ptrDec("50.01"),
			},
			wantErr: "DISCOUNT_EXCEEDS_SUBTOTAL",
		},
		{
			name: "percent discount above 100 rejected",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				DiscountType: ptrType(DiscountPercent), DiscountValue: ptrDec("100.01"),
			},
			wantErr: "INVALID_PERCENT",
		},
		{
			name: "percent discount below 0 rejected",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				DiscountType: ptrType(DiscountPercent), DiscountValue: ptrDec("-0.01"),
			},
			wantErr: "INVALID_PERCENT",
		},
		{
			name: "tax percent above 100 rejected",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				TaxPercent: ptrDec("100.01"),
			},
			wantErr: "INVALID_PERCENT",
		},
		{
			name: "tax percent below 0 rejected",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				TaxPercent: ptrDec("-0.01"),
			},
			wantErr: "INVALID_PERCENT",
		},
		{
			name: "fixed discount negative rejected",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				DiscountType: ptrType(DiscountFixed), DiscountValue: ptrDec("-1"),
			},
			wantErr: "INVALID_DISCOUNT_VALUE",
		},
		{
			name: "discount type set but value nil",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				DiscountType: ptrType(DiscountPercent),
			},
			wantErr: "INVALID_DISCOUNT_TYPE",
		},
		{
			name: "discount value set but type nil",
			input: LineInput{
				Description: "widget", Quantity: 1, UnitPrice: dec("50.00"),
				DiscountValue: ptrDec("10"),
			},
			wantErr: "INVALID_DISCOUNT_TYPE",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputeLine(tc.input)
			if tc.wantErr != "" {
				require.Error(t, err)
				var appErr *apperr.AppError
				require.True(t, errors.As(err, &appErr), "expected *apperr.AppError, got %T", err)
				require.Equal(t, tc.wantErr, appErr.Code)
				return
			}
			require.NoError(t, err)
			assertLine(t, tc.expected, got)
		})
	}
}

// TestSampleDocument reproduces the sample document from the spec.
// GrandTotal MUST equal 421.50; a regression here is a blocking bug.
func TestSampleDocument(t *testing.T) {
	lines := []LineInput{
		{
			Description: "Widget A", Quantity: 2, UnitPrice: dec("100.00"),
			DiscountType: ptrType(DiscountPercent), DiscountValue: ptrDec("10"),
			TaxPercent: ptrDec("5"),
		},
		{
			Description: "Widget B", Quantity: 1, UnitPrice: dec("50.00"),
			TaxPercent: ptrDec("5"),
		},
		{
			Description: "Service fee", Quantity: 1, UnitPrice: dec("200.00"),
			DiscountType: ptrType(DiscountFixed), DiscountValue: ptrDec("20"),
		},
	}
	doc, err := ComputeDocument(lines)
	require.NoError(t, err)
	require.Equal(t, "450.00", doc.Subtotal.StringFixed(2))
	require.Equal(t, "40.00", doc.TotalDiscount.StringFixed(2))
	require.Equal(t, "11.50", doc.TotalTax.StringFixed(2))
	require.Equal(t, "421.50", doc.GrandTotal.StringFixed(2))
	require.Equal(t, "189.00", doc.Lines[0].LineTotal.StringFixed(2))
	require.Equal(t, "52.50", doc.Lines[1].LineTotal.StringFixed(2))
	require.Equal(t, "180.00", doc.Lines[2].LineTotal.StringFixed(2))
}

func TestComputeDocument_Empty(t *testing.T) {
	doc, err := ComputeDocument(nil)
	require.NoError(t, err)
	require.Equal(t, "0.00", doc.Subtotal.StringFixed(2))
	require.Equal(t, "0.00", doc.TotalDiscount.StringFixed(2))
	require.Equal(t, "0.00", doc.TotalTax.StringFixed(2))
	require.Equal(t, "0.00", doc.GrandTotal.StringFixed(2))
	require.Empty(t, doc.Lines)
}

func TestComputeDocument_50IdenticalLines(t *testing.T) {
	lines := make([]LineInput, 50)
	for i := range lines {
		lines[i] = LineInput{Description: "widget", Quantity: 1, UnitPrice: dec("10.00")}
	}
	doc, err := ComputeDocument(lines)
	require.NoError(t, err)
	require.Len(t, doc.Lines, 50)
	require.Equal(t, "500.00", doc.Subtotal.StringFixed(2))
	require.Equal(t, "0.00", doc.TotalDiscount.StringFixed(2))
	require.Equal(t, "0.00", doc.TotalTax.StringFixed(2))
	require.Equal(t, "500.00", doc.GrandTotal.StringFixed(2))
}

// TestComputeDocument_FieldPathPrefix verifies that ComputeDocument
// prefixes the per-line field path with "lines.<i>." so downstream
// clients can point at the exact bad input.
func TestComputeDocument_FieldPathPrefix(t *testing.T) {
	lines := []LineInput{
		{Description: "ok", Quantity: 1, UnitPrice: dec("10.00")},
		{
			Description: "bad", Quantity: 1, UnitPrice: dec("10.00"),
			DiscountType: ptrType(DiscountFixed), DiscountValue: ptrDec("100"),
		},
	}
	_, err := ComputeDocument(lines)
	require.Error(t, err)
	var appErr *apperr.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, "DISCOUNT_EXCEEDS_SUBTOTAL", appErr.Code)
	require.Equal(t, "lines.1.discount_value", appErr.Field)
}
