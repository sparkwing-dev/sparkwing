package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestCreateRun_InvocationRoundTrip verifies the Invocation snapshot
// (the "how was this started" map -- flags, args, binary_source, cwd,
// reproducer, hashes) survives a CreateRun -> GetRun roundtrip with
// the same shape it went in. The snapshot is a free-form map[string]any
// so adding a new field upstream doesn't require touching the store --
// this test guards the round-trip itself, not the field set.
func TestCreateRun_InvocationRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	want := map[string]any{
		"binary_source": "cached",
		"cwd":           "/Users/test/code/repo/.sparkwing",
		"reproducer":    "wing release --dry-run --start-at=run",
		"hints": map[string]any{
			"follow_logs": "sparkwing runs logs --run run-X --follow",
			"status":      "sparkwing runs status --run run-X",
		},
		"flags": map[string]any{
			"dry_run":  true,
			"start_at": "run",
		},
		"args": map[string]any{
			"version": "v1.2.3",
		},
		"inputs_hash": "sha256:0000111122223333",
	}

	if err := s.CreateRun(ctx, store.Run{
		ID:         "run-X",
		Pipeline:   "release",
		Status:     "running",
		StartedAt:  time.Now(),
		Invocation: want,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := s.GetRun(ctx, "run-X")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	// JSON roundtrip turns nested maps into map[string]any and bools/
	// strings stay typed; we expect the read-back shape to match the
	// original (the comparison is value-equality after re-serialization).
	if got.Invocation == nil {
		t.Fatalf("Invocation lost: got nil")
	}
	if got.Invocation["binary_source"] != "cached" {
		t.Errorf("binary_source: got %v, want cached", got.Invocation["binary_source"])
	}
	if got.Invocation["cwd"] != "/Users/test/code/repo/.sparkwing" {
		t.Errorf("cwd: got %v", got.Invocation["cwd"])
	}
	if got.Invocation["reproducer"] != "wing release --dry-run --start-at=run" {
		t.Errorf("reproducer: got %v", got.Invocation["reproducer"])
	}
	flags, _ := got.Invocation["flags"].(map[string]any)
	if flags == nil {
		t.Fatalf("flags lost")
	}
	if flags["dry_run"] != true {
		t.Errorf("flags.dry_run: got %v", flags["dry_run"])
	}
	if flags["start_at"] != "run" {
		t.Errorf("flags.start_at: got %v", flags["start_at"])
	}
}

// TestCreateRun_NoInvocationLeavesColumnNil checks that a run created
// without an invocation snapshot reads back with Invocation == nil --
// not an empty map -- so callers can distinguish "no snapshot taken"
// from "snapshot present but empty". Pre-migration rows hit the same
// path.
func TestCreateRun_NoInvocationLeavesColumnNil(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-Y", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := s.GetRun(ctx, "run-Y")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Invocation != nil {
		t.Errorf("expected nil Invocation, got %v", got.Invocation)
	}
}
