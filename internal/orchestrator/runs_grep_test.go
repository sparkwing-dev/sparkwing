package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunGrepRemoteFindsOlderRunMatchingSourceBeforeLimit(t *testing.T) {
	older := &store.Run{ID: "older-match", Pipeline: "build", Status: "failed", GitBranch: "rare", GitSHA: "deadbeef1234"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runs":
			w.Header().Set("X-Sparkwing-Run-Filter-Version", store.RunFilterVersion)
			if r.URL.Query().Get("status") == "failed" &&
				r.URL.Query().Get("git_branch") == "rare" && strings.EqualFold(r.URL.Query().Get("git_sha"), "deadbee") {
				_ = json.NewEncoder(w).Encode(map[string]any{"runs": []*store.Run{older}})
				return
			}
			newer := make([]*store.Run, 1000)
			for i := range newer {
				newer[i] = &store.Run{ID: fmt.Sprintf("newer-%04d", i), Pipeline: "build", Status: "failed", GitBranch: "main", GitSHA: "cafebabe1234"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": newer})
		case "/api/v1/runs/older-match/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []*store.Node{{RunID: older.ID, NodeID: "build"}}})
		case "/api/v1/logs/older-match/build":
			_, _ = w.Write([]byte("needle in older log\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	for _, prefix := range []string{"deadbee", "DEADBEE"} {
		t.Run(prefix, func(t *testing.T) {
			var output bytes.Buffer
			err := RunGrepRemote(context.Background(), srv.URL, srv.URL, "", GrepOpts{
				Pattern: "needle", Limit: 1, Quiet: true, Statuses: []string{"failed"},
				Filter: CompiledFilter{Branches: []string{"rare"}, SHAPrefixes: []string{prefix}},
			}, &output)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(output.String()); got != older.ID {
				t.Fatalf("matching older run = %q, want %q", got, older.ID)
			}
		})
	}
}

func TestRunGrepRemoteUsesAnnouncedLogsService(t *testing.T) {
	logs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/logs/archived/build" || r.Header.Get("Authorization") != "Bearer test-token" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("needle in archived log\n"))
	}))
	t.Cleanup(logs.Close)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services":
			_ = json.NewEncoder(w).Encode(map[string]string{"logs": logs.URL})
		case "/api/v1/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []*store.Run{{ID: "archived", Pipeline: "build", Status: "failed"}}})
		case "/api/v1/runs/archived/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []*store.Node{{RunID: "archived", NodeID: "build"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	var output bytes.Buffer
	err := RunGrepRemote(context.Background(), controller.URL, controller.URL, "test-token",
		GrepOpts{Pattern: "needle", Quiet: true}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(output.String()); got != "archived" {
		t.Fatalf("matching archived run = %q, want archived", got)
	}
}

func writeLogFile(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGrepNodeFile_MatchesSubstring(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "n.log")
	writeLogFile(t, tmp, []string{
		"starting",
		"ERROR: permission denied for /etc/x",
		"retrying",
		"ERROR: permission denied for /etc/y",
		"giving up",
	})
	got, err := grepNodeFile(tmp, "permission denied", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2", len(got))
	}
	if got[0].lineNo != 2 || got[1].lineNo != 4 {
		t.Errorf("line numbers off: %+v", got)
	}
}

func TestGrepNodeFile_MaxMatchesCap(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "n.log")
	writeLogFile(t, tmp, []string{"hit", "hit", "hit", "hit", "miss"})
	got, err := grepNodeFile(tmp, "hit", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("max-matches cap broken: %+v", got)
	}
}

func TestGrepNodeFile_NonexistentFileIsNoop(t *testing.T) {
	got, err := grepNodeFile("/no/such/path", "x", 0)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestEmitGrepMatches_QuietDedupAndSort(t *testing.T) {
	matches := []GrepMatch{
		{RunID: "run-b", NodeID: "n1", LineNo: 1, Line: "hit"},
		{RunID: "run-a", NodeID: "n1", LineNo: 2, Line: "hit"},
		{RunID: "run-a", NodeID: "n2", LineNo: 3, Line: "hit"},
	}
	var buf bytes.Buffer
	if err := emitGrepMatches(matches, GrepOpts{Quiet: true}, &buf); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(buf.String())
	if got != "run-a\nrun-b" {
		t.Errorf("quiet output = %q, want run-a\\nrun-b", got)
	}
}

func TestEmitGrepMatches_JSONShape(t *testing.T) {
	matches := []GrepMatch{
		{RunID: "run-a", NodeID: "build", LineNo: 7, Line: "ERROR: x"},
	}
	var buf bytes.Buffer
	if err := emitGrepMatches(matches, GrepOpts{JSON: true}, &buf); err != nil {
		t.Fatal(err)
	}
	got := decodeNDJSON[GrepMatch](t, buf.String())
	if len(got) != 1 || got[0].RunID != "run-a" || got[0].LineNo != 7 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestEmitGrepMatches_TableHasHeader(t *testing.T) {
	matches := []GrepMatch{
		{RunID: "run-a", NodeID: "build", LineNo: 7, Line: "ERROR: x"},
	}
	var buf bytes.Buffer
	if err := emitGrepMatches(matches, GrepOpts{}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"RUN", "NODE", "LINE", "TEXT", "run-a", "ERROR: x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestEmitGrepMatches_NoMatchesPrintsHint(t *testing.T) {
	var buf bytes.Buffer
	if err := emitGrepMatches(nil, GrepOpts{}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no matches") {
		t.Errorf("expected 'no matches' hint: %q", buf.String())
	}
}

func TestEmitGrepMatches_QuietJSONEmptyStream(t *testing.T) {
	var buf bytes.Buffer
	if err := emitGrepMatches(nil, GrepOpts{Quiet: true, JSON: true}, &buf); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); out != "" {
		t.Errorf("expected an empty stream; got %q", out)
	}
}

func TestResolveRunLimit_DefaultsAndCap(t *testing.T) {
	if resolveRunLimit(GrepOpts{}) != grepDefaultRunLimit {
		t.Errorf("default limit wrong")
	}
	if got := resolveRunLimit(GrepOpts{Limit: 5000}); got != grepMaxRunLimit {
		t.Errorf("max cap not honored, got %d", got)
	}
	if got := resolveRunLimit(GrepOpts{Limit: 10}); got != 10 {
		t.Errorf("user limit wrong, got %d", got)
	}
}
