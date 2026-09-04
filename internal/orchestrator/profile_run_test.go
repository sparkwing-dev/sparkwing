package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func writeInnerProfiles(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestFleetProfileRejectsRemoteAuthorityButLocalOnlyCanOverride(t *testing.T) {
	local := &profile.Profile{
		Secrets: &backends.Spec{Type: backends.TypeEnv},
		State:   &backends.Spec{Type: backends.TypeSQLite},
		Cache:   &backends.Spec{Type: backends.TypeFilesystem, Path: "/tmp/cache"},
		Logs:    &backends.Spec{Type: backends.TypeFilesystem, Path: "/tmp/logs"},
	}
	if fleetProfileUsesRemoteAuthority(local) {
		t.Fatal("local profile classified as remote authority")
	}
	for name, remote := range map[string]*profile.Profile{
		"controller": {Controller: &profile.ControllerSpec{URL: "https://controller.example.com"}},
		"postgres":   {State: &backends.Spec{Type: backends.TypePostgres, URL: "postgres://example"}},
		"s3":         {Cache: &backends.Spec{Type: backends.TypeS3, Bucket: "shared"}},
	} {
		if !fleetProfileUsesRemoteAuthority(remote) {
			t.Errorf("%s profile was not classified as remote authority", name)
		}
	}
}

func TestResolveActiveProfile_NoneSelected(t *testing.T) {
	os.Unsetenv("SPARKWING_PROFILE")
	p, chain, err := resolveActiveProfile(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil profile, got %#v", p)
	}
	if chain == nil || string(chain.Source) != "none" {
		t.Fatalf("want chain source=none, got %#v", chain)
	}
}

func TestResolveActiveProfile_UserProfileViaEnv(t *testing.T) {
	writeInnerProfiles(t, `
profiles:
  team:
    state: { type: s3, bucket: team, prefix: state }
`)
	t.Setenv("SPARKWING_PROFILE", "team")
	p, chain, err := resolveActiveProfile(nil, nil)
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

func TestResolveActiveProfile_UserProfileNotFound(t *testing.T) {
	writeInnerProfiles(t, `
profiles:
  team: { state: { type: sqlite } }
`)
	t.Setenv("SPARKWING_PROFILE", "ghost")
	_, _, err := resolveActiveProfile(nil, nil)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the profile: %v", err)
	}
}
