package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunIDFromArgs(t *testing.T) {
	cases := []struct {
		name    string
		rest    []string
		flag    string
		want    string
		wantErr string
	}{
		{name: "flag only", rest: nil, flag: "run-a", want: "run-a"},
		{name: "positional only", rest: []string{"run-a"}, want: "run-a"},
		{name: "neither", rest: nil, wantErr: "a run id is required"},
		{name: "both", rest: []string{"run-a"}, flag: "run-b", wantErr: "given twice"},
		{name: "two positionals", rest: []string{"run-a", "run-b"}, wantErr: "unexpected positional"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runIDFromArgs("sparkwing runs status", tc.rest, tc.flag)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("id = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunsReadVerbsAcceptABareRunID(t *testing.T) {
	const runID = "run-20260910-090000-0123456789abcdef"
	for _, verb := range []string{"status", "errors"} {
		t.Run(verb, func(t *testing.T) {
			var asked []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = append(asked, r.URL.Path)
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

			// safety: status exits non-zero on a run with no terminal state, so the
			// proof is the id reaching the controller, not the verb's exit.
			var runErr error
			captureStdout(t, func() { runErr = runJobs([]string{verb, runID, "--profile", "prod"}) })
			if !slices.ContainsFunc(asked, func(path string) bool { return strings.Contains(path, runID) }) {
				t.Fatalf("runs %s read %v, none of which names the run id it was given (err: %v)", verb, asked, runErr)
			}
		})
	}
}
