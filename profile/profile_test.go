package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/profile"
)

// TestLoad_MissingFile returns an empty Config without error so
// callers can distinguish "no profiles yet" from "malformed yaml".
func TestLoad_MissingFile(t *testing.T) {
	cfg, err := profile.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(cfg.Profiles) != 0 {
		t.Fatalf("expected 0 profiles, got %d", len(cfg.Profiles))
	}
}

// TestLoadSaveRoundTrip: save a config, reload it, check shape.
func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	in := &profile.Config{
		Default: "local",
		Profiles: map[string]*profile.Profile{
			"local": {Controller: "http://127.0.0.1:4344"},
			"prod": {
				Controller:    "https://api.example.dev",
				Token:         "swu_test",
				Gitcache:      "https://gitcache.example.com",
				LogStore:      "s3://your-team-sparkwing-store/logs",
				ArtifactStore: "s3://your-team-sparkwing-store/cache",
			},
		},
	}
	if err := profile.Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Default != "local" {
		t.Fatalf("default: %q", out.Default)
	}
	if out.Profiles["local"].Controller != "http://127.0.0.1:4344" {
		t.Fatalf("local profile roundtrip: %+v", out.Profiles["local"])
	}
	if out.Profiles["prod"].Token != "swu_test" {
		t.Fatalf("prod token roundtrip: %+v", out.Profiles["prod"])
	}
	if out.Profiles["prod"].LogStore != "s3://your-team-sparkwing-store/logs" ||
		out.Profiles["prod"].ArtifactStore != "s3://your-team-sparkwing-store/cache" {
		t.Fatalf("storage URL roundtrip: %+v", out.Profiles["prod"])
	}
	// .Name is populated on load so Resolve can return a fully-formed Profile.
	if out.Profiles["local"].Name != "local" {
		t.Fatalf("Name not stamped on load: %+v", out.Profiles["local"])
	}
}

// TestSave_0600Mode: saved file is 0600 so accidental shared-dir
// writes surface as permission errors instead of silent credential
// exposure.
func TestSave_0600Mode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := profile.Save(path, &profile.Config{
		Profiles: map[string]*profile.Profile{
			"local": {Controller: "http://x"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode: got %o, want 0600", mode)
	}
}

// TestResolve_ExplicitWins: --on wins even when a default is set.
func TestResolve_ExplicitWins(t *testing.T) {
	cfg := &profile.Config{
		Default: "local",
		Profiles: map[string]*profile.Profile{
			"local": {Name: "local", Controller: "http://127.0.0.1:4344"},
			"prod":  {Name: "prod", Controller: "https://api.example.dev"},
		},
	}
	p, err := profile.Resolve(cfg, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "prod" {
		t.Fatalf("resolved %q, want prod", p.Name)
	}
}

// TestResolve_DefaultFallback: no --on, default fires.
func TestResolve_DefaultFallback(t *testing.T) {
	cfg := &profile.Config{
		Default: "local",
		Profiles: map[string]*profile.Profile{
			"local": {Name: "local", Controller: "http://127.0.0.1:4344"},
		},
	}
	p, err := profile.Resolve(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "local" {
		t.Fatalf("default: got %q, want local", p.Name)
	}
}

// TestResolve_NoProfile: nothing configured, no --on -> ErrNoProfile.
func TestResolve_NoProfile(t *testing.T) {
	cfg := &profile.Config{Profiles: map[string]*profile.Profile{}}
	_, err := profile.Resolve(cfg, "")
	if !errors.Is(err, profile.ErrNoProfile) {
		t.Fatalf("want ErrNoProfile, got %v", err)
	}
}

// TestResolve_ProfileNotFound: --on names a missing profile.
func TestResolve_ProfileNotFound(t *testing.T) {
	cfg := &profile.Config{
		Profiles: map[string]*profile.Profile{
			"local": {Name: "local"},
		},
	}
	_, err := profile.Resolve(cfg, "staging")
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Fatalf("error should name the requested profile: %v", err)
	}
}

// TestResolve_DefaultMissing: the default points at a profile that
// doesn't exist (user deleted it without clearing the default).
// Surface as ErrProfileNotFound rather than silently ignoring.
func TestResolve_DefaultMissing(t *testing.T) {
	cfg := &profile.Config{
		Default:  "gone",
		Profiles: map[string]*profile.Profile{},
	}
	_, err := profile.Resolve(cfg, "")
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
}

// TestNames_Sorted ensures list output is deterministic so operators
// get the same result every invocation.
func TestNames_Sorted(t *testing.T) {
	cfg := &profile.Config{
		Profiles: map[string]*profile.Profile{
			"zulu":  {},
			"alpha": {},
			"mike":  {},
		},
	}
	names := cfg.Names()
	if got := strings.Join(names, ","); got != "alpha,mike,zulu" {
		t.Fatalf("got %q, want alpha,mike,zulu", got)
	}
}

// TestDefaultPath_RespectsEnv: env overrides fire first.
func TestDefaultPath_RespectsEnv(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", "/explicit/path.yaml")
	p, err := profile.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/explicit/path.yaml" {
		t.Fatalf("got %q", p)
	}
}

// TestDefaultPath_XDG: XDG_CONFIG_HOME takes second precedence.
func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	p, err := profile.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/xdg/sparkwing/profiles.yaml" {
		t.Fatalf("got %q", p)
	}
}
