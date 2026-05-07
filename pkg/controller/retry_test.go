package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

func TestRetry_CreatesNewTriggerWithSameInputs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	src := store.Run{
		ID:        "src-run",
		Pipeline:  "deploy",
		Args:      map[string]string{"env": "prod", "tag": "v1"},
		Status:    "failed",
		GitBranch: "main",
		GitSHA:    "abc123",
		StartedAt: time.Now().Add(-5 * time.Minute),
	}
	if err := st.CreateRun(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, src.ID, "failed", "exit 1"); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/runs/"+src.ID+"/retry", "application/json", bytes.NewBufferString(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	newID, _ := body["id"].(string)
	if newID == "" || newID == src.ID {
		t.Fatalf("expected a fresh run id, got %q", newID)
	}
	if body["pipeline"] != src.Pipeline {
		t.Errorf("pipeline=%v want %s", body["pipeline"], src.Pipeline)
	}
	if body["retry_of"] != src.ID {
		t.Errorf("retry_of=%v want %s", body["retry_of"], src.ID)
	}

	// Confirm the trigger row landed with matching args / git.
	trig, err := st.GetTrigger(ctx, newID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.Pipeline != src.Pipeline {
		t.Errorf("trigger pipeline=%s want %s", trig.Pipeline, src.Pipeline)
	}
	if trig.Args["env"] != "prod" || trig.Args["tag"] != "v1" {
		t.Errorf("trigger args didn't copy: %+v", trig.Args)
	}
	if trig.TriggerSource != "retry" {
		t.Errorf("trigger source=%s want retry", trig.TriggerSource)
	}
	if trig.GitSHA != src.GitSHA {
		t.Errorf("git_sha=%s want %s", trig.GitSHA, src.GitSHA)
	}
}

func TestRetry_UnknownRunReturns404(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "s.db"))
	defer st.Close()
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/runs/missing/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}
