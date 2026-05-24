package orchestrator

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestListJobs_ReadsFromProfileBackend confirms ListOpts.Profile routes
// the read through OpenReadBackendForProfile (the profile's resolved
// backend) rather than the legacy cwd backends.yaml flow.
func TestListJobs_ReadsFromProfileBackend(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "profile-state.db")

	// Seed the profile's state store with a run, then close it so
	// ListJobs opens its own handle.
	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.CreateRun(ctx, store.Run{ID: "run-xyz", Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatalf("seed CreateRun: %v", err)
	}
	_ = seed.Close()

	p := &profile.Profile{Name: "local", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}}
	var buf bytes.Buffer
	// Paths.Root points elsewhere (no state.db there); the run must come
	// from the profile's backend, proving the routing.
	err = ListJobs(ctx, Paths{Root: t.TempDir()}, ListOpts{Profile: p, Quiet: true}, &buf)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(buf.String(), "run-xyz") {
		t.Fatalf("profile-backed list missing the run; output:\n%s", buf.String())
	}
}
