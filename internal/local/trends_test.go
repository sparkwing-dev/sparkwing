package local_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	controller "github.com/sparkwing-dev/sparkwing/internal/local"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func httpGet(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func TestTrends_BucketsRuns(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()

	// Seed 3 successful runs in the same hour, 1 failure in the
	// previous hour.
	for i := range 3 {
		id := "r-succ-" + string(rune('a'+i))
		start := now.Add(-10 * time.Minute).Add(time.Duration(i) * time.Minute)
		if err := st.CreateRun(ctx, store.Run{
			ID:        id,
			Pipeline:  "demo",
			Status:    "running",
			StartedAt: start,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishRun(ctx, id, "success", ""); err != nil {
			t.Fatal(err)
		}
	}

	{
		id := "r-fail-1"
		if err := st.CreateRun(ctx, store.Run{
			ID:        id,
			Pipeline:  "demo",
			Status:    "running",
			StartedAt: now.Add(-2 * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishRun(ctx, id, "failed", "nope"); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := httpGet(srv.URL + "/api/v1/trends?hours=24")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Points []controller.TrendPoint `json:"points"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp)
	}
	if len(body.Points) < 2 {
		t.Fatalf("expected at least 2 buckets, got %d: %+v", len(body.Points), body.Points)
	}
	var totalPassed, totalFailed int
	for _, p := range body.Points {
		totalPassed += p.Passed
		totalFailed += p.Failed
	}
	if totalPassed != 3 {
		t.Errorf("passed=%d want 3", totalPassed)
	}
	if totalFailed != 1 {
		t.Errorf("failed=%d want 1", totalFailed)
	}
}

func TestTrends_PipelineFilter(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()
	for i, p := range []string{"alpha", "beta", "alpha"} {
		id := "r-" + p + "-" + string(rune('a'+i))
		if err := st.CreateRun(ctx, store.Run{
			ID:        id,
			Pipeline:  p,
			Status:    "running",
			StartedAt: now.Add(-5 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishRun(ctx, id, "success", ""); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := httpGet(srv.URL + "/api/v1/trends?hours=24&pipeline=alpha")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Points   []controller.TrendPoint `json:"points"`
		Pipeline string                  `json:"pipeline"`
	}
	_ = json.Unmarshal(resp, &body)
	if body.Pipeline != "alpha" {
		t.Errorf("pipeline echo=%q want alpha", body.Pipeline)
	}
	total := 0
	for _, p := range body.Points {
		total += p.Total
	}
	if total != 2 {
		t.Errorf("alpha total=%d want 2 (filtered payload=%+v)", total, body)
	}
}
