package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A debug pause belongs to its run's team: another team's handle can
// neither open one on that run nor see or release the one that is open.
func TestTenantDebugPausesStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	now := time.Now()
	pause := store.DebugPause{RunID: "run-a", NodeID: "build", Reason: "manual", PausedAt: now, ExpiresAt: now.Add(time.Hour)}

	if err := globex.CreateDebugPause(ctx, pause); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.CreateDebugPause = %v, want ErrNotFound", err)
	}
	if err := acme.CreateDebugPause(ctx, pause); err != nil {
		t.Fatalf("acme.CreateDebugPause: %v", err)
	}
	if _, err := globex.GetActiveDebugPause(ctx, "run-a", "build"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.GetActiveDebugPause = %v, want ErrNotFound", err)
	}
	if pauses, err := globex.ListDebugPauses(ctx, "run-a"); err != nil || len(pauses) != 0 {
		t.Fatalf("globex.ListDebugPauses = %v, %v, want none", pauses, err)
	}
	if err := globex.ReleaseDebugPause(ctx, "run-a", "build", "mallory", store.PauseReleaseManual); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.ReleaseDebugPause = %v, want ErrNotFound", err)
	}
	if p, err := acme.GetActiveDebugPause(ctx, "run-a", "build"); err != nil || p.Reason != "manual" {
		t.Fatalf("acme.GetActiveDebugPause = %+v, %v, want the open pause", p, err)
	}
	if err := acme.ReleaseDebugPause(ctx, "run-a", "build", "alice", store.PauseReleaseManual); err != nil {
		t.Fatalf("acme.ReleaseDebugPause: %v", err)
	}
	if pauses, err := acme.ListDebugPauses(ctx, "run-a"); err != nil || len(pauses) != 1 || pauses[0].ReleasedBy != "alice" {
		t.Fatalf("acme.ListDebugPauses = %+v, %v, want the released pause", pauses, err)
	}
}
