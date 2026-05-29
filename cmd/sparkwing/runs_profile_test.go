package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setProfilesFixture(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestRunsList_OnFlagRetired(t *testing.T) {
	err := runJobs([]string{"list", "--on", "prod"})
	if err == nil {
		t.Fatal("expected retired-flag error for --on")
	}
	if !strings.Contains(err.Error(), "--on") || !strings.Contains(err.Error(), "--profile") {
		t.Errorf("message = %q, want --on -> --profile migration pointer", err.Error())
	}
}

func TestRunsList_ProfileNotFound(t *testing.T) {
	setProfilesFixture(t, `
profiles:
  prod: { controller: { url: https://api.example.dev } }
`)
	err := runJobs([]string{"list", "--profile", "ghost"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), `profile "ghost" not found`) {
		t.Errorf("message = %q, want not-found text", err.Error())
	}
}

func TestRunsStatus_OnFlagRetired(t *testing.T) {
	err := runJobs([]string{"status", "--run", "r1", "--on", "prod"})
	if err == nil || !strings.Contains(err.Error(), "--on") {
		t.Fatalf("status: want retired --on pointer, got %v", err)
	}
}

func TestRunsLogs_OnFlagRetired(t *testing.T) {
	err := runJobs([]string{"logs", "--run", "r1", "--on", "prod"})
	if err == nil || !strings.Contains(err.Error(), "--on") {
		t.Fatalf("logs: want retired --on pointer, got %v", err)
	}
}
