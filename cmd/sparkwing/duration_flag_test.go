package main

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestLookbackDurationFlagAcceptsDocumentedSyntax(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{raw: "7d", want: 7 * 24 * time.Hour},
		{raw: "2w", want: 2 * 7 * 24 * time.Hour},
		{raw: "90m", want: 90 * time.Minute},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			inv, err := clisurface.Parse("sparkwing", []string{"runs", "list", "--since", tc.raw})
			if err != nil {
				t.Fatalf("parse --since %s: %v", tc.raw, err)
			}
			since, err := clisurface.ParseLookback(inv.Value("since"))
			if err != nil || since != tc.want {
				t.Fatalf("--since %s = %s, %v; want %s", tc.raw, since, err, tc.want)
			}
		})
	}
}
