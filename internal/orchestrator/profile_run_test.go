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
	writeInnerProfiles(t, "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Chdir(t.TempDir()) // no sparkwing.yaml hint in an empty cwd
	p, chain, err := profileFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil || chain == nil {
		t.Fatal("unset SPARKWING_PROFILE should still resolve the built-in laptop fallback")
	}
	if p.Name != "laptop" || string(chain.Source) != "builtin" {
		t.Fatalf("want built-in laptop, got name=%q source=%q", p.Name, chain.Source)
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
