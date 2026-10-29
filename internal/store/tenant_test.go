package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func defaultTenant(t *testing.T, st *store.Store) *store.Tenant {
	t.Helper()
	return tenant(t, st, store.DefaultTeam)
}

func tenantFor(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	ctx := context.Background()
	if err := st.AsOperator().TestOnlyCreateTeam(ctx, team); err != nil {
		t.Fatalf("TestOnlyCreateTeam(%q): %v", team, err)
	}
	tn, err := st.ForTeam(ctx, team)
	if err != nil {
		t.Fatalf("ForTeam(%q): %v", team, err)
	}
	return tn
}

func runTeam(ctx context.Context, t *testing.T, st *store.Store, runID string) *store.Tenant {
	t.Helper()
	team, err := st.AsOperator().RunTeam(ctx, runID)
	if err != nil {
		t.Fatalf("RunTeam(%s): %v", runID, err)
	}
	tn, err := st.ForTeam(ctx, team)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

func seedTenantRun(t *testing.T, tn *store.Tenant, id, pipeline string) {
	t.Helper()
	if err := tn.TestOnlySeedRun(context.Background(), store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: pipeline, Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatalf("CreateRun(%s): %v", id, err)
	}
}

func TestForTeamRefusesTheEmptyTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if _, err := st.ForTeam(ctx, ""); !errors.Is(err, store.ErrNoTeam) {
		t.Fatalf("ForTeam(\"\") = %v, want ErrNoTeam", err)
	}
	if _, err := st.ForTeam(ctx, "   "); !errors.Is(err, store.ErrNoTeam) {
		t.Fatalf("ForTeam(blank) = %v, want ErrNoTeam", err)
	}
}

func TestForTeamRefusesAnUnregisteredTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if _, err := st.ForTeam(ctx, "ghost"); !errors.Is(err, store.ErrUnknownTeam) {
		t.Fatalf("ForTeam(ghost) = %v, want ErrUnknownTeam", err)
	}
	if err := st.AsOperator().TestOnlyCreateTeam(ctx, "ghost"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ForTeam(ctx, "ghost"); err != nil {
		t.Fatalf("ForTeam after registration: %v", err)
	}
}

// Decision 0004 routes on <team>.sparkwing.dev and DNS is
// case-insensitive, so spellings that address one host address one team.
func TestTeamNamesNormalizeToOneTenant(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.AsOperator().TestOnlyCreateTeam(ctx, "  Acme  "); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []store.Team{"acme", "ACME", " acme", "Acme "} {
		tn, err := st.ForTeam(ctx, spelling)
		if err != nil {
			t.Fatalf("ForTeam(%q): %v", spelling, err)
		}
		if tn.Team() != "acme" {
			t.Errorf("ForTeam(%q).Team() = %q, want acme", spelling, tn.Team())
		}
	}
	teams := liveTeams(t, st.AsOperator())
	for _, team := range teams {
		if team != store.DefaultTeam && team != "acme" {
			t.Errorf("EachTeam has %q; the spellings should have registered one team", team)
		}
	}

	seeded, err := st.ForTeam(ctx, " acme ")
	if err != nil {
		t.Fatal(err)
	}
	seedTenantRun(t, seeded, "run-acme", "build")
	other, err := st.ForTeam(ctx, "ACME")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.GetRun(ctx, "run-acme"); err != nil {
		t.Errorf("a differently-spelled handle could not read its own run: %v", err)
	}
}

func TestTenantRunsAreInvisibleToAnotherTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	op := st.AsOperator()
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

	teams := liveTeams(t, op)
	if len(teams) != 3 {
		t.Errorf("EachTeam = %v, want default, alpha and beta", teams)
	}
}

func TestTenantWritesReportNotFoundForAnotherTeamsRun(t *testing.T) {
	st := storetest.New(t).Open(t)
	own, finish := runCompletionFixture(t, st)
	foreign := tenantFor(t, st, "foreign")
	before, err := own.ReadRun(t.Context(), string(finish.Unit.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.ReadRun(t.Context(), string(finish.Unit.RunID)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	if _, err := foreign.FinishRunAndUnit(t.Context(), finish); err == nil {
		t.Fatal("foreign tenant finished run")
	}
	missing := finish
	missing.Unit.RunID = "missing"
	missing.Transition.RunID = "missing"
	missing.Transition.Actor.Owner = missing.Unit
	if _, err := own.FinishRunAndUnit(t.Context(), missing); err == nil {
		t.Fatal("missing run finish succeeded")
	}
	after, err := own.ReadRun(t.Context(), string(finish.Unit.RunID))
	if err != nil || before.State != after.State || after.FinishedAt != nil || before.StateSeq != after.StateSeq {
		t.Fatalf("refused mutation changed run: %+v, %v", after, err)
	}
	if _, err := own.FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
}

// A run seeded for the default team reads back through the default team's handle.
func TestUnportedStoreWritesLandInTheDefaultTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{RunSummary: runstate.RunSummary{ID: "run-legacy", Pipeline: "build", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	def, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
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

func TestEachTeamSkipsDeletionAndContinuesAfterTenantFailure(t *testing.T) {
	st := storetest.Open(t)
	for _, team := range []store.Team{"aaa", "deleted", "zzz"} {
		tenantFor(t, st, team)
	}
	if _, _, err := st.AsOperator().RequestTeamDeletion(t.Context(), "deleted"); err != nil {
		t.Fatal(err)
	}
	var visited []store.Team
	err := st.AsOperator().EachTeam(t.Context(), []store.Team{"aaa", "deleted", "zzz"}, func(tn *store.Tenant) error {
		visited = append(visited, tn.Team())
		if tn.Team() == "aaa" {
			return errors.New("broken tenant")
		}
		return nil
	})
	if err != nil || len(visited) != 2 || visited[0] != "aaa" || visited[1] != "zzz" {
		t.Fatalf("visited %v, error %v", visited, err)
	}
}

func liveTeams(t *testing.T, op *store.Operator) []store.Team {
	t.Helper()
	var teams []store.Team
	if err := op.EachTeam(t.Context(), nil, func(tenant *store.Tenant) error {
		teams = append(teams, tenant.Team())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return teams
}
