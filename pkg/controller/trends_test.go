package controller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func TestTrends_AvgWaitMs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()

	// One run with a clear 500ms wait (created 500ms before started),
	// one with a 1500ms wait. Average should be 1000ms.
	start := now.Add(-5 * time.Minute)
	for i, waitMs := range []int{500, 1500} {
		id := "r-wait-" + string(rune('a'+i))
		if err := st.CreateRun(ctx, store.Run{
			ID:        id,
			Pipeline:  "demo",
			Status:    "running",
			CreatedAt: start.Add(-time.Duration(waitMs) * time.Millisecond),
			StartedAt: start,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishRun(ctx, id, "success", ""); err != nil {
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
	var got int64
	for _, p := range body.Points {
		if p.AvgWaitMs > got {
			got = p.AvgWaitMs
		}
	}
	if got != 1000 {
		t.Errorf("avg_wait_ms=%d want 1000 across %d buckets: %+v", got, len(body.Points), body.Points)
	}
}

func TestTrends_AvgWaitMs_ExcludesLegacyRows(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()
	start := now.Add(-5 * time.Minute)

	// One run with a real wait; one "legacy" row with created_at == 0
	// (simulated by writing the column directly after insert).
	if err := st.CreateRun(ctx, store.Run{
		ID:        "r-real",
		Pipeline:  "demo",
		Status:    "running",
		CreatedAt: start.Add(-2 * time.Second),
		StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "r-real", "success", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID:        "r-legacy",
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "r-legacy", "success", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET created_at = 0 WHERE id = ?`, "r-legacy"); err != nil {
		t.Fatal(err)
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
	_ = json.Unmarshal(resp, &body)
	var got int64
	for _, p := range body.Points {
		if p.AvgWaitMs > got {
			got = p.AvgWaitMs
		}
	}
	// Legacy row excluded; real-row wait (~2000ms) is the average.
	if got < 1900 || got > 2100 {
		t.Errorf("avg_wait_ms=%d want ~2000 (legacy row should have been excluded): %+v", got, body.Points)
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
