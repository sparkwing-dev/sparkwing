package controller

import (
	"strings"
	"testing"
)

func TestOperatorReasonCountsCodePoints(t *testing.T) {
	for _, tc := range []struct {
		reason string
		ok     bool
	}{
		{strings.Repeat("é", 500), true},
		{strings.Repeat("🚀", 500), true},
		{strings.Repeat("é", 501), false},
		{"  ", false},
	} {
		if _, err := OperatorReason(tc.reason); (err == nil) != tc.ok {
			t.Fatalf("%d runes: %v", len([]rune(tc.reason)), err)
		}
	}
}
