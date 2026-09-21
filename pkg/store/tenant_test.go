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
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam(%q): %v", team, err)
	}
	tn, err := st.ForTeam(ctx, team)
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
	if err := st.AsOperator().CreateTeam(ctx, "ghost"); err != nil {
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
	if err := st.AsOperator().CreateTeam(ctx, "  Acme  "); err != nil {
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
	teams, err := st.AsOperator().ListTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range teams {
		if team != store.DefaultTeam && team != "acme" {
			t.Errorf("ListTeams has %q; the spellings should have registered one team", team)
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

	all, err := op.ListRunsAcrossTeams(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("operator ListRunsAcrossTeams: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("operator ListRunsAcrossTeams = %v, want both runs", runIDs(all))
	}
	if n, err := op.CountRunsAcrossTeams(ctx, store.RunFilter{}); err != nil || n != 2 {
		t.Errorf("operator CountRunsAcrossTeams = %d, %v; want 2", n, err)
	}
	teams, err := op.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 3 {
		t.Errorf("ListTeams = %v, want default, alpha and beta", teams)
	}
}

// A mutator handed another team's id reports the same not-found its read
// side reports, because a cancel endpoint that answered 200 and cancelled
// nothing is the shape this disagreement takes in production.
func TestTenantWritesReportNotFoundForAnotherTeamsRun(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	seedTenantRun(t, alpha, "run-alpha", "build")

	if err := beta.FinishRun(ctx, "run-alpha", "failed", "not yours"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("beta.FinishRun(run-alpha) = %v, want ErrNotFound", err)
	}
	if err := beta.FinishRunsIfActive(ctx, []string{"run-alpha"}, "cancelled", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("beta.FinishRunsIfActive(run-alpha) = %v, want ErrNotFound", err)
	}
	if err := beta.TouchRunHeartbeat(ctx, "run-alpha"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("beta.TouchRunHeartbeat(run-alpha) = %v, want ErrNotFound", err)
	}
	if err := beta.FinishRun(ctx, "run-nowhere", "failed", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("beta.FinishRun(run-nowhere) = %v, want ErrNotFound", err)
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

// CreateRun is an upsert whose conflict target is the caller-supplied run
// id, so a team that names another team's pending run id would reach that
// row unless the conflict guard carries the team too. The victim would
// then execute the writer's plan under its own repo and secrets while the
// writer was told it succeeded.
func TestTenantCreateRunCannotOverwriteAnotherTeamsPendingRun(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")

	if err := alpha.CreateRun(ctx, store.Run{
		ID: "shared-id", Pipeline: "alpha-pipeline", Status: "pending",
		Repo: "alpha-repo", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("alpha.CreateRun: %v", err)
	}

	err := beta.CreateRun(ctx, store.Run{
		ID: "shared-id", Pipeline: "beta-pipeline", Status: "pending",
		Repo: "beta-repo", StartedAt: time.Now(),
	})
	if !errors.Is(err, store.ErrIDOwnedByAnotherTeam) {
		t.Errorf("beta.CreateRun on alpha's pending id = %v, want ErrIDOwnedByAnotherTeam", err)
	}

	got, err := alpha.GetRun(ctx, "shared-id")
	if err != nil {
		t.Fatalf("alpha.GetRun: %v", err)
	}
	if got.Pipeline != "alpha-pipeline" {
		t.Errorf("pipeline = %q after the cross-team insert, want alpha-pipeline", got.Pipeline)
	}
	if got.Repo != "alpha-repo" {
		t.Errorf("repo = %q after the cross-team insert, want alpha-repo", got.Repo)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q after the cross-team insert, want pending", got.Status)
	}

	if _, err := beta.GetRun(ctx, "shared-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("beta.GetRun(shared-id) = %v; beta must not read what it did not create", err)
	}
	if n, err := beta.CountRuns(ctx, store.RunFilter{}); err != nil || n != 0 {
		t.Errorf("beta.CountRuns = %d, %v; want 0", n, err)
	}
}

// Re-creating a team's own pending run is the upsert's reason to exist,
// so the guard must not have closed it.
func TestTenantCreateRunStillUpsertsItsOwnPendingRun(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")

	if err := alpha.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "first", Status: "pending", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("first CreateRun: %v", err)
	}
	if err := alpha.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "second", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("second CreateRun: %v", err)
	}
	got, err := alpha.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Pipeline != "second" || got.Status != "running" {
		t.Errorf("run = %s/%s after its own team re-created it, want second/running", got.Pipeline, got.Status)
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
