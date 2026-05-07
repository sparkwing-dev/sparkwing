package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/profile"
)

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := profile.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(cfg.Profiles) != 0 {
		t.Fatalf("expected 0 profiles, got %d", len(cfg.Profiles))
	}
}

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
	if out.Profiles["local"].Name != "local" {
		t.Fatalf("Name not stamped on load: %+v", out.Profiles["local"])
	}
}

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

func TestResolve_NoProfile(t *testing.T) {
	cfg := &profile.Config{Profiles: map[string]*profile.Profile{}}
	_, err := profile.Resolve(cfg, "")
	if !errors.Is(err, profile.ErrNoProfile) {
		t.Fatalf("want ErrNoProfile, got %v", err)
	}
}

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

// TestResolve_DefaultMissing covers the case where the default points
// at a deleted profile.
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
