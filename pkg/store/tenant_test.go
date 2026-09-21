package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func tenantFor(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	tn, err := st.ForTeam(team)
	if err != nil {
		t.Fatalf("ForTeam(%q): %v", team, err)
	}
	return tn
}

func seedTenantRun(t *testing.T, tn *store.Tenant, id, pipeline string) {
	t.Helper()
	if err := tn.CreateRun(context.Background(), store.Run{
		ID:        id,
		Pipeline:  pipeline,
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun(%s): %v", id, err)
	}
}

func TestForTeamRefusesTheEmptyTeam(t *testing.T) {
	st := storetest.New(t).Open(t)
	if _, err := st.ForTeam(""); !errors.Is(err, store.ErrNoTeam) {
		t.Fatalf("ForTeam(\"\") = %v, want ErrNoTeam", err)
	}
	if _, err := st.ForTeam("   "); !errors.Is(err, store.ErrNoTeam) {
		t.Fatalf("ForTeam(blank) = %v, want ErrNoTeam", err)
	}
}

func TestTenantRunsAreInvisibleToAnotherTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	op := st.AsOperator()
	for _, team := range []store.Team{"alpha", "beta"} {
		if err := op.CreateTeam(ctx, team); err != nil {
			t.Fatalf("CreateTeam(%s): %v", team, err)
		}
	}
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")

	seedTenantRun(t, alpha, "run-alpha", "build")
	seedTenantRun(t, beta, "run-beta", "build")

	if _, err := alpha.GetRun(ctx, "run-beta"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alpha.GetRun(run-beta) = %v, want ErrNotFound", err)
	}
	if _, err := beta.GetRun(ctx, "run-alpha"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("beta.GetRun(run-alpha) = %v, want ErrNotFound", err)
	}
	if got, err := alpha.GetRun(ctx, "run-alpha"); err != nil || got.ID != "run-alpha" {
		t.Errorf("alpha.GetRun(run-alpha) = %v, %v", got, err)
	}

	runs, err := alpha.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("alpha.ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-alpha" {
		t.Errorf("alpha.ListRuns = %v, want only run-alpha", runIDs(runs))
	}
	if n, err := alpha.CountRuns(ctx, store.RunFilter{}); err != nil || n != 1 {
		t.Errorf("alpha.CountRuns = %d, %v; want 1", n, err)
	}

	all, err := op.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("operator ListRuns: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("operator ListRuns = %v, want both runs", runIDs(all))
	}
	teams, err := op.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 3 {
		t.Errorf("ListTeams = %v, want default, alpha and beta", teams)
	}
}

func TestTenantWritesCannotReachAnotherTeamsRun(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	seedTenantRun(t, alpha, "run-alpha", "build")

	if err := beta.FinishRun(ctx, "run-alpha", "failed", "not yours"); err != nil {
		t.Fatalf("beta.FinishRun on a foreign run should be a no-op, got: %v", err)
	}
	if err := beta.FinishRunsIfActive(ctx, []string{"run-alpha"}, "cancelled", ""); err != nil {
		t.Fatalf("beta.FinishRunsIfActive on a foreign run should be a no-op, got: %v", err)
	}
	if err := beta.TouchRunHeartbeat(ctx, "run-alpha"); err != nil {
		t.Fatalf("beta.TouchRunHeartbeat on a foreign run should be a no-op, got: %v", err)
	}

	got, err := alpha.GetRun(ctx, "run-alpha")
	if err != nil {
		t.Fatalf("alpha.GetRun: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("status = %q after another team tried to finish it, want running", got.Status)
	}
	if got.FinishedAt != nil {
		t.Errorf("finished_at = %v after another team tried to finish it, want nil", got.FinishedAt)
	}

	if err := alpha.FinishRun(ctx, "run-alpha", "success", ""); err != nil {
		t.Fatalf("alpha.FinishRun: %v", err)
	}
	got, err = alpha.GetRun(ctx, "run-alpha")
	if err != nil {
		t.Fatalf("alpha.GetRun after finish: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("status = %q after its own team finished it, want success", got.Status)
	}
}

// The un-ported *Store surface writes the default team, so a caller
// that has not moved yet and the default team's handle see one store.
func TestUnportedStoreWritesLandInTheDefaultTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-legacy", Pipeline: "build", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	def := tenantFor(t, st, store.DefaultTeam)
	got, err := def.GetRun(ctx, "run-legacy")
	if err != nil {
		t.Fatalf("default team GetRun: %v", err)
	}
	if got.ID != "run-legacy" {
		t.Errorf("GetRun = %s, want run-legacy", got.ID)
	}
	runs, err := def.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("default team ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("default team ListRuns = %v, want the one legacy run", runIDs(runs))
	}
}

func runIDs(runs []*store.Run) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.ID
	}
	return out
}
