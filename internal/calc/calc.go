// Package calc holds the pure money math: line totals, discount
// application, tax application, and document aggregation. Everything
// here is deterministic and side-effect free so it can be exercised
// by table-driven tests.
//
// Implementations arrive in B3; this file exists so the layout
// compiles from B1 onward.
package calc
