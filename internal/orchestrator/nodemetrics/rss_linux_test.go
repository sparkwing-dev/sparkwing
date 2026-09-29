//go:build linux

package nodemetrics

import (
	"fmt"
	"math"
	"os"
	"testing"
)

func TestProcessStatmRange(t *testing.T) {
	page := int64(os.Getpagesize())
	for _, tc := range []struct {
		data string
		want int64
		ok   bool
	}{
		{"9 2 1", 2 * page, true},
		{"9 0 1", 0, true},
		{"9 -1 1", 0, false},
		{"9 broken", 0, false},
		{"9", 0, false},
		{fmt.Sprintf("9 %d", math.MaxInt64/page), math.MaxInt64 / page * page, true},
		{fmt.Sprintf("9 %d", math.MaxInt64/page+1), 0, false},
	} {
		got, ok := parseProcessStatm(tc.data)
		if got != tc.want || ok != tc.ok {
			t.Errorf("statm %q = %d,%t; want %d,%t", tc.data, got, ok, tc.want, tc.ok)
		}
	}
}
