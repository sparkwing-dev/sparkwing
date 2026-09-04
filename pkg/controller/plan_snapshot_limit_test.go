package controller_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func postPlanSnapshot(t *testing.T, url, runID string, size int) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		url+"/api/v1/runs/"+runID+"/plan", strings.NewReader(strings.Repeat("x", size)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST plan: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestUpdatePlanSnapshot_BodyLimit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seedRunNode(t, st, "run-plan", "build")

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	if got := postPlanSnapshot(t, srv.URL, "run-plan", 1<<10); got != http.StatusNoContent {
		t.Fatalf("small snapshot status=%d want 204", got)
	}
	if got := postPlanSnapshot(t, srv.URL, "run-plan", store.MaxNodeDispatchEnvelope+1); got != http.StatusBadRequest {
		t.Errorf("over-cap snapshot status=%d want 400", got)
	}

	run, err := st.GetRun(context.Background(), "run-plan")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(run.PlanSnapshot) != 1<<10 {
		t.Errorf("stored snapshot is %d bytes, want the 1 KiB one the refused upload did not replace", len(run.PlanSnapshot))
	}
}

func TestLoopbackUpdatePlanSnapshot_BodyLimit(t *testing.T) {
	backend := s3state.New(newMemArt())
	t.Cleanup(func() { _ = backend.Close() })
	c, srv := newLoopbackClient(t, s3Adapter{Backend: backend}, contractRunID, nil, nil)

	ctx := context.Background()
	if err := c.CreateRun(ctx, store.Run{
		ID: contractRunID, Pipeline: "contract", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/runs/"+contractRunID+"/plan",
		strings.NewReader(strings.Repeat("x", store.MaxNodeDispatchEnvelope+1)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+loopbackToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST plan: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("over-cap snapshot status=%d want 400", resp.StatusCode)
	}
}
