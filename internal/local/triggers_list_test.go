package local_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	controller "github.com/sparkwing-dev/sparkwing/internal/local"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestListTriggers_RoundTrip verifies that a trigger posted via
// POST /api/v1/triggers is surfaced by the new GET /api/v1/triggers
// list endpoint, and that status + pipeline filters narrow results
// correctly.
func TestListTriggers_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	// Fire two triggers on different pipelines.
	for _, pipeline := range []string{"alpha", "beta"} {
		resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
			"pipeline": pipeline,
			"trigger":  map[string]string{"source": "test"},
			"git":      map[string]string{"branch": "main"},
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST /triggers pipeline=%s status=%d want 202",
				pipeline, resp.StatusCode)
		}
	}

	// No filter: both triggers, status=pending.
	all, err := c.ListTriggers(context.Background(), store.TriggerFilter{})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListTriggers len=%d want 2", len(all))
	}
	for _, tr := range all {
		if tr.Status != "pending" {
			t.Errorf("trigger %s status=%q want pending", tr.ID, tr.Status)
		}
	}

	// Pipeline filter narrows to one.
	only, err := c.ListTriggers(context.Background(), store.TriggerFilter{
		Pipelines: []string{"alpha"},
	})
	if err != nil {
		t.Fatalf("ListTriggers alpha: %v", err)
	}
	if len(only) != 1 || only[0].Pipeline != "alpha" {
		t.Fatalf("pipeline filter got %+v want one alpha", only)
	}

	// Claim one trigger so status=claimed is populated.
	if _, err := st.ClaimNextTrigger(context.Background(), 0); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	claimed, err := c.ListTriggers(context.Background(), store.TriggerFilter{
		Statuses: []string{"claimed"},
	})
	if err != nil {
		t.Fatalf("ListTriggers claimed: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed filter len=%d want 1", len(claimed))
	}

	pending, err := c.ListTriggers(context.Background(), store.TriggerFilter{
		Statuses: []string{"pending"},
	})
	if err != nil {
		t.Fatalf("ListTriggers pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending filter len=%d want 1", len(pending))
	}
}

// TestListTriggers_RepoFilter verifies the GITHUB_REPOSITORY env
// filter matches triggers whose trigger_env carries the requested
// repo.
func TestListTriggers_RepoFilter(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	mk := func(pipeline, repo string) {
		resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
			"pipeline": pipeline,
			"trigger": map[string]any{
				"source": "test",
				"env": map[string]string{
					"GITHUB_REPOSITORY": repo,
				},
			},
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST /triggers pipeline=%s status=%d want 202",
				pipeline, resp.StatusCode)
		}
	}
	mk("p1", "owner/one")
	mk("p2", "owner/two")
	mk("p3", "owner/one")

	got, err := c.ListTriggers(context.Background(), store.TriggerFilter{
		Repo: "owner/one",
	})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("repo filter got=%d want 2", len(got))
	}
	for _, tr := range got {
		if tr.TriggerEnv["GITHUB_REPOSITORY"] != "owner/one" {
			t.Errorf("trigger %s GITHUB_REPOSITORY=%q", tr.ID, tr.TriggerEnv["GITHUB_REPOSITORY"])
		}
	}
}
