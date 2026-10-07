package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runretry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A manual retry is asked for by run id, so it belongs to the source run's
// team. Filed under the default team it is invisible to the team that asked
// and claimable, repository URL and all, by the default team's runners.
func TestManualRetryStaysInTheSourceRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	now := time.Now()
	if err := acme.CreateRun(ctx, store.Run{
		ID: "run-src", Pipeline: "deploy", Status: "failed", StartedAt: now,
		RepoURL: "https://example.com/acme/private.git",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runretry.Create(ctx, st, acme, "run-src", "run-retry", false, now); err != nil {
		t.Fatalf("runretry.Create: %v", err)
	}
	if got := storedTeam(t, st, `SELECT team FROM triggers WHERE id = ?`, "run-retry"); got != "acme" {
		t.Errorf("retry trigger of an acme run carries team %q, want acme", got)
	}
	if got := storedTeam(t, st, `SELECT team FROM runs WHERE id = ?`, "run-retry"); got != "acme" {
		t.Errorf("retry run of an acme run carries team %q, want acme", got)
	}

	home := mintClaimant(t, st, "agent:default-laptop")
	if trig, err := st.ClaimNextTriggerFor(ctx, home, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a default-team runner claimed %+v (err %v), want not found", trig, err)
	}
	own := mintTeamClaimant(t, acme, "agent:acme")
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-retry", own, time.Minute); err != nil {
		t.Fatalf("acme's runner claiming its own retry: %v", err)
	}
}

func TestCreateRetryWithRunRefusesAMissingSource(t *testing.T) {
	st := storetest.New(t).Open(t)
	now := time.Now()
	err := st.CreateRetryWithRun(context.Background(), "run-gone",
		store.Trigger{ID: "run-retry", Pipeline: "deploy", CreatedAt: now},
		store.Run{ID: "run-retry", Pipeline: "deploy", Status: "pending", StartedAt: now})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateRetryWithRun of a missing source = %v, want not found", err)
	}
}

func TestManualRetryPreservesOnlyItsTeamsGithubRepositoryID(t *testing.T) {
	for _, tc := range []struct {
		name        string
		triggerTeam store.Team
		repoID      int64
		want        int64
	}{
		{"github-app", "acme", 1023983482, 1023983482},
		{"non-app", "acme", 0, 0},
		{"another-team", "other", 1023983482, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := storetest.New(t).Open(t)
			acme := tenantFor(t, st, "acme")
			now := time.Now()
			source := store.Run{
				ID: "source", Pipeline: "pre-push", Status: "failed", StartedAt: now,
				RepoURL: "https://github.com/acme/private", GithubOwner: "acme", GithubRepo: "private", GitSHA: "recorded-sha",
			}
			if err := acme.CreateRun(ctx, source); err != nil {
				t.Fatal(err)
			}
			if err := tenantFor(t, st, tc.triggerTeam).CreateTrigger(ctx, store.Trigger{
				ID: source.ID, Pipeline: source.Pipeline, TriggerSource: "github:push", GithubOwner: source.GithubOwner,
				GithubRepo: source.GithubRepo, GithubRepoID: tc.repoID, GitSHA: source.GitSHA, CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := tenantFor(t, st, "unrelated").GetRun(ctx, source.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("unrelated tenant read source: %v", err)
			}
			if _, err := runretry.Create(ctx, st, acme, source.ID, "retry", false, now); err != nil {
				t.Fatal(err)
			}
			trig, err := acme.GetTrigger(ctx, "retry")
			if err != nil {
				t.Fatal(err)
			}
			repoID, err := st.TriggerRepoID(ctx, "acme", "retry")
			if err != nil {
				t.Fatal(err)
			}
			if repoID != tc.want {
				t.Errorf("retry repository ID = %d, want %d", repoID, tc.want)
			}
			if trig.Team != "acme" || trig.GithubOwner != source.GithubOwner || trig.GithubRepo != source.GithubRepo || trig.GitSHA != source.GitSHA || trig.RetryOf != source.ID {
				t.Fatalf("retry lost source metadata: %+v", trig)
			}
		})
	}
}
