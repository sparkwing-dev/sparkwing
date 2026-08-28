package billing

import "errors"

var ErrNegative = errors.New("billing: negative amount")

type Line struct {
	Description string
	Cents       int64
	Quantity    int
}

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

func ApplyDiscount(total int64, percent int) int64 {
	if percent <= 0 {
		return total
	}
	if percent >= 100 {
		return 0
	}
	return total - (total*int64(percent))/100
}
