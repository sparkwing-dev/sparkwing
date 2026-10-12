package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunsGrepReadsThroughTheControllerWithLineNumbers(t *testing.T) {
	var runQuery string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runs":
			runQuery = r.URL.RawQuery
			w.Header().Set("X-Sparkwing-Run-Filter-Version", store.RunFilterVersion)
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []*store.Run{{
				ID: "run-match", Pipeline: "build", Status: "failed", GitBranch: "release", GitSHA: "abc123",
			}}})
		case "/api/v1/runs/run-match/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []*store.Node{{RunID: "run-match", NodeID: "build"}}})
		case "/api/v1/runs/run-match/logs/build":
			if q := r.URL.Query(); q.Get("grep") != "fatal" || q.Get("line_numbers") != "1" {
				http.Error(w, "unnumbered grep", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"line_no": 3, "line": "fatal error"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	setProfilesFixture(t, fmt.Sprintf("profiles:\n  prod:\n    controller: {url: %q}\n    logs: {type: controller}\n",
		controller.URL))
	var grepErr error
	output := captureStdout(t, func() {
		grepErr = runJobs([]string{"logs", "--profile", "prod", "--grep", "fatal", "--status", "failed", "--branch", "release", "--sha", "abc", "-o", "json"})
	})
	if grepErr != nil {
		t.Fatal(grepErr)
	}
	if !strings.Contains(runQuery, "status=failed") || !strings.Contains(runQuery, "git_branch=release") || !strings.Contains(runQuery, "git_sha=abc") {
		t.Fatalf("run query = %q, want status, branch, and SHA filters", runQuery)
	}
	var match struct {
		RunID  string `json:"run_id"`
		LineNo int    `json:"line_no"`
		Line   string `json:"line"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &match); err != nil {
		t.Fatalf("grep output %q: %v", output, err)
	}
	if match.RunID != "run-match" || match.LineNo != 3 || match.Line != "fatal error" {
		t.Fatalf("grep match = %+v, want source line 3", match)
	}
}

func TestRunsGrepControllerOnlyProfileReadsThroughTheController(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []*store.Run{{ID: "run-only", Status: "failed"}}})
		case "/api/v1/runs/run-only/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []*store.Node{{RunID: "run-only", NodeID: "build"}}})
		case "/api/v1/runs/run-only/logs/build":
			_ = json.NewEncoder(w).Encode(map[string]any{"line_no": 2, "line": "fatal error"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	setProfilesFixture(t, fmt.Sprintf("profiles:\n  prod: {controller: {url: %q}}\n", controller.URL))
	var grepErr error
	output := captureStdout(t, func() {
		grepErr = runJobs([]string{"logs", "--profile", "prod", "--grep", "fatal", "-q", "-o", "plain"})
	})
	if grepErr != nil || strings.TrimSpace(output) != "run-only" {
		t.Fatalf("grep through a controller-only profile = (%q, %v)", output, grepErr)
	}
}
