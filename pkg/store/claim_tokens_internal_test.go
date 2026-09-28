package store

import (
	"context"
	"path/filepath"
	"testing"
)

// A token outlives no run, so only this reaches the missing-row branch; it
// must read as cancelled rather than as a run with no cancel request.
func TestClaimRunCancelled_AMissingRunReadsAsCancelled(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cancelled, err := claimRunCancelled(context.Background(), s.queryRow, DefaultTeam, "no-such-run", "")
	if err != nil || !cancelled {
		t.Fatalf("cancelled = %v, err = %v; want a missing run to refuse", cancelled, err)
	}
}
