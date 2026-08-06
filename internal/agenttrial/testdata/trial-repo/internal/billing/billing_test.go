package billing

import (
	"errors"
	"testing"
)

func TestTotal(t *testing.T) {
	cases := map[string]struct {
		lines []Line
		want  int64
		err   error
	}{
		"empty":            {nil, 0, nil},
		"single":           {[]Line{{Cents: 500, Quantity: 1}}, 500, nil},
		"quantity":         {[]Line{{Cents: 250, Quantity: 4}}, 1000, nil},
		"zero quantity":    {[]Line{{Cents: 250, Quantity: 0}}, 250, nil},
		"negative rejects": {[]Line{{Cents: -1}}, 0, ErrNegative},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Total(tc.lines)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if got != tc.want {
				t.Errorf("Total = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestApplyDiscount(t *testing.T) {
	cases := []struct {
		total   int64
		percent int
		want    int64
	}{
		{1000, 0, 1000},
		{1000, 10, 900},
		{1000, 100, 0},
		{1000, -5, 1000},
		{999, 10, 900},
	}
	for _, tc := range cases {
		if got := ApplyDiscount(tc.total, tc.percent); got != tc.want {
			t.Errorf("ApplyDiscount(%d, %d) = %d, want %d", tc.total, tc.percent, got, tc.want)
		}
	}
}
