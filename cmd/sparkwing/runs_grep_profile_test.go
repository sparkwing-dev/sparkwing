package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunsGrepUsesExplicitLogsURLAndOriginalLineNumber(t *testing.T) {
	logServer, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	logsHTTP := httptest.NewServer(logServer.Handler())
	t.Cleanup(logsHTTP.Close)
	if err := logs.NewClient(logsHTTP.URL, nil).Append(context.Background(), "run-match", "build", []byte("start\nunrelated\nfatal error\n")); err != nil {
		t.Fatal(err)
	}
	var runQuery string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services":
			http.NotFound(w, r)
		case "/api/v1/runs":
			runQuery = r.URL.RawQuery
			w.Header().Set("X-Sparkwing-Run-Filter-Version", store.RunFilterVersion)
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []*store.Run{{
				ID: "run-match", Pipeline: "build", Status: "failed", GitBranch: "release", GitSHA: "abc123",
			}}})
		case "/api/v1/runs/run-match/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []*store.Node{{RunID: "run-match", NodeID: "build"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	home := t.TempDir()
	profiles := filepath.Join(home, "profiles.yaml")
	body := fmt.Sprintf("profiles:\n  prod:\n    controller: {url: %q}\n    logs: {type: controller, url: %q}\n", controller.URL, logsHTTP.URL)
	if err := os.WriteFile(profiles, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_PROFILES", profiles)
	t.Setenv("SPARKWING_HOME", home)
	var grepErr error
	output := captureStdout(t, func() {
		grepErr = runJobs([]string{"grep", "--profile", "prod", "--pattern", "fatal", "--status", "failed", "--branch", "release", "--sha", "abc", "-o", "json"})
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

func TestRunsGrepControllerOnlyProfileNeedsLogsAnnouncement(t *testing.T) {
	controller := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(controller.Close)
	setProfilesFixture(t, fmt.Sprintf("profiles:\n  prod: {controller: {url: %q}}\n", controller.URL))
	err := runJobs([]string{"grep", "--profile", "prod", "--pattern", "failure"})
	if err == nil || !strings.Contains(err.Error(), "logs service") {
		t.Fatalf("missing logs announcement error = %v", err)
	}
}

func TestRunsGrepDiscoversLogsWhenProfileURLWasInherited(t *testing.T) {
	logsHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/logs/run-inherited/build" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"line_no": 2, "line": "fatal error"})
	}))
	t.Cleanup(logsHTTP.Close)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services":
			_ = json.NewEncoder(w).Encode(map[string]string{"logs": logsHTTP.URL})
		case "/api/v1/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []*store.Run{{ID: "run-inherited", Status: "failed"}}})
		case "/api/v1/runs/run-inherited/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []*store.Node{{RunID: "run-inherited", NodeID: "build"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	setProfilesFixture(t, fmt.Sprintf("profiles:\n  prod:\n    controller: {url: %q}\n    logs: {type: controller}\n", controller.URL))
	var grepErr error
	output := captureStdout(t, func() {
		grepErr = runJobs([]string{"grep", "--profile", "prod", "--pattern", "fatal", "-q", "-o", "plain"})
	})
	if grepErr != nil || strings.TrimSpace(output) != "run-inherited" {
		t.Fatalf("grep inherited logs URL = (%q, %v)", output, grepErr)
	}
}
