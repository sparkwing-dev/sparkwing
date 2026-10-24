package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func seedRepoTrigger(t *testing.T, st *store.Store, id, repoURL string, at time.Time) {
	t.Helper()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedTriggerWithRun(context.Background(), store.Trigger{
		ID: id, Pipeline: "demo", TriggerSource: "manual", RepoURL: repoURL, CreatedAt: at,
	}, store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "demo", Status: "pending", RepoURL: repoURL, CreatedAt: at, StartedAt: at}}); err != nil {
		t.Fatalf("CreateTriggerWithRun(%s): %v", id, err)
	}
}

func seedRepoNode(t *testing.T, st *store.Store, runID, repoURL string) {
	t.Helper()
	ctx := context.Background()
	if repoURL == "" {
		if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{RunSummary: runstate.RunSummary{ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now()}}); err != nil {
			t.Fatal(err)
		}
	} else {
		seedRepoTrigger(t, st, runID, repoURL, time.Now())
	}
	if err := teststore.SeedNode(st, ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNodeReady(st, ctx, runID, "build"); err != nil {
		t.Fatal(err)
	}
}
