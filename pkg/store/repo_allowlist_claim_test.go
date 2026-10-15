package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func allowRepos(t *testing.T, patterns ...string) context.Context {
	t.Helper()
	allow, err := sourceurl.ParseRepoAllowlist(patterns)
	if err != nil {
		t.Fatal(err)
	}
	return store.WithClaimProfile(context.Background(), match.Profile{Accept: allow})
}

func seedRepoTrigger(t *testing.T, st *store.Store, id, repoURL string, at time.Time) {
	t.Helper()
	if err := st.CreateTriggerWithRun(context.Background(), store.Trigger{
		ID: id, Pipeline: "demo", TriggerSource: "manual", RepoURL: repoURL, CreatedAt: at,
	}, store.Run{ID: id, Pipeline: "demo", Status: "pending", RepoURL: repoURL, CreatedAt: at, StartedAt: at}); err != nil {
		t.Fatalf("CreateTriggerWithRun(%s): %v", id, err)
	}
}

func seedRepoNode(t *testing.T, st *store.Store, runID, repoURL string) {
	t.Helper()
	ctx := context.Background()
	if repoURL == "" {
		if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	} else {
		seedRepoTrigger(t, st, runID, repoURL, time.Now())
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, runID, "build"); err != nil {
		t.Fatal(err)
	}
}

func TestClaimNextReadyNodeTakesOnlyNodesOfAllowedRepositories(t *testing.T) {
	st := storetest.Open(t)
	seedRepoNode(t, st, "run-b", "https://github.com/acme/b.git")
	seedRepoNode(t, st, "run-a", "https://github.com/acme/a.git")
	seedRepoNode(t, st, "run-local", "")

	ctx := allowRepos(t, "github.com/acme/a")
	first, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "laptop:1", time.Minute, nil)
	if err != nil || first.RunID != "run-a" {
		t.Fatalf("filtered claim = %+v, %v; want run-a", first, err)
	}
	// safety: a run that names no repository is fetched from nowhere, so the list has nothing to refuse.
	second, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "laptop:2", time.Minute, nil)
	if err != nil || second.RunID != "run-local" {
		t.Fatalf("second filtered claim = %+v, %v; want run-local", second, err)
	}
	if _, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "laptop:3", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("third filtered claim = %v, want run-b left for another runner", err)
	}
	other, err := st.ClaimNextReadyNode(context.Background(), store.ClaimIdentity{}, "cloud:1", time.Minute, nil)
	if err != nil || other.RunID != "run-b" {
		t.Fatalf("unfiltered claim = %+v, %v; want run-b", other, err)
	}
}

// A live local runner holds a node back from the cloud only if it could take
// it; one whose list refuses the node's repository would leave the cloud
// waiting out the hold for nothing.
func TestPlacementHoldIgnoresALocalRunnerWhoseListRefusesTheRepository(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []string
		held     bool
	}{
		{"list refuses the repository", []string{"github.com/acme/a"}, false},
		{"list admits the repository", []string{"github.com/acme/*"}, true},
		{"runner sent no list", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			seedRepoNode(t, st, "run-b", "https://github.com/acme/b.git")
			local := store.RunnerPresence{Name: "laptop", Labels: []string{"location=local"}, FreeSlots: 1}
			if tc.patterns != nil {
				allow, err := sourceurl.ParseRepoAllowlist(tc.patterns)
				if err != nil {
					t.Fatal(err)
				}
				local.Profile.Accept = allow
			}
			ctx := store.WithClaimPlacement(context.Background(), store.ClaimPlacement{
				DefaultPrefers: []string{"location=local"}, Hold: time.Hour, Live: []store.RunnerPresence{local},
			})
			_, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "cloud:1", time.Minute, nil)
			if tc.held && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("cloud claim = %v, want the node held for the local runner", err)
			}
			if !tc.held && err != nil {
				t.Fatalf("cloud claim = %v, want the node, since the local runner cannot take it", err)
			}
		})
	}
}
