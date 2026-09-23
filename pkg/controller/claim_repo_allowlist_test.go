package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedRepoRunNode(t *testing.T, st *store.Store, runID, repoURL string, prefers []string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "demo", TriggerSource: "manual", RepoURL: repoURL, CreatedAt: now,
	}, store.Run{ID: runID, Pipeline: "demo", Status: "running", RepoURL: repoURL, CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatalf("CreateTriggerWithRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending", PrefersLabels: prefers}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, runID, "build"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}

// A runner's list travels with its claim, so the controller hands it only
// work from those repositories and leaves the rest for other runners.
func TestClaimsCarryTheRunnersRepositoryList(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	ctx := context.Background()
	now := time.Now()
	for i, repo := range []string{"https://github.com/acme/b.git", "https://github.com/acme/a.git"} {
		at := now.Add(time.Duration(i) * time.Millisecond)
		if err := st.CreateTriggerWithRun(ctx, store.Trigger{
			ID: []string{"run-b", "run-a"}[i], Pipeline: "demo", TriggerSource: "manual", RepoURL: repo, CreatedAt: at,
		}, store.Run{ID: []string{"run-b", "run-a"}[i], Pipeline: "demo", Status: "pending", CreatedAt: at, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	laptop := client.New(srv.URL, nil).WithAllowRepos([]string{"github.com/acme/a"})
	got, err := laptop.ClaimTrigger(ctx)
	if err != nil || got == nil || got.ID != "run-a" {
		t.Fatalf("laptop trigger claim = %+v, %v; want run-a", got, err)
	}
	if again, err := laptop.ClaimTrigger(ctx); err != nil || again != nil {
		t.Fatalf("second laptop claim = %+v, %v; want an empty queue", again, err)
	}
	if none, err := client.New(srv.URL, nil).WithAllowRepos([]string{}).ClaimTrigger(ctx); err != nil || none != nil {
		t.Fatalf("empty list claim = %+v, %v; want nothing", none, err)
	}

	seedRepoRunNode(t, st, "run-node-b", "https://github.com/acme/b.git", nil)
	if n, err := laptop.ClaimNode(ctx, "runner:laptop:1", nil, time.Minute, nil); err != nil || n != nil {
		t.Fatalf("laptop node claim = %+v, %v; want nothing", n, err)
	}
	if n, err := client.New(srv.URL, nil).ClaimNode(ctx, "runner:cloud:1", nil, time.Minute, nil); err != nil || n == nil {
		t.Fatalf("a runner that sends no list = %+v, %v; want the node", n, err)
	}
}

func TestClaimsRefuseABadRepositoryPattern(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	bad := client.New(srv.URL, nil).WithAllowRepos([]string{"https://github.com/acme/*"})
	if _, err := bad.ClaimTrigger(context.Background()); err == nil {
		t.Fatal("a trigger claim with a bad pattern succeeded")
	}
	resp := postJSON(t, srv.URL+"/api/v1/nodes/claim", map[string]any{
		"holder_id": "runner:x:1", "allow_repos": []string{"*/acme/app"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("node claim with a bad pattern = %d, want 400", resp.StatusCode)
	}
}

// A local runner whose list refuses a node's repository cannot take it, so it
// must not make the cloud wait out the local-first hold.
func TestPlacement_CloudIsNotHeldForALocalRunnerThatRefusesTheRepository(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	ctx := context.Background()
	laptop := client.New(srv.URL, nil).WithAllowRepos([]string{"github.com/acme/a"})
	seedRepoRunNode(t, st, "run-proof", "https://github.com/acme/a.git", nil)
	if n, err := laptop.ClaimNodeWithCapacity(ctx, "runner:laptop:0", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 4}); err != nil || n == nil {
		t.Fatalf("laptop warmup claim = %+v, %v", n, err)
	}
	if _, err := laptop.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 4, ActiveClaims: 1}); err != nil {
		t.Fatalf("laptop poll: %v", err)
	}

	seedRepoRunNode(t, st, "run-b", "https://github.com/acme/b.git", []string{"location=local"})
	cloud := client.New(srv.URL, nil)
	got, err := cloud.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil || got == nil || got.RunID != "run-b" {
		t.Fatalf("cloud claim = %+v, %v; want run-b at once", got, err)
	}

	// safety: the negative control, a node the laptop's list admits, is still held for it.
	seedRepoRunNode(t, st, "run-a2", "https://github.com/acme/a.git", []string{"location=local"})
	if held, err := cloud.ClaimNode(ctx, "runner:cloudpod:2", []string{"location=cloud"}, time.Minute, nil); err != nil || held != nil {
		t.Fatalf("cloud claim of an admitted repository's node = %+v, %v; want it held", held, err)
	}
}
