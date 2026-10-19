//go:build qatrust

package main

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestNextIndexNeverMovesLatestBackwards(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	index, err := nextIndex(engine.VersionIndex{Format: engine.VersionIndexFormat}, "v0.68.0-qa.3", now)
	if err != nil {
		t.Fatal(err)
	}
	if index, err = nextIndex(index, "v0.68.0-qa.2", now); err != nil {
		t.Fatal(err)
	}
	if index.Latest != "v0.68.0-qa.3" || len(index.Versions) != 2 {
		t.Fatalf("latest=%s versions=%d, want v0.68.0-qa.3 and 2", index.Latest, len(index.Versions))
	}
	if _, err := nextIndex(index, "v0.68.0-qa.2", now); err == nil {
		t.Fatal("republished an existing version")
	}
}
