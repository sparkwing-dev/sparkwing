//go:build qatrust

package main

import (
	"strings"
	"testing"
)

func TestQABuildRefusesToUpdateFromProductionReleases(t *testing.T) {
	if _, err := updateBinary("", false, false); err == nil || !strings.Contains(err.Error(), "qa-releases.sparkwing.dev") {
		t.Fatalf("updateBinary() = %v, want a QA refusal", err)
	}
}
