package main

import (
	"testing"
	"time"

	flag "github.com/spf13/pflag"
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
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			since := lookbackDuration(fs, "since", 0, "")

			if err := fs.Parse([]string{"--since", tc.raw}); err != nil {
				t.Fatalf("parse --since %s: %v", tc.raw, err)
			}
			if *since != tc.want {
				t.Fatalf("--since %s = %s, want %s", tc.raw, *since, tc.want)
			}
		})
	}
}
