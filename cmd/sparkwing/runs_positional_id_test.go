package main

import (
	"os"
	"path/filepath"
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
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_PROFILES", filepath.Join(home, "profiles.yaml"))
	dir := t.TempDir()
	restore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })

	for _, verb := range []string{"status", "errors"} {
		var runErr error
		captureStdout(t, func() { runErr = runJobs([]string{verb, "run-20260910-090000-0123456789abcdef"}) })
		if runErr != nil && strings.Contains(runErr.Error(), "--run is required") {
			t.Errorf("runs %s rejected a bare run id: %v", verb, runErr)
		}
	}
}
