package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfilesFixture(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestResolveProfileFlag_NotFound(t *testing.T) {
	writeProfilesFixture(t, `
profiles:
  prod: { controller: https://api.example.dev }
  team: { state: { type: sqlite } }
`)
	_, err := resolveProfileFlag("bogus")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `profile "bogus" not found`) {
		t.Errorf("message should name the missing profile: %q", msg)
	}
	if !strings.Contains(msg, "Available profiles:") || !strings.Contains(msg, "prod") || !strings.Contains(msg, "team") {
		t.Errorf("message should list available profiles: %q", msg)
	}
}

func TestResolveProfileFlag_Success(t *testing.T) {
	writeProfilesFixture(t, `
profiles:
  prod: { controller: https://api.example.dev, token: swu_x }
`)
	p, err := resolveProfileFlag("prod")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p == nil || p.Name != "prod" || p.Controller != "https://api.example.dev" {
		t.Fatalf("resolved %#v", p)
	}
}

func TestDispatchRun_RejectsRetiredSwProfile(t *testing.T) {
	// The retired --sw-profile flag must error with a migration pointer
	// before any discovery/compile rather than falling through to the
	// pipeline binary.
	err := dispatchRun([]string{"some-pipeline", "--profile", "x", "--sw-profile", "y"})
	if err == nil {
		t.Fatal("expected retired-flag error for --sw-profile")
	}
	if !strings.Contains(err.Error(), "--sw-profile") {
		t.Errorf("message = %q, want --sw-profile migration pointer", err.Error())
	}
}

func TestDispatchRun_ProfileNotFoundBeforeCompile(t *testing.T) {
	writeProfilesFixture(t, `
profiles:
  prod: { controller: https://api.example.dev }
`)
	// A bad --profile must fail fast (before findSparkwingDir / compile),
	// so this resolves and errors even outside any .sparkwing/ project.
	err := dispatchRun([]string{"some-pipeline", "--profile", "ghost"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), `profile "ghost" not found`) {
		t.Errorf("message = %q, want not-found text", err.Error())
	}
	// Sanity: not a mutual-exclusion error and not exit-2.
	if errors.Is(err, errHelpRequested) {
		t.Error("unexpected help-requested error")
	}
}
