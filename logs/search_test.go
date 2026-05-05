package logs

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

	resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=error")
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
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}

func TestSearch_EmptyLogsVolume(t *testing.T) {
	// Logs service freshly created, no runs dir yet -- should return
	// an empty result set, not a 500.
	root := t.TempDir()
	s, _ := New(root, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/logs/search?q=anything")
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
