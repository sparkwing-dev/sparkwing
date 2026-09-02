package logs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedLog(t *testing.T, root, runID, nodeID, content string) {
	t.Helper()
	dir := filepath.Join(root, "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nodeID+".log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearch_FindsMatches(t *testing.T) {
	root := t.TempDir()
	seedLog(t, root, "run-1", "node-a", "hello world\nERROR: boom\nall good\n")
	seedLog(t, root, "run-1", "node-b", "nothing to see\nerror lowercase\n")
	seedLog(t, root, "run-2", "node-a", "calm waters\n")

	s, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=error&run_id=run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var body SearchResponse
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, data)
	}
	if body.Total != 2 {
		t.Fatalf("total=%d want 2 (payload=%+v)", body.Total, body)
	}
	if len(body.Results) != 2 {
		t.Fatalf("results=%d want 2", len(body.Results))
	}
}

func TestSearch_RunIDFilter(t *testing.T) {
	root := t.TempDir()
	seedLog(t, root, "run-1", "node-a", "pattern here\n")
	seedLog(t, root, "run-2", "node-a", "pattern here too\n")

	s, _ := New(root, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=pattern&run_id=run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var body SearchResponse
	_ = json.Unmarshal(data, &body)
	if body.Total != 1 {
		t.Fatalf("total=%d want 1 (filtered to run-1)", body.Total)
	}
	if len(body.Results) == 0 || body.Results[0].RunID != "run-1" {
		t.Fatalf("unexpected results: %+v", body.Results)
	}
}

func TestSearch_MissingQueryReturns400(t *testing.T) {
	root := t.TempDir()
	s, _ := New(root, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/logs/search")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}

func TestSearch_EmptyLogsVolume(t *testing.T) {
	root := t.TempDir()
	s, _ := New(root, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=anything&run_id=run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var body SearchResponse
	_ = json.Unmarshal(data, &body)
	if body.Total != 0 {
		t.Fatalf("expected zero results, got %+v", body)
	}
}

func TestSearch_NodeFilterAcceptsHierarchicalID(t *testing.T) {
	root := t.TempDir()
	seedLog(t, root, "run-1", "scan__pkg-a", "pattern here\n")
	seedLog(t, root, "run-1", "node-b", "pattern here too\n")

	s, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=pattern&run_id=run-1&node_id=scan%2Fpkg-a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var body SearchResponse
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, data)
	}
	if body.Total != 1 {
		t.Fatalf("total=%d want 1 (payload=%+v)", body.Total, body)
	}
	if body.Results[0].NodeID != "scan__pkg-a" {
		t.Errorf("node_id=%q want %q", body.Results[0].NodeID, "scan__pkg-a")
	}
}

func TestSearch_MissingRunIDReturns400(t *testing.T) {
	root := t.TempDir()
	seedLog(t, root, "run-1", "node-a", "pattern here\n")
	s, _ := New(root, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=pattern")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "run_id is required") {
		t.Errorf("body=%s want run_id requirement", data)
	}
}

func TestSearch_BudgetsBoundTheScan(t *testing.T) {
	root := t.TempDir()
	var big strings.Builder
	for i := 0; i < 5000; i++ {
		big.WriteString("pattern line filler filler filler\n")
	}
	seedLog(t, root, "run-1", "node-a", big.String())

	cases := []struct {
		name   string
		limits Limits
	}{
		{"byte budget", Limits{SearchMaxBytes: 1024}},
		{"time budget", Limits{SearchTimeout: time.Nanosecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := New(root, nil)
			s.WithLimits(tc.limits)
			srv := httptest.NewServer(s.Handler())
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=pattern&run_id=run-1")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var body SearchResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !body.Truncated {
				t.Fatalf("truncated=false, total=%d; want the budget to stop the scan", body.Total)
			}
			if body.Total >= 5000 {
				t.Errorf("total=%d; want fewer than the 5000 seeded matches", body.Total)
			}
		})
	}
}

func TestSearch_StopsOnCanceledRequest(t *testing.T) {
	root := t.TempDir()
	var big strings.Builder
	for i := 0; i < 20000; i++ {
		big.WriteString("pattern line filler filler filler\n")
	}
	seedLog(t, root, "run-1", "node-a", big.String())

	s, _ := New(root, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/v1/logs/search?q=pattern&run_id=run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var body SearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Truncated {
		t.Fatalf("truncated=false, total=%d; want the canceled request to stop the scan", body.Total)
	}
}
