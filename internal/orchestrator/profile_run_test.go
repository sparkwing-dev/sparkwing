package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInnerProfiles(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestProfileFromEnv_Unset(t *testing.T) {
	os.Unsetenv("SPARKWING_PROFILE")
	p, chain, err := profileFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil || chain != nil {
		t.Fatalf("unset SPARKWING_PROFILE should yield nil profile + chain, got %#v / %#v", p, chain)
	}
}

func TestProfileFromEnv_Resolves(t *testing.T) {
	writeInnerProfiles(t, `
profiles:
  team:
    state: { type: s3, bucket: team, prefix: state }
`)
	t.Setenv("SPARKWING_PROFILE", "team")
	p, chain, err := profileFromEnv()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p == nil || p.Name != "team" || p.State == nil || p.State.Bucket != "team" {
		t.Fatalf("resolved %#v", p)
	}
	if chain == nil || chain.Selected != "team" {
		t.Fatalf("chain should report team selected, got %#v", chain)
	}
}

func TestProfileFromEnv_NotFound(t *testing.T) {
	writeInnerProfiles(t, `
profiles:
  team: { state: { type: sqlite } }
`)
	t.Setenv("SPARKWING_PROFILE", "ghost")
	_, _, err := profileFromEnv()
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the profile: %v", err)
	}
}
