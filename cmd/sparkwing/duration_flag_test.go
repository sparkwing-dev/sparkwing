package main

import (
	"testing"
	"time"

	flag "github.com/spf13/pflag"
)

func TestLookbackDurationFlagAcceptsDocumentedDaySuffix(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	since := lookbackDuration(fs, "since", 0, "")

	if err := fs.Parse([]string{"--since", "7d"}); err != nil {
		t.Fatalf("parse documented --since 7d: %v", err)
	}
	if *since != 7*24*time.Hour {
		t.Fatalf("--since 7d = %s, want %s", *since, 7*24*time.Hour)
	}
}
