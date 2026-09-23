package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func allowRepos(t *testing.T, patterns ...string) context.Context {
	t.Helper()
	allow, err := sourceurl.ParseRepoAllowlist(patterns)
	if err != nil {
		t.Fatal(err)
	}
	return store.WithRepoFilter(context.Background(), allow)
}

func seedRepoTrigger(t *testing.T, st *store.Store, id, repoURL string, at time.Time) {
	t.Helper()
	if err := st.CreateTriggerWithRun(context.Background(), store.Trigger{
		ID: id, Pipeline: "demo", TriggerSource: "manual", RepoURL: repoURL, CreatedAt: at,
	}, store.Run{ID: id, Pipeline: "demo", Status: "pending", RepoURL: repoURL, CreatedAt: at, StartedAt: at}); err != nil {
		t.Fatalf("CreateTriggerWithRun(%s): %v", id, err)
	}
}

// A laptop that allows repository A must never take repository B's run, even
// when B's runs fill the head of the queue past one scan batch: the claim
// passes over them and keeps scanning.
func TestClaimNextTriggerForTakesOnlyAnAllowedRepositoryPastTheQueueHead(t *testing.T) {
	st := storetest.Open(t)
	base := time.Now().Add(-time.Hour)
	for i := range 70 {
		seedRepoTrigger(t, st, fmt.Sprintf("run-b-%02d", i), "https://github.com/acme/b.git", base.Add(time.Duration(i)*time.Millisecond))
	}
	seedRepoTrigger(t, st, "run-none", "", base.Add(80*time.Millisecond))
	seedRepoTrigger(t, st, "run-a", "git@github.com:Acme/A.git", base.Add(90*time.Millisecond))

	got, err := st.ClaimNextTriggerFor(allowRepos(t, "github.com/acme/a"), store.ClaimIdentity{}, time.Minute, nil, nil)
	if err != nil {
		t.Fatalf("filtered claim: %v", err)
	}
	if got.ID != "run-a" {
		t.Fatalf("filtered claim took %s, want run-a", got.ID)
	}
	if _, err := st.ClaimNextTriggerFor(allowRepos(t, "github.com/acme/a"), store.ClaimIdentity{}, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second filtered claim = %v, want an empty queue", err)
	}
	if _, err := st.ClaimNextTriggerFor(allowRepos(t), store.ClaimIdentity{}, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an empty list = %v, want it to claim nothing", err)
	}
	// safety: a claimant that sends no list keeps today's order, so the passed-over runs are still there.
	unfiltered, err := st.ClaimNextTriggerFor(context.Background(), store.ClaimIdentity{}, time.Minute, nil, nil)
	if err != nil || unfiltered.ID != "run-b-00" {
		t.Fatalf("unfiltered claim = %+v, %v; want run-b-00", unfiltered, err)
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
				local.AllowRepos = allow
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
