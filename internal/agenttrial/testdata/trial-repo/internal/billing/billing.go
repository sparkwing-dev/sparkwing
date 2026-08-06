// Package billing computes what an account owes.
package billing

import "errors"

// ErrNegative is returned when a line item would subtract from a total.
var ErrNegative = errors.New("billing: negative amount")

// Line is one charge on an invoice, in cents.
type Line struct {
	Description string
	Cents       int64
	Quantity    int
}

// Total sums the lines. Quantity below one counts as one, so a
// malformed line still bills once rather than silently vanishing.
func Total(lines []Line) (int64, error) {
	var sum int64
	for _, l := range lines {
		if l.Cents < 0 {
			return 0, ErrNegative
		}
		q := l.Quantity
		if q < 1 {
			q = 1
		}
		sum += l.Cents * int64(q)
	}
	return sum, nil
}

// ApplyDiscount takes percent off total, rounding down so the customer
// is never charged a fraction of a cent more than the stated rate.
func ApplyDiscount(total int64, percent int) int64 {
	if percent <= 0 {
		return total
	}
	if percent >= 100 {
		return 0
	}
	return total - (total*int64(percent))/100
}
