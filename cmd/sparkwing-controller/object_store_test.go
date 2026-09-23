package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/license"
)

// A multi-team controller enforces free allowances only over the object
// store, so it refuses to start without one; a single-team install keeps its
// disk-backed stores.
func TestAMultiTeamControllerRequiresAnObjectStore(t *testing.T) {
	multi, _ := secretsTestServer(t, license.FeatureMultiTeam)
	single, _ := secretsTestServer(t)
	err := checkMultiTeamObjectStore(multi, "")
	if err == nil || !strings.Contains(err.Error(), "--bucket-store") {
		t.Fatalf("multi-team with no object store = %v, want a refusal naming --bucket-store", err)
	}
	if err := checkMultiTeamObjectStore(multi, "s3://bucket/prefix"); err != nil {
		t.Fatalf("multi-team with an object store: %v", err)
	}
	if err := checkMultiTeamObjectStore(single, ""); err != nil {
		t.Fatalf("single-team with no object store: %v", err)
	}
}
