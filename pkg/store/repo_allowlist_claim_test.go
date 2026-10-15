package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
