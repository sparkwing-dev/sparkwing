package controller_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
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
	if got := postPlanSnapshot(t, srv.URL, "run-plan", store.MaxPlanBytes+1); got != http.StatusBadRequest {
		t.Errorf("over-cap snapshot status=%d want 400", got)
	}

	run, err := teststore.ForTeam(t, st, store.DefaultTeam).GetRun(context.Background(), "run-plan")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(run.PlanSnapshot) != 1<<10 {
		t.Errorf("stored snapshot is %d bytes, want the 1 KiB one the refused upload did not replace", len(run.PlanSnapshot))
	}
}
