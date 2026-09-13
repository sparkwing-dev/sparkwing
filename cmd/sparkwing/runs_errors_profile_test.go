package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunsErrorsReadsTheProfilesController(t *testing.T) {
	const runID = "run-20260910-090000-0123456789abcdef"
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	profiles := filepath.Join(home, "profiles.yaml")
	body := "profiles:\n  prod:\n    controller:\n      url: " + srv.URL + "\n"
	if err := os.WriteFile(profiles, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_PROFILES", profiles)
	t.Setenv("SPARKWING_HOME", home)

	var err error
	captureStdout(t, func() { err = runJobs([]string{"errors", runID, "--profile", "prod"}) })
	if err != nil {
		t.Fatalf("runs errors --profile: %v", err)
	}
	if want := "/api/v1/runs/" + runID + "/nodes"; asked != want {
		t.Fatalf("controller path = %q, want %q", asked, want)
	}
}
