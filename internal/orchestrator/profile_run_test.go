package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func writeInnerProfiles(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
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
	p, chain, err := resolveActiveProfile(nil)
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
	p, chain, err := resolveActiveProfile(nil)
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
	_, _, err := resolveActiveProfile(nil)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the profile: %v", err)
	}
}

func TestResolveActiveProfile_DeclaredProfilesSelectNothingWithoutTheHandoff(t *testing.T) {
	t.Setenv(ProfileEnv, "")
	writeInnerProfiles(t, `
profiles:
  cloud:
    controller: { url: https://user.example.dev }
`)
	cfg := &projectconfig.Config{
		Profiles: map[string]*profile.Profile{
			"cloud": {Name: "cloud", Controller: &profile.ControllerSpec{URL: "https://project.example.dev"}},
		},
	}
	p, chain, err := resolveActiveProfile(cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p != nil || chain == nil || chain.Source != profile.ChainSourceNone {
		t.Fatalf("resolved %#v (chain %#v), want the local store", p, chain)
	}
}

func TestResolveActiveProfile_RefusesAControllerProfile(t *testing.T) {
	writeInnerProfiles(t, `
profiles:
  prod:
    controller: { url: https://api.example.dev }
  team:
    state: { type: sqlite }
`)
	for _, name := range []string{"prod", "team"} {
		t.Setenv("SPARKWING_PROFILE", name)
		if _, _, err := resolveActiveProfile(nil); err == nil || !strings.Contains(err.Error(), "sparkwing pipeline trigger <pipeline> --profile "+name) {
			t.Errorf("profile %s on a local run = %v, want a refusal naming pipeline trigger", name, err)
		}
	}
}

func TestRemoteStateProfile_AcceptsOnlyS3OrPostgresStateWithoutAController(t *testing.T) {
	for name, tc := range map[string]struct {
		p  *profile.Profile
		ok bool
	}{
		"s3":         {&profile.Profile{Name: "s3", State: &backends.Spec{Type: backends.TypeS3, Bucket: "b"}}, true},
		"postgres":   {&profile.Profile{Name: "pg", State: &backends.Spec{Type: backends.TypePostgres, URL: "postgres://x"}}, true},
		"sqlite":     {&profile.Profile{Name: "lite", State: &backends.Spec{Type: backends.TypeSQLite}}, false},
		"controller": {&profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "https://api.example.dev"}}, false},
	} {
		if err := RemoteStateProfile(tc.p); (err == nil) != tc.ok {
			t.Errorf("%s: RemoteStateProfile = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

func TestRefusePinnedLocalRun(t *testing.T) {
	if err := RefusePinnedLocalRun(nil); err != nil {
		t.Fatalf("an undeclared pipeline was refused: %v", err)
	}
	if err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "build"}); err != nil {
		t.Fatalf("an unpinned pipeline was refused: %v", err)
	}
	err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "release", Profile: "prod"})
	if err == nil || !strings.Contains(err.Error(), "sparkwing pipeline trigger release --profile prod") {
		t.Fatalf("a pinned pipeline ran locally or the refusal named no trigger command: %v", err)
	}
}

// A leftover key the strict decoder rejects must not hide a pin from a cron,
// detached run or local child trigger.
func TestRefusePinnedCheckoutRun_FailsWhenTheConfigCannotBeRead(t *testing.T) {
	checkoutAt(t, "defaults:\n  profile: prod\npipelines:\n  - name: release\n    entrypoint: Release\n    profile: prod\n")
	err := refusePinnedCheckoutRun(nil)
	if err == nil || !strings.Contains(err.Error(), "profile pin") {
		t.Fatalf("refusePinnedCheckoutRun with an unreadable config = %v, want a refusal", err)
	}
	checkoutAt(t, "pipelines:\n  - name: release\n    entrypoint: Release\n")
	if err := refusePinnedCheckoutRun(nil); err != nil {
		t.Fatalf("a readable config with no pin was refused: %v", err)
	}
}

func TestDispatchesLocally_FollowsTheDispatchBackend(t *testing.T) {
	if !LocalBackends(Paths{Root: t.TempDir()}, nil, nil).dispatchesLocally() {
		t.Error("a run on this machine's store is not reported local")
	}
	if RemoteBackends(client.NewWithToken("http://127.0.0.1:1", nil, ""), nil, nil, nil, 0).dispatchesLocally() {
		t.Error("a run a controller dispatched is reported local")
	}
}

func TestNodeProfileResolution_ACoordinatedNodeSeesItsLocalDispatcher(t *testing.T) {
	t.Setenv(ProfileEnv, "shared")
	got := nodeProfileResolution(true)
	if !got.IsLocal || got.Name != "shared" {
		t.Errorf("coordinated node resolution = %+v, want local with the dispatcher's profile", got)
	}
	if got := nodeProfileResolution(false); got.IsLocal || got.Name != "" {
		t.Errorf("controller-dispatched node resolution = %+v, want remote and unnamed", got)
	}
}
